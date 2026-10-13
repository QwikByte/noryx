package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/access"
	masterapp "github.com/QwikByte/noryx/internal/master/app"
	"github.com/QwikByte/noryx/internal/master/network"
	"github.com/QwikByte/noryx/internal/master/workflow"
)

// A workflow chains steps with data: it finds servers, goes through them, decides, catches a
// failure and returns a result, with the inputs of the run and the permissions of its author.
func TestWorkflowRuns(t *testing.T) {
	m := startMaster(t)
	a := m.startAgent(t, "node-1")
	lobby := m.createServer(t, a, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565)
	m.createServer(t, a, "Survival", noryxv1.ServerType_SERVER_TYPE_PAPER, 25566)
	svc := m.services(t)
	srv := httptest.NewTLSServer(masterapp.Handler(svc))
	t.Cleanup(srv.Close)
	admin, err := svc.Users.CreateUser(t.Context(), "admin", "the-admins-password")
	check(t, err)
	check(t, svc.Access.MakeAdmin(t.Context(), admin.ID))
	api := browser(t, srv)
	api.do("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "the-admins-password"}, http.StatusOK, nil)
	api.do("POST", "/api/nodes/"+lobby.NodeID+"/servers/"+lobby.ServerID+"/start", nil, http.StatusNoContent, nil)

	step := func(id, kind string, with map[string]any) map[string]any {
		return map[string]any{"id": id, "kind": kind, "with": with}
	}
	steps := []map[string]any{
		step("find", "servers", map[string]any{"targets": []map[string]string{{"nodeId": a.node.ID}}, "state": "running"}),
		{
			"id": "each", "kind": "foreach", "with": map[string]any{"items": "{{steps.find.servers}}"},
			"steps": []map[string]any{step("greet", "command", map[string]any{"from": "{{item}}", "commands": []string{"say {{inputs.greeting}} on {{server.name}}"}})},
		},
		{
			"id": "check", "kind": "if", "with": map[string]any{"condition": map[string]any{"match": "all", "rules": []map[string]string{{"left": "{{steps.find.count}}", "op": "eq", "right": "1"}}}},
			"steps": []map[string]any{step("count", "set", map[string]any{"name": "count", "value": "{{steps.find.count | add:41}}"})},
			"else":  []map[string]any{step("fail", "terminate", map[string]any{"failed": true, "message": "Wrong count"})},
		},
		{
			"id": "attempt", "kind": "try",
			"steps": []map[string]any{step("local", "http", map[string]any{"url": "https://127.0.0.1/"})},
			"else":  []map[string]any{step("caught", "set", map[string]any{"name": "caught", "value": "{{error.step}}"})},
		},
		step("names", "set", map[string]any{"name": "names", "value": "{{steps.find.servers | map:name | join}}"}),
		step("done", "terminate", map[string]any{"result": "{{vars.count}} {{vars.caught}} {{vars.names}}"}),
	}
	draft := map[string]any{
		"name": "Greeter", "enabled": true, "triggers": []any{}, "steps": steps,
		"params": []map[string]any{{"name": "greeting", "type": "text", "required": true}},
	}

	// Someone who may manage workflows, but not send console commands, can't make one send them.
	var group access.Group
	api.do("POST", "/api/groups", map[string]any{"name": "Automators", "permissions": []string{"workflows.manage"}}, http.StatusCreated, &group)
	var invited struct{ User struct{ ID int64 } }
	api.do("POST", "/api/users", map[string]any{"username": "automator", "groups": []string{group.ID}}, http.StatusCreated, &invited)
	grants, err := svc.Access.Grants(t.Context(), invited.User.ID)
	check(t, err)
	handler := masterapp.API(svc)
	restricted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r.WithContext(access.WithGrants(r.Context(), grants)))
	}))
	t.Cleanup(restricted.Close)
	automator := apiClient{t: t, url: restricted.URL}
	if body := automator.do("POST", "/api/workflows", draft, http.StatusForbidden, nil); !strings.Contains(body, "Send console commands") {
		t.Fatalf("denied with %s", body)
	}

	var wf workflow.Workflow
	api.do("POST", "/api/workflows", draft, http.StatusCreated, &wf)
	if wf.SavedBy != "admin" || len(wf.Steps) != len(steps) {
		t.Fatalf("created %+v", wf)
	}
	// Nor can they run it, or make another workflow run it for them.
	automator.do("POST", "/api/workflows/"+wf.ID+"/run", map[string]any{"inputs": map[string]any{"greeting": "Hi"}}, http.StatusForbidden, nil)
	caller := map[string]any{
		"name": "Caller", "enabled": true, "triggers": []any{},
		"steps": []any{step("call", "call", map[string]any{"workflow": wf.ID, "inputs": map[string]string{"greeting": "Hi"}})},
	}
	automator.do("POST", "/api/workflows", caller, http.StatusForbidden, nil)

	api.do("POST", "/api/workflows/"+wf.ID+"/run", map[string]any{"inputs": map[string]any{}}, http.StatusBadRequest, nil)
	run := runWorkflow(t, api, wf.ID, map[string]any{"greeting": "Hi"})
	if run.Outcome != workflow.Succeeded {
		t.Fatalf("run = %+v", run)
	}
	if got := a.runtime.commandsTo(lobby.ServerID); !slices.Contains(got, "say Hi on Lobby") {
		t.Fatalf("commands to the lobby = %q", got)
	}
	result := map[string]string{}
	for _, s := range run.Steps {
		result[s.ID] = s.Outcome
		if s.ID == "done" {
			var out struct{ Result string }
			check(t, json.Unmarshal(s.Output, &out))
			result["result"] = out.Result
		}
	}
	want := map[string]string{
		"find": "succeeded", "each": "succeeded", "greet": "succeeded", "check": "succeeded", "count": "succeeded", "attempt": "succeeded",
		"local": "failed", "caught": "succeeded", "names": "succeeded", "done": "succeeded", "result": "42 local Lobby",
	}
	for k, v := range want {
		if result[k] != v {
			t.Fatalf("%s = %q, want %q; steps = %+v", k, result[k], v, run.Steps)
		}
	}
}

// Secret headers are never shown and keep their values, and a webhook starts a workflow with
// its body, once it has a URL that only its creation shows.
func TestWorkflowSecretsAndWebhooks(t *testing.T) {
	m := startMaster(t)
	svc := m.services(t)
	srv := httptest.NewTLSServer(masterapp.Handler(svc))
	t.Cleanup(srv.Close)
	admin, err := svc.Users.CreateUser(t.Context(), "admin", "the-admins-password")
	check(t, err)
	check(t, svc.Access.MakeAdmin(t.Context(), admin.ID))
	api := browser(t, srv)
	api.do("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "the-admins-password"}, http.StatusOK, nil)

	request := map[string]any{"id": "call", "kind": "http", "continue": true, "with": map[string]any{
		"method": "POST", "url": "https://192.0.2.10/hook", "body": `{"text":"{{trigger.body.text}}"}`,
		"headers": []map[string]any{{"name": "Authorization", "value": "Bearer the-secret", "secret": true}},
	}}
	draft := map[string]any{
		"name": "Hooked", "enabled": true, "triggers": []map[string]any{{"kind": "webhook"}},
		"steps": []any{request, map[string]any{"id": "note", "kind": "set", "with": map[string]any{"name": "text", "value": "{{trigger.body.text}}"}}},
	}
	var wf workflow.Workflow
	api.do("POST", "/api/workflows", draft, http.StatusCreated, &wf)
	if body := api.do("GET", "/api/workflows/"+wf.ID, nil, http.StatusOK, nil); strings.Contains(body, "the-secret") {
		t.Fatalf("the secret is shown: %s", body)
	}
	request["with"].(map[string]any)["headers"] = []map[string]any{{"name": "Authorization", "value": "", "secret": true}}
	api.do("PUT", "/api/workflows/"+wf.ID, draft, http.StatusOK, nil)
	var raw string
	check(t, m.db.QueryRowContext(t.Context(), `SELECT definition FROM workflows WHERE id = ?`, wf.ID).Scan(&raw))
	if !strings.Contains(raw, "Bearer the-secret") {
		t.Fatalf("the secret was lost: %s", raw)
	}

	// Without a URL, or with a wrong one, nothing starts.
	public := srv.Client()
	call := func(path, body string, want int) {
		t.Helper()
		res, err := public.Post(srv.URL+path, "application/json", strings.NewReader(body))
		check(t, err)
		res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("POST %s = %d, want %d", path, res.StatusCode, want)
		}
	}
	call(workflow.HookPath+"unknown", `{}`, http.StatusNotFound)
	var hook struct{ Path string }
	api.do("POST", "/api/workflows/"+wf.ID+"/hook", nil, http.StatusOK, &hook)
	call(hook.Path, `{"text":"hello"}`, http.StatusAccepted)
	var runs []workflow.Run
	for deadline := time.Now().Add(10 * time.Second); len(runs) == 0 || runs[0].Outcome == workflow.Running; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the webhook started no run")
		}
		api.do("GET", "/api/workflows/"+wf.ID+"/runs", nil, http.StatusOK, &runs)
	}
	var run workflow.Run
	api.do("GET", "/api/workflows/"+wf.ID+"/runs/"+strconv.FormatInt(runs[0].ID, 10), nil, http.StatusOK, &run)
	if run.Trigger != workflow.OnWebhook || run.Outcome != workflow.Succeeded || !strings.Contains(string(run.Data), "hello") || len(run.Steps) != 2 ||
		run.Steps[0].Outcome != workflow.Failed || !strings.Contains(run.Steps[0].Detail, "public address") {
		t.Fatalf("run = %+v", run)
	}

	// A new URL replaces the one before; a paused workflow and one without a URL start nothing.
	api.do("POST", "/api/workflows/"+wf.ID+"/hook", nil, http.StatusOK, &hook)
	draft["enabled"] = false
	api.do("PUT", "/api/workflows/"+wf.ID, draft, http.StatusOK, nil)
	call(hook.Path, `{}`, http.StatusNotFound)
	api.do("DELETE", "/api/workflows/"+wf.ID+"/hook", nil, http.StatusNoContent, nil)
	draft["enabled"] = true
	api.do("PUT", "/api/workflows/"+wf.ID, draft, http.StatusOK, nil)
	call(hook.Path, `{}`, http.StatusNotFound)
}

// runWorkflow runs a workflow now and returns its run once it ended.
func runWorkflow(t *testing.T, api apiClient, id string, inputs map[string]any) workflow.Run {
	t.Helper()
	var run workflow.Run
	api.do("POST", "/api/workflows/"+id+"/run", map[string]any{"inputs": inputs}, http.StatusAccepted, &run)
	for deadline := time.Now().Add(10 * time.Second); run.Outcome == workflow.Running; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the workflow didn't end")
		}
		api.do("GET", "/api/workflows/"+id+"/runs/"+strconv.FormatInt(run.ID, 10), nil, http.StatusOK, &run)
	}
	return run
}

// Deleting a server takes it out of workflows: a trigger that only watched it goes, a step that
// only acted on it is turned off, and the others keep their other servers.
func TestWorkflowsForgetDeletedServers(t *testing.T) {
	m := startMaster(t)
	a := m.startAgent(t, "node-1")
	lobby := m.createServer(t, a, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565)
	survival := m.createServer(t, a, "Survival", noryxv1.ServerType_SERVER_TYPE_PAPER, 25566)
	api := apiClient{t: t, url: m.panel(t).URL}
	server := func(ref network.Ref) map[string]string {
		return map[string]string{"kind": "server", "nodeId": ref.NodeID, "serverId": ref.ServerID}
	}
	var wf workflow.Workflow
	api.do("POST", "/api/workflows", map[string]any{
		"name": "Watched", "enabled": true,
		"triggers": []any{map[string]any{"kind": "server", "on": "started", "targets": []any{server(lobby)}}},
		"steps": []any{
			map[string]any{"id": "only", "kind": "restart", "with": map[string]any{"targets": []any{server(lobby)}}},
			map[string]any{"id": "both", "kind": "restart", "with": map[string]any{"targets": []any{server(lobby), server(survival)}}},
		},
	}, http.StatusCreated, &wf)
	api.do("DELETE", "/api/nodes/"+lobby.NodeID+"/servers/"+lobby.ServerID, nil, http.StatusNoContent, nil)
	api.do("GET", "/api/workflows/"+wf.ID, nil, http.StatusOK, &wf)
	if len(wf.Triggers) != 0 || !wf.Steps[0].Disabled || wf.Steps[1].Disabled || strings.Contains(string(wf.Steps[1].With), lobby.ServerID) ||
		!strings.Contains(string(wf.Steps[1].With), survival.ServerID) {
		t.Fatalf("after deleting the lobby: %+v", wf)
	}
}
