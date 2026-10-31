package schedule

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/status"

	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/database"
)

const (
	// maxRuns is how many runs of a task are kept.
	maxRuns = 50
	// maxSteps is how many steps of a run are kept, e.g. of the servers a job backed up.
	maxSteps = 500
	maxError = 4000

	// Outcomes of runs and their steps.
	Succeeded = "succeeded"
	Failed    = "failed"
	// OutcomeSkipped is that of a step that left a server out, e.g. as players were online,
	// and of a run whose steps all did.
	OutcomeSkipped = "skipped"
)

// Run is a run of a task.
type Run struct {
	ID        int64     `json:"id"`
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt"`
	// StartedBy is the user who started the run by hand; empty for its schedule.
	StartedBy string `json:"startedBy,omitempty"`
	// Outcome is succeeded, failed or skipped.
	Outcome string `json:"outcome"`
	Error   string `json:"error,omitempty"`
	// Note tells what the run left out, e.g. servers without data to back up.
	Note string `json:"note,omitempty"`
	// Steps are what the run did on each server, in the order they ended.
	Steps []Step `json:"steps"`
}

// Step is the outcome of an action of a run on a server.
type Step struct {
	// Action is what the log calls it, e.g. "Restart server".
	Action  string `json:"action"`
	Server  string `json:"server"`
	Node    string `json:"node"`
	Outcome string `json:"outcome"`
	// Detail tells why the action failed or left the server out.
	Detail string `json:"detail,omitempty"`
	// Change tells what the action changed, e.g. the plugins it updated.
	Change string `json:"change,omitempty"`
}

// Skipped is the error of an action that left a server out rather than failed on it, e.g. a
// backup of a server without data yet. Report notes it on the run, which succeeds.
type Skipped string

func (s Skipped) Error() string { return string(s) }

// Report logs the outcome of an action of a task on the server, e.g. "Restart server",
// records it as a step of the run, and returns its error with the name of the server.
func (s Server) Report(t Task, category slog.Attr, action string, err error) error {
	return s.ReportChange(t, category, action, "", err)
}

// ReportChange is Report for an action that tells what it changed on the server, if anything,
// e.g. the plugins it updated.
func (s Server) ReportChange(t Task, category slog.Attr, action, change string, err error) error {
	attrs := []any{category, "task", t.Name, logging.KeyNode, s.NodeID, logging.KeyNodeName, s.NodeName,
		logging.KeyServer, s.GetId(), logging.KeyServerName, s.GetName()}
	step := Step{Action: action, Server: s.GetName(), Node: s.NodeName, Outcome: Succeeded, Change: change}
	var skipped Skipped
	switch {
	case err == nil && change != "":
		slog.Info(action, append(attrs, "change", change)...)
	case err == nil:
		slog.Info(action, attrs...)
	case errors.As(err, &skipped):
		slog.Info(action+" skipped", append(attrs, "reason", string(skipped))...)
		s.journal.note(fmt.Sprintf("%s on %s: %s", s.GetName(), s.NodeName, skipped))
		step.Outcome, step.Detail, err = OutcomeSkipped, string(skipped), nil
	default:
		msg := status.Convert(err).Message()
		slog.Warn(action+" failed", append(attrs, "err", msg)...)
		step.Outcome, step.Detail, err = Failed, msg, fmt.Errorf("%s on %s: %s", s.GetName(), s.NodeName, msg)
	}
	s.journal.step(step)
	return err
}

// journal is what a run tells besides its error: notes, e.g. on the servers it skipped, and
// its steps.
type journal struct {
	mu    sync.Mutex
	notes []string
	steps []Step
}

func (j *journal) note(note string) {
	if j == nil { // a server outside of a run
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.notes = append(j.notes, note)
}

func (j *journal) step(s Step) {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.steps = append(j.steps, s)
}

// end completes a run that ended with err with what the journal tells: its note, its steps,
// and its outcome, which is skipped if all its steps were.
func (j *journal) end(r *Run, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	slices.Sort(j.notes)
	r.EndedAt, r.Note = time.Now(), database.Clip(strings.Join(slices.Compact(j.notes), "\n"), maxError)
	r.Steps = append([]Step{}, j.steps[:min(len(j.steps), maxSteps)]...)
	switch {
	case err != nil:
		r.Outcome, r.Error = Failed, database.Clip(err.Error(), maxError)
	case len(r.Steps) > 0 && !slices.ContainsFunc(r.Steps, func(s Step) bool { return s.Outcome != OutcomeSkipped }):
		r.Outcome = OutcomeSkipped
	default:
		r.Outcome = Succeeded
	}
}

// clip shortens the error or note of a run to what is stored.

// record stores a run of a task, unless the task was deleted meanwhile, and forgets the
// oldest runs beyond maxRuns.
func (s *Service) record(ctx context.Context, id string, r Run) error {
	steps, err := json.Marshal(append([]Step{}, r.Steps...))
	if err == nil {
		_, err = s.db.ExecContext(ctx, `
			INSERT INTO task_runs (task_id, started_at, ended_at, started_by, outcome, error, note, steps)
			SELECT id, ?, ?, ?, ?, ?, ?, ? FROM tasks WHERE id = ?`,
			r.StartedAt.Unix(), r.EndedAt.Unix(), r.StartedBy, r.Outcome, r.Error, r.Note, steps, id)
	}
	if err == nil {
		_, err = s.db.ExecContext(ctx, `DELETE FROM task_runs WHERE task_id = ? AND id <= (
			SELECT id FROM task_runs WHERE task_id = ? ORDER BY id DESC LIMIT 1 OFFSET ?)`, id, id, maxRuns)
	}
	return err
}

// Runs returns the kept runs of a task, newest first.
func (s *Service) Runs(ctx context.Context, kind, id string) ([]Run, error) {
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM tasks WHERE id = ? AND kind = ?)`, id, kind).Scan(&exists); err != nil || !exists {
		return nil, cmp.Or(err, errNotFound)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+runColumns+` FROM task_runs r WHERE task_id = ? ORDER BY id DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := []Run{}
	for rows.Next() {
		var r runRow
		if err := rows.Scan(r.dest()...); err != nil {
			return nil, err
		}
		run, err := r.run()
		if err != nil {
			return nil, err
		}
		runs = append(runs, *run)
	}
	return runs, rows.Err()
}

// runColumns are the columns of a run in task_runs r, which runRow reads.
const runColumns = "r.id, r.started_at, r.ended_at, r.started_by, r.outcome, r.error, r.note, r.steps"

// runRow reads a run, which may be missing, e.g. for a task that never ran.
type runRow struct {
	id, started, ended                sql.NullInt64
	by, outcome, errMsg, notes, steps sql.NullString
}

func (r *runRow) dest() []any {
	return []any{&r.id, &r.started, &r.ended, &r.by, &r.outcome, &r.errMsg, &r.notes, &r.steps}
}

func (r *runRow) run() (*Run, error) {
	if !r.id.Valid {
		return nil, nil
	}
	run := &Run{
		ID: r.id.Int64, StartedAt: time.Unix(r.started.Int64, 0), EndedAt: time.Unix(r.ended.Int64, 0),
		StartedBy: r.by.String, Outcome: r.outcome.String, Error: r.errMsg.String, Note: r.notes.String,
	}
	return run, json.Unmarshal([]byte(r.steps.String), &run.Steps)
}
