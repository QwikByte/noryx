package usage

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/httpapi"
)

// Measures of servers and nodes that thresholds apply to.
const (
	// CPU is the CPU time in use, in percent of a server's limit, or of all cores of its node
	// if it has none, and of all cores of a node.
	CPU = "cpu"
	// Memory is the memory in use, without the page cache, in percent of a server's limit,
	// including Java's overhead, and of a node's memory.
	Memory = "memory"
	// TPS are the ticks per second of a server over the last minute, which warn below their
	// threshold. Only Paper and its forks except Folia tell them.
	TPS = "tps"
	// Storage is the space in use of each storage location of a node, in percent.
	Storage = "storage"
)

var (
	serverMeasures = []string{CPU, Memory, TPS}
	nodeMeasures   = []string{CPU, Memory, Storage}
)

// maxMinutes is the longest a value has to last before it warns: a day.
const maxMinutes = 24 * 60

// Threshold warns about a measure once it has reached Value for Minutes, or fallen below it
// for TPS. Off turns it off.
type Threshold struct {
	Value   float64 `json:"value"`
	Minutes int     `json:"minutes"`
	Off     bool    `json:"off"`
}

// crossed tells whether a value of the measure is beyond the threshold.
func (t Threshold) crossed(measure string, value float64) bool {
	if measure == TPS {
		return value < t.Value
	}
	return value >= t.Value
}

// Thresholds are the thresholds of a node or server by measure.
type Thresholds map[string]Threshold

// Defaults are the thresholds of the master's settings, which apply to nodes and servers
// unless they have their own.
type Defaults struct {
	Servers Thresholds `json:"servers"`
	Nodes   Thresholds `json:"nodes"`
}

// DefaultThresholds apply until the settings change them.
func DefaultThresholds() Defaults {
	return Defaults{
		Servers: Thresholds{CPU: {90, 10, false}, Memory: {95, 5, false}, TPS: {15, 5, false}},
		Nodes:   Thresholds{CPU: {90, 10, false}, Memory: {90, 5, false}, Storage: {90, 1, false}},
	}
}

// Clone returns a copy that shares no map with d.
func (d Defaults) Clone() Defaults { return Defaults{maps.Clone(d.Servers), maps.Clone(d.Nodes)} }

// Validate checks that the defaults have a valid threshold for every measure.
func (d Defaults) Validate() error {
	if len(d.Servers) != len(serverMeasures) || len(d.Nodes) != len(nodeMeasures) {
		return httpapi.Errorf(http.StatusBadRequest, "Enter a threshold for every measure of servers and nodes.")
	}
	return cmp.Or(httpapi.Within("servers", d.Servers.validate(serverMeasures)), httpapi.Within("nodes", d.Nodes.validate(nodeMeasures)))
}

// validate checks that the thresholds are of the given measures and within their ranges.
func (t Thresholds) validate(measures []string) error {
	for measure, th := range t {
		limit := 100.0
		if measure == TPS {
			limit = 20
		}
		switch {
		case !slices.Contains(measures, measure):
			return httpapi.Errorf(http.StatusBadRequest, "Servers have thresholds of CPU, memory and ticks per second, nodes of CPU, memory and storage.")
		case !(th.Value >= 1 && th.Value <= limit):
			return httpapi.FieldErrorf(http.StatusBadRequest, measure, "Enter thresholds of ticks per second from 1 to 20, and the others from 1 to 100 %%.")
		case th.Minutes < 0 || th.Minutes > maxMinutes:
			return httpapi.FieldErrorf(http.StatusBadRequest, measure, "Enter how long a value has to last from 0 to %d minutes.", maxMinutes)
		}
	}
	return nil
}

// target is a node, or a server of it.
type target struct{ node, server string }

// effective tells the thresholds that apply: the defaults, unless nodes and servers have
// their own.
type effective struct {
	defaults Defaults
	own      map[target]Thresholds
}

func (e effective) of(nodeID, serverID string) Thresholds {
	t := maps.Clone(e.defaults.Servers)
	if serverID == "" {
		t = maps.Clone(e.defaults.Nodes)
	}
	maps.Copy(t, e.own[target{nodeID, serverID}])
	return t
}

// thresholds returns the thresholds that apply now.
func (s *Store) thresholds(ctx context.Context) (effective, error) {
	e := effective{s.conf.Thresholds(), map[target]Thresholds{}}
	rows, err := s.db.QueryContext(ctx, `SELECT node_id, server_id, thresholds FROM usage_thresholds`)
	if err != nil {
		return e, err
	}
	defer rows.Close()
	for rows.Next() {
		var k target
		var value []byte
		var t Thresholds
		if err := rows.Scan(&k.node, &k.server, &value); err != nil {
			return e, err
		}
		if err := json.Unmarshal(value, &t); err != nil {
			return e, err
		}
		e.own[k] = t
	}
	return e, rows.Err()
}

// Own returns the thresholds that a node, or a server of it, has instead of the defaults.
func (s *Store) Own(ctx context.Context, nodeID, serverID string) (Thresholds, error) {
	t := Thresholds{}
	var value []byte
	err := s.db.QueryRowContext(ctx, `SELECT thresholds FROM usage_thresholds WHERE node_id = ? AND server_id = ?`, nodeID, serverID).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return t, nil
	}
	if err == nil {
		err = json.Unmarshal(value, &t)
	}
	return t, err
}

// SetOwn validates and stores the thresholds that a node, or a server of it, has instead of
// the defaults; without any, the defaults apply again.
func (s *Store) SetOwn(ctx context.Context, nodeID, serverID string, t Thresholds) error {
	measures := serverMeasures
	if serverID == "" {
		measures = nodeMeasures
	}
	if err := t.validate(measures); err != nil {
		return err
	}
	if len(t) == 0 {
		_, err := s.db.ExecContext(ctx, `DELETE FROM usage_thresholds WHERE node_id = ? AND server_id = ?`, nodeID, serverID)
		return err
	}
	value, err := json.Marshal(t)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO usage_thresholds (node_id, server_id, thresholds) VALUES (?, ?, ?)
		ON CONFLICT (node_id, server_id) DO UPDATE SET thresholds = excluded.thresholds`, nodeID, serverID, value)
	return err
}

// exists fails unless the node exists, and the server on it if one is named, so that
// thresholds of neither stay.
func (s *Store) exists(ctx context.Context, nodeID, serverID string) error {
	if _, err := s.nodes.Get(ctx, nodeID); err != nil || serverID == "" {
		return err
	}
	conn, err := s.nodes.Conn(ctx, nodeID)
	if err != nil {
		return err
	}
	res, err := noryxv1.NewServerServiceClient(conn).ListServers(ctx, &noryxv1.ListServersRequest{})
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(res.GetServers(), func(srv *noryxv1.Server) bool { return srv.GetId() == serverID }) {
		return httpapi.Errorf(http.StatusNotFound, "Server not found.")
	}
	return nil
}
