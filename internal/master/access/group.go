package access

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/httpapi"
)

// AdminGroup is the ID of the built-in Administrators group.
const AdminGroup = "administrators"

const maxTargets = 200

var (
	errGroupNotFound = httpapi.Errorf(http.StatusNotFound, "Group not found. It may have been deleted.")
	serverPattern    = regexp.MustCompile(`^[a-z2-7]{26}$`)
)

// Group gives its members permissions.
type Group struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Builtin is set for the Administrators group, which has every permission.
	Builtin     bool         `json:"builtin"`
	Permissions []Permission `json:"permissions"`
	// AllServers lets the node and server permissions apply everywhere instead of only
	// to the targets.
	AllServers bool      `json:"allServers"`
	Targets    []Target  `json:"targets"`
	Members    []int64   `json:"members"`
	CreatedAt  time.Time `json:"createdAt"`
}

// GroupInput is a new or changed group.
type GroupInput struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Permissions []Permission `json:"permissions"`
	AllServers  bool         `json:"allServers"`
	Targets     []Target     `json:"targets"`
}

// grants returns the permissions the group gives its members.
func (g Group) grants() Grants {
	if g.Builtin {
		return Admin()
	}
	var grants Grants
	for _, p := range g.Permissions {
		grants.add(p, g.AllServers, g.Targets)
	}
	return grants
}

type Service struct{ db *sql.DB }

func NewService(db *sql.DB) *Service { return &Service{db: db} }

// Grants returns the permissions of a user.
func (s *Service) Grants(ctx context.Context, userID int64) (Grants, error) {
	groups, err := s.load(ctx, "", userID)
	var g Grants
	for _, group := range groups {
		g.merge(group.grants())
	}
	return g, err
}

// Groups returns all groups, the Administrators first.
func (s *Service) Groups(ctx context.Context) ([]Group, error) { return s.load(ctx, "", 0) }

func (s *Service) Group(ctx context.Context, id string) (Group, error) {
	groups, err := s.load(ctx, id, 0)
	if err == nil && len(groups) == 0 {
		err = errGroupNotFound
	}
	if err != nil {
		return Group{}, err
	}
	return groups[0], nil
}

func (s *Service) createGroup(ctx context.Context, in GroupInput) (Group, error) {
	id := strings.ToLower(rand.Text())
	return s.saveGroup(ctx, id, in, func(tx *sql.Tx, perms []byte) (sql.Result, error) {
		return tx.ExecContext(ctx, `INSERT INTO user_groups (id, name, description, permissions, all_servers, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			id, in.Name, in.Description, perms, in.AllServers, time.Now().Unix())
	})
}

func (s *Service) updateGroup(ctx context.Context, id string, in GroupInput) (Group, error) {
	return s.saveGroup(ctx, id, in, func(tx *sql.Tx, perms []byte) (sql.Result, error) {
		return tx.ExecContext(ctx, `UPDATE user_groups SET name = ?, description = ?, permissions = ?, all_servers = ? WHERE id = ?`,
			in.Name, in.Description, perms, in.AllServers, id)
	})
}

func (s *Service) saveGroup(ctx context.Context, id string, in GroupInput, write func(*sql.Tx, []byte) (sql.Result, error)) (Group, error) {
	perms, err := json.Marshal(in.Permissions)
	if err != nil {
		return Group{}, err
	}
	err = database.InTx(ctx, s.db, func(tx *sql.Tx) error {
		res, err := write(tx, perms)
		if err == nil && database.RowsAffected(res) == 0 {
			err = errGroupNotFound
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `DELETE FROM group_targets WHERE group_id = ?`, id)
		}
		for _, t := range in.Targets {
			if err == nil {
				_, err = tx.ExecContext(ctx, `INSERT INTO group_targets (group_id, node_id, server_id) VALUES (?, ?, ?)`, id, t.NodeID, t.ServerID)
			}
		}
		return err
	})
	if err != nil {
		return Group{}, constraint(err, in.Name)
	}
	return s.Group(ctx, id)
}

func (s *Service) deleteGroup(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM user_groups WHERE id = ?`, id)
	if err == nil && database.RowsAffected(res) == 0 {
		err = errGroupNotFound
	}
	return err
}

// check validates a group and adds the permissions that the chosen ones require.
func (in *GroupInput) check() error {
	in.Name, in.Description = strings.TrimSpace(in.Name), strings.TrimSpace(in.Description)
	if in.Targets == nil || in.AllServers {
		in.Targets = []Target{}
	}
	switch {
	case in.Name == "" || len(in.Name) > 64:
		return httpapi.Errorf(http.StatusBadRequest, "Enter a name with up to 64 characters.")
	case len(in.Description) > 500:
		return httpapi.Errorf(http.StatusBadRequest, "Enter a description with up to 500 characters.")
	case len(in.Targets) > maxTargets:
		return httpapi.Errorf(http.StatusBadRequest, "Choose up to %d nodes and servers.", maxTargets)
	}
	for _, p := range in.Permissions {
		if _, ok := lookup(p); !ok {
			return httpapi.Errorf(http.StatusBadRequest, "The permission %q doesn't exist.", p)
		}
	}
	for _, t := range in.Targets {
		if t.ServerID != "" && !serverPattern.MatchString(t.ServerID) {
			return httpapi.Errorf(http.StatusBadRequest, "Choose servers that exist.")
		}
	}
	in.Permissions = append([]Permission{}, withRequired(in.Permissions)...)
	slices.SortFunc(in.Targets, func(a, b Target) int { return strings.Compare(a.NodeID+a.ServerID, b.NodeID+b.ServerID) })
	in.Targets = slices.Compact(in.Targets)
	return nil
}

func constraint(err error, name string) error {
	switch msg := err.Error(); {
	case strings.Contains(msg, "UNIQUE"):
		return httpapi.Errorf(http.StatusConflict, "A group named %q exists already.", name)
	case strings.Contains(msg, "FOREIGN KEY"):
		return httpapi.Errorf(http.StatusBadRequest, "Choose nodes and users that exist.")
	}
	return err
}

// load reads one group if id is set, the groups of a user if userID is set, or all groups.
func (s *Service) load(ctx context.Context, id string, userID int64) ([]Group, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, description, permissions, all_servers, created_at FROM user_groups
		WHERE ? IN ('', id) AND (? = 0 OR id IN (SELECT group_id FROM group_members WHERE user_id = ?))
		ORDER BY id != ?, name`, id, userID, userID, AdminGroup)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := []Group{}
	index := map[string]int{}
	for rows.Next() {
		g := Group{Targets: []Target{}, Members: []int64{}}
		var perms string
		var created int64
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &perms, &g.AllServers, &created); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(perms), &g.Permissions); err != nil {
			return nil, err
		}
		// Permissions that a later version removed are ignored.
		g.Permissions = slices.DeleteFunc(append([]Permission{}, g.Permissions...), func(p Permission) bool { _, ok := lookup(p); return !ok })
		g.Builtin, g.CreatedAt = g.ID == AdminGroup, time.Unix(created, 0)
		index[g.ID] = len(groups)
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	err = s.each(ctx, `SELECT group_id, node_id, server_id FROM group_targets ORDER BY rowid`, func(rows *sql.Rows) error {
		var groupID string
		var t Target
		err := rows.Scan(&groupID, &t.NodeID, &t.ServerID)
		if i, ok := index[groupID]; ok {
			groups[i].Targets = append(groups[i].Targets, t)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return groups, s.each(ctx, `SELECT group_id, user_id FROM group_members ORDER BY user_id`, func(rows *sql.Rows) error {
		var groupID string
		var member int64
		err := rows.Scan(&groupID, &member)
		if i, ok := index[groupID]; ok {
			groups[i].Members = append(groups[i].Members, member)
		}
		return err
	})
}

func (s *Service) each(ctx context.Context, query string, scan func(*sql.Rows) error) error {
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// members returns the IDs of the users in a group.
func (s *Service) members(ctx context.Context, groupID string) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id FROM group_members WHERE group_id = ?`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// setGroups replaces the groups of a user.
func (s *Service) setGroups(ctx context.Context, userID int64, groupIDs []string) error {
	err := database.InTx(ctx, s.db, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM group_members WHERE user_id = ?`, userID)
		for _, id := range groupIDs {
			if err == nil {
				_, err = tx.ExecContext(ctx, `INSERT INTO group_members (group_id, user_id) VALUES (?, ?)`, id, userID)
			}
		}
		return err
	})
	if err != nil && strings.Contains(err.Error(), "FOREIGN KEY") {
		err = errGroupNotFound
	}
	return err
}

// InGroups reports whether a user is a member of one of the groups.
func (s *Service) InGroups(ctx context.Context, userID int64, groupIDs []string) (bool, error) {
	ids, err := json.Marshal(groupIDs)
	var in bool
	if err == nil {
		err = s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM group_members WHERE user_id = ? AND group_id IN (SELECT value FROM json_each(?)))`,
			userID, ids).Scan(&in)
	}
	return in, err
}

// MakeAdmin adds a user to the Administrators, e.g. the first user created on the command line.
func (s *Service) MakeAdmin(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO group_members (group_id, user_id) VALUES (?, ?)`, AdminGroup, userID)
	return err
}

// Forget removes a deleted server from the scopes of the groups.
func (s *Service) Forget(ctx context.Context, nodeID, serverID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM group_targets WHERE node_id = ? AND server_id = ?`, nodeID, serverID)
	return err
}

// Move keeps a server that moved to another node in the scopes of the groups that name it.
func (s *Service) Move(ctx context.Context, serverID, from, to string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE group_targets SET node_id = ? WHERE node_id = ? AND server_id = ?`, to, from, serverID)
	return err
}
