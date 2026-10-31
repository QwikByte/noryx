package sftp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/pkg/sftp"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
)

// Folders have the names of their nodes and servers, unless these are no names for paths or
// would name two folders.
func TestNamed(t *testing.T) {
	const id = "aaaaaaaaaaaaaaaaaaaaaaaaaa"
	folders := named([]folder{
		{label: "Lobby", serverID: "lobby"}, {label: "Survival", serverID: "survival"}, {label: "survival", serverID: "other"},
		{label: "a/b", serverID: "slash"}, {label: "..", serverID: "dots"}, {label: "", serverID: "empty"},
		{label: "bad\nname", serverID: "control"}, {label: id, serverID: "id"},
	})
	var got []string
	for _, f := range folders {
		got = append(got, f.name)
	}
	if want := []string{"Lobby", "survival", "other", "slash", "dots", "empty", "control", "id"}; !slices.Equal(got, want) {
		t.Fatalf("names = %q, want %q", got, want)
	}
	for name, want := range map[string]string{"Lobby": "lobby", "survival": "survival", "other": "other", "Survival": ""} {
		if f, ok := find(folders, name); ok != (want != "") || f.serverID != want {
			t.Errorf("find(%q) = %+v, %v", name, f, ok)
		}
	}
}

// file is a file of an agent, which it sends from an offset in small messages.
type file struct {
	content []byte
	opened  []int64 // the offsets it was opened at
}

func (f *file) open(_ context.Context, offset int64) (*noryxv1.ReadFileResponse, stream, error) {
	f.opened = append(f.opened, offset)
	rest := f.content[min(offset, int64(len(f.content))):]
	s := &chunks{}
	for len(rest) > 0 {
		n := min(len(rest), 1000)
		s.msgs, rest = append(s.msgs, &noryxv1.ReadFileResponse{Data: rest[:n]}), rest[n:]
	}
	first := &noryxv1.ReadFileResponse{Offset: offset}
	if len(s.msgs) > 0 {
		first.Data, s.msgs = s.msgs[0].Data, s.msgs[1:]
	}
	return first, s, nil
}

type chunks struct{ msgs []*noryxv1.ReadFileResponse }

func (c *chunks) Recv() (*noryxv1.ReadFileResponse, error) {
	if len(c.msgs) == 0 {
		return nil, io.EOF
	}
	msg := c.msgs[0]
	c.msgs = c.msgs[1:]
	return msg, nil
}

// Reading serves parts that arrive out of order from one stream, and reads the file again only
// for parts far away.
func TestReader(t *testing.T) {
	f := &file{content: make([]byte, 4*readWindow)}
	for i := range f.content {
		f.content[i] = byte(i % 251)
	}
	closed := false
	r, err := newReader(t.Context(), f.open, func() { closed = true })
	if err != nil {
		t.Fatal(err)
	}
	read := func(off int64, n int) {
		t.Helper()
		p := make([]byte, n)
		got, err := r.ReadAt(p, off)
		end := min(off+int64(n), int64(len(f.content)))
		if want := f.content[min(off, end):end]; got != len(want) || !bytes.Equal(p[:got], want) || (got < n) != errors.Is(err, io.EOF) {
			t.Fatalf("read %d at %d: %d, %v", n, off, got, err)
		}
	}
	const part = 32 << 10
	for _, i := range []int64{1, 0, 3, 2, 5, 4, 7, 6, 8} {
		read(i*part, part)
	}
	if !slices.Equal(f.opened, []int64{0}) {
		t.Fatalf("opened at %v, want once", f.opened)
	}
	read(3*readWindow, part)    // a jump ahead
	read(0, part)               // and back to the start
	read(4*readWindow-10, part) // past the end
	if !slices.Equal(f.opened, []int64{0, 3 * readWindow, 0, 4*readWindow - 10}) {
		t.Fatalf("opened at %v", f.opened)
	}
	if err := r.Close(); err != nil || !closed {
		t.Fatalf("close: %v, %v", err, closed)
	}
}

// Writing streams parts in order, keeps those that come early, and discards the file if a
// part is missing.
func TestWriter(t *testing.T) {
	var early atomic.Int64
	write := func(written *bytes.Buffer, result *error) *writer {
		return newWriter(t.Context(), &early, func(_ context.Context, content io.Reader) error {
			_, err := io.Copy(written, content)
			return err
		}, func(err error) { *result = err })
	}

	var written bytes.Buffer
	var result error
	w := write(&written, &result)
	for _, part := range []struct {
		off  int64
		data string
	}{{8, "89"}, {4, "4567"}, {0, "0123"}, {10, ""}} {
		if n, err := w.WriteAt([]byte(part.data), part.off); n != len(part.data) || err != nil {
			t.Fatalf("write at %d: %d, %v", part.off, n, err)
		}
	}
	if err := w.Close(); err != nil || written.String() != "0123456789" || result != nil || early.Load() != 0 {
		t.Fatalf("written %q: %v, %v, %d early", written.String(), err, result, early.Load())
	}

	// A gap, a part written again and too much that waits all discard the file.
	for name, parts := range map[string][][2]int64{"gap": {{0, 10}, {20, 10}}, "again": {{0, 10}, {10, 10}, {0, 10}}, "too much": {{10, 10}, {20, maxEarly}}} {
		written.Reset()
		w := write(&written, &result)
		var err error
		for _, part := range parts {
			if _, err = w.WriteAt(make([]byte, part[1]), part[0]); err != nil {
				break
			}
		}
		if err = errors.Join(err, w.Close()); !errors.Is(err, sftp.ErrSSHFxOpUnsupported) || !errors.Is(result, sftp.ErrSSHFxOpUnsupported) || early.Load() != 0 {
			t.Errorf("%s: %v, %v, %d early", name, err, result, early.Load())
		}
	}

	// A client that goes away discards the file too.
	w = write(&written, &result)
	if _, err := w.WriteAt([]byte("partial"), 0); err != nil {
		t.Fatal(err)
	}
	w.TransferError(io.ErrUnexpectedEOF)
	if err := w.Close(); err == nil || result == nil {
		t.Fatalf("close after the client went away: %v, %v", err, result)
	}
}
