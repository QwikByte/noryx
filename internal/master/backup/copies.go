package backup

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"github.com/QwikByte/noryx/internal/master/s3"
	"github.com/QwikByte/noryx/internal/master/schedule"
)

const (
	// chunkSize is that of the chunks of archives the master sends to agents.
	chunkSize = 256 << 10
	// copyTimeout covers copying large backups to a storage or node, and back.
	copyTimeout = 24 * time.Hour
)

var errNoCopy = httpapi.Errorf(http.StatusNotFound, "Copy not found. It may have been deleted.")

// CopyNodes are the nodes, and connections to their agents.
type CopyNodes interface {
	Nodes
	Get(ctx context.Context, id string) (node.Node, error)
}

// Copies keeps copies of the backups that jobs make away from the servers' nodes, in
// S3-compatible storage or on another node, so that a server can be restored when its node
// or its disk is lost. A copy is the download of a backup with the secrets of the server
// hidden, so that restoring it works like restoring a backup into another server. The master
// relays every copy; agents never connect to each other or to a storage.
type Copies struct {
	db    *sql.DB
	nodes CopyNodes
	// transport sends the requests to storages; nil for Go's default.
	transport http.RoundTripper
}

func NewCopies(db *sql.DB, nodes CopyNodes, transport http.RoundTripper) *Copies {
	return &Copies{db: db, nodes: nodes, transport: transport}
}

// CopyTo is where a backup job copies its backups of servers to: a storage, or another node,
// which keeps them in a storage location.
type CopyTo struct {
	Storage string `json:"storage,omitempty"`
	Node    string `json:"node,omitempty"`
	// Location is the storage location on the node; empty means its default one.
	Location string `json:"location,omitempty"`
}

// check returns a message for the administrator if the storage or node doesn't exist.
func (to CopyTo) check(ctx context.Context, c *Copies) string {
	switch {
	case (to.Storage == "") == (to.Node == ""), to.Storage != "" && to.Location != "":
		return "Choose a storage or a node to copy the backups to."
	case to.Location != "" && !locationPattern.MatchString(to.Location):
		return "Choose a storage location of the node."
	case to.Storage != "":
		if _, err := c.storage(ctx, to.Storage); err != nil {
			return "Choose a storage that exists."
		}
	default:
		if _, err := c.nodes.Get(ctx, to.Node); err != nil {
			return "Choose a node that exists."
		}
	}
	return ""
}

// Copy is a copy of a backup of a server away from its node.
type Copy struct {
	ID int64 `json:"id"`
	// JobID is the job that made it; empty once the job is deleted.
	JobID      string `json:"jobId,omitempty"`
	ServerID   string `json:"serverId"`
	ServerName string `json:"serverName"`
	// NodeID and NodeName are those of the node that the server was on when it was copied, or
	// moved to since. A copy belongs to the server on its node, as a node tells the IDs of its
	// servers itself.
	NodeID   string `json:"nodeId"`
	NodeName string `json:"nodeName"`
	Proxy    bool   `json:"proxy"`
	// The backup.
	BackupID  string    `json:"backupId"`
	Label     string    `json:"label"`
	CreatedAt time.Time `json:"createdAt"`
	Size      int64     `json:"size"`
	Paths     []string  `json:"paths"`
	Exclude   []string  `json:"exclude"`
	// Kept copies, like kept backups, are never deleted by their job.
	Kept     bool      `json:"kept"`
	CopiedAt time.Time `json:"copiedAt"`
	// StorageID is the storage that holds it, or CopyNodeID the node, in its storage
	// location Location; Where names it.
	StorageID  string `json:"storageId,omitempty"`
	CopyNodeID string `json:"copyNodeId,omitempty"`
	Location   string `json:"location,omitempty"`
	Where      string `json:"where"`
}

// filter selects copies; each field that isn't empty must match.
type filter struct {
	id                                  int64
	serverID, nodeID, storage, copyNode string
}

// list returns the copies that a filter selects, newest first.
func (c *Copies) list(ctx context.Context, f filter) ([]Copy, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT c.id, coalesce(c.task_id, ''), c.server_id, c.server_name, c.node_id,
			coalesce((SELECT name FROM nodes WHERE id = c.node_id), c.node_name), c.proxy, c.backup_id, c.label,
			c.created_at, c.size, c.paths, c.exclude, c.kept, c.copied_at, coalesce(c.storage_id, ''), coalesce(c.copy_node, ''),
			c.location, coalesce(s.name, n.name, '')
		FROM backup_copies c
		LEFT JOIN backup_storages s ON s.id = c.storage_id
		LEFT JOIN nodes n ON n.id = c.copy_node
		WHERE ? IN (0, c.id) AND ? IN ('', c.server_id) AND ? IN ('', c.node_id) AND ? IN ('', c.storage_id) AND ? IN ('', c.copy_node)
		ORDER BY c.created_at DESC, c.backup_id DESC`, f.id, f.serverID, f.nodeID, f.storage, f.copyNode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	copies := []Copy{}
	for rows.Next() {
		var cp Copy
		var created, copied int64
		var paths, exclude string
		if err := rows.Scan(&cp.ID, &cp.JobID, &cp.ServerID, &cp.ServerName, &cp.NodeID, &cp.NodeName, &cp.Proxy, &cp.BackupID, &cp.Label,
			&created, &cp.Size, &paths, &exclude, &cp.Kept, &copied, &cp.StorageID, &cp.CopyNodeID, &cp.Location, &cp.Where); err != nil {
			return nil, err
		}
		cp.CreatedAt, cp.CopiedAt = time.Unix(created, 0), time.Unix(copied, 0)
		if err := errors.Join(json.Unmarshal([]byte(paths), &cp.Paths), json.Unmarshal([]byte(exclude), &cp.Exclude)); err != nil {
			return nil, err
		}
		copies = append(copies, cp)
	}
	return copies, rows.Err()
}

// get returns a copy.
func (c *Copies) get(ctx context.Context, id string) (Copy, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return Copy{}, errNoCopy
	}
	list, err := c.list(ctx, filter{id: n})
	if err == nil && len(list) == 0 {
		err = errNoCopy
	}
	if err != nil {
		return Copy{}, err
	}
	return list[0], nil
}

// add records a copy that was made.
func (c *Copies) add(ctx context.Context, cp *Copy) error {
	paths, err := json.Marshal(cp.Paths)
	if err != nil {
		return err
	}
	exclude, err := json.Marshal(cp.Exclude)
	if err != nil {
		return err
	}
	res, err := c.db.ExecContext(ctx, `
		INSERT INTO backup_copies (task_id, storage_id, copy_node, location, server_id, server_name, proxy, node_id, node_name, backup_id,
			label, created_at, size, paths, exclude, kept, copied_at)
		VALUES (?, nullif(?, ''), nullif(?, ''), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		cp.JobID, cp.StorageID, cp.CopyNodeID, cp.Location, cp.ServerID, cp.ServerName, cp.Proxy, cp.NodeID, cp.NodeName, cp.BackupID,
		cp.Label, cp.CreatedAt.Unix(), cp.Size, paths, exclude, cp.Kept, cp.CopiedAt.Unix())
	if err == nil {
		cp.ID, err = res.LastInsertId()
	}
	return err
}

// Move keeps the copies of a server that moved to another node with the server.
func (c *Copies) Move(ctx context.Context, serverID, from, to string) error {
	_, err := c.db.ExecContext(ctx, `
		UPDATE backup_copies SET node_id = ?1, node_name = coalesce((SELECT name FROM nodes WHERE id = ?1), node_name)
		WHERE node_id = ?2 AND server_id = ?3`, to, from, serverID)
	return err
}

// Forget keeps the copies of a deleted server, so that it can still be restored.
func (c *Copies) Forget(context.Context, string, string) error { return nil }

// place is where a job copies backups to, ready to take them.
type place struct {
	CopyTo
	name    string
	storage storage
	client  *s3.Client // of the storage
}

func (c *Copies) place(ctx context.Context, to CopyTo) (place, error) {
	p := place{CopyTo: to}
	if to.Storage != "" {
		var err error
		if p.storage, err = c.storage(ctx, to.Storage); err != nil {
			return p, err
		}
		p.name = p.storage.Name
		p.client, err = p.storage.client(c.transport)
		return p, err
	}
	n, err := c.nodes.Get(ctx, to.Node)
	if err != nil {
		return p, fmt.Errorf("the node of the copies: %w", err)
	}
	p.name = n.Name
	return p, nil
}

// Sync copies the backups of a job of a server that have no copy yet to where the job copies
// them, and deletes the copies that the job no longer keeps: those of backups that are no
// longer on the node and that its retention doesn't keep among the copies. So a node that
// lost its backups takes none of their copies with it. The backups on the node tell which
// copies are marked to keep. A backup that can't be copied is copied in the next run. It
// returns what it changed.
//
// Nodes tell the IDs of their servers themselves, so a compromised node can claim the ID of a
// server of another node: Sync only keeps the copies of the server on its node, and replaces
// no copy of another node or job that has the ID of one of its backups.
func (c *Copies) Sync(ctx context.Context, t schedule.Task, s JobSettings, srv schedule.Server) (string, error) {
	if s.Copy.Node == srv.NodeID {
		return "", schedule.Skipped("Its node keeps the copies of the job. Choose another node for them.")
	}
	p, err := c.place(ctx, *s.Copy)
	if err != nil {
		return "", err
	}
	conn, err := c.nodes.Conn(ctx, srv.NodeID)
	if err != nil {
		return "", err
	}
	listCtx, cancel := context.WithTimeout(ctx, queryTimeout)
	res, err := noryxv1.NewBackupServiceClient(conn).ListBackups(listCtx, &noryxv1.ListBackupsRequest{ServerId: srv.GetId()})
	cancel()
	if err != nil {
		return "", err
	}
	onNode := slices.DeleteFunc(res.GetBackups(), func(b *noryxv1.Backup) bool { return b.GetJobId() != t.ID })
	copies, err := c.list(ctx, filter{serverID: srv.GetId(), storage: p.Storage, copyNode: p.Node})
	if err != nil {
		return "", err
	}
	theirs := func(cp Copy) bool { return cp.NodeID != srv.NodeID || cp.JobID != t.ID }
	created := func(b *noryxv1.Backup) string { return time.Unix(b.GetCreatedUnix(), 0).UTC().Format(time.DateTime) }
	var missing []*noryxv1.Backup
	var errs []error
	for _, b := range onNode {
		i := slices.IndexFunc(copies, func(cp Copy) bool { return cp.BackupID == b.GetId() })
		switch {
		case i < 0:
			missing = append(missing, b)
		case theirs(copies[i]):
			errs = append(errs, fmt.Errorf("can't copy the backup of %s: %s keeps a copy of another node or job with its ID", created(b), p.name))
		case copies[i].Kept != b.GetKept():
			copies[i].Kept = b.GetKept()
			if _, err := c.db.ExecContext(ctx, `UPDATE backup_copies SET kept = ? WHERE id = ?`, b.GetKept(), copies[i].ID); err != nil {
				return "", err
			}
		}
	}
	copies = slices.DeleteFunc(copies, theirs)
	copied, deleted := 0, 0
	for _, b := range missing { // newest first
		made, err := c.copy(ctx, p, t, srv, b)
		if err != nil {
			errs = append(errs, fmt.Errorf("can't copy the backup of %s: %s", created(b), httpapi.Message(err)))
			continue
		}
		copies, copied = append(copies, made), copied+1
	}
	keep := keeps(copies, s.retention(t.Schedule.TimeZone))
	for i, cp := range copies {
		if keep[i] || slices.ContainsFunc(onNode, func(b *noryxv1.Backup) bool { return b.GetId() == cp.BackupID }) {
			continue
		}
		if err := c.delete(ctx, cp); err != nil {
			errs = append(errs, fmt.Errorf("can't delete the copy of %s: %s", cp.CreatedAt.UTC().Format(time.DateTime), httpapi.Message(err)))
			continue
		}
		deleted++
	}
	var change []string
	if copied > 0 {
		change = append(change, fmt.Sprintf("copied %d to %s", copied, p.name))
	}
	if deleted > 0 {
		change = append(change, fmt.Sprintf("deleted %d it no longer keeps", deleted))
	}
	return strings.Join(change, ", "), errors.Join(errs...)
}

// keeps sorts copies newest first and tells which of them a retention keeps: all that are
// kept, and those that the retention keeps of the others.
func keeps(copies []Copy, r *noryxv1.BackupRetention) []bool {
	slices.SortStableFunc(copies, func(a, b Copy) int {
		return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), strings.Compare(b.BackupID, a.BackupID))
	})
	var created []time.Time
	for _, cp := range copies {
		if !cp.Kept {
			created = append(created, cp.CreatedAt)
		}
	}
	retained := r.Keeps(created)
	keep := make([]bool, len(copies))
	for i, cp := range copies {
		if keep[i] = cp.Kept; !cp.Kept {
			keep[i], retained = retained[0], retained[1:]
		}
	}
	return keep
}

// copy copies a backup of a server, with the secrets of the server hidden, and records it.
func (c *Copies) copy(ctx context.Context, p place, t schedule.Task, srv schedule.Server, b *noryxv1.Backup) (Copy, error) {
	cp := Copy{
		JobID: t.ID, ServerID: srv.GetId(), ServerName: srv.GetName(), NodeID: srv.NodeID, NodeName: srv.NodeName, Proxy: srv.GetType().Proxy(),
		BackupID: b.GetId(), Label: b.GetLabel(), CreatedAt: time.Unix(b.GetCreatedUnix(), 0),
		Paths: append([]string{}, b.GetPaths()...), Exclude: append([]string{}, b.GetExclude()...),
		Kept: b.GetKept(), StorageID: p.Storage, CopyNodeID: p.Node, Location: p.Location, Where: p.name,
	}
	ctx, cancel := context.WithTimeout(ctx, copyTimeout)
	defer cancel()
	from, err := c.nodes.Conn(ctx, srv.NodeID)
	if err != nil {
		return cp, err
	}
	download := func(ctx context.Context) (grpc.ServerStreamingClient[noryxv1.DownloadBackupResponse], error) {
		return noryxv1.NewBackupServiceClient(from).DownloadBackup(ctx, &noryxv1.DownloadBackupRequest{ServerId: cp.ServerID, BackupId: cp.BackupID, HideSecrets: true})
	}
	if p.client != nil {
		stream, err := download(ctx)
		if err == nil {
			cp.Size, err = p.client.Put(ctx, p.storage.key(cp.ServerID, cp.BackupID), readChunks(stream), b.GetSize())
		}
		if err != nil {
			return cp, err
		}
	} else {
		to, err := c.nodes.Conn(ctx, p.Node)
		if err != nil {
			return cp, err
		}
		header := &noryxv1.ImportBackupHeader{ServerId: cp.ServerID, Backup: &noryxv1.Backup{
			Id: cp.BackupID, Label: cp.Label, CreatedUnix: b.GetCreatedUnix(), Size: hidden(b.GetSize()), Location: p.Location, Paths: cp.Paths,
			Exclude: cp.Exclude, JobId: t.ID, Kept: cp.Kept,
		}}
		relay := func() (*noryxv1.ImportCopyResponse, error) {
			return node.Relay(ctx, download, noryxv1.NewBackupServiceClient(to).ImportCopy,
				&noryxv1.ImportCopyRequest{Content: &noryxv1.ImportCopyRequest_Header{Header: header}},
				func(data []byte) *noryxv1.ImportCopyRequest {
					return &noryxv1.ImportCopyRequest{Content: &noryxv1.ImportCopyRequest_Data{Data: data}}
				},
				func(int) {})
		}
		res, err := relay()
		if status.Code(err) == codes.AlreadyExists { // copied before, but not recorded
			_, err = noryxv1.NewBackupServiceClient(to).DeleteCopy(ctx, &noryxv1.DeleteCopyRequest{ServerId: cp.ServerID, BackupId: cp.BackupID})
			if err == nil {
				res, err = relay()
			}
		}
		if status.Code(err) == codes.Unimplemented {
			return cp, httpapi.Errorf(http.StatusNotImplemented, "Update the agent of %s to keep copies of backups.", p.name)
		}
		if err != nil {
			return cp, err
		}
		cp.Size = res.GetBackup().GetSize()
	}
	cp.CopiedAt = time.Now()
	return cp, c.add(context.WithoutCancel(ctx), &cp)
}

// delete deletes a copy where it is kept, and forgets it.
func (c *Copies) delete(ctx context.Context, cp Copy) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	if cp.StorageID != "" {
		st, err := c.storage(ctx, cp.StorageID)
		var client *s3.Client
		if err == nil {
			client, err = st.client(c.transport)
		}
		if err == nil {
			err = client.Delete(ctx, st.key(cp.ServerID, cp.BackupID))
		}
		if err != nil {
			return err
		}
	} else {
		conn, err := c.nodes.Conn(ctx, cp.CopyNodeID)
		if err == nil {
			_, err = noryxv1.NewBackupServiceClient(conn).DeleteCopy(ctx, &noryxv1.DeleteCopyRequest{ServerId: cp.ServerID, BackupId: cp.BackupID})
		}
		if err != nil && status.Code(err) != codes.NotFound {
			return err
		}
	}
	_, err := c.db.ExecContext(ctx, `DELETE FROM backup_copies WHERE id = ?`, cp.ID)
	return err
}

// open opens the archive of a copy where it is kept.
func (c *Copies) open(ctx context.Context, cp Copy) (io.ReadCloser, error) {
	if cp.StorageID != "" {
		st, err := c.storage(ctx, cp.StorageID)
		if err != nil {
			return nil, err
		}
		client, err := st.client(c.transport)
		if err != nil {
			return nil, err
		}
		r, err := client.Get(ctx, st.key(cp.ServerID, cp.BackupID))
		if s3.NotFound(err) {
			err = httpapi.Errorf(http.StatusNotFound, "The copy is no longer in %s.", st.Name)
		}
		return r, err
	}
	conn, err := c.nodes.Conn(ctx, cp.CopyNodeID)
	if err != nil {
		return nil, err
	}
	stream, err := noryxv1.NewBackupServiceClient(conn).DownloadCopy(ctx, &noryxv1.DownloadCopyRequest{ServerId: cp.ServerID, BackupId: cp.BackupID})
	if err != nil {
		return nil, err
	}
	return io.NopCloser(readChunks(stream)), nil
}

// readChunks reads the data of a stream of chunks from an agent, e.g. a download of a backup.
func readChunks[T any, PT interface {
	*T
	GetData() []byte
}](stream grpc.ServerStreamingClient[T]) io.Reader {
	return &chunks[T, PT]{stream: stream}
}

type chunks[T any, PT interface {
	*T
	GetData() []byte
}] struct {
	stream grpc.ServerStreamingClient[T]
	rest   []byte
}

func (c *chunks[T, PT]) Read(p []byte) (int, error) {
	for len(c.rest) == 0 {
		msg, err := c.stream.Recv()
		if err != nil {
			return 0, err
		}
		c.rest = PT(msg).GetData()
	}
	n := copy(p, c.rest)
	c.rest = c.rest[n:]
	return n, nil
}

// upload sends an archive that r reads to the agent of a node as a backup of one of its
// servers, which header describes. The agent discards what it received if reading fails.
func upload(ctx context.Context, conn grpc.ClientConnInterface, header *noryxv1.ImportBackupHeader, r io.Reader, progress func(int64)) (*noryxv1.Backup, error) {
	res, err := send(ctx, noryxv1.NewBackupServiceClient(conn).ImportBackup,
		&noryxv1.ImportBackupRequest{Content: &noryxv1.ImportBackupRequest_Header{Header: header}},
		func(data []byte) *noryxv1.ImportBackupRequest {
			return &noryxv1.ImportBackupRequest{Content: &noryxv1.ImportBackupRequest_Data{Data: data}}
		}, r, progress)
	return res.GetBackup(), err
}

// send opens a stream to an agent, sends the header and then what r reads in chunks, which
// data wraps, and returns the agent's answer. progress tells how much it sent. If reading
// fails, it cancels the stream, so that the agent discards what it received.
func send[Req, Res any](ctx context.Context, open func(context.Context, ...grpc.CallOption) (grpc.ClientStreamingClient[Req, Res], error),
	header *Req, data func([]byte) *Req, r io.Reader, progress func(int64),
) (*Res, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	up, err := open(ctx)
	if err == nil {
		err = up.Send(header)
	}
	var sent int64
	for err == nil {
		chunk := make([]byte, chunkSize) // gRPC may still hold the one sent before
		n, readErr := io.ReadFull(r, chunk)
		if n > 0 {
			if err = up.Send(data(chunk[:n])); err == nil {
				sent += int64(n)
				progress(sent)
			}
		}
		if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
			break
		}
		if readErr != nil {
			return nil, readErr // cancels the stream
		}
	}
	if err != nil && !errors.Is(err, io.EOF) { // io.EOF: the agent ended the stream, and its answer tells why
		return nil, err
	}
	return up.CloseAndRecv()
}

// registerCopies registers the routes of the copies of backups and of the storages for them.
func (h *Handler) registerCopies(mux access.Mux) {
	h.copies.registerStorages(mux)
	const base = "/api/nodes/{node}/servers/{id}/copies"
	mux.Handle("GET "+base, access.OnServer(access.BackupsView), h.serverCopies)
	// Restoring a copy needs the permission to restore backups of the server it is restored into.
	mux.Handle("POST "+base+"/{copy}/restore", access.OnServer(access.BackupsView), h.restoreCopy)
	mux.Handle("DELETE "+base+"/{copy}", access.OnServer(access.BackupsDelete), h.deleteCopy)
	// The copies of all servers, also of those that are gone, e.g. with their node.
	mux.Handle("GET /api/backup-copies", access.Everywhere(access.BackupsView), func(w http.ResponseWriter, r *http.Request) {
		list, err := h.copies.list(r.Context(), filter{})
		respond(w, r, http.StatusOK, list, err)
	})
	mux.Handle("POST /api/backup-copies/{copy}/restore", access.Everywhere(access.BackupsView), h.restoreCopy)
	mux.Handle("DELETE /api/backup-copies/{copy}", access.Everywhere(access.BackupsDelete), h.deleteCopy)
}

// serverCopies lists the copies of the backups of a server of a node.
func (h *Handler) serverCopies(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	// The permission applies to the server on this node, so it must be there.
	_, err := h.server(ctx, r.PathValue("node"), r.PathValue("id"))
	var list []Copy
	if err == nil {
		list, err = h.copies.list(ctx, filter{serverID: r.PathValue("id"), nodeID: r.PathValue("node")})
	}
	respond(w, r, http.StatusOK, list, err)
}

// copyOf returns the copy of a request, which must be of the server of its path, if any, on
// the node of its path.
func (h *Handler) copyOf(ctx context.Context, r *http.Request) (Copy, error) {
	cp, err := h.copies.get(ctx, r.PathValue("copy"))
	if err != nil || r.PathValue("id") == "" {
		return cp, err
	}
	if cp.ServerID != r.PathValue("id") || cp.NodeID != r.PathValue("node") {
		return cp, errNoCopy
	}
	_, err = h.server(ctx, r.PathValue("node"), r.PathValue("id"))
	return cp, err
}

// restoreCopy restores a copy into a server of the same kind, on its current node: the
// server it is of, or another one. The master relays the copy into the backups of the server,
// restores it like a backup of another server, which keeps the server's secrets, and deletes
// it again.
func (h *Handler) restoreCopy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Node          string `json:"node"`
		Server        string `json:"server"`
		SnapshotFirst bool   `json:"snapshotFirst"`
	}
	if r.ContentLength != 0 {
		if err := httpapi.ReadJSON(w, r, &req); err != nil {
			httpapi.WriteError(w, r, err)
			return
		}
	}
	if req.Node == "" && req.Server == "" {
		req.Node, req.Server = r.PathValue("node"), r.PathValue("id")
	}
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	cp, target, err := h.checkCopyRestore(ctx, r, req.Node, req.Server)
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	logging.Note(ctx, slog.Int64("copy", cp.ID), slog.String("of", cp.ServerName), slog.String("of_id", cp.ServerID), slog.String("where", cp.Where),
		slog.String("into", target.GetName()), slog.String("into_id", target.GetId()), slog.String("into_node", req.Node), slog.Bool("snapshot_first", req.SnapshotFirst))
	spec := operation.Spec{
		Kind: "backup.restore-copy", Subject: target.GetName(), NodeID: req.Node, ServerID: req.Server, Steps: []string{"copy", "restore"},
		Status: http.StatusOK, Timeout: copyTimeout, Category: logging.Backups,
		Visible: func(g access.Grants) bool { return g.On(access.BackupsView, req.Node, req.Server) },
	}
	h.ops.Run(w, r, spec, func(ctx context.Context) (any, error) {
		operation.Step(ctx, "copy")
		conn, err := h.nodes.Conn(ctx, req.Node)
		if err != nil {
			return nil, err
		}
		archive, err := h.copies.open(ctx, cp)
		if err != nil {
			return nil, err
		}
		defer archive.Close()
		header := &noryxv1.ImportBackupHeader{ServerId: req.Server, Backup: &noryxv1.Backup{
			Id: noryxv1.NewBackupID(cp.CreatedAt), Label: cmp.Or(cp.Label, cp.ServerName), CreatedUnix: cp.CreatedAt.Unix(), Size: cp.Size,
			Paths: cp.Paths, Exclude: cp.Exclude, Untrusted: true,
		}}
		// The storage or node that keeps the copy can't make it larger than it was.
		imported, err := upload(ctx, conn, header, io.LimitReader(archive, cp.Size), func(n int64) { operation.Count(ctx, n, cp.Size, "bytes") })
		if err != nil {
			return nil, err
		}
		defer func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), queryTimeout)
			defer cancel()
			if _, err := noryxv1.NewBackupServiceClient(conn).DeleteBackup(ctx, &noryxv1.DeleteBackupRequest{ServerId: req.Server, BackupId: imported.GetId()}); err != nil {
				slog.Warn("Can't delete the copy of a backup", logging.Backups, logging.KeyNode, req.Node, logging.KeyServer, req.Server, "backup", imported.GetId(), "err", err)
			}
		}()
		if !imported.GetUntrusted() { // an older agent would trust it
			return nil, httpapi.Errorf(http.StatusNotImplemented, "Update the agent of the node to restore copies of backups into its servers.")
		}
		return h.restoreOn(ctx, req.Node, req.Server, imported.GetId(), restoreRequest{SnapshotFirst: req.SnapshotFirst})
	})
}

// checkCopyRestore checks that the copy of a request can be restored into a server, which the
// user may restore backups of, and returns the copy and the server.
func (h *Handler) checkCopyRestore(ctx context.Context, r *http.Request, nodeID, serverID string) (Copy, *noryxv1.Server, error) {
	if nodeID == "" || serverID == "" {
		return Copy{}, nil, httpapi.Errorf(http.StatusBadRequest, "Choose a server.")
	}
	if !access.From(ctx).On(access.BackupsRestore, nodeID, serverID) {
		return Copy{}, nil, access.Denied(access.BackupsRestore)
	}
	cp, err := h.copyOf(ctx, r)
	if err == nil {
		err = h.moving(serverID)
	}
	if err != nil {
		return cp, nil, err
	}
	target, err := h.server(ctx, nodeID, serverID)
	if err != nil {
		return cp, nil, err
	}
	if cp.Proxy != target.GetType().Proxy() {
		return cp, nil, httpapi.Errorf(http.StatusConflict, "Restore backups of proxies into proxies, and those of game servers into game servers.")
	}
	return cp, target, h.supports(ctx, nodeID, serverID)
}

// deleteCopy deletes a copy where it is kept.
func (h *Handler) deleteCopy(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	cp, err := h.copyOf(ctx, r)
	if err == nil {
		logging.Note(ctx, slog.Int64("copy", cp.ID), slog.String("of", cp.ServerName), slog.String("of_id", cp.ServerID), slog.String("where", cp.Where))
		err = h.copies.delete(ctx, cp)
	}
	respond(w, r, http.StatusNoContent, nil, err)
}
