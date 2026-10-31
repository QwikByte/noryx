package node

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"testing/iotest"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// chunk is a message with data, as agents send and receive files.
type chunk struct{ data string }

func (c *chunk) GetData() []byte { return []byte(c.data) }

// download is a stream of chunks from an agent, which ends with err after them.
type download struct {
	grpc.ClientStream
	chunks []string
	err    error
}

func (d *download) Recv() (*chunk, error) {
	if len(d.chunks) == 0 {
		return nil, d.err
	}
	c := &chunk{d.chunks[0]}
	d.chunks = d.chunks[1:]
	return c, nil
}

// upload is a stream of chunks to an agent, which takes up to limit chunks after the header,
// like an agent that refuses more, and answers with what it received or with refused.
type upload struct {
	grpc.ClientStream
	ctx      context.Context
	limit    int
	received []string
	refused  error
	closed   bool
}

func (u *upload) Send(c *chunk) error {
	if len(u.received) > u.limit {
		return io.EOF // the agent ended the stream
	}
	u.received = append(u.received, c.data)
	return nil
}

func (u *upload) CloseAndRecv() (*chunk, error) {
	u.closed = true
	if u.refused != nil {
		return nil, u.refused
	}
	return &chunk{strings.Join(u.received, "|")}, nil
}

// Relay sends the header, then each chunk of the download, and returns the agent's answer. A
// download that fails cancels the upload, so that the agent discards what it received; an agent
// that ends the upload tells why.
func TestRelay(t *testing.T) {
	relay := func(d *download, u *upload) (string, int, error) {
		sent := 0
		res, err := Relay(t.Context(),
			func(context.Context) (grpc.ServerStreamingClient[chunk], error) {
				if d == nil {
					return nil, status.Error(codes.NotFound, "backup not found")
				}
				return d, nil
			},
			func(ctx context.Context, _ ...grpc.CallOption) (grpc.ClientStreamingClient[chunk, chunk], error) {
				u.ctx = ctx
				return u, nil
			},
			&chunk{"header"}, func(data []byte) *chunk { return &chunk{string(data)} }, func(n int) { sent += n })
		var data string
		if res != nil {
			data = res.data
		}
		return data, sent, err
	}

	u := &upload{limit: 10}
	if got, sent, err := relay(&download{chunks: []string{"ab", "cde"}, err: io.EOF}, u); err != nil || got != "header|ab|cde" || sent != 5 {
		t.Errorf("relay = %q, %v, %d bytes", got, err, sent)
	}

	// The upload isn't closed, but cancelled, so that the agent doesn't take a part as a whole.
	u = &upload{limit: 10}
	if _, _, err := relay(&download{chunks: []string{"ab"}, err: status.Error(codes.DataLoss, "the disk failed")}, u); status.Code(err) != codes.DataLoss || u.closed ||
		!errors.Is(u.ctx.Err(), context.Canceled) {
		t.Errorf("relay of a broken download = %v, closed %t", err, u.closed)
	}

	u = &upload{}
	if _, _, err := relay(nil, u); status.Code(err) != codes.NotFound || u.ctx != nil {
		t.Errorf("relay of a missing download = %v, upload opened: %t", err, u.ctx != nil)
	}

	u = &upload{limit: 1, refused: status.Error(codes.InvalidArgument, "The archive is larger than announced.")}
	if _, sent, err := relay(&download{chunks: []string{"ab", "cd", "ef"}, err: io.EOF}, u); status.Code(err) != codes.InvalidArgument || sent != 2 {
		t.Errorf("relay to an agent that ended the upload = %v, %d bytes", err, sent)
	}
}

// Upload sends the header and the content in chunks, and returns the agent's answer, also if
// the agent ended the stream; reading errors are returned.
func TestUpload(t *testing.T) {
	send := func(u *upload, content io.Reader) (string, error) {
		res, err := Upload(t.Context(), func(context.Context, ...grpc.CallOption) (grpc.ClientStreamingClient[chunk, chunk], error) {
			return u, nil
		},
			&chunk{"header"}, func(data []byte) *chunk { return &chunk{string(data)} }, content)
		if res == nil {
			return "", err
		}
		return res.data, err
	}
	large := strings.Repeat("x", chunkSize+10)
	u := &upload{limit: 10}
	if got, err := send(u, strings.NewReader(large)); err != nil || got != "header|"+large[:chunkSize]+"|xxxxxxxxxx" {
		t.Errorf("upload of %d bytes = %d bytes, %v", len(large), len(got), err)
	}
	if got, err := send(&upload{limit: 10}, strings.NewReader("")); err != nil || got != "header" {
		t.Errorf("upload of nothing = %q, %v", got, err)
	}
	broken := iotest.TimeoutReader(strings.NewReader(large)) // fails on the second read
	if _, err := send(&upload{limit: 10}, broken); !errors.Is(err, iotest.ErrTimeout) {
		t.Errorf("upload of a broken reader = %v", err)
	}
	u = &upload{limit: 0, refused: status.Error(codes.ResourceExhausted, "The node has no space left.")}
	if _, err := send(u, strings.NewReader(large+large)); status.Code(err) != codes.ResourceExhausted || !slices.Equal(u.received, []string{"header"}) {
		t.Errorf("upload to an agent that ended it = %v, received %d chunks", err, len(u.received))
	}
}
