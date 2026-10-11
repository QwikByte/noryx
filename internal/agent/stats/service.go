// Package stats measures what the node and its servers use, every few seconds, for the
// panel to show and the master to keep a history of.
package stats

import (
	"cmp"
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/properties"
	"github.com/QwikByte/noryx/internal/agent/runtime"
)

const (
	// defaultInterval is the time between two measurements unless SetInterval chose another,
	// and the longest that a measurement takes.
	defaultInterval = 5 * time.Second
	diskInterval    = 5 * time.Minute
	probeTimeout    = 2 * time.Second
	// consoleRetry is how long the agent waits before it asks a console again that failed,
	// as the server logs every attempt to connect.
	consoleRetry = time.Minute
	// maxListed is the most players listed of a server.
	maxListed = 1000
)

var (
	formatting = regexp.MustCompile(`§.`)
	tpsPattern = regexp.MustCompile(`TPS from last 1m, 5m, 15m: \*?([0-9.]+)`)
	listCounts = regexp.MustCompile(`There are (\d+)(?: of a max of |/)(\d+) players`)
)

// Service measures the usage every few seconds and serves the latest measurement.
type Service struct {
	noryxv1.UnimplementedStatsServiceServer
	rt runtime.Runtime

	measuring  sync.Mutex // one measurement at a time; guards host, servers and datastores
	host       cpuTimes   // of the measurement before
	servers    map[string]*serverState
	datastores map[string]*counters

	mu     sync.Mutex
	latest *noryxv1.GetStatsResponse
	disks  map[string]uint64 // size of the data of each server

	interval atomic.Int64 // the time.Duration between two measurements, 0 for defaultInterval
}

// serverState is what the service keeps about a server between measurements.
type serverState struct {
	counters
	consoleRetry time.Time
	offline      *bool // whether it runs in offline mode, read once per run
}

// counters are the counters of a container's measurement before, which rates need.
type counters struct {
	usage runtime.Usage
	at    time.Time
}

// rates returns the CPU time used and the bytes received and sent per second since the
// measurement before, if any, and keeps u for the next one. Counters start again when the
// container restarts.
func (c *counters) rates(u runtime.Usage, now time.Time) (cpuMillis uint32, received, sent uint64) {
	prev, elapsed := c.usage, now.Sub(c.at)
	if !c.at.IsZero() && elapsed > 0 && u.CPUTime >= prev.CPUTime && u.NetRxBytes >= prev.NetRxBytes && u.NetTxBytes >= prev.NetTxBytes {
		cpuMillis = uint32((u.CPUTime - prev.CPUTime) * 1000 / elapsed) //nolint:gosec // at most the cores
		received, sent = perSecond(u.NetRxBytes-prev.NetRxBytes, elapsed), perSecond(u.NetTxBytes-prev.NetTxBytes, elapsed)
	}
	c.usage, c.at = u, now
	return cpuMillis, received, sent
}

func NewService(rt runtime.Runtime) *Service {
	return &Service{rt: rt, servers: map[string]*serverState{}, datastores: map[string]*counters{}, disks: map[string]uint64{}}
}

// SetInterval sets the time between two measurements, from the next one on.
func (s *Service) SetInterval(d time.Duration) { s.interval.Store(int64(d)) }

func (s *Service) every() time.Duration {
	return cmp.Or(time.Duration(s.interval.Load()), defaultInterval)
}

// GetStats returns the latest measurement, or measures now if there is none of the last
// few seconds, e.g. while the agent starts.
func (s *Service) GetStats(ctx context.Context, _ *noryxv1.GetStatsRequest) (*noryxv1.GetStatsResponse, error) {
	if latest, fresh := s.get(); fresh {
		return latest, nil
	}
	s.measuring.Lock()
	defer s.measuring.Unlock()
	if _, fresh := s.get(); !fresh { // unless a concurrent call measured meanwhile
		s.measureLocked(ctx)
	}
	latest, _ := s.get()
	return latest, nil
}

// get returns the latest measurement and whether it is of the last few seconds.
func (s *Service) get() (*noryxv1.GetStatsResponse, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latest, s.latest != nil && time.Since(time.Unix(s.latest.GetTimeUnix(), 0)) <= 2*s.every()
}

// Run measures the usage every few seconds, and the size of the servers' data every few
// minutes, until ctx ends.
func (s *Service) Run(ctx context.Context) {
	go repeat(ctx, func() time.Duration { return diskInterval }, s.measureDisks)
	repeat(ctx, s.every, s.measure)
	s.measuring.Lock()
	defer s.measuring.Unlock()
	for _, state := range s.servers {
		state.reset()
	}
}

// repeat calls fn now and then again each interval() after it started, until ctx ends.
func repeat(ctx context.Context, interval func() time.Duration, fn func(context.Context)) {
	for {
		start := time.Now()
		fn(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(start.Add(interval()))):
		}
	}
}

func (s *Service) measure(ctx context.Context) {
	s.measuring.Lock()
	defer s.measuring.Unlock()
	s.measureLocked(ctx)
}

func (s *Service) measureLocked(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, defaultInterval)
	defer cancel()
	now := time.Now()
	res := &noryxv1.GetStatsResponse{TimeUnix: now.Unix(), Node: s.measureHost()}
	list, err := s.rt.List(ctx)
	if err != nil {
		slog.Debug("Can't list the servers to measure them", "err", err)
	}
	// In parallel, as the probes wait for the servers; each touches only its own state.
	res.Servers = make([]*noryxv1.ServerStats, len(list))
	var wg sync.WaitGroup
	for i, srv := range list {
		state := s.servers[srv.ID]
		if state == nil {
			state = &serverState{}
			s.servers[srv.ID] = state
		}
		wg.Go(func() { res.Servers[i] = state.measure(ctx, s.rt, srv, now) })
	}
	datastores, dsErr := s.rt.ListDatastores(ctx)
	if dsErr != nil {
		slog.Debug("Can't list the datastores to measure them", "err", dsErr)
	}
	res.Datastores = make([]*noryxv1.DatastoreStats, len(datastores))
	for i, ds := range datastores {
		c := s.datastores[ds.ID]
		if c == nil {
			c = &counters{}
			s.datastores[ds.ID] = c
		}
		wg.Go(func() { res.Datastores[i] = c.measureDatastore(ctx, s.rt, ds, now) })
	}
	wg.Wait()
	for id, state := range s.servers {
		if err == nil && !slices.ContainsFunc(list, func(srv runtime.Server) bool { return srv.ID == id }) {
			state.reset()
			delete(s.servers, id)
		}
	}
	for id := range s.datastores {
		if dsErr == nil && !slices.ContainsFunc(datastores, func(ds runtime.Datastore) bool { return ds.ID == id }) {
			delete(s.datastores, id)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, srv := range res.Servers {
		srv.DiskBytes = s.disks[srv.Id]
	}
	s.latest = res
}

func (s *Service) measureHost() *noryxv1.NodeStats {
	node := &noryxv1.NodeStats{}
	if t, err := readCPU(); err == nil {
		if prev := s.host; prev.total > 0 && t.total > prev.total && t.busy >= prev.busy {
			node.CpuMillis = uint32((t.busy - prev.busy) * uint64(t.cores) * 1000 / (t.total - prev.total)) //nolint:gosec // at most the cores
		}
		node.CpuCount, s.host = t.cores, t
	}
	node.MemoryUsedBytes, node.MemoryTotalBytes, _ = readMemory()
	return node
}

func (st *serverState) measure(ctx context.Context, rt runtime.Runtime, srv runtime.Server, now time.Time) *noryxv1.ServerStats {
	stats := &noryxv1.ServerStats{Id: srv.ID, Proxy: srv.Type.Proxy()}
	if srv.State == noryxv1.ServerState_SERVER_STATE_STOPPED {
		st.reset()
		return stats
	}
	u, err := rt.Usage(ctx, srv.ID)
	if err != nil {
		st.reset()
		return stats
	}
	stats.CpuMillis, stats.NetworkReceivedBytesPerSecond, stats.NetworkSentBytesPerSecond = st.rates(u, now)
	stats.Running, stats.MemoryBytes, stats.MemoryLimitBytes, stats.CpuLimitMillis = true, u.MemoryBytes, u.MemoryLimit, srv.CPUMillis
	if srv.State != noryxv1.ServerState_SERVER_STATE_RUNNING {
		return stats // starting servers don't answer yet
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	if !srv.Type.Bungee() { // which logs every status request
		stats.Players, _ = ping(ctx, srv.Port)
	}
	if srv.Type.Proxy() {
		return stats
	}
	stats.OfflineMode = !srv.BehindProxy && st.offlineMode(ctx, rt, srv.ID)
	// The ping lists only some players, the console all of them. The console also counts them
	// when the ping fails: servers behind a proxy only listen within the network of their proxy.
	if out, ok := st.command(ctx, rt, srv.ID, now, "minecraft:list"); ok {
		switch players, ok := listed(out); {
		case !ok:
		case stats.Players == nil:
			stats.Players = players
		default:
			stats.Players.Names = players.GetNames()
		}
	}
	// Folia tells the ticks per second of each region instead.
	if srv.Type.Paper() && srv.Type != noryxv1.ServerType_SERVER_TYPE_FOLIA {
		if out, ok := st.command(ctx, rt, srv.ID, now, "tps"); ok {
			if m := tpsPattern.FindStringSubmatch(out); m != nil {
				tps, _ := strconv.ParseFloat(m[1], 64)
				stats.Tps = min(tps, 20)
			}
		}
	}
	return stats
}

// measureDatastore measures what a datastore uses; the size of its data is the runtime's.
func (c *counters) measureDatastore(ctx context.Context, rt runtime.Runtime, ds runtime.Datastore, now time.Time) *noryxv1.DatastoreStats {
	stats := &noryxv1.DatastoreStats{Id: ds.ID, DiskBytes: uint64(max(ds.Size, 0))}
	u, err := rt.DatastoreUsage(ctx, ds.ID)
	if err != nil {
		c.at = time.Time{}
		return stats
	}
	stats.CpuMillis, _, _ = c.rates(u.Usage, now)
	stats.Running, stats.MemoryBytes, stats.MemoryLimitBytes, stats.CpuLimitMillis = true, u.MemoryBytes, u.MemoryLimit, ds.CPUMillis
	if u.Connections >= 0 {
		stats.Connections = new(uint32(u.Connections)) //nolint:gosec // far fewer
	}
	return stats
}

func perSecond(bytes uint64, d time.Duration) uint64 { return uint64(float64(bytes) / d.Seconds()) }

// command runs a console command of a game server and returns its output without
// formatting, or false if the console can't be reached.
func (st *serverState) command(ctx context.Context, rt runtime.Runtime, id string, now time.Time, command string) (string, bool) {
	if now.Before(st.consoleRetry) {
		return "", false
	}
	out, err := rt.SendCommand(ctx, id, command)
	if err != nil {
		slog.Debug("Can't run a command on the console of a server", "server", id, "err", err)
		st.consoleRetry = now.Add(consoleRetry)
		return "", false
	}
	return plain(out), true
}

// listed returns the players in the output of the list command: "There are 2 of a max of
// 20 players online: Alex, Steve", or before Minecraft 1.13 "There are 2/20 players online:"
// with the names on a new line. Without the numbers, it counts the names.
func listed(out string) (*noryxv1.Players, bool) {
	head, names, ok := strings.Cut(out, ":")
	if !ok {
		return nil, false // e.g. an unknown command
	}
	players := &noryxv1.Players{Names: []string{}}
	for _, name := range strings.FieldsFunc(names, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
		if !noryxv1.ValidPlayerName(name) {
			continue
		}
		if players.Online++; len(players.Names) < maxListed {
			players.Names = append(players.Names, name)
		}
	}
	if m := listCounts.FindStringSubmatch(head); m != nil {
		online, _ := strconv.ParseUint(m[1], 10, 32)
		limit, _ := strconv.ParseUint(m[2], 10, 32)
		players.Online, players.Max = uint32(online), uint32(limit)
	}
	return players, true
}

// offlineMode reports whether a running game server is in offline mode, as its
// server.properties tells when first asked; changes apply when it starts again.
func (st *serverState) offlineMode(ctx context.Context, rt runtime.Runtime, id string) bool {
	if st.offline == nil {
		dir, err := rt.Data(ctx, id)
		if err != nil {
			return false
		}
		props, err := properties.Read(dir)
		if err = errors.Join(err, dir.Close()); err != nil {
			return false
		}
		st.offline = new(props["online-mode"] == "false")
	}
	return *st.offline
}

// reset forgets a server that stopped.
func (st *serverState) reset() {
	st.at, st.offline, st.consoleRetry = time.Time{}, nil, time.Time{}
}

// measureDisks measures the size of the data of all servers, which takes a while for
// big worlds.
func (s *Service) measureDisks(ctx context.Context) {
	list, err := s.rt.List(ctx)
	if err != nil {
		return
	}
	sizes := make(map[string]uint64, len(list))
	for _, srv := range list {
		if size, err := s.diskSize(ctx, srv.ID); err == nil {
			sizes[srv.ID] = size
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.disks = sizes
}

func (s *Service) diskSize(ctx context.Context, id string) (uint64, error) {
	dir, err := s.rt.Data(ctx, id)
	if err != nil {
		return 0, err
	}
	defer dir.Close()
	var size uint64
	err = fs.WalkDir(dir.FS(), ".", func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				size += uint64(info.Size()) //nolint:gosec // sizes aren't negative
			}
		}
		return ctx.Err() // folders that can't be read are left out
	})
	return size, err
}

// plain removes the formatting codes of Minecraft from text.
func plain(text string) string { return formatting.ReplaceAllString(text, "") }
