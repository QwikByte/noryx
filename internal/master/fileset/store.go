package fileset

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/httpapi"
)

var errNotFound = httpapi.Errorf(http.StatusNotFound, "File set not found.")

// store keeps the sets in the master's database.
type store struct{ db *sql.DB }

// summaries returns all sets with the paths of their files, sorted by name.
func (s store) summaries(ctx context.Context) ([]Summary, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.id, s.name, s.description, s.version, v.files, v.created_at
		FROM file_sets s JOIN file_set_versions v ON v.set_id = s.id AND v.version = s.version ORDER BY s.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Summary{}
	for rows.Next() {
		var sum Summary
		var files string
		var updated int64
		if err := rows.Scan(&sum.ID, &sum.Name, &sum.Description, &sum.Version, &files, &updated); err != nil {
			return nil, err
		}
		var fs []File
		if err := json.Unmarshal([]byte(files), &fs); err != nil {
			return nil, err
		}
		sum.Paths, sum.UpdatedAt = []string{}, time.Unix(updated, 0)
		for _, f := range fs {
			sum.Paths = append(sum.Paths, f.Path)
		}
		list = append(list, sum)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	targets, err := s.targets(ctx)
	for i := range list {
		list[i].Targets = append([]Target{}, targets[list[i].ID]...)
	}
	return list, err
}

// get returns a set with its newest files, its targets and variables, the names of its
// secrets and its versions.
func (s store) get(ctx context.Context, id string) (Set, error) {
	set := Set{ID: id, Targets: []Target{}, Secrets: []Secret{}, Versions: []Version{}}
	var created int64
	var variables string
	err := s.db.QueryRowContext(ctx, `SELECT name, description, version, variables, created_at FROM file_sets WHERE id = ?`, id).
		Scan(&set.Name, &set.Description, &set.Version, &variables, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return set, errNotFound
	}
	if err == nil {
		err = json.Unmarshal([]byte(variables), &set.Variables)
	}
	if err != nil {
		return set, err
	}
	set.CreatedAt = time.Unix(created, 0)
	v, err := s.version(ctx, id, set.Version)
	if err != nil {
		return set, err
	}
	set.Files = v.Files
	targets, err := s.targets(ctx)
	if err != nil {
		return set, err
	}
	set.Targets = append(set.Targets, targets[id]...)
	rows, err := s.db.QueryContext(ctx, `SELECT name, updated_at FROM file_set_secrets WHERE set_id = ? ORDER BY name`, id)
	if err != nil {
		return set, err
	}
	defer rows.Close()
	for rows.Next() {
		var sec Secret
		var updated int64
		if err := rows.Scan(&sec.Name, &updated); err != nil {
			return set, err
		}
		sec.UpdatedAt = time.Unix(updated, 0)
		set.Secrets = append(set.Secrets, sec)
	}
	if err := rows.Err(); err != nil {
		return set, err
	}
	versions, err := s.db.QueryContext(ctx, `SELECT version, username, created_at FROM file_set_versions WHERE set_id = ? ORDER BY version DESC`, id)
	if err != nil {
		return set, err
	}
	defer versions.Close()
	for versions.Next() {
		var v Version
		var created int64
		if err := versions.Scan(&v.Version, &v.User, &created); err != nil {
			return set, err
		}
		v.CreatedAt = time.Unix(created, 0)
		set.Versions = append(set.Versions, v)
	}
	return set, versions.Err()
}

// version returns a kept version of a set with its files.
func (s store) version(ctx context.Context, id string, version int64) (Version, error) {
	v := Version{Version: version}
	var files string
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT files, username, created_at FROM file_set_versions WHERE set_id = ? AND version = ?`, id, version).
		Scan(&files, &v.User, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return v, httpapi.Errorf(http.StatusNotFound, "This version of the file set isn't kept anymore.")
	}
	if err != nil {
		return v, err
	}
	v.CreatedAt = time.Unix(created, 0)
	return v, json.Unmarshal([]byte(files), &v.Files)
}

// targets returns the targets of all sets, by set.
func (s store) targets(ctx context.Context) (map[string][]Target, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT set_id, kind, value, role FROM file_set_targets ORDER BY kind, value, role`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	targets := map[string][]Target{}
	for rows.Next() {
		var id string
		var t Target
		if err := rows.Scan(&id, &t.Kind, &t.Value, &t.Role); err != nil {
			return nil, err
		}
		targets[id] = append(targets[id], t)
	}
	return targets, rows.Err()
}

// secrets returns the values of the secrets of a set, by name.
func (s store) secrets(ctx context.Context, id string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, value FROM file_set_secrets WHERE set_id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := map[string]string{}
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return nil, err
		}
		values[name] = value
	}
	return values, rows.Err()
}

// save creates a set, if id is new, or changes it: a new version if its files changed,
// and its targets and variables. It fails if the set has a newer version than in.Version.
func (s store) save(ctx context.Context, id string, in Input, user string, create bool) (bool, error) {
	files, err := json.Marshal(in.Files)
	if err != nil {
		return false, err
	}
	variables, err := json.Marshal(in.Variables)
	if err != nil {
		return false, err
	}
	version, changed := int64(1), true
	err = database.InTx(ctx, s.db, func(tx *sql.Tx) error {
		now := time.Now().Unix()
		var err error
		if create {
			_, err = tx.ExecContext(ctx, `INSERT INTO file_sets (id, name, description, version, variables, created_at) VALUES (?, ?, ?, 1, ?, ?)`,
				id, in.Name, in.Description, variables, now)
		} else {
			var current []byte
			err = tx.QueryRowContext(ctx, `
				SELECT s.version, v.files FROM file_sets s JOIN file_set_versions v ON v.set_id = s.id AND v.version = s.version WHERE s.id = ?`, id).
				Scan(&version, &current)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				return errNotFound
			case err != nil:
				return err
			case version != in.Version:
				return httpapi.Errorf(http.StatusConflict, "Someone saved the file set since you opened it. Open it again to see the changes.")
			}
			if changed = string(current) != string(files); changed {
				version++
			}
			_, err = tx.ExecContext(ctx, `UPDATE file_sets SET name = ?, description = ?, version = ?, variables = ? WHERE id = ?`,
				in.Name, in.Description, version, variables, id)
		}
		if err != nil {
			return unique(err, in.Name)
		}
		if changed {
			if _, err := tx.ExecContext(ctx, `INSERT INTO file_set_versions (set_id, version, files, username, created_at) VALUES (?, ?, ?, ?, ?)`,
				id, version, files, user, now); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM file_set_versions WHERE set_id = ? AND version <= ?`, id, version-keepVersions); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM file_set_targets WHERE set_id = ?`, id); err != nil {
			return err
		}
		for _, t := range in.Targets {
			if _, err := tx.ExecContext(ctx, `INSERT INTO file_set_targets (set_id, kind, value, role) VALUES (?, ?, ?, ?)`, id, t.Kind, t.Value, t.Role); err != nil {
				return err
			}
		}
		return nil
	})
	return changed && err == nil, err
}

func (s store) delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM file_sets WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	return nil
}

// setSecret sets the value of a secret of a set that exists; a set has up to max secrets.
func (s store) setSecret(ctx context.Context, id, name, value string, max int) (Secret, error) {
	sec := Secret{Name: name, UpdatedAt: time.Now()}
	return sec, database.InTx(ctx, s.db, func(tx *sql.Tx) error {
		var others int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM file_set_secrets WHERE set_id = ? AND name != ?`, id, name).Scan(&others); err != nil {
			return err
		}
		if others >= max {
			return httpapi.Errorf(http.StatusBadRequest, "A file set has up to %d secrets.", max)
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO file_set_secrets (set_id, name, value, updated_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (set_id, name) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			id, name, value, sec.UpdatedAt.Unix())
		return err
	})
}

func (s store) deleteSecret(ctx context.Context, id, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM file_set_secrets WHERE set_id = ? AND name = ?`, id, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return httpapi.Errorf(http.StatusNotFound, "Secret not found.")
	}
	return nil
}

// ids returns the IDs of all sets.
func (s store) ids(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM file_sets`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func unique(err error, name string) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return httpapi.Errorf(http.StatusConflict, "A file set named %q already exists.", name)
	}
	return err
}
