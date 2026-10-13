package schedule

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/tag"
)

const (
	maxTargets = 200

	// Kinds of targets.
	KindServer  = "server" // a server, or all servers of a node
	KindTag     = "tag"
	KindNetwork = "network"
	// Roles of the servers of a network that a target names; empty names all of them.
	RoleServers = "servers"
	RoleProxy   = "proxy"
)

var (
	errNotFound = httpapi.Errorf(http.StatusNotFound, "Not found. It may have been deleted.")
	errTargets  = httpapi.Errorf(http.StatusBadRequest, "Choose 1 to %d nodes, servers, tags or networks.", maxTargets)
	idPattern   = regexp.MustCompile(`^[a-z2-7]{26}$`)
)

// Task runs on its target servers at the times of its schedule. Its settings are those of
// its kind, e.g. what a backup job backs up.
type Task struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Enabled  bool            `json:"enabled"`
	Schedule Schedule        `json:"schedule"`
	Targets  []Target        `json:"targets"`
	Settings json.RawMessage `json:"settings"`
	LastRun  *Run            `json:"lastRun,omitempty"`
	// NextRun is the scheduled time of the next run of an enabled task.
	NextRun   *time.Time `json:"nextRun,omitempty"`
	Running   bool       `json:"running"`
	CreatedAt time.Time  `json:"createdAt"`
	// SavedBy is the user who saved the task last, whose permissions its runs need if its
	// settings need more than the one to manage it, see Needing.
	SavedBy   string `json:"savedBy,omitempty"`
	kind      string
	author    int64 // the ID of SavedBy, 0 if the user was deleted or is disabled
	withdrawn <-chan struct{}
}

// Withdrawn is closed during a run once the task is deleted, or paused while it was enabled,
// so that the run stops waiting, e.g. for players to leave.
func (t Task) Withdrawn() <-chan struct{} { return t.withdrawn }

// Author is the user who saves a task or runs it by hand, with the permissions of the request.
type Author struct {
	ID     int64
	Name   string
	Grants access.Grants
}

// allowed checks that the author has the permissions everywhere.
func (a Author) allowed(perms []access.Permission) error {
	for _, p := range perms {
		if !a.Grants.Has(p) {
			return access.Denied(p)
		}
	}
	return nil
}

// Target names servers of a task: a server, all servers of a node (without ServerID), those
// with a tag, or those of a network. Nodes, tags and networks include the servers they get
// later, as targets are resolved at each run.
type Target struct {
	// Kind is server, tag or network; empty is server, as targets were before the others.
	Kind     string `json:"kind"`
	NodeID   string `json:"nodeId,omitempty"`
	ServerID string `json:"serverId,omitempty"`
	// Value is the tag, or the ID of the network.
	Value string `json:"value,omitempty"`
	// Role chooses the game servers or the proxy of a network; empty is all its servers.
	Role string `json:"role,omitempty"`
}

// Input is a new or changed task.
type Input struct {
	Name     string          `json:"name"`
	Enabled  bool            `json:"enabled"`
	Schedule Schedule        `json:"schedule"`
	Targets  []Target        `json:"targets"`
	Settings json.RawMessage `json:"settings"`
}

func (s *Service) List(ctx context.Context, kind string) ([]Task, error) {
	return s.load(ctx, kind, "")
}

func (s *Service) Get(ctx context.Context, kind, id string) (Task, error) {
	tasks, err := s.load(ctx, kind, id)
	if err == nil && len(tasks) == 0 {
		err = errNotFound
	}
	if err != nil {
		return Task{}, err
	}
	return tasks[0], nil
}

// Covering returns the tasks of a kind whose targets include a server now.
func (s *Service) Covering(ctx context.Context, kind string, srv tag.Server) ([]Task, error) {
	tasks, err := s.List(ctx, kind)
	var g groups
	if err == nil {
		g, err = s.groups(ctx, []Target{{Kind: KindTag}, {Kind: KindNetwork}})
	}
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(tasks, func(t Task) bool { return !g.resolve(t.Targets, nil).covers(srv) }), nil
}

// Create creates a task, which its author saved last.
func (s *Service) Create(ctx context.Context, kind string, in Input, by Author) (Task, error) {
	t, err := s.build(kind, in, by)
	if err != nil {
		return t, err
	}
	t.ID, t.CreatedAt = strings.ToLower(rand.Text()), time.Now()
	return s.save(ctx, t, func(tx *sql.Tx, schedule, settings []byte) (sql.Result, error) {
		return tx.ExecContext(ctx, `INSERT INTO tasks (id, kind, name, enabled, schedule, settings, created_at, saved_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			t.ID, kind, t.Name, t.Enabled, schedule, settings, t.CreatedAt.Unix(), user(by))
	})
}

// Update changes a task, which its author saved last then.
func (s *Service) Update(ctx context.Context, kind, id string, in Input, by Author) (Task, error) {
	t, err := s.build(kind, in, by)
	if err != nil {
		return t, err
	}
	t.ID = id
	return s.save(ctx, t, func(tx *sql.Tx, schedule, settings []byte) (sql.Result, error) {
		return tx.ExecContext(ctx, `UPDATE tasks SET name = ?, enabled = ?, schedule = ?, settings = ?, saved_by = ? WHERE id = ? AND kind = ?`,
			t.Name, t.Enabled, schedule, settings, user(by), id, kind)
	})
}

// user is the ID of an author in the database, NULL for none, e.g. in tests.
func user(by Author) any {
	if by.ID == 0 {
		return nil
	}
	return by.ID
}

// Delete removes a task. A run in progress finishes, but stops waiting.
func (s *Service) Delete(ctx context.Context, kind, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM tasks WHERE id = ? AND kind = ?`, id, kind)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	s.mu.Lock()
	s.withdraw(id)
	s.mu.Unlock()
	s.plan(Task{ID: id}) // disabled, so it is no longer scheduled
	return nil
}

// Forget removes a deleted server from the targets of all tasks.
func (s *Service) Forget(ctx context.Context, nodeID, serverID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM task_targets WHERE node_id = ? AND server_id = ?`, nodeID, serverID)
	return err
}

// Move keeps a server that moved to another node a target of its tasks.
func (s *Service) Move(ctx context.Context, serverID, from, to string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE task_targets SET node_id = ? WHERE node_id = ? AND server_id = ?`, to, from, serverID)
	return err
}

// build validates a task and lets its kind check its settings, which its author needs the
// permissions for.
func (s *Service) build(kind string, in Input, by Author) (Task, error) {
	t := Task{Name: strings.TrimSpace(in.Name), Enabled: in.Enabled, Schedule: in.Schedule, kind: kind}
	k, ok := s.kinds[kind]
	if !ok {
		return t, fmt.Errorf("unknown kind of task %q", kind)
	}
	var err error
	t.Targets, err = CheckTargets(in.Targets)
	optional, _ := k.(OptionalTargets)
	switch msg := t.Schedule.Normalize(); {
	case t.Name == "" || len(t.Name) > 64:
		return t, httpapi.Errorf(http.StatusBadRequest, "Enter a name with up to 64 characters.")
	case msg != "":
		return t, httpapi.Errorf(http.StatusBadRequest, "%s", msg)
	case t.Enabled && t.Schedule.Next(time.Now()).IsZero():
		return t, httpapi.Errorf(http.StatusBadRequest, "All dates have passed. Add one that is still to come, or pause it.")
	case err != nil:
		return t, err
	case len(t.Targets) == 0 && (optional == nil || !optional.TargetsOptional(in.Settings)):
		return t, errTargets
	}
	if t.Settings, err = k.Check(in.Settings); err == nil {
		err = by.allowed(needs(k, t.Settings))
	}
	return t, err
}

// CheckTargets checks targets and puts them into their canonical form, without duplicates and
// without single servers of nodes whose servers are all targets anyway.
func CheckTargets(in []Target) ([]Target, error) {
	if len(in) > maxTargets {
		return nil, errTargets
	}
	canonical := make([]Target, len(in))
	for i, t := range in {
		var err error
		if canonical[i], err = t.canonical(); err != nil {
			return nil, err
		}
	}
	out := []Target{}
	for _, t := range canonical {
		wholeNode := t.ServerID != "" && slices.Contains(canonical, Target{Kind: KindServer, NodeID: t.NodeID})
		if !wholeNode && !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out, nil
}

// canonical returns a target with only the fields of its kind, or an error if it is invalid.
func (t Target) canonical() (Target, error) {
	switch t.Kind {
	case "", KindServer:
		if t.NodeID != "" && (t.ServerID == "" || idPattern.MatchString(t.ServerID)) {
			return Target{Kind: KindServer, NodeID: t.NodeID, ServerID: t.ServerID}, nil
		}
	case KindTag:
		tags, err := tag.Normalize([]string{t.Value})
		if err != nil {
			return t, err
		}
		return Target{Kind: KindTag, Value: tags[0]}, nil
	case KindNetwork:
		if idPattern.MatchString(t.Value) && slices.Contains([]string{"", RoleServers, RoleProxy}, t.Role) {
			return Target{Kind: KindNetwork, Value: t.Value, Role: t.Role}, nil
		}
	}
	return t, errTargets
}

// save writes a task and its targets with the statement write, and schedules it.
func (s *Service) save(ctx context.Context, t Task, write func(tx *sql.Tx, schedule, settings []byte) (sql.Result, error)) (Task, error) {
	schedule, err := json.Marshal(t.Schedule)
	if err != nil {
		return t, err
	}
	err = database.InTx(ctx, s.db, func(tx *sql.Tx) error {
		res, err := write(tx, schedule, t.Settings)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errNotFound
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM task_targets WHERE task_id = ?`, t.ID); err != nil {
			return err
		}
		for _, target := range t.Targets {
			var node, network any // NULL unless the target names one
			tag := ""
			switch target.Kind {
			case KindServer:
				node = target.NodeID
			case KindTag:
				tag = target.Value
			case KindNetwork:
				network = target.Value
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO task_targets (task_id, node_id, server_id, tag, network_id, role) VALUES (?, ?, ?, ?, ?, ?)`,
				t.ID, node, target.ServerID, tag, network, target.Role); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return t, constraint(err, t.Name)
	}
	s.plan(t)
	return s.Get(ctx, t.kind, t.ID)
}

func constraint(err error, name string) error {
	switch msg := err.Error(); {
	case strings.Contains(msg, "UNIQUE"):
		return httpapi.Errorf(http.StatusConflict, "The name %q is taken already.", name)
	case strings.Contains(msg, "FOREIGN KEY"):
		return httpapi.Errorf(http.StatusBadRequest, "Choose nodes and networks that exist.")
	}
	return err
}

// load reads the tasks of a kind, or one of them if id isn't empty, with their latest runs.
func (s *Service) load(ctx context.Context, kind, id string) ([]Task, error) {
	// A run records its outcome before it ends, so reading which tasks run first ensures
	// that a task that isn't running shows the outcome of its latest run.
	running := s.runningTasks()
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.kind, t.name, t.enabled, t.schedule, t.settings, t.created_at,
			coalesce(u.username, ''), iif(u.disabled, 0, coalesce(u.id, 0)), `+runColumns+`
		FROM tasks t
		LEFT JOIN users u ON u.id = t.saved_by
		LEFT JOIN task_runs r ON r.id = (SELECT max(id) FROM task_runs WHERE task_id = t.id)
		WHERE ? IN ('', t.kind) AND ? IN ('', t.id) ORDER BY t.name`, kind, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := []Task{}
	index := map[string]int{}
	for rows.Next() {
		t := Task{Targets: []Target{}}
		var schedule, settings string
		var createdAt int64
		var last runRow
		dest := append([]any{&t.ID, &t.kind, &t.Name, &t.Enabled, &schedule, &settings, &createdAt, &t.SavedBy, &t.author}, last.dest()...)
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(schedule), &t.Schedule); err != nil {
			return nil, err
		}
		t.Settings, t.CreatedAt = json.RawMessage(settings), time.Unix(createdAt, 0)
		if t.LastRun, err = last.run(); err != nil {
			return nil, err
		}
		t.NextRun, t.Running = s.nextRun(t.ID), running[t.ID]
		index[t.ID] = len(tasks)
		tasks = append(tasks, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	targets, err := s.db.QueryContext(ctx, `
		SELECT task_id, coalesce(node_id, ''), server_id, tag, coalesce(network_id, ''), role
		FROM task_targets WHERE ? IN ('', task_id) ORDER BY rowid`, id)
	if err != nil {
		return nil, err
	}
	defer targets.Close()
	for targets.Next() {
		var taskID, tag, network string
		t := Target{Kind: KindServer}
		if err := targets.Scan(&taskID, &t.NodeID, &t.ServerID, &tag, &network, &t.Role); err != nil {
			return nil, err
		}
		switch {
		case tag != "":
			t.Kind, t.Value = KindTag, tag
		case network != "":
			t.Kind, t.Value = KindNetwork, network
		}
		if i, ok := index[taskID]; ok {
			tasks[i].Targets = append(tasks[i].Targets, t)
		}
	}
	return tasks, targets.Err()
}
