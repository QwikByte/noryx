package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode"

	"google.golang.org/grpc/status"

	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/database"
)

const (
	// maxRuns are kept per workflow.
	maxRuns = 100
	// maxRecords are the steps recorded per run; a loop may run many more.
	maxRecords = 1000
	// maxExecuted are the steps a run executes at most, so that a loop can't run forever.
	maxExecuted = 10_000
	// maxRunTime is how long a run may take, with its waits.
	maxRunTime = 72 * time.Hour
	maxError   = 4000
	// maxOutput is how much of the output of a step its record keeps.
	maxOutput = 4 << 10
	// maxData is how much of the data of its trigger and its inputs a run keeps.
	maxData = 16 << 10

	// Outcomes of runs and their steps.
	Running   = "running"
	Succeeded = "succeeded"
	Failed    = "failed"
	Cancelled = "cancelled"
	Skipped   = "skipped"
)

// Run is a run of a workflow.
type Run struct {
	ID int64 `json:"id"`
	// Trigger is what started it: a kind of trigger, manual or workflow.
	Trigger string `json:"trigger"`
	// StartedBy is the user or the workflow that started it.
	StartedBy string     `json:"startedBy,omitempty"`
	StartedAt time.Time  `json:"startedAt"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
	// Outcome is running, succeeded, failed or cancelled.
	Outcome string `json:"outcome"`
	Error   string `json:"error,omitempty"`
	// Steps are what it did, in the order the steps began; only with a single run.
	Steps []StepRun `json:"steps,omitempty"`
	// Data are those of its trigger and its inputs; only with a single run.
	Data json.RawMessage `json:"data,omitempty"`
}

// StepRun is what a step did in a run.
type StepRun struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
	// Index is that of the item of the innermost loop it ran in.
	Index     *int      `json:"index,omitempty"`
	Outcome   string    `json:"outcome"`
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt,omitzero"`
	// Detail tells why it failed, or what it decided, e.g. which case of a switch ran.
	Detail string          `json:"detail,omitempty"`
	Output json.RawMessage `json:"output,omitempty"`
}

// run is a run of a workflow in progress.
type run struct {
	svc     *Service
	wf      Workflow
	id      int64
	calls   int // workflows that called this one, one within the other
	cancel  func(cause error)
	started time.Time
	loc     *time.Location
	done    chan struct{} // closed once the run is recorded

	mu       sync.Mutex
	data     map[string]any // trigger, inputs, workflow and run
	vars     map[string]any
	outputs  map[string]any // of the steps by ID, the latest of each
	records  []StepRun
	dropped  int // records beyond maxRecords
	executed int
	result   any // that a stop step returned, for a calling workflow
	outcome  string
	err      string
}

// scope is where steps run: the whole run, an item of a loop or a branch. Outputs of steps
// within a loop are those of its item there, and those of the latest item after it.
type scope struct {
	run     *run
	parent  *scope
	locals  map[string]any // item, index and error
	outputs map[string]any
}

func (x *scope) child(locals map[string]any) *scope {
	return &scope{run: x.run, parent: x, locals: locals, outputs: map[string]any{}}
}

// lookup finds a path in the data of the run: trigger, inputs, vars, steps, item, index,
// error, workflow, run and now.
func (x *scope) lookup(path []string) any {
	r := x.run
	r.mu.Lock()
	defer r.mu.Unlock()
	var root any
	switch path[0] {
	case "trigger", "inputs", "workflow", "run":
		root = r.data[path[0]]
	case "vars":
		root = r.vars
	case "now":
		root = now(time.Now().In(r.loc))
	case "steps":
		if len(path) < 2 {
			return nil
		}
		for s := x; s != nil; s = s.parent {
			if out, ok := s.outputs[path[1]]; ok {
				return get(out, path[2:])
			}
		}
		return get(r.outputs[path[1]], path[2:])
	default:
		for s := x; s != nil; s = s.parent {
			if v, ok := s.locals[path[0]]; ok {
				return get(v, path[1:])
			}
		}
		return nil
	}
	return get(root, path[1:])
}

// now are the date and time as templates see them, e.g. {{now.time}}.
func now(t time.Time) map[string]any {
	return map[string]any{
		"iso": t.Format(time.RFC3339), "unix": float64(t.Unix()), "date": t.Format(time.DateOnly), "time": t.Format("15:04"),
		"weekday": float64(t.Weekday()), "day": float64(t.Day()), "month": float64(t.Month()), "year": float64(t.Year()),
		"hour": float64(t.Hour()), "minute": float64(t.Minute()),
	}
}

// value evaluates a template, keeping the type of a single expression.
func (x *scope) value(s string) (any, error) {
	t, err := parse(s)
	if err != nil {
		return nil, bad("The template %q is invalid: %s.", database.Clip(s, 60), err)
	}
	v, err := t.eval(x.lookup)
	if err != nil {
		return nil, bad("The template %q failed: %s.", database.Clip(s, 60), err)
	}
	return v, nil
}

// text evaluates a template to text.
func (x *scope) text(s string) (string, error) {
	v, err := x.value(s)
	return text(v), err
}

// line evaluates a template to text on one line without control characters, e.g. for a
// console command.
func (x *scope) line(s string) (string, error) {
	v, err := x.text(s)
	v = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, v)
	return strings.Join(strings.Fields(v), " "), err
}

// holds evaluates a condition.
func (x *scope) holds(c *Condition) (bool, error) { return c.eval(x.value) }

// setOutput keeps what a step tells later ones, in its scope and for the whole run.
func (x *scope) setOutput(id string, out any) {
	x.run.mu.Lock()
	defer x.run.mu.Unlock()
	for s := x; s != nil; s = s.parent {
		if s.outputs != nil {
			s.outputs[id] = out
			break
		}
	}
	x.run.outputs[id] = out
}

// failure is the error of a step that failed, which the steps around it pass on.
type failure struct {
	step Step
	err  error
}

func (f *failure) Error() string { return fmt.Sprintf("Step %s: %s", f.step.label(), message(f.err)) }
func (f *failure) Unwrap() error { return f.err }

// stop ends a run early, with its outcome, through all steps around it.
type stop struct {
	failed  bool
	message string
}

func (s *stop) Error() string { return s.message }

var errBudget = bad("It ran more than %d steps, e.g. in a loop that doesn't end.", maxExecuted)

// message is what the panel shows of an error of a step: that of the API or of an agent, or
// the text of an error that a step put together from such messages, e.g. of several servers.
func message(err error) string {
	if st, ok := status.FromError(err); ok {
		return st.Message()
	}
	return err.Error()
}

// steps runs steps one after the other, until one fails.
func (x *scope) steps(ctx context.Context, list []Step) error {
	for _, s := range list {
		if err := x.step(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

// step runs a step and records what it did. A step that fails ends the steps around it, unless
// it continues on errors; its output then tells the error.
func (x *scope) step(ctx context.Context, s Step) error {
	if s.Disabled {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r := x.run
	r.mu.Lock()
	r.executed++
	over := r.executed > maxExecuted
	r.mu.Unlock()
	if over {
		return &failure{s, errBudget}
	}
	k := kinds[s.Kind]
	rec := r.begin(s, x.index())
	settings, err := k.decode(s.With)
	var out any
	if err == nil {
		out, err = k.run(ctx, x, s, settings)
	}
	var st *stop
	var inner *failure
	outcome, detail := Succeeded, ""
	switch {
	case errors.As(err, &st):
		detail = st.message
		if st.failed {
			outcome = Failed
		}
		r.end(rec, outcome, detail, out)
		return err
	case ctx.Err() != nil:
		r.end(rec, Cancelled, "", out)
		return ctx.Err()
	case errors.As(err, &inner):
		outcome, detail = Failed, fmt.Sprintf("Step %s within failed.", inner.step.label())
	case err != nil:
		outcome, detail = Failed, message(err)
		err = &failure{s, err}
	case out != nil:
		if d, ok := out.(map[string]any)["detail"].(string); ok {
			detail = d
		}
	}
	m, _ := normalize(out).(map[string]any)
	if m == nil {
		m = map[string]any{}
	}
	m["outcome"] = outcome
	if err != nil {
		m["error"] = message(err)
	}
	x.setOutput(s.ID, m)
	r.end(rec, outcome, detail, out)
	if err != nil && s.Continue {
		return nil
	}
	return err
}

// index is that of the item of the innermost loop, if any.
func (x *scope) index() *int {
	for s := x; s != nil; s = s.parent {
		if i, ok := s.locals["index"].(float64); ok {
			n := int(i)
			return &n
		}
	}
	return nil
}

// begin records that a step began, and returns its record.
func (r *run) begin(s Step, index *int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.records) >= maxRecords {
		r.dropped++
		return -1
	}
	r.records = append(r.records, StepRun{ID: s.ID, Kind: s.Kind, Name: s.Name, Index: index, Outcome: Running, StartedAt: time.Now()})
	return len(r.records) - 1
}

// end records how a step ended and what it told.
func (r *run) end(rec int, outcome, detail string, out any) {
	var output json.RawMessage
	if out != nil {
		if b, err := json.Marshal(out); err == nil && len(b) <= maxOutput {
			output = b
		} else if err == nil {
			output, _ = json.Marshal(database.Clip(string(b), maxOutput))
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec < 0 {
		return
	}
	s := &r.records[rec]
	s.Outcome, s.Detail, s.Output, s.EndedAt = outcome, database.Clip(detail, maxError), output, time.Now()
}

// snapshot returns the records of the run so far.
func (r *run) snapshot() []StepRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]StepRun{}, r.records...)
}

// normalize turns a value into data as templates have them, as JSON would.
func normalize(v any) any {
	switch v.(type) {
	case nil, string, bool, float64:
		return v
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	if json.Unmarshal(b, &out) != nil {
		return nil
	}
	return out
}

// execute runs the steps of a run and records how it ended.
func (r *run) execute(ctx context.Context) {
	x := &scope{run: r}
	err := x.steps(ctx, r.wf.Steps)
	var st *stop
	outcome, msg := Succeeded, ""
	switch {
	case errors.As(err, &st):
		if st.failed {
			outcome, msg = Failed, st.message
		}
	case ctx.Err() != nil && errors.Is(context.Cause(ctx), errCancelled):
		outcome, msg = Cancelled, "It was cancelled."
	case ctx.Err() != nil && errors.Is(context.Cause(ctx), context.DeadlineExceeded):
		outcome, msg = Failed, fmt.Sprintf("It took longer than %s.", maxRunTime)
	case ctx.Err() != nil:
		outcome, msg = Failed, "The master stopped during the run."
	case err != nil:
		outcome, msg = Failed, err.Error()
	}
	r.mu.Lock()
	if r.dropped > 0 {
		msg = strings.TrimSpace(msg + fmt.Sprintf(" %d more steps ran than the run keeps.", r.dropped))
	}
	r.mu.Unlock()
	attrs := []any{logging.Workflows, "workflow", r.wf.Name, "workflow_id", r.wf.ID, "run", r.id}
	switch outcome {
	case Failed:
		slog.Warn("A workflow failed", append(attrs, "err", msg)...)
	case Cancelled:
		slog.Info("A workflow was cancelled", attrs...)
	default:
		slog.Info("A workflow ran", attrs...)
	}
	r.svc.finish(r, outcome, database.Clip(msg, maxError))
}

var errCancelled = errors.New("cancelled")
