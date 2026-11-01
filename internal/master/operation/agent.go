package operation

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
)

var calls atomic.Int64

// patience is how long following a call may hold it up at its start, and wait for its last
// progress at its end.
const patience = 2 * time.Second

// Agent prepares a call to the agent of conn whose progress belongs to the operation of
// ctx: it returns the context for the call, and stop, which stops following the call once
// it returned. If the agent can't follow the call, the operation shows only its steps.
func Agent(ctx context.Context, conn grpc.ClientConnInterface) (call context.Context, stop func()) {
	r := from(ctx)
	if r == nil {
		return ctx, func() {}
	}
	id := fmt.Sprintf("%s-%d", r.e.ID, calls.Add(1))
	watch, cancel := context.WithCancel(ctx)
	following, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		stream, err := noryxv1.NewProgressServiceClient(conn).WatchProgress(watch, &noryxv1.WatchProgressRequest{OperationId: id})
		if err == nil {
			_, err = stream.Header() // the agent follows the call now, or can't
		}
		close(following)
		for err == nil {
			var p *noryxv1.WatchProgressResponse
			if p, err = stream.Recv(); err == nil {
				Step(ctx, p.GetStep())
				Count(ctx, p.GetDone(), p.GetTotal(), "bytes")
			}
		}
	}()
	select {
	case <-following:
	case <-time.After(patience):
	}
	// The agent ends the stream with the last progress of the call. Progress that arrives
	// after stop must not take the operation back to an earlier step.
	return metadata.AppendToOutgoingContext(ctx, noryxv1.OperationKey, id), func() {
		select {
		case <-done:
		case <-time.After(patience):
		}
		cancel()
		<-done
	}
}
