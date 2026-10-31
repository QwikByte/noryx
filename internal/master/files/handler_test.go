package files

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
)

func TestByteRange(t *testing.T) {
	for header, want := range map[string][2]int64{
		"":                            {0, 0},
		"bytes=-1048576":              {-1048576, 0},
		"bytes=100-":                  {100, 0},
		"bytes=100-199":               {100, 100},
		"bytes=0-0":                   {0, 1},
		"bytes=-0":                    {0, 0},
		"bytes=5-4":                   {0, 0},
		"bytes=-5-":                   {0, 0},
		"bytes=0-1,5-6":               {0, 0},
		"items=0-5":                   {0, 0},
		"bytes=0-9223372036854775807": {0, 0},
	} {
		if offset, limit := byteRange(header); offset != want[0] || limit != want[1] {
			t.Errorf("byteRange(%q) = %d, %d, want %v", header, offset, limit, want)
		}
	}
}

// agent is the FileService of the agent of a node, with one file, which the master reaches
// over a connection in memory.
type agent struct {
	noryxv1.UnimplementedFileServiceServer
	content []byte
	version *noryxv1.FileVersion
	// older sends whole files without their size, as agents of older versions do.
	older bool
	err   error // the answer to the next call, if set

	mu      sync.Mutex
	read    *noryxv1.ReadFileRequest
	header  *noryxv1.WriteFileHeader
	written []byte
}

func (a *agent) ReadFile(req *noryxv1.ReadFileRequest, stream noryxv1.FileService_ReadFileServer) error {
	a.mu.Lock()
	a.read = req
	a.mu.Unlock()
	if a.err != nil {
		return a.err
	}
	size := int64(len(a.content))
	res := &noryxv1.ReadFileResponse{Size: size, Version: a.version}
	start, n := int64(0), size
	if !a.older && (req.GetOffset() != 0 || req.GetLimit() != 0) {
		// The part the agent sends, see part in internal/agent/files.
		if start = min(req.GetOffset(), size); req.GetOffset() < 0 {
			start = max(size+req.GetOffset(), 0)
		}
		if n = size - start; req.GetLimit() > 0 {
			n = min(n, req.GetLimit())
		}
		res.Size, res.Offset, res.FileSize = n, start, size
	}
	data := a.content[start : start+n]
	for first := true; first || len(data) > 0; first = false { // in small chunks, the first always
		k := min(len(data), 3)
		res.Data, data = data[:k], data[k:]
		if err := stream.Send(res); err != nil {
			return err
		}
		res = &noryxv1.ReadFileResponse{}
	}
	return nil
}

func (a *agent) WriteFile(stream noryxv1.FileService_WriteFileServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	var data []byte
	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		data = append(data, msg.GetData()...)
	}
	a.mu.Lock()
	a.header, a.written = first.GetHeader(), data
	a.mu.Unlock()
	if a.err != nil {
		return a.err
	}
	return stream.SendAndClose(&noryxv1.WriteFileResponse{
		File:    &noryxv1.FileInfo{Name: "server.yml", Size: int64(len(data)), ModifiedUnix: 1_800_000_000},
		Version: &noryxv1.FileVersion{ModifiedUnixNano: 1_800_000_000_000_000_001, Size: int64(len(data))},
	})
}

// nodes connects to the agent of every node.
type nodes struct{ conn *grpc.ClientConn }

func (n nodes) Conn(context.Context, string) (grpc.ClientConnInterface, error) { return n.conn, nil }

// newHandler returns a handler whose nodes run a.
func newHandler(t *testing.T, a *agent) *Handler {
	t.Helper()
	ln := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	noryxv1.RegisterFileServiceServer(srv, a)
	go srv.Serve(ln) //nolint:errcheck // ends with the test
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///agent", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return ln.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return NewHandler(nodes{conn}, nil)
}

// serve sends a request to a handler of a server's files.
func serve(t *testing.T, handle http.HandlerFunc, method, target string, body io.Reader, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), method, target, body)
	r.SetPathValue("node", "n1")
	r.SetPathValue("id", "s1")
	for k, v := range header {
		r.Header[k] = v
	}
	w := httptest.NewRecorder()
	handle(w, r)
	return w
}

// Downloads answer a Range with the part the agent sends, and tell the version of the file
// as ETag.
func TestDownload(t *testing.T) {
	a := &agent{content: []byte("0123456789"), version: &noryxv1.FileVersion{ModifiedUnixNano: 1_700_000_000_123_456_789, Size: 10}}
	h := newHandler(t, a)
	for _, tc := range []struct {
		name, rangeHeader string
		older             bool
		status            int
		contentRange      string
		body              string
		offset, limit     int64
	}{
		{"the whole file", "", false, http.StatusOK, "", "0123456789", 0, 0},
		{"a part", "bytes=2-5", false, http.StatusPartialContent, "bytes 2-5/10", "2345", 2, 4},
		{"from an offset", "bytes=7-", false, http.StatusPartialContent, "bytes 7-9/10", "789", 7, 0},
		{"the end", "bytes=-3", false, http.StatusPartialContent, "bytes 7-9/10", "789", -3, 0},
		{"more than the file", "bytes=-30", false, http.StatusPartialContent, "bytes 0-9/10", "0123456789", -30, 0},
		{"beyond the end", "bytes=8-20", false, http.StatusPartialContent, "bytes 8-9/10", "89", 8, 13},
		{"after the end", "bytes=10-", false, http.StatusRequestedRangeNotSatisfiable, "bytes */10", "", 10, 0},
		{"several parts", "bytes=0-1,5-6", false, http.StatusOK, "", "0123456789", 0, 0},
		{"of an older agent", "bytes=2-5", true, http.StatusOK, "", "0123456789", 2, 4},
	} {
		a.older = tc.older
		w := serve(t, h.download, "GET", "/files/content?path=plugins/x/config.yml", nil, http.Header{"Range": {tc.rangeHeader}})
		got := []any{w.Code, w.Header().Get("Content-Range"), w.Body.String(), a.read.GetOffset(), a.read.GetLimit()}
		if want := []any{tc.status, tc.contentRange, tc.body, tc.offset, tc.limit}; !slices.Equal(got, want) {
			t.Errorf("%s: status, Content-Range, body, offset, limit = %v, want %v", tc.name, got, want)
		}
		if tc.status != http.StatusRequestedRangeNotSatisfiable && w.Header().Get("Content-Length") != strconv.Itoa(len(tc.body)) {
			t.Errorf("%s: Content-Length %q", tc.name, w.Header().Get("Content-Length"))
		}
		if got := w.Header().Get("ETag"); got != `"1700000000123456789-10"` {
			t.Errorf("%s: ETag %q", tc.name, got)
		}
		if got := w.Header().Get("Content-Disposition"); got != `attachment; filename=config.yml` {
			t.Errorf("%s: Content-Disposition %q", tc.name, got)
		}
	}

	// Older agents tell no version, and the agent's errors arrive with the first message.
	a.older, a.version = false, nil
	if w := serve(t, h.download, "GET", "/files/content?path=x", nil, nil); w.Code != http.StatusOK || w.Header().Get("ETag") != "" {
		t.Errorf("without a version: %d, ETag %q", w.Code, w.Header().Get("ETag"))
	}
	a.content = nil
	if w := serve(t, h.download, "GET", "/files/content?path=x", nil, http.Header{"Range": {"bytes=-100"}}); w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Errorf("an empty file: %d %q", w.Code, w.Body)
	}
	a.err = status.Error(codes.NotFound, "File not found.")
	if w := serve(t, h.download, "GET", "/files/content?path=x", nil, nil); w.Code != http.StatusNotFound || w.Header().Get("Content-Disposition") != "" {
		t.Errorf("a missing file: %d %v", w.Code, w.Header())
	}
}

// An upload with If-Match only replaces the version of the file it names, and answers 412
// if the agent finds that the file changed.
func TestUploadIfMatch(t *testing.T) {
	a := &agent{}
	h := newHandler(t, a)
	content := bytes.Repeat([]byte("motd: hi\n"), chunkSize/4) // several chunks
	w := serve(t, h.upload, "PUT", "/files/content?path=server.yml&overwrite=true", bytes.NewReader(content), nil)
	if w.Code != http.StatusCreated || !bytes.Equal(a.written, content) || a.header.GetExpected() != nil || !a.header.GetOverwrite() ||
		a.header.GetPath() != "server.yml" || a.header.GetServerId() != "s1" || a.header.GetSize() != int64(len(content)) {
		t.Fatalf("upload: %d %s, header %v", w.Code, w.Body, a.header)
	}
	etag := w.Header().Get("ETag")
	if etag != `"1800000000000000001-`+strconv.Itoa(len(content))+`"` {
		t.Fatalf("ETag %q", etag)
	}

	// The ETag of a download or upload names the version the agent compares.
	w = serve(t, h.upload, "PUT", "/files/content?path=server.yml", strings.NewReader("motd: changed"), http.Header{"If-Match": {etag}})
	want := &noryxv1.FileVersion{ModifiedUnixNano: 1_800_000_000_000_000_001, Size: int64(len(content))}
	if w.Code != http.StatusCreated || !proto.Equal(a.header.GetExpected(), want) || a.header.GetOverwrite() {
		t.Fatalf("upload with If-Match: %d %s, header %v", w.Code, w.Body, a.header)
	}
	a.err = status.Error(codes.FailedPrecondition, "The file changed since you opened it.")
	if w := serve(t, h.upload, "PUT", "/files/content?path=server.yml", strings.NewReader("x"), http.Header{"If-Match": {etag}}); w.Code != http.StatusPreconditionFailed {
		t.Errorf("a changed file: %d %s", w.Code, w.Body)
	}
	if w := serve(t, h.upload, "PUT", "/files/content?path=server.yml", strings.NewReader("x"), nil); w.Code != http.StatusConflict {
		t.Errorf("failed precondition without If-Match: %d %s", w.Code, w.Body)
	}

	// An If-Match that is no ETag of the master is refused before the agent gets anything.
	a.err, a.header = nil, nil
	for _, tag := range []string{`*`, `W/"1-2"`, `"1-"`, `"abc"`, `1-2`} {
		if w := serve(t, h.upload, "PUT", "/files/content?path=server.yml", strings.NewReader("x"), http.Header{"If-Match": {tag}}); w.Code != http.StatusBadRequest || a.header != nil {
			t.Errorf("If-Match %s: %d %s, agent got %v", tag, w.Code, w.Body, a.header)
		}
	}
}
