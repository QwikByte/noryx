package pki

import (
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"testing"
	"time"
)

func TestLoadOrCreateCAIsStable(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadOrCreateCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreateCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	if Fingerprint(first.Cert) != Fingerprint(second.Cert) {
		t.Fatal("CA changed between starts")
	}
}

func TestMutualTLS(t *testing.T) {
	ca := newCA(t)
	master := holder(t, must(ca.MasterCertificate()))
	node1, node2 := enrollNode(t, ca, "node-1"), enrollNode(t, ca, "node-2")
	agent := AgentServerTLS(holder(t, node1), ca.Cert)

	tests := []struct {
		name           string
		client, server *tls.Config
		wantErr        bool
	}{
		{"master reaches agent", NodeClientTLS(master, ca.Cert, "node-1"), agent, false},
		{"node cannot command agent", NodeClientTLS(holder(t, node2), ca.Cert, "node-1"), agent, true},
		{"master rejects wrong node identity", NodeClientTLS(master, ca.Cert, "node-2"), agent, true},
		{"enrollment with the CA of the token", EnrollmentTLS(ca.Cert), MasterServerTLS(master), false},
		{"enrollment rejects other CA", EnrollmentTLS(newCA(t).Cert), MasterServerTLS(master), true},
		{"enrollment rejects node posing as master", EnrollmentTLS(ca.Cert), MasterServerTLS(holder(t, withCA(node2, ca))), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := handshake(t, tt.client, tt.server); (err != nil) != tt.wantErr {
				t.Fatalf("handshake error = %v, want error %v", err, tt.wantErr)
			}
		})
	}
}

func TestRenewedCertificateIsUsedWithoutRestart(t *testing.T) {
	ca := newCA(t)
	node := holder(t, enrollNode(t, ca, "node-1"))
	client, server := NodeClientTLS(holder(t, must(ca.MasterCertificate())), ca.Cert, "node-1"), AgentServerTLS(node, ca.Cert)

	before, err := handshake(t, client, server)
	if err != nil {
		t.Fatal(err)
	}
	if err := node.Set(enrollNode(t, ca, "node-1")); err != nil {
		t.Fatal(err)
	}
	after, err := handshake(t, client, server)
	if err != nil {
		t.Fatal(err)
	}
	if before.SerialNumber.Cmp(after.SerialNumber) == 0 || after.SerialNumber.Cmp(node.Get().Leaf.SerialNumber) != 0 {
		t.Fatal("the agent did not present its renewed certificate")
	}
}

func TestNeedsRenewal(t *testing.T) {
	start := time.Now()
	cert := &x509.Certificate{NotBefore: start, NotAfter: start.Add(90 * 24 * time.Hour)}
	if NeedsRenewal(cert, start.Add(59*24*time.Hour)) {
		t.Error("renewal due with two thirds of the lifetime left")
	}
	if !NeedsRenewal(cert, start.Add(61*24*time.Hour)) {
		t.Error("renewal not due with less than a third of the lifetime left")
	}
}

func TestKeyPairStorage(t *testing.T) {
	ca, dir := newCA(t), t.TempDir()
	for _, pair := range []tls.Certificate{enrollNode(t, ca, "node-1"), enrollNode(t, ca, "node-1")} { // the second renews
		if err := SaveKeyPair(dir, "node", pair.Certificate[0], pair.PrivateKey.(crypto.Signer)); err != nil {
			t.Fatal(err)
		}
		if loaded, err := LoadKeyPair(dir, "node"); err != nil || loaded.Leaf.SerialNumber.Cmp(pair.Leaf.SerialNumber) != 0 {
			t.Fatalf("saved key pair not loaded: %v", err)
		}
	}
}

func newCA(t *testing.T) *CA {
	t.Helper()
	return must(LoadOrCreateCA(t.TempDir()))
}

func enrollNode(t *testing.T, ca *CA, nodeID string) tls.Certificate {
	t.Helper()
	key := NewKey()
	der, err := ca.SignNodeCSR(must(NewCSR(key)), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: must(x509.ParseCertificate(der))}
}

func withCA(cert tls.Certificate, ca *CA) tls.Certificate {
	cert.Certificate = append(cert.Certificate, ca.Cert.Raw)
	return cert
}

func holder(t *testing.T, cert tls.Certificate) *Holder {
	t.Helper()
	return must(NewHolder(cert))
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// handshake runs a TLS handshake over loopback. It returns the certificate the server
// presented and the errors of both sides.
func handshake(t *testing.T, client, server *tls.Config) (*x509.Certificate, error) {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", server)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	serverErr := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		serverErr <- conn.(*tls.Conn).Handshake()
	}()

	var presented *x509.Certificate
	conn, err := tls.Dial("tcp", ln.Addr().String(), client)
	if err == nil {
		presented = conn.ConnectionState().PeerCertificates[0]
		conn.Close()
	}
	return presented, errors.Join(err, <-serverErr)
}
