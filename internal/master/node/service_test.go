package node

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"google.golang.org/grpc"

	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/pki"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
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
	s := NewService(db, ca, holder, nil)
	t.Cleanup(s.Close)
	return s
}

func TestConnIsSharedPerNode(t *testing.T) {
	s := newTestService(t)
	ids := []string{"n1", "n2", "n3"}
	for _, id := range ids {
		if _, err := s.db.Exec(`INSERT INTO nodes (id, name, address, enrolled_at, created_at) VALUES (?, ?, '127.0.0.1:7443', 1, 0)`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	got := map[string]map[grpc.ClientConnInterface]bool{}
	var wg sync.WaitGroup
	for range 20 {
		for _, id := range ids {
			wg.Go(func() {
				conn, err := s.Conn(t.Context(), id)
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				if got[id] == nil {
					got[id] = map[grpc.ClientConnInterface]bool{}
				}
				got[id][conn] = true
			})
		}
	}
	wg.Wait()
	for _, id := range ids {
		if len(got[id]) != 1 {
			t.Errorf("node %s got %d connections, want 1", id, len(got[id]))
		}
	}

	// A new address and deleting the node replace and drop the connection.
	first, _ := s.Conn(t.Context(), "n1")
	n, err := s.Get(t.Context(), "n1")
	if err != nil {
		t.Fatal(err)
	}
	n.Address = "127.0.0.2:7443"
	if _, err := s.Update(t.Context(), n); err != nil {
		t.Fatal(err)
	}
	if conn, err := s.Conn(t.Context(), "n1"); err != nil || conn == first || conn.(*grpc.ClientConn).Target() != n.Address {
		t.Errorf("Conn after a new address = %v, %v, want a connection to %s", conn, err, n.Address)
	}
	if err := s.Delete(t.Context(), "n1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Conn(t.Context(), "n1"); err == nil {
		t.Error("Conn returned a connection to a deleted node")
	}
}

// Conn keeps no connection to a node that changed while it loaded the node: it loads it again
// and connects to its new address, or fails once the node was removed.
func TestConnOfNodeChangedMeanwhile(t *testing.T) {
	s := newTestService(t)
	for _, id := range []string{"n1", "n2"} {
		if _, err := s.db.Exec(`INSERT INTO nodes (id, name, address, enrolled_at, created_at) VALUES (?, ?, '127.0.0.1:7443', 1, 0)`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	// change changes the node the first time Conn loaded it.
	change := func(fn func() error) {
		loads := 0
		s.loaded = func() {
			if loads++; loads == 1 {
				if err := fn(); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	change(func() error {
		n, err := s.Get(t.Context(), "n1")
		if err == nil {
			n.Address = "127.0.0.2:7443"
			_, err = s.Update(t.Context(), n)
		}
		return err
	})
	target := func() string {
		conn, err := s.Conn(t.Context(), "n1")
		if err != nil {
			t.Fatal(err)
		}
		return conn.(*grpc.ClientConn).Target()
	}
	if got := target(); got != "127.0.0.2:7443" {
		t.Fatalf("Conn of a node whose address changed meanwhile connects to %s", got)
	}
	if got := target(); got != "127.0.0.2:7443" {
		t.Fatalf("Conn again connects to %s", got)
	}

	change(func() error { return s.Delete(t.Context(), "n2") })
	if _, err := s.Conn(t.Context(), "n2"); !errors.Is(err, errNotFound) {
		t.Fatalf("Conn of a node removed meanwhile = %v", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.conns["n2"]; ok {
		t.Error("a connection to the removed node was kept")
	}
}
