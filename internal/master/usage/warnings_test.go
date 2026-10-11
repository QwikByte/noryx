package usage

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/database"
)

// capture makes the default logger log to a buffer for the test, and returns a function that
// returns the entries logged since it was last called.
func capture(t *testing.T) func() []map[string]any {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return func() []map[string]any {
		var entries []map[string]any
		for line := range strings.Lines(buf.String()) {
			var e map[string]any
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				t.Fatal(err)
			}
			entries = append(entries, e)
		}
		buf.Reset()
		return entries
	}
}

// checker samples a node with the lobby on it, minute by minute, and checks what is logged.
type checker struct {
	t      *testing.T
	s      *Store
	th     effective
	start  time.Time
	logged func() []map[string]any
}

func newChecker(t *testing.T) *checker {
	return &checker{t, NewStore(nil, nil, nil), effective{DefaultThresholds(), map[target]Thresholds{}}, time.Now(), capture(t)}
}

// lobbyStats is a node with 4 cores, 8 GiB of memory, of which memoryGiB are used, and the
// lobby with a limit of 2 cores and 1 GiB of memory.
func lobbyStats(cpuMillis uint32, tps float64, memoryGiB uint64) *noryxv1.GetStatsResponse {
	return &noryxv1.GetStatsResponse{
		Node: &noryxv1.NodeStats{CpuMillis: 1000, CpuCount: 4, MemoryUsedBytes: memoryGiB << 30, MemoryTotalBytes: 8 << 30},
		Servers: []*noryxv1.ServerStats{{
			Id: lobby, Running: true, CpuMillis: cpuMillis, CpuLimitMillis: 2000, MemoryBytes: 512 << 20, MemoryLimitBytes: 1 << 30, Tps: tps,
		}},
	}
}

// sample checks a sample at the given minute and fails unless exactly want were logged.
func (c *checker) sample(minute int, stats *noryxv1.GetStatsResponse, storage []*noryxv1.StorageLocation, keepStorage bool, want ...string) {
	c.t.Helper()
	c.s.check("n1", c.start.Add(time.Duration(minute)*time.Minute), readings("n1", stats, storage, c.th), keepStorage)
	var got []string
	for _, e := range c.logged() {
		if e["category"] != "usage" || e["node"] != "n1" {
			c.t.Errorf("minute %d: entry %v isn't about the usage of the node", minute, e)
		}
		got = append(got, e["msg"].(string))
	}
	if !slices.Equal(got, want) {
		c.t.Fatalf("minute %d: logged %q, want %q", minute, got, want)
	}
}

func (c *checker) warnings() []string {
	var ws []string
	for _, w := range c.s.Warnings(func(string, string) bool { return true }) {
		ws = append(ws, w.ServerID+"/"+w.Measure+w.Storage)
	}
	return ws
}

// A measure warns once it was beyond its threshold for the threshold's minutes, and once it is
// fine again; nothing in between, and nothing for values that only come close.
func TestCheckCrossing(t *testing.T) {
	c := newChecker(t)
	for minute := range 10 { // 95 % of the CPU limit, 90 % for 10 minutes by default
		c.sample(minute, lobbyStats(1900, 19, 1), nil, false)
	}
	if ws := c.warnings(); len(ws) != 0 {
		t.Fatalf("warnings before 10 minutes: %v", ws)
	}
	c.sample(10, lobbyStats(1900, 19, 1), nil, false, "A server uses too much CPU")
	c.sample(11, lobbyStats(1950, 19, 1), nil, false)
	ws := c.s.Warnings(func(string, string) bool { return true })
	if len(ws) != 1 || ws[0].ServerID != lobby || ws[0].Measure != CPU || ws[0].Value != 97.5 || !ws[0].Since.Equal(c.start) || ws[0].Threshold.Value != 90 {
		t.Fatalf("warnings = %+v", ws)
	}
	c.sample(12, lobbyStats(500, 19, 1), nil, false, "A server uses less CPU again")
	c.sample(13, lobbyStats(500, 19, 1), nil, false)
	if ws := c.warnings(); len(ws) != 0 {
		t.Fatalf("warnings when fine again: %v", ws)
	}

	// Falling back below the threshold starts over.
	for minute := 20; minute < 25; minute++ {
		c.sample(minute, lobbyStats(1900, 19, 1), nil, false)
	}
	c.sample(25, lobbyStats(1000, 19, 1), nil, false)
	for minute := 26; minute < 36; minute++ {
		c.sample(minute, lobbyStats(1900, 19, 1), nil, false)
	}
	c.sample(36, lobbyStats(1900, 19, 1), nil, false, "A server uses too much CPU")
}

// Ticks per second warn below their threshold. While they are unknown, e.g. as the console
// doesn't answer, a crossing goes on, unless they are unknown for long.
func TestCheckUnknown(t *testing.T) {
	c := newChecker(t)
	c.sample(0, lobbyStats(0, 12, 1), nil, false)
	c.sample(2, lobbyStats(0, 0, 1), nil, false)
	c.sample(4, lobbyStats(0, 14.9, 1), nil, false)
	c.sample(5, lobbyStats(0, 12, 1), nil, false, "A server ticks too slowly")
	c.sample(6, lobbyStats(0, 0, 1), nil, false)
	c.sample(20, lobbyStats(0, 0, 1), nil, false)
	if ws := c.warnings(); !slices.Equal(ws, []string{lobby + "/tps"}) {
		t.Fatalf("warnings while unknown: %v", ws)
	}
	c.sample(21, lobbyStats(0, 20, 1), nil, false, "A server ticks fast enough again")

	// A gap of more than a few minutes starts a crossing over that wasn't warned about.
	c.sample(30, lobbyStats(0, 10, 1), nil, false)
	c.sample(36, lobbyStats(0, 10, 1), nil, false)
	c.sample(40, lobbyStats(0, 10, 1), nil, false)
	c.sample(41, lobbyStats(0, 10, 1), nil, false, "A server ticks too slowly")
}

// Servers and nodes can have their own thresholds, and turn them off.
func TestCheckOwnThresholds(t *testing.T) {
	c := newChecker(t)
	c.th.own[target{"n1", lobby}] = Thresholds{CPU: {Value: 50}, TPS: {Value: 15, Off: true}}
	c.th.own[target{"n1", ""}] = Thresholds{Memory: {Value: 50, Minutes: 1}}
	c.sample(0, lobbyStats(1200, 5, 5), nil, false, "A server uses too much CPU")
	c.sample(1, lobbyStats(1200, 5, 5), nil, false, "A node is running out of memory")
	if ws := c.warnings(); !slices.Equal(ws, []string{lobby + "/cpu", "/memory"}) {
		t.Fatalf("warnings = %v", ws)
	}

	// Turned off, a warning ends without an entry; it isn't fine, only no longer checked. The
	// ticks per second have the default threshold again.
	c.th.own[target{"n1", lobby}] = Thresholds{CPU: {Value: 50, Off: true}}
	c.sample(2, lobbyStats(1200, 5, 5), nil, false)
	if ws := c.warnings(); !slices.Equal(ws, []string{"/memory"}) {
		t.Fatalf("warnings = %v", ws)
	}
	// So does the warning of a server that stopped.
	for minute := 3; minute < 7; minute++ {
		c.sample(minute, lobbyStats(1200, 5, 5), nil, false)
	}
	c.sample(7, lobbyStats(1200, 5, 5), nil, false, "A server ticks too slowly")
	stopped := lobbyStats(0, 0, 5)
	stopped.Servers[0].Running = false
	c.sample(8, stopped, nil, false)
	if ws := c.warnings(); !slices.Equal(ws, []string{"/memory"}) {
		t.Fatalf("warnings after the lobby stopped = %v", ws)
	}
}

// Each storage location of a node is checked, and kept while the locations can't be read.
func TestCheckStorage(t *testing.T) {
	c := newChecker(t)
	storage := []*noryxv1.StorageLocation{
		{Name: "default", FreeBytes: 5 << 30, TotalBytes: 100 << 30},
		{Name: "ssd", FreeBytes: 50 << 30, TotalBytes: 100 << 30},
	}
	c.sample(0, lobbyStats(0, 20, 1), storage, false)
	c.sample(1, lobbyStats(0, 20, 1), storage, false, "A storage location of a node is almost full")
	c.sample(2, lobbyStats(0, 20, 1), nil, true)
	if ws := c.warnings(); !slices.Equal(ws, []string{"/storagedefault"}) {
		t.Fatalf("warnings = %v", ws)
	}
	c.sample(3, lobbyStats(0, 20, 1), storage[1:], false)
	if ws := c.warnings(); len(ws) != 0 {
		t.Fatalf("warnings of a removed location = %v", ws)
	}
}

// Warnings only go to users who may see their node or server.
func TestWarningsHandler(t *testing.T) {
	c := newChecker(t)
	c.th.own[target{"n1", lobby}] = Thresholds{CPU: {Value: 10}}
	c.th.own[target{"n1", ""}] = Thresholds{CPU: {Value: 10}}
	c.sample(0, lobbyStats(1000, 20, 1), nil, false, "A node uses too much CPU", "A server uses too much CPU")
	get := func(g access.Grants) []Warning {
		rec := httptest.NewRecorder()
		NewHandler(c.s).warnings(rec, httptest.NewRequestWithContext(access.WithGrants(t.Context(), g), http.MethodGet, "/api/usage/warnings", nil))
		var ws []Warning
		if err := json.Unmarshal(rec.Body.Bytes(), &ws); err != nil {
			t.Fatal(err)
		}
		return ws
	}
	if ws := get(access.Admin()); len(ws) != 2 {
		t.Errorf("an administrator sees %+v", ws)
	}
	if ws := get(access.Grants{}); ws == nil || len(ws) != 0 {
		t.Errorf("a user without permissions sees %+v", ws)
	}
}

// Thresholds of nodes and servers are validated, follow a server that moves, and go away
// with it.
func TestOwnThresholds(t *testing.T) {
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
	s, ctx := NewStore(db, nil, config{DefaultThresholds()}), t.Context()
	for name, bad := range map[string]Thresholds{
		"storage of a server": {Storage: {Value: 90}},
		"unknown measure":     {"disk": {Value: 90}},
		"no value":            {CPU: {}},
		"above 100 %":         {Memory: {Value: 101}},
		"TPS above 20":        {TPS: {Value: 21}},
		"a day and a minute":  {CPU: {Value: 90, Minutes: 24*60 + 1}},
		"negative minutes":    {CPU: {Value: 90, Minutes: -1}},
	} {
		if err := s.SetOwn(ctx, "n1", lobby, bad); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := s.SetOwn(ctx, "n1", "", Thresholds{TPS: {Value: 15}}); err == nil {
		t.Error("TPS of a node accepted")
	}
	own := Thresholds{CPU: {Value: 95, Minutes: 15}, TPS: {Value: 15, Off: true}}
	if err := s.SetOwn(ctx, "n1", lobby, own); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOwn(ctx, "n1", "", Thresholds{Storage: {Value: 80}}); err != nil {
		t.Fatal(err)
	}
	th, err := s.thresholds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := th.of("n1", lobby); got[CPU] != own[CPU] || !got[TPS].Off || got[Memory] != DefaultThresholds().Servers[Memory] {
		t.Errorf("thresholds of the lobby = %v", got)
	}
	if got := th.of("n1", ""); got[Storage].Value != 80 || got[CPU] != DefaultThresholds().Nodes[CPU] {
		t.Errorf("thresholds of the node = %v", got)
	}

	if err := s.Move(ctx, lobby, "n1", "n2"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Own(ctx, "n2", lobby); err != nil || got[CPU] != own[CPU] {
		t.Fatalf("moved thresholds = %v, %v", got, err)
	}
	if err := s.Forget(ctx, "n2", lobby); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Own(ctx, "n2", lobby); err != nil || len(got) != 0 {
		t.Fatalf("forgotten thresholds = %v, %v", got, err)
	}
	// Without any, the defaults apply again.
	if err := s.SetOwn(ctx, "n1", "", Thresholds{}); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM usage_thresholds`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("%d rows left: %v", rows, err)
	}

	// The defaults need every measure.
	d := DefaultThresholds()
	delete(d.Nodes, Storage)
	if d.Validate() == nil {
		t.Error("defaults without storage accepted")
	}
}

type config struct{ defaults Defaults }

func (c config) Thresholds() Defaults { return c.defaults.Clone() }

func (config) UsageRetention() time.Duration { return 30 * 24 * time.Hour }
