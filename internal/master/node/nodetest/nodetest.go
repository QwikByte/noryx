// Package nodetest enrolls nodes for tests of the master: their agents run in the test and
// serve the gRPC services that it registers, over mutual TLS like real agents.
package nodetest

import (
	"crypto/tls"
	"database/sql"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/QwikByte/noryx/internal/master/node"
	"github.com/QwikByte/noryx/internal/pki"
)

// Nodes are the nodes of a master, whose certificates come from its CA.
type Nodes struct {
	*node.Service
	db *sql.DB
	ca *pki.CA
}

// New returns the nodes of the master with the database db.
func New(t testing.TB, db *sql.DB) *Nodes {
	t.Helper()
	ca, err := pki.LoadOrCreateCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cert, err := ca.MasterCertificate()
	if err != nil {
		t.Fatal(err)
	}
	holder, err := pki.NewHolder(cert)
	if err != nil {
		t.Fatal(err)
	}
	s := node.NewService(db, ca, holder, nil)
	t.Cleanup(s.Close)
	return &Nodes{Service: s, db: db, ca: ca}
}

// Add adds an enrolled node, named like its ID, whose agent serves what register registers
// until the test ends; without register, nothing answers at its address. It returns the
// holder of the agent's certificate.
func (n *Nodes) Add(t testing.TB, id string, register func(*grpc.Server)) *pki.Holder {
	t.Helper()
	key := pki.NewKey()
	csr, err := pki.NewCSR(key)
	if err != nil {
		t.Fatal(err)
	}
	der, err := n.ca.SignNodeCSR(csr, id)
	if err != nil {
		t.Fatal(err)
	}
	holder, err := pki.NewHolder(tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if register == nil {
		ln.Close()
	} else {
		srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(pki.AgentServerTLS(holder, n.ca.Cert))))
		register(srv)
		go srv.Serve(ln) //nolint:errcheck // ends with the test
		t.Cleanup(srv.Stop)
	}
	if _, err := n.db.Exec(`INSERT INTO nodes (id, name, address, enrolled_at, created_at) VALUES (?, ?, ?, 1, 0)`, id, id, ln.Addr().String()); err != nil {
		t.Fatal(err)
	}
	return holder
}
