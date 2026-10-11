package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/buildinfo"
	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/auth"
	"github.com/QwikByte/noryx/internal/master/backup"
	"github.com/QwikByte/noryx/internal/master/datastore"
	"github.com/QwikByte/noryx/internal/master/files"
	"github.com/QwikByte/noryx/internal/master/fileset"
	"github.com/QwikByte/noryx/internal/master/geysermc"
	"github.com/QwikByte/noryx/internal/master/hangar"
	"github.com/QwikByte/noryx/internal/master/https"
	"github.com/QwikByte/noryx/internal/master/logs"
	"github.com/QwikByte/noryx/internal/master/modpack"
	"github.com/QwikByte/noryx/internal/master/modrinth"
	"github.com/QwikByte/noryx/internal/master/mojang"
	"github.com/QwikByte/noryx/internal/master/network"
	"github.com/QwikByte/noryx/internal/master/node"
	"github.com/QwikByte/noryx/internal/master/notify"
	"github.com/QwikByte/noryx/internal/master/operation"
	"github.com/QwikByte/noryx/internal/master/overlay"
	"github.com/QwikByte/noryx/internal/master/player"
	"github.com/QwikByte/noryx/internal/master/plugin"
	"github.com/QwikByte/noryx/internal/master/policy"
	"github.com/QwikByte/noryx/internal/master/preference"
	"github.com/QwikByte/noryx/internal/master/properties"
	"github.com/QwikByte/noryx/internal/master/schedule"
	"github.com/QwikByte/noryx/internal/master/server"
	"github.com/QwikByte/noryx/internal/master/settings"
	"github.com/QwikByte/noryx/internal/master/tag"
	"github.com/QwikByte/noryx/internal/master/template"
	"github.com/QwikByte/noryx/internal/master/terminal"
	"github.com/QwikByte/noryx/internal/master/update"
	"github.com/QwikByte/noryx/internal/master/usage"
	"github.com/QwikByte/noryx/internal/master/workflow"
	"github.com/QwikByte/noryx/internal/pki"
	"github.com/QwikByte/noryx/web"
)

// RestartExit is the error serve returns when an administrator restarts the master from the
// panel. The program exits with it as code, for which its service manager starts it again.
type RestartExit int

func (r RestartExit) Error() string { return fmt.Sprintf("restart with exit code %d", int(r)) }

func serve(ctx context.Context, cfg config) error {
	proxies, err := auth.ParseProxies(cfg.trustedProxies)
	if err != nil {
		return err
	}
	db, err := openDB(cfg.dataDir)
	if err != nil {
		return err
	}
	defer db.Close()
	ca, err := pki.LoadOrCreateCA(filepath.Join(cfg.dataDir, "pki"))
	if err != nil {
		return err
	}
	cert, err := ca.MasterCertificate()
	if err != nil {
		return err
	}
	masterCert, err := pki.NewHolder(cert)
	if err != nil {
		return err
	}
	publicAddr, err := cfg.publicEnrollAddr()
	if err != nil {
		return err
	}

	master := settings.Master{
		Version: buildinfo.Version, StartedAt: time.Now(), PanelDefaultAddr: cfg.httpAddr,
		EnrollListenAddr: cfg.enrollAddr, EnrollAddr: publicAddr, CAFingerprint: pki.Fingerprint(ca.Cert),
		Restartable: cfg.restartCode != 0,
	}
	if cfg.tlsCert != "" {
		master.PanelHTTPS = https.Files
	}
	conf, err := settings.Load(ctx, db, master, masterCert)
	if err != nil {
		return err
	}
	panelListener, err := conf.ListenPanel()
	if err != nil {
		return err
	}
	defer panelListener.Close()
	panelCert, err := conf.PanelHTTPS(filepath.Join(cfg.dataDir, "https"))
	if err != nil {
		return err
	}

	users := auth.NewService(db)
	nodes := node.NewService(db, ca, masterCert, conf)
	defer nodes.Close()
	names := logs.NewNames(nodes)
	logStore := logs.NewStore(db, names, conf)
	logFile, err := logging.Setup(cfg.log, logStore.Handler(cfg.log.Level))
	if err != nil {
		return err
	}
	defer logFile.Close()
	logStore.Start(ctx)
	defer logStore.Close() // before the log file and the database close
	go logStore.Collect(ctx, nodes)
	go masterCert.Maintain(ctx, time.Hour, ca.MasterCertificate)
	go nodes.MaintainCertificates(ctx, 6*time.Hour)
	go nodes.WatchPresence(ctx, 30*time.Second)
	if ok, err := users.HasUsers(ctx); err == nil && !ok {
		slog.Warn("No administrator account exists yet, create one with: noryx-master user add <username>", logging.Auth)
	}
	if m := conf.Master(); m.PanelAddrError != "" {
		slog.Warn("The panel can't listen at the address from the settings, so it listens at the one from the command line",
			logging.Settings, "addr", m.PanelAddr, "err", m.PanelAddrError)
	}

	enrollListener, err := net.Listen("tcp", cfg.enrollAddr)
	if err != nil {
		return err
	}
	grpcServer := grpc.NewServer(grpc.Creds(credentials.NewTLS(pki.MasterServerTLS(masterCert))))
	noryxv1.RegisterEnrollmentServiceServer(grpcServer, nodes)
	modrinthClient := modrinth.New(modrinth.DefaultAPI, modrinth.DefaultCDN)
	geyser := geysermc.New(geysermc.DownloadAPI, geysermc.GlobalAPI, geysermc.CacheTime)
	plugins := plugin.NewService(db, nodes, modrinthClient, hangar.New(hangar.DefaultAPI, hangar.DefaultCDN), geyser)
	moves := server.NewMoves()
	// Requests whose operation takes longer are answered right away, and the operation goes on.
	ops := operation.New(time.Second)
	overlays := overlay.NewService(db, nodes)
	datastores := datastore.NewStore(db, nodes, overlays)
	networks := network.NewService(db, nodes, plugins, overlays, datastores)
	tags := tag.NewStore(db)
	accessService := access.NewService(db)
	sightings := player.NewSightings(db, conf)
	usageStore := usage.NewStore(db, nodes, conf, sightings)
	copies := backup.NewCopies(db, nodes, nil)
	jobs := backup.NewJobs(nodes, datastores, copies)
	tasks := schedule.NewService(db, nodes, tags, networks, accessService, map[string]schedule.Kind{
		backup.TaskKind: jobs, policy.TaskKind: policy.New(nodes, networks, usageStore, jobs, plugins, conf),
	}, moves.Busy)
	if err := tasks.Start(ctx); err != nil {
		return err
	}
	notifications := notify.New(db, logStore, conf, notify.Options{})
	go notifications.Run(ctx)
	workflows := workflow.NewService(db, workflow.Deps{
		Nodes: nodes, Targets: tasks, Networks: networks, Players: player.NewService(nodes, networks, geyser), Usage: usageStore,
		Backups: jobs, Plugins: plugins, Notify: notifications, Logs: logStore, Names: names, Access: accessService, Config: conf,
	})
	usageStore.Watch(workflows)
	if err := workflows.Start(ctx); err != nil {
		return err
	}
	updates := update.New(nodes, conf, update.Options{DataDir: cfg.dataDir})
	go updates.Run(ctx)
	go usageStore.Run(ctx)
	go overlays.Run(ctx)
	fileSets := fileset.NewService(db, nodes, networks, tags, datastores, moves)
	go fileSets.Run(ctx)
	// restarted is closed when an administrator restarts the master. Moves would be cut off.
	restarted, once := make(chan struct{}), sync.Once{}
	var restart func() error
	if cfg.restartCode != 0 {
		restart = func() error {
			err := cmp.Or(moves.CheckIdle(), ops.CheckIdle())
			if err == nil {
				once.Do(func() { close(restarted) })
			}
			return err
		}
	}
	// Requests in flight end when the master stops, so that streams such as the warnings
	// the panel follows don't hold it up until the timeout.
	requests, endRequests := context.WithCancel(context.Background())
	defer endRequests()
	services := Services{
		Users: users, Access: accessService, Settings: conf, Nodes: nodes, Networks: networks, Overlay: overlays,
		Plugins: plugins, GeyserMC: geyser, Mojang: mojang.New(mojang.API, mojang.SessionServer, mojang.Textures), Modpacks: modpack.NewService(db, nodes, modrinthClient), Templates: template.NewService(db, plugins), FileSets: fileSets,
		Datastores: datastore.NewService(datastores, nodes, networks),
		Tasks:      tasks, Workflows: workflows, Copies: copies, Logs: logStore, Notify: notifications, Updates: updates, Usage: usageStore, Sightings: sightings, Tags: tags, Preferences: preference.NewStore(db), Operations: ops, Moves: moves, Restart: restart,
		HSTS: cfg.tlsCert != "" || panelCert != nil && panelCert.Trusted(),
	}
	go keepTidy(ctx, services.Tidy)
	httpServer := &http.Server{
		BaseContext:       func(net.Listener) context.Context { return requests },
		Handler:           proxies.Handler(Handler(services)),
		ReadHeaderTimeout: 10 * time.Second,
		// Requests themselves have no time limit, as uploads and streams last long.
		IdleTimeout: 2 * time.Minute,
		ErrorLog:    logging.ServerErrors(slog.Default()),
	}
	httpServer.RegisterOnShutdown(endRequests)
	if panelCert != nil {
		httpServer.TLSConfig = panelCert.TLSConfig()
		go panelCert.Run(ctx, conf.Master().PanelAddr)
	}

	errc := make(chan error, 2)
	go func() { errc <- grpcServer.Serve(enrollListener) }()
	go func() {
		switch {
		case cfg.tlsCert != "":
			errc <- httpServer.ServeTLS(panelListener, cfg.tlsCert, cfg.tlsKey)
		case panelCert != nil:
			errc <- httpServer.ServeTLS(panelListener, "", "")
		default:
			errc <- httpServer.Serve(panelListener)
		}
	}()
	slog.Info("Master started", "version", buildinfo.Version, "panel", conf.Master().PanelAddr,
		"enrollment", cfg.enrollAddr, "public_enrollment", conf.EnrollAddr(), "ca_fingerprint", pki.Fingerprint(ca.Cert))

	restarting := false
	select {
	case err = <-errc:
	case <-ctx.Done():
	case <-restarted:
		restarting = true
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	grpcServer.GracefulStop()
	err = errors.Join(err, httpServer.Shutdown(shutdownCtx))
	switch {
	case err != nil:
		slog.Error("Master stopped", "err", err)
	case restarting:
		slog.Info("Master stopped to restart")
	default:
		slog.Info("Master stopped")
	}
	if restarting {
		return RestartExit(cfg.restartCode)
	}
	return err
}

// Services are what the master serves over HTTP.
type Services struct {
	Users    *auth.Service
	Access   *access.Service
	Settings *settings.Service
	Nodes    *node.Service
	Networks *network.Service
	Overlay  *overlay.Service
	Plugins  *plugin.Service
	GeyserMC *geysermc.Client
	// Mojang knows the skins of players.
	Mojang    *mojang.Client
	Modpacks  *modpack.Service
	Templates *template.Service
	FileSets  *fileset.Service
	// Datastores are the databases of networks.
	Datastores *datastore.Service
	Tasks      *schedule.Service
	Workflows  *workflow.Service
	Copies     *backup.Copies
	Logs       *logs.Store
	Notify     *notify.Service
	Updates    *update.Service
	Usage      *usage.Store
	// Sightings are where and when players were online.
	Sightings *player.Sightings
	Tags      *tag.Store
	// Preferences are what each user chose for the panel: the layout of the overview, pinned servers and settings.
	Preferences *preference.Store
	// Operations are the long actions in progress.
	Operations *operation.Operations
	Moves      *server.Moves
	// Restart restarts the master, if its service manager starts it again; otherwise nil.
	Restart func() error
	// HSTS makes browsers use HTTPS only, if the master serves a certificate they trust.
	HSTS bool
}

// Handler returns everything the master serves over HTTP: the panel, the sign-in and the
// API, which needs a session and loads the user's permissions for every request.
func Handler(s Services) http.Handler {
	authHandler := auth.NewHandler(s.Users, s.Settings, func(ctx context.Context, userID int64) (bool, error) {
		required := s.Settings.Get().RequireMFA
		if required.All || len(required.Groups) == 0 {
			return required.All, nil
		}
		return s.Access.InGroups(ctx, userID, required.Groups)
	})
	api := API(s)
	// The routes of the user's own account and preferences need no permission.
	authHandler.Register(api)
	preference.NewHandler(s.Preferences).Register(api)

	mux := http.NewServeMux()
	authHandler.RegisterPublic(mux)
	workflow.NewHandler(s.Workflows).RegisterPublic(mux)
	settings.NewHandler(s.Settings, s.Restart).RegisterPublic(mux)
	mux.Handle("/api/", authHandler.Require(s.Access.Middleware(api)))
	mux.Handle("/", web.Handler())
	// Webhooks are called with a token of their own, not with a session, from anywhere.
	origins := http.NewCrossOriginProtection()
	origins.AddInsecureBypassPattern("POST " + workflow.HookPath + "{token}")
	return securityHeaders(origins.Handler(mux), s.HSTS)
}

// API returns the routes of the API that act for a user, each with the permission it needs.
// Requests that change something are logged.
func API(s Services) *http.ServeMux {
	api := http.NewServeMux()
	m := access.NewMux(api, s.Moves.Guard, s.Logs.Audit())
	access.NewHandler(s.Access, s.Users, s.Settings).Register(m)
	settings.NewHandler(s.Settings, s.Restart).Register(m)
	logs.NewHandler(s.Logs).Register(m)
	notify.NewHandler(s.Notify, s.Tidy).Register(m)
	terminal.NewHandler(s.Nodes, s.Settings, s.Logs, s.Moves.Check).Register(m)
	node.NewHandler(s.Nodes, s.Networks, s.Overlay, s.Operations, s.FileSets, s.Tidy).Register(m)
	overlay.NewHandler(s.Overlay, s.Networks, s.Operations).Register(m)
	server.NewHandler(s.Nodes, s.Networks, s.Tags, s.Plugins, s.Modpacks, s.Operations, s.Moves, s.FileSets, s.Settings, s.Tasks, s.Access, s.Usage, s.Tags, s.Preferences, s.Modpacks, s.Plugins, s.Sightings, s.Copies, s.Workflows, s.Notify, s.FileSets).Register(m)
	operation.NewHandler(s.Operations).Register(m)
	network.NewHandler(s.Networks, s.Operations, s.FileSets, s.Tidy).Register(m)
	player.NewHandler(player.NewService(s.Nodes, s.Networks, s.GeyserMC), s.Sightings, player.NewFaces(s.Mojang, s.GeyserMC), s.Operations).Register(m)
	files.NewHandler(s.Nodes, s.Operations).Register(m)
	properties.NewHandler(s.Nodes).Register(m)
	plugin.NewHandler(s.Plugins, s.Operations, s.Networks).Register(m)
	modpack.NewHandler(s.Modpacks, s.Plugins, s.Operations).Register(m)
	template.NewHandler(s.Templates).Register(m)
	fileset.NewHandler(s.FileSets, s.Operations).Register(m)
	datastore.NewHandler(s.Datastores, s.Operations, s.Tidy).Register(m)
	backup.NewHandler(s.Nodes, s.Networks, s.Operations, s.Moves.Check, s.Copies).Register(m)
	schedule.NewHandler(s.Tasks, backup.TaskKind, access.BackupJobsView, access.BackupJobsManage).Register(m, "/api/backup-jobs")
	schedule.NewHandler(s.Tasks, policy.TaskKind, access.PoliciesView, access.PoliciesManage).Register(m, "/api/policies")
	workflow.NewHandler(s.Workflows).Register(m)
	update.NewHandler(s.Updates).Register(m)
	usage.NewHandler(s.Usage).Register(m)
	return api
}

func securityHeaders(next http.Handler, hsts bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		// Scripts are restricted to the bundle; the UI libraries inject <style> elements at runtime.
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		// Behind a proxy, the proxy decides.
		if hsts && r.TLS != nil {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

func (c config) publicEnrollAddr() (string, error) {
	if c.publicAddr != "" {
		return c.publicAddr, nil
	}
	_, port, err := net.SplitHostPort(c.enrollAddr)
	if err != nil {
		return "", err
	}
	host, err := os.Hostname()
	return net.JoinHostPort(host, port), err
}
