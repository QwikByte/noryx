// Package files is the file manager of a server: the master streams files between the
// browser and the agent of the server's node, which confines all paths. SFTP does what the
// file manager does with the same calls of the agent, see Service.
package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/operation"
)

const (
	chunkSize      = 256 << 10
	maxUploadBytes = 16 << 30 // the agent enforces the same limit
	maxArchived    = 1000     // files and folders chosen for an archive, which the log names
	opTimeout      = 30 * time.Second
	// longTimeout covers extracting and copying large worlds.
	longTimeout = 2 * time.Hour
)

type Handler struct {
	files *Service
	ops   *operation.Operations
}

func NewHandler(files *Service, ops *operation.Operations) *Handler {
	return &Handler{files: files, ops: ops}
}

func (h *Handler) Register(mux access.Mux) {
	const base = "/api/nodes/{node}/servers/{id}/files"
	read, write := access.OnServer(access.FilesRead), access.OnServer(access.FilesWrite)
	mux.Handle("GET "+base, read, h.list)
	mux.Handle("DELETE "+base, write, h.delete)
	mux.Handle("GET "+base+"/content", read, h.download)
	mux.Handle("PUT "+base+"/content", write, h.upload)
	mux.Handle("GET "+base+"/archive", read, h.archive)
	mux.Handle("POST "+base+"/directories", write, h.createDirectory)
	mux.Handle("POST "+base+"/move", write, h.move)
	mux.Handle("POST "+base+"/extract", write, h.extract)
	mux.Handle("POST "+base+"/copy", write, h.copy)
	mux.Handle("GET "+base+"/search", read, h.search)
}

type fileView struct {
	Name      string    `json:"name"`
	Directory bool      `json:"directory"`
	Size      int64     `json:"size"`
	Modified  time.Time `json:"modified"`
	// FileSet is the file set that wrote the file, whose next apply replaces changes.
	FileSet string `json:"fileSet,omitempty"`
}

func toView(f *noryxv1.FileInfo) fileView {
	return fileView{f.GetName(), f.GetDirectory(), f.GetSize(), time.Unix(f.GetModifiedUnix(), 0), f.GetFileSet()}
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), opTimeout)
	defer cancel()
	res, err := h.files.List(ctx, r.PathValue("node"), r.PathValue("id"), r.URL.Query().Get("path"))
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	files := make([]fileView, 0, len(res.GetFiles()))
	for _, f := range res.GetFiles() {
		files = append(files, toView(f))
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"files": files, "truncated": res.GetTruncated()})
}

func (h *Handler) download(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	req := &noryxv1.ReadFileRequest{ServerId: r.PathValue("id"), Path: p}
	req.Offset, req.Limit = byteRange(r.Header.Get("Range"))
	first, stream, err := h.files.Read(r.Context(), r.PathValue("node"), req)
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	httpapi.Attachment(w, path.Base(p))
	setETag(w, first.GetVersion())
	// The agent tells the size of the whole file only for a part of it that was asked for, and
	// not for an empty file, which comes whole like a file without a range.
	if total := first.GetFileSize(); total > 0 {
		start, n := first.GetOffset(), first.GetSize()
		if n == 0 {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", total))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+n-1, total))
		w.Header().Set("Content-Length", strconv.FormatInt(n, 10))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.Header().Set("Content-Length", strconv.FormatInt(first.GetSize(), 10))
	}
	httpapi.Relay(w, first, stream)
}

// byteRange returns the part of a file that a Range header asks for, as offset and limit of
// ReadFileRequest: bytes=first-last, bytes=first- or bytes=-length. Without one, or for other
// ranges, e.g. several, the whole file is read, as HTTP allows.
func byteRange(header string) (offset, limit int64) {
	spec, ok := strings.CutPrefix(header, "bytes=")
	first, last, dash := strings.Cut(spec, "-")
	if !ok || !dash || strings.Contains(spec, ",") {
		return 0, 0
	}
	a, errA := strconv.ParseInt(first, 10, 64)
	b, errB := strconv.ParseInt(last, 10, 64)
	switch {
	case first == "" && errB == nil && b > 0:
		return -b, 0
	case errA == nil && a >= 0 && last == "":
		return a, 0
	case errA == nil && errB == nil && a >= 0 && b >= a && b < math.MaxInt64:
		return a, b - a + 1
	}
	return 0, 0
}

// setETag tells the version of a file, which an upload can expect with If-Match.
func setETag(w http.ResponseWriter, v *noryxv1.FileVersion) {
	w.Header().Set("ETag", fmt.Sprintf(`"%d-%d"`, v.GetModifiedUnixNano(), v.GetSize()))
}

// ifMatch is the version of a file that an upload expects, from an ETag of setETag.
func ifMatch(r *http.Request) (*noryxv1.FileVersion, error) {
	tag := r.Header.Get("If-Match")
	if tag == "" {
		return nil, nil
	}
	var v noryxv1.FileVersion
	if _, err := fmt.Sscanf(tag, `"%d-%d"`, &v.ModifiedUnixNano, &v.Size); err != nil {
		return nil, httpapi.Errorf(http.StatusBadRequest, "If-Match must be the ETag of the file.")
	}
	return &v, nil
}

// archive downloads a folder as a ZIP archive, or only the files and folders in it that the
// parameter name names.
func (h *Handler) archive(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithCancel(r.Context()) // ends archives that the agent can't make
	defer cancel()
	p, names := r.URL.Query().Get("path"), r.URL.Query()["name"]
	if len(names) > maxArchived {
		httpapi.WriteError(w, r, httpapi.Errorf(http.StatusBadRequest, "Download up to %d files and folders at once.", maxArchived))
		return
	}
	if len(names) > 0 {
		logging.Note(ctx, slog.Any("names", names))
	}
	c, err := h.files.Client(ctx, r.PathValue("node"))
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	stream, err := c.ArchiveDirectory(ctx, &noryxv1.ArchiveDirectoryRequest{ServerId: r.PathValue("id"), Path: p, Paths: names, HideSecrets: true})
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	first, err := stream.Recv()
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	name := path.Base("/" + p)
	if name == "/" {
		name = "server"
	}
	httpapi.Attachment(w, name+".zip")
	httpapi.Relay(w, first, stream)
}

// upload creates or replaces a file. With If-Match, it only replaces the version of the file
// that the ETag of a download named, and answers 412 if the file changed since.
func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithCancel(r.Context()) // cancelling discards the partial file
	defer cancel()
	expected, err := ifMatch(r)
	var res *noryxv1.WriteFileResponse
	if err == nil {
		res, err = h.files.Write(ctx, r.PathValue("node"), &noryxv1.WriteFileHeader{
			ServerId: r.PathValue("id"), Path: r.URL.Query().Get("path"), Overwrite: r.URL.Query().Get("overwrite") == "true",
			Size: max(r.ContentLength, 0), Expected: expected,
		}, http.MaxBytesReader(w, r.Body, maxUploadBytes))
	}
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		httpapi.WriteError(w, r, httpapi.Errorf(http.StatusRequestEntityTooLarge, "Files can have up to %d GB.", maxUploadBytes>>30))
	case expected != nil && status.Code(err) == codes.FailedPrecondition:
		httpapi.WriteError(w, r, httpapi.Errorf(http.StatusPreconditionFailed, "%s", status.Convert(err).Message()))
	case err != nil:
		httpapi.WriteError(w, r, err)
	default:
		setETag(w, res.GetVersion())
		httpapi.WriteJSON(w, http.StatusCreated, toView(res.GetFile()))
	}
}

// Write creates or replaces a file of a server, as header describes it, with content.
func Write(ctx context.Context, c noryxv1.FileServiceClient, header *noryxv1.WriteFileHeader, content io.Reader) (*noryxv1.WriteFileResponse, error) {
	stream, err := c.WriteFile(ctx)
	if err != nil {
		return nil, err
	}
	err = stream.Send(&noryxv1.WriteFileRequest{Content: &noryxv1.WriteFileRequest_Header{Header: header}})
	buf := make([]byte, chunkSize)
	for err == nil {
		var n int
		n, err = io.ReadFull(content, buf)
		if n > 0 {
			if sendErr := stream.Send(&noryxv1.WriteFileRequest{Content: &noryxv1.WriteFileRequest_Data{Data: buf[:n]}}); sendErr != nil {
				err = sendErr
			}
		}
	}
	// The content ended, or the agent ended the stream (io.EOF), whose reply tells why.
	if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	return stream.CloseAndRecv()
}

// Copy writes the file of a server, as the file manager shows it, into a file of another
// server, which it replaces. It fails with codes.NotFound if the file doesn't exist.
func Copy(ctx context.Context, from noryxv1.FileServiceClient, src *noryxv1.ReadFileRequest, to noryxv1.FileServiceClient, dst *noryxv1.WriteFileHeader) error {
	ctx, cancel := context.WithCancel(ctx) // ends the download if the upload fails
	defer cancel()
	stream, err := from.ReadFile(ctx, src)
	if err != nil {
		return err
	}
	first, err := stream.Recv() // errors such as a missing file arrive here
	if err != nil {
		return err
	}
	r, w := io.Pipe()
	defer r.Close()
	go func() {
		msg, err := first, error(nil)
		for err == nil {
			if _, err = w.Write(msg.GetData()); err == nil {
				msg, err = stream.Recv()
			}
		}
		w.CloseWithError(err) // io.EOF ends the content
	}()
	dst.Size, dst.Overwrite = first.GetSize(), true
	_, err = Write(ctx, to, dst, r)
	return err
}

func (h *Handler) createDirectory(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	h.unary(w, r, &req, func(ctx context.Context, node, id string) error {
		logging.Note(ctx, slog.String("path", req.Path))
		return h.files.CreateDirectory(ctx, node, id, req.Path)
	})
}

func (h *Handler) move(w http.ResponseWriter, r *http.Request) {
	var req struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	h.unary(w, r, &req, func(ctx context.Context, node, id string) error {
		logging.Note(ctx, slog.String("from", req.From), slog.String("to", req.To))
		return h.files.Move(ctx, node, id, req.From, req.To)
	})
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	h.unary(w, r, nil, func(ctx context.Context, node, id string) error {
		return h.files.Delete(ctx, node, id, r.URL.Query().Get("path"))
	})
}

// extract extracts an archive of the server into a folder as an operation, as worlds take a
// while. The agent checks the archive, which is untrusted, before it writes anything.
func (h *Handler) extract(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path        string `json:"path"`
		Destination string `json:"destination"`
		Overwrite   bool   `json:"overwrite"`
	}
	if err := httpapi.ReadJSON(w, r, &req); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	logging.Note(r.Context(), slog.String("path", req.Path), slog.String("destination", req.Destination), slog.Bool("overwrite", req.Overwrite))
	spec := h.spec(r, "files.extract", req.Path, "extract")
	h.ops.Run(w, r, spec, func(ctx context.Context) (any, error) {
		var res *noryxv1.ExtractArchiveResponse
		err := h.agent(ctx, spec.NodeID, func(ctx context.Context, c noryxv1.FileServiceClient) (err error) {
			res, err = c.ExtractArchive(ctx, &noryxv1.ExtractArchiveRequest{
				ServerId: spec.ServerID, Path: req.Path, Destination: req.Destination, Overwrite: req.Overwrite,
			})
			return err
		})
		if err != nil {
			return nil, err
		}
		return map[string]int64{"files": res.GetFiles(), "size": res.GetSize()}, nil
	})
}

// copy copies a file or folder within the server's data as an operation, as worlds take a while.
func (h *Handler) copy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := httpapi.ReadJSON(w, r, &req); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	logging.Note(r.Context(), slog.String("from", req.From), slog.String("to", req.To))
	spec := h.spec(r, "files.copy", req.From, "copy")
	spec.Status = http.StatusNoContent
	h.ops.Run(w, r, spec, func(ctx context.Context) (any, error) {
		return nil, h.agent(ctx, spec.NodeID, func(ctx context.Context, c noryxv1.FileServiceClient) error {
			_, err := c.CopyFile(ctx, &noryxv1.CopyFileRequest{ServerId: spec.ServerID, From: req.From, To: req.To})
			return err
		})
	})
}

type match struct {
	Path string `json:"path"`
	Line int64  `json:"line"`
	Text string `json:"text"`
}

// search searches the text files of a folder for the text of the parameter query, as the file
// manager shows them.
func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), opTimeout)
	defer cancel()
	c, err := h.files.Client(ctx, r.PathValue("node"))
	var res *noryxv1.SearchFilesResponse
	if err == nil {
		res, err = c.SearchFiles(ctx, &noryxv1.SearchFilesRequest{ServerId: r.PathValue("id"), Path: r.URL.Query().Get("path"), Query: r.URL.Query().Get("query")})
	}
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	matches := make([]match, 0, len(res.GetMatches()))
	for _, m := range res.GetMatches() {
		matches = append(matches, match{m.GetPath(), m.GetLine(), m.GetText()})
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"matches": matches, "truncated": res.GetTruncated(), "files": res.GetFiles(), "tooLarge": res.GetTooLarge(),
	})
}

// spec describes an operation on the file subject of the server of a request, which those see
// who may read its files.
func (h *Handler) spec(r *http.Request, kind, subject, step string) operation.Spec {
	nodeID, serverID := r.PathValue("node"), r.PathValue("id")
	return operation.Spec{
		Kind: kind, Subject: path.Base("/" + subject), NodeID: nodeID, ServerID: serverID, Steps: []string{step}, Status: http.StatusOK,
		Timeout: longTimeout, Category: logging.Files, Visible: func(g access.Grants) bool { return g.On(access.FilesRead, nodeID, serverID) },
	}
}

// agent calls the agent of a node within an operation, which follows the call's progress.
func (h *Handler) agent(ctx context.Context, nodeID string, call func(context.Context, noryxv1.FileServiceClient) error) error {
	conn, err := h.files.nodes.Conn(ctx, nodeID)
	if err != nil {
		return err
	}
	ctx, stop := operation.Agent(ctx, conn)
	defer stop()
	return call(ctx, noryxv1.NewFileServiceClient(conn))
}

// unary reads an optional JSON body into req and runs an operation without result on the node
// and server of the request.
func (h *Handler) unary(w http.ResponseWriter, r *http.Request, req any, op func(ctx context.Context, node, id string) error) {
	ctx, cancel := context.WithTimeout(r.Context(), opTimeout)
	defer cancel()
	var err error
	if req != nil {
		err = httpapi.ReadJSON(w, r, req)
	}
	if err == nil {
		err = op(ctx, r.PathValue("node"), r.PathValue("id"))
	}
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
