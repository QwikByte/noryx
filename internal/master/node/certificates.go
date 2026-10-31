package node

import (
	"context"
	"crypto/x509"
	"errors"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/pki"
)

const (
	renewTimeout = 30 * time.Second
	// connDrainTime lets requests in flight finish on a connection that is replaced.
	connDrainTime = 15 * time.Minute
)

// Status asks the agent for its machine info. It also returns the certificate the
// agent presented, whose expiry drives the automatic renewal, and stores its expiry.
func (s *Service) Status(ctx context.Context, id string) (*noryxv1.GetInfoResponse, *x509.Certificate, error) {
	conn, err := s.Conn(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	var p peer.Peer
	info, err := noryxv1.NewNodeServiceClient(conn).GetInfo(ctx, &noryxv1.GetInfoRequest{}, grpc.Peer(&p))
	if err != nil {
		return nil, nil, err
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(tlsInfo.State.PeerCertificates) == 0 {
		return nil, nil, errors.New("the agent presented no certificate")
	}
	cert := tlsInfo.State.PeerCertificates[0]
	s.seen(ctx, id, cert.NotAfter)
	return info, cert, nil
}

// seen stores when a certificate of a node expires, so that it is known while the node is
// offline, also after a restart. Newer certificates expire later, so the stored expiry only
// moves forward: neither a call that started before a renewal nor a node that presents an
// older certificate brings back an earlier one.
func (s *Service) seen(ctx context.Context, id string, notAfter time.Time) {
	s.mu.Lock()
	stored := s.expiries[id]
	s.mu.Unlock()
	if !notAfter.After(stored) {
		return
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE nodes SET certificate_expires_at = max(coalesce(certificate_expires_at, 0), ?) WHERE id = ?`,
		notAfter.Unix(), id)
	if err != nil {
		slog.Warn("Can't store when the certificate of a node expires", logging.Nodes, logging.KeyNode, id, "err", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if database.RowsAffected(res) == 1 && notAfter.After(s.expiries[id]) { // not for a node removed meanwhile
		s.expiries[id] = notAfter
	}
}

// RenewCertificate replaces the certificate of a node. The agent creates the new key,
// so only a signing request and the signed certificate cross the network.
func (s *Service) RenewCertificate(ctx context.Context, id string) (*x509.Certificate, error) {
	s.renewMu.Lock()
	defer s.renewMu.Unlock()
	conn, err := s.Conn(ctx, id)
	if err != nil {
		return nil, err
	}
	client := noryxv1.NewNodeServiceClient(conn)
	csr, err := client.CreateCSR(ctx, &noryxv1.CreateCSRRequest{})
	if err != nil {
		return nil, err
	}
	der, err := s.ca.SignNodeCSR(csr.GetCsrDer(), id)
	if err != nil {
		return nil, err
	}
	if _, err := client.InstallCertificate(ctx, &noryxv1.InstallCertificateRequest{CertificateDer: der}); err != nil {
		return nil, err
	}
	// TLS checks certificates only when connecting, so a fresh connection is needed
	// to see the new one.
	s.retire(id)
	cert, err := x509.ParseCertificate(der)
	if err == nil {
		s.seen(ctx, id, cert.NotAfter)
	}
	return cert, err
}

// MaintainCertificates renews every node certificate once less than a third of its
// lifetime is left, checking at the given interval until ctx is done. Nodes that are
// offline are retried at the next check.
func (s *Service) MaintainCertificates(ctx context.Context, interval time.Duration) {
	for {
		s.renewDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

func (s *Service) renewDue(ctx context.Context) {
	nodes, err := s.List(ctx)
	if err != nil {
		slog.Error("Can't list the nodes to renew their certificates", logging.Nodes, "err", err)
		return
	}
	for _, n := range nodes {
		if n.EnrolledAt == nil {
			continue
		}
		ctx, cancel := context.WithTimeout(ctx, renewTimeout)
		if _, cert, err := s.Status(ctx, n.ID); err == nil && pki.NeedsRenewal(cert, time.Now()) {
			if renewed, err := s.RenewCertificate(ctx, n.ID); err != nil {
				slog.Warn("Renew node certificate failed", logging.Nodes, logging.KeyNode, n.ID, logging.KeyNodeName, n.Name, "err", err)
			} else {
				slog.Info("Renew node certificate", logging.Nodes, logging.KeyNode, n.ID, logging.KeyNodeName, n.Name, "not_after", renewed.NotAfter)
			}
		}
		cancel()
	}
}

// retire makes the next request open a fresh connection to the node, while requests
// in flight get time to finish on the old one.
func (s *Service) retire(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gen++
	if conn, ok := s.conns[id]; ok {
		delete(s.conns, id)
		time.AfterFunc(connDrainTime, func() { conn.Close() })
	}
}
