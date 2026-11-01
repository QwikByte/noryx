// Package https serves the panel over HTTPS, which browsers need to keep the session cookie
// anywhere but on localhost: with a certificate from Let's Encrypt for the panel's domain, or
// with a self-signed one, which also works for IP addresses but makes browsers warn.
package https

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"

	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/pki"
)

// The certificates the panel can serve. Files is the one given on the command line.
const (
	SelfSigned  = "self-signed"
	LetsEncrypt = "letsencrypt"
	Files       = "files"
)

// retryAfter is how long the panel serves the self-signed certificate after Let's Encrypt
// failed, rather than asking again on every visit: it allows only 5 failures an hour.
const retryAfter = 30 * time.Minute

// Certificate describes the certificate the panel serves.
type Certificate struct {
	SelfSigned bool `json:"selfSigned"`
	// Names are the domain names and IP addresses it is valid for.
	Names     []string  `json:"names"`
	ExpiresAt time.Time `json:"expiresAt"`
	// Fingerprint is its SHA-256 fingerprint, to compare with the one browsers show.
	Fingerprint string `json:"fingerprint"`
	// Error tells why Let's Encrypt issued no certificate, so that the self-signed one is served.
	Error string `json:"error,omitempty"`
}

// Server provides the certificates of the panel.
type Server struct {
	domain   string
	fallback *tls.Certificate
	acme     *autocert.Manager // nil without Let's Encrypt
	issue    func(*tls.ClientHelloInfo) (*tls.Certificate, error)

	mu       sync.Mutex
	issued   *x509.Certificate // the latest certificate of Let's Encrypt
	err      error
	failedAt time.Time
}

// New prepares the certificates of the panel in dir. The self-signed one covers domain, names
// and the names and IP addresses of this machine. With Let's Encrypt, the panel serves it for
// other names than domain, e.g. an IP address, and while Let's Encrypt issued no certificate.
func New(mode, domain string, names []string, dir string) (*Server, error) {
	fallback, err := selfSigned(dir, append(append(localNames(), names...), domain))
	if err != nil {
		return nil, err
	}
	s := &Server{domain: domain, fallback: fallback}
	if mode == LetsEncrypt {
		s.acme = &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			Cache:      autocert.DirCache(filepath.Join(dir, "letsencrypt")),
			HostPolicy: autocert.HostWhitelist(domain),
		}
		s.issue = s.acme.GetCertificate
	}
	return s, nil
}

// TLSConfig is the TLS configuration of the panel.
func (s *Server) TLSConfig() *tls.Config {
	c := &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1"}, GetCertificate: s.certificate}
	if s.acme != nil {
		c.NextProtos = append(c.NextProtos, acme.ALPNProto) // the check of Let's Encrypt at port 443
	}
	return c
}

// Trusted tells whether browsers trust the certificate, so that the panel may tell them to
// use HTTPS only. They ignore that over connections with a certificate they don't trust.
func (s *Server) Trusted() bool { return s.acme != nil }

func (s *Server) certificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if s.acme == nil || !strings.EqualFold(strings.TrimSuffix(hello.ServerName, "."), s.domain) {
		return s.fallback, nil
	}
	if slices.Contains(hello.SupportedProtos, acme.ALPNProto) {
		return s.acme.GetCertificate(hello)
	}
	s.mu.Lock()
	wait := s.issued == nil && time.Since(s.failedAt) < retryAfter
	s.mu.Unlock()
	if wait {
		return s.fallback, nil
	}
	if cert, err := s.obtain(hello); err == nil {
		return cert, nil
	}
	return s.fallback, nil
}

// obtain gets the certificate of Let's Encrypt, which issues one if there is none or it is due
// for renewal, and records the outcome.
func (s *Server) obtain(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	cert, err := s.issue(hello)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.err, s.failedAt = err, time.Now()
		return nil, err
	}
	s.issued, s.err = cert.Leaf, nil
	return cert, nil
}

// Run answers the checks of Let's Encrypt at port 80 of the panel's IP address, sends other
// requests there to HTTPS at the domain, and gets the certificate right away rather than on the
// first visit, trying again until it has one. Let's Encrypt renews it then. Without it, Run does nothing.
func (s *Server) Run(ctx context.Context, panelAddr string) {
	if s.acme == nil {
		return
	}
	host, port, _ := net.SplitHostPort(panelAddr)
	if ln, err := net.Listen("tcp", net.JoinHostPort(host, "80")); err != nil {
		slog.Warn("Port 80 isn't free, so Let's Encrypt can only check the domain at port 443 and HTTP isn't sent to HTTPS",
			logging.Settings, "err", err)
	} else {
		origin := "https://" + s.domain
		if port != "443" {
			origin = "https://" + net.JoinHostPort(s.domain, port)
		}
		// It only answers challenges and redirects, so requests are short and idle clients go.
		srv := &http.Server{Handler: s.acme.HTTPHandler(redirect(origin)), ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second,
			IdleTimeout: time.Minute, ErrorLog: logging.ServerErrors(slog.Default())}
		go func() { _ = srv.Serve(ln) }()
		defer srv.Close()
	}
	// Browsers support ECDSA, so the certificate fetched here is the one they get.
	hello := &tls.ClientHelloInfo{ServerName: s.domain, CipherSuites: []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256}}
	for {
		_, err := s.obtain(hello)
		if err == nil {
			break
		}
		slog.Warn("Let's Encrypt issued no certificate for the panel, which serves its self-signed one meanwhile",
			logging.Settings, "domain", s.domain, "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(retryAfter):
		}
	}
	<-ctx.Done()
}

// redirect sends requests to the same path at origin. The certificate is only valid for the
// domain, not for IP addresses, so the target is always the domain.
func redirect(origin string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		//nolint:gosec // only the path comes from the request, the target stays at the panel's origin
		http.Redirect(w, r, origin+r.URL.RequestURI(), http.StatusMovedPermanently)
	})
}

// Certificate describes the certificate the panel serves for its domain.
func (s *Server) Certificate() Certificate {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.issued != nil {
		return describe(s.issued)
	}
	c := describe(s.fallback.Leaf)
	c.SelfSigned = true
	if s.err != nil {
		c.Error = s.err.Error()
	}
	return c
}

func describe(cert *x509.Certificate) Certificate {
	names := slices.Clone(cert.DNSNames)
	for _, ip := range cert.IPAddresses {
		names = append(names, ip.String())
	}
	return Certificate{Names: names, ExpiresAt: cert.NotAfter, Fingerprint: pki.Fingerprint(cert)}
}
