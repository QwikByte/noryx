package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"maps"
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
	"github.com/QwikByte/noryx/internal/master/auth"
	"github.com/QwikByte/noryx/internal/master/logs"
	"github.com/QwikByte/noryx/internal/master/network"
)

// A script uses the API with a token instead of the password, with no more than the token's
// permissions and its user's current ones, and the log names the token.
func TestAPITokens(t *testing.T) {
	m := startMaster(t)
	prev := slog.Default()
	slog.SetDefault(slog.New(m.logs.Handler(slog.LevelInfo)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	a := m.startAgent(t, "node-1")
	lobby := m.createServer(t, a, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565)
	survival := m.createServer(t, a, "Survival", noryxv1.ServerType_SERVER_TYPE_PAPER, 25566)
	path := func(s network.Ref) string { return "/api/nodes/" + s.NodeID + "/servers/" + s.ServerID }
	svc := m.services(t)
	srv := httptest.NewTLSServer(masterapp.Handler(svc))
	t.Cleanup(srv.Close)

	const password = "a-long-enough-password"
	admin, err := svc.Users.CreateUser(t.Context(), "admin", password)
	check(t, err)
	check(t, svc.Access.MakeAdmin(t.Context(), admin.ID))
	alice, err := svc.Users.CreateUser(t.Context(), "alice", password)
	check(t, err)
	alicePath := "/api/users/" + strconv.FormatInt(alice.ID, 10)
	signIn := func(username string) apiClient {
		c := browser(t, srv)
		c.do("POST", "/api/auth/login", map[string]string{"username": username, "password": password}, http.StatusOK, nil)
		return c
	}
	root := signIn("admin")
	var operators access.Group
	root.do("POST", "/api/groups", map[string]any{
		"name": "Lobby operators", "permissions": []string{"servers.start", "servers.stop"}, "targets": []network.Ref{lobby},
	}, http.StatusCreated, &operators)
	root.do("PUT", alicePath, map[string]any{"groups": []string{operators.ID}}, http.StatusNoContent, nil)

	// Alice creates tokens on her account page with her password: one that may only start servers.
	browserOfAlice := signIn("alice")
	type created struct {
		auth.Token
		Secret string `json:"secret"`
	}
	var starter, full created
	browserOfAlice.do("POST", "/api/auth/tokens", map[string]any{"name": "starter", "permissions": []string{"servers.start"}, "password": "wrong"}, http.StatusBadRequest, nil)
	// Tokens last a year at most, unless the settings say otherwise.
	browserOfAlice.do("POST", "/api/auth/tokens", map[string]any{"name": "starter", "permissions": []string{"servers.start"}, "password": password}, http.StatusBadRequest, nil)
	year := time.Now().Add(365 * 24 * time.Hour)
	browserOfAlice.do("POST", "/api/auth/tokens", map[string]any{"name": "starter", "permissions": []string{"servers.start"}, "expiresAt": year, "password": password}, http.StatusOK, &starter)
	browserOfAlice.do("POST", "/api/auth/tokens", map[string]any{"name": "full", "expiresAt": year, "password": password}, http.StatusOK, &full)
	script := withToken(t, srv, starter.Secret)

	// The script starts the lobby without the password, but does no more than the token and Alice may.
	script.do("POST", path(lobby)+"/start", nil, http.StatusNoContent, nil)
	script.do("POST", path(lobby)+"/stop", nil, http.StatusForbidden, nil)
	script.do("POST", path(survival)+"/start", nil, http.StatusForbidden, nil)
	var grants struct {
		Admin       bool                      `json:"admin"`
		Permissions map[access.Permission]any `json:"permissions"`
	}
	script.do("GET", "/api/access/me", nil, http.StatusOK, &grants)
	if got := slices.Sorted(maps.Keys(grants.Permissions)); grants.Admin || !slices.Equal(got, []access.Permission{access.ServersStart, access.ServersView}) {
		t.Fatalf("permissions of the token = %v", got)
	}
	withToken(t, srv, full.Secret).do("POST", path(lobby)+"/stop", nil, http.StatusNoContent, nil)

	// Tokens can't use the account: not create tokens, change the password or sign in to the panel.
	script.do("POST", "/api/auth/tokens", map[string]any{"name": "more", "password": password}, http.StatusForbidden, nil)
	script.do("PUT", "/api/auth/password", map[string]string{"current": password, "new": password + "!"}, http.StatusForbidden, nil)
	script.do("GET", "/api/auth/me", nil, http.StatusForbidden, nil)
	script.do("GET", "/api/preferences", nil, http.StatusForbidden, nil)

	// Pages of other sites can't use a token in a browser: CORS stays closed, and their changes are refused.
	for _, method := range []string{"OPTIONS", "POST"} {
		req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path(lobby)+"/start", nil)
		check(t, err)
		req.Header.Set("Origin", "https://attacker.example")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "authorization")
		res, err := script.client.Do(req)
		check(t, err)
		res.Body.Close()
		if res.Header.Get("Access-Control-Allow-Origin") != "" || method == "POST" && res.StatusCode != http.StatusForbidden {
			t.Errorf("%s from another site: %d %v", method, res.StatusCode, res.Header)
		}
	}

	// The account page lists the tokens with their last use, but never the tokens themselves.
	var tokens []auth.Token
	listed := browserOfAlice.do("GET", "/api/auth/tokens", nil, http.StatusOK, &tokens)
	i := slices.IndexFunc(tokens, func(tk auth.Token) bool { return tk.ID == starter.ID })
	if len(tokens) != 2 || i < 0 || tokens[i].IP != "127.0.0.1" || tokens[i].LastUsedAt.IsZero() || strings.Contains(listed, auth.TokenPrefix) {
		t.Fatalf("tokens = %s", listed)
	}

	// The description lists every route with its permissions, also to scripts.
	var doc struct {
		Paths map[string]map[string]struct {
			Permissions []access.Permission `json:"x-permissions"`
		} `json:"paths"`
	}
	script.do("GET", "/api/openapi.json", nil, http.StatusOK, &doc)
	for route, want := range map[[2]string][]access.Permission{
		{"/api/nodes/{node}/servers/{id}/start", "post"}: {access.ServersStart},
		{"/api/nodes/{node}/servers/{id}/move", "post"}:  {access.ServersDelete, access.FilesRead},
		{"/api/update/master", "post"}:                   {access.Administrators},
		{"/api/servers", "get"}:                          {},
		{"/api/terminal", "post"}:                        {access.Terminal},
	} {
		if got := doc.Paths[route[0]][route[1]].Permissions; !slices.Equal(got, want) {
			t.Errorf("%s %s needs %v, want %v", route[1], route[0], got, want)
		}
	}
	if _, ok := doc.Paths["/api/auth/tokens"]; ok || len(doc.Paths) < 100 {
		t.Errorf("the description has %d paths, among them those of the account: %v", len(doc.Paths), ok)
	}

	// The log names the token besides the user, and holds neither the token nor its hash.
	var entries []logEntry
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		root.do("GET", "/api/logs?source=master", nil, http.StatusOK, &entries)
		if slices.ContainsFunc(entries, func(e logEntry) bool { return e.Message == "Stop server denied" }) {
			break
		}
	}
	find := func(message string) logEntry {
		i := slices.IndexFunc(entries, func(e logEntry) bool { return e.Message == message && e.Source == logs.FromMaster })
		if i < 0 {
			t.Fatalf("no entry %q in %+v", message, entries)
		}
		return entries[i]
	}
	if e := find("Start server"); e.User != "alice" || e.Attrs["token"] != "starter" || e.Attrs["token_id"] != starter.ID {
		t.Errorf("entry of the script = %+v", e)
	}
	if e := find("Stop server denied"); e.User != "alice" || e.Attrs["token"] != "starter" {
		t.Errorf("denied entry of the script = %+v", e)
	}
	if e := find("Create API token"); e.User != "alice" || e.Attrs["token_id"] == "" || e.Attrs["token"] == "" {
		t.Errorf("entry of the new token = %+v", e)
	}
	data, err := json.Marshal(entries)
	check(t, err)
	for _, secret := range []string{starter.Secret, full.Secret} {
		hash := sha256.Sum256([]byte(secret))
		if strings.Contains(string(data), secret[len(auth.TokenPrefix):]) || strings.Contains(string(data), hex.EncodeToString(hash[:])) {
			t.Fatalf("the log holds a token or its hash: %s", data)
		}
	}

	// Required two-factor authentication applies to tokens too, until it is set up.
	thresholds := path(lobby) + "/usage/thresholds"
	root.do("PUT", "/api/settings", map[string]any{"requireMfa": map[string]any{"groups": []string{operators.ID}}}, http.StatusOK, nil)
	if answer := script.do("GET", thresholds, nil, http.StatusForbidden, nil); !strings.Contains(answer, `"mfa-setup-required"`) {
		t.Errorf("token while two-factor authentication is required: %s", answer)
	}
	browserOfAlice.do("POST", "/api/auth/tokens", map[string]any{"name": "more", "password": password}, http.StatusForbidden, nil)
	root.do("PUT", "/api/settings", map[string]any{"requireMfa": map[string]any{"groups": []string{}}}, http.StatusOK, nil)
	script.do("GET", thresholds, nil, http.StatusOK, nil)

	// Changes of Alice's permissions apply to her tokens at once, and disabling her ends them.
	root.do("PUT", alicePath, map[string]any{"groups": []string{}}, http.StatusNoContent, nil)
	script.do("GET", thresholds, nil, http.StatusForbidden, nil)
	root.do("PUT", alicePath, map[string]any{"groups": []string{operators.ID}, "disabled": true}, http.StatusNoContent, nil)
	root.do("PUT", alicePath, map[string]any{"groups": []string{operators.ID}}, http.StatusNoContent, nil)
	script.do("GET", thresholds, nil, http.StatusUnauthorized, nil)

	// Wrong tokens take attempts like sign-ins, which this client used some of already.
	wrong := withToken(t, srv, auth.TokenPrefix+"WRONG")
	var refused []int
	for len(refused) < 6 && !slices.Contains(refused, http.StatusTooManyRequests) {
		res, err := wrong.client.Get(srv.URL + "/api/servers")
		check(t, err)
		res.Body.Close()
		refused = append(refused, res.StatusCode)
	}
	if refused[0] != http.StatusUnauthorized || refused[len(refused)-1] != http.StatusTooManyRequests {
		t.Errorf("wrong tokens: %v", refused)
	}
}

// withToken returns a client of a panel that sends an API token, like a script.
func withToken(t *testing.T, srv *httptest.Server, token string) apiClient {
	client := *srv.Client()
	client.Transport = bearer{token, client.Transport}
	return apiClient{t: t, url: srv.URL, client: &client}
}

type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r)
}
