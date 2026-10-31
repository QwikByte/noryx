package modpack

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// installed is the version of a modpack a server has, the Minecraft and loader version it
// needs, and the files the pack wrote.
type installed struct {
	project, version, number string
	minecraft, loader        string
	files                    map[string]written
}

// written is a file that a pack wrote into a server's data.
type written struct {
	SHA512 string `json:"sha512"`
	// Project is the Modrinth project of a mod. Mods follow the pack, while other files keep
	// what the administrator changed.
	Project string `json:"project,omitempty"`
}

// record returns the files of a pack as a server has them once it is installed.
func (p *Pack) record() map[string]written {
	files := make(map[string]written, len(p.files))
	for _, f := range p.files {
		files[f.path] = written{f.sha512, f.project}
	}
	return files
}

// remember records which version of a pack a server has, and the files the pack wrote.
func (s *Service) remember(ctx context.Context, nodeID, serverID string, p *Pack, files map[string]written) error {
	data, err := json.Marshal(files)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO server_modpacks (node_id, server_id, project, version, number, minecraft, loader, files)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (node_id, server_id) DO UPDATE SET project = excluded.project, version = excluded.version,
		number = excluded.number, minecraft = excluded.minecraft, loader = excluded.loader, files = excluded.files`,
		nodeID, serverID, p.Project, p.Version, p.Number, p.GameVersion, p.LoaderVersion, string(data))
	return err
}

// installed returns the version of a modpack a server has; ok is false for servers that
// weren't created from a pack.
func (s *Service) installed(ctx context.Context, nodeID, serverID string) (i installed, ok bool, err error) {
	var data []byte
	err = s.db.QueryRowContext(ctx, `SELECT project, version, number, minecraft, loader, files FROM server_modpacks WHERE node_id = ? AND server_id = ?`,
		nodeID, serverID).Scan(&i.project, &i.version, &i.number, &i.minecraft, &i.loader, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return i, false, nil
	}
	if err == nil {
		err = json.Unmarshal(data, &i.files)
	}
	return i, err == nil, err
}

// Copy gives a copy of a server the pack of the original, whose files it has.
func (s *Service) Copy(ctx context.Context, nodeID, from, to string) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO server_modpacks (node_id, server_id, project, version, number, minecraft, loader, files)
		SELECT node_id, ?, project, version, number, minecraft, loader, files FROM server_modpacks WHERE node_id = ? AND server_id = ?`, to, nodeID, from)
	return err
}

// Forget forgets the pack of a deleted server.
func (s *Service) Forget(ctx context.Context, nodeID, serverID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM server_modpacks WHERE node_id = ? AND server_id = ?`, nodeID, serverID)
	return err
}

// Move keeps the pack of a server that moved to another node.
func (s *Service) Move(ctx context.Context, serverID, from, to string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE server_modpacks SET node_id = ? WHERE node_id = ? AND server_id = ?`, to, from, serverID)
	return err
}
