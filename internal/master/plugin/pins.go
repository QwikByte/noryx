package plugin

import (
	"cmp"
	"context"
	"database/sql"
	"net/http"

	"github.com/QwikByte/noryx/internal/master/httpapi"
)

// maxPins limits the projects a server keeps at their version, and those it takes in another
// channel than releases, as many as it can have files.
const maxPins = 1000

// pins are what servers keep of their projects: the projects they keep at their version, which
// updates of all plugins leave out, and the channels they take projects in. Servers live on
// their agents, so the master keeps them by node and server.
type pins struct{ db *sql.DB }

// kept is what a server keeps of its projects, by project ID: those it keeps at their version,
// and the channels it takes others in than releases.
type kept struct {
	pinned   map[string]bool
	channels map[string]string
}

// channel returns the channel the server takes a project in.
func (k kept) channel(project string) string { return cmp.Or(k.channels[project], "release") }

// all returns what each server that keeps something keeps, or one server if set.
func (p pins) all(ctx context.Context, only *Ref) (map[Ref]kept, error) {
	query, args := `SELECT node_id, server_id, project_id, '' FROM plugin_pins
		UNION ALL SELECT node_id, server_id, project_id, channel FROM plugin_channels`, []any{}
	if only != nil {
		query, args = `SELECT * FROM (`+query+`) WHERE node_id = ? AND server_id = ?`, []any{only.NodeID, only.ServerID}
	}
	rows, err := p.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	all := map[Ref]kept{}
	for rows.Next() {
		var ref Ref
		var project, channel string
		if err := rows.Scan(&ref.NodeID, &ref.ServerID, &project, &channel); err != nil {
			return nil, err
		}
		k, ok := all[ref]
		if !ok {
			k = kept{pinned: map[string]bool{}, channels: map[string]string{}}
			all[ref] = k
		}
		if channel == "" {
			k.pinned[project] = true
		} else {
			k.channels[project] = channel
		}
	}
	return all, rows.Err()
}

// of returns what a server keeps.
func (p pins) of(ctx context.Context, ref Ref) (kept, error) {
	all, err := p.all(ctx, &ref)
	return all[ref], err
}

// set pins a project of a server, or unpins it.
func (p pins) set(ctx context.Context, ref Ref, project string, pinned bool) error {
	if !ValidProjectID(project) {
		return httpapi.Errorf(http.StatusBadRequest, "invalid project ID")
	}
	if !pinned {
		_, err := p.db.ExecContext(ctx, `DELETE FROM plugin_pins WHERE node_id = ? AND server_id = ? AND project_id = ?`, ref.NodeID, ref.ServerID, project)
		return err
	}
	var count int
	if err := p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_pins WHERE node_id = ? AND server_id = ?`, ref.NodeID, ref.ServerID).Scan(&count); err != nil {
		return err
	}
	if count >= maxPins {
		return httpapi.Errorf(http.StatusConflict, "A server can keep up to %d projects at their version.", maxPins)
	}
	_, err := p.db.ExecContext(ctx, `INSERT OR IGNORE INTO plugin_pins (node_id, server_id, project_id) VALUES (?, ?, ?)`, ref.NodeID, ref.ServerID, project)
	return err
}

// track sets the channel a server takes a project in; releases, the default, aren't kept.
func (p pins) track(ctx context.Context, ref Ref, project, channel string) error {
	switch {
	case !ValidProjectID(project):
		return httpapi.Errorf(http.StatusBadRequest, "invalid project ID")
	case !ValidChannel(channel):
		return errChannel
	case channel == "release":
		_, err := p.db.ExecContext(ctx, `DELETE FROM plugin_channels WHERE node_id = ? AND server_id = ? AND project_id = ?`, ref.NodeID, ref.ServerID, project)
		return err
	}
	var count int
	if err := p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_channels WHERE node_id = ? AND server_id = ? AND project_id != ?`,
		ref.NodeID, ref.ServerID, project).Scan(&count); err != nil {
		return err
	}
	if count >= maxPins {
		return httpapi.Errorf(http.StatusConflict, "A server can take up to %d projects in another channel than releases.", maxPins)
	}
	_, err := p.db.ExecContext(ctx, `INSERT OR REPLACE INTO plugin_channels (node_id, server_id, project_id, channel) VALUES (?, ?, ?, ?)`,
		ref.NodeID, ref.ServerID, project, channel)
	return err
}

// Forget deletes what a deleted server kept.
func (s *Service) Forget(ctx context.Context, nodeID, serverID string) error {
	for _, query := range []string{
		`DELETE FROM plugin_pins WHERE node_id = ? AND server_id = ?`,
		`DELETE FROM plugin_channels WHERE node_id = ? AND server_id = ?`,
	} {
		if _, err := s.pins.db.ExecContext(ctx, query, nodeID, serverID); err != nil {
			return err
		}
	}
	return nil
}

// Move keeps what a server that moved to another node kept.
func (s *Service) Move(ctx context.Context, serverID, from, to string) error {
	for _, query := range []string{
		`UPDATE OR REPLACE plugin_pins SET node_id = ? WHERE node_id = ? AND server_id = ?`,
		`UPDATE OR REPLACE plugin_channels SET node_id = ? WHERE node_id = ? AND server_id = ?`,
	} {
		if _, err := s.pins.db.ExecContext(ctx, query, to, from, serverID); err != nil {
			return err
		}
	}
	return nil
}
