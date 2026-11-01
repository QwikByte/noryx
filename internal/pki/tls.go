package pki

import (
	"crypto/tls"
	"crypto/x509"
)

// The configurations read certificates from a Holder, so renewed certificates are used
// for new handshakes without restarting anything.

// AgentServerTLS is used by agents and only admits clients that present a master
// certificate issued by ca. Node certificates are rejected, so agents can never
// command each other.
func AgentServerTLS(node *Holder, ca *x509.Certificate) *tls.Config {
	return &tls.Config{
		MinVersion:     tls.VersionTLS13,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return node.Get(), nil },
		ClientAuth:     tls.RequireAndVerifyClientCert,
		ClientCAs:      pool(ca),
		VerifyConnection: func(cs tls.ConnectionState) error {
			return cs.PeerCertificates[0].VerifyHostname(MasterName)
		},
	}
}

// NodeClientTLS lets the master connect to the agent of the node with the given ID.
func NodeClientTLS(master *Holder, ca *x509.Certificate, nodeID string) *tls.Config {
	return &tls.Config{
		MinVersion:           tls.VersionTLS13,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return master.Get(), nil },
		RootCAs:              pool(ca),
		ServerName:           NodeName(nodeID),
	}
}

// MasterServerTLS serves the enrollment endpoint of the master. Clients do not
// have a certificate yet; they authenticate with their join token instead.
func MasterServerTLS(master *Holder) *tls.Config {
	return &tls.Config{
		MinVersion:     tls.VersionTLS13,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return master.Get(), nil },
	}
}

// EnrollmentTLS is used by an agent during enrollment, before it holds a certificate of
// its own. It only trusts a master certificate issued by ca, the CA from the join token.
func EnrollmentTLS(ca *x509.Certificate) *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: pool(ca), ServerName: MasterName}
}

func pool(cert *x509.Certificate) *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(cert)
	return p
}
