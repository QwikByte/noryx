package preference

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/httpapi"
)

func open(t *testing.T) *sql.DB {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func id() string { return strings.ToLower(rand.Text()) }

func exec(t *testing.T, db *sql.DB, query string, args ...any) sql.Result {
	t.Helper()
	res, err := db.Exec(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func addUser(t *testing.T, db *sql.DB, name string) int64 {
	t.Helper()
	userID, err := exec(t, db, `INSERT INTO users (username, password_hash, created_at) VALUES (?, '', 0)`, name).LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return userID
}

func addNode(t *testing.T, db *sql.DB) string {
	t.Helper()
	nodeID := id()
	exec(t, db, `INSERT INTO nodes (id, name, address, created_at) VALUES (?, ?, 'host:7443', 0)`, nodeID, nodeID)
	return nodeID
}

func TestValidation(t *testing.T) {
	db := open(t)
	s, ctx, user, node := NewStore(db), t.Context(), addUser(t, db, "alice"), addNode(t, db)
	widgets := func(n int) []Widget {
		w := make([]Widget, n)
		for i := range w {
			w[i] = Widget{ID: fmt.Sprint("w", i), Columns: 1}
		}
		return w
	}
	servers := func(n int) []Server {
		srv := make([]Server, n)
		for i := range srv {
			srv[i] = Server{NodeID: node, ServerID: id()}
		}
		return srv
	}
	badRequest := func(err error) bool {
		var apiErr *httpapi.Error
		return errors.As(err, &apiErr) && apiErr.Status == http.StatusBadRequest
	}

	for name, w := range map[string][]Widget{
		"no id":         {{Columns: 1}},
		"capitals":      {{ID: "Servers", Columns: 1}},
		"leading digit": {{ID: "1servers", Columns: 1}},
		"path":          {{ID: "../servers", Columns: 1}},
		"long id":       {{ID: "s" + strings.Repeat("x", 32), Columns: 1}},
		"twice":         {{ID: "servers", Columns: 1}, {ID: "servers", Columns: 2}},
		"no columns":    {{ID: "servers"}},
		"four columns":  {{ID: "servers", Columns: 4}},
		"too many":      widgets(maxWidgets + 1),
		"no options":    {{ID: "figures", Columns: 1, Options: map[string]string{"count": "5"}}},
		"unknown":       {{ID: "top-servers", Columns: 1, Options: map[string]string{"level": "info"}}},
		"other value":   {{ID: "top-servers", Columns: 1, Options: map[string]string{"count": "8"}}},
		"no value":      {{ID: "activity", Columns: 1, Options: map[string]string{"level": ""}}},
		"node":          {{ID: "nodes", Columns: 1, Options: map[string]string{"nodes": node + ",../" + node[3:]}}},
		"no node":       {{ID: "nodes", Columns: 1, Options: map[string]string{"nodes": ""}}},
		"trailing":      {{ID: "nodes", Columns: 1, Options: map[string]string{"nodes": node + ","}}},
		"many nodes":    {{ID: "resources", Columns: 1, Options: map[string]string{"nodes": strings.Repeat(node+",", maxChosen) + node}}},
	} {
		if err := s.SetDashboard(ctx, user, w); !badRequest(err) {
			t.Errorf("%s: SetDashboard = %v, want a bad request", name, err)
		}
	}
	srv := id()
	for name, pins := range map[string][]Server{
		"no node":    {{ServerID: srv}},
		"no server":  {{NodeID: node}},
		"capitals":   {{NodeID: strings.ToUpper(node), ServerID: srv}},
		"short":      {{NodeID: node, ServerID: srv[1:]}},
		"path":       {{NodeID: node, ServerID: "../" + srv[3:]}},
		"bad letter": {{NodeID: node, ServerID: "0" + srv[1:]}},
		"twice":      {{NodeID: node, ServerID: srv}, {NodeID: node, ServerID: srv}},
		"too many":   servers(maxPinned + 1),
	} {
		if err := s.SetPinned(ctx, user, pins); !badRequest(err) {
			t.Errorf("%s: SetPinned = %v, want a bad request", name, err)
		}
	}
	if p, err := s.Get(ctx, user); err != nil || len(p.Dashboard) != 0 || len(p.Pinned) != 0 {
		t.Fatalf("after invalid changes, Get = %+v, %v", p, err)
	}

	// The limits themselves are fine.
	most := widgets(maxWidgets)
	most[0] = Widget{ID: "s" + strings.Repeat("x", 31), Columns: maxColumns, Hidden: true}
	most[1] = Widget{ID: "resources", Columns: 2, Options: map[string]string{"nodes": strings.Repeat(node+",", maxChosen-1) + node, "range": "week"}}
	if err := s.SetDashboard(ctx, user, most); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPinned(ctx, user, servers(maxPinned)); err != nil {
		t.Fatal(err)
	}
}

func TestStore(t *testing.T) {
	db := open(t)
	s, ctx := NewStore(db), t.Context()
	alice, bob := addUser(t, db, "alice"), addUser(t, db, "bob")
	n1, n2 := addNode(t, db), addNode(t, db)
	lobby, game := id(), id()
	check := func(user int64, want Preferences) {
		t.Helper()
		if want.Settings == nil {
			want.Settings = Settings{}
		}
		if want.Alerts.Level == "" {
			want.Alerts = noAlerts
		}
		if want.Hidden == nil {
			want.Hidden = []HiddenItem{}
		}
		if want.Folded == nil {
			want.Folded = []string{}
		}
		if want.Views == nil {
			want.Views = []View{}
		}
		got, err := s.Get(ctx, user)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("Get(%d) = %+v, %v, want %+v", user, got, err, want)
		}
	}
	none := Preferences{Dashboard: []Widget{}, Pinned: []Server{}, Alerts: noAlerts, Settings: Settings{}, Hidden: []HiddenItem{},
		Folded: []string{}, Views: []View{}}
	// Users start with the default layout and no pins, as empty lists rather than nil.
	check(alice, none)

	dashboard := []Widget{{ID: "servers", Columns: 2}, {ID: "nodes", Columns: 1, Hidden: true}, {ID: "usage", Columns: 3}}
	pinned := []Server{{n2, game}, {n1, lobby}, {n1, game}}
	if err := s.SetDashboard(ctx, alice, dashboard); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPinned(ctx, alice, pinned); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPinned(ctx, bob, []Server{{n1, lobby}}); err != nil {
		t.Fatal(err)
	}
	check(alice, Preferences{Dashboard: dashboard, Pinned: pinned})
	check(bob, Preferences{Dashboard: []Widget{}, Pinned: []Server{{n1, lobby}}})

	// New lists replace the old ones in their order; an empty layout brings back the default.
	// Servers of nodes that were removed in the meantime aren't pinned.
	dashboard = []Widget{dashboard[2], dashboard[0]}
	if err := s.SetDashboard(ctx, alice, dashboard); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPinned(ctx, alice, []Server{{n1, game}, {id(), lobby}, {n1, lobby}, {n2, game}}); err != nil {
		t.Fatal(err)
	}
	check(alice, Preferences{Dashboard: dashboard, Pinned: []Server{{n1, game}, {n1, lobby}, {n2, game}}})
	if err := s.SetDashboard(ctx, alice, nil); err != nil {
		t.Fatal(err)
	}
	check(alice, Preferences{Dashboard: []Widget{}, Pinned: []Server{{n1, game}, {n1, lobby}, {n2, game}}})

	// A server that moves stays pinned, once for those who pinned it at both places; a deleted
	// one is unpinned for everyone, as are the servers of a removed node.
	if err := s.Move(ctx, game, n1, n2); err != nil {
		t.Fatal(err)
	}
	if err := s.Move(ctx, lobby, n1, n2); err != nil {
		t.Fatal(err)
	}
	check(alice, Preferences{Dashboard: []Widget{}, Pinned: []Server{{n2, lobby}, {n2, game}}})
	check(bob, Preferences{Dashboard: []Widget{}, Pinned: []Server{{n2, lobby}}})
	if err := s.Forget(ctx, n2, lobby); err != nil {
		t.Fatal(err)
	}
	check(alice, Preferences{Dashboard: []Widget{}, Pinned: []Server{{n2, game}}})
	check(bob, none)
	exec(t, db, `DELETE FROM nodes WHERE id = ?`, n2)
	check(alice, none)

	// The preferences go with the user.
	if err := s.SetDashboard(ctx, alice, dashboard); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPinned(ctx, alice, []Server{{n1, lobby}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangeSettings(ctx, alice, map[string]*string{"theme": ptr("dark")}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAlerts(ctx, alice, Alerts{Level: "error"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetHidden(ctx, alice, []HiddenItem{{Key: "offline/" + n1, Until: time.Now().Add(time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFolded(ctx, alice, []string{"tag/lobby"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetViews(ctx, alice, []View{{Name: "Lobbies", Search: map[string]string{"tag": "lobby"}}}); err != nil {
		t.Fatal(err)
	}
	exec(t, db, `DELETE FROM users WHERE id = ?`, alice)
	var left int
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM dashboards) + (SELECT COUNT(*) FROM pinned_servers) +
		(SELECT COUNT(*) FROM user_settings) + (SELECT COUNT(*) FROM alert_filters) + (SELECT COUNT(*) FROM hidden_items) +
		(SELECT COUNT(*) FROM folded_groups) + (SELECT COUNT(*) FROM server_views)`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("%d preferences left of a deleted user, %v", left, err)
	}
}

func ptr(s string) *string { return &s }

// noAlerts are the alerts of users who never chose any: all warnings and errors pop up.
var noAlerts = Alerts{Level: "warn", Nodes: []string{}, Servers: []string{}, Categories: []string{}}

func TestSettings(t *testing.T) {
	db := open(t)
	s, ctx := NewStore(db), t.Context()
	alice, bob := addUser(t, db, "alice"), addUser(t, db, "bob")
	change := func(user int64, c map[string]*string) {
		t.Helper()
		if err := s.ChangeSettings(ctx, user, c); err != nil {
			t.Fatal(err)
		}
	}
	check := func(user int64, want Settings) {
		t.Helper()
		got, err := s.Get(ctx, user)
		if err != nil || !reflect.DeepEqual(got.Settings, want) {
			t.Fatalf("settings of %d = %v, %v, want %v", user, got.Settings, err, want)
		}
	}

	// Unknown keys and values are refused, and nothing changes.
	for name, c := range map[string]map[string]*string{
		"unknown key":   {"font": ptr("large")},
		"unknown value": {"theme": ptr("blue")},
		"accent":        {"accent": ptr("#ff0000")},
		"density":       {"density": ptr("tight")},
		"capitals":      {"theme": ptr("Dark")},
		"empty":         {"clock": ptr("")},
		"key of a list": {"serverview": ptr("table")},
		"one of two":    {"theme": ptr("dark"), "clock": ptr("25h")},
		"lines":         {"consoleLines": ptr("1000000")},
		"time zone":     {"timeZone": ptr("Mars/Olympus_Mons")},
		"no time zone":  {"timeZone": ptr("")},
		"local":         {"timeZone": ptr("Local")},
		"path":          {"timeZone": ptr("../../etc/passwd")},
		"long":          {"timeZone": ptr("Europe/" + strings.Repeat("x", 64))},
		"mods":          {"pluginType": ptr("fabric")},
		"debug":         {"logLevel": ptr("debug")},
		// The panel opens only at a page of its sidebar, never at another address.
		"start page":   {"startPage": ptr("https://example.com/")},
		"account":      {"startPage": ptr("/account")},
		"column":       {"serverColumns": ptr("node,disk")},
		"column twice": {"serverColumns": ptr("cpu,node,cpu")},
		"no column":    {"serverColumns": ptr("node,")},
		"spaces":       {"serverColumns": ptr("node, cpu")},
		"node sort":    {"nodeSort": ptr("players")},
		"network sort": {"networkSort": ptr("cpu")},
		"user sort":    {"userSort": ptr("groups")},
	} {
		var apiErr *httpapi.Error
		if err := s.ChangeSettings(ctx, alice, c); !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
			t.Errorf("%s: ChangeSettings = %v, want a bad request", name, err)
		}
	}
	check(alice, Settings{})

	// Changes merge with what is stored; null removes a key, and nothing changes nothing.
	change(alice, map[string]*string{"theme": ptr("dark"), "serverSort": ptr("players"), "accent": ptr("violet")})
	change(alice, map[string]*string{"clock": ptr("24h"), "serverSort": ptr("cpu"), "density": ptr("compact")})
	change(bob, map[string]*string{"theme": ptr("light")})
	check(alice, Settings{"theme": "dark", "accent": "violet", "density": "compact", "clock": "24h", "serverSort": "cpu"})
	change(alice, map[string]*string{"theme": nil, "accent": nil, "serverView": nil})
	change(alice, nil)
	check(alice, Settings{"density": "compact", "clock": "24h", "serverSort": "cpu"})
	check(bob, Settings{"theme": "light"})

	// The time zone is any IANA time zone, the other settings one of their values.
	change(bob, map[string]*string{"timeZone": ptr("America/Argentina/Buenos_Aires"), "codeSize": ptr("large"), "csvSeparator": ptr("semicolon")})
	check(bob, Settings{"theme": "light", "timeZone": "America/Argentina/Buenos_Aires", "codeSize": "large", "csvSeparator": "semicolon"})
	change(bob, map[string]*string{"timeZone": ptr("UTC"), "codeSize": nil, "csvSeparator": nil})
	check(bob, Settings{"theme": "light", "timeZone": "UTC"})

	// The choices of lists and the log's level are kept too, the software of plugins apart from that of mods.
	change(alice, map[string]*string{"pluginType": ptr("purpur"), "modType": ptr("neoforge"), "playerTab": ptr("seen"), "logLevel": ptr("warn")})
	check(alice, Settings{"density": "compact", "clock": "24h", "serverSort": "cpu", "pluginType": "purpur", "modType": "neoforge", "playerTab": "seen", "logLevel": "warn"})
	// The columns of tables of servers are some of theirs in any order, or none.
	change(bob, map[string]*string{"serverColumns": ptr("tps,node,version")})
	check(bob, Settings{"theme": "light", "timeZone": "UTC", "serverColumns": "tps,node,version"})
	change(bob, map[string]*string{"serverColumns": ptr("")})
	check(bob, Settings{"theme": "light", "timeZone": "UTC", "serverColumns": ""})
	change(bob, map[string]*string{"serverColumns": nil})

	// Lists of nodes, networks and users keep their views and sorts like those of servers.
	change(bob, map[string]*string{"nodeView": ptr("table"), "nodeSort": ptr("memory"), "networkSort": ptr("players"), "userSort": ptr("added"),
		"userOrder": ptr("desc")})
	check(bob, Settings{"theme": "light", "timeZone": "UTC", "nodeView": "table", "nodeSort": "memory", "networkSort": "players",
		"userSort": "added", "userOrder": "desc"})
	change(bob, map[string]*string{"nodeView": nil, "nodeSort": nil, "networkSort": nil, "userSort": nil, "userOrder": nil})

	// Values that a newer version stored, or an older one knew, aren't shown.
	exec(t, db, `UPDATE user_settings SET settings = '{"theme":"sepia","clock":"12h","font":"large","timeZone":"Local"}' WHERE user_id = ?`, bob)
	check(bob, Settings{"clock": "12h"})
	change(bob, map[string]*string{"theme": ptr("system")})
	check(bob, Settings{"theme": "system", "clock": "12h"})
}

func TestAlerts(t *testing.T) {
	db := open(t)
	s, ctx, alice := NewStore(db), t.Context(), addUser(t, db, "alice")
	node, srv := id(), id()
	ids := func(n int) []string {
		l := make([]string, n)
		for i := range l {
			l[i] = id()
		}
		return l
	}
	categories := make([]string, maxCategories+1)
	for i := range categories {
		categories[i] = string([]byte{'c', byte('a' + i/26), byte('a' + i%26)})
	}
	for name, a := range map[string]Alerts{
		"no level":        {},
		"info":            {Level: "info"},
		"node":            {Level: "warn", Nodes: []string{"../" + node[3:]}},
		"server":          {Level: "warn", Servers: []string{strings.ToUpper(srv)}},
		"category":        {Level: "warn", Categories: []string{"Servers"}},
		"many nodes":      {Level: "warn", Nodes: ids(maxChosen + 1)},
		"many servers":    {Level: "warn", Servers: ids(maxChosen + 1)},
		"many categories": {Level: "warn", Categories: categories},
	} {
		var apiErr *httpapi.Error
		if err := s.SetAlerts(ctx, alice, a); !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
			t.Errorf("%s: SetAlerts = %v, want a bad request", name, err)
		}
	}

	// Lists are sorted without repeats; a later change replaces the earlier one.
	quiet := time.Date(2026, 10, 8, 18, 0, 0, 0, time.UTC)
	a := Alerts{Level: "error", Only: true, Pinned: true, Nodes: []string{node, node}, Servers: []string{srv},
		Categories: []string{"usage", "servers", "usage"}, QuietUntil: &quiet}
	if err := s.SetAlerts(ctx, alice, a); err != nil {
		t.Fatal(err)
	}
	a.Nodes, a.Categories = []string{node}, []string{"servers", "usage"}
	if p, err := s.Get(ctx, alice); err != nil || !reflect.DeepEqual(p.Alerts, a) {
		t.Fatalf("alerts = %+v, %v, want %+v", p.Alerts, err, a)
	}
	if err := s.SetAlerts(ctx, alice, Alerts{Level: "warn"}); err != nil {
		t.Fatal(err)
	}
	if p, err := s.Get(ctx, alice); err != nil || !reflect.DeepEqual(p.Alerts, noAlerts) {
		t.Fatalf("alerts = %+v, %v, want %+v", p.Alerts, err, noAlerts)
	}
}

func TestWidgetOptions(t *testing.T) {
	db := open(t)
	s, ctx, alice := NewStore(db), t.Context(), addUser(t, db, "alice")
	node := id()
	dashboard := []Widget{
		{ID: "activity", Columns: 2, Options: map[string]string{"count": "20", "level": "warn"}},
		{ID: "resources", Columns: 2, Options: map[string]string{"measure": "memory", "range": "week", "nodes": node + "," + id()}},
		{ID: "networks", Columns: 1, Options: map[string]string{"networks": id()}},
		{ID: "figures", Columns: 3},
	}
	if err := s.SetDashboard(ctx, alice, dashboard); err != nil {
		t.Fatal(err)
	}
	if p, err := s.Get(ctx, alice); err != nil || !reflect.DeepEqual(p.Dashboard, dashboard) {
		t.Fatalf("dashboard = %+v, %v, want %+v", p.Dashboard, err, dashboard)
	}

	// Options and values that a newer version stored, or an older one knew, aren't shown.
	exec(t, db, `UPDATE dashboards SET widgets = ? WHERE user_id = ?`, `[{"id":"activity","columns":2,"options":{"count":"50","level":"error","color":"red"}},`+
		`{"id":"nodes","columns":1,"options":{"nodes":"`+node+`,x"}},{"id":"chart","columns":1,"options":{"count":"5"}}]`, alice)
	want := []Widget{
		{ID: "activity", Columns: 2, Options: map[string]string{"level": "error"}},
		{ID: "nodes", Columns: 1, Options: map[string]string{}},
		{ID: "chart", Columns: 1, Options: map[string]string{}},
	}
	if p, err := s.Get(ctx, alice); err != nil || !reflect.DeepEqual(p.Dashboard, want) {
		t.Fatalf("dashboard = %+v, %v, want %+v", p.Dashboard, err, want)
	}
}

func TestHidden(t *testing.T) {
	db := open(t)
	s, ctx, alice := NewStore(db), t.Context(), addUser(t, db, "alice")
	node, srv := id(), id()
	soon := time.Now().Add(time.Hour).UTC().Round(time.Second)
	hidden := func(keys ...string) []HiddenItem {
		items := make([]HiddenItem, len(keys))
		for i, key := range keys {
			items[i] = HiddenItem{Key: key, Until: soon}
		}
		return items
	}
	many := make([]string, maxHidden+1)
	for i := range many {
		many[i] = "crash/" + id()
	}
	for name, items := range map[string][]HiddenItem{
		"no key":     hidden(""),
		"kind only":  hidden("offline"),
		"capitals":   hidden("offline/" + strings.ToUpper(node)),
		"path":       hidden("offline/../" + node),
		"long":       hidden("offline/" + strings.Repeat("x", 33)),
		"deep":       hidden("usage/" + node + "/" + srv + "/storage/default/x"),
		"twice":      hidden("offline/"+node, "offline/"+node),
		"state":      {{Key: "offline/" + node, State: "AB", Until: soon}},
		"long state": {{Key: "offline/" + node, State: strings.Repeat("a", 17), Until: soon}},
		"too many":   hidden(many...),
	} {
		var apiErr *httpapi.Error
		if err := s.SetHidden(ctx, alice, items); !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
			t.Errorf("%s: SetHidden = %v, want a bad request", name, err)
		}
	}
	check := func(want ...HiddenItem) {
		t.Helper()
		p, err := s.Get(ctx, alice)
		if err != nil || len(p.Hidden) != len(want) {
			t.Fatalf("hidden = %+v, %v, want %+v", p.Hidden, err, want)
		}
		for i, h := range p.Hidden {
			if h.Key != want[i].Key || h.State != want[i].State || !h.Until.Equal(want[i].Until) {
				t.Fatalf("hidden = %+v, want %+v", p.Hidden, want)
			}
		}
	}

	// Items whose time passed are left out, and none is hidden for longer than a week.
	items := []HiddenItem{
		{Key: "offline/" + node, State: "1k2j3h", Until: time.Now().Add(30 * 24 * time.Hour)},
		{Key: "usage/" + node + "//storage/default", Until: soon},
		{Key: "crash/" + srv, Until: time.Now().Add(-time.Minute)},
		{Key: "task/" + id(), Until: soon},
	}
	before := time.Now()
	if err := s.SetHidden(ctx, alice, items); err != nil {
		t.Fatal(err)
	}
	p, err := s.Get(ctx, alice)
	if err != nil || len(p.Hidden) != 3 || p.Hidden[0].Until.Before(before.Add(maxHiding)) || p.Hidden[0].Until.After(time.Now().Add(maxHiding)) {
		t.Fatalf("hidden = %+v, %v", p.Hidden, err)
	}
	check(p.Hidden[0], items[1], items[3])

	// Those that show again by now aren't returned; none at all leaves no row.
	exec(t, db, `UPDATE hidden_items SET items = json_set(items, '$[1].until', ?) WHERE user_id = ?`, time.Now().Add(-time.Second).Format(time.RFC3339), alice)
	check(p.Hidden[0], items[3])
	if err := s.SetHidden(ctx, alice, nil); err != nil {
		t.Fatal(err)
	}
	check()
	var left int
	if err := db.QueryRow(`SELECT COUNT(*) FROM hidden_items`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("%d rows left, %v", left, err)
	}
}

func TestFolded(t *testing.T) {
	db := open(t)
	s, ctx, alice := NewStore(db), t.Context(), addUser(t, db, "alice")
	many := make([]string, maxFolded+1)
	for i := range many {
		many[i] = fmt.Sprint("tag/t", i)
	}
	for name, groups := range map[string][]string{
		"no grouping": {"lobby"},
		"unknown":     {"state/running"},
		"path":        {"tag/../lobby"},
		"space":       {"tag/lobby 1"},
		"long":        {"tag/" + strings.Repeat("x", 33)},
		"twice":       {"tag/lobby", "tag/lobby"},
		"too many":    many,
	} {
		var apiErr *httpapi.Error
		if err := s.SetFolded(ctx, alice, groups); !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
			t.Errorf("%s: SetFolded = %v, want a bad request", name, err)
		}
	}

	// Groups are what servers are grouped by and a value, also none for those without network or tags.
	groups := []string{"network/" + id(), "network/none", "node/" + id(), "type/paper", "tag/größe", "tag/"}
	if err := s.SetFolded(ctx, alice, groups); err != nil {
		t.Fatal(err)
	}
	if p, err := s.Get(ctx, alice); err != nil || !reflect.DeepEqual(p.Folded, groups) {
		t.Fatalf("folded = %v, %v, want %v", p.Folded, err, groups)
	}
	if err := s.SetFolded(ctx, alice, many[:maxFolded]); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFolded(ctx, alice, nil); err != nil {
		t.Fatal(err)
	}
	if p, err := s.Get(ctx, alice); err != nil || p.Folded == nil || len(p.Folded) != 0 {
		t.Fatalf("folded = %v, %v, want none", p.Folded, err)
	}
}

func TestViews(t *testing.T) {
	db := open(t)
	s, ctx, alice, bob := NewStore(db), t.Context(), addUser(t, db, "alice"), addUser(t, db, "bob")
	view := func(name string, search map[string]string) []View { return []View{{Name: name, Search: search}} }
	many := make([]View, maxViews+1)
	for i := range many {
		many[i] = View{Name: fmt.Sprint("View ", i)}
	}
	for name, views := range map[string][]View{
		"no name":       view("", nil),
		"spaces":        view(" Lobbies", nil),
		"long name":     view(strings.Repeat("x", maxViewName+1), nil),
		"line break":    view("Lobbies\nand more", nil),
		"twice":         {{Name: "Lobbies"}, {Name: "Lobbies"}},
		"too many":      many,
		"unknown key":   view("Lobbies", map[string]string{"columns": "cpu"}),
		"empty":         view("Lobbies", map[string]string{"q": ""}),
		"long search":   view("Lobbies", map[string]string{"q": strings.Repeat("x", maxViewQuery+1)}),
		"control":       view("Lobbies", map[string]string{"q": "lobby\x00"}),
		"state":         view("Lobbies", map[string]string{"state": "paused"}),
		"type":          view("Lobbies", map[string]string{"type": "Paper"}),
		"node":          view("Lobbies", map[string]string{"node": "../" + id()[3:]}),
		"network":       view("Lobbies", map[string]string{"network": "all"}),
		"tag":           view("Lobbies", map[string]string{"tag": "lobby 1"}),
		"sort":          view("Lobbies", map[string]string{"sort": "disk"}),
		"order":         view("Lobbies", map[string]string{"order": "up"}),
		"group":         view("Lobbies", map[string]string{"group": "state"}),
		"view":          view("Lobbies", map[string]string{"view": "list"}),
		"one of two ok": view("Lobbies", map[string]string{"tag": "lobby", "view": "list"}),
	} {
		var apiErr *httpapi.Error
		if err := s.SetViews(ctx, alice, views); !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
			t.Errorf("%s: SetViews = %v, want a bad request", name, err)
		}
	}

	// Views keep their order, and those without a search get an empty one.
	views := []View{
		{Name: "Lobbies on node 2", Search: map[string]string{"q": "lobby  eu", "tag": "lobby", "node": id(), "state": "running",
			"type": "paper", "network": "none", "sort": "network", "order": "desc", "group": "tag", "view": "table"}},
		{Name: "Everything", Search: map[string]string{}},
		{Name: strings.Repeat("ü", maxViewName)},
	}
	if err := s.SetViews(ctx, alice, views); err != nil {
		t.Fatal(err)
	}
	views[2].Search = map[string]string{}
	if p, err := s.Get(ctx, alice); err != nil || !reflect.DeepEqual(p.Views, views) {
		t.Fatalf("views = %+v, %v, want %+v", p.Views, err, views)
	}
	if p, err := s.Get(ctx, bob); err != nil || p.Views == nil || len(p.Views) != 0 {
		t.Fatalf("views of another user = %+v, %v", p.Views, err)
	}
	if err := s.SetViews(ctx, alice, many[:maxViews]); err != nil {
		t.Fatal(err)
	}
	if err := s.SetViews(ctx, alice, nil); err != nil {
		t.Fatal(err)
	}
	if p, err := s.Get(ctx, alice); err != nil || p.Views == nil || len(p.Views) != 0 {
		t.Fatalf("views = %+v, %v, want none", p.Views, err)
	}
}
