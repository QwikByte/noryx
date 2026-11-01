package httpapi

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// logTail is how many past lines a browser gets first, and maxTail how many at most of what
	// it missed when it connects again.
	logTail = 300
	maxTail = 1000
	// A log stream ends after maxStream with a "renew" event, so that the browser connects
	// again, which checks the session and the permissions again.
	maxStream = 5 * time.Minute
)

// lineBreaks would let a log line forge additional events in the stream.
var lineBreaks = strings.NewReplacer("\r", " ", "\n", " ")

// LogLine is a message of a stream from an agent that carries a line of a log and when it was
// written, in nanoseconds since 1970, or 0 if unknown.
type LogLine interface {
	GetLine() string
	GetTimeUnixNano() int64
}

// Stream is a stream of messages from an agent.
type Stream[T any] interface{ Recv() (T, error) }

// StreamLog streams a log from an agent as Server-Sent Events, one line per event, whose ID
// is the time the line was written. open asks the agent for the last tail lines written after
// the time after, if it isn't 0, and to follow the log. A browser that connects again with the
// ID of the last line it got, as Last-Event-ID or after, continues after it. An "end" event
// tells the browser not to connect again, e.g. because the server stopped.
func StreamLog[T LogLine](w http.ResponseWriter, r *http.Request, open func(ctx context.Context, tail uint32, after int64) (Stream[T], error)) {
	after, err := strconv.ParseInt(cmp.Or(r.Header.Get("Last-Event-ID"), r.URL.Query().Get("after"), "0"), 10, 64)
	if err != nil {
		WriteError(w, r, Errorf(http.StatusBadRequest, "The log can't continue after that line."))
		return
	}
	tail := uint32(logTail)
	if after > 0 {
		tail = maxTail
	}
	ctx, cancel := context.WithTimeout(r.Context(), maxStream)
	defer cancel()
	stream, err := open(ctx, tail, after)
	var res T
	if err == nil {
		// Errors such as an unknown server arrive with the first message, before any output.
		res, err = stream.Recv()
	}
	if err != nil && !errors.Is(err, io.EOF) {
		WriteError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no") // keeps nginx from buffering the stream
	flusher := http.NewResponseController(w)
	for ; err == nil; res, err = stream.Recv() {
		if t := res.GetTimeUnixNano(); t > 0 {
			fmt.Fprintf(w, "id: %d\n", t)
		}
		// Plain text for EventSource, served with nosniff and rendered as text by the panel.
		fmt.Fprintf(w, "data: %s\n\n", lineBreaks.Replace(res.GetLine())) //nolint:gosec // not HTML, see above
		if flusher.Flush() != nil {
			return
		}
	}
	switch {
	case errors.Is(err, io.EOF):
		fmt.Fprint(w, "event: end\ndata:\n\n")
	case r.Context().Err() == nil && errors.Is(ctx.Err(), context.DeadlineExceeded):
		fmt.Fprint(w, "event: renew\ndata:\n\n")
	}
	_ = flusher.Flush()
}
