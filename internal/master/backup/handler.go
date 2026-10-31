// Package backup backs up and restores servers through the agents of their nodes, which
// keep the backups, and runs backup jobs on a schedule.
package backup

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/node"
	"github.com/QwikByte/noryx/internal/master/operation"
)

const (
	queryTimeout = 30 * time.Second
	// backupTimeout covers backing up and restoring servers with large worlds.
	backupTimeout = 2 * time.Hour
	// maxUpload limits uploaded backups like the files of the file manager; the agent too.
	maxUpload = 16 << 30
)

// Nodes provides connections to node agents.
type Nodes interface {
	Conn(ctx context.Context, nodeID string) (grpc.ClientConnInterface, error)
}

// Selection chooses what of a server is backed up; the agent finds the matching files.
type Selection struct {
	Everything bool     `json:"everything"`
	Worlds     bool     `json:"worlds"`
	Plugins    bool     `json:"plugins"`
	Config     bool     `json:"config"`
	Paths      []string `json:"paths"` // relative to the server's folder
	// Exclude are files and folders inside the others that are left out, e.g. the tiles of a
	// map plugin.
	Exclude []string `json:"exclude"`
}

func (s Selection) empty() bool {
	return !s.Everything && !s.Worlds && !s.Plugins && !s.Config && len(s.Paths) == 0
}

// check returns a message for the administrator if the selection is invalid.
func (s *Selection) check() string {
	if s.Paths == nil {
		s.Paths = []string{}
	}
	if s.Exclude == nil {
		s.Exclude = []string{}
	}
	if s.empty() {
		return "Choose what to back up."
	}
	if i := slices.IndexFunc(s.Exclude, func(p string) bool { return strings.Trim(p, "/") == "" }); i >= 0 {
		return pathProblem(s.Exclude[i])
	}
	return cmp.Or(checkPaths(s.Paths), checkPaths(s.Exclude))
}

// checkPaths returns a message for the administrator if paths in a server's folder are invalid.
func checkPaths(paths []string) string {
	if len(paths) > noryxv1.MaxBackupPaths {
		return "Enter at most 20 files or folders."
	}
	for _, p := range paths {
		if p == "" || len(p) > 1024 || strings.ContainsAny(p, "\x00\\") || slices.Contains(strings.Split(p, "/"), "..") {
			return pathProblem(p)
		}
	}
	return ""
}

func pathProblem(p string) string {
	return "The path " + p + " is invalid. Enter paths inside the server's folder, like plugins/LuckPerms."
}

func (s Selection) proto() *noryxv1.BackupSelection {
	return &noryxv1.BackupSelection{Everything: s.Everything, Worlds: s.Worlds, Plugins: s.Plugins, Config: s.Config, Paths: s.Paths, Exclude: s.Exclude}
}

type view struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	CreatedAt time.Time `json:"createdAt"`
	Size      int64     `json:"size"`
	Location  string    `json:"location"`
	Paths     []string  `json:"paths"` // "." is everything
	Exclude   []string  `json:"exclude"`
	JobID     string    `json:"jobId,omitempty"`
	// Kept backups are never deleted by their job.
	Kept bool `json:"kept"`
	// Untrusted backups came from elsewhere, e.g. an upload; restoring them trusts nothing that
	// their archive tells.
	Untrusted bool `json:"untrusted"`
}

func toView(b *noryxv1.Backup) view {
	return view{
		b.GetId(), b.GetLabel(), time.Unix(b.GetCreatedUnix(), 0), b.GetSize(), b.GetLocation(), append([]string{}, b.GetPaths()...),
		append([]string{}, b.GetExclude()...), b.GetJobId(), b.GetKept(), b.GetUntrusted(),
	}
}

// Networks configure the network of a server again, if it is part of one.
type Networks interface {
	Reapply(ctx context.Context, serverID string) error
}

type Handler struct {
	nodes    Nodes
	networks Networks
	ops      *operation.Operations
	// moving refuses changes to a server while it moves to another node.
	moving func(serverID string) error
	copies *Copies
}

func NewHandler(nodes Nodes, networks Networks, ops *operation.Operations, moving func(serverID string) error, copies *Copies) *Handler {
	return &Handler{nodes: nodes, networks: networks, ops: ops, moving: moving, copies: copies}
}

func (h *Handler) Register(mux access.Mux) {
	const base = "/api/nodes/{node}/servers/{id}/backups"
	mux.Handle("GET "+base, access.OnServer(access.BackupsView), h.list)
	mux.Handle("POST "+base, access.OnServer(access.BackupsCreate), h.create)
	// Letting the job delete a backup again also needs the permission to delete backups.
	mux.Handle("PATCH "+base+"/{backup}", access.OnServer(access.BackupsCreate), h.update)
	mux.Handle("GET "+base+"/{backup}/files", access.OnServer(access.BackupsView), h.files)
	mux.Handle("POST "+base+"/{backup}/restore", access.OnServer(access.BackupsRestore), h.restore)
	// Restoring into another server needs the permission to restore backups there.
	mux.Handle("POST "+base+"/{backup}/restore-into", access.OnServer(access.BackupsView), h.restoreInto)
	mux.Handle("DELETE "+base+"/{backup}", access.OnServer(access.BackupsDelete), h.delete)
	mux.Handle("GET "+base+"/{backup}/download", access.OnServer(access.BackupsView), h.download)
	h.registerCopies(mux)
	// An uploaded backup is there to be restored, and can bring any file, e.g. a plugin that
	// reads the secrets of the server, like the file manager.
	mux.Handle("POST "+base+"/upload", access.All(access.OnServer(access.BackupsCreate), access.OnServer(access.BackupsRestore),
		access.OnServer(access.FilesWrite)), h.upload)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	c, err := h.client(ctx, r.PathValue("node"))
	var res *noryxv1.ListBackupsResponse
	if err == nil {
		res, err = c.ListBackups(ctx, &noryxv1.ListBackupsRequest{ServerId: r.PathValue("id")})
	}
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	views := make([]view, 0, len(res.GetBackups()))
	for _, b := range res.GetBackups() {
		views = append(views, toView(b))
	}
	httpapi.WriteJSON(w, http.StatusOK, views)
}

// create backs up a server. It continues if the browser goes away, as large worlds take a while.
func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Label     string    `json:"label"`
		Selection Selection `json:"selection"`
		Location  string    `json:"location"`
	}
	if err := httpapi.ReadJSON(w, r, &req); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	if msg := req.Selection.check(); msg != "" {
		httpapi.WriteError(w, r, httpapi.Errorf(http.StatusBadRequest, "%s", msg))
		return
	}
	spec := h.spec(r, "backup.create", []string{"save", "archive"}, http.StatusCreated)
	spec.Cancel = access.OnServer(access.BackupsCreate) // the agent discards the unfinished archive
	h.ops.Run(w, r, spec, func(ctx context.Context) (any, error) {
		var res *noryxv1.CreateBackupResponse
		err := h.agent(ctx, spec.NodeID, func(ctx context.Context, c noryxv1.BackupServiceClient) (err error) {
			res, err = c.CreateBackup(ctx, &noryxv1.CreateBackupRequest{
				ServerId: spec.ServerID, Label: req.Label, Selection: req.Selection.proto(), Location: req.Location,
			})
			return err
		})
		if err != nil {
			return nil, err
		}
		return toView(res.GetBackup()), nil
	})
}

// update changes the label of a backup and whether its job keeps it.
func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Label *string `json:"label"`
		Kept  *bool   `json:"kept"`
	}
	if err := httpapi.ReadJSON(w, r, &req); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	nodeID, serverID := r.PathValue("node"), r.PathValue("id")
	if req.Kept != nil && !*req.Kept && !access.From(r.Context()).On(access.BackupsDelete, nodeID, serverID) {
		httpapi.WriteError(w, r, access.Denied(access.BackupsDelete))
		return
	}
	if req.Label != nil {
		logging.Note(r.Context(), slog.String("label", *req.Label))
	}
	if req.Kept != nil {
		logging.Note(r.Context(), slog.Bool("kept", *req.Kept))
	}
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	c, err := h.client(ctx, nodeID)
	var res *noryxv1.UpdateBackupResponse
	if err == nil {
		res, err = c.UpdateBackup(ctx, &noryxv1.UpdateBackupRequest{ServerId: serverID, BackupId: r.PathValue("backup"), Label: req.Label, Kept: req.Kept})
	}
	if status.Code(err) == codes.Unimplemented {
		err = httpapi.Errorf(http.StatusNotImplemented, "Update the agent of the node to change backups.")
	}
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, toView(res.GetBackup()))
}

type fileView struct {
	Name      string    `json:"name"`
	Directory bool      `json:"directory"`
	Size      int64     `json:"size"`
	Modified  time.Time `json:"modified"`
}

// files lists a folder of a backup, like the file manager lists one of the server.
func (h *Handler) files(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	c, err := h.client(ctx, r.PathValue("node"))
	var res *noryxv1.ListBackupFilesResponse
	if err == nil {
		res, err = c.ListBackupFiles(ctx, &noryxv1.ListBackupFilesRequest{
			ServerId: r.PathValue("id"), BackupId: r.PathValue("backup"), Path: r.URL.Query().Get("path"),
		})
	}
	if status.Code(err) == codes.Unimplemented {
		err = httpapi.Errorf(http.StatusNotImplemented, "Update the agent of the node to look into backups.")
	}
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	files := make([]fileView, 0, len(res.GetFiles()))
	for _, f := range res.GetFiles() {
		files = append(files, fileView{f.GetName(), f.GetDirectory(), f.GetSize(), time.Unix(f.GetModifiedUnix(), 0)})
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"files": files, "truncated": res.GetTruncated()})
}

// restoreRequest chooses what of a backup is restored, and whether what that replaces is
// backed up first.
type restoreRequest struct {
	// Paths of the backup to restore; none restores all of it.
	Paths         []string `json:"paths"`
	SnapshotFirst bool     `json:"snapshotFirst"`
}

// read reads a restore request from a request's body, which may be empty to restore all of
// a backup without backing up first.
func (req *restoreRequest) read(w http.ResponseWriter, r *http.Request, into any) error {
	if r.ContentLength != 0 {
		if err := httpapi.ReadJSON(w, r, into); err != nil {
			return err
		}
	}
	if msg := checkPaths(req.Paths); msg != "" {
		return httpapi.Errorf(http.StatusBadRequest, "%s", msg)
	}
	logging.Note(r.Context(), slog.Any("paths", req.Paths), slog.Bool("snapshot_first", req.SnapshotFirst))
	return nil
}

// restored is the answer to a restore.
type restored struct {
	// Snapshot is the backup of what the restore replaced, if one was made.
	Snapshot *view `json:"snapshot,omitempty"`
	// Warning tells that the server's network couldn't be configured again.
	Warning string `json:"warning,omitempty"`
}

// restore replaces the data of a server with a backup, or some of it. It finishes if the
// browser goes away and can't be cancelled, so the server isn't left stopped or half
// restored.
func (h *Handler) restore(w http.ResponseWriter, r *http.Request) {
	var req restoreRequest
	if err := req.read(w, r, &req); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	spec, backupID := h.spec(r, "backup.restore", []string{"restore"}, http.StatusOK), r.PathValue("backup")
	if len(req.Paths) > 0 || req.SnapshotFirst {
		ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
		err := h.supports(ctx, spec.NodeID, spec.ServerID)
		cancel()
		if err != nil {
			httpapi.WriteError(w, r, err)
			return
		}
	}
	h.ops.Run(w, r, spec, func(ctx context.Context) (any, error) {
		return h.restoreOn(ctx, spec.NodeID, spec.ServerID, backupID, req)
	})
}

// restoreInto restores a backup of a server into another server, also on another node: the
// master copies the backup, with the secrets of the server hidden, to the other server,
// restores it there and deletes the copy. Like a duplicate, the other server loses the files
// with secrets of file sets, and keeps its own secrets and those of its network.
func (h *Handler) restoreInto(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Node   string `json:"node"`
		Server string `json:"server"`
		restoreRequest
	}
	if err := req.read(w, r, &req); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	b, source, target, err := h.checkInto(ctx, r, req.Node, req.Server)
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	logging.Note(ctx, slog.String("into", target.GetName()), slog.String("into_id", target.GetId()), slog.String("into_node", req.Node))
	spec := operation.Spec{
		Kind: "backup.restore-into", Subject: target.GetName(), NodeID: req.Node, ServerID: req.Server, Steps: []string{"copy", "restore"},
		Status: http.StatusOK, Timeout: backupTimeout, Category: logging.Backups,
		Visible: func(g access.Grants) bool { return g.On(access.BackupsView, req.Node, req.Server) },
	}
	nodeID := r.PathValue("node")
	h.ops.Run(w, r, spec, func(ctx context.Context) (any, error) {
		copied, err := h.copy(ctx, nodeID, source, b, req.Node, req.Server)
		if err != nil {
			return nil, err
		}
		defer func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), queryTimeout)
			defer cancel()
			c, err := h.client(ctx, req.Node)
			if err == nil {
				_, err = c.DeleteBackup(ctx, &noryxv1.DeleteBackupRequest{ServerId: req.Server, BackupId: copied.GetId()})
			}
			if err != nil {
				slog.Warn("Can't delete the copy of a backup", logging.Backups, logging.KeyNode, req.Node, logging.KeyServer, req.Server, "backup", copied.GetId(), "err", err)
			}
		}()
		if b.GetUntrusted() && !copied.GetUntrusted() { // an older agent would trust it
			return nil, httpapi.Errorf(http.StatusNotImplemented, "Update the agent of the node to restore backups from elsewhere into its servers.")
		}
		return h.restoreOn(ctx, req.Node, req.Server, copied.GetId(), req.restoreRequest)
	})
}

// checkInto checks that the backup of a request can be restored into another server, which
// the user may restore backups of, and returns the backup and both servers.
func (h *Handler) checkInto(ctx context.Context, r *http.Request, nodeID, serverID string) (*noryxv1.Backup, *noryxv1.Server, *noryxv1.Server, error) {
	switch {
	case nodeID == "" || serverID == "":
		return nil, nil, nil, httpapi.Errorf(http.StatusBadRequest, "Choose a server.")
	case nodeID == r.PathValue("node") && serverID == r.PathValue("id"):
		return nil, nil, nil, httpapi.Errorf(http.StatusBadRequest, "Choose another server, or restore the backup without one.")
	case !access.From(ctx).On(access.BackupsRestore, nodeID, serverID):
		return nil, nil, nil, access.Denied(access.BackupsRestore)
	}
	if err := h.moving(serverID); err != nil {
		return nil, nil, nil, err
	}
	source, err := h.server(ctx, r.PathValue("node"), r.PathValue("id"))
	if err != nil {
		return nil, nil, nil, err
	}
	target, err := h.server(ctx, nodeID, serverID)
	if err != nil {
		return nil, nil, nil, err
	}
	switch from, to := source.GetType(), target.GetType(); {
	case from.Proxy() != to.Proxy():
		return nil, nil, nil, httpapi.Errorf(http.StatusConflict, "Restore backups of proxies into proxies, and those of game servers into game servers.")
	case from.Bedrock() != to.Bedrock():
		return nil, nil, nil, httpapi.Errorf(http.StatusConflict, "Restore backups of Bedrock servers into Bedrock servers, and those of Java servers into Java servers.")
	}
	c, err := h.client(ctx, r.PathValue("node"))
	var list *noryxv1.ListBackupsResponse
	if err == nil {
		list, err = c.ListBackups(ctx, &noryxv1.ListBackupsRequest{ServerId: source.GetId()})
	}
	if err != nil {
		return nil, nil, nil, err
	}
	i := slices.IndexFunc(list.GetBackups(), func(b *noryxv1.Backup) bool { return b.GetId() == r.PathValue("backup") })
	if i < 0 {
		return nil, nil, nil, httpapi.Errorf(http.StatusNotFound, "Backup not found.")
	}
	return list.GetBackups()[i], source, target, h.supports(ctx, nodeID, serverID)
}

// supports refuses restores that agents of older versions would do in another way: restore
// all of a backup, without backing up first, or restore a backup of another server without
// the secrets of the server.
func (h *Handler) supports(ctx context.Context, nodeID, serverID string) error {
	c, err := h.client(ctx, nodeID)
	if err != nil {
		return err
	}
	if _, err := c.ListBackupFiles(ctx, &noryxv1.ListBackupFilesRequest{ServerId: serverID}); status.Code(err) == codes.Unimplemented {
		return httpapi.Errorf(http.StatusNotImplemented, "Update the agent of the node to restore chosen files, into other servers or with a backup first.")
	}
	return nil // the agent knows the call; what is wrong with this one doesn't matter
}

// copy copies a backup of a server to another server, with the secrets of the server
// hidden, as a backup made by hand that is named after the server if it has no label.
func (h *Handler) copy(ctx context.Context, nodeID string, source *noryxv1.Server, b *noryxv1.Backup, toNode, toServer string) (*noryxv1.Backup, error) {
	operation.Step(ctx, "copy")
	from, err := h.nodes.Conn(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	to, err := h.nodes.Conn(ctx, toNode)
	if err != nil {
		return nil, err
	}
	header := &noryxv1.ImportBackupHeader{ServerId: toServer, Backup: &noryxv1.Backup{
		Id: noryxv1.NewBackupID(time.Unix(b.GetCreatedUnix(), 0)), Label: cmp.Or(b.GetLabel(), source.GetName()), CreatedUnix: b.GetCreatedUnix(),
		Size: hidden(b.GetSize()), Paths: b.GetPaths(), Exclude: b.GetExclude(), Untrusted: b.GetUntrusted(),
	}}
	var copied int64
	res, err := node.Relay(ctx,
		func(ctx context.Context) (grpc.ServerStreamingClient[noryxv1.DownloadBackupResponse], error) {
			return noryxv1.NewBackupServiceClient(from).DownloadBackup(ctx, &noryxv1.DownloadBackupRequest{
				ServerId: source.GetId(), BackupId: b.GetId(), HideSecrets: true,
			})
		},
		noryxv1.NewBackupServiceClient(to).ImportBackup,
		&noryxv1.ImportBackupRequest{Content: &noryxv1.ImportBackupRequest_Header{Header: header}},
		func(data []byte) *noryxv1.ImportBackupRequest {
			return &noryxv1.ImportBackupRequest{Content: &noryxv1.ImportBackupRequest_Data{Data: data}}
		},
		func(n int) {
			copied += int64(n)
			operation.Count(ctx, copied, b.GetSize(), "bytes")
		})
	return res.GetBackup(), err
}

// hidden is the size that the download of a backup of size bytes with the secrets hidden has at
// most, which the agent that takes it holds it to: a placeholder can be longer than a secret,
// and the files with secrets are compressed again.
func hidden(size int64) int64 { return size + size/10 + 1<<20 }

// restoreOn restores a backup of a server within an operation, and configures the server's
// network again, as the backup may have the proxy's servers or Geyser's port of another
// time; a failure of that is a warning, as the backup was restored. The agent keeps the
// server's forwarding as it is.
func (h *Handler) restoreOn(ctx context.Context, nodeID, serverID, backupID string, req restoreRequest) (restored, error) {
	var res *noryxv1.RestoreBackupResponse
	err := h.agent(ctx, nodeID, func(ctx context.Context, c noryxv1.BackupServiceClient) (err error) {
		res, err = c.RestoreBackup(ctx, &noryxv1.RestoreBackupRequest{
			ServerId: serverID, BackupId: backupID, Paths: req.Paths, SnapshotFirst: req.SnapshotFirst,
		})
		return err
	})
	if err != nil {
		return restored{}, err
	}
	var done restored
	if res.GetSnapshot() != nil {
		snapshot := toView(res.GetSnapshot())
		done.Snapshot = &snapshot
	}
	if err := h.networks.Reapply(ctx, serverID); err != nil {
		done.Warning = httpapi.Message(err)
	}
	return done, nil
}

// spec describes an operation on the backups of the server of a request, which those see who
// may see its backups.
func (h *Handler) spec(r *http.Request, kind string, steps []string, status int) operation.Spec {
	nodeID, serverID := r.PathValue("node"), r.PathValue("id")
	return operation.Spec{
		Kind: kind, NodeID: nodeID, ServerID: serverID, Steps: steps, Status: status, Timeout: backupTimeout, Category: logging.Backups,
		Visible: func(g access.Grants) bool { return g.On(access.BackupsView, nodeID, serverID) },
	}
}

// agent calls the agent of a node within an operation, which follows the call's progress.
func (h *Handler) agent(ctx context.Context, nodeID string, call func(context.Context, noryxv1.BackupServiceClient) error) error {
	conn, err := h.nodes.Conn(ctx, nodeID)
	if err != nil {
		return err
	}
	ctx, stop := operation.Agent(ctx, conn)
	defer stop()
	return call(ctx, noryxv1.NewBackupServiceClient(conn))
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	c, err := h.client(ctx, r.PathValue("node"))
	if err == nil {
		_, err = c.DeleteBackup(ctx, &noryxv1.DeleteBackupRequest{ServerId: r.PathValue("id"), BackupId: r.PathValue("backup")})
	}
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) download(w http.ResponseWriter, r *http.Request) {
	c, err := h.client(r.Context(), r.PathValue("node"))
	var stream grpc.ServerStreamingClient[noryxv1.DownloadBackupResponse]
	if err == nil {
		stream, err = c.DownloadBackup(r.Context(), &noryxv1.DownloadBackupRequest{ServerId: r.PathValue("id"), BackupId: r.PathValue("backup"), HideSecrets: true})
	}
	var first *noryxv1.DownloadBackupResponse
	if err == nil {
		first, err = stream.Recv() // errors such as an unknown backup arrive here
	}
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	httpapi.Attachment(w, "backup-"+r.PathValue("backup")+".zip")
	if size := first.GetSize(); size > 0 { // unknown while the agent hides the secrets
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	httpapi.Relay(w, first, stream)
}

// upload adds a ZIP archive, the body of the request, as a backup of the server with the label
// and in the storage location of the query. It streams through the master like an upload of
// the file manager; the agent checks the archive and keeps it apart from its own backups.
func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithCancel(r.Context()) // cancelling discards the partial archive
	defer cancel()
	q := r.URL.Query()
	logging.Note(ctx, slog.String("label", q.Get("label")), slog.String("location", q.Get("location")))
	c, err := h.client(ctx, r.PathValue("node"))
	var res *noryxv1.UploadBackupResponse
	if err == nil {
		header := &noryxv1.UploadBackupHeader{ServerId: r.PathValue("id"), Label: q.Get("label"), Location: q.Get("location"), Size: max(r.ContentLength, 0)}
		res, err = node.Upload(ctx, c.UploadBackup, &noryxv1.UploadBackupRequest{Content: &noryxv1.UploadBackupRequest_Header{Header: header}},
			func(data []byte) *noryxv1.UploadBackupRequest {
				return &noryxv1.UploadBackupRequest{Content: &noryxv1.UploadBackupRequest_Data{Data: data}}
			}, http.MaxBytesReader(w, r.Body, maxUpload))
	}
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		err = httpapi.Errorf(http.StatusRequestEntityTooLarge, "Backups can have up to %d GB.", maxUpload>>30)
	case status.Code(err) == codes.Unimplemented:
		err = httpapi.Errorf(http.StatusNotImplemented, "Update the agent of the node to upload backups.")
	}
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	logging.Note(ctx, slog.String("backup", res.GetBackup().GetId()))
	httpapi.WriteJSON(w, http.StatusCreated, toView(res.GetBackup()))
}

func (h *Handler) client(ctx context.Context, nodeID string) (noryxv1.BackupServiceClient, error) {
	conn, err := h.nodes.Conn(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	return noryxv1.NewBackupServiceClient(conn), nil
}

// server returns a server of a node.
func (h *Handler) server(ctx context.Context, nodeID, id string) (*noryxv1.Server, error) {
	conn, err := h.nodes.Conn(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	res, err := noryxv1.NewServerServiceClient(conn).ListServers(ctx, &noryxv1.ListServersRequest{})
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(res.GetServers(), func(s *noryxv1.Server) bool { return s.GetId() == id })
	if i < 0 {
		return nil, httpapi.Errorf(http.StatusNotFound, "Server not found.")
	}
	return res.GetServers()[i], nil
}
