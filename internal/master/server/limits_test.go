package server

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/node"
)

// limitsNodes is a node with 3379 MB of memory, of which 1024 MB are reserved, and servers
// and datastores.
type limitsNodes struct {
	Nodes
	servers    []*noryxv1.Server
	datastores []*noryxv1.Datastore
}

func (limitsNodes) Get(_ context.Context, id string) (node.Node, error) {
	return node.Node{ID: id, Name: "Agent 1", MemoryReserveMB: new(uint32(1024))}, nil
}

func (n limitsNodes) Conn(context.Context, string) (grpc.ClientConnInterface, error) {
	return limitsAgent{servers: n.servers, datastores: n.datastores}, nil
}

type limitsAgent struct {
	grpc.ClientConnInterface
	servers    []*noryxv1.Server
	datastores []*noryxv1.Datastore
}

func (a limitsAgent) Invoke(_ context.Context, _ string, _, reply any, _ ...grpc.CallOption) error {
	switch r := reply.(type) {
	case *noryxv1.GetInfoResponse:
		r.MemoryBytes = 3379 << 20
	case *noryxv1.ListServersResponse:
		r.Servers = a.servers
	case *noryxv1.ListDatastoresResponse:
		r.Datastores = a.datastores
	}
	return nil
}

func TestCheckLimitsCountsContainers(t *testing.T) {
	check := func(h *Handler, serverID string, memoryMB uint32) (func(), error) {
		return h.checkLimits(t.Context(), "n1", serverID, 25565, memoryMB)
	}
	// Two servers with 1 GB fit the 2355 MB left for servers, but their containers don't:
	// they may use 1536 MB each.
	full := NewHandler(limitsNodes{servers: []*noryxv1.Server{{Id: "s1", MemoryMb: 1024}, {Id: "s2", MemoryMb: 1024}}}, nil, nil, nil, nil, nil, NewMoves(), nil, nil)
	if _, err := check(full, "", 256); err == nil || !strings.Contains(err.Error(), "up to 0 MB") {
		t.Errorf("a new server on a full node: %v", err)
	}
	for _, mb := range []uint32{1024, 512} {
		if _, err := check(full, "s1", mb); err != nil {
			t.Errorf("a change of s1 to %d MB, which needs no more memory: %v", mb, err)
		}
	}
	if _, err := check(full, "s1", 1025); err == nil {
		t.Error("s1 got more memory on a full node")
	}

	// With one server, 819 MB are left, enough for a container of 450 MB of heap: 818 MB.
	h := NewHandler(limitsNodes{servers: []*noryxv1.Server{{Id: "s1", MemoryMb: 1024}}}, nil, nil, nil, nil, nil, NewMoves(), nil, nil)
	if _, err := check(h, "", 1024); err == nil || !strings.Contains(err.Error(), "up to 450 MB") {
		t.Fatalf("a second server with 1 GB: %v", err)
	}
	release, err := check(h, "", 450)
	if err != nil {
		t.Fatalf("a server that fits: %v", err)
	}
	if _, err := check(h, "", 1); err == nil {
		t.Fatal("a server fit while the memory of another was reserved")
	}
	release()
	if _, err := check(h, "", 450); err != nil {
		t.Fatalf("after the release: %v", err)
	}

	// A datastore takes its memory limit: 819 MB minus 512 MB leave a container for 40 MB of heap.
	h = NewHandler(limitsNodes{servers: []*noryxv1.Server{{Id: "s1", MemoryMb: 1024}}, datastores: []*noryxv1.Datastore{{Id: "d1", MemoryMb: 512}}}, nil, nil, nil, nil, nil, NewMoves(), nil, nil)
	if _, err := check(h, "", 450); err == nil || !strings.Contains(err.Error(), "up to 40 MB") {
		t.Fatalf("next to a datastore: %v", err)
	}
}
