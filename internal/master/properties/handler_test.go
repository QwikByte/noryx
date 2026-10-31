package properties

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/access"
)

// agent answers the PropertiesService of a node and records what it was asked.
type agent struct {
	grpc.ClientConnInterface
	node     string
	get      *noryxv1.GetServerPropertiesResponse
	err      error
	requests []proto.Message
}

func (a *agent) Conn(_ context.Context, nodeID string) (grpc.ClientConnInterface, error) {
	a.node = nodeID
	return a, nil
}

func (a *agent) Invoke(_ context.Context, method string, args, reply any, _ ...grpc.CallOption) error {
	a.requests = append(a.requests, args.(proto.Message))
	if a.err != nil {
		return a.err
	}
	if method == noryxv1.PropertiesService_GetServerProperties_FullMethodName {
		proto.Merge(reply.(proto.Message), a.get)
	}
	return nil
}

// serve sends a request through the routes of the handler, as a user with grants.
func serve(t *testing.T, a *agent, grants access.Grants, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	NewHandler(a).Register(access.NewMux(mux))
	r := httptest.NewRequestWithContext(access.WithGrants(t.Context(), grants), method, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func TestGet(t *testing.T) {
	a := &agent{get: &noryxv1.GetServerPropertiesResponse{
		Exists: true, Properties: map[string]string{"motd": "Hi", "max-players": "20"},
		Locked: []*noryxv1.LockedProperty{{Key: "server-port", Reason: noryxv1.ManagedProperties["server-port"]}},
	}}
	w := serve(t, a, access.Admin(), "GET", "/api/nodes/n1/servers/s1/properties", "")
	var view struct {
		Exists     bool
		Properties map[string]string
		Locked     []struct{ Key, Reason string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || w.Code != http.StatusOK || !view.Exists ||
		!maps.Equal(view.Properties, map[string]string{"motd": "Hi", "max-players": "20"}) || len(view.Locked) != 1 || view.Locked[0].Key != "server-port" {
		t.Fatalf("view = %d %s, %v", w.Code, w.Body, err)
	}
	if a.node != "n1" || !proto.Equal(a.requests[0], &noryxv1.GetServerPropertiesRequest{ServerId: "s1"}) {
		t.Errorf("asked node %s for %v", a.node, a.requests)
	}

	// A server that didn't start yet has no file; the panel gets empty lists rather than null.
	a.get = &noryxv1.GetServerPropertiesResponse{}
	if w := serve(t, a, access.Admin(), "GET", "/api/nodes/n1/servers/s1/properties", ""); w.Body.String() != `{"exists":false,"properties":{},"locked":[]}`+"\n" {
		t.Errorf("view without a file = %s", w.Body)
	}
}

func TestUpdate(t *testing.T) {
	a := &agent{}
	w := serve(t, a, access.Admin(), "PUT", "/api/nodes/n1/servers/s1/properties", `{"properties":{"motd":"Welcome","pvp":"false"}}`)
	want := &noryxv1.UpdateServerPropertiesRequest{ServerId: "s1", Properties: map[string]string{"motd": "Welcome", "pvp": "false"}}
	if w.Code != http.StatusNoContent || len(a.requests) != 1 || !proto.Equal(a.requests[0], want) {
		t.Fatalf("update: %d %s, sent %v", w.Code, w.Body, a.requests)
	}
	// The agent refuses what Noryx manages itself, which the panel learns.
	a.err = status.Error(codes.InvalidArgument, "server-port is managed by Noryx")
	if w := serve(t, a, access.Admin(), "PUT", "/api/nodes/n1/servers/s1/properties", `{"properties":{"server-port":"1"}}`); w.Code != http.StatusBadRequest ||
		!strings.Contains(w.Body.String(), "managed by Noryx") {
		t.Errorf("refused update: %d %s", w.Code, w.Body)
	}
	a.requests = nil
	for _, body := range []string{`{"properties":{"motd":1}}`, `{"props":{}}`, `not json`} {
		if w := serve(t, a, access.Admin(), "PUT", "/api/nodes/n1/servers/s1/properties", body); w.Code != http.StatusBadRequest {
			t.Errorf("body %s: %d", body, w.Code)
		}
	}
	if len(a.requests) != 0 {
		t.Errorf("invalid bodies reached the agent: %v", a.requests)
	}
}

// Both routes need the permission to edit server.properties.
func TestPermission(t *testing.T) {
	a := &agent{get: &noryxv1.GetServerPropertiesResponse{}}
	for _, method := range []string{"GET", "PUT"} {
		if w := serve(t, a, access.Grants{}, method, "/api/nodes/n1/servers/s1/properties", `{"properties":{}}`); w.Code != http.StatusForbidden ||
			!strings.Contains(w.Body.String(), access.Label(access.Properties)) {
			t.Errorf("%s without the permission: %d %s", method, w.Code, w.Body)
		}
	}
	if len(a.requests) != 0 {
		t.Errorf("the agent was asked: %v", a.requests)
	}
}
