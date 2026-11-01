// Package enroll registers a freshly installed agent with its master.
package enroll

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/enrollment"
	"github.com/QwikByte/noryx/internal/pki"
)

// Names of the credential files inside the agent's PKI directory.
const (
	NodeCert = "node"
	CACert   = "ca"
)

// Run exchanges a join token for a node certificate and stores the credentials in dir.
// The private key is generated locally and never leaves the node.
func Run(ctx context.Context, joinToken, dir string) error {
	token, err := enrollment.ParseToken(joinToken)
	if err != nil {
		return err
	}
	ca, err := x509.ParseCertificate(token.CA)
	if err != nil {
		return errors.New("the join token holds no valid CA certificate")
	}
	key := pki.NewKey()
	csr, err := pki.NewCSR(key)
	if err != nil {
		return err
	}

	creds := credentials.NewTLS(pki.EnrollmentTLS(ca))
	conn, err := grpc.NewClient(token.Master, grpc.WithTransportCredentials(creds))
	if err != nil {
		return err
	}
	defer conn.Close()
	res, err := noryxv1.NewEnrollmentServiceClient(conn).Enroll(ctx, &noryxv1.EnrollRequest{
		NodeId: token.NodeID,
		Secret: token.Secret,
		CsrDer: csr,
	})
	if err != nil {
		return fmt.Errorf("enroll at %s: %w", token.Master, err)
	}
	if err := pki.SaveCert(dir, CACert, ca.Raw); err != nil {
		return err
	}
	return pki.SaveKeyPair(dir, NodeCert, res.GetCertificateDer(), key)
}
