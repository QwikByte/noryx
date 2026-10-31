package sftp

import (
	"bytes"
	"cmp"
	"context"
	"io"
	"sync"
	"sync/atomic"

	"github.com/pkg/sftp"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
)

const (
	// readWindow is how far apart the parts of a file that a client asks for may be from where
	// the agent's stream is, and how much of what it read is kept for parts asked for late.
	// Clients ask for up to 32 KiB at a time, and SFTP serves 8 of their requests at once.
	readWindow = 512 << 10
	// maxEarly is how much of what a connection writes may wait for the parts before it.
	maxEarly = 8 << 20
)

var errOrder = statusError{sftp.ErrSSHFxOpUnsupported, "Files are written over SFTP from the start to the end in one go: resuming uploads and changing parts of files isn't possible."}

// stream is a stream of a file from an agent, see files.Service.Read.
type stream interface {
	Recv() (*noryxv1.ReadFileResponse, error)
}

// reader reads a file of a server for a client, which asks for parts of it at offsets: in
// order, but several at once, so that they arrive out of order. It reads the file from the
// agent as one stream, keeps what it read last for parts that come late, and reads the file
// from another offset only for clients that jump there.
type reader struct {
	ctx  context.Context
	open func(ctx context.Context, offset int64) (*noryxv1.ReadFileResponse, stream, error)
	done func() // once the file is closed

	mu     sync.Mutex
	stream stream
	stop   context.CancelFunc // ends the stream
	start  int64              // the offset of data
	data   []byte
	err    error // with which the stream ended
}

// newReader opens a file; errors such as a missing file arrive now.
func newReader(ctx context.Context, open func(context.Context, int64) (*noryxv1.ReadFileResponse, stream, error), done func()) (*reader, error) {
	r := &reader{ctx: ctx, open: open, done: done}
	return r, r.seek(0)
}

// seek reads the file from offset on.
func (r *reader) seek(offset int64) error {
	if r.stop != nil {
		r.stop()
	}
	ctx, stop := context.WithCancel(r.ctx)
	first, s, err := r.open(ctx, offset)
	if err != nil {
		stop()
		r.err = err // the stream before has ended
		return err
	}
	// Agents of older versions send the whole file, from the start.
	r.stream, r.stop, r.start, r.data, r.err = s, stop, first.GetOffset(), bytes.Clone(first.GetData()), nil
	return nil
}

func (r *reader) ReadAt(p []byte, off int64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if off < r.start || off > r.start+int64(len(r.data))+readWindow {
		if err := r.seek(off); err != nil {
			return 0, toStatus(err)
		}
	}
	for r.err == nil && r.start+int64(len(r.data)) < off+int64(len(p)) {
		msg, err := r.stream.Recv()
		r.data, r.err = append(r.data, msg.GetData()...), err
		// What lies far before the part asked for is dropped, a window at a time.
		if cut := min(off-readWindow-r.start, int64(len(r.data))); cut >= readWindow {
			r.data, r.start = append(r.data[:0], r.data[cut:]...), r.start+cut
		}
	}
	n := copy(p, r.data[min(off-r.start, int64(len(r.data))):])
	if n < len(p) {
		return n, toStatus(r.err)
	}
	return n, nil
}

func (r *reader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stop()
	r.done()
	return nil
}

// writer writes a file of a server for a client, which sends parts of it at offsets: in
// order, but several at once, so that they arrive out of order. It streams them to the agent
// in order, and keeps those that come early up to what the connection may keep. The agent
// writes the file once the client closes it; one that leaves a gap or goes away discards it.
type writer struct {
	early  *atomic.Int64 // what the parts that came early of the connection's files take
	pipe   *io.PipeWriter
	cancel context.CancelFunc // discards the file
	done   func(error)        // once the file is closed, with how it ended
	ended  chan struct{}
	err    error // of the agent, once ended

	mu     sync.Mutex
	at     int64 // how much was written
	parts  map[int64][]byte
	failed error
}

// newWriter starts to write a file with write, which gets the content to write.
func newWriter(ctx context.Context, early *atomic.Int64, write func(context.Context, io.Reader) error, done func(error)) *writer {
	ctx, cancel := context.WithCancel(ctx)
	r, pipe := io.Pipe()
	w := &writer{early: early, pipe: pipe, cancel: cancel, done: done, ended: make(chan struct{}), parts: map[int64][]byte{}}
	go func() {
		defer close(w.ended)
		w.err = write(ctx, r)
		r.CloseWithError(cmp.Or(w.err, io.ErrClosedPipe)) // ends writes the agent doesn't take
	}()
	return w
}

func (w *writer) WriteAt(p []byte, off int64) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch {
	case w.failed != nil:
		return 0, w.failed
	case off < w.at:
		return 0, w.fail(errOrder)
	case off > w.at:
		if old, ok := w.parts[off]; ok {
			w.early.Add(-int64(len(old)))
		}
		if w.early.Add(int64(len(p))) > maxEarly {
			w.early.Add(-int64(len(p)))
			delete(w.parts, off)
			return 0, w.fail(errOrder)
		}
		w.parts[off] = bytes.Clone(p) // p is the packet's, which SFTP reuses
		return len(p), nil
	}
	// The parts that came early follow, as long as they continue the file.
	for next, ok := p, true; ok; {
		if _, err := w.pipe.Write(next); err != nil {
			return 0, w.fail(toStatus(err))
		}
		w.at += int64(len(next))
		if next, ok = w.parts[w.at]; ok {
			delete(w.parts, w.at)
			w.early.Add(-int64(len(next)))
		}
	}
	return len(p), nil
}

// fail discards the file for err, and returns the error that discarded it.
func (w *writer) fail(err error) error {
	if w.failed == nil {
		w.failed = err
		for _, part := range w.parts {
			w.early.Add(-int64(len(part)))
		}
		clear(w.parts)
		w.pipe.CloseWithError(err)
		w.cancel()
	}
	return w.failed
}

// TransferError discards the file of a client that went away.
func (w *writer) TransferError(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.fail(err)
}

// Close writes the file, unless a part is missing or failed.
func (w *writer) Close() error {
	w.mu.Lock()
	if len(w.parts) > 0 {
		_ = w.fail(errOrder)
	}
	if w.failed == nil {
		w.pipe.Close()
	}
	failed := w.failed
	w.mu.Unlock()
	<-w.ended
	w.cancel()
	err := cmp.Or(failed, toStatus(w.err))
	w.done(err)
	return err
}
