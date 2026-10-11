package operation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/auth"
	"github.com/QwikByte/noryx/internal/master/httpapi"
)

func spec() Spec {
	return Spec{Kind: "server.create", Subject: "Lobby", Steps: []string{"image", "container"}, Status: http.StatusCreated,
		Timeout: time.Minute, Visible: func(access.Grants) bool { return false }}
}

func run(ops *Operations, task Task) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	ops.Run(rec, httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", nil), spec(), task)
	return rec
}

// Operations that end right away are answered like ordinary requests.
func TestQuick(t *testing.T) {
	ops := New(time.Minute)
	rec := run(ops, func(context.Context) (any, error) { return map[string]string{"id": "s1"}, nil })
	if rec.Code != http.StatusCreated || rec.Body.String() != "{\"id\":\"s1\"}\n" {
		t.Fatalf("answer %d: %s", rec.Code, rec.Body)
	}
	rec = run(ops, func(context.Context) (any, error) {
		return nil, httpapi.Errorf(http.StatusConflict, "Port 25565 is used.")
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("answer %d: %s", rec.Code, rec.Body)
	}
	if err := ops.CheckIdle(); err != nil {
		t.Fatal(err)
	}
}

// Others are answered with 202 Accepted, and go on with their steps and progress; steps
// that weren't planned are added.
func TestBackground(t *testing.T) {
	ops := New(0)
	proceed := make(chan struct{})
	rec := run(ops, func(ctx context.Context) (any, error) {
		Step(ctx, "image")
		Count(ctx, 50, 100, "bytes")
		Step(ctx, "plugins")
		Target(ctx, "n1", "s1")
		<-proceed
		return "done", nil
	})
	var op Operation
	if err := json.Unmarshal(rec.Body.Bytes(), &op); err != nil || rec.Code != http.StatusAccepted || op.FinishedAt != nil {
		t.Fatalf("answer %d: %s", rec.Code, rec.Body)
	}
	if ops.CheckIdle() == nil {
		t.Fatal("an operation in progress allows restarting")
	}
	waitFor(t, ops, func(op Operation) bool { return op.ServerID == "s1" })
	got := ops.List(auth.User{}, access.Grants{})[0]
	if !slices.Equal(got.Steps, []string{"image", "container", "plugins"}) || got.Step != 2 || got.Done != 0 || got.Unit != "" {
		t.Fatalf("operation = %+v", got)
	}
	close(proceed)
	got = waitFor(t, ops, func(op Operation) bool { return op.FinishedAt != nil })
	if got.Result != "done" || got.Error != "" || got.User != "" {
		t.Fatalf("operation = %+v", got)
	}
}

// Users see the operations they started, and those of others they may see.
func TestList(t *testing.T) {
	ops := New(time.Minute)
	run(ops, func(context.Context) (any, error) { return nil, nil })
	if len(ops.List(auth.User{ID: 1}, access.Grants{})) != 0 {
		t.Fatal("another user sees the operation")
	}
	if len(ops.List(auth.User{ID: 1}, access.Admin())) != 0 {
		t.Fatal("the operation isn't visible to others, yet listed")
	}
	if len(ops.List(auth.User{}, access.Grants{})) != 1 {
		t.Fatal("the user doesn't see the own operation")
	}
}

type kept time.Duration

func (k kept) OperationsKept() time.Duration { return time.Duration(k) }

// Finished operations are listed as long as the settings say.
func TestKeep(t *testing.T) {
	ops := New(time.Minute)
	ops.KeepFor(kept(2 * time.Hour))
	run(ops, func(context.Context) (any, error) { return nil, nil })
	ops.mu.Lock()
	ops.ops[0].FinishedAt = new(time.Now().Add(-90 * time.Minute))
	ops.mu.Unlock()
	if len(ops.List(auth.User{}, access.Grants{})) != 1 {
		t.Fatal("an operation that ended 90 minutes ago isn't listed for 2 hours")
	}
	ops.KeepFor(kept(time.Hour))
	if len(ops.List(auth.User{}, access.Grants{})) != 0 {
		t.Fatal("an operation that ended 90 minutes ago is listed for an hour")
	}
}

// cancellable runs an operation that waits until it is cancelled, which those may cancel who
// may create servers everywhere; others may see it if visible.
func cancellable(ops *Operations, visible bool, task Task) {
	s := spec()
	s.Visible = func(access.Grants) bool { return visible }
	s.Cancel = access.Everywhere(access.ServersCreate)
	ops.Run(httptest.NewRecorder(), httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", nil), s, task)
}

// The user who started an operation may cancel it, and others who may do what it does; its
// context ends, and it ends as cancelled.
func TestCancel(t *testing.T) {
	ops := New(0)
	stopped := make(chan error, 1)
	cancellable(ops, true, func(ctx context.Context) (any, error) {
		<-ctx.Done()
		stopped <- context.Cause(ctx)
		return nil, ctx.Err()
	})
	id := ops.List(auth.User{}, access.Grants{})[0].ID
	if !ops.List(auth.User{}, access.Grants{})[0].Cancellable || ops.List(auth.User{ID: 1}, access.Grants{})[0].Cancellable || !ops.List(auth.User{ID: 1}, access.Admin())[0].Cancellable {
		t.Fatal("cancellable for the wrong users")
	}
	if _, err := ops.Cancel(t.Context(), id, auth.User{ID: 1}, access.Grants{}); status(err) != http.StatusForbidden {
		t.Fatalf("another user without the permission: %v", err)
	}
	if _, err := ops.Cancel(t.Context(), "unknown", auth.User{}, access.Grants{}); status(err) != http.StatusNotFound {
		t.Fatalf("unknown operation: %v", err)
	}
	op, err := ops.Cancel(t.Context(), id, auth.User{ID: 1}, access.Admin())
	if err != nil || !op.Cancelled || op.Cancellable {
		t.Fatalf("cancel = %+v, %v", op, err)
	}
	if err := <-stopped; !errors.Is(err, errCancelled) {
		t.Fatalf("cause = %v", err)
	}
	got := waitFor(t, ops, func(op Operation) bool { return op.FinishedAt != nil })
	if !got.Cancelled || got.Error != "The operation was cancelled." || got.Cancellable {
		t.Fatalf("operation = %+v", got)
	}
	if _, err := ops.Cancel(t.Context(), id, auth.User{}, access.Grants{}); status(err) != http.StatusConflict {
		t.Fatalf("cancelling an ended operation: %v", err)
	}

	// The user who started it may cancel it without the permission; others who don't see it can't.
	cancellable(ops, false, func(ctx context.Context) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	id = ops.List(auth.User{}, access.Grants{})[0].ID
	if _, err := ops.Cancel(t.Context(), id, auth.User{ID: 1}, access.Admin()); status(err) != http.StatusNotFound {
		t.Fatalf("a user who doesn't see it: %v", err)
	}
	if _, err := ops.Cancel(t.Context(), id, auth.User{}, access.Grants{}); err != nil {
		t.Fatal(err)
	}
	if got := waitFor(t, ops, func(op Operation) bool { return op.ID == id && op.FinishedAt != nil }); !got.Cancelled {
		t.Fatalf("operation = %+v", got)
	}
}

// Operations that don't stop safely refuse to be cancelled, as do those that came to a part
// they must finish, and those that were done before they noticed don't end as cancelled.
func TestUncancellable(t *testing.T) {
	ops := New(0)
	proceed := make(chan struct{})
	run(ops, func(context.Context) (any, error) {
		<-proceed
		return nil, nil
	})
	op := ops.List(auth.User{}, access.Admin())[0]
	if _, err := ops.Cancel(t.Context(), op.ID, auth.User{}, access.Admin()); op.Cancellable || status(err) != http.StatusConflict {
		t.Fatalf("cancellable = %v, cancel: %v", op.Cancellable, err)
	}
	close(proceed)
	waitFor(t, ops, func(op Operation) bool { return op.FinishedAt != nil })

	kept, proceed := make(chan struct{}), make(chan struct{})
	cancellable(ops, true, func(ctx context.Context) (any, error) {
		err := Keep(ctx)
		close(kept)
		<-proceed
		return "restarted", errors.Join(err, ctx.Err())
	})
	<-kept
	op = ops.List(auth.User{}, access.Grants{})[0]
	if _, err := ops.Cancel(t.Context(), op.ID, auth.User{}, access.Grants{}); op.Cancellable || status(err) != http.StatusConflict {
		t.Fatalf("cancellable = %v, cancel: %v", op.Cancellable, err)
	}
	close(proceed)
	if got := waitFor(t, ops, func(op Operation) bool { return op.FinishedAt != nil }); got.Cancelled || got.Error != "" {
		t.Fatalf("operation = %+v", got)
	}

	// Cancelled before it kept the rest, it stops there.
	cancelled, proceed := make(chan struct{}), make(chan struct{})
	cancellable(ops, true, func(ctx context.Context) (any, error) {
		<-cancelled
		return nil, Keep(ctx)
	})
	op = ops.List(auth.User{}, access.Grants{})[0]
	if _, err := ops.Cancel(t.Context(), op.ID, auth.User{}, access.Grants{}); err != nil {
		t.Fatal(err)
	}
	close(cancelled)
	if got := waitFor(t, ops, func(op Operation) bool { return op.FinishedAt != nil }); !got.Cancelled {
		t.Fatalf("operation = %+v", got)
	}

	// Done before it noticed, it isn't cancelled.
	cancellable(ops, true, func(context.Context) (any, error) {
		<-proceed
		return "done", nil
	})
	op = ops.List(auth.User{}, access.Grants{})[0]
	if _, err := ops.Cancel(t.Context(), op.ID, auth.User{}, access.Grants{}); err != nil {
		t.Fatal(err)
	}
	close(proceed)
	if got := waitFor(t, ops, func(op Operation) bool { return op.FinishedAt != nil }); got.Cancelled || got.Result != "done" {
		t.Fatalf("operation = %+v", got)
	}
}

// Once an operation is cancelled, Each begins no more servers, but those it began finish.
func TestEachCancelled(t *testing.T) {
	ops := New(0)
	began, finish := make(chan struct{}), make(chan struct{})
	cancellable(ops, true, func(ctx context.Context) (any, error) {
		results := make([]string, PerNode+1)
		Each(ctx, slices.Repeat([]string{"n1"}, PerNode+1), func(ctx context.Context, i int) {
			began <- struct{}{}
			<-finish
			results[i] = "done"
			if ctx.Err() != nil {
				results[i] = "cut short"
			}
		}, func(i int, err error) { results[i] = httpapi.Message(err) })
		return results, nil
	})
	for range PerNode {
		<-began
	}
	if _, err := ops.Cancel(t.Context(), ops.List(auth.User{}, access.Grants{})[0].ID, auth.User{}, access.Grants{}); err != nil {
		t.Fatal(err)
	}
	close(finish)
	got := waitFor(t, ops, func(op Operation) bool { return op.FinishedAt != nil })
	results, _ := got.Result.([]string)
	slices.Sort(results)
	if want := append([]string{"The operation was cancelled before it got to this server."}, slices.Repeat([]string{"done"}, PerNode)...); !got.Cancelled || !slices.Equal(results, want) {
		t.Fatalf("operation = %+v", got)
	}
}

func status(err error) int {
	var apiErr *httpapi.Error
	if !errors.As(err, &apiErr) {
		return 0
	}
	return apiErr.Status
}

func waitFor(t *testing.T, ops *Operations, ok func(Operation) bool) Operation {
	t.Helper()
	for range 500 {
		if list := ops.List(auth.User{}, access.Grants{}); len(list) > 0 && ok(list[0]) {
			return list[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the operation didn't get there")
	return Operation{}
}
