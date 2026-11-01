package logs

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
)

// listingNodes answers each ListServers with the next of its answers; nil is an unreachable agent.
type listingNodes struct {
	fakeNodes
	answers []map[string]string
}

func (l *listingNodes) Conn(context.Context, string) (grpc.ClientConnInterface, error) {
	answer := l.answers[0]
	l.answers = l.answers[1:]
	if answer == nil {
		return nil, errors.New("offline")
	}
	return listingConn(answer), nil
}

type listingConn map[string]string

func (c listingConn) Invoke(_ context.Context, _ string, _, reply any, _ ...grpc.CallOption) error {
	res := reply.(*noryxv1.ListServersResponse)
	for id, name := range c {
		res.Servers = append(res.Servers, &noryxv1.Server{Id: id, Name: name})
	}
	return nil
}

func (listingConn) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, errors.New("unused")
}

func TestServerNames(t *testing.T) {
	nodes := &listingNodes{answers: []map[string]string{{"s1": "lobby"}, {"s2": "game"}, nil, {"s3": "hub"}}}
	names, ctx := NewNames(nodes), t.Context()
	lookup := func(id string) string {
		names.servers["n1"].at = time.Time{} // asks the agent again
		return names.Server(ctx, "n1", id)
	}
	if got := names.Server(ctx, "n1", "s1"); got != "lobby" {
		t.Fatalf("s1 = %q", got)
	}
	// A deleted server keeps its name for the entries that come in after it was deleted,
	if got := lookup("s1"); got != "lobby" {
		t.Errorf("s1 after it was deleted = %q", got)
	}
	// also while the agent can't be reached,
	if got := lookup("s1"); got != "lobby" {
		t.Errorf("s1 while the agent is offline = %q", got)
	}
	// but not forever.
	if got := lookup("s1"); got != "" {
		t.Errorf("s1 two answers after it was deleted = %q", got)
	}
	if got, known := names.Server(ctx, "n1", "s2"), len(names.servers["n1"].names); got != "game" || known != 2 {
		t.Errorf("s2 = %q, %d names known", got, known)
	}
	// Requests may name nodes that don't exist, which aren't kept.
	if got := names.Server(ctx, "made-up", "s1"); got != "" || len(names.servers) != 1 {
		t.Errorf("a server of an unknown node = %q, %d nodes known", got, len(names.servers))
	}
}
