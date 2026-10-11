package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"testing"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	masterapp "github.com/QwikByte/noryx/internal/master/app"
	"github.com/QwikByte/noryx/internal/master/backup"
	"github.com/QwikByte/noryx/internal/master/fileset"
	"github.com/QwikByte/noryx/internal/master/notify"
	"github.com/QwikByte/noryx/internal/master/preference"
	"github.com/QwikByte/noryx/internal/master/schedule"
	"github.com/QwikByte/noryx/internal/master/workflow"
)

// What refers to a server, a node or a notification channel follows when it is deleted, without
// ever widening what it does: rules of a server are deleted rather than sending the entries of
// its whole node, and a job without its copy node no longer copies.
func TestReferencesFollowDeletions(t *testing.T) {
	m := startMaster(t)
	a1, a2 := m.startAgent(t, "node-1"), m.startAgent(t, "node-2")
	lobby := m.createServer(t, a1, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565)
	_, url, roots := m.startWebhook(t)
	svc := m.services(t)
	svc.Notify = notify.New(m.db, m.logs, m.settings, notify.Options{Allow: func(ip netip.Addr) bool { return ip.IsLoopback() }, Roots: roots})
	srv := httptest.NewTLSServer(masterapp.Handler(svc))
	t.Cleanup(srv.Close)
	admin, err := svc.Users.CreateUser(t.Context(), "admin", "the-admins-password")
	check(t, err)
	check(t, svc.Access.MakeAdmin(t.Context(), admin.ID))
	api := browser(t, srv)
	api.do("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "the-admins-password"}, http.StatusOK, nil)

	var ch, other notify.Channel
	api.do("POST", "/api/notifications/channels", map[string]string{"name": "Ops", "kind": "webhook", "url": url + "/ops"}, http.StatusCreated, &ch)
	api.do("POST", "/api/notifications/channels", map[string]string{"name": "Other", "kind": "webhook", "url": url + "/other"}, http.StatusCreated, &other)
	for _, rule := range []notify.RuleInput{
		{NodeID: lobby.NodeID, ServerID: lobby.ServerID}, {NodeID: a2.node.ID}, {NodeID: lobby.NodeID},
	} {
		rule.ChannelID, rule.Enabled, rule.Level = ch.ID, true, "warn"
		api.do("POST", "/api/notifications/rules", rule, http.StatusCreated, nil)
	}
	alerts := preference.Alerts{Level: "warn", Only: true, Nodes: []string{lobby.NodeID, a2.node.ID}, Servers: []string{lobby.ServerID}}
	api.do("PUT", "/api/preferences/alerts", alerts, http.StatusOK, nil)
	var set fileset.Set
	api.do("POST", "/api/filesets", fileset.Input{Name: "Motd", Variables: []fileset.Variable{{Name: "motd", Values: []fileset.Value{
		{Kind: fileset.KindServer, Scope: lobby.ServerID, Value: "Lobby"}, {Kind: fileset.KindAll, Value: "Noryx"},
	}}}}, http.StatusCreated, &set)
	var job schedule.Task
	api.do("POST", "/api/backup-jobs", map[string]any{
		"name": "Nightly", "enabled": true, "schedule": map[string]any{"times": []string{"03:00"}, "timeZone": "UTC"},
		"targets":  []map[string]string{{"nodeId": lobby.NodeID}},
		"settings": map[string]any{"selection": map[string]any{"everything": true}, "keep": 2, "copy": map[string]string{"node": a2.node.ID}},
	}, http.StatusCreated, &job)
	var wf workflow.Workflow
	api.do("POST", "/api/workflows", map[string]any{"name": "Tell", "steps": []any{
		map[string]any{"id": "ops", "kind": "notify", "with": map[string]any{"channel": ch.ID, "message": "hi"}},
		map[string]any{"id": "other", "kind": "notify", "with": map[string]any{"channel": other.ID, "message": "hi"}},
	}}, http.StatusCreated, &wf)

	rules := func() []notify.Rule {
		var got struct{ Rules []notify.Rule }
		api.do("GET", "/api/notifications", nil, http.StatusOK, &got)
		return got.Rules
	}
	prefs := func() preference.Alerts {
		var got preference.Preferences
		api.do("GET", "/api/preferences", nil, http.StatusOK, &got)
		return got.Alerts
	}

	// A deleted server leaves the rules, alerts and values of variables that were for it.
	api.do("DELETE", "/api/nodes/"+lobby.NodeID+"/servers/"+lobby.ServerID, nil, http.StatusNoContent, nil)
	if got := rules(); len(got) != 2 || slices.ContainsFunc(got, func(r notify.Rule) bool { return r.ServerID != "" }) {
		t.Fatalf("rules after deleting the lobby = %+v", got)
	}
	if got := prefs(); len(got.Servers) != 0 || len(got.Nodes) != 2 || !got.Only {
		t.Fatalf("alerts after deleting the lobby = %+v", got)
	}
	api.do("GET", "/api/filesets/"+set.ID, nil, http.StatusOK, &set)
	if values := set.Variables[0].Values; len(values) != 1 || values[0].Kind != fileset.KindAll {
		t.Fatalf("values after deleting the lobby = %+v", values)
	}

	// A removed node leaves its rules, the alerts and the job that copied to it.
	api.do("DELETE", "/api/nodes/"+a2.node.ID, nil, http.StatusNoContent, nil)
	if got := rules(); len(got) != 1 || got[0].NodeID != lobby.NodeID {
		t.Fatalf("rules after removing node-2 = %+v", got)
	}
	if got := prefs(); !slices.Equal(got.Nodes, []string{lobby.NodeID}) {
		t.Fatalf("alerts after removing node-2 = %+v", got)
	}
	api.do("GET", "/api/backup-jobs/"+job.ID, nil, http.StatusOK, &job)
	var settings backup.JobSettings
	check(t, json.Unmarshal(job.Settings, &settings))
	if settings.Copy != nil || !job.Enabled {
		t.Fatalf("job after removing node-2 = %+v", job)
	}

	// A deleted channel turns off the steps that notify it.
	api.do("DELETE", "/api/notifications/channels/"+other.ID, nil, http.StatusNoContent, nil)
	api.do("GET", "/api/workflows/"+wf.ID, nil, http.StatusOK, &wf)
	if wf.Steps[0].Disabled || !wf.Steps[1].Disabled {
		t.Fatalf("steps after deleting a channel = %+v", wf.Steps)
	}
}
