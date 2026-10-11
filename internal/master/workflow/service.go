package workflow

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/logs"
	"github.com/QwikByte/noryx/internal/master/network"
	"github.com/QwikByte/noryx/internal/master/player"
	"github.com/QwikByte/noryx/internal/master/plugin"
	"github.com/QwikByte/noryx/internal/master/ratelimit"
	"github.com/QwikByte/noryx/internal/master/schedule"
	"github.com/QwikByte/noryx/internal/master/server"
)

const (
	// maxQueue are the runs that triggers queue while a workflow runs.
	maxQueue = 20
	// maxParallel are the runs of a workflow at the same time that triggers start.
	maxParallel = 10
	// maxStored is how much of the steps of a run is stored; beyond, their outputs are left out.
	maxStored = 256 << 10
	// Triggers start a workflow burst times at once, then once every interval.
	burst    = 30
	interval = 10 * time.Second
)

// Nodes connect to the agents.
type Nodes interface {
	Conn(ctx context.Context, id string) (grpc.ClientConnInterface, error)
}

// Targets find the servers of targets, see schedule.Service.
type Targets interface {
	Resolve(ctx context.Context, targets []schedule.Target) ([]schedule.Server, error)
	Matcher(ctx context.Context, targets []schedule.Target) (func(nodeID, serverID string) bool, error)
}

// Networks are the networks that steps act on.
type Networks interface {
	Get(ctx context.Context, id string) (network.Network, error)
	RollingRestart(ctx context.Context, n network.Network, batch int, only ...network.Ref) error
	SetMaintenance(ctx context.Context, n network.Network, c network.MaintenanceChange) (network.Maintenance, error)
}

// Players change and move players, see player.Service.
type Players interface {
	Change(ctx context.Context, changes []*noryxv1.PlayerChange, servers []network.Ref) []player.Result
	Send(ctx context.Context, n network.Network, player, server string) error
}

// Usage tells the latest measurements of the nodes.
type Usage interface {
	Latest(ctx context.Context, nodeID string) (*noryxv1.GetStatsResponse, error)
}

// Backups back up servers as backup jobs do.
type Backups interface {
	BackUp(ctx context.Context, nodeID, serverID string, t schedule.Task) (*noryxv1.Backup, error)
}

// Plugins update the plugins and mods of servers.
type Plugins interface {
	Update(ctx context.Context, servers []plugin.Ref, projects []string) []plugin.Result
}

// Notify sends messages through notification channels and requests to public addresses.
type Notify interface {
	Post(channelID string, e logs.Entry) error
	Fetch(req *http.Request) (*http.Response, error)
}

// Logs are the log, whose entries start workflows.
type Logs interface {
	List(ctx context.Context, f logs.Filter, oldest bool, limit int) ([]logs.Entry, error)
	Changed() <-chan struct{}
}

// Names name the nodes and servers of measurements.
type Names interface {
	Node(ctx context.Context, id string) string
	Server(ctx context.Context, nodeID, id string) string
}

// Access tells the permissions of the users who saved workflows.
type Access interface {
	Grants(ctx context.Context, userID int64) (access.Grants, error)
}

// Config tells how the players are warned, as the settings say when a step warns them.
type Config interface {
	Warnings() server.Warnings
}

// Deps are what workflows act on and learn from.
type Deps struct {
	Nodes    Nodes
	Targets  Targets
	Networks Networks
	Players  Players
	Usage    Usage
	Backups  Backups
	Plugins  Plugins
	Notify   Notify
	Logs     Logs
	Names    Names
	Access   Access
	Config   Config
}

// Author is the user who saves a workflow or runs it, with the permissions of the request.
type Author struct {
	ID     int64
	Name   string
	Grants access.Grants
}

// allowed checks that the author has the permissions on all servers.
func (a Author) allowed(perms []access.Permission) error {
	for _, p := range perms {
		if !a.Grants.Has(p) {
			return access.Denied(p)
		}
	}
	return nil
}

// Service stores the workflows, starts them when their triggers fire and keeps their runs.
type Service struct {
	db    *sql.DB
	deps  Deps
	limit *ratelimit.Limiter // runs that triggers start, per workflow
	wake  chan struct{}

	mu      sync.Mutex
	ctx     context.Context // ends the runs when the master stops
	active  map[string]*active
	runs    map[int64]*run
	limited map[string]time.Time // when it was logged that triggers fired too often

	watch watch

	changes sync.Mutex // one change of the stored workflows at a time
}

// active is a workflow as its triggers and runs need it.
type active struct {
	wf      Workflow
	needs   []access.Permission
	hook    string      // the SHA-256 of the token of its URL, if any
	next    []time.Time // of its schedule and interval triggers
	running int
	queue   []pending
}

// pending is a run that a trigger queued.
type pending struct {
	trigger string
	data    map[string]any
}

func NewService(db *sql.DB, deps Deps) *Service {
	return &Service{
		db: db, deps: deps, limit: ratelimit.New(burst, interval), wake: make(chan struct{}, 1), ctx: context.Background(),
		active: map[string]*active{}, runs: map[int64]*run{}, limited: map[string]time.Time{},
		watch: watch{seen: map[string]map[string]*observed{}, crossings: map[crossKey]*crossingAt{}, matchers: map[string]matcher{}},
	}
}

// Start loads the workflows and runs their triggers until ctx is done. Runs that the master
// stopped are recorded as failed.
func (s *Service) Start(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE workflow_runs SET outcome = ?, error = ?, ended_at = started_at WHERE outcome = ?`,
		Failed, "The master stopped during the run.", Running); err != nil {
		return err
	}
	s.mu.Lock()
	s.ctx = ctx
	s.mu.Unlock()
	if err := s.reload(ctx); err != nil {
		return err
	}
	go s.schedule(ctx)
	go s.follow(ctx)
	return nil
}

// columns are those of a workflow in workflows w, which scan reads.
const columns = `w.id, w.name, w.description, w.enabled, w.definition, w.hook, w.created_at, w.updated_at,
	coalesce(u.username, ''), iif(u.disabled, 0, coalesce(u.id, 0))`

// load reads the workflows, or one of them if id isn't empty, with their secrets.
func (s *Service) load(ctx context.Context, id string) ([]Workflow, map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM workflows w LEFT JOIN users u ON u.id = w.saved_by
		WHERE ? IN ('', w.id) ORDER BY w.name`, id)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	list, hooks := []Workflow{}, map[string]string{}
	for rows.Next() {
		var wf Workflow
		var def, hook string
		var created, updated int64
		if err := rows.Scan(&wf.ID, &wf.Name, &wf.Description, &wf.Enabled, &def, &hook, &created, &updated, &wf.SavedBy, &wf.author); err != nil {
			return nil, nil, err
		}
		if err := json.Unmarshal([]byte(def), &wf.Definition); err != nil {
			return nil, nil, fmt.Errorf("read the workflow %s: %w", wf.Name, err)
		}
		wf.CreatedAt, wf.UpdatedAt, wf.Hook = time.Unix(created, 0), time.Unix(updated, 0), hook != ""
		hooks[wf.ID] = hook
		list = append(list, wf)
	}
	return list, hooks, rows.Err()
}

// reload makes the stored workflows those that triggers start, keeping what runs.
func (s *Service) reload(ctx context.Context) error {
	list, hooks, err := s.load(ctx, "")
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fresh := map[string]*active{}
	now := time.Now()
	for _, wf := range list {
		d := Draft{Name: wf.Name, Description: wf.Description, Enabled: wf.Enabled, Definition: wf.Definition}
		needs, err := d.check()
		if err != nil { // stored by an older version that allowed more
			slog.Warn("A workflow is invalid and doesn't run", logging.Workflows, "workflow", wf.Name, "err", err)
			wf.Enabled = false
		}
		a := &active{wf: wf, needs: needs, hook: hooks[wf.ID], next: make([]time.Time, len(wf.Triggers))}
		if old := s.active[wf.ID]; old != nil {
			a.running, a.queue = old.running, old.queue
		}
		if wf.Enabled {
			for i, t := range wf.Triggers {
				a.next[i] = next(t, now)
			}
		}
		fresh[wf.ID] = a
	}
	s.active = fresh
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return nil
}

// List returns the workflows, without the values of secret headers.
func (s *Service) List(ctx context.Context) ([]Workflow, error) {
	list, _, err := s.load(ctx, "")
	if err != nil {
		return nil, err
	}
	last, err := s.lastRuns(ctx)
	if err != nil {
		return nil, err
	}
	for i := range list {
		list[i] = s.view(list[i], last[list[i].ID])
	}
	return list, nil
}

// Get returns a workflow, without the values of secret headers.
func (s *Service) Get(ctx context.Context, id string) (Workflow, error) {
	wf, err := s.get(ctx, id)
	if err != nil {
		return wf, err
	}
	last, err := s.lastRuns(ctx)
	return s.view(wf, last[id]), err
}

func (s *Service) get(ctx context.Context, id string) (Workflow, error) {
	list, _, err := s.load(ctx, id)
	if err == nil && len(list) == 0 {
		err = errNotFound
	}
	if err != nil {
		return Workflow{}, err
	}
	return list[0], nil
}

// view adds what runs and runs next to a workflow, and hides its secrets.
func (s *Service) view(wf Workflow, last *Run) Workflow {
	wf.LastRun = last
	s.mu.Lock()
	if a := s.active[wf.ID]; a != nil {
		wf.Running = a.running
		for _, at := range a.next {
			if !at.IsZero() && (wf.NextRun == nil || at.Before(*wf.NextRun)) {
				wf.NextRun = &at
			}
		}
	}
	s.mu.Unlock()
	wf.Steps = redact(wf.Steps)
	return wf
}

// redact returns steps without the values of secret headers of requests.
func redact(steps []Step) []Step {
	steps = clone(steps)
	walk(steps, func(st *Step) {
		if st.Kind != "http" {
			return
		}
		var h httpSettings
		if json.Unmarshal(st.With, &h) != nil {
			return
		}
		for i := range h.Headers {
			if h.Headers[i].Secret {
				h.Headers[i].Value = ""
			}
		}
		st.With, _ = json.Marshal(h)
	})
	return steps
}

// keepSecrets gives the secret headers that a change leaves empty the values they had: those
// of the same step with the same name, as long as the step sends them to the same URL, so that
// nobody can send a secret elsewhere without knowing it.
func keepSecrets(steps, old []Step) {
	secrets := map[string]string{}
	key := func(st *Step, url, name string) string { return st.ID + "\x00" + url + "\x00" + strings.ToLower(name) }
	walk(old, func(st *Step) {
		var h httpSettings
		if st.Kind == "http" && json.Unmarshal(st.With, &h) == nil {
			for _, hd := range h.Headers {
				if hd.Secret {
					secrets[key(st, strings.TrimSpace(h.URL), hd.Name)] = hd.Value
				}
			}
		}
	})
	walk(steps, func(st *Step) {
		var h httpSettings
		if st.Kind != "http" || json.Unmarshal(st.With, &h) != nil {
			return
		}
		for i, hd := range h.Headers {
			if hd.Secret && hd.Value == "" {
				h.Headers[i].Value = secrets[key(st, strings.TrimSpace(h.URL), hd.Name)]
			}
		}
		st.With, _ = json.Marshal(h)
	})
}

func clone(steps []Step) []Step {
	b, _ := json.Marshal(steps)
	var out []Step
	_ = json.Unmarshal(b, &out)
	if out == nil {
		out = []Step{}
	}
	return out
}

// needs checks a draft and returns the permissions it needs, with those of the workflows it
// calls, which run with others' permissions but on its behalf.
func (s *Service) needs(d *Draft) ([]access.Permission, error) {
	needs, err := d.check()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	walk(d.Steps, func(st *Step) {
		var c callSettings
		switch {
		case st.Kind != "call" || st.Disabled || json.Unmarshal(st.With, &c) != nil:
		case s.active[c.Workflow] == nil:
			err = cmp.Or(err, bad("Step %s: Choose a workflow that exists.", st.label()))
		default:
			needs = append(needs, s.active[c.Workflow].needs...)
		}
	})
	slices.Sort(needs)
	return slices.Compact(needs), err
}

// Create stores a new workflow, which its author saved last.
func (s *Service) Create(ctx context.Context, d Draft, by Author) (Workflow, error) {
	s.changes.Lock()
	defer s.changes.Unlock()
	needs, err := s.needs(&d)
	if err == nil {
		err = by.allowed(needs)
	}
	if err == nil {
		err = d.missingSecrets()
	}
	if err != nil {
		return Workflow{}, err
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflows`).Scan(&n); err != nil {
		return Workflow{}, err
	}
	if n >= maxWorkflows {
		return Workflow{}, httpapi.Errorf(http.StatusConflict, "The master keeps at most %d workflows.", maxWorkflows)
	}
	def, err := json.Marshal(d.Definition)
	if err != nil {
		return Workflow{}, err
	}
	id, now := strings.ToLower(rand.Text()), time.Now().Unix()
	_, err = s.db.ExecContext(ctx, `INSERT INTO workflows (id, name, description, enabled, definition, saved_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, d.Name, d.Description, d.Enabled, def, user(by), now, now)
	if err != nil {
		return Workflow{}, taken(err, d.Name)
	}
	if err := s.reload(ctx); err != nil {
		return Workflow{}, err
	}
	return s.Get(ctx, id)
}

// Update changes a workflow, which its author saved last then. Secret headers left empty keep
// their values.
func (s *Service) Update(ctx context.Context, id string, d Draft, by Author) (Workflow, error) {
	s.changes.Lock()
	defer s.changes.Unlock()
	old, err := s.get(ctx, id)
	if err != nil {
		return old, err
	}
	keepSecrets(d.Steps, old.Steps)
	needs, err := s.needs(&d)
	if err == nil {
		err = by.allowed(needs)
	}
	if err == nil {
		err = d.missingSecrets()
	}
	if err != nil {
		return Workflow{}, err
	}
	def, err := json.Marshal(d.Definition)
	if err != nil {
		return Workflow{}, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE workflows SET name = ?, description = ?, enabled = ?, definition = ?, saved_by = ?, updated_at = ?
		WHERE id = ?`, d.Name, d.Description, d.Enabled, def, user(by), time.Now().Unix(), id)
	if err != nil {
		return Workflow{}, taken(err, d.Name)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Workflow{}, errNotFound
	}
	if err := s.reload(ctx); err != nil {
		return Workflow{}, err
	}
	return s.Get(ctx, id)
}

// missingSecrets tells about secret headers without a value.
func (d Draft) missingSecrets() error {
	var err error
	walk(d.Steps, func(st *Step) {
		var h httpSettings
		if st.Kind == "http" && json.Unmarshal(st.With, &h) == nil {
			for _, hd := range h.Headers {
				if hd.Secret && hd.Value == "" && err == nil {
					err = bad("Step %s: Enter the value of the secret header %s, also after changing the URL.", st.label(), hd.Name)
				}
			}
		}
	})
	return err
}

func user(by Author) any {
	if by.ID == 0 {
		return nil
	}
	return by.ID
}

func taken(err error, name string) error {
	if strings.Contains(err.Error(), "UNIQUE") {
		return httpapi.Errorf(http.StatusConflict, "The name %q is taken already.", name)
	}
	return err
}

// Delete removes a workflow and cancels its runs. The steps of others that run it are turned
// off.
func (s *Service) Delete(ctx context.Context, id string) error {
	s.changes.Lock()
	res, err := s.db.ExecContext(ctx, `DELETE FROM workflows WHERE id = ?`, id)
	s.changes.Unlock()
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	s.mu.Lock()
	for _, r := range s.runs {
		if r.wf.ID == id {
			r.cancel(errCancelled)
		}
	}
	s.mu.Unlock()
	if err := s.reload(ctx); err != nil {
		return err
	}
	return s.Prune(ctx)
}

// NewHook gives a workflow a new URL, which replaces the one before, and returns its token.
// Only its SHA-256 is stored, so the token can't be shown again.
func (s *Service) NewHook(ctx context.Context, id string) (string, error) {
	token := strings.ToLower(rand.Text() + rand.Text())
	res, err := s.db.ExecContext(ctx, `UPDATE workflows SET hook = ? WHERE id = ?`, hash(token), id)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", errNotFound
	}
	return token, s.reload(ctx)
}

// DeleteHook removes the URL of a workflow.
func (s *Service) DeleteHook(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE workflows SET hook = '' WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	return s.reload(ctx)
}

func hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Hook starts the enabled workflow whose URL has the token, if it has a webhook trigger. It
// returns whether the run started, was queued or skipped.
func (s *Service) Hook(token string, data map[string]any) (string, error) {
	h := hash(token)
	s.mu.Lock()
	id := ""
	for _, a := range s.active {
		if a.hook != "" && a.hook == h && a.wf.Enabled && slices.ContainsFunc(a.wf.Triggers, func(t Trigger) bool { return t.Kind == OnWebhook }) {
			id = a.wf.ID
		}
	}
	s.mu.Unlock()
	if id == "" {
		return "", httpapi.Errorf(http.StatusNotFound, "Not found.")
	}
	return s.fire(id, OnWebhook, data), nil
}

// RunNow runs a workflow for a user, also a paused one, with inputs. The user needs the
// permissions that it needs.
func (s *Service) RunNow(ctx context.Context, id string, inputs map[string]any, by Author) (Run, error) {
	s.mu.Lock()
	a := s.active[id]
	s.mu.Unlock()
	if a == nil {
		return Run{}, errNotFound
	}
	// A copy, as checking normalizes it.
	var d Draft
	raw, err := json.Marshal(Draft{Name: a.wf.Name, Definition: a.wf.Definition})
	if err == nil {
		err = json.Unmarshal(raw, &d)
	}
	var needs []access.Permission
	if err == nil {
		needs, err = s.needs(&d)
	}
	if err == nil {
		err = by.allowed(needs)
	}
	if err != nil {
		return Run{}, err
	}
	r, err := s.launch(a.wf, ByHand, by.Name, map[string]any{"kind": ByHand, "user": by.Name}, inputs, 0)
	if err != nil {
		return Run{}, err
	}
	return Run{ID: r.id, Trigger: ByHand, StartedBy: by.Name, StartedAt: r.started, Outcome: Running}, nil
}

// fire starts a run for a trigger of an enabled workflow: right away, or once the runs before
// ended if it queues; not at all if it skips while it runs, or if triggers fire too often. It
// returns started, queued or skipped.
func (s *Service) fire(id, trigger string, data map[string]any) string {
	s.mu.Lock()
	a := s.active[id]
	switch {
	case a == nil || !a.wf.Enabled:
		s.mu.Unlock()
		return Skipped
	case !s.limit.Allow(id):
		if time.Since(s.limited[id]) > time.Hour {
			s.limited[id] = time.Now()
			slog.Warn("A workflow's triggers fire too often, so some are skipped", logging.Workflows, "workflow", a.wf.Name, "workflow_id", id)
		}
		s.mu.Unlock()
		return Skipped
	case a.running > 0 && a.wf.Overlap == Queue:
		if len(a.queue) >= maxQueue {
			s.mu.Unlock()
			return Skipped
		}
		a.queue = append(a.queue, pending{trigger, data})
		s.mu.Unlock()
		return "queued"
	case a.running > 0 && a.wf.Overlap == Skip, a.running >= maxParallel:
		s.mu.Unlock()
		return Skipped
	}
	a.running++ // reserved for the run
	wf := a.wf
	s.mu.Unlock()
	go func() {
		if _, err := s.begin(wf, trigger, "", data, nil, 0); err != nil {
			slog.Warn("Can't start a workflow", logging.Workflows, "workflow", wf.Name, "workflow_id", wf.ID, "err", err)
			s.release(wf.ID)
		}
	}()
	return "started"
}

// launch starts a run that no trigger started, whatever else runs.
func (s *Service) launch(wf Workflow, trigger, by string, data, inputs map[string]any, calls int) (*run, error) {
	s.mu.Lock()
	a := s.active[wf.ID]
	if a == nil {
		s.mu.Unlock()
		return nil, errNotFound
	}
	a.running++
	s.mu.Unlock()
	r, err := s.begin(wf, trigger, by, data, inputs, calls)
	if err != nil {
		s.release(wf.ID)
	}
	return r, err
}

// begin records a run and runs it in the background, for which its workflow counts as running.
func (s *Service) begin(wf Workflow, trigger, by string, data, given map[string]any, calls int) (*run, error) {
	inputs, err := wf.inputs(given)
	if err != nil {
		return nil, err
	}
	loc, err := time.LoadLocation(wf.TimeZone)
	if err != nil {
		loc = time.UTC
	}
	r := &run{
		svc: s, wf: wf, calls: calls, started: time.Now(), loc: loc, done: make(chan struct{}),
		data: map[string]any{"trigger": normalize(data), "inputs": inputs}, vars: map[string]any{}, outputs: map[string]any{},
	}
	stored, err := json.Marshal(map[string]any{"trigger": r.data["trigger"], "inputs": inputs})
	if err != nil || len(stored) > maxData {
		stored = []byte(`{"note":"The data were too large to keep."}`)
	}
	s.mu.Lock()
	ctx := s.ctx
	s.mu.Unlock()
	res, err := s.db.ExecContext(ctx, `INSERT INTO workflow_runs (workflow_id, trigger, started_by, started_at, ended_at, outcome, error, steps, data)
		VALUES (?, ?, ?, ?, 0, ?, '', '[]', ?)`, wf.ID, trigger, by, r.started.UnixMilli(), Running, stored)
	if err == nil {
		r.id, err = res.LastInsertId()
	}
	if err != nil {
		return nil, err
	}
	r.data["workflow"] = map[string]any{"id": wf.ID, "name": wf.Name}
	r.data["run"] = map[string]any{"id": float64(r.id), "trigger": trigger, "startedBy": by}
	ctx, cancel := context.WithCancelCause(ctx)
	ctx, timeout := context.WithTimeoutCause(ctx, maxRunTime, context.DeadlineExceeded)
	r.cancel = func(cause error) {
		cancel(cause)
		timeout()
	}
	s.mu.Lock()
	s.runs[r.id] = r
	var needs []access.Permission
	if a := s.active[wf.ID]; a != nil {
		needs = a.needs
	}
	s.mu.Unlock()
	go func() {
		if err := s.authorize(ctx, wf, needs); err != nil {
			slog.Warn("A workflow can't run", logging.Workflows, "workflow", wf.Name, "workflow_id", wf.ID, "err", err)
			s.finish(r, Failed, err.Error())
			return
		}
		r.execute(ctx)
	}()
	return r, nil
}

// authorize checks that the user who saved a workflow last still has the permissions it needs.
func (s *Service) authorize(ctx context.Context, wf Workflow, needs []access.Permission) error {
	if len(needs) == 0 {
		return nil
	}
	if wf.author == 0 {
		return errors.New("The user who saved it last was deleted or disabled, so it runs with nobody's permissions. Save it again.") //nolint:staticcheck // shown in the panel
	}
	g, err := s.deps.Access.Grants(ctx, wf.author)
	if err != nil {
		return err
	}
	for _, p := range needs {
		if !g.Has(p) {
			return fmt.Errorf("%s, who saved it last, no longer has the permission %q on all servers. Save it again with that permission", wf.SavedBy, access.Label(p))
		}
	}
	return nil
}

// finish records how a run ended, and starts the next queued run of its workflow.
func (s *Service) finish(r *run, outcome, msg string) {
	r.mu.Lock()
	r.outcome, r.err = outcome, msg
	records := append([]StepRun{}, r.records...)
	r.mu.Unlock()
	steps, err := json.Marshal(records)
	if err == nil && len(steps) > maxStored {
		for i := range records {
			records[i].Output = nil
		}
		for steps, err = json.Marshal(records); err == nil && len(steps) > maxStored; steps, err = json.Marshal(records) {
			records = records[:len(records)/2]
		}
	}
	s.mu.Lock()
	ctx := context.WithoutCancel(s.ctx)
	s.mu.Unlock()
	if err == nil {
		_, err = s.db.ExecContext(ctx, `UPDATE workflow_runs SET ended_at = ?, outcome = ?, error = ?, steps = ? WHERE id = ?`,
			time.Now().UnixMilli(), outcome, msg, steps, r.id)
	}
	if err == nil {
		_, err = s.db.ExecContext(ctx, `DELETE FROM workflow_runs WHERE workflow_id = ? AND outcome != ? AND id <= (
			SELECT id FROM workflow_runs WHERE workflow_id = ? ORDER BY id DESC LIMIT 1 OFFSET ?)`, r.wf.ID, Running, r.wf.ID, maxRuns)
	}
	if err != nil {
		slog.Error("Can't record the run of a workflow", logging.Workflows, "workflow", r.wf.Name, "err", err)
	}
	r.cancel(nil)
	s.mu.Lock()
	delete(s.runs, r.id)
	s.mu.Unlock()
	close(r.done)
	s.release(r.wf.ID)
}

// release counts a run of a workflow as ended, and starts the next one it queued.
func (s *Service) release(id string) {
	s.mu.Lock()
	a := s.active[id]
	if a == nil {
		s.mu.Unlock()
		return
	}
	a.running--
	if len(a.queue) == 0 || !a.wf.Enabled {
		a.queue = nil
		s.mu.Unlock()
		return
	}
	p := a.queue[0]
	a.queue = a.queue[1:]
	a.running++
	wf := a.wf
	s.mu.Unlock()
	if _, err := s.begin(wf, p.trigger, "", p.data, nil, 0); err != nil {
		slog.Warn("Can't start a workflow", logging.Workflows, "workflow", wf.Name, "workflow_id", wf.ID, "err", err)
		s.release(id)
	}
}

// call runs another workflow for a step, and waits for its run to end if it should.
func (s *Service) call(ctx context.Context, caller *run, id string, inputs map[string]any, wait bool) (any, error) {
	s.mu.Lock()
	a := s.active[id]
	s.mu.Unlock()
	if a == nil {
		return nil, bad("The workflow to run no longer exists.")
	}
	// It runs on behalf of the caller, whose author needs what it needs too.
	if err := s.authorize(ctx, caller.wf, a.needs); err != nil {
		return nil, err
	}
	data := map[string]any{"kind": ByWorkflow, "workflow": map[string]any{"id": caller.wf.ID, "name": caller.wf.Name, "run": float64(caller.id)}}
	r, err := s.launch(a.wf, ByWorkflow, caller.wf.Name, data, inputs, caller.calls+1)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"run": r.id, "workflow": a.wf.Name}
	if !wait {
		out["detail"] = fmt.Sprintf("It started %s.", a.wf.Name)
		return out, nil
	}
	select {
	case <-r.done:
	case <-ctx.Done():
		r.cancel(errCancelled)
		<-r.done
	}
	r.mu.Lock()
	out["outcome"], out["result"], out["error"] = r.outcome, r.result, r.err
	r.mu.Unlock()
	out["detail"] = fmt.Sprintf("%s ended: %s.", a.wf.Name, out["outcome"])
	if out["outcome"] != Succeeded {
		return out, bad("%s %s: %s", a.wf.Name, out["outcome"], out["error"])
	}
	return out, nil
}

// Cancel cancels a run in progress.
func (s *Service) Cancel(id string, runID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[runID]
	if r == nil || r.wf.ID != id {
		return httpapi.Errorf(http.StatusConflict, "The run has ended already.")
	}
	r.cancel(errCancelled)
	return nil
}

// runColumns are those of a run in workflow_runs r, without its steps and data.
const runColumns = `r.id, r.workflow_id, r.trigger, r.started_by, r.started_at, r.ended_at, r.outcome, r.error`

func scanRun(row interface{ Scan(...any) error }, extra ...any) (Run, string, error) {
	var r Run
	var workflow string
	var started, ended int64
	err := row.Scan(append([]any{&r.ID, &workflow, &r.Trigger, &r.StartedBy, &started, &ended, &r.Outcome, &r.Error}, extra...)...)
	r.StartedAt = time.UnixMilli(started)
	if r.Outcome != Running {
		end := time.UnixMilli(ended)
		r.EndedAt = &end
	}
	return r, workflow, err
}

// lastRuns returns the latest run of each workflow.
func (s *Service) lastRuns(ctx context.Context) (map[string]*Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+runColumns+` FROM workflow_runs r
		WHERE r.id IN (SELECT max(id) FROM workflow_runs GROUP BY workflow_id)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	last := map[string]*Run{}
	for rows.Next() {
		r, workflow, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		last[workflow] = &r
	}
	return last, rows.Err()
}

// Runs returns the kept runs of a workflow, newest first, without their steps.
func (s *Service) Runs(ctx context.Context, id string) ([]Run, error) {
	if _, err := s.get(ctx, id); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+runColumns+` FROM workflow_runs r WHERE r.workflow_id = ? ORDER BY r.id DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := []Run{}
	for rows.Next() {
		r, _, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

// Run returns a run of a workflow with its steps, so far for a run in progress.
func (s *Service) Run(ctx context.Context, id string, runID int64) (Run, error) {
	var steps, data string
	r, _, err := scanRun(s.db.QueryRowContext(ctx, `SELECT `+runColumns+`, r.steps, r.data FROM workflow_runs r WHERE r.id = ? AND r.workflow_id = ?`, runID, id), &steps, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return r, httpapi.Errorf(http.StatusNotFound, "Run not found. It may have been deleted.")
	}
	if err != nil {
		return r, err
	}
	r.Data = json.RawMessage(data)
	s.mu.Lock()
	live := s.runs[runID]
	s.mu.Unlock()
	if live != nil {
		r.Steps = live.snapshot()
		return r, nil
	}
	return r, json.Unmarshal([]byte(steps), &r.Steps)
}

// Upcoming is a run that a schedule or an interval starts.
type Upcoming struct {
	ID string    `json:"id"`
	At time.Time `json:"at"`
}

// Upcoming returns the runs that the schedules and intervals of enabled workflows start until
// a time, in order, at most maxUpcoming.
func (s *Service) Upcoming(until time.Time) []Upcoming {
	const maxUpcoming = 1000
	list := []Upcoming{}
	s.mu.Lock()
	for id, a := range s.active {
		for i, at := range a.next {
			for ; !at.IsZero() && !at.After(until) && len(list) < 10*maxUpcoming; at = next(a.wf.Triggers[i], at) {
				list = append(list, Upcoming{id, at})
			}
		}
	}
	s.mu.Unlock()
	slices.SortFunc(list, func(a, b Upcoming) int { return cmp.Or(a.At.Compare(b.At), cmp.Compare(a.ID, b.ID)) })
	return list[:min(len(list), maxUpcoming)]
}
