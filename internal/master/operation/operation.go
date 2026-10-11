// Package operation runs long actions of the panel in the background, e.g. creating a server,
// which may download its image first. The panel follows their steps and progress, and learns
// how they ended, also if the browser went away meanwhile or a proxy in front of the master
// gave up waiting. Operations whose steps can stop safely can be cancelled. Operations live
// in memory: the master shows those that ended within the time the settings say.
package operation

import (
	"context"
	"crypto/rand"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/auth"
	"github.com/QwikByte/noryx/internal/master/httpapi"
)

// maxKept is the most finished operations listed.
const maxKept = 200

// Config tells how long finished operations are listed. It can change at any time.
type Config interface {
	OperationsKept() time.Duration
}

// Operation is an action in progress, or one that ended recently.
type Operation struct {
	ID string `json:"id"`
	// Kind tells what the operation does, e.g. server.create; the panel names it.
	Kind string `json:"kind"`
	// Subject is what it is about, e.g. the name of the server it creates.
	Subject   string `json:"subject"`
	NodeID    string `json:"nodeId,omitempty"`
	ServerID  string `json:"serverId,omitempty"`
	NetworkID string `json:"networkId,omitempty"`
	// User is who started it.
	User string `json:"user"`
	// Steps are the steps it takes, and Step the index of the one it is at, or failed at.
	Steps []string `json:"steps"`
	Step  int      `json:"step"`
	// Done and Total measure the current step in Unit, e.g. bytes, servers or the minutes of a
	// warning; Total is 0 if unknown.
	Done       int64      `json:"done"`
	Total      int64      `json:"total"`
	Unit       string     `json:"unit,omitempty"`
	Error      string     `json:"error,omitempty"`
	Result     any        `json:"result,omitempty"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	// Cancellable tells that the user it is shown to may cancel it now.
	Cancellable bool `json:"cancellable,omitempty"`
	// Cancelled tells that it was cancelled: it stops, or it stopped before it was done.
	// One that was done before it noticed isn't.
	Cancelled bool `json:"cancelled,omitempty"`
}

// Spec describes an operation to start.
type Spec struct {
	Kind, Subject               string
	NodeID, ServerID, NetworkID string
	// Steps it plans; steps it reports besides are added.
	Steps []string
	// Status answers a request whose operation ended right away, e.g. 201 Created.
	Status  int
	Timeout time.Duration
	// Visible tells whether users other than the one who started it may see it.
	Visible func(access.Grants) bool
	// Cancel, if set, lets it be cancelled until its task calls Keep: by the user who started
	// it, and by others whose grants it allows the request that started it, like the
	// permission of a route. Only operations whose steps stop safely set it.
	Cancel access.Need
	// Category is that of its entry in the log, if it ends after the request was answered.
	Category slog.Attr
}

// errCancelled ends the operations that were cancelled.
var errCancelled = &httpapi.Error{Status: http.StatusConflict, Message: "The operation was cancelled.", Code: "cancelled"}

// Task does the work of an operation. Its context carries the operation, to report progress.
type Task func(ctx context.Context) (result any, err error)

type entry struct {
	Operation
	owner    owner
	starter  []slog.Attr // names who started it in the log
	visible  func(access.Grants) bool
	category slog.Attr
	err      error
	answered bool // the request was answered before the operation ended
	done     chan struct{}
	cancel   context.CancelCauseFunc
	// mayCancel tells whether users other than the one who started it may cancel it, and
	// which permission they lack if not; nil once nobody may.
	mayCancel func(access.Grants) (access.Permission, bool)
	skipped   bool // Each left servers out, as it was cancelled
}

// Operations are the operations in progress and those that ended recently.
type Operations struct {
	quick time.Duration
	keep  func() time.Duration // how long finished operations are listed
	mu    sync.Mutex
	ops   []*entry // oldest first
}

// New returns the operations. Requests whose operation ends within quick are answered like
// ordinary requests, with the result or the error; 0 always answers 202 Accepted at once.
// Finished operations are listed for an hour, unless KeepFor says otherwise.
func New(quick time.Duration) *Operations {
	return &Operations{quick: quick, keep: func() time.Duration { return time.Hour }}
}

// KeepFor lists finished operations for as long as conf says, still at most maxKept. Call it
// before Run.
func (o *Operations) KeepFor(conf Config) { o.keep = conf.OperationsKept }

// Run starts an operation and answers the request: like an ordinary request if the
// operation ends within quick, otherwise with 202 Accepted and the operation, which goes on.
func (o *Operations) Run(w http.ResponseWriter, r *http.Request, spec Spec, task Task) {
	user, _ := auth.UserFrom(r.Context())
	ctx, stop := context.WithTimeout(context.WithoutCancel(r.Context()), spec.Timeout)
	ctx, cancel := context.WithCancelCause(ctx)
	e := &entry{
		Operation: Operation{
			ID: strings.ToLower(rand.Text()), Kind: spec.Kind, Subject: spec.Subject, NodeID: spec.NodeID, ServerID: spec.ServerID,
			NetworkID: spec.NetworkID, User: user.Username, Steps: slices.Clone(spec.Steps), StartedAt: time.Now(),
		},
		owner: ownerOf(user), starter: user.LogAttrs(), visible: spec.Visible, category: spec.Category, done: make(chan struct{}), cancel: cancel,
		answered: o.quick == 0, // also if the operation ends before the answer
	}
	if spec.Cancel != nil {
		e.mayCancel = func(g access.Grants) (access.Permission, bool) { return spec.Cancel(r, g) }
	}
	o.add(e)
	go func() {
		defer stop()
		result, err := task(context.WithValue(ctx, key{}, &reporter{o, e}))
		o.finish(e, result, err)
	}()

	if o.quick > 0 {
		select {
		case <-e.done:
		case <-time.After(o.quick):
		}
	}
	o.mu.Lock()
	finished, view := o.quick > 0 && e.FinishedAt != nil, e.view(e.owner, access.From(r.Context()))
	e.answered = !finished
	o.mu.Unlock()
	switch {
	case !finished:
		logging.Note(r.Context(), slog.String("operation", e.ID))
		httpapi.WriteJSON(w, http.StatusAccepted, view)
	case e.err != nil:
		httpapi.WriteError(w, r, e.err)
	case spec.Status == http.StatusNoContent:
		w.WriteHeader(http.StatusNoContent)
	default:
		httpapi.WriteJSON(w, spec.Status, e.Result)
	}
}

func (o *Operations) add(e *entry) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.prune()
	o.ops = append(o.ops, e)
}

// prune forgets the finished operations that are no longer listed, while o.mu is held.
func (o *Operations) prune() {
	finished, keep := 0, o.keep()
	for _, old := range o.ops {
		if old.FinishedAt != nil {
			finished++
		}
	}
	o.ops = slices.DeleteFunc(o.ops, func(old *entry) bool {
		gone := old.FinishedAt != nil && (time.Since(*old.FinishedAt) > keep || finished > maxKept)
		if gone {
			finished--
		}
		return gone
	})
}

// finish records how an operation ended, and logs it if the request was answered before.
// One that was cancelled ends as cancelled, unless it was done before it noticed.
func (o *Operations) finish(e *entry, result any, err error) {
	o.mu.Lock()
	now := time.Now()
	if e.Cancelled = e.Cancelled && (err != nil || e.skipped); e.Cancelled {
		err = errCancelled
	}
	e.FinishedAt, e.Result, e.err, e.mayCancel = &now, result, err, nil
	if err != nil {
		e.Error = httpapi.Message(err)
	}
	answered, cancelled := e.answered, e.Cancelled
	o.mu.Unlock()
	close(e.done)
	if !answered {
		return
	}
	attrs := []any{e.category, "operation", e.ID, "kind", e.Kind, "subject", e.Subject}
	for _, a := range e.starter {
		attrs = append(attrs, a)
	}
	if e.NodeID != "" {
		attrs = append(attrs, logging.KeyNode, e.NodeID)
	}
	if e.ServerID != "" {
		attrs = append(attrs, logging.KeyServer, e.ServerID)
	}
	switch {
	case cancelled:
		slog.Info("Operation cancelled", attrs...)
	case err != nil:
		slog.Warn("Operation failed", append(attrs, "err", e.Error)...)
	default:
		slog.Info("Operation finished", attrs...)
	}
}

// owner is who started an operation: a user in the panel, or with one of the user's API
// tokens, which may have fewer permissions than the user and so doesn't own the others.
type owner struct {
	user  int64
	token string
}

func ownerOf(user auth.User) owner {
	o := owner{user: user.ID}
	if user.Token != nil {
		o.token = user.Token.ID
	}
	return o
}

// view returns a copy of an operation for whom, while o.mu is held.
func (e *entry) view(whom owner, grants access.Grants) Operation {
	op := e.Operation
	op.Steps = slices.Clone(e.Steps)
	op.Cancellable = !e.Cancelled && e.checkCancel(whom, grants) == nil
	return op
}

// sees reports whether whom sees an operation: one whom started, or one of others about
// what whom may see.
func (e *entry) sees(whom owner, grants access.Grants) bool {
	return e.owner == whom || e.visible(grants)
}

// checkCancel returns why whom can't cancel an operation, or nil, while o.mu is held.
func (e *entry) checkCancel(whom owner, grants access.Grants) error {
	switch {
	case e.FinishedAt != nil:
		return httpapi.Errorf(http.StatusConflict, "The operation has ended already.")
	case e.mayCancel == nil:
		return httpapi.Errorf(http.StatusConflict, "The operation can't be cancelled, as what it does now must finish once it began.")
	case e.owner == whom:
		return nil
	}
	if p, ok := e.mayCancel(grants); !ok {
		return access.Denied(p)
	}
	return nil
}

// List returns the operations a user may see, the newest first: those the user started,
// and those of others about what the user may see. Those that an API token started are the
// token's, not its user's.
func (o *Operations) List(user auth.User, grants access.Grants) []Operation {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.prune()
	whom, ops := ownerOf(user), []Operation{}
	for _, e := range slices.Backward(o.ops) {
		if e.sees(whom, grants) {
			ops = append(ops, e.view(whom, grants))
		}
	}
	return ops
}

// Cancel cancels an operation that a user sees, if the user may: the user who started it,
// or one whom its Spec allows. It stops at its next step that can stop, and calls to agents
// stop with it; then it ends as cancelled. Cancelling it again changes nothing. The log
// entry of ctx notes what it is.
func (o *Operations) Cancel(ctx context.Context, id string, user auth.User, grants access.Grants) (Operation, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	whom := ownerOf(user)
	i := slices.IndexFunc(o.ops, func(e *entry) bool { return e.ID == id && e.sees(whom, grants) })
	if i < 0 {
		return Operation{}, httpapi.Errorf(http.StatusNotFound, "Operation not found.")
	}
	e := o.ops[i]
	logging.Note(ctx, e.category, slog.String("kind", e.Kind), slog.String("subject", e.Subject), slog.String("started_by", e.User))
	if err := e.checkCancel(whom, grants); err != nil {
		return Operation{}, err
	}
	if !e.Cancelled {
		e.Cancelled = true
		e.cancel(errCancelled)
	}
	return e.view(whom, grants), nil
}

// CheckIdle refuses to cut off operations in progress, e.g. by restarting the master.
func (o *Operations) CheckIdle() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, e := range o.ops {
		if e.FinishedAt == nil {
			return httpapi.Errorf(http.StatusConflict, "%s isn't finished yet. Try again when it is done.", e.Subject)
		}
	}
	return nil
}

type key struct{}

// reporter changes the progress of an operation for its task.
type reporter struct {
	o *Operations
	e *entry
}

func from(ctx context.Context) *reporter {
	r, _ := ctx.Value(key{}).(*reporter)
	return r
}

func (r *reporter) update(change func(op *Operation)) {
	r.o.mu.Lock()
	defer r.o.mu.Unlock()
	change(&r.e.Operation)
}

// Step moves the operation of a task to a step, which starts with nothing done. A step it
// didn't plan is added.
func Step(ctx context.Context, step string) {
	if r := from(ctx); r != nil {
		r.update(func(op *Operation) {
			i := slices.Index(op.Steps, step)
			if i < 0 {
				op.Steps = append(op.Steps, step)
				i = len(op.Steps) - 1
			}
			if i != op.Step {
				op.Step, op.Done, op.Total, op.Unit = i, 0, 0, ""
			}
		})
	}
}

// Count tells how much of the current step of an operation is done, and of how much, in unit.
func Count(ctx context.Context, done, total int64, unit string) {
	if r := from(ctx); r != nil {
		r.update(func(op *Operation) { op.Done, op.Total, op.Unit = done, total, unit })
	}
}

// Keep makes the rest of the operation of ctx uncancellable, as it must finish once it
// begins, e.g. restarting servers. It fails if the operation was cancelled already.
func Keep(ctx context.Context) error {
	if r := from(ctx); r != nil {
		r.o.mu.Lock()
		defer r.o.mu.Unlock()
		if r.e.Cancelled {
			return errCancelled
		}
		r.e.mayCancel = nil
	}
	return nil
}

// Target names the server an operation is about once it is known, e.g. a server it created.
func Target(ctx context.Context, nodeID, serverID string) {
	if r := from(ctx); r != nil {
		r.update(func(op *Operation) { op.NodeID, op.ServerID = nodeID, serverID })
	}
}
