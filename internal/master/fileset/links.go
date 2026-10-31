package fileset

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"

	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/database"
)

// Forget removes the values of variables for a deleted server from all sets.
func (s *Service) Forget(ctx context.Context, _, serverID string) error {
	return s.store.dropValues(ctx, func(v Value) bool { return v.Kind == KindServer && v.Scope == serverID })
}

// Move changes nothing: values are for a server by its ID, wherever it is.
func (s *Service) Move(context.Context, string, string, string) error { return nil }

// Prune removes the targets and the values of variables of networks that were deleted, which
// sets no longer apply to anyway.
func (s *Service) Prune(ctx context.Context) error {
	res, err := s.store.db.ExecContext(ctx, `DELETE FROM file_set_targets WHERE kind = ? AND value NOT IN (SELECT id FROM networks)`, KindNetwork)
	if err != nil {
		return err
	}
	if n := database.RowsAffected(res); n > 0 {
		slog.Info("Removed deleted networks from the targets of file sets", logging.Files, "targets", n)
	}
	exists, err := database.IDs(ctx, s.store.db, `SELECT id FROM networks`)
	if err != nil {
		return err
	}
	return s.store.dropValues(ctx, func(v Value) bool { return v.Kind == KindNetwork && !exists[v.Scope] })
}

// dropValues removes the values of variables that drop chooses from all sets.
func (s store) dropValues(ctx context.Context, drop func(Value) bool) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, variables FROM file_sets`)
	if err != nil {
		return err
	}
	type set struct{ id, name, data string }
	changed := map[set][]Variable{}
	for rows.Next() {
		var id, name, data string
		var vars []Variable
		if err := rows.Scan(&id, &name, &data); err != nil {
			rows.Close()
			return err
		}
		if json.Unmarshal([]byte(data), &vars) != nil {
			continue
		}
		dropped := false
		for i := range vars {
			n := len(vars[i].Values)
			vars[i].Values = slices.DeleteFunc(vars[i].Values, drop)
			dropped = dropped || len(vars[i].Values) != n
		}
		if dropped {
			changed[set{id, name, data}] = vars
		}
	}
	err = errors.Join(rows.Err(), rows.Close())
	for set, vars := range changed {
		data, merr := json.Marshal(vars)
		if merr == nil {
			// A set saved in the meantime keeps what it was saved with.
			var res sql.Result
			if res, merr = s.db.ExecContext(ctx, `UPDATE file_sets SET variables = ? WHERE id = ? AND CAST(variables AS TEXT) = ?`, string(data), set.id, set.data); merr == nil {
				if database.RowsAffected(res) > 0 {
					slog.Info("Removed values of variables for servers or networks that are gone", logging.Files, "file_set", set.name)
				}
			}
		}
		err = errors.Join(err, merr)
	}
	return err
}
