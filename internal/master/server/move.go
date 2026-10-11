package server

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/auth"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/node"
)

const (
	moveTimeout = 24 * time.Hour // copying big worlds between nodes takes a while
	keepMoves   = time.Hour      // how long the panel shows finished moves
)

var errMoving = httpapi.Errorf(http.StatusConflict, "This server is moving to another node. Try again when it is done.")

// Move is a server moving to another node.
type Move struct {
	ServerID   string `json:"serverId"`
	ServerName string `json:"serverName"`
	From       string `json:"from"`
	To         string `json:"to"`
	ToName     string `json:"toName"`
	// Phase is stopping, copying, backups, finishing, done or failed.
	Phase string `json:"phase"`
	// Bytes is how much of the data and backups was copied, as compressed archives.
	Bytes        int64 `json:"bytes"`
	Backups      int   `json:"backups"`
	BackupsTotal int   `json:"backupsTotal"`
	// Error tells why the move failed; the server stayed where it was.
	Error string `json:"error,omitempty"`
	// Warnings tell what went wrong once the server was on the new node.
	Warnings   []string   `json:"warnings"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// Moves are the moves in progress and those that finished within the last hour.
type Moves struct {
	mu    sync.Mutex
	moves map[string]*Move // by server ID
}

func NewMoves() *Moves { return &Moves{moves: map[string]*Move{}} }

// Busy reports whether a server is moving.
func (m *Moves) Busy(serverID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	mv := m.moves[serverID]
	return mv != nil && mv.FinishedAt == nil
}

// CheckIdle refuses to cut off moves in progress, e.g. by restarting the master.
func (m *Moves) CheckIdle() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, mv := range m.moves {
		if mv.FinishedAt == nil {
			return httpapi.Errorf(http.StatusConflict, "%s is moving to another node. Try again when it is done.", mv.ServerName)
		}
	}
	return nil
}

// Check refuses changes to a server while it moves, as they would be lost.
func (m *Moves) Check(serverID string) error {
	if m.Busy(serverID) {
		return errMoving
	}
	return nil
}

// Guard is an access.Wrapper that refuses requests that change a server while it moves.
func (m *Moves) Guard(pattern string, h http.HandlerFunc) http.HandlerFunc {
	if strings.HasPrefix(pattern, http.MethodGet+" ") || !strings.Contains(pattern, "/servers/{id}") {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if err := m.Check(r.PathValue("id")); err != nil {
			httpapi.WriteError(w, r, err)
			return
		}
		h(w, r)
	}
}

// start registers a move unless the server is moving already.
func (m *Moves) start(mv *Move) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	maps.DeleteFunc(m.moves, func(_ string, old *Move) bool {
		return old.FinishedAt != nil && time.Since(*old.FinishedAt) > keepMoves
	})
	if old := m.moves[mv.ServerID]; old != nil && old.FinishedAt == nil {
		return false
	}
	m.moves[mv.ServerID] = mv
	return true
}

func (m *Moves) update(serverID string, change func(*Move)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	change(m.moves[serverID])
}

func (m *Moves) list() []Move {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := make([]Move, 0, len(m.moves))
	for _, mv := range m.moves {
		list = append(list, *mv)
	}
	slices.SortFunc(list, func(a, b Move) int { return b.StartedAt.Compare(a.StartedAt) })
	return list
}

// listMoves returns the moves of the servers the user may see.
func (h *Handler) listMoves(w http.ResponseWriter, r *http.Request) {
	grants := access.From(r.Context())
	moves := slices.DeleteFunc(h.moves.list(), func(mv Move) bool {
		return !grants.On(access.ServersView, mv.From, mv.ServerID) && !grants.On(access.ServersView, mv.To, mv.ServerID)
	})
	httpapi.WriteJSON(w, http.StatusOK, moves)
}

type moveRequest struct {
	Node    string `json:"node"`
	Port    uint32 `json:"port"`
	Storage string `json:"storage"`
	Backups bool   `json:"backups"`
	// WithoutDatabases confirms the move to a node that doesn't reach the datastores of the
	// server's network.
	WithoutDatabases bool `json:"withoutDatabases"`
}

// move starts moving a server to another node, which needs the permission to create
// servers there. It checks what it can before it stops the server.
func (h *Handler) move(w http.ResponseWriter, r *http.Request) {
	var req moveRequest
	if err := httpapi.ReadJSON(w, r, &req); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	from, id := r.PathValue("node"), r.PathValue("id")
	var src *noryxv1.Server
	release, err := h.checkMove(ctx, from, id, &req, &src)
	var mv *Move
	if err == nil {
		mv = &Move{ServerID: id, ServerName: src.GetName(), From: from, To: req.Node, Phase: "stopping", Warnings: []string{}, StartedAt: time.Now()}
		if to, err := h.nodes.Get(ctx, req.Node); err == nil {
			mv.ToName = to.Name
		}
		if !h.moves.start(mv) {
			release()
			err = errMoving
		}
	}
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	logging.Note(ctx, slog.String("to", mv.ToName), slog.Bool("backups", req.Backups))
	user, _ := auth.UserFrom(ctx)
	started := *mv // the move changes mv from now on
	go func() {
		defer release()
		h.runMove(context.WithoutCancel(r.Context()), started, src, req, user)
	}()
	httpapi.WriteJSON(w, http.StatusAccepted, started)
}

// checkMove finds the server and checks that it can move to the node of the request with
// its port and storage location, which default to the server's port and the node's default
// storage. Its memory is reserved on the node until release is called; see checkLimits.
func (h *Handler) checkMove(ctx context.Context, from, id string, req *moveRequest, src **noryxv1.Server) (release func(), err error) {
	switch {
	case req.Node == "" || req.Node == from:
		return nil, httpapi.Errorf(http.StatusBadRequest, "Choose another node.")
	case !access.From(ctx).On(access.ServersCreate, req.Node, ""):
		return nil, access.Denied(access.ServersCreate)
	}
	source, err := h.find(ctx, from, id)
	if err != nil {
		return nil, err
	}
	if err := h.networks.CheckMove(ctx, id, from, req.Node, req.WithoutDatabases); err != nil {
		return nil, err
	}
	*src = source
	to, err := h.nodes.Get(ctx, req.Node)
	if err != nil {
		return nil, err
	}
	req.Port = cmp.Or(req.Port, source.GetPort())
	req.Storage = cmp.Or(req.Storage, to.DefaultStorage)
	conn, err := h.nodes.Conn(ctx, to.ID)
	if err != nil {
		return nil, err
	}
	info, err := noryxv1.NewNodeServiceClient(conn).GetInfo(ctx, &noryxv1.GetInfoRequest{})
	if err != nil {
		return nil, err
	}
	list, err := noryxv1.NewServerServiceClient(conn).ListServers(ctx, &noryxv1.ListServersRequest{})
	if err != nil {
		return nil, err
	}
	used := slices.IndexFunc(list.GetServers(), func(s *noryxv1.Server) bool { return s.GetPort() == req.Port })
	switch {
	case !slices.ContainsFunc(info.GetStorage(), func(l *noryxv1.StorageLocation) bool { return l.GetName() == req.Storage }):
		return nil, httpapi.Errorf(http.StatusBadRequest, "Choose a storage location of %s.", to.Name)
	case info.GetCpuCount() > 0 && source.GetCpuMillis() > info.GetCpuCount()*1000:
		return nil, httpapi.Errorf(http.StatusConflict, "%s has %d CPU cores. Lower the CPU limit of the server first.", to.Name, info.GetCpuCount())
	case used >= 0:
		return nil, httpapi.Errorf(http.StatusConflict, "Port %d is used by %q on %s. Choose another port.", req.Port, list.GetServers()[used].GetName(), to.Name)
	case slices.ContainsFunc(list.GetServers(), func(s *noryxv1.Server) bool { return s.GetId() == id }):
		return nil, httpapi.Errorf(http.StatusConflict, "The server exists on %s already.", to.Name)
	}
	return h.checkLimits(ctx, to.ID, "", req.Port, source.GetMemoryMb())
}

// find returns a server of a node.
func (h *Handler) find(ctx context.Context, nodeID, id string) (*noryxv1.Server, error) {
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

// runMove moves the server and logs how it went.
func (h *Handler) runMove(ctx context.Context, mv Move, src *noryxv1.Server, req moveRequest, user auth.User) {
	ctx, cancel := context.WithTimeout(ctx, moveTimeout)
	defer cancel()
	running := src.GetState() != noryxv1.ServerState_SERVER_STATE_STOPPED
	imported, err := h.transfer(ctx, mv, src, req, running)
	var warnings []string
	if err == nil {
		warnings = h.finish(ctx, mv, running)
		if w := olderAgentWarning(src.GetStopTimeoutSeconds(), src.GetTimeZone(), imported); w != "" {
			warnings = append(warnings, w)
		}
		if w := olderAgentAdvanced(advancedOf(src), imported); w != "" {
			warnings = append(warnings, w)
		}
	}
	attrs := []any{logging.Servers, logging.KeyNode, mv.From, logging.KeyServer, mv.ServerID,
		logging.KeyServerName, mv.ServerName, "to", mv.ToName, "to_id", mv.To}
	for _, a := range user.LogAttrs() {
		attrs = append(attrs, a)
	}
	if n, err := h.nodes.Get(ctx, mv.From); err == nil {
		attrs = append(attrs, logging.KeyNodeName, n.Name)
	}
	if err != nil {
		slog.Warn("Move server failed", append(attrs, "err", status.Convert(err).Message())...)
	} else {
		slog.Info("Moved server", append(attrs, "warnings", warnings)...)
	}
	h.moves.update(mv.ServerID, func(m *Move) {
		now := time.Now()
		m.FinishedAt, m.Warnings = &now, append(m.Warnings, warnings...)
		if m.Phase = "done"; err != nil {
			m.Phase, m.Error = "failed", status.Convert(err).Message()
		}
	})
}

// transfer stops the server and copies it, and its backups if asked, to the new node, which
// answers with the server as it imported it. If that fails, the copy goes away and the
// server runs again as before.
func (h *Handler) transfer(ctx context.Context, mv Move, src *noryxv1.Server, req moveRequest, running bool) (imported *noryxv1.Server, err error) {
	from, err := h.nodes.Conn(ctx, mv.From)
	if err != nil {
		return nil, err
	}
	to, err := h.nodes.Conn(ctx, mv.To)
	if err != nil {
		return nil, err
	}
	source, target := noryxv1.NewServerServiceClient(from), noryxv1.NewServerServiceClient(to)
	if running {
		if _, err := source.StopServer(ctx, &noryxv1.StopServerRequest{Id: mv.ServerID}); err != nil {
			return nil, err
		}
	}
	defer func() {
		if err == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), actionTimeout)
		defer cancel()
		if _, err := target.DeleteServer(ctx, &noryxv1.DeleteServerRequest{Id: mv.ServerID}); err != nil && status.Code(err) != codes.NotFound {
			slog.Error("Can't delete the copy of a server that failed to move", logging.Servers, logging.KeyNode, mv.To, logging.KeyServer, mv.ServerID, "err", err)
		}
		if running {
			if _, err := source.StartServer(ctx, &noryxv1.StartServerRequest{Id: mv.ServerID}); err != nil {
				slog.Error("Can't start a server again that failed to move", logging.Servers, logging.KeyNode, mv.From, logging.KeyServer, mv.ServerID, "err", err)
			}
		}
		if status.Code(err) == codes.Unimplemented {
			err = httpapi.Errorf(http.StatusNotImplemented, "Update the agents of both nodes to move servers between them.")
		}
	}()

	progress := func(n int) { h.moves.update(mv.ServerID, func(m *Move) { m.Bytes += int64(n) }) }
	h.moves.update(mv.ServerID, func(m *Move) { m.Phase = "copying" })
	header := &noryxv1.Server{
		Id: mv.ServerID, Name: src.GetName(), Type: src.GetType(), Version: src.GetVersion(), MemoryMb: src.GetMemoryMb(),
		Port: req.Port, Storage: req.Storage, Java: src.GetJava(), RestartPolicy: src.GetRestartPolicy(),
		AikarFlags: src.GetAikarFlags(), JvmOptions: src.GetJvmOptions(), CpuMillis: src.GetCpuMillis(), LoaderVersion: src.GetLoaderVersion(),
		StopTimeoutSeconds: src.GetStopTimeoutSeconds(), TimeZone: src.GetTimeZone(),
		HealthStartMinutes: src.GetHealthStartMinutes(), CrashLimit: src.GetCrashLimit(), StableAfterMinutes: src.GetStableAfterMinutes(),
		UnhealthyRestartMinutes: src.GetUnhealthyRestartMinutes(),
	}
	var res *noryxv1.ImportServerResponse
	res, err = node.Relay(ctx,
		func(ctx context.Context) (grpc.ServerStreamingClient[noryxv1.ArchiveDirectoryResponse], error) {
			return noryxv1.NewFileServiceClient(from).ArchiveDirectory(ctx, &noryxv1.ArchiveDirectoryRequest{ServerId: mv.ServerID, Path: "."})
		},
		target.ImportServer,
		&noryxv1.ImportServerRequest{Content: &noryxv1.ImportServerRequest_Header{Header: header}},
		func(data []byte) *noryxv1.ImportServerRequest {
			return &noryxv1.ImportServerRequest{Content: &noryxv1.ImportServerRequest_Data{Data: data}}
		},
		progress)
	if err != nil || !req.Backups {
		return res.GetServer(), err
	}

	backups := noryxv1.NewBackupServiceClient(from)
	list, err := backups.ListBackups(ctx, &noryxv1.ListBackupsRequest{ServerId: mv.ServerID})
	if err != nil {
		return nil, err
	}
	h.moves.update(mv.ServerID, func(m *Move) { m.Phase, m.BackupsTotal = "backups", len(list.GetBackups()) })
	for _, b := range list.GetBackups() {
		imported := &noryxv1.Backup{
			Id: b.GetId(), Label: b.GetLabel(), CreatedUnix: b.GetCreatedUnix(), Location: req.Storage, Paths: b.GetPaths(), JobId: b.GetJobId(),
			Exclude: b.GetExclude(), Kept: b.GetKept(), Untrusted: b.GetUntrusted(),
			Size: b.GetSize(), // refused upfront if it doesn't fit
		}
		copied, err := node.Relay(ctx,
			func(ctx context.Context) (grpc.ServerStreamingClient[noryxv1.DownloadBackupResponse], error) {
				return backups.DownloadBackup(ctx, &noryxv1.DownloadBackupRequest{ServerId: mv.ServerID, BackupId: b.GetId()})
			},
			noryxv1.NewBackupServiceClient(to).ImportBackup,
			&noryxv1.ImportBackupRequest{Content: &noryxv1.ImportBackupRequest_Header{Header: &noryxv1.ImportBackupHeader{ServerId: mv.ServerID, Backup: imported}}},
			func(data []byte) *noryxv1.ImportBackupRequest {
				return &noryxv1.ImportBackupRequest{Content: &noryxv1.ImportBackupRequest_Data{Data: data}}
			},
			progress)
		// Agents of older versions would restore what the backup left out as missing, let its
		// job delete it, and trust one from elsewhere.
		if got := copied.GetBackup(); err == nil && (len(got.GetExclude()) != len(b.GetExclude()) || got.GetKept() != b.GetKept() || b.GetUntrusted() && !got.GetUntrusted()) {
			err = httpapi.Errorf(http.StatusNotImplemented, "Update the agent of %s to move backups that leave out files, are kept or came from elsewhere.", mv.ToName)
		}
		if err != nil {
			return nil, fmt.Errorf("backup %q: %w", cmp.Or(b.GetLabel(), b.GetId()), err)
		}
		h.moves.update(mv.ServerID, func(m *Move) { m.Backups++ })
	}
	return res.GetServer(), nil
}

// finish points everything at the server's new node, starts it if it ran and deletes the
// original. What fails is a warning, as the server is on the new node already.
func (h *Handler) finish(ctx context.Context, mv Move, running bool) []string {
	h.moves.update(mv.ServerID, func(m *Move) { m.Phase = "finishing" })
	warnings := []string{}
	warn := func(format string, err error) {
		if err != nil {
			warnings = append(warnings, fmt.Sprintf(format, status.Convert(err).Message()))
		}
	}
	for _, ref := range h.refs {
		warn("Some references to the server were not updated: %s", ref.Move(ctx, mv.ServerID, mv.From, mv.To))
	}
	warn("Its network couldn't be configured again: %s Apply the network again on its page.", h.networks.Move(ctx, mv.ServerID, mv.From, mv.To))
	ctx, cancel := context.WithTimeout(ctx, actionTimeout)
	defer cancel()
	if running {
		warn("It couldn't be started on the new node: %s", h.start(ctx, mv.To, mv.ServerID))
	}
	warn("The original couldn't be deleted from the old node: %s Delete it there.", h.remove(ctx, mv.From, mv.ServerID))
	return warnings
}

func (h *Handler) start(ctx context.Context, nodeID, id string) error {
	conn, err := h.nodes.Conn(ctx, nodeID)
	if err == nil {
		_, err = noryxv1.NewServerServiceClient(conn).StartServer(ctx, &noryxv1.StartServerRequest{Id: id})
	}
	return err
}

func (h *Handler) remove(ctx context.Context, nodeID, id string) error {
	conn, err := h.nodes.Conn(ctx, nodeID)
	if err == nil {
		_, err = noryxv1.NewServerServiceClient(conn).DeleteServer(ctx, &noryxv1.DeleteServerRequest{Id: id})
	}
	return err
}
