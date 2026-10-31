package node

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/enrollment"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/pki"
)

// config is what the settings of the master tell about nodes.
type config struct{}

func (config) EnrollAddr() string          { return "master.example.com:7444" }
func (config) JoinTokenTTL() time.Duration { return time.Hour }
func (config) NodeDefaults() Limits        { return Limits{} }

// from is the context of a call of a client at an address.
func from(ctx context.Context, ip string) context.Context {
	return peer.NewContext(ctx, &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP(ip), Port: 40000}})
}

// enroll sends the request of an agent with a new key, from its own address unless ip is set.
func enroll(t *testing.T, s *Service, token enrollment.Token, ip string) (*noryxv1.EnrollResponse, error) {
	t.Helper()
	csr, err := pki.NewCSR(pki.NewKey())
	if err != nil {
		t.Fatal(err)
	}
	return s.Enroll(from(t.Context(), ip), &noryxv1.EnrollRequest{NodeId: token.NodeID, Secret: token.Secret, CsrDer: csr})
}

func enrolled(t *testing.T, s *Service, id string) bool {
	t.Helper()
	n, err := s.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return n.EnrolledAt != nil
}

// A join token enrolls its node once, before it expires, and only with a valid signing
// request; a new one replaces it.
func TestEnrollConsumesToken(t *testing.T) {
	s := newTestService(t)
	s.config = config{}
	ctx := t.Context()
	n, token, err := s.Create(ctx, " node-1 ", " 203.0.113.10:7443 ")
	if err != nil {
		t.Fatal(err)
	}
	if n.Name != "node-1" || n.Address != "203.0.113.10:7443" || n.Agent != agentOf(noryxv1.DefaultNodeSettings()) || n.DefaultStorage != "default" {
		t.Errorf("created %+v", n)
	}
	if token.NodeID != n.ID || token.Master != "master.example.com:7444" || token.CAFingerprint != pki.Fingerprint(s.ca.Cert) ||
		time.Until(token.ExpiresAt) < 59*time.Minute || !strings.HasSuffix(token.InstallCommand(), "--join "+token.String()) {
		t.Errorf("token %+v, command %q", token, token.InstallCommand())
	}

	other, otherToken, err := s.Create(ctx, "node-2", "203.0.113.11:7443")
	if err != nil {
		t.Fatal(err)
	}
	wrong := token.Token
	wrong.Secret = "wrong"
	otherNode := token.Token
	otherNode.NodeID = other.ID
	for i, tc := range []struct {
		name  string
		token enrollment.Token
	}{
		{"a wrong secret", wrong},
		{"the secret of another node", otherNode},
		{"no node", enrollment.Token{Secret: token.Secret}},
	} {
		if _, err := enroll(t, s, tc.token, fmt.Sprintf("198.51.100.%d", i)); status.Code(err) != codes.PermissionDenied {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	// An invalid signing request leaves the token as it was.
	if _, err := s.Enroll(from(ctx, "198.51.100.10"), &noryxv1.EnrollRequest{NodeId: n.ID, Secret: token.Secret, CsrDer: []byte("no CSR")}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("invalid CSR: %v", err)
	}
	if enrolled(t, s, n.ID) || enrolled(t, s, other.ID) {
		t.Fatal("a node enrolled with a wrong request")
	}

	res, err := enroll(t, s, token.Token, "198.51.100.11")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(res.GetCertificateDer())
	if err != nil || !slices.Contains(cert.DNSNames, pki.NodeName(n.ID)) || !slices.Equal(res.GetCaCertificateDer(), s.ca.Cert.Raw) {
		t.Fatalf("certificate for %v, %v", cert.DNSNames, err)
	}
	if !enrolled(t, s, n.ID) || enrolled(t, s, other.ID) {
		t.Fatal("enrolled the wrong node")
	}
	if _, err := enroll(t, s, token.Token, "198.51.100.12"); status.Code(err) != codes.PermissionDenied {
		t.Errorf("a used token: %v", err)
	}

	// A new token replaces the pending one, which no longer works.
	newer, err := s.NewJoinToken(ctx, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := enroll(t, s, otherToken.Token, "198.51.100.13"); status.Code(err) != codes.PermissionDenied {
		t.Errorf("a replaced token: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE nodes SET join_expires_at = ? WHERE id = ?`, time.Now().Add(-time.Second).Unix(), other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := enroll(t, s, newer.Token, "198.51.100.14"); status.Code(err) != codes.PermissionDenied {
		t.Errorf("an expired token: %v", err)
	}
	if _, err := s.NewJoinToken(ctx, "nope"); !errors.Is(err, errNotFound) {
		t.Errorf("token of an unknown node: %v", err)
	}
}

// A client gets 5 attempts to enroll, then one every 12 seconds, and others keep theirs.
func TestEnrollIsRateLimited(t *testing.T) {
	s := newTestService(t)
	s.config = config{}
	_, token, err := s.Create(t.Context(), "node-1", "203.0.113.10:7443")
	if err != nil {
		t.Fatal(err)
	}
	wrong := token.Token
	wrong.Secret = "guess"
	for range enrollBurst {
		if _, err := enroll(t, s, wrong, "198.51.100.1"); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("a guess: %v", err)
		}
	}
	if _, err := enroll(t, s, token.Token, "198.51.100.1"); status.Code(err) != codes.ResourceExhausted {
		t.Errorf("an attempt beyond the limit: %v", err)
	}
	if _, err := enroll(t, s, token.Token, "198.51.100.2"); err != nil {
		t.Errorf("another client: %v", err)
	}
}

func TestNodeSettings(t *testing.T) {
	s := newTestService(t)
	s.config = config{}
	ctx := t.Context()
	n, _, err := s.Create(ctx, "node-1", "203.0.113.10:7443")
	if err != nil {
		t.Fatal(err)
	}
	port := func(p uint32) *uint32 { return &p }
	for _, tc := range []struct {
		name   string
		edit   func(n *Node)
		status int
	}{
		{"no name", func(n *Node) { n.Name = " " }, http.StatusBadRequest},
		{"a long name", func(n *Node) { n.Name = strings.Repeat("n", 65) }, http.StatusBadRequest},
		{"an address without port", func(n *Node) { n.Address = "203.0.113.10" }, http.StatusBadRequest},
		{"an unknown storage location", func(n *Node) { n.DefaultStorage = "../data" }, http.StatusBadRequest},
		{"half a port range", func(n *Node) { n.PortMin = port(25565) }, http.StatusBadRequest},
		{"a privileged port range", func(n *Node) { n.PortMin, n.PortMax = port(80), port(25565) }, http.StatusBadRequest},
		{"a reversed port range", func(n *Node) { n.PortMin, n.PortMax = port(30000), port(25565) }, http.StatusBadRequest},
		{"a large reserve", func(n *Node) { n.MemoryReserveMB = port(1<<20 + 1) }, http.StatusBadRequest},
		{"settings of the agent out of bounds", func(n *Node) { n.KeepFreeGB = 0 }, http.StatusBadRequest},
		{"a node that doesn't exist", func(n *Node) { n.ID = "nope" }, http.StatusNotFound},
	} {
		changed := n
		tc.edit(&changed)
		var apiErr *httpapi.Error
		if _, err := s.Update(ctx, changed); !errors.As(err, &apiErr) || apiErr.Status != tc.status {
			t.Errorf("%s: %v, want %d", tc.name, err, tc.status)
		}
	}
	n.PortMin, n.PortMax, n.MemoryReserveMB, n.DefaultStorage, n.LogFileMB, n.LogFiles = port(25565), port(25600), port(2048), "ssd", 0, 0
	if updated, err := s.Update(ctx, n); err != nil || *updated.PortMin != 25565 || *updated.PortMax != 25600 || *updated.MemoryReserveMB != 2048 ||
		updated.DefaultStorage != "ssd" || updated.LogFiles != 0 {
		t.Errorf("updated %+v, %v", updated, err)
	}
	var apiErr *httpapi.Error
	if _, _, err := s.Create(ctx, "NODE-1", "203.0.113.11:7443"); !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict {
		t.Errorf("a name in use: %v", err)
	}

	// Only enrolled nodes are connected to.
	if _, err := s.Conn(ctx, n.ID); !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict {
		t.Errorf("a node that didn't enroll: %v", err)
	}
	if _, err := s.Conn(ctx, "nope"); !errors.Is(err, errNotFound) {
		t.Errorf("an unknown node: %v", err)
	}
}
