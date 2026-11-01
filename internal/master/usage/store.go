// Package usage keeps a history of what nodes, their servers and datastores use, from the
// measurements of the agents: each minute for a week, and the averages of each hour for as long
// as the settings say. It serves it to the panel along with the latest measurement. It warns
// when nodes and servers use too much for a while, as thresholds of the settings, or of a node
// or server itself, tell.
package usage

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"time"

	"google.golang.org/grpc"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/node"
)

const (
	sampleInterval = time.Minute
	// retention is how long the minutes are kept; the hours are kept as long as the settings say.
	retention    = 7 * 24 * time.Hour
	hourSeconds  = int64(time.Hour / time.Second)
	statsTimeout = 10 * time.Second
	// maxServers is the most servers of a node recorded per sample, far more than a node runs, so
	// that a compromised agent can't fill the database.
	maxServers = 500
)

// serverPattern matches the IDs of servers, random base32 in lowercase.
var serverPattern = regexp.MustCompile(`^[a-z2-7]{26}$`)

// Ranges are the time spans a history covers, with the step its averages are taken over. Those
// beyond the week of the minutes come from the hours, as far as the settings keep them.
var Ranges = map[string]struct{ Span, Step time.Duration }{
	"day":     {24 * time.Hour, 5 * time.Minute},
	"week":    {retention, 30 * time.Minute},
	"month":   {30 * 24 * time.Hour, 2 * time.Hour},
	"quarter": {90 * 24 * time.Hour, 6 * time.Hour},
	"year":    {365 * 24 * time.Hour, 24 * time.Hour},
}

// Nodes provides the nodes and connections to their agents.
type Nodes interface {
	List(ctx context.Context) ([]node.Node, error)
	Get(ctx context.Context, id string) (node.Node, error)
	Conn(ctx context.Context, nodeID string) (grpc.ClientConnInterface, error)
}

// Config provides the settings of the master that concern usage. They can change at any time.
type Config interface {
	// Thresholds are the default thresholds of nodes and servers.
	Thresholds() Defaults
	// UsageRetention is how long the averages of each hour are kept.
	UsageRetention() time.Duration
}

// Observer learns of the running servers of every measurement that the store records, e.g. to
// note which players were online. Their IDs are valid, each once, and at most maxServers.
type Observer interface {
	Record(ctx context.Context, nodeID string, at time.Time, servers []*noryxv1.ServerStats) error
}

// Store keeps what nodes and servers used, and the measures that are beyond their thresholds.
type Store struct {
	db        *sql.DB
	nodes     Nodes
	conf      Config
	observers []Observer
	watchers  []Watcher
	averaged  int64 // the end of the hours that sample averaged last, which it does once an hour

	mu        sync.Mutex
	crossings map[key]*crossing
}

func NewStore(db *sql.DB, nodes Nodes, conf Config, observers ...Observer) *Store {
	return &Store{db: db, nodes: nodes, conf: conf, observers: observers, crossings: map[key]*crossing{}}
}

// Watcher learns of every measurement like an Observer, with what the agent told of its node,
// e.g. its CPU cores, to compare the servers with their thresholds as warnings do.
type Watcher interface {
	Measured(ctx context.Context, nodeID string, at time.Time, stats *noryxv1.GetStatsResponse, servers []*noryxv1.ServerStats)
}

// Watch adds a watcher of the measurements, e.g. the triggers of workflows, before Run.
func (s *Store) Watch(w Watcher) { s.watchers = append(s.watchers, w) }

// Run records the latest measurement of every agent each minute until ctx ends.
func (s *Store) Run(ctx context.Context) {
	t := time.NewTicker(sampleInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.sample(ctx, now)
		}
	}
}

// sample records the latest measurement of every agent, and checks it against the thresholds
// unless they can't be read.
func (s *Store) sample(ctx context.Context, now time.Time) {
	nodes, err := s.nodes.List(ctx)
	if err != nil {
		slog.Warn("Can't record the usage of the nodes", logging.Nodes, "err", err)
		return
	}
	th, thErr := s.thresholds(ctx)
	if thErr != nil {
		slog.Warn("Can't read the thresholds of usage", logging.Usage, "err", thErr)
	}
	enrolled := map[string]bool{}
	var wg sync.WaitGroup
	for _, n := range nodes {
		if n.EnrolledAt == nil {
			continue
		}
		enrolled[n.ID] = true
		wg.Go(func() {
			stats, err := s.Latest(ctx, n.ID)
			if err == nil {
				err = s.add(ctx, n.ID, now, stats)
			}
			if err != nil { // offline nodes are left out
				slog.Debug("Can't record the usage of a node", logging.Nodes, logging.KeyNode, n.ID, "err", err)
				return
			}
			if thErr == nil {
				s.checkNode(ctx, n.ID, now, stats, th)
			}
			servers := recorded(stats)
			for _, o := range s.observers {
				if err := o.Record(ctx, n.ID, now, servers); err != nil {
					slog.Warn("Can't record a measurement of a node", logging.Nodes, logging.KeyNode, n.ID, "err", err)
				}
			}
			for _, w := range s.watchers {
				w.Measured(ctx, n.ID, now, stats, servers)
			}
		})
	}
	wg.Wait()
	s.forget(func(k key) bool { return !enrolled[k.node] })
	if hour := now.Truncate(time.Hour).Unix(); hour > s.averaged {
		if err := s.average(ctx, hour); err != nil {
			slog.Warn("Can't average the usage of the last hours", logging.Nodes, "err", err)
		} else {
			s.averaged = hour
		}
	}
	minutes, hours := now.Add(-retention).Unix(), now.Add(-s.conf.UsageRetention()).Unix()
	for _, old := range []struct {
		sql    string
		before int64
	}{
		{`DELETE FROM usage_samples WHERE time < ?`, minutes}, {`DELETE FROM datastore_usage WHERE time < ?`, minutes},
		{`DELETE FROM usage_hours WHERE time < ?`, hours}, {`DELETE FROM datastore_usage_hours WHERE time < ?`, hours},
	} {
		if _, err := s.db.ExecContext(ctx, old.sql, old.before); err != nil {
			slog.Warn("Can't delete old usage", logging.Nodes, "err", err)
		}
	}
}

// hourly are the tables of the minutes and of their hours, and how an hour is averaged: like a
// step of the charts, with the most players and connections and the largest size of the data.
var hourly = []struct{ minutes, hours, average string }{
	{"usage_samples", "usage_hours", `
		INSERT OR REPLACE INTO usage_hours (time, node_id, server_id, samples, cpu_millis, memory_bytes, net_received, net_sent, disk_bytes, players, tps)
		SELECT ?1, node_id, server_id, COUNT(*), AVG(cpu_millis), AVG(memory_bytes), AVG(net_received), AVG(net_sent), MAX(disk_bytes), MAX(players), AVG(tps)
		FROM usage_samples WHERE time >= ?1 AND time < ?1 + ?2 GROUP BY node_id, server_id`},
	{"datastore_usage", "datastore_usage_hours", `
		INSERT OR REPLACE INTO datastore_usage_hours (time, datastore_id, samples, cpu_millis, memory_bytes, connections, disk_bytes)
		SELECT ?1, datastore_id, COUNT(*), AVG(cpu_millis), AVG(memory_bytes), MAX(connections), MAX(disk_bytes)
		FROM datastore_usage WHERE time >= ?1 AND time < ?1 + ?2 GROUP BY datastore_id`},
}

// average averages each hour that ended by end, from the one after the last hour averaged, or
// else the first hour of the minutes, so that a restart of the master neither loses nor
// doubles an hour. It averages one hour at a time, so that others don't wait long to write.
func (s *Store) average(ctx context.Context, end int64) error {
	for _, h := range hourly {
		var last, first sql.NullInt64
		if err := s.db.QueryRowContext(ctx, `SELECT (SELECT MAX(time) FROM `+h.hours+`), (SELECT MIN(time) FROM `+h.minutes+`)`).Scan(&last, &first); err != nil {
			return err
		}
		next := first.Int64 / hourSeconds * hourSeconds
		if last.Valid {
			next = max(next, last.Int64+hourSeconds)
		}
		for ; first.Valid && next < end; next += hourSeconds {
			if _, err := s.db.ExecContext(ctx, h.average, next, hourSeconds); err != nil {
				return err
			}
		}
	}
	return nil
}

// Latest asks the agent of a node for its latest measurement.
func (s *Store) Latest(ctx context.Context, nodeID string) (*noryxv1.GetStatsResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, statsTimeout)
	defer cancel()
	conn, err := s.nodes.Conn(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	return noryxv1.NewStatsServiceClient(conn).GetStats(ctx, &noryxv1.GetStatsRequest{})
}

// add records the node and its running servers.
func (s *Store) add(ctx context.Context, nodeID string, at time.Time, stats *noryxv1.GetStatsResponse) error {
	return database.InTx(ctx, s.db, func(tx *sql.Tx) error {
		insert, err := tx.PrepareContext(ctx, `
			INSERT INTO usage_samples (time, node_id, server_id, cpu_millis, memory_bytes, net_received, net_sent, disk_bytes, players, tps)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer insert.Close()
		n := stats.GetNode()
		if _, err := insert.ExecContext(ctx, at.Unix(), nodeID, "", n.GetCpuMillis(), n.GetMemoryUsedBytes(), 0, 0, 0, nil, nil); err != nil {
			return err
		}
		for _, srv := range recorded(stats) {
			var players, tps any
			if p := srv.GetPlayers(); p != nil {
				players = p.GetOnline()
			}
			if validTPS(srv.GetTps()) {
				tps = srv.GetTps()
			}
			if _, err := insert.ExecContext(ctx, at.Unix(), nodeID, srv.GetId(), srv.GetCpuMillis(), srv.GetMemoryBytes(),
				srv.GetNetworkReceivedBytesPerSecond(), srv.GetNetworkSentBytesPerSecond(), srv.GetDiskBytes(), players, tps); err != nil {
				return err
			}
		}
		// Only the running datastores that the master has on the node, each once.
		insertDatastore, err := tx.PrepareContext(ctx, `
			INSERT INTO datastore_usage (time, datastore_id, cpu_millis, memory_bytes, connections, disk_bytes)
			SELECT ?, id, ?, ?, ?, ? FROM datastores WHERE id = ? AND node_id = ?`)
		if err != nil {
			return err
		}
		defer insertDatastore.Close()
		seen := map[string]bool{}
		for _, ds := range stats.GetDatastores() {
			if !ds.GetRunning() || seen[ds.GetId()] || len(seen) == maxServers {
				continue
			}
			seen[ds.GetId()] = true
			var connections any
			if ds.Connections != nil {
				connections = ds.GetConnections()
			}
			if _, err := insertDatastore.ExecContext(ctx, at.Unix(), ds.GetCpuMillis(), ds.GetMemoryBytes(), connections, ds.GetDiskBytes(), ds.GetId(), nodeID); err != nil {
				return err
			}
		}
		return nil
	})
}

// recorded returns the running servers of a measurement with valid IDs, each once, and at most
// maxServers of them.
func recorded(stats *noryxv1.GetStatsResponse) []*noryxv1.ServerStats {
	var servers []*noryxv1.ServerStats
	seen := map[string]bool{}
	for _, srv := range stats.GetServers() {
		if srv.GetRunning() && serverPattern.MatchString(srv.GetId()) && !seen[srv.GetId()] && len(servers) < maxServers {
			seen[srv.GetId()] = true
			servers = append(servers, srv)
		}
	}
	return servers
}

// Point is what a node or server used on average during a step of a history.
type Point struct {
	Time            time.Time `json:"time"`
	CPUMillis       float64   `json:"cpuMillis"`
	MemoryBytes     float64   `json:"memoryBytes"`
	NetworkReceived float64   `json:"networkReceived"`
	NetworkSent     float64   `json:"networkSent"`
	DiskBytes       int64     `json:"diskBytes"`
	// Players is the most players during the step.
	Players *int64   `json:"players"`
	TPS     *float64 `json:"tps"`
}

// keeps tells whether the usage is kept for span.
func (s *Store) keeps(span time.Duration) bool {
	return span <= max(retention, s.conf.UsageRetention())
}

// source is where a history over span comes from: the minutes, which are kept for a week, or
// else the hours. Each row weighs as much as the minutes it averages.
func source(minutes, hours string, span time.Duration) string {
	if span <= retention {
		return `(SELECT *, 1 AS samples FROM ` + minutes + `)`
	}
	return hours
}

// History returns the usage of a node, or of one of its servers, since span ago, with the
// averages of each step. Steps in which it didn't run are missing.
func (s *Store) History(ctx context.Context, nodeID, serverID string, span, step time.Duration) ([]Point, error) {
	//nolint:gosec // source names one of two tables
	rows, err := s.db.QueryContext(ctx, `
		SELECT time / ?1 * ?1 AS start, TOTAL(cpu_millis * samples) / SUM(samples), TOTAL(memory_bytes * samples) / SUM(samples),
			TOTAL(net_received * samples) / SUM(samples), TOTAL(net_sent * samples) / SUM(samples), MAX(disk_bytes), MAX(players),
			TOTAL(tps * samples) / SUM(samples) FILTER (WHERE tps IS NOT NULL)
		FROM `+source("usage_samples", "usage_hours", span)+` WHERE node_id = ?2 AND server_id = ?3 AND time >= ?4
		GROUP BY start ORDER BY start`,
		int64(step.Seconds()), nodeID, serverID, time.Now().Add(-span).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	points := []Point{}
	for rows.Next() {
		var p Point
		var start int64
		var players sql.NullInt64
		var tps sql.NullFloat64
		if err := rows.Scan(&start, &p.CPUMillis, &p.MemoryBytes, &p.NetworkReceived, &p.NetworkSent, &p.DiskBytes, &players, &tps); err != nil {
			return nil, err
		}
		p.Time = time.Unix(start, 0)
		if players.Valid {
			p.Players = &players.Int64
		}
		if tps.Valid {
			p.TPS = &tps.Float64
		}
		points = append(points, p)
	}
	return points, rows.Err()
}

// DatastorePoint is what a datastore used on average during a step of its history.
type DatastorePoint struct {
	Time        time.Time `json:"time"`
	CPUMillis   float64   `json:"cpuMillis"`
	MemoryBytes float64   `json:"memoryBytes"`
	// Connections is the most connections during the step.
	Connections *int64 `json:"connections"`
	DiskBytes   int64  `json:"diskBytes"`
}

var errNoDatastore = httpapi.Errorf(http.StatusNotFound, "Datastore not found.")

// DatastoreHistory returns the usage of a datastore since span ago, with the averages of each
// step. Steps in which it didn't run are missing.
func (s *Store) DatastoreHistory(ctx context.Context, id string, span, step time.Duration) ([]DatastorePoint, error) {
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM datastores WHERE id = ?`, id).Scan(&id); errors.Is(err, sql.ErrNoRows) {
		return nil, errNoDatastore
	} else if err != nil {
		return nil, err
	}
	//nolint:gosec // source names one of two tables
	rows, err := s.db.QueryContext(ctx, `
		SELECT time / ?1 * ?1 AS start, TOTAL(cpu_millis * samples) / SUM(samples), TOTAL(memory_bytes * samples) / SUM(samples),
			MAX(connections), MAX(disk_bytes)
		FROM `+source("datastore_usage", "datastore_usage_hours", span)+` WHERE datastore_id = ?2 AND time >= ?3
		GROUP BY start ORDER BY start`,
		int64(step.Seconds()), id, time.Now().Add(-span).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	points := []DatastorePoint{}
	for rows.Next() {
		var p DatastorePoint
		var start int64
		var connections sql.NullInt64
		if err := rows.Scan(&start, &p.CPUMillis, &p.MemoryBytes, &connections, &p.DiskBytes); err != nil {
			return nil, err
		}
		p.Time = time.Unix(start, 0)
		if connections.Valid {
			p.Connections = &connections.Int64
		}
		points = append(points, p)
	}
	return points, rows.Err()
}

// ofServers are the tables that keep something of a server: its minutes, its hours and its
// thresholds.
var ofServers = []string{"usage_samples", "usage_hours", "usage_thresholds"}

// Forget deletes the history and the thresholds of a deleted server, and its warnings.
func (s *Store) Forget(ctx context.Context, nodeID, serverID string) error {
	s.forget(func(k key) bool { return k.node == nodeID && k.server == serverID })
	for _, table := range ofServers {
		//nolint:gosec // one of the tables of ofServers
		if _, err := s.db.ExecContext(ctx, `DELETE FROM `+table+` WHERE node_id = ? AND server_id = ?`, nodeID, serverID); err != nil {
			return err
		}
	}
	return nil
}

// Move keeps the history and the thresholds of a server that moved to another node. Its
// warnings start over there.
func (s *Store) Move(ctx context.Context, serverID, from, to string) error {
	s.forget(func(k key) bool { return k.node == from && k.server == serverID })
	for _, table := range ofServers {
		//nolint:gosec // one of the tables of ofServers
		if _, err := s.db.ExecContext(ctx, `UPDATE OR REPLACE `+table+` SET node_id = ? WHERE node_id = ? AND server_id = ?`, to, from, serverID); err != nil {
			return err
		}
	}
	return nil
}
