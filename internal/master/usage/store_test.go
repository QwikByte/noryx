package usage

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/node"
)

const (
	lobby   = "lobbylobbylobbylobbylobbya"
	stopped = "stoppedstoppedstoppedstopp"
)

func TestHistory(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, id := range []string{"n1", "n2"} {
		if _, err := db.Exec(`INSERT INTO nodes (id, name, address, created_at) VALUES (?, ?, 'host:7443', 0)`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	s, ctx := NewStore(db, nil, nil), t.Context()
	step := 5 * time.Minute
	start := time.Now().Truncate(step).Add(-step)
	for i, players := range []uint32{3, 7} {
		stats := &noryxv1.GetStatsResponse{
			Node: &noryxv1.NodeStats{CpuMillis: 2000, MemoryUsedBytes: 4 << 30},
			Servers: []*noryxv1.ServerStats{
				{Id: lobby, Running: true, CpuMillis: uint32(500 * (i + 1)), MemoryBytes: 1 << 30, DiskBytes: 100, Tps: 19.5, Players: &noryxv1.Players{Online: players}}, //nolint:gosec // small
				{Id: stopped, DiskBytes: 100},
			},
		}
		if err := s.add(ctx, "n1", start.Add(time.Duration(i)*time.Minute), stats); err != nil {
			t.Fatal(err)
		}
	}

	// Samples of a step are averaged, players are the most of the step.
	points, err := s.History(ctx, "n1", lobby, time.Hour, step)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 || points[0].CPUMillis != 750 || *points[0].Players != 7 || *points[0].TPS != 19.5 || !points[0].Time.Equal(start) {
		t.Fatalf("points = %+v", points)
	}
	if points, _ := s.History(ctx, "n1", stopped, time.Hour, step); len(points) != 0 {
		t.Fatalf("stopped server recorded: %+v", points)
	}
	if points, _ := s.History(ctx, "n1", "", time.Hour, step); len(points) != 1 || points[0].Players != nil || points[0].MemoryBytes != 4<<30 {
		t.Fatalf("node = %+v", points)
	}

	// The history moves with the server and goes away with it.
	if err := s.Move(ctx, lobby, "n1", "n2"); err != nil {
		t.Fatal(err)
	}
	if points, _ := s.History(ctx, "n2", lobby, time.Hour, step); len(points) != 1 {
		t.Fatalf("moved history = %+v", points)
	}
	if err := s.Forget(ctx, "n2", lobby); err != nil {
		t.Fatal(err)
	}
	if points, _ := s.History(ctx, "n2", lobby, time.Hour, step); len(points) != 0 {
		t.Fatalf("forgotten history = %+v", points)
	}
}

type noNodes struct{ Nodes }

func (noNodes) List(context.Context) ([]node.Node, error) { return nil, nil }

// The hours that ended are averaged once, also after a restart, weighing as many minutes as
// they have, and kept beyond the minutes for as long as the settings say.
func TestHours(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, id := range []string{"n1", "n2"} {
		if _, err := db.Exec(`INSERT INTO nodes (id, name, address, created_at) VALUES (?, ?, 'host:7443', 0)`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	s, ctx := NewStore(db, noNodes{}, config{DefaultThresholds()}), t.Context()
	start := time.Now().Truncate(2 * time.Hour).Add(-2 * time.Hour)
	for _, m := range []struct {
		at           time.Duration
		cpu, players uint32
		disk         uint64
		tps          float64
	}{{0, 1000, 3, 100, 20}, {30 * time.Minute, 2000, 5, 200, 18}, {time.Hour, 4000, 1, 150, 16}} {
		stats := &noryxv1.GetStatsResponse{Node: &noryxv1.NodeStats{}, Servers: []*noryxv1.ServerStats{
			{Id: lobby, Running: true, CpuMillis: m.cpu, DiskBytes: m.disk, Tps: m.tps, Players: &noryxv1.Players{Online: m.players}},
		}}
		if err := s.add(ctx, "n1", start.Add(m.at), stats); err != nil {
			t.Fatal(err)
		}
	}
	end := time.Now().Truncate(time.Hour).Unix()
	for range 2 { // as after a restart
		if err := NewStore(db, nil, nil).average(ctx, end); err != nil {
			t.Fatal(err)
		}
	}
	var hours int
	if err := db.QueryRow(`SELECT COUNT(*) FROM usage_hours WHERE server_id = ?`, lobby).Scan(&hours); err != nil || hours != 2 {
		t.Fatalf("%d hours of the lobby, %v", hours, err)
	}

	// Beyond a week, the history comes from the hours, also once the minutes are gone.
	if _, err := db.Exec(`DELETE FROM usage_samples`); err != nil {
		t.Fatal(err)
	}
	month := Ranges["month"]
	points, err := s.History(ctx, "n1", lobby, month.Span, month.Step)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 || points[0].CPUMillis != 7000.0/3 || *points[0].Players != 5 || points[0].DiskBytes != 200 || *points[0].TPS != 18 || !points[0].Time.Equal(start) {
		t.Fatalf("points = %+v", points)
	}

	// The hours move with the server and go away with it.
	if err := s.Move(ctx, lobby, "n1", "n2"); err != nil {
		t.Fatal(err)
	}
	if points, _ := s.History(ctx, "n2", lobby, month.Span, month.Step); len(points) != 1 {
		t.Fatalf("moved hours = %+v", points)
	}
	if err := s.Forget(ctx, "n2", lobby); err != nil {
		t.Fatal(err)
	}
	if points, _ := s.History(ctx, "n2", lobby, month.Span, month.Step); len(points) != 0 {
		t.Fatalf("forgotten hours = %+v", points)
	}

	// Hours older than the settings keep them go.
	old := time.Now().Add(-31 * 24 * time.Hour).Truncate(time.Hour).Unix()
	if _, err := db.Exec(`INSERT INTO usage_hours (time, node_id, server_id, samples, cpu_millis, memory_bytes, net_received, net_sent, disk_bytes)
		VALUES (?, 'n1', '', 60, 0, 0, 0, 0, 0)`, old); err != nil {
		t.Fatal(err)
	}
	s.sample(ctx, time.Now())
	if err := db.QueryRow(`SELECT COUNT(*) FROM usage_hours WHERE time <= ?`, old).Scan(&hours); err != nil || hours != 0 {
		t.Fatalf("%d old hours left, %v", hours, err)
	}
	if !s.keeps(month.Span) || s.keeps(Ranges["quarter"].Span) {
		t.Error("the ranges kept don't follow the settings")
	}
}

func TestAddLimits(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO nodes (id, name, address, created_at) VALUES ('n1', 'n1', 'host:7443', 0)`); err != nil {
		t.Fatal(err)
	}
	// A compromised agent reports invalid, repeated and far too many servers.
	stats := &noryxv1.GetStatsResponse{Node: &noryxv1.NodeStats{}}
	for _, id := range []string{"../x", strings.Repeat("a", 10000), lobby, lobby} {
		stats.Servers = append(stats.Servers, &noryxv1.ServerStats{Id: id, Running: true})
	}
	for i := range 2 * maxServers {
		id := strings.Map(func(r rune) rune { return 'a' + r - '0' }, fmt.Sprintf("%026d", i)) // digits as letters
		stats.Servers = append(stats.Servers, &noryxv1.ServerStats{Id: id, Running: true})
	}
	if err := NewStore(db, nil, nil).add(t.Context(), "n1", time.Now(), stats); err != nil {
		t.Fatal(err)
	}
	var servers, lobbies int
	if err := db.QueryRow(`SELECT COUNT(*), COUNT(*) FILTER (WHERE server_id = ?) FROM usage_samples WHERE server_id != ''`, lobby).Scan(&servers, &lobbies); err != nil {
		t.Fatal(err)
	}
	if servers != maxServers || lobbies != 1 {
		t.Errorf("recorded %d servers, the lobby %d times", servers, lobbies)
	}
}

// The master records the running datastores it has on the node that measured them, each once,
// and forgets their history with them.
func TestDatastoreHistory(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, sql := range []string{
		`INSERT INTO nodes (id, name, address, created_at) VALUES ('n1', 'n1', 'host:7443', 0), ('n2', 'n2', 'host:7443', 0)`,
		`INSERT INTO networks (id, name, proxy_node_id, proxy_server_id, proxy_type, forwarding_secret, created_at) VALUES ('net', 'Main', 'n1', 'proxy', 'velocity', '', 0)`,
		`INSERT INTO datastores (id, network_id, node_id, name, engine, version, memory_mb, cpu_millis, storage, created_at)
			VALUES ('main', 'net', 'n1', 'main', 'mariadb', '11.8', 512, 0, '', 0), ('other', 'net', 'n2', 'other', 'postgres', '18', 512, 0, '', 0)`,
	} {
		if _, err := db.Exec(sql); err != nil {
			t.Fatal(err)
		}
	}
	s, ctx := NewStore(db, nil, nil), t.Context()
	step := 5 * time.Minute
	start := time.Now().Truncate(step).Add(-step)
	for i, connections := range []*uint32{new(uint32(3)), nil, new(uint32(5))} {
		stats := &noryxv1.GetStatsResponse{Node: &noryxv1.NodeStats{}, Datastores: []*noryxv1.DatastoreStats{
			{Id: "main", Running: true, CpuMillis: uint32(100 * (i + 1)), MemoryBytes: 1 << 30, Connections: connections, DiskBytes: 100}, //nolint:gosec // small
			{Id: "main", Running: true, CpuMillis: 9000},
			{Id: "other", Running: true},
			{Id: "unknown", Running: true},
		}}
		if err := s.add(ctx, "n1", start.Add(time.Duration(i)*time.Minute), stats); err != nil {
			t.Fatal(err)
		}
	}
	points, err := s.DatastoreHistory(ctx, "main", time.Hour, step)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 || points[0].CPUMillis != 200 || *points[0].Connections != 5 || points[0].DiskBytes != 100 || !points[0].Time.Equal(start) {
		t.Fatalf("points = %+v", points)
	}
	if points, err := s.DatastoreHistory(ctx, "other", time.Hour, step); err != nil || len(points) != 0 {
		t.Fatalf("a datastore of another node recorded: %+v, %v", points, err)
	}
	if _, err := s.DatastoreHistory(ctx, "unknown", time.Hour, step); err == nil {
		t.Fatal("history of an unknown datastore")
	}
	if _, err := db.Exec(`DELETE FROM datastores WHERE id = 'main'`); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := db.QueryRow(`SELECT COUNT(*) FROM datastore_usage`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("%d samples left, %v", left, err)
	}
}
