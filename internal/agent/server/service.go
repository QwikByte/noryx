// Package server implements the ServerService of the agent on top of a runtime.
// All input from the master and the local CLI is validated here.
package server

import (
	"archive/zip"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/archive"
	"github.com/QwikByte/noryx/internal/agent/datadir"
	"github.com/QwikByte/noryx/internal/agent/fileset"
	mcnet "github.com/QwikByte/noryx/internal/agent/network"
	"github.com/QwikByte/noryx/internal/agent/properties"
	"github.com/QwikByte/noryx/internal/agent/runtime"
	"github.com/QwikByte/noryx/internal/agent/secrets"
	"github.com/QwikByte/noryx/internal/agent/storage"
	"github.com/QwikByte/noryx/internal/logging"
)

const (
	minPort    = 1024
	maxPort    = 65535
	maxTail    = 1000
	maxCommand = 1000
	// Limits of the configuration of a network.
	maxBackends    = 256
	maxForcedHosts = 256
	maxMotd        = 256
	// Limits of the JVM options and the CPU limit.
	maxJVMOptions = 32
	minCPUMillis  = 100
)

// errUnknownType is returned for server types of newer masters.
var errUnknownType = status.Error(codes.FailedPrecondition, "This node's agent doesn't know this server type yet. Update it first.")

var (
	namePattern    = regexp.MustCompile(`^[\pL\pN][\pL\pN _.-]{0,31}$`)
	versionPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)
	// Versions of mod loaders, e.g. 0.16.10 or 1.20.1-47.3.0, end up in a variable of the image.
	loaderVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
	secretPattern        = regexp.MustCompile(`^[A-Za-z0-9]{16,128}$`)
	hostPattern          = regexp.MustCompile(`^[A-Za-z0-9.:-]{1,253}$`)
	// Host names that players connect through, e.g. survival.example.com.
	forcedHostPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)
	// JVM options end up in a shell variable that the image splits at spaces, so they
	// have no spaces, quotes or glob characters.
	jvmOptionPattern = regexp.MustCompile(`^-[A-Za-z0-9:._+=,/@%-]{1,200}$`)
	// memoryOption matches options that would override the memory the agent manages.
	memoryOption = regexp.MustCompile(`^-(Xm[sx]|XX:(Max|Min|Initial)RAM)`)
	// codeOption, codeFlag and codeProperty match the names of options, -XX flags and system
	// properties that can run code, so that changing settings can't run code in the container.
	// codeOption: agents, a debugger, and class or module paths and programs to run.
	codeOption = regexp.MustCompile(`^-(javaagent:|agentpath:|agentlib:|Xrun|Xbootclasspath|(cp|classpath|-class-path|p|-module-path|-upgrade-module-path|-patch-module|m|-module|jar|-source)$)`)
	// codeFlag: commands on errors, options read from files, and classes or native code
	// loaded from archives, checkpoints or JVMCI compilers.
	codeFlag = regexp.MustCompile(`^(On(OutOfMemory)?Error|VMOptionsFile|Flags|SharedArchiveFile|AOT\w*|CRaC\w*|\w*JVMCI\w*)$`)
	// codeProperty: the properties of Java, JMX and JNDI, logging libraries and JNA, which
	// name classes, native libraries or configurations to load, also from URLs, e.g.
	// log4j2.configurationFile; except safeProperties.
	codeProperty   = regexp.MustCompile(`(?i)^(log4j|(java|javax|jdk|sun|com\.sun|jvmci|jna|logback|org\.apache\.logging)\.)`)
	safeProperties = []string{"java.awt.headless", "java.net.preferIPv4Stack", "java.net.preferIPv6Addresses", "sun.stdout.encoding", "sun.stderr.encoding", "log4j2.formatMsgNoLookups"}
)

type Service struct {
	noryxv1.UnimplementedServerServiceServer
	rt      runtime.Runtime
	backups Backups
	overlay Overlay

	mu       sync.Mutex
	reserved map[uint32]string // ports about to be used, by the ID of their server
}

// Backups are deleted together with their server.
type Backups interface {
	RemoveAll(serverID string) error
}

// Overlay publishes the ports of backends in the private network of the nodes.
type Overlay interface {
	// Admit lets clients, the address in the network of the node of a server's proxy, reach
	// the server's port there as long as its peer has its key, the one the master sent or
	// else the one it has now, and returns the node's address in the network.
	Admit(id string, port uint32, clients, keys []string) (string, error)
	// Dismiss closes the port of a server in the network.
	Dismiss(id string) error
}

func NewService(rt runtime.Runtime, backups Backups, overlay Overlay) *Service {
	return &Service{rt: rt, backups: backups, overlay: overlay, reserved: map[uint32]string{}}
}

func (s *Service) ListServers(ctx context.Context, _ *noryxv1.ListServersRequest) (*noryxv1.ListServersResponse, error) {
	servers, err := s.rt.List(ctx)
	if err != nil {
		return nil, toStatus(err)
	}
	res := &noryxv1.ListServersResponse{}
	for _, srv := range servers {
		res.Servers = append(res.Servers, toProto(srv))
	}
	return res, nil
}

func (s *Service) CreateServer(ctx context.Context, req *noryxv1.CreateServerRequest) (*noryxv1.CreateServerResponse, error) {
	spec, err := newServer(req)
	if err != nil {
		return nil, err
	}
	if msg := properties.Check(spec, req.GetProperties()); msg != "" {
		return nil, status.Error(codes.InvalidArgument, msg)
	}
	release, err := s.check(ctx, spec)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := s.rt.Create(ctx, spec); err != nil {
		return nil, toStatus(err)
	}
	// The server exists now: a cancelled call mustn't leave it half made.
	ctx = context.WithoutCancel(ctx)
	if len(req.GetProperties()) > 0 {
		if err := s.writeProperties(ctx, spec.ID, req.GetProperties()); err != nil {
			return nil, toStatus(errors.Join(err, s.rt.Remove(ctx, spec.ID)))
		}
	}
	return &noryxv1.CreateServerResponse{Server: toProto(runtime.Server{Spec: spec, State: noryxv1.ServerState_SERVER_STATE_STOPPED})}, nil
}

// CreateServerFromArchive creates a server like CreateServer whose data is an archive from
// elsewhere, which is untrusted: it is checked like an archive of the file manager, and the
// server takes neither the files with secrets nor those of the agent from it, nor the secrets
// and the forwarding settings in other files. Nothing of the server remains if that fails.
func (s *Service) CreateServerFromArchive(stream noryxv1.ServerService_CreateServerFromArchiveServer) error {
	ctx := stream.Context()
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	h := first.GetHeader()
	if h == nil {
		return status.Error(codes.InvalidArgument, "the first message must describe the server")
	}
	spec, err := newServer(h.GetServer())
	switch {
	case err != nil:
		return err
	case len(h.GetServer().GetProperties()) > 0:
		return status.Error(codes.InvalidArgument, "A server from an archive takes the server.properties of the archive.")
	}
	release, err := s.check(ctx, spec)
	if err != nil {
		return err
	}
	defer release()
	if err := s.rt.Create(ctx, spec); err != nil {
		return toStatus(err)
	}
	leftOut, err := s.adopt(ctx, spec, h.GetSize(), stream)
	if err != nil {
		if err := s.rt.Remove(context.WithoutCancel(ctx), spec.ID); err != nil {
			slog.Error("Can't delete a server whose archive failed", logging.Servers, logging.KeyServer, spec.ID, "err", err)
		}
		return toStatus(err)
	}
	return stream.SendAndClose(&noryxv1.CreateServerFromArchiveResponse{
		Server: toProto(runtime.Server{Spec: spec, State: noryxv1.ServerState_SERVER_STATE_STOPPED}), LeftOut: leftOut,
	})
}

// adopt receives an archive from elsewhere into a temporary file of the data of a new server,
// which has size bytes if known, extracts it as the server's data and returns what it left out.
func (s *Service) adopt(ctx context.Context, spec runtime.Spec, size int64, stream noryxv1.ServerService_CreateServerFromArchiveServer) ([]string, error) {
	dir, err := s.rt.Data(ctx, spec.ID)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	top, err := dir.Open(".")
	if err != nil {
		return nil, err
	}
	defer top.Close()
	if err := storage.Fits(top, size); err != nil {
		return nil, err
	}
	tmp := datadir.TempName(".")
	f, err := dir.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	defer dir.Remove(tmp) //nolint:errcheck // a leftover is removed with the server
	defer f.Close()
	n, err := archive.Receive(stream.Recv, storage.Guard(f, top))
	if err != nil {
		return nil, err
	}
	a, err := archive.Open(f, n, archive.Uploads)
	if err != nil {
		return nil, err
	}
	res, err := a.Extract(ctx, dir, ".", archive.Options{Protected: archive.Foreign(secrets.With(fileset.ManifestFile)), Skip: true})
	if err != nil {
		return nil, err
	}
	// The server compares its files with those of an empty server, which has no secrets and
	// trusts no proxy.
	empty := datadir.TempName(".")
	if err := dir.Mkdir(empty, 0o700); err != nil {
		return nil, err
	}
	defer dir.Remove(empty) //nolint:errcheck // a leftover is removed with the server
	none, err := dir.Sub(empty)
	if err != nil {
		return nil, err
	}
	defer none.Close()
	return res.LeftOut, errors.Join(secrets.Take(none, dir), mcnet.KeepForwarding(none, dir, spec.Type))
}

// newServer checks the request for a new server and returns the server's settings.
func newServer(req *noryxv1.CreateServerRequest) (runtime.Spec, error) {
	_, knownType := noryxv1.ServerType_name[int32(req.GetType())]
	spec := runtime.Spec{
		ID:            runtime.NewID(),
		Name:          req.GetName(),
		Type:          req.GetType(),
		Version:       cmp.Or(req.GetVersion(), "LATEST"),
		MemoryMB:      req.GetMemoryMb(),
		Port:          req.GetPort(),
		Storage:       req.GetStorage(),
		Java:          req.GetJava(),
		RestartPolicy: req.GetRestartPolicy(),
		AikarFlags:    req.GetAikarFlags(),
		JVMOptions:    req.GetJvmOptions(),
		CPUMillis:     req.GetCpuMillis(),
		LoaderVersion: req.GetLoaderVersion(),
		StopTimeout:   req.GetStopTimeoutSeconds(),
		TimeZone:      req.GetTimeZone(),
	}
	switch {
	case !req.GetAcceptEula() && !req.GetType().Proxy():
		return spec, status.Error(codes.InvalidArgument, "Accept the Minecraft EULA to create a game server.")
	case req.GetType() == noryxv1.ServerType_SERVER_TYPE_UNSPECIFIED:
		return spec, status.Error(codes.InvalidArgument, "Choose a server type.")
	case !knownType:
		return spec, errUnknownType
	}
	return spec, nil
}

func (s *Service) writeProperties(ctx context.Context, id string, changes map[string]string) error {
	dir, err := s.rt.Data(ctx, id)
	if err != nil {
		return err
	}
	return errors.Join(properties.Write(dir, changes), dir.Close())
}

func (s *Service) DuplicateServer(ctx context.Context, req *noryxv1.DuplicateServerRequest) (*noryxv1.DuplicateServerResponse, error) {
	source, err := s.find(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	spec := source.Spec
	spec.ID, spec.Name, spec.Port, spec.BehindProxy, spec.ProxyOnNode = runtime.NewID(), req.GetName(), req.GetPort(), false, false
	spec.BedrockPort, spec.BedrockPlayers, spec.Overlay = 0, false, "" // the copy isn't part of the network
	release, err := s.check(ctx, spec)
	if err != nil {
		return nil, err
	}
	defer release()
	resume, err := runtime.PauseSaving(ctx, s.rt, source)
	if errors.Is(err, runtime.ErrNotReady) {
		return nil, status.Error(codes.FailedPrecondition, "Wait until the server has started, or stop it, to duplicate it.")
	}
	if err != nil {
		return nil, toStatus(err)
	}
	defer resume()
	if err := s.rt.Duplicate(ctx, source.ID, spec); err != nil {
		return nil, toStatus(err)
	}
	// The copy is no target of the file sets of the original.
	if err := s.standalone(ctx, spec.ID, source.ID); err != nil {
		return nil, toStatus(errors.Join(err, s.rt.Remove(context.WithoutCancel(ctx), spec.ID)))
	}
	return &noryxv1.DuplicateServerResponse{Server: toProto(runtime.Server{Spec: spec, State: noryxv1.ServerState_SERVER_STATE_STOPPED})}, nil
}

// standalone removes the files with secrets of file sets from the copy of a server.
func (s *Service) standalone(ctx context.Context, id, original string) error {
	dir, err := s.rt.Data(ctx, id)
	if err != nil {
		return err
	}
	defer dir.Close()
	src, err := s.rt.Data(ctx, original)
	if err != nil {
		return err
	}
	defer src.Close()
	return fileset.Standalone(dir, src)
}

// ImportServer creates a stopped server with the ID and settings of a server of another
// node, from the archive of its data. Nothing of it remains if that fails.
func (s *Service) ImportServer(stream noryxv1.ServerService_ImportServerServer) error {
	ctx := stream.Context()
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	h := first.GetHeader()
	if h == nil {
		return status.Error(codes.InvalidArgument, "the first message must describe the server")
	}
	spec := runtime.Spec{
		ID: h.GetId(), Name: h.GetName(), Type: h.GetType(), Version: h.GetVersion(), MemoryMB: h.GetMemoryMb(), Port: h.GetPort(),
		Storage: h.GetStorage(), Java: h.GetJava(), RestartPolicy: h.GetRestartPolicy(), AikarFlags: h.GetAikarFlags(),
		JVMOptions: h.GetJvmOptions(), CPUMillis: h.GetCpuMillis(), LoaderVersion: h.GetLoaderVersion(),
		StopTimeout: h.GetStopTimeoutSeconds(), TimeZone: h.GetTimeZone(),
	}
	_, knownType := noryxv1.ServerType_name[int32(spec.Type)]
	switch {
	case !runtime.ValidID(spec.ID):
		return status.Error(codes.InvalidArgument, "invalid server ID")
	case spec.Type == noryxv1.ServerType_SERVER_TYPE_UNSPECIFIED:
		return status.Error(codes.InvalidArgument, "Choose a server type.")
	case !knownType:
		return errUnknownType
	}
	release, err := s.check(ctx, spec)
	if err != nil {
		return err
	}
	defer release()
	if _, err := runtime.Find(ctx, s.rt, spec.ID); !errors.Is(err, runtime.ErrNotFound) {
		return cmp.Or(toStatus(err), status.Error(codes.AlreadyExists, "The server exists on this node already."))
	}
	if err := s.rt.Create(ctx, spec); err != nil {
		return toStatus(err)
	}
	if err := s.extract(ctx, spec.ID, stream); err != nil {
		return toStatus(errors.Join(err, s.rt.Remove(context.WithoutCancel(ctx), spec.ID)))
	}
	return stream.SendAndClose(&noryxv1.ImportServerResponse{Server: toProto(runtime.Server{Spec: spec, State: noryxv1.ServerState_SERVER_STATE_STOPPED})})
}

// extract receives an archive into a temporary file of a server's data directory, as
// reading it needs random access, and extracts it there.
func (s *Service) extract(ctx context.Context, id string, stream noryxv1.ServerService_ImportServerServer) error {
	dir, err := s.rt.Data(ctx, id)
	if err != nil {
		return err
	}
	defer dir.Close()
	top, err := dir.Open(".")
	if err != nil {
		return err
	}
	defer top.Close()
	tmp := datadir.TempName(".")
	f, err := dir.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer dir.Remove(tmp) //nolint:errcheck // a leftover is removed with the server
	defer f.Close()
	w, size := storage.Guard(f, top), int64(0) // the old node may send without end
	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		n, err := w.Write(msg.GetData())
		if size += int64(n); err != nil {
			return err
		}
	}
	zr, err := zip.NewReader(f, size)
	if err != nil {
		return err
	}
	return dir.ExtractZip(ctx, zr, ".")
}

func (s *Service) UpdateServer(ctx context.Context, req *noryxv1.UpdateServerRequest) (*noryxv1.UpdateServerResponse, error) {
	srv, err := s.find(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	srv.Name, srv.Version, srv.MemoryMB, srv.Port = req.GetName(), cmp.Or(req.GetVersion(), "LATEST"), req.GetMemoryMb(), req.GetPort()
	srv.Java, srv.RestartPolicy, srv.AikarFlags = req.GetJava(), req.GetRestartPolicy(), req.GetAikarFlags()
	srv.JVMOptions, srv.CPUMillis, srv.LoaderVersion = req.GetJvmOptions(), req.GetCpuMillis(), req.GetLoaderVersion()
	srv.StopTimeout, srv.TimeZone = req.GetStopTimeoutSeconds(), req.GetTimeZone()
	release, err := s.check(ctx, srv.Spec)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := s.rt.Update(ctx, srv.Spec); err != nil {
		return nil, toStatus(err)
	}
	return &noryxv1.UpdateServerResponse{Server: toProto(srv)}, nil
}

func (s *Service) UpdateImage(ctx context.Context, req *noryxv1.UpdateImageRequest) (*noryxv1.UpdateImageResponse, error) {
	var updated bool
	err := s.apply(ctx, req.GetId(), func(ctx context.Context, id string) (err error) {
		updated, err = s.rt.UpdateImage(ctx, id)
		return err
	})
	return &noryxv1.UpdateImageResponse{Updated: updated}, err
}

// find returns the server with the given ID.
func (s *Service) find(ctx context.Context, id string) (runtime.Server, error) {
	if !runtime.ValidID(id) {
		return runtime.Server{}, status.Error(codes.InvalidArgument, "invalid server ID")
	}
	srv, err := runtime.Find(ctx, s.rt, id)
	return srv, toStatus(err)
}

// check validates the settings of a server and reserves its port; see reserve.
func (s *Service) check(ctx context.Context, spec runtime.Spec) (release func(), err error) {
	var cpus uint32
	if info, err := s.rt.Info(ctx); err == nil {
		cpus = info.CPUs
	}
	if msg := checkSettings(spec, cpus); msg != "" {
		return nil, status.Error(codes.InvalidArgument, msg)
	}
	return s.reserve(ctx, spec.ID, spec.Port, func(servers []runtime.Server) error {
		if i := slices.IndexFunc(servers, func(srv runtime.Server) bool {
			return srv.Uses(spec.Port) && (srv.ID != spec.ID || spec.BedrockPort == spec.Port)
		}); i >= 0 {
			return status.Errorf(codes.AlreadyExists, "Port %d is already used by %q. Choose another port.", spec.Port, servers[i].Name)
		}
		return nil
	})
}

// reserve checks with the servers of the node that the server id may use port, and
// reserves it until release is called, once the server's container has it, so that
// concurrent requests can't take the same port.
func (s *Service) reserve(ctx context.Context, id string, port uint32, check func([]runtime.Server) error) (release func(), err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	servers, err := s.rt.List(ctx)
	if err != nil {
		return nil, toStatus(err)
	}
	if err := check(servers); err != nil {
		return nil, err
	}
	datastores, err := s.rt.ListDatastores(ctx)
	if err != nil {
		return nil, toStatus(err)
	}
	if slices.ContainsFunc(datastores, func(ds runtime.Datastore) bool { return ds.Port == port }) {
		return nil, status.Errorf(codes.AlreadyExists, "Port %d is used by a datastore of a network. Choose another port.", port)
	}
	if other, ok := s.reserved[port]; ok && other != id {
		return nil, status.Errorf(codes.AlreadyExists, "Port %d is about to be used by another server. Choose another port.", port)
	}
	s.reserved[port] = id
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.reserved, port)
	}, nil
}

func (s *Service) StartServer(ctx context.Context, req *noryxv1.StartServerRequest) (*noryxv1.StartServerResponse, error) {
	return &noryxv1.StartServerResponse{}, s.apply(ctx, req.GetId(), s.rt.Start)
}

func (s *Service) StopServer(ctx context.Context, req *noryxv1.StopServerRequest) (*noryxv1.StopServerResponse, error) {
	return &noryxv1.StopServerResponse{}, s.apply(ctx, req.GetId(), s.rt.Stop)
}

func (s *Service) RestartServer(ctx context.Context, req *noryxv1.RestartServerRequest) (*noryxv1.RestartServerResponse, error) {
	return &noryxv1.RestartServerResponse{}, s.apply(ctx, req.GetId(), s.rt.Restart)
}

// DeleteServer deletes a server, and succeeds for one that is gone already or is being
// deleted, e.g. by the request of a double click.
func (s *Service) DeleteServer(ctx context.Context, req *noryxv1.DeleteServerRequest) (*noryxv1.DeleteServerResponse, error) {
	if err := s.apply(ctx, req.GetId(), s.rt.Remove); err != nil && status.Code(err) != codes.NotFound {
		return nil, err
	}
	return &noryxv1.DeleteServerResponse{}, toStatus(errors.Join(s.backups.RemoveAll(req.GetId()), s.overlay.Dismiss(req.GetId())))
}

func (s *Service) StreamLogs(req *noryxv1.StreamLogsRequest, stream noryxv1.ServerService_StreamLogsServer) error {
	if !runtime.ValidID(req.GetId()) {
		return status.Error(codes.InvalidArgument, "invalid server ID")
	}
	var after time.Time
	if req.GetAfterUnixNano() > 0 {
		after = time.Unix(0, req.GetAfterUnixNano())
	}
	for line, err := range s.rt.Logs(stream.Context(), req.GetId(), int(min(req.GetTail(), maxTail)), after) {
		if err != nil {
			return toStatus(err)
		}
		res := &noryxv1.StreamLogsResponse{Line: plain(line.Text), Formatted: formatted(line.Text)}
		if !line.Time.IsZero() {
			res.TimeUnixNano = line.Time.UnixNano()
		}
		if err := stream.Send(res); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) SendCommand(ctx context.Context, req *noryxv1.SendCommandRequest) (*noryxv1.SendCommandResponse, error) {
	if !runtime.ValidID(req.GetId()) {
		return nil, status.Error(codes.InvalidArgument, "invalid server ID")
	}
	command := strings.TrimPrefix(strings.TrimSpace(req.GetCommand()), "/")
	if command == "" || len(command) > maxCommand || strings.ContainsFunc(command, unicode.IsControl) {
		return nil, status.Errorf(codes.InvalidArgument, "Enter a single command with up to %d characters.", maxCommand)
	}
	if req.GetNoWait() {
		ctx = runtime.NoWait(ctx)
	}
	output, err := s.rt.SendCommand(ctx, req.GetId(), command)
	switch {
	case errors.Is(err, runtime.ErrUnsupported):
		return nil, status.Error(codes.FailedPrecondition, "This proxy was created by an older agent. It accepts console commands once its settings were saved or its network applied again.")
	case errors.Is(err, runtime.ErrNoSend):
		return nil, status.Error(codes.FailedPrecondition, "The proxy has no send command, as it couldn't download its module cmd_send. Waterfall can't anymore, as it reached its end of life: use Velocity or BungeeCord instead.")
	case errors.Is(err, runtime.ErrNotSent):
		return nil, status.Error(codes.NotFound, err.Error())
	case err != nil:
		return nil, toStatus(err)
	}
	return &noryxv1.SendCommandResponse{Output: plain(output), Formatted: formatted(output)}, nil
}

func (s *Service) ConfigureNetwork(ctx context.Context, req *noryxv1.ConfigureNetworkRequest) (*noryxv1.ConfigureNetworkResponse, error) {
	network, msg := networkOf(req)
	if msg != "" {
		return nil, status.Error(codes.InvalidArgument, msg)
	}
	release, err := s.checkBedrock(ctx, req.GetId(), network.BedrockPort)
	if err != nil {
		return nil, err
	}
	defer release()
	if network.Overlay, err = s.publish(ctx, req.GetId(), req.GetOverlayClient(), req.GetOverlayClientKey()); err != nil {
		return nil, err
	}
	restarted, err := s.rt.Configure(ctx, req.GetId(), network)
	switch {
	case errors.Is(err, runtime.ErrUnsupported):
		return nil, status.Error(codes.FailedPrecondition, "Vanilla servers can't be part of a network, as they can't verify the players a proxy forwards.")
	case errors.Is(err, mcnet.ErrModernOnly), errors.Is(err, runtime.ErrReload):
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	case err != nil:
		return nil, toStatus(err)
	}
	return &noryxv1.ConfigureNetworkResponse{Restarted: restarted}, nil
}

// checkBedrock checks that only a proxy gets a Bedrock port, and reserves it; see reserve.
func (s *Service) checkBedrock(ctx context.Context, id string, port uint32) (release func(), err error) {
	if port == 0 {
		return func() {}, nil
	}
	return s.reserve(ctx, id, port, func(servers []runtime.Server) error {
		i := slices.IndexFunc(servers, func(srv runtime.Server) bool { return srv.ID == id })
		switch {
		case i < 0:
			return toStatus(runtime.ErrNotFound)
		case !servers[i].Type.Proxy():
			return status.Error(codes.InvalidArgument, "Only proxies let Bedrock players join.")
		case port < minPort || port > maxPort:
			return status.Errorf(codes.InvalidArgument, "The Bedrock port must be between %d and %d.", minPort, maxPort)
		}
		if other := slices.IndexFunc(servers, func(srv runtime.Server) bool { return srv.Uses(port) && (srv.ID != id || srv.Port == port) }); other >= 0 {
			return status.Errorf(codes.AlreadyExists, "Port %d is already used by %q. Choose another Bedrock port.", port, servers[other].Name)
		}
		return nil
	})
}

// networkOf validates a network configuration, which ends up in configuration files.
// publish lets the client of a backend, the peer with key if one is given, reach its port in
// the private network of the nodes, and returns the node's address there; without a client,
// it closes the port there.
func (s *Service) publish(ctx context.Context, id, client, key string) (string, error) {
	if client == "" {
		return "", toStatus(s.overlay.Dismiss(id))
	}
	srv, err := s.find(ctx, id)
	if err != nil {
		return "", err
	}
	if srv.Type.Proxy() {
		return "", status.Error(codes.InvalidArgument, "Players reach a proxy at all of the node's addresses.")
	}
	var keys []string
	if key != "" {
		keys = []string{key}
	}
	addr, err := s.overlay.Admit(id, srv.Port, []string{client}, keys)
	if status.Code(err) == codes.Unknown {
		err = status.Error(codes.FailedPrecondition, err.Error())
	}
	return addr, err
}

func networkOf(req *noryxv1.ConfigureNetworkRequest) (runtime.Network, string) {
	network := runtime.Network{ForwardingSecret: req.GetForwardingSecret(), Try: req.GetTry(), BedrockPort: req.GetBedrockPort()}
	switch req.GetForwarding() {
	case noryxv1.Forwarding_FORWARDING_UNSPECIFIED: // sent by older masters
		network.Forwarding = runtime.ForwardingNone
		if network.ForwardingSecret != "" {
			network.Forwarding = runtime.ForwardingModern
		}
	case noryxv1.Forwarding_FORWARDING_NONE:
	case noryxv1.Forwarding_FORWARDING_MODERN:
		network.Forwarding = runtime.ForwardingModern
	case noryxv1.Forwarding_FORWARDING_LEGACY:
		network.Forwarding = runtime.ForwardingLegacy
	default:
		return network, "invalid forwarding"
	}
	behind := network.Forwarding != runtime.ForwardingNone
	network.ProxyOnNode, network.BedrockPlayers = req.GetProxyOnNode() && behind, req.GetBedrockPlayers() && behind
	switch {
	case !runtime.ValidID(req.GetId()):
		return network, "invalid server ID"
	case network.ForwardingSecret != "" && !secretPattern.MatchString(network.ForwardingSecret),
		network.Forwarding == runtime.ForwardingModern && network.ForwardingSecret == "":
		return network, "invalid forwarding secret"
	case len(req.GetBackends()) > maxBackends || len(req.GetForcedHosts()) > maxForcedHosts:
		return network, "too many backends or forced hosts"
	case req.GetOverlayClient() != "" && (!behind || !validIPv4(req.GetOverlayClient())):
		return network, "invalid overlay client"
	case len(req.GetDatastores()) > noryxv1.MaxNetworkDatastores || slices.ContainsFunc(req.GetDatastores(), func(id string) bool { return !runtime.ValidID(id) }):
		return network, "invalid datastores"
	}
	network.Datastores = req.GetDatastores()
	names := map[string]bool{}
	for _, b := range req.GetBackends() {
		backend := runtime.NetworkBackend{Name: b.GetName(), ServerID: b.GetServerId(), Address: b.GetAddress(), Restricted: b.GetRestricted(), Motd: b.GetMotd()}
		switch {
		case !noryxv1.ValidBackendName(backend.Name) || names[backend.Name]:
			return network, fmt.Sprintf("invalid or duplicate backend name %q", backend.Name)
		case backend.ServerID != "" && !runtime.ValidID(backend.ServerID):
			return network, "invalid backend server ID"
		case backend.ServerID == "" && !validAddress(backend.Address):
			return network, fmt.Sprintf("invalid backend address %q", backend.Address)
		case len(backend.Motd) > maxMotd || strings.ContainsFunc(backend.Motd, func(r rune) bool { return r != '\n' && unicode.IsControl(r) }):
			return network, fmt.Sprintf("invalid MOTD of %s", backend.Name)
		}
		names[backend.Name] = true
		network.Backends = append(network.Backends, backend)
	}
	if len(network.Try) == 0 && len(network.Backends) > 0 && req.GetForwarding() == noryxv1.Forwarding_FORWARDING_UNSPECIFIED {
		network.Try = []string{network.Backends[0].Name} // older masters let players join the first
	}
	if msg := checkNames(network.Try, names); msg != "" {
		return network, "invalid list of servers to try: " + msg
	}
	hosts := map[string]bool{}
	for _, h := range req.GetForcedHosts() {
		host := runtime.ForcedHost{Host: h.GetHost(), Servers: h.GetServers()}
		if !forcedHostPattern.MatchString(host.Host) || len(host.Host) > 253 || hosts[host.Host] {
			return network, fmt.Sprintf("invalid or duplicate forced host %q", host.Host)
		}
		if msg := checkNames(host.Servers, names); msg != "" || len(host.Servers) == 0 {
			return network, fmt.Sprintf("invalid servers of the forced host %s: %s", host.Host, cmp.Or(msg, "none"))
		}
		hosts[host.Host] = true
		network.ForcedHosts = append(network.ForcedHosts, host)
	}
	return network, ""
}

// checkNames returns a message if the list names a backend twice or one that doesn't exist.
func checkNames(list []string, backends map[string]bool) string {
	seen := map[string]bool{}
	for _, name := range list {
		if !backends[name] || seen[name] {
			return fmt.Sprintf("unknown or duplicate server %q", name)
		}
		seen[name] = true
	}
	return ""
}

func validAddress(address string) bool {
	host, port, err := net.SplitHostPort(address)
	n, perr := strconv.Atoi(port)
	return err == nil && perr == nil && hostPattern.MatchString(host) && n > 0 && n <= maxPort
}

func validIPv4(s string) bool {
	addr, err := netip.ParseAddr(s)
	return err == nil && addr.Is4()
}

func (s *Service) apply(ctx context.Context, id string, op func(context.Context, string) error) error {
	if !runtime.ValidID(id) {
		return status.Error(codes.InvalidArgument, "invalid server ID")
	}
	return toStatus(op(ctx, id))
}

// checkSettings returns a message for the operator if a setting is invalid. cpus is
// the number of CPU cores of the node, or 0 if unknown.
func checkSettings(spec runtime.Spec, cpus uint32) string {
	_, knownPolicy := noryxv1.RestartPolicy_name[int32(spec.RestartPolicy)]
	switch {
	case !namePattern.MatchString(spec.Name):
		return "Use 1-32 letters, digits, spaces, '.', '_' or '-' for the name."
	case !versionPattern.MatchString(spec.Version):
		return "Enter a Minecraft version like 1.21.4, or leave it empty for the latest."
	case spec.LoaderVersion != "" && !spec.Type.Modded():
		return "Only Fabric, Quilt, Forge and NeoForge servers have a mod loader."
	case spec.LoaderVersion != "" && !loaderVersionPattern.MatchString(spec.LoaderVersion):
		return "Enter the version of the mod loader like 0.16.10, or leave it empty for the newest."
	case spec.MemoryMB < noryxv1.MinMemoryMB || spec.MemoryMB > noryxv1.MaxMemoryMB:
		return fmt.Sprintf("Memory must be between %d and %d MB.", noryxv1.MinMemoryMB, noryxv1.MaxMemoryMB)
	case spec.Port < minPort || spec.Port > maxPort:
		return fmt.Sprintf("Port must be between %d and %d.", minPort, maxPort)
	case !noryxv1.ValidJava(spec.Java):
		return "Choose Java 8, 11, 17, 21 or 25, or the newest."
	case spec.Type.Proxy() && (spec.Java != "" || spec.AikarFlags):
		return "The Java version and Aikar's flags can only be set for game servers."
	case !knownPolicy:
		return "Choose when the server starts on its own."
	case len(spec.JVMOptions) > maxJVMOptions:
		return fmt.Sprintf("Use at most %d JVM options.", maxJVMOptions)
	case spec.CPUMillis != 0 && spec.CPUMillis < minCPUMillis:
		return "Give the server at least 0.1 CPU cores, or no limit."
	case cpus > 0 && spec.CPUMillis > cpus*1000:
		return fmt.Sprintf("The node has %d CPU cores.", cpus)
	case !noryxv1.ValidStopTimeout(spec.StopTimeout):
		return fmt.Sprintf("Give the server %.0f seconds to %.0f minutes to stop.", noryxv1.MinStopTimeout.Seconds(), noryxv1.MaxStopTimeout.Minutes())
	case !noryxv1.ValidTimeZone(spec.TimeZone):
		return "Choose a time zone such as Europe/Berlin, or none for UTC."
	}
	for _, option := range spec.JVMOptions {
		if msg := checkJVMOption(option); msg != "" {
			return msg
		}
	}
	return ""
}

// checkJVMOption returns a message for the operator if a JVM option is refused.
func checkJVMOption(option string) string {
	switch {
	case !jvmOptionPattern.MatchString(option):
		return fmt.Sprintf("The JVM option %q is invalid. Options start with '-' and contain no spaces or quotes.", option)
	case memoryOption.MatchString(option):
		return fmt.Sprintf("Set the memory in the settings instead of with %s.", option)
	case runsCode(option):
		return fmt.Sprintf("The JVM option %s can load or run code, so it can't be set here. Install agents as plugins or mods instead.", option)
	}
	return ""
}

// refusedOptions returns the JVM options of a server that are refused now, as they were set
// before an update of the agent refused them.
func refusedOptions(options []string) []string {
	return slices.DeleteFunc(slices.Clone(options), func(o string) bool { return checkJVMOption(o) == "" })
}

// WarnRefusedOptions logs the servers whose JVM options are refused now, e.g. after an update
// of the agent. They still start with them, until they are removed in their settings.
func (s *Service) WarnRefusedOptions(ctx context.Context) {
	servers, err := s.rt.List(ctx)
	if err != nil {
		return
	}
	for _, srv := range servers {
		if refused := refusedOptions(srv.JVMOptions); len(refused) > 0 {
			slog.Warn("A server starts with JVM options that are refused now; remove them in its settings", logging.Servers,
				logging.KeyServer, srv.ID, logging.KeyServerName, srv.Name, "options", refused)
		}
	}
}

// runsCode reports whether a JVM option can run code, by its name.
func runsCode(option string) bool {
	name, _, _ := strings.Cut(option, "=")
	if key, ok := strings.CutPrefix(name, "-D"); ok {
		return codeProperty.MatchString(key) && !slices.Contains(safeProperties, key)
	}
	if flag, ok := strings.CutPrefix(name, "-XX:"); ok {
		return codeFlag.MatchString(strings.TrimLeft(flag, "+-"))
	}
	return codeOption.MatchString(name)
}

func toProto(s runtime.Server) *noryxv1.Server {
	return &noryxv1.Server{
		Id: s.ID, Name: s.Name, Type: s.Type, Version: s.Version, MemoryMb: s.MemoryMB, Port: s.Port, State: s.State,
		Storage: cmp.Or(s.Storage, storage.Default), Java: s.Java, RestartPolicy: s.RestartPolicy, AikarFlags: s.AikarFlags,
		JvmOptions: s.JVMOptions, CpuMillis: s.CPUMillis, Crashes: uint32(s.Crashes), ExitCode: int32(s.ExitCode), //nolint:gosec // small numbers
		LoaderVersion: s.LoaderVersion, BedrockPort: s.BedrockPort, RefusedJvmOptions: refusedOptions(s.JVMOptions),
		Overlay: s.Overlay != "", Unhealthy: s.Unhealthy, StopTimeoutSeconds: s.StopTimeout, TimeZone: s.TimeZone,
	}
}

func toStatus(err error) error {
	switch {
	case err == nil:
		return nil
	case status.Code(err) != codes.Unknown: // e.g. a refused archive
		return err
	case errors.As(err, new(storage.FullError)):
		return status.Error(codes.ResourceExhausted, err.Error())
	case errors.Is(err, runtime.ErrNotFound):
		return status.Error(codes.NotFound, "Server not found.")
	case errors.Is(err, runtime.ErrNotRunning):
		return status.Error(codes.FailedPrecondition, "Start the server to send commands.")
	case errors.Is(err, runtime.ErrNotReady):
		return status.Error(codes.FailedPrecondition, "The server is starting. Wait until it runs to send commands.")
	case errors.Is(err, runtime.ErrUnsupported):
		return status.Error(codes.FailedPrecondition, "This type of server does not support that.")
	case errors.Is(err, storage.ErrUnknown):
		return status.Errorf(codes.InvalidArgument, "%s. Add it on the node with: noryx-agent storage add", err)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return status.FromContextError(err).Err()
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
