package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/database"
)

const (
	maxItems       = 1000
	maxConcurrency = 10
	maxRepeat      = 1000
	maxDelay       = 3600
	maxWait        = 24 * time.Hour
	maxCalls       = 5
)

var timeOfDay = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

// settings are those of a kind of step, which check validates and normalizes.
type settings interface{ check() error }

// kind is a kind of step: how it reads its settings, what it needs and how it runs, and
// which steps it has within.
type kind struct {
	decode func(raw json.RawMessage) (settings, error)
	needs  func(settings) []access.Permission
	run    func(ctx context.Context, x *scope, s Step, set settings) (any, error)

	steps, elses, cases, branches bool
}

// needer is implemented by settings that need permissions beyond those of their kind,
// depending on what they do.
type needer interface{ needs() []access.Permission }

// define returns a kind with settings of type T, which needs the permissions on all servers.
func define[T any, P interface {
	*T
	settings
}](needs []access.Permission, run func(ctx context.Context, x *scope, s Step, set P) (any, error)) kind {
	return kind{
		decode: func(raw json.RawMessage) (settings, error) {
			p := P(new(T))
			if len(raw) > 0 && string(raw) != "null" {
				if err := json.Unmarshal(raw, p); err != nil {
					return nil, bad("Its settings are invalid.")
				}
			}
			return p, p.check()
		},
		needs: func(s settings) []access.Permission {
			perms := slices.Clone(needs)
			if n, ok := s.(needer); ok {
				perms = append(perms, n.needs()...)
			}
			return perms
		},
		run: func(ctx context.Context, x *scope, s Step, set settings) (any, error) { return run(ctx, x, s, set.(P)) },
	}
}

// with returns a kind with steps within it.
func (k kind) with(steps, elses, cases, branches bool) kind {
	k.steps, k.elses, k.cases, k.branches = steps, elses, cases, branches
	return k
}

// kinds are the kinds of steps by name.
var kinds map[string]kind

func init() {
	kinds = map[string]kind{
		"if":        define(nil, runIf).with(true, true, false, false),
		"switch":    define(nil, runSwitch).with(false, true, true, false),
		"foreach":   define(nil, runForeach).with(true, false, false, false),
		"repeat":    define(nil, runRepeat).with(true, false, false, false),
		"parallel":  define(nil, runParallel).with(false, false, false, true),
		"try":       define(nil, runTry).with(true, true, false, false),
		"wait":      define(nil, runWait),
		"set":       define(nil, runSet),
		"terminate": define(nil, runTerminate),
		"call":      define(nil, runCall),
	}
	for name, k := range actionKinds() {
		kinds[name] = k
	}
}

// none are the settings of kinds without any.
type none struct{}

func (*none) check() error { return nil }

type ifSettings struct {
	Condition Condition `json:"condition"`
}

func (s *ifSettings) check() error { return checked(s.Condition.check(1)) }

// checked makes the error of a condition one for the panel.
func checked(err error) error {
	if err != nil {
		return bad("%s%s.", strings.ToUpper(err.Error()[:1]), err.Error()[1:])
	}
	return nil
}

func runIf(ctx context.Context, x *scope, s Step, set *ifSettings) (any, error) {
	ok, err := x.holds(&set.Condition)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"result": ok, "detail": "The condition held."}
	branch := s.Steps
	if !ok {
		out["detail"], branch = "The condition didn't hold.", s.Else
	}
	return out, x.steps(ctx, branch)
}

type switchSettings struct {
	Value string `json:"value"`
}

func (s *switchSettings) check() error {
	if strings.TrimSpace(s.Value) == "" {
		return bad("Enter the value to compare.")
	}
	return nil
}

func runSwitch(ctx context.Context, x *scope, s Step, set *switchSettings) (any, error) {
	v, err := x.value(set.Value)
	if err != nil {
		return nil, err
	}
	for _, c := range s.Cases {
		cv, err := x.value(c.Value)
		if err != nil {
			return nil, err
		}
		if equal(v, cv) {
			return map[string]any{"case": text(cv), "detail": fmt.Sprintf("The case %q ran.", database.Clip(text(cv), 100))}, x.steps(ctx, c.Steps)
		}
	}
	return map[string]any{"case": nil, "detail": "No case matched, the default ran."}, x.steps(ctx, s.Else)
}

type foreachSettings struct {
	// Items is a template whose value is a list, e.g. {{steps.find.servers}}.
	Items string `json:"items"`
	// Concurrency is how many items run at a time.
	Concurrency int `json:"concurrency"`
}

func (s *foreachSettings) check() error {
	if s.Concurrency == 0 {
		s.Concurrency = 1
	}
	switch {
	case strings.TrimSpace(s.Items) == "":
		return bad("Choose what to go through, e.g. the servers of an earlier step.")
	case s.Concurrency < 1 || s.Concurrency > maxConcurrency:
		return bad("Go through 1 to %d items at a time.", maxConcurrency)
	}
	return nil
}

// runForeach runs its steps for each item, as {{item}} and {{index}} from 0, a few at a time.
// Once an item fails, it begins no more.
func runForeach(ctx context.Context, x *scope, s Step, set *foreachSettings) (any, error) {
	v, err := x.value(set.Items)
	if err != nil {
		return nil, err
	}
	list := items(v)
	if len(list) > maxItems {
		return nil, bad("There are %d items, more than the %d a loop goes through.", len(list), maxItems)
	}
	var (
		mu    sync.Mutex
		first error
		wg    sync.WaitGroup
		slots = make(chan struct{}, set.Concurrency)
	)
	for i, item := range list {
		slots <- struct{}{}
		mu.Lock()
		stop := first != nil
		mu.Unlock()
		if stop || ctx.Err() != nil {
			<-slots
			break
		}
		wg.Go(func() {
			defer func() { <-slots }()
			if err := x.child(map[string]any{"item": item, "index": float64(i)}).steps(ctx, s.Steps); err != nil {
				mu.Lock()
				first = cmpErr(first, err)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	return map[string]any{"count": len(list), "detail": fmt.Sprintf("It went through %d items.", len(list))}, first
}

// cmpErr keeps the first error, unless a later one stops the run.
func cmpErr(first, err error) error {
	var st *stop
	if first == nil || errors.As(err, &st) {
		return err
	}
	return first
}

type repeatSettings struct {
	// Until ends the loop once it holds after the steps.
	Until Condition `json:"until"`
	Max   int       `json:"max"`
	// Delay is the seconds between the times.
	Delay int `json:"delay"`
}

func (s *repeatSettings) check() error {
	if s.Max == 0 {
		s.Max = 10
	}
	switch {
	case s.Max < 1 || s.Max > maxRepeat:
		return bad("Repeat 1 to %d times.", maxRepeat)
	case s.Delay < 0 || s.Delay > maxDelay:
		return bad("Wait 0 to %d seconds between the times.", maxDelay)
	}
	return checked(s.Until.check(1))
}

// runRepeat runs its steps again and again, as {{index}} from 0, until its condition holds
// after them, at most Max times.
func runRepeat(ctx context.Context, x *scope, s Step, set *repeatSettings) (any, error) {
	for i := range set.Max {
		if i > 0 && sleep(ctx, time.Duration(set.Delay)*time.Second) != nil {
			return nil, ctx.Err()
		}
		inner := x.child(map[string]any{"index": float64(i)})
		if err := inner.steps(ctx, s.Steps); err != nil {
			return map[string]any{"count": i + 1, "done": false}, err
		}
		done, err := inner.holds(&set.Until)
		if err != nil || done {
			return map[string]any{"count": i + 1, "done": done, "detail": fmt.Sprintf("The condition held after %d times.", i+1)}, err
		}
	}
	return map[string]any{"count": set.Max, "done": false, "detail": fmt.Sprintf("The condition didn't hold after %d times.", set.Max)}, nil
}

// runParallel runs its branches at the same time and waits for all of them.
func runParallel(ctx context.Context, x *scope, s Step, _ *none) (any, error) {
	var (
		mu    sync.Mutex
		first error
		wg    sync.WaitGroup
	)
	for _, b := range s.Branches {
		wg.Go(func() {
			if err := x.child(nil).steps(ctx, b); err != nil {
				mu.Lock()
				first = cmpErr(first, err)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	return map[string]any{"detail": fmt.Sprintf("%d branches ran.", len(s.Branches))}, first
}

// runTry runs its steps, and the steps of its catch if one of them fails, with {{error}}.
func runTry(ctx context.Context, x *scope, s Step, _ *none) (any, error) {
	err := x.child(nil).steps(ctx, s.Steps)
	var st *stop
	var f *failure
	switch {
	case err == nil:
		return map[string]any{"failed": false}, nil
	case errors.As(err, &st), ctx.Err() != nil:
		return nil, err
	case errors.As(err, &f):
		caught := map[string]any{"message": f.Error(), "step": f.step.ID}
		out := map[string]any{"failed": true, "caught": caught, "detail": "Caught: " + f.Error()}
		return out, x.child(map[string]any{"error": caught}).steps(ctx, s.Else)
	}
	return nil, err
}

type waitSettings struct {
	// For is a template of a number of Units: seconds, minutes or hours.
	For  string `json:"for,omitempty"`
	Unit string `json:"unit,omitempty"`
	// Until is a time of day like 22:00, in the time zone of the workflow.
	Until string `json:"until,omitempty"`
}

var units = map[string]time.Duration{"seconds": time.Second, "minutes": time.Minute, "hours": time.Hour}

func (s *waitSettings) check() error {
	s.For, s.Until = strings.TrimSpace(s.For), strings.TrimSpace(s.Until)
	switch {
	case (s.For == "") == (s.Until == ""):
		return bad("Wait for a time, or until a time of day.")
	case s.For != "" && units[s.Unit] == 0:
		return bad("Choose seconds, minutes or hours.")
	case s.Until != "" && !strings.Contains(s.Until, "{{") && !timeOfDay.MatchString(s.Until):
		return bad("Enter a time of day like 22:00.")
	}
	if s.Until != "" {
		s.Unit = ""
	}
	return nil
}

func runWait(ctx context.Context, x *scope, _ Step, set *waitSettings) (any, error) {
	var d time.Duration
	if set.For != "" {
		v, err := x.value(set.For)
		if err != nil {
			return nil, err
		}
		n, ok := number(v)
		if !ok || n < 0 {
			return nil, bad("%q is no time to wait.", text(v))
		}
		d = time.Duration(n * float64(units[set.Unit]))
	} else {
		at, err := x.text(set.Until)
		if err != nil {
			return nil, err
		}
		if !timeOfDay.MatchString(at) {
			return nil, bad("%q is no time of day like 22:00.", at)
		}
		d = untilTime(time.Now().In(x.run.loc), at)
	}
	if d > maxWait {
		return nil, bad("A step waits up to %s.", maxWait)
	}
	if err := sleep(ctx, d); err != nil {
		return nil, err
	}
	return map[string]any{"detail": fmt.Sprintf("It waited %s.", d.Round(time.Second))}, nil
}

// untilTime is how long it is from now to the next time of day hh:mm.
func untilTime(now time.Time, hm string) time.Duration {
	var h, m int
	_, _ = fmt.Sscanf(hm, "%d:%d", &h, &m)
	at := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
	if !at.After(now) {
		at = time.Date(now.Year(), now.Month(), now.Day()+1, h, m, 0, 0, now.Location())
	}
	return at.Sub(now)
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

type setSettings struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	// Mode is set, append to a list, or add to a number.
	Mode string `json:"mode"`
}

func (s *setSettings) check() error {
	if s.Mode == "" {
		s.Mode = "set"
	}
	switch {
	case !namePattern.MatchString(s.Name):
		return bad("Name the variable with up to 32 letters, digits and underscores, starting with a letter.")
	case s.Mode != "set" && s.Mode != "append" && s.Mode != "add":
		return bad("Choose whether to set, append or add.")
	}
	return nil
}

// runSet sets a variable, {{vars.name}}, appends to it as a list, or adds to it as a number.
func runSet(_ context.Context, x *scope, _ Step, set *setSettings) (any, error) {
	v, err := x.value(set.Value)
	if err != nil {
		return nil, err
	}
	v = normalize(v)
	r := x.run
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.vars[set.Name]
	switch set.Mode {
	case "append":
		list, _ := old.([]any)
		if old != nil && list == nil {
			list = []any{old}
		}
		if len(list) >= maxItems {
			return nil, bad("The list %s has %d items already.", set.Name, maxItems)
		}
		v = append(append([]any{}, list...), v)
	case "add":
		a, _ := number(old)
		b, ok := number(v)
		if !ok {
			return nil, bad("%q is no number.", text(v))
		}
		v = a + b
	}
	if b, _ := json.Marshal(v); len(b) > maxText {
		return nil, bad("The variable %s would be longer than %d KB.", set.Name, maxText>>10)
	}
	r.vars[set.Name] = v
	return map[string]any{"value": v}, nil
}

type terminateSettings struct {
	// Failed ends the run as failed rather than succeeded.
	Failed  bool   `json:"failed"`
	Message string `json:"message"`
	// Result is what a workflow that called this one gets.
	Result string `json:"result"`
}

func (*terminateSettings) check() error { return nil }

func runTerminate(_ context.Context, x *scope, _ Step, set *terminateSettings) (any, error) {
	msg, err := x.text(set.Message)
	var result any
	if err == nil {
		result, err = x.value(set.Result)
	}
	if err != nil {
		return nil, err
	}
	if msg == "" && set.Failed {
		msg = "A step ended it as failed."
	}
	x.run.mu.Lock()
	x.run.result = normalize(result)
	x.run.mu.Unlock()
	return map[string]any{"result": result}, &stop{failed: set.Failed, message: database.Clip(msg, maxError)}
}

type callSettings struct {
	Workflow string            `json:"workflow"`
	Inputs   map[string]string `json:"inputs"`
	// Wait waits for the run to end, so that its result and outcome are known.
	Wait bool `json:"wait"`
}

func (s *callSettings) check() error {
	if !idPattern.MatchString(s.Workflow) {
		return bad("Choose a workflow.")
	}
	if len(s.Inputs) > maxParams {
		return bad("Pass up to %d inputs.", maxParams)
	}
	for name := range s.Inputs {
		if !namePattern.MatchString(name) {
			return bad("%q is no name of an input.", name)
		}
	}
	return nil
}

// runCall runs another workflow with inputs, and waits for it if it should.
func runCall(ctx context.Context, x *scope, _ Step, set *callSettings) (any, error) {
	if x.run.calls >= maxCalls {
		return nil, bad("Workflows call others up to %d deep.", maxCalls)
	}
	inputs := map[string]any{}
	for name, t := range set.Inputs {
		v, err := x.value(t)
		if err != nil {
			return nil, err
		}
		inputs[name] = v
	}
	return x.run.svc.call(ctx, x.run, set.Workflow, inputs, set.Wait)
}
