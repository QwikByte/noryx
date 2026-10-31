package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/node"
)

const (
	// maxArchive limits the archive of an imported server like the uploads of the file manager;
	// the agent too.
	maxArchive  = 16 << 30
	maxSettings = 64 << 10
)

// imported are the settings of a server created from an archive: those of a new server
// without what the archive brings, such as server.properties.
type imported struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Version    string `json:"version"`
	MemoryMB   uint32 `json:"memoryMb"`
	Port       uint32 `json:"port"`
	AcceptEULA bool   `json:"acceptEula"`
	Storage    string `json:"storage"`
	settings
}

// importServer creates a server whose data is a ZIP or .tar.gz archive of a server from
// elsewhere. The body is multipart: the part "server" holds the settings as JSON, the part
// "archive" the archive, which streams through the master to the agent, like an upload of the
// file manager. The agent checks the archive and cleans it up; a server without all of it is
// deleted. It is no operation, as the request carries the archive, and cancelling it deletes
// the server.
func (h *Handler) importServer(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	nodeID := r.PathValue("node")
	r.Body = http.MaxBytesReader(w, r.Body, maxArchive+maxSettings)
	var req imported
	parts, err := r.MultipartReader()
	var archive *multipart.Part
	if err != nil {
		err = httpapi.Errorf(http.StatusBadRequest, "Send the settings and the archive of the server as multipart/form-data.")
	} else {
		archive, err = readImport(parts, &req)
	}
	var policy noryxv1.RestartPolicy
	var cpuMillis uint32
	if err == nil {
		policy, cpuMillis, err = req.check()
	}
	release := noRelease
	if err == nil {
		var reserved func()
		typ := noryxv1.ParseServerType(req.Type)
		if err = h.checkType(ctx, nodeID, typ); err == nil {
			reserved, err = h.checkLimits(ctx, nodeID, "", req.Port, noryxv1.LastPort(typ, req.Port, req.MaxPlayers), req.MemoryMB)
		}
		if err == nil {
			release = reserved
		}
	}
	defer release()
	var res *noryxv1.CreateServerFromArchiveResponse
	if err == nil {
		res, err = h.upload(ctx, nodeID, &noryxv1.CreateServerFromArchiveHeader{Size: max(r.ContentLength, 0), Server: &noryxv1.CreateServerRequest{
			Name: req.Name, Type: noryxv1.ParseServerType(req.Type), Version: req.Version, MemoryMb: req.MemoryMB, Port: req.Port,
			AcceptEula: req.AcceptEULA, Storage: req.Storage, Java: req.Java, RestartPolicy: policy, AikarFlags: req.AikarFlags,
			JvmOptions: req.JVMOptions, CpuMillis: cpuMillis, LoaderVersion: req.LoaderVersion, StopTimeoutSeconds: req.StopTimeout,
			TimeZone: req.TimeZone, HealthStartMinutes: req.HealthStart, CrashLimit: req.CrashLimit, StableAfterMinutes: req.StableAfter,
			UnhealthyRestartMinutes: req.UnhealthyRestart, ProcessLimit: req.ProcessLimit, InitMemoryMb: req.InitMemoryMB, MaxPlayers: req.MaxPlayers,
		}}, archive)
	}
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		err = httpapi.Errorf(http.StatusRequestEntityTooLarge, "Archives can have up to %d GB.", maxArchive>>30)
	case status.Code(err) == codes.Unimplemented:
		err = httpapi.Errorf(http.StatusNotImplemented, "Update the agent of the node to create servers from archives.")
	}
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	srv := res.GetServer()
	logging.Note(ctx, slog.String(logging.KeyServer, srv.GetId()), slog.String(logging.KeyServerName, srv.GetName()), slog.Any("left_out", res.GetLeftOut()))
	httpapi.WriteJSON(w, http.StatusCreated, struct {
		view
		// LeftOut are files of the archive the server didn't get, e.g. those with secrets.
		LeftOut []string `json:"leftOut"`
		// Warning tells what the server didn't get from an older agent.
		Warning string `json:"warning,omitempty"`
	}{toView(srv), append([]string{}, res.GetLeftOut()...), strings.TrimSpace(olderAgentWarning(req.StopTimeout, req.TimeZone, srv) + " " + olderAgentAdvanced(req.advanced, srv))})
}

// readImport reads the settings of an imported server from the part "server" of a request,
// and returns the next part, the archive.
func readImport(parts *multipart.Reader, req *imported) (*multipart.Part, error) {
	part, err := parts.NextPart()
	if err != nil || part.FormName() != "server" {
		return nil, httpapi.Errorf(http.StatusBadRequest, "Send the settings of the server first, in the part server.")
	}
	dec := json.NewDecoder(io.LimitReader(part, maxSettings))
	dec.DisallowUnknownFields()
	if err := dec.Decode(req); err != nil {
		return nil, httpapi.Errorf(http.StatusBadRequest, "The settings of the server are invalid: %s", err)
	}
	if part, err = parts.NextPart(); err != nil || part.FormName() != "archive" {
		return nil, httpapi.Errorf(http.StatusBadRequest, "Send the archive of the server in the part archive.")
	}
	return part, nil
}

// upload creates a server on a node from an archive, which the agent receives in chunks.
func (h *Handler) upload(ctx context.Context, nodeID string, header *noryxv1.CreateServerFromArchiveHeader, archive io.Reader) (*noryxv1.CreateServerFromArchiveResponse, error) {
	conn, err := h.nodes.Conn(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	return node.Upload(ctx, noryxv1.NewServerServiceClient(conn).CreateServerFromArchive,
		&noryxv1.CreateServerFromArchiveRequest{Content: &noryxv1.CreateServerFromArchiveRequest_Header{Header: header}},
		func(data []byte) *noryxv1.CreateServerFromArchiveRequest {
			return &noryxv1.CreateServerFromArchiveRequest{Content: &noryxv1.CreateServerFromArchiveRequest_Data{Data: data}}
		}, archive)
}
