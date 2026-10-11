package player

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/network"
)

const (
	lobby = "lobbylobbylobbylobbylobbya"
	game  = "gamegamegamegamegamegamega"
	proxy = "proxyproxyproxyproxyproxya"
)

type config struct{ off bool }

func (c *config) SeenPlayers() bool { return !c.off }

func (*config) SeenRetention() time.Duration { return 30 * 24 * time.Hour }

func newSightings(t *testing.T) *Sightings {
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, id := range []string{"n1", "n2"} {
		if _, err := db.Exec(`INSERT INTO nodes (id, name, address, created_at) VALUES (?, ?, 'host:7443', 0)`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	return NewSightings(db, &config{})
}

func online(id string, names ...string) *noryxv1.ServerStats {
	return &noryxv1.ServerStats{Id: id, Running: true, Players: &noryxv1.Players{Online: uint32(len(names)), Names: names}} //nolint:gosec // few
}

func everything(network.Ref) bool { return true }

func TestSightings(t *testing.T) {
	s, ctx := newSightings(t), t.Context()
	start := time.Date(2026, 10, 7, 23, 58, 0, 0, time.UTC)
	record := func(at time.Time, servers ...*noryxv1.ServerStats) {
		t.Helper()
		if err := s.Record(ctx, "n1", at, servers); err != nil {
			t.Fatal(err)
		}
	}
	// Alex plays on the lobby for 3 minutes, across midnight, then on the game. Invalid and
	// repeated names, and the players of proxies, aren't recorded.
	for i := range 3 {
		proxied := &noryxv1.ServerStats{Id: proxy, Running: true, Proxy: true, Players: &noryxv1.Players{Names: []string{"Alex", "Steve"}}}
		record(start.Add(time.Duration(i)*time.Minute), online(lobby, "Alex", "alex", "@a", "§aAlex", "a b"), proxied)
	}
	record(start.Add(10*time.Minute), online(game, "ALEX", ".Bedrock_Kid"))

	h, err := s.History(ctx, "alex", everything)
	if err != nil {
		t.Fatal(err)
	}
	if h.Name != "ALEX" || h.Minutes != 4 || !h.FirstSeen.Equal(start) || !h.LastSeen.Equal(start.Add(10*time.Minute)) {
		t.Fatalf("history = %+v", h)
	}
	if len(h.Servers) != 2 || h.Servers[0].ServerID != game || h.Servers[1].ServerID != lobby || h.Servers[1].Minutes != 3 {
		t.Fatalf("servers = %+v", h.Servers)
	}
	if len(h.Days) != 2 || h.Days[0] != (Day{"2026-10-07", 2}) || h.Days[1] != (Day{"2026-10-08", 2}) {
		t.Fatalf("days = %+v", h.Days)
	}

	// Only the servers a user may see count.
	onLobby := func(ref network.Ref) bool { return ref.ServerID == lobby }
	if h, _ := s.History(ctx, "Alex", onLobby); h.Minutes != 3 || len(h.Servers) != 1 {
		t.Fatalf("history on the lobby = %+v", h)
	}
	players, total, err := s.SeenPlayers(ctx, "", MaxSeen, onLobby)
	if err != nil || total != 1 || players[0].Name != "Alex" || players[0].Minutes != 3 {
		t.Fatalf("seen on the lobby = %+v, %d, %v", players, total, err)
	}
	if players, total, _ := s.SeenPlayers(ctx, "", 1, everything); total != 2 || len(players) != 1 || players[0].Name != ".Bedrock_Kid" {
		t.Fatalf("seen = %+v, %d", players, total)
	}
	// Searches match parts of names, and underscores only as they are.
	for query, want := range map[string]int{"LEX": 1, "_kid": 1, "%": 0, "_": 1, "x_": 0} {
		if _, total, _ := s.SeenPlayers(ctx, query, MaxSeen, everything); total != want {
			t.Errorf("search %q found %d, want %d", query, total, want)
		}
	}
	if h, _ := s.History(ctx, "Nobody", everything); h.Name != "Nobody" || len(h.Servers) != 0 || h.Minutes != 0 {
		t.Fatalf("history of nobody = %+v", h)
	}

	// Sightings move with their server and go with it.
	if err := s.Move(ctx, lobby, "n1", "n2"); err != nil {
		t.Fatal(err)
	}
	if h, _ := s.History(ctx, "Alex", everything); h.Servers[1].NodeID != "n2" {
		t.Fatalf("moved = %+v", h.Servers)
	}
	if err := s.Forget(ctx, "n2", lobby); err != nil {
		t.Fatal(err)
	}
	if h, _ := s.History(ctx, "Alex", everything); h.Minutes != 1 || len(h.Servers) != 1 {
		t.Fatalf("forgotten = %+v", h)
	}

	// Days that ended before the retention began go.
	s.pruned = time.Time{}
	record(start.Add(32*24*time.Hour), online(game, "Steve"))
	if players, total, _ := s.SeenPlayers(ctx, "", MaxSeen, everything); total != 1 || players[0].Name != "Steve" {
		t.Fatalf("seen after the retention = %+v", players)
	}

	// Once the settings turn recording off, nobody new is noted, and what was can be deleted.
	s.conf.(*config).off = true
	record(start.Add(32*24*time.Hour+time.Minute), online(game, "Steve", "Notch"))
	if h, _ := s.History(ctx, "Steve", everything); h.Minutes != 1 {
		t.Fatalf("recorded while off = %+v", h)
	}
	if err := s.DeleteAll(ctx); err != nil {
		t.Fatal(err)
	}
	if _, total, _ := s.SeenPlayers(ctx, "", MaxSeen, everything); total != 0 {
		t.Fatalf("%d players seen after deleting them", total)
	}
}

// A compromised agent that makes up names fills at most maxPerDay rows of a day.
func TestSightingsLimits(t *testing.T) {
	s, ctx := newSightings(t), t.Context()
	now := time.Now()
	var servers []*noryxv1.ServerStats
	for server := range maxPerDay/maxNames + 1 {
		names := make([]string, 2*maxNames)
		for i := range names {
			names[i] = fmt.Sprintf("p%d_%d", server, i)
		}
		servers = append(servers, online(fmt.Sprintf("%026d", server), names...))
	}
	if err := s.Record(ctx, "n1", now, servers); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM player_sightings`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != maxPerDay {
		t.Fatalf("recorded %d rows, want %d", rows, maxPerDay)
	}
	// Players already seen that day still count their minutes, new ones wait for the next day.
	if err := s.Record(ctx, "n1", now, []*noryxv1.ServerStats{online(servers[0].GetId(), "p0_0", "Newcomer")}); err != nil {
		t.Fatal(err)
	}
	known, _ := s.History(ctx, "p0_0", everything)
	newcomer, _ := s.History(ctx, "Newcomer", everything)
	if known.Minutes != 2 || newcomer.Minutes != 0 {
		t.Fatalf("minutes of a known player = %d, of a new one %d", known.Minutes, newcomer.Minutes)
	}
}
