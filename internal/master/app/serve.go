package app

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

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
	"github.com/QwikByte/noryx/internal/master/sftp"
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

// master is the running master: what it keeps, what it serves with and where it listens.
type master struct {
	cfg       config
	proxies   auth.Proxies // trusted to tell the address of clients
	db        *sql.DB
	ca        *pki.CA
	cert      *pki.Holder // of the master for its agents
	conf      *settings.Service
	panelCert *https.Server // nil if the panel has no certificate of its own
	panel     net.Listener
	enroll    net.Listener
	// sftp listens for SFTP with the host key, unless SFTP is off.
	sftp     net.Listener
	hostKey  ssh.Signer
	services Services
	// closers close what the master opened, the last first.
	closers []func() error
	// restarted is closed when an administrator restarts the master.
	restarted chan struct{}
}

func serve(ctx context.Context, cfg config) error {
	proxies, err := auth.ParseProxies(cfg.trustedProxies)
	if err != nil {
		return err
	}
	m := &master{cfg: cfg, proxies: proxies, restarted: make(chan struct{})}
	defer m.close()
	if err := m.open(ctx); err != nil {
		return err
	}
	if err := m.listen(); err != nil {
		return err
	}
	if err := m.start(ctx); err != nil {
		return err
	}
	return m.run(ctx)
}

func (m *master) close() {
	for i := len(m.closers) - 1; i >= 0; i-- {
		_ = m.closers[i]()
	}
}

// open opens what the master keeps: the database, the certificate authority with the master's certificate, the
// settings and the panel's certificate for HTTPS.
func (m *master) open(ctx context.Context) error {
	// noryx-master restore refuses to replace the database and the CA of a master that runs.
	unlock, err := lockDataDir(m.cfg.dataDir)
	if errors.Is(err, errRunning) {
		err = fmt.Errorf("another noryx-master runs with %s", m.cfg.dataDir)
	}
	if err != nil {
		return err
	}
	m.closers = append(m.closers, unlock)
	db, err := openDB(m.cfg.dataDir)
	if err != nil {
		return err
	}
	m.db = db
	m.closers = append(m.closers, db.Close)
	if m.ca, err = pki.LoadOrCreateCA(filepath.Join(m.cfg.dataDir, "pki")); err != nil {
		return err
	}
	cert, err := m.ca.MasterCertificate()
	if err != nil {
		return err
	}
	if m.cert, err = pki.NewHolder(cert); err != nil {
		return err
	}
	publicAddr, err := m.cfg.publicEnrollAddr()
	if err != nil {
		return err
	}
	desc := settings.Master{
		Version: buildinfo.Version, StartedAt: time.Now(), PanelDefaultAddr: m.cfg.httpAddr,
		EnrollListenAddr: m.cfg.enrollAddr, EnrollAddr: publicAddr, CAFingerprint: pki.Fingerprint(m.ca.Cert),
		Restartable: m.cfg.restartCode != 0,
	}
	if m.cfg.tlsCert != "" {
		desc.PanelHTTPS = https.Files
	}
	if m.conf, err = settings.Load(ctx, db, desc, m.cert); err != nil {
		return err
	}
	m.panelCert, err = m.conf.PanelHTTPS(filepath.Join(m.cfg.dataDir, "https"))
	return err
}

// listen listens at the addresses of the panel, of the enrollment of nodes and of SFTP, if it is on.
func (m *master) listen() error {
	var err error
	if m.panel, err = m.conf.ListenPanel(); err != nil {
		return err
	}
	m.closers = append(m.closers, m.panel.Close)
	if m.enroll, err = net.Listen("tcp", m.cfg.enrollAddr); err != nil {
		return err
	}
	m.closers = append(m.closers, m.enroll.Close)
	m.sftp = m.conf.ListenSFTP(func() (string, error) {
		key, err := sftp.HostKey(filepath.Join(m.cfg.dataDir, "sftp"))
		if err != nil {
			return "", err
		}
		m.hostKey = key
		return sftp.Fingerprint(key), nil
	})
	if m.sftp != nil {
		m.closers = append(m.closers, m.sftp.Close)
	}
	return nil
}

// start builds the services, sets up logging into the database and starts the work they do in the background
// until ctx ends.
func (m *master) start(ctx context.Context) error {
	db, conf := m.db, m.conf
	users := auth.NewService(db, conf)
	nodes := node.NewService(db, m.ca, m.cert, conf)
	m.closers = append(m.closers, func() error { nodes.Close(); return nil })
	names := logs.NewNames(nodes)
	logStore := logs.NewStore(db, names, conf)
	logFile, err := logging.Setup(m.cfg.log, logStore.Handler(m.cfg.log.Level))
	if err != nil {
		return err
	}
	m.closers = append(m.closers, logFile.Close)
	logStore.Start(ctx)
	m.closers = append(m.closers, func() error { logStore.Close(); return nil }) // before the log file and the database close
	go logStore.Collect(ctx, nodes)
	go m.cert.Maintain(ctx, time.Hour, m.ca.MasterCertificate)
	go nodes.MaintainCertificates(ctx, 6*time.Hour)
	go nodes.WatchPresence(ctx, 30*time.Second)
	if ok, err := users.HasUsers(ctx); err == nil && !ok {
		slog.Warn("No administrator account exists yet, create one with: noryx-master user add <username>", logging.Auth)
	}
	if desc := conf.Master(); desc.PanelAddrError != "" {
		slog.Warn("The panel can't listen at the address from the settings, so it listens at the one from the command line",
			logging.Settings, "addr", desc.PanelAddr, "err", desc.PanelAddrError)
	}
	if desc := conf.Master(); desc.SFTPError != "" {
		slog.Warn("SFTP is off, as it didn't start", logging.Settings, "addr", conf.Get().SFTPAddr, "err", desc.SFTPError)
	}

	modrinthClient := modrinth.New(modrinth.DefaultAPI, modrinth.DefaultCDN)
	geyser := geysermc.New(geysermc.DownloadAPI, geysermc.GlobalAPI, geysermc.CacheTime)
	plugins := plugin.NewService(db, nodes, modrinthClient, hangar.New(hangar.DefaultAPI, hangar.DefaultCDN), geyser)
	moves := server.NewMoves()
	// Requests whose operation takes longer are answered right away, and the operation goes on.
	ops := operation.New(time.Second)
	ops.KeepFor(conf)
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
		backup.TaskKind: jobs, backup.MasterTaskKind: backup.NewMasterJob(db, m.cfg.dataDir, copies),
		policy.TaskKind: policy.New(nodes, networks, usageStore, jobs, plugins, conf),
	}, moves.Busy)
	if err := tasks.Start(ctx); err != nil {
		return err
	}
	notifications := notify.New(db, logStore, tasks, conf, notify.Options{})
	go notifications.Run(ctx)
	workflows := workflow.NewService(db, workflow.Deps{
		Nodes: nodes, Targets: tasks, Networks: networks, Players: player.NewService(nodes, networks, geyser), Usage: usageStore,
		Backups: jobs, Plugins: plugins, Notify: notifications, Logs: logStore, Names: names, Access: accessService, Config: conf,
	})
	usageStore.Watch(workflows)
	if err := workflows.Start(ctx); err != nil {
		return err
	}
	updates := update.New(nodes, conf, update.Options{DataDir: m.cfg.dataDir, Prereleases: m.cfg.prereleases})
	go updates.Run(ctx)
	go usageStore.Run(ctx)
	go overlays.Run(ctx)
	fileSets := fileset.NewService(db, nodes, networks, tags, datastores, moves)
	go fileSets.Run(ctx)
	// Restarting would cut off moves and other operations.
	var restart func() error
	if m.cfg.restartCode != 0 {
		var once sync.Once
		restart = func() error {
			err := cmp.Or(moves.CheckIdle(), ops.CheckIdle())
			if err == nil {
				once.Do(func() { close(m.restarted) })
			}
			return err
		}
	}
	m.services = Services{
		Users: users, Access: accessService, Settings: conf, Nodes: nodes, Networks: networks, Overlay: overlays,
		Plugins: plugins, GeyserMC: geyser, Mojang: mojang.New(mojang.API, mojang.SessionServer, mojang.Textures),
		Modpacks: modpack.NewService(db, nodes, modrinthClient), Templates: template.NewService(db, plugins), FileSets: fileSets,
		Datastores: datastore.NewService(datastores, nodes, networks), Tasks: tasks, Workflows: workflows, Copies: copies,
		Logs: logStore, Notify: notifications, Updates: updates, Usage: usageStore, Sightings: sightings, Tags: tags,
		Preferences: preference.NewStore(db), Operations: ops, Moves: moves, Restart: restart,
		HSTS: m.cfg.tlsCert != "" || m.panelCert != nil && m.panelCert.Trusted(),
	}
	go keepTidy(ctx, m.services.Tidy)
	return nil
}

// loopback reports whether a listener only takes connections from this machine.
func loopback(addr net.Addr) bool {
	ap, err := netip.ParseAddrPort(addr.String())
	return err == nil && ap.Addr().IsLoopback()
}

// run serves the panel and the enrollment of nodes until ctx ends, one of them fails or an administrator restarts
// the master, and then shuts them down.
func (m *master) run(ctx context.Context) error {
	// Requests in flight end when the master stops, so that streams such as the warnings
	// the panel follows don't hold it up until the timeout.
	requests, endRequests := context.WithCancel(context.Background())
	defer endRequests()
	signIns := SignIns(m.services)
	httpServer := &http.Server{
		BaseContext:       func(net.Listener) context.Context { return requests },
		Handler:           m.proxies.Handler(handler(m.services, signIns)),
		ReadHeaderTimeout: 10 * time.Second,
		// Requests themselves have no time limit, as uploads and streams last long.
		IdleTimeout: 2 * time.Minute,
		ErrorLog:    logging.ServerErrors(slog.Default()),
	}
	httpServer.RegisterOnShutdown(endRequests)
	if m.panelCert != nil {
		httpServer.TLSConfig = m.panelCert.TLSConfig()
		go m.panelCert.Run(ctx, m.conf.Master().PanelAddr)
	}
	grpcServer := m.services.Nodes.EnrollmentServer()

	errc := make(chan error, 3)
	go func() { errc <- grpcServer.Serve(m.enroll) }()
	var sftpServer *sftp.Server
	if m.sftp != nil {
		sftpServer = SFTP(m.services, signIns, m.hostKey)
		go func() { errc <- sftpServer.Serve(m.sftp) }()
	}
	go func() {
		switch {
		case m.cfg.tlsCert != "":
			errc <- httpServer.ServeTLS(m.panel, m.cfg.tlsCert, m.cfg.tlsKey)
		case m.panelCert != nil:
			errc <- httpServer.ServeTLS(m.panel, "", "")
		default:
			errc <- httpServer.Serve(m.panel)
		}
	}()
	if m.cfg.tlsCert == "" && m.panelCert == nil && len(m.proxies) == 0 && !loopback(m.panel.Addr()) {
		slog.Warn("The panel is served over plain HTTP at an address other networks reach, where browsers send passwords "+
			"unencrypted and refuse its cookies. Turn on HTTPS under Settings → General, or serve it through a reverse proxy "+
			"with HTTPS named with --trusted-proxy.", logging.System, "panel", m.panel.Addr().String())
	}
	desc := m.conf.Master()
	slog.Info("Master started", "version", buildinfo.Version, "panel", desc.PanelAddr, "enrollment", m.cfg.enrollAddr,
		"public_enrollment", m.conf.EnrollAddr(), "ca_fingerprint", pki.Fingerprint(m.ca.Cert), "sftp", desc.SFTPAddr, "sftp_host_key", desc.SFTPHostKey)

	var err error
	restarting := false
	select {
	case err = <-errc:
	case <-ctx.Done():
	case <-m.restarted:
		restarting = true
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	grpcServer.GracefulStop()
	if sftpServer != nil {
		err = errors.Join(err, sftpServer.Close())
	}
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
		return RestartExit(m.cfg.restartCode)
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

// SignIns returns what signs users in to the panel and to SFTP, so that the attempts of a
// client count together.
func SignIns(s Services) *auth.Handler {
	return auth.NewHandler(s.Users, s.Settings, func(ctx context.Context, userID int64) (bool, error) {
		required := s.Settings.Get().RequireMFA
		if required.All || len(required.Groups) == 0 {
			return required.All, nil
		}
		return s.Access.InGroups(ctx, userID, required.Groups)
	})
}

// SFTP returns the server of SFTP, which identifies with key and signs users in with signIns.
func SFTP(s Services, signIns *auth.Handler, key ssh.Signer) *sftp.Server {
	return sftp.NewServer(key, sftp.Deps{Nodes: s.Nodes, Users: signIns, Access: s.Access, Moving: s.Moves.Check})
}

// Handler returns everything the master serves over HTTP: the panel, the sign-in and the
// API, which needs a session and loads the user's permissions for every request.
func Handler(s Services) http.Handler { return handler(s, SignIns(s)) }

func handler(s Services, authHandler *auth.Handler) http.Handler {
	api := API(s)
	// The routes of the user's own account and preferences need no permission.
	authHandler.Register(api)
	preference.NewHandler(s.Preferences).Register(api)

	mux := http.NewServeMux()
	authHandler.RegisterPublic(mux)
	workflow.NewHandler(s.Workflows).RegisterPublic(mux)
	settings.NewHandler(s.Settings, s.Users, s.Restart).RegisterPublic(mux)
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
	settings.NewHandler(s.Settings, s.Users, s.Restart).Register(m)
	logs.NewHandler(s.Logs).Register(m)
	notify.NewHandler(s.Notify, s.Tidy).Register(m)
	terminal.NewHandler(s.Nodes, s.Settings, s.Logs, s.Moves.Check).Register(m)
	node.NewHandler(s.Nodes, s.Networks, s.Overlay, s.Operations, s.FileSets, s.Tidy).Register(m)
	overlay.NewHandler(s.Overlay, s.Networks, s.Operations).Register(m)
	server.NewHandler(server.Deps{
		Nodes: s.Nodes, Networks: s.Networks, Tags: s.Tags, Plugins: s.Plugins, Modpacks: s.Modpacks, Operations: s.Operations,
		Moves: s.Moves, FileSets: s.FileSets, Config: s.Settings,
		Tasks: s.Tasks, Access: s.Access, Usage: s.Usage, Preferences: s.Preferences, Sightings: s.Sightings, Copies: s.Copies,
		Workflows: s.Workflows, Notify: s.Notify,
	}).Register(m)
	operation.NewHandler(s.Operations).Register(m)
	network.NewHandler(s.Networks, s.Operations, s.FileSets, s.Tidy).Register(m)
	player.NewHandler(player.NewService(s.Nodes, s.Networks, s.GeyserMC), s.Sightings, player.NewFaces(s.Mojang, s.GeyserMC), s.Operations).Register(m)
	files.NewHandler(files.NewService(s.Nodes), s.Operations).Register(m)
	sftp.NewHandler(s.Settings, s.Nodes).Register(m)
	properties.NewHandler(s.Nodes).Register(m)
	plugin.NewHandler(s.Plugins, s.Operations, s.Networks).Register(m)
	modpack.NewHandler(s.Modpacks, s.Plugins, s.Operations).Register(m)
	template.NewHandler(s.Templates).Register(m)
	fileset.NewHandler(s.FileSets, s.Operations).Register(m)
	datastore.NewHandler(s.Datastores, s.Operations, s.Tidy).Register(m)
	backup.NewHandler(s.Nodes, s.Networks, s.Operations, s.Moves.Check, s.Copies).Register(m)
	schedule.NewHandler(s.Tasks, backup.TaskKind, access.Everywhere(access.BackupJobsView), access.Everywhere(access.BackupJobsManage)).
		Register(m, "/api/backup-jobs")
	// Its archives hold the CA's private key, with which anyone controls the agents.
	schedule.NewHandler(s.Tasks, backup.MasterTaskKind, access.AdminsSignedIn, access.AdminsSignedIn).Register(m, "/api/master-backups")
	schedule.NewHandler(s.Tasks, policy.TaskKind, access.Everywhere(access.PoliciesView), access.Everywhere(access.PoliciesManage)).
		Register(m, "/api/policies")
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
