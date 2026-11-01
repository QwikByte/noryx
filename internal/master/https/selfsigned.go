package https

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"log/slog"
	"math/big"
	"net"
	"os"
	"slices"
	"time"

	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/pki"
)

// Browsers of Apple refuse certificates valid for longer, even ones the user trusts.
const selfSignedValidity = 825 * 24 * time.Hour

// selfSigned loads the self-signed certificate from dir, or creates one if it lacks one of names
// or expires within 30 days. One with names that are no longer needed stays, so that browsers
// don't warn again when an IP address of the machine goes away.
func selfSigned(dir string, names []string) (*tls.Certificate, error) {
	names = slices.DeleteFunc(slices.Compact(slices.Sorted(slices.Values(names))), func(n string) bool { return n == "" })
	if cert, err := pki.LoadKeyPair(dir, "self-signed"); err == nil && time.Until(cert.Leaf.NotAfter) > 30*24*time.Hour &&
		!slices.ContainsFunc(names, func(n string) bool { return cert.Leaf.VerifyHostname(n) != nil }) {
		return &cert, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader) // browsers don't take Ed25519
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "Noryx"},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(selfSignedValidity),
	}
	for _, n := range names {
		if ip := net.ParseIP(n); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, n)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return nil, err
	}
	if err := pki.SaveKeyPair(dir, "self-signed", der, key); err != nil {
		return nil, err
	}
	cert, err := pki.LoadKeyPair(dir, "self-signed")
	return &cert, err
}

// localNames are the names and IP addresses of this machine.
func localNames() []string {
	names := []string{"localhost"}
	if host, err := os.Hostname(); err == nil {
		names = append(names, host)
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		slog.Warn("The self-signed certificate leaves out the IP addresses of this machine", logging.Settings, "err", err)
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLinkLocalUnicast() {
			names = append(names, n.IP.String())
		}
	}
	return names
}
