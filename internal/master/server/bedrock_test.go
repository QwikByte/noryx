package server

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/node"
)

// bedrockNodes is a node with the ports from 19132 to 19141, whose agent creates the types it
// lists.
type bedrockNodes struct {
	Nodes
	types []noryxv1.ServerType
}

func (bedrockNodes) Get(_ context.Context, id string) (node.Node, error) {
	return node.Node{ID: id, Name: "Agent 1", PortMin: new(uint32(19132)), PortMax: new(uint32(19141))}, nil
}

func (n bedrockNodes) Conn(context.Context, string) (grpc.ClientConnInterface, error) {
	return typesAgent{types: n.types}, nil
}

type typesAgent struct {
	grpc.ClientConnInterface
	types []noryxv1.ServerType
}

func (a typesAgent) Invoke(_ context.Context, _ string, _, reply any, _ ...grpc.CallOption) error {
	reply.(*noryxv1.GetInfoResponse).ServerTypes = a.types
	return nil
}

// The players of a Bedrock server take ports of the node's range too, and only agents that know
// Bedrock servers create them.
func TestBedrockLimits(t *testing.T) {
	h := NewHandler(Deps{Nodes: bedrockNodes{}, Moves: NewMoves()})
	if _, err := h.checkLimits(t.Context(), "n1", "", 19132, 19141, 1024); err != nil {
		t.Errorf("players within the range: %v", err)
	}
	if _, err := h.checkLimits(t.Context(), "n1", "", 19133, 19142, 1024); err == nil || !strings.Contains(err.Error(), "up to 19142") {
		t.Errorf("players beyond the range: %v", err)
	}
	bedrock := noryxv1.ServerType_SERVER_TYPE_BEDROCK
	if err := h.checkType(t.Context(), "n1", bedrock); err == nil || !strings.Contains(err.Error(), "Bedrock") {
		t.Errorf("Bedrock on an older agent: %v", err)
	}
	if err := h.checkType(t.Context(), "n1", noryxv1.ServerType_SERVER_TYPE_PAPER); err != nil {
		t.Errorf("Paper on an older agent: %v", err)
	}
	h = NewHandler(Deps{Nodes: bedrockNodes{types: []noryxv1.ServerType{bedrock}}, Moves: NewMoves()})
	if err := h.checkType(t.Context(), "n1", bedrock); err != nil {
		t.Errorf("Bedrock on an agent that knows it: %v", err)
	}
	servers := []*noryxv1.Server{{Name: "Survival", Port: 25565}, {Name: "Bedrock", Type: bedrock, Port: 19132, MaxPlayers: 5}}
	if s, port := usedBy(servers, 19130, 19140); s.GetName() != "Bedrock" || port != 19132 {
		t.Errorf("used by %q at %d", s.GetName(), port)
	}
	if s, _ := usedBy(servers, 19137, 19140); s != nil {
		t.Errorf("ports after the players used by %q", s.GetName())
	}
}
