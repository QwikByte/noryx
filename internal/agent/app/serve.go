package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"slices"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/local"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/backup"
	"github.com/QwikByte/noryx/internal/agent/datastore"
	"github.com/QwikByte/noryx/internal/agent/files"
	"github.com/QwikByte/noryx/internal/agent/fileset"
	agentlogs "github.com/QwikByte/noryx/internal/agent/logs"
	"github.com/QwikByte/noryx/internal/agent/network"
	"github.com/QwikByte/noryx/internal/agent/node"
	"github.com/QwikByte/noryx/internal/agent/overlay"
	"github.com/QwikByte/noryx/internal/agent/player"
	"github.com/QwikByte/noryx/internal/agent/plugin"
	"github.com/QwikByte/noryx/internal/agent/progress"
	"github.com/QwikByte/noryx/internal/agent/properties"
	"github.com/QwikByte/noryx/internal/agent/runtime"
	"github.com/QwikByte/noryx/internal/agent/runtime/docker"
	"github.com/QwikByte/noryx/internal/agent/server"
	"github.com/QwikByte/noryx/internal/agent/stats"
	"github.com/QwikByte/noryx/internal/agent/storage"
	"github.com/QwikByte/noryx/internal/buildinfo"
	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/pki"
)

func serve(ctx context.Context, cfg config) error {
	runtimeOpts, err := cfg.runtime.options()
	if err != nil {
		return err
	}
	buf := agentlogs.NewBuffer()
	logFile, err := logging.Setup(cfg.log, buf.Handler(cfg.log.Level))
	if err != nil {
		return err
	}
	defer logFile.Close()
	if err := ensureDataDir(cfg); err != nil {
		return err
	}
	identity, err := node.LoadIdentity(cfg.pkiDir())
	if errors.Is(err, fs.ErrNotExist) {
		return errors.New("this agent is not enrolled yet, run: noryx-agent enroll <join-token>")
	}
	if err != nil {
		return err
	}
	locations := storage.New(cfg.dataDir)
	if runtimeOpts.Podman {
		if err := isolate(); err != nil {
			return err
		}
	}
	rt, err := docker.New(locations, runtimeOpts)
	if err != nil {
		return err
	}
	defer rt.Close()

	// The master connects via mutual TLS, the local CLI via the Unix socket.
	svc := newServices(rt, identity, cfg.dataDir, locations, overlay.NewService(cfg.dataDir, overlay.Linux{}), buf, slog.Default())
	svc.node.Prereleases = cfg.prereleases
	remote := svc.grpcServer(agentlogs.FromMaster, grpc.Creds(credentials.NewTLS(pki.AgentServerTLS(identity.Holder, identity.CA))))
	localSrv := svc.grpcServer(agentlogs.FromLocal, grpc.Creds(local.NewCredentials()))
	go svc.stats.Run(ctx)
	go svc.server.WarnRefusedOptions(ctx)
	go svc.player.Run(ctx)
	go rt.Watch(ctx)

	tcpListener, err := net.Listen("tcp", cfg.listenAddr)
	if err != nil {
		return err
	}
	if err := os.Remove(cfg.socket()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	unixListener, err := net.Listen("unix", cfg.socket())
	if err != nil {
		return fmt.Errorf("%w (choose a shorter path with --socket)", err)
	}
	if err := os.Chmod(cfg.socket(), 0o600); err != nil {
		return err
	}

	errc := make(chan error, 2)
	go func() { errc <- remote.Serve(tcpListener) }()
	go func() { errc <- localSrv.Serve(unixListener) }()
	slog.Info("Agent started", "version", buildinfo.Version, "listen", cfg.listenAddr, "socket", cfg.socket(), "runtime", rt.Name(),
		"identity", identity.Get().Leaf.Subject.CommonName, "certificate_not_after", identity.Get().Leaf.NotAfter,
		"ca_fingerprint", pki.Fingerprint(identity.CA))

	select {
	case err = <-errc:
	case <-ctx.Done():
	}
	// Running servers are not affected: containers keep running without the agent.
	remote.Stop()
	localSrv.Stop()
	if err != nil {
		slog.Error("Agent stopped", "err", err)
	} else {
		slog.Info("Agent stopped")
	}
	return err
}

// services are the gRPC services of the agent. The servers for the master and the local CLI
// share them, so both see the same state, e.g. a backup in progress.
type services struct {
	rt         runtime.Runtime
	node       *node.Service
	server     *server.Service
	files      *files.Service
	filesets   *fileset.Service
	properties *properties.Service
	proxy      *network.Service
	plugin     *plugin.Service
	player     *player.Service
	backup     *backup.Service
	datastore  *datastore.Service
	overlay    *overlay.Service
	stats      *stats.Service
	log        *agentlogs.Service
	progress   *progress.Registry
	calls      *slog.Logger
}

// newServices returns the services, which log the calls they receive to calls, and applies the
// settings of the node kept in dataDir.
func newServices(rt runtime.Runtime, identity *node.Identity, dataDir string, locations *storage.Locations, ov *overlay.Service, buf *agentlogs.Buffer, calls *slog.Logger) *services {
	backups := backup.NewService(rt, locations)
	measures := stats.NewService(rt)
	return &services{
		rt:         rt,
		node:       node.NewService(rt, identity, locations, node.LoadSettings(dataDir, rt, measures)),
		server:     server.NewService(rt, backups, ov),
		files:      files.NewService(rt),
		filesets:   fileset.NewService(rt),
		properties: properties.NewService(rt),
		proxy:      network.NewService(rt),
		plugin:     plugin.NewService(rt),
		player:     player.NewService(rt),
		backup:     backups,
		datastore:  datastore.NewService(rt, locations, ov),
		overlay:    ov,
		stats:      measures,
		log:        agentlogs.NewService(buf),
		progress:   progress.NewRegistry(),
		calls:      calls,
	}
}

// grpcServer serves the services to callers from origin, logging their calls. If a call failed
// because the runtime is down, the log keeps the original error and the caller learns that.
func (s *services) grpcServer(origin string, opts ...grpc.ServerOption) *grpc.Server {
	opts = slices.Concat(runtime.Interceptors(s.rt), agentlogs.Interceptors(origin, s.calls), []grpc.ServerOption{s.progress.Interceptor()}, opts)
	srv := grpc.NewServer(opts...)
	noryxv1.RegisterNodeServiceServer(srv, s.node)
	noryxv1.RegisterServerServiceServer(srv, s.server)
	noryxv1.RegisterFileServiceServer(srv, s.files)
	noryxv1.RegisterFileSetServiceServer(srv, s.filesets)
	noryxv1.RegisterPropertiesServiceServer(srv, s.properties)
	noryxv1.RegisterProxyServiceServer(srv, s.proxy)
	noryxv1.RegisterPluginServiceServer(srv, s.plugin)
	noryxv1.RegisterPlayerServiceServer(srv, s.player)
	noryxv1.RegisterBackupServiceServer(srv, s.backup)
	noryxv1.RegisterDatastoreServiceServer(srv, s.datastore)
	noryxv1.RegisterOverlayServiceServer(srv, s.overlay)
	noryxv1.RegisterStatsServiceServer(srv, s.stats)
	noryxv1.RegisterLogServiceServer(srv, s.log)
	noryxv1.RegisterProgressServiceServer(srv, s.progress)
	return srv
}

// NewGRPCServer registers all agent services on a new gRPC server for the master. The calls
// are logged to buf, which the log service reads.
func NewGRPCServer(rt runtime.Runtime, identity *node.Identity, dataDir string, locations *storage.Locations, ov *overlay.Service, buf *agentlogs.Buffer, opts ...grpc.ServerOption) *grpc.Server {
	calls := slog.New(buf.Handler(slog.LevelDebug))
	return newServices(rt, identity, dataDir, locations, ov, buf, calls).grpcServer(agentlogs.FromMaster, opts...)
}
