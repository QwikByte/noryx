// Package pki implements the certificate authority of the master and the mutual
// TLS configuration that secures every connection between master and agents.
//
// Identities are synthetic DNS names (see MasterName and NodeName) rather than
// network addresses, so nodes can change their IP without re-enrolling.
package pki

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"time"
)

// MasterName is the identity carried by every master certificate.
const MasterName = "master.noryx.internal"

const (
	caValidity = 10 * 365 * 24 * time.Hour
	// Short-lived leaves limit the damage of a leaked key; they are renewed automatically.
	leafValidity = 90 * 24 * time.Hour
)

// NodeName is the identity carried by the certificate of the node with the given ID.
func NodeName(nodeID string) string { return nodeID + ".node.noryx.internal" }

// CA signs the certificates of the master and of all enrolled nodes.
type CA struct {
	Cert *x509.Certificate
	key  crypto.Signer
}

// LoadOrCreateCA loads the CA stored in dir, creating it on first start.
func LoadOrCreateCA(dir string) (*CA, error) {
	pair, err := LoadKeyPair(dir, "ca")
	if errors.Is(err, fs.ErrNotExist) {
		return createCA(dir)
	}
	if err != nil {
		return nil, fmt.Errorf("load CA: %w", err)
	}
	return &CA{Cert: pair.Leaf, key: pair.PrivateKey.(crypto.Signer)}, nil
}

func createCA(dir string) (*CA, error) {
	key := NewKey()
	tmpl := &x509.Certificate{
		Subject:               pkix.Name{CommonName: "Noryx CA"},
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(caValidity),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return nil, err
	}
	if err := SaveKeyPair(dir, "ca", der, key); err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	return &CA{Cert: cert, key: key}, err
}

// Issue signs a certificate for pub with the given identity and key usages.
func (ca *CA) Issue(pub crypto.PublicKey, name string, usage ...x509.ExtKeyUsage) ([]byte, error) {
	tmpl := &x509.Certificate{
		Subject:     pkix.Name{CommonName: name},
		DNSNames:    []string{name},
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: usage,
		NotBefore:   time.Now().Add(-time.Minute),
		NotAfter:    time.Now().Add(leafValidity),
	}
	return x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, pub, ca.key)
}

// SignNodeCSR issues the certificate of an enrolling node. Only the public key is
// taken from the CSR; the identity is always derived from the node ID.
func (ca *CA) SignNodeCSR(csrDER []byte, nodeID string) ([]byte, error) {
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return nil, err
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, err
	}
	return ca.Issue(csr.PublicKey, NodeName(nodeID), x509.ExtKeyUsageServerAuth)
}

// MasterCertificate issues a fresh certificate for the running master. It serves
// the enrollment endpoint and authenticates the master towards agents.
func (ca *CA) MasterCertificate() (tls.Certificate, error) {
	key := NewKey()
	der, err := ca.Issue(key.Public(), MasterName, x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der, ca.Cert.Raw}, PrivateKey: key}, nil
}

// Fingerprint returns the hex encoded SHA-256 hash of a certificate.
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

// NewKey generates a new Ed25519 private key.
func NewKey() ed25519.PrivateKey {
	_, key, _ := ed25519.GenerateKey(rand.Reader) // never fails with crypto/rand
	return key
}

// NewCSR creates a certificate signing request for key.
func NewCSR(key crypto.Signer) ([]byte, error) {
	return x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
}
