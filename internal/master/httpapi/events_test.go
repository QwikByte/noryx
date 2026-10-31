package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type line struct {
	text string
	time int64
}

func (l line) GetLine() string        { return l.text }
func (l line) GetTimeUnixNano() int64 { return l.time }

// lines is a stream of lines that ends with err, or io.EOF.
type lines struct {
	sent []line
	err  error
}

func (s *lines) Recv() (line, error) {
	if len(s.sent) == 0 {
		if s.err == nil {
			return line{}, io.EOF
		}
		return line{}, s.err
	}
	l := s.sent[0]
	s.sent = s.sent[1:]
	return l, nil
}

func TestStreamLog(t *testing.T) {
	for _, tc := range []struct {
		name, query string
		stream      *lines
		status      int
		tail        uint32
		body        string
	}{
		{"lines", "", &lines{sent: []line{{"a\r\nevent: end", 1}, {"b", 0}}}, http.StatusOK, logTail, "id: 1\ndata: a  event: end\n\ndata: b\n\nevent: end\ndata:\n\n"},
		{"again", "?after=7", &lines{}, http.StatusOK, maxTail, "event: end\ndata:\n\n"},
		{"invalid after", "?after=x", &lines{}, http.StatusBadRequest, 0, ""},
		{"unknown", "", &lines{err: status.Error(codes.NotFound, "Datastore not found.")}, http.StatusNotFound, logTail, ""},
		{"an agent without the log", "", &lines{err: status.Error(codes.Unimplemented, "unknown method")}, http.StatusNotImplemented, logTail, ""},
	} {
		w := httptest.NewRecorder()
		var tail uint32
		StreamLog(w, httptest.NewRequest(http.MethodGet, "/logs"+tc.query, nil), func(_ context.Context, n uint32, _ int64) (Stream[line], error) {
			tail = n
			return tc.stream, nil
		})
		if w.Code != tc.status || tail != tc.tail || tc.status == http.StatusOK && w.Body.String() != tc.body {
			t.Errorf("%s: status %d, tail %d, body %q", tc.name, w.Code, tail, w.Body.String())
		}
	}
}
