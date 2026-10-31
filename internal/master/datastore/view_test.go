package datastore

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/overlay"
)

// A datastore shows the state its agent reports, and which of its databases a running one
// lacks; one that its node doesn't know, or can't tell, says so.
func TestView(t *testing.T) {
	ds := Datastore{ID: "d1", Engine: "postgres", Databases: []Database{{Name: "luckperms", password: "secret"}, {Name: "coreprotect", password: "secret"}}}
	res := &noryxv1.ListDatastoresResponse{
		Datastores: []*noryxv1.Datastore{{Id: "d1", State: noryxv1.DatastoreState_DATASTORE_STATE_RUNNING, Size: 4096, PreviousVersion: "16", Databases: []string{"luckperms"}}},
		Versions: []*noryxv1.DatastoreVersions{
			{Engine: noryxv1.DatastoreEngine_DATASTORE_ENGINE_MARIADB, Versions: []string{"11"}},
			{Engine: noryxv1.DatastoreEngine_DATASTORE_ENGINE_POSTGRES, Versions: []string{"16", "17"}},
		},
	}
	v := view(ds, res, nil)
	if v.State != "running" || v.Size != 4096 || v.Previous != "16" || !slices.Equal(v.Versions, []string{"16", "17"}) ||
		!slices.Equal(v.Missing, []string{"coreprotect"}) || v.Problem != "" {
		t.Errorf("view = %+v", v)
	}
	res.Datastores[0].State = noryxv1.DatastoreState_DATASTORE_STATE_STARTING
	if v := view(ds, res, nil); v.State != "starting" || len(v.Missing) != 0 {
		t.Errorf("a starting datastore lacks %v", v.Missing)
	}
	if v := view(ds, &noryxv1.ListDatastoresResponse{}, nil); v.State != "unknown" || !strings.Contains(v.Problem, "no such datastore") {
		t.Errorf("a datastore the node doesn't know: %+v", v)
	}
	if v := view(ds, nil, httpapi.Errorf(http.StatusBadGateway, "The node can't be reached.")); v.State != "unknown" || v.Problem != "The node can't be reached." || v.Versions == nil || v.Missing == nil {
		t.Errorf("a node that can't be reached: %+v", v)
	}

	// The passwords of the databases never reach the panel.
	data, err := json.Marshal(v)
	if err != nil || strings.Contains(string(data), "secret") || strings.Contains(string(data), "password") {
		t.Errorf("view as JSON = %s, %v", data, err)
	}
}

// Servers on the node of a datastore reach it by the name of its container, those of other
// nodes at its node's address in the private network, once it has a port there.
func TestEndpoints(t *testing.T) {
	ds := Datastore{ID: "d1", Engine: "mariadb"}
	if got := endpoints(ds, "10.77.0.1"); !slices.Equal(got, []Endpoint{{Host: "noryx-db-d1", Port: 3306}}) {
		t.Errorf("without a port = %+v", got)
	}
	ds.Engine, ds.Port = "postgres", 40001
	if got := endpoints(ds, "10.77.0.1"); !slices.Equal(got, []Endpoint{{Host: "noryx-db-d1", Port: 5432}, {Host: "10.77.0.1", Port: 40001, Remote: true}}) {
		t.Errorf("with a port = %+v", got)
	}
	if got := endpoints(ds, ""); len(got) != 1 {
		t.Errorf("without an address in the private network = %+v", got)
	}

	members := map[string]overlay.Member{"n1": {Address: "10.77.0.1"}, "n2": {Address: "10.77.0.2"}, "n3": {}}
	for _, tc := range []struct {
		from, to string
		want     bool
	}{
		{"n1", "n2", true},
		{"n2", "n1", true},
		{"n1", "n1", false}, // on its own node, by the name of its container
		{"n1", "n3", false},
		{"n4", "n1", false},
	} {
		if got := reaches(members, tc.from, tc.to); got != tc.want {
			t.Errorf("reaches(%s, %s) = %v", tc.from, tc.to, got)
		}
	}
}

// The passwords of databases have 160 random bits in characters that need no quoting.
func TestNewPassword(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		p := newPassword()
		if !noryxv1.DatabasePassword.MatchString(p) || seen[p] {
			t.Fatalf("password %q", p)
		}
		seen[p] = true
	}
}
