// Package tag keeps the tags of servers, e.g. lobby or bedwars, by which the panel finds and
// groups them, and their notes, e.g. what a server is for. Servers live on their agents, so
// the master keeps both.
package tag

import (
	"context"
	"database/sql"
	"net/http"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/httpapi"
)

const (
	MaxPerServer = 10
	maxLength    = 24
	maxNotes     = 500
)

// Server is a server on a node.
type Server struct {
	NodeID   string `json:"nodeId"`
	ServerID string `json:"serverId"`
}

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// All returns the sorted tags of all servers that have some.
func (s *Store) All(ctx context.Context) (map[Server][]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT node_id, server_id, tag FROM server_tags ORDER BY tag`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tags := map[Server][]string{}
	for rows.Next() {
		var srv Server
		var tag string
		if err := rows.Scan(&srv.NodeID, &srv.ServerID, &tag); err != nil {
			return nil, err
		}
		tags[srv] = append(tags[srv], tag)
	}
	return tags, rows.Err()
}

// Change adds and removes tags of servers, all or none of them.
func (s *Store) Change(ctx context.Context, servers []Server, add, remove []string) error {
	add, err := Normalize(add)
	if err == nil {
		remove, err = Normalize(remove)
	}
	if err != nil {
		return err
	}
	return database.InTx(ctx, s.db, func(tx *sql.Tx) error {
		for _, srv := range servers {
			for _, tag := range remove {
				if _, err := tx.ExecContext(ctx, `DELETE FROM server_tags WHERE node_id = ? AND server_id = ? AND tag = ?`, srv.NodeID, srv.ServerID, tag); err != nil {
					return err
				}
			}
			for _, tag := range add {
				if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO server_tags (node_id, server_id, tag) VALUES (?, ?, ?)`, srv.NodeID, srv.ServerID, tag); err != nil {
					return err
				}
			}
		}
		// Only the changed servers can have too many, as all others had few enough before.
		var tooMany bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM server_tags GROUP BY node_id, server_id HAVING COUNT(*) > ?)`, MaxPerServer).Scan(&tooMany)
		switch {
		case err != nil:
			return err
		case tooMany:
			return httpapi.Errorf(http.StatusBadRequest, "A server can have up to %d tags.", MaxPerServer)
		}
		return nil
	})
}

// Notes returns the notes of all servers that have some.
func (s *Store) Notes(ctx context.Context) (map[Server]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT node_id, server_id, notes FROM server_notes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	notes := map[Server]string{}
	for rows.Next() {
		var srv Server
		var text string
		if err := rows.Scan(&srv.NodeID, &srv.ServerID, &text); err != nil {
			return nil, err
		}
		notes[srv] = text
	}
	return notes, rows.Err()
}

// SetNotes replaces the notes of a server; empty notes delete them. Notes are text of up to
// maxNotes characters, which the panel shows as text only.
func (s *Store) SetNotes(ctx context.Context, srv Server, notes string) error {
	notes = strings.TrimSpace(strings.ReplaceAll(notes, "\r\n", "\n"))
	if utf8.RuneCountInString(notes) > maxNotes || strings.ContainsFunc(notes, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) {
		return httpapi.Errorf(http.StatusBadRequest, "Notes are text of up to %d characters.", maxNotes)
	}
	var err error
	if notes == "" {
		_, err = s.db.ExecContext(ctx, `DELETE FROM server_notes WHERE node_id = ? AND server_id = ?`, srv.NodeID, srv.ServerID)
	} else {
		_, err = s.db.ExecContext(ctx, `INSERT INTO server_notes (node_id, server_id, notes) VALUES (?, ?, ?)
			ON CONFLICT (node_id, server_id) DO UPDATE SET notes = excluded.notes`, srv.NodeID, srv.ServerID, notes)
	}
	return err
}

// Copy gives a copy of a server the tags and notes of the original.
func (s *Store) Copy(ctx context.Context, from, to Server) error {
	return s.exec(ctx, []any{to.NodeID, to.ServerID, from.NodeID, from.ServerID},
		`INSERT OR IGNORE INTO server_tags (node_id, server_id, tag) SELECT ?, ?, tag FROM server_tags WHERE node_id = ? AND server_id = ?`,
		`INSERT OR IGNORE INTO server_notes (node_id, server_id, notes) SELECT ?, ?, notes FROM server_notes WHERE node_id = ? AND server_id = ?`)
}

// Forget deletes the tags and notes of a deleted server.
func (s *Store) Forget(ctx context.Context, nodeID, serverID string) error {
	return s.exec(ctx, []any{nodeID, serverID},
		`DELETE FROM server_tags WHERE node_id = ? AND server_id = ?`,
		`DELETE FROM server_notes WHERE node_id = ? AND server_id = ?`)
}

// Move keeps the tags and notes of a server that moved to another node.
func (s *Store) Move(ctx context.Context, serverID, from, to string) error {
	return s.exec(ctx, []any{to, from, serverID},
		`UPDATE server_tags SET node_id = ? WHERE node_id = ? AND server_id = ?`,
		`UPDATE server_notes SET node_id = ? WHERE node_id = ? AND server_id = ?`)
}

// exec runs statements with the same arguments, all or none of them.
func (s *Store) exec(ctx context.Context, args []any, statements ...string) error {
	return database.InTx(ctx, s.db, func(tx *sql.Tx) error {
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement, args...); err != nil {
				return err
			}
		}
		return nil
	})
}

// Normalize trims and lowercases tags, and sorts them without duplicates. Tags consist of
// letters, digits, - and _, so that they read well in the panel and in addresses.
func Normalize(tags []string) ([]string, error) {
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		valid := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' }
		if tag == "" || utf8.RuneCountInString(tag) > maxLength || strings.ContainsFunc(tag, func(r rune) bool { return !valid(r) }) {
			return nil, httpapi.Errorf(http.StatusBadRequest, "Tags have up to %d letters, digits, - and _.", maxLength)
		}
		out = append(out, tag)
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}
