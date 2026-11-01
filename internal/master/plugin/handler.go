package plugin

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/modrinth"
	"github.com/QwikByte/noryx/internal/master/operation"
	"github.com/QwikByte/noryx/internal/master/tag"
)

const (
	queryTimeout = 30 * time.Second
	// installTimeout covers downloading the files, each at 256 KiB/s or faster
	// (modrinth.DownloadTimeout), and sending them to all servers.
	installTimeout = time.Hour
	maxServers     = 100
	// everywhereTimeout covers updating or removing a project on up to maxEverywhere servers;
	// restarting them afterwards takes as long as they need.
	everywhereTimeout = 30 * time.Minute
	maxEverywhere     = 500
	// maxBatch is the most game servers of a network that restart at a time, as for rolling restarts.
	maxBatch = 50
)

var gameVersion = regexp.MustCompile(`^[A-Za-z0-9._-]{0,32}$`)

// Restarter restarts servers so that they load their plugins: the game servers of a network a
// few at a time, so that it stays open, the others at once. It returns the error of each.
type Restarter interface {
	RestartServers(ctx context.Context, servers []tag.Server, batch int) []error
}

type Handler struct {
	svc      *Service
	ops      *operation.Operations
	networks Restarter
}

func NewHandler(svc *Service, ops *operation.Operations, networks Restarter) *Handler {
	return &Handler{svc: svc, ops: ops, networks: networks}
}

// Register adds the routes. Searching Modrinth needs no permission; what servers have installed
// only shows the servers the user may see, and actions on many servers at once check the
// permission for each of them.
func (h *Handler) Register(mux access.Mux) {
	mux.Handle("GET /api/plugins/search", access.SignedIn, h.search)
	mux.Handle("GET /api/plugins/game-versions", access.SignedIn, h.gameVersions)
	mux.Handle("GET /api/plugins/projects/{project}/versions", access.SignedIn, h.versions)
	mux.Handle("GET /api/plugins/projects/{project}/changes", access.SignedIn, h.changes)
	mux.Handle("GET /api/plugins/icons/{project}/{file}", access.SignedIn, h.icon)
	mux.Handle("GET /api/plugins/installed", access.SignedIn, h.installed)
	mux.Handle("POST /api/plugins/install", access.SignedIn, h.install)
	mux.Handle("POST /api/plugins/update", access.SignedIn, h.update)
	mux.Handle("POST /api/plugins/remove", access.SignedIn, h.removeEverywhere)
	const base = "/api/nodes/{node}/servers/{id}/plugins"
	mux.Handle("GET "+base, access.OnServer(access.ServersView), h.list)
	mux.Handle("PUT "+base+"/{file}", access.OnServer(access.Plugins), h.upload)
	mux.Handle("DELETE "+base+"/{file}", access.OnServer(access.Plugins), h.remove)
	mux.Handle("POST "+base+"/{file}/enable", access.OnServer(access.Plugins), h.enable(true))
	mux.Handle("POST "+base+"/{file}/disable", access.OnServer(access.Plugins), h.enable(false))
	mux.Handle("PUT "+base+"/pins/{project}", access.OnServer(access.Plugins), h.pin(true))
	mux.Handle("DELETE "+base+"/pins/{project}", access.OnServer(access.Plugins), h.pin(false))
	mux.Handle("PUT "+base+"/channels/{project}", access.OnServer(access.Plugins), h.track)
}

type hit struct {
	Project
	Description string    `json:"description"`
	Author      string    `json:"author"`
	Downloads   int64     `json:"downloads"`
	Follows     int64     `json:"follows"`
	Updated     time.Time `json:"updated"`
	// ClientSide tells whether players need the project too: required, optional, unsupported or unknown.
	ClientSide string   `json:"clientSide"`
	Loaders    []string `json:"loaders"`
	Categories []string `json:"categories"` // the main ones, e.g. economy
}

// search finds plugins or mods, or both, for a server type and Minecraft version, both
// optional, in categories, sorted and limited to those players don't need; on Modrinth, or
// plugins on Hangar with the source hangar.
func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	q := r.URL.Query()
	s := modrinth.Search{
		Query: q.Get("query"), Kind: q.Get("kind"), Categories: q["category"], Sort: q.Get("sort"), ServerOnly: q.Get("serverOnly") == "true",
	}
	s.Offset, _ = strconv.Atoi(q.Get("offset"))
	t := target{gameVersion: q.Get("version")}
	var err error
	switch {
	case !s.Valid() || !gameVersion.MatchString(t.gameVersion) || s.Offset < 0 || s.Offset > 10_000:
		err = httpapi.Errorf(http.StatusBadRequest, "invalid search")
	case q.Get("type") != "":
		t, err = h.svc.target(ctx, noryxv1.ParseServerType(q.Get("type")), t.gameVersion)
	case t.gameVersion == "LATEST":
		t.gameVersion, err = h.svc.catalogue.modrinth.LatestRelease(ctx)
	}
	var res modrinth.SearchResult
	if err == nil {
		s.Loaders, s.GameVersion = t.loaders, t.gameVersion
		if len(s.Loaders) == 0 {
			s.Loaders = modrinth.AllLoaders(s.Kind)
		}
		if q.Get("source") == "hangar" {
			res, err = h.svc.catalogue.hangar.Search(ctx, s)
		} else {
			res, err = h.svc.catalogue.modrinth.Search(ctx, s)
		}
	}
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	hits := make([]hit, 0, len(res.Hits))
	loader := func(c string) bool { return slices.Contains(s.Loaders, c) }
	for _, m := range res.Hits {
		project := h.svc.Describe(modrinth.Project{ID: m.ProjectID, Slug: m.Slug, Title: m.Title, IconURL: m.IconURL})
		// The categories include the loaders; only those searched for are of interest, so
		// that a project for plugins and mods only shows as either. Both are lists in the
		// JSON, also when Modrinth leaves them out.
		loaders := slices.DeleteFunc(append([]string{}, m.Categories...), func(c string) bool { return !loader(c) })
		categories := slices.DeleteFunc(append([]string{}, m.DisplayCategories...), loader)
		hits = append(hits, hit{project, m.Description, m.Author, m.Downloads, m.Follows, m.Updated, m.ClientSide, loaders, categories})
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"hits": hits, "total": res.Total})
}

// gameVersions lists the releases of Minecraft, the newest first.
func (h *Handler) gameVersions(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	releases, err := h.svc.catalogue.modrinth.Releases(ctx)
	write(w, r, http.StatusOK, releases, err)
}

// versions lists the versions of a project for a server type and Minecraft version.
func (h *Handler) versions(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	q := r.URL.Query()
	if !gameVersion.MatchString(q.Get("version")) {
		httpapi.WriteError(w, r, httpapi.Errorf(http.StatusBadRequest, "invalid Minecraft version"))
		return
	}
	versions, err := h.svc.Versions(ctx, r.PathValue("project"), noryxv1.ParseServerType(q.Get("type")), q.Get("version"))
	write(w, r, http.StatusOK, versions, err)
}

// changes tells what changed in the versions of a project for a server type and Minecraft
// version, after the version from up to the version to.
func (h *Handler) changes(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	q := r.URL.Query()
	from, to := q.Get("from"), q.Get("to")
	if !gameVersion.MatchString(q.Get("version")) || from != "" && !ValidProjectID(from) || to != "" && !ValidProjectID(to) {
		httpapi.WriteError(w, r, httpapi.Errorf(http.StatusBadRequest, "invalid version"))
		return
	}
	changes, err := h.svc.Changes(ctx, r.PathValue("project"), noryxv1.ParseServerType(q.Get("type")), q.Get("version"), from, to)
	write(w, r, http.StatusOK, changes, err)
}

// icon serves a project icon from Modrinth's CDN, so the browser doesn't contact Modrinth.
func (h *Handler) icon(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	icon := h.svc.catalogue.modrinth.Icon
	name := r.PathValue("project") + "/" + r.PathValue("file")
	if r.PathValue("project")+"/" == hangarIcons {
		icon, name = h.svc.catalogue.hangar.Icon, r.PathValue("file")
	}
	data, contentType, err := icon(ctx, name)
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	_, _ = w.Write(data) //nolint:gosec // an image of a fixed type, sandboxed, see above
}

// installed tells what the servers the user may see have installed, by project.
func (h *Handler) installed(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	everywhere, err := h.svc.Everywhere(ctx, access.From(r.Context()))
	write(w, r, http.StatusOK, everywhere, err)
}

// many are the servers of an action on many servers, and whether the running ones whose
// plugins changed restart afterwards: the game servers of a network Batch at a time.
type many struct {
	Servers []Ref `json:"servers"`
	Restart bool  `json:"restart"`
	Batch   int   `json:"batch"`
}

func (m *many) check(limit int) error {
	switch {
	case len(m.Servers) == 0 || len(m.Servers) > limit || len(slices.Compact(slices.SortedFunc(slices.Values(m.Servers), compareRefs))) != len(m.Servers):
		return httpapi.Errorf(http.StatusBadRequest, "Choose up to %d different servers.", limit)
	case m.Restart && m.Batch == 0:
		m.Batch = 1
	case m.Restart && (m.Batch < 1 || m.Batch > maxBatch):
		return httpapi.Errorf(http.StatusBadRequest, "Restart from 1 to %d servers of a network at a time.", maxBatch)
	}
	return nil
}

// action is an action on many servers that runs as an operation.
type action struct {
	kind, subject string
	timeout       time.Duration
	// partly leaves out the servers whose plugins the user may not manage and tells so, rather
	// than refusing the request.
	partly bool
	run    func(ctx context.Context, servers []Ref) []Result
}

// run runs an action on the servers of a request as an operation, and restarts the running
// servers whose plugins changed if asked, which then can't be cancelled.
func (h *Handler) run(w http.ResponseWriter, r *http.Request, m many, limit int, a action) {
	grants := access.From(r.Context())
	allowed := slices.DeleteFunc(slices.Clone(m.Servers), func(s Ref) bool { return !grants.On(access.Plugins, s.NodeID, s.ServerID) })
	err := m.check(limit)
	switch {
	case err != nil:
	case len(allowed) == 0 || !a.partly && len(allowed) < len(m.Servers):
		err = access.Denied(access.Plugins)
	case m.Restart && !onAll(grants, access.ServersRestart, allowed):
		err = access.Denied(access.ServersRestart)
	}
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	logging.Note(r.Context(), slog.Int("servers", len(m.Servers)), slog.Bool("restart", m.Restart))
	spec := operation.Spec{
		Kind: a.kind, Subject: a.subject, Steps: []string{"plugins"}, Status: http.StatusOK, Timeout: a.timeout, Category: logging.Plugins,
		Visible: func(g access.Grants) bool { return onAll(g, access.ServersView, m.Servers) },
		Cancel: func(_ *http.Request, g access.Grants) (access.Permission, bool) {
			if !onAll(g, access.Plugins, allowed) {
				return access.Plugins, false
			}
			return access.ServersRestart, !m.Restart || onAll(g, access.ServersRestart, allowed)
		},
	}
	if m.Restart {
		spec.Steps = append(spec.Steps, "restart")
	}
	if len(m.Servers) == 1 {
		spec.NodeID, spec.ServerID = m.Servers[0].NodeID, m.Servers[0].ServerID
	}
	h.ops.Run(w, r, spec, func(ctx context.Context) (any, error) {
		results := a.run(ctx, allowed)
		denied := httpapi.Message(access.Denied(access.Plugins))
		for _, ref := range m.Servers {
			if !slices.Contains(allowed, ref) {
				results = append(results, Result{Ref: ref, Installed: []Installed{}, Error: denied})
			}
		}
		if !m.Restart || !slices.ContainsFunc(results, func(r Result) bool { return r.Restart }) {
			return map[string]any{"results": results}, nil
		}
		if err := operation.Keep(ctx); err != nil {
			return map[string]any{"results": results}, err // nothing restarts; the results tell which servers need it
		}
		operation.Step(ctx, "restart")
		h.restart(ctx, results, m.Batch)
		return map[string]any{"results": results}, nil
	})
}

// restart restarts the servers of results that run without what changed.
func (h *Handler) restart(ctx context.Context, results []Result, batch int) {
	var servers []tag.Server
	var indexes []int
	for i, r := range results {
		if r.Restart {
			servers, indexes = append(servers, tag.Server(r.Ref)), append(indexes, i)
		}
	}
	for j, err := range h.networks.RestartServers(ctx, servers, batch) {
		r := &results[indexes[j]]
		r.Restart, r.Restarted = err != nil, err == nil
		if err != nil {
			r.Error = strings.TrimSpace(r.Error + " " + fmt.Sprintf("It couldn't restart: %s", httpapi.Message(err)))
		}
	}
}

func (h *Handler) install(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Projects []string `json:"projects"`
		// Versions are the IDs of the versions chosen for projects, by project ID.
		Versions map[string]string `json:"versions"`
		// Channels are the channels the servers take projects in, by project ID; releases if not set.
		Channels map[string]string `json:"channels"`
		many
	}
	if err := httpapi.ReadJSON(w, r, &req); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	badVersion := func(project string) bool {
		return !slices.Contains(req.Projects, project) || !ValidProjectID(req.Versions[project])
	}
	switch err := cmp.Or(checkProjects(req.Projects), checkChannels(req.Projects, req.Channels)); {
	case err != nil:
		httpapi.WriteError(w, r, err)
		return
	case slices.ContainsFunc(slices.Collect(maps.Keys(req.Versions)), badVersion):
		httpapi.WriteError(w, r, httpapi.Errorf(http.StatusBadRequest, "Choose versions only for the projects to install."))
		return
	}
	h.run(w, r, req.many, maxServers, action{
		kind: "plugins.install", subject: strconv.Itoa(len(req.Servers)), timeout: installTimeout,
		run: func(ctx context.Context, servers []Ref) []Result {
			return h.svc.Install(ctx, req.Projects, req.Versions, req.Channels, servers)
		},
	})
}

// update updates all plugins and mods of servers, or one project, to the newest suitable
// version of the channel each server takes them in; see Service.Update.
func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	var req struct {
		// Project, if set, is the only project to update.
		Project string `json:"project"`
		many
	}
	if err := httpapi.ReadJSON(w, r, &req); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	var projects []string
	subject := strconv.Itoa(len(req.Servers))
	if req.Project != "" {
		if !ValidProjectID(req.Project) {
			httpapi.WriteError(w, r, httpapi.Errorf(http.StatusBadRequest, "invalid project ID"))
			return
		}
		projects, subject = []string{req.Project}, h.title(r.Context(), req.Project)
	}
	h.run(w, r, req.many, maxEverywhere, action{
		kind: "plugins.update", subject: subject, timeout: everywhereTimeout, partly: true,
		run: func(ctx context.Context, servers []Ref) []Result { return h.svc.Update(ctx, servers, projects) },
	})
}

// removeEverywhere removes a project from servers, also where it is turned off.
func (h *Handler) removeEverywhere(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Project string `json:"project"`
		many
	}
	if err := httpapi.ReadJSON(w, r, &req); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	if !ValidProjectID(req.Project) {
		httpapi.WriteError(w, r, httpapi.Errorf(http.StatusBadRequest, "invalid project ID"))
		return
	}
	h.run(w, r, req.many, maxEverywhere, action{
		kind: "plugins.remove", subject: h.title(r.Context(), req.Project), timeout: everywhereTimeout, partly: true,
		run: func(ctx context.Context, servers []Ref) []Result {
			return h.svc.RemoveProject(ctx, req.Project, servers)
		},
	})
}

// title returns the title of a project to name an operation by, or its ID.
func (h *Handler) title(ctx context.Context, project string) string {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	return (&installation{Service: h.svc}).title(ctx, project)
}

// checkProjects checks the Modrinth projects to install, before anything is looked up.
func checkProjects(projects []string) error {
	if len(projects) == 0 || len(projects) > maxProjects || slices.ContainsFunc(projects, func(id string) bool { return !ValidProjectID(id) }) {
		return httpapi.Errorf(http.StatusBadRequest, "Choose up to %d projects to install.", maxProjects)
	}
	return nil
}

func compareRefs(a, b Ref) int {
	return strings.Compare(a.NodeID+"/"+a.ServerID, b.NodeID+"/"+b.ServerID)
}

// onAll reports whether the grants allow p on all servers.
func onAll(g access.Grants, p access.Permission, servers []Ref) bool {
	return !slices.ContainsFunc(servers, func(s Ref) bool { return !g.On(p, s.NodeID, s.ServerID) })
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	l, err := h.svc.List(ctx, ref(r))
	write(w, r, http.StatusOK, l, err)
}

func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithCancel(r.Context()) // cancelling discards the partial file
	defer cancel()
	p, err := h.svc.Upload(ctx, ref(r), r.PathValue("file"), http.MaxBytesReader(w, r.Body, modrinth.MaxFileSize))
	if tooLarge := new(http.MaxBytesError); errors.As(err, &tooLarge) {
		err = httpapi.Errorf(http.StatusRequestEntityTooLarge, "Plugins can have up to %d MB.", modrinth.MaxFileSize>>20)
	}
	write(w, r, http.StatusCreated, p, err)
}

// remove deletes a plugin file, a turned-off one with ?disabled=true.
func (h *Handler) remove(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	write(w, r, http.StatusNoContent, nil, h.svc.Remove(ctx, ref(r), r.PathValue("file"), r.URL.Query().Get("disabled") == "true"))
}

// enable turns a plugin file on or off.
func (h *Handler) enable(on bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
		defer cancel()
		write(w, r, http.StatusNoContent, nil, h.svc.Enable(ctx, ref(r), r.PathValue("file"), on))
	}
}

// pin keeps a project of a server at its version, or lets updates of all plugins update it again.
func (h *Handler) pin(pinned bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
		defer cancel()
		logging.Note(r.Context(), slog.String("project", r.PathValue("project")))
		write(w, r, http.StatusNoContent, nil, h.svc.Pin(ctx, ref(r), r.PathValue("project"), pinned))
	}
}

// track sets the channel a server takes a project in.
func (h *Handler) track(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Channel string `json:"channel"`
	}
	if err := httpapi.ReadJSON(w, r, &req); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	logging.Note(r.Context(), slog.String("project", r.PathValue("project")), slog.String("channel", req.Channel))
	write(w, r, http.StatusNoContent, nil, h.svc.Track(ctx, ref(r), r.PathValue("project"), req.Channel))
}

func ref(r *http.Request) Ref { return Ref{NodeID: r.PathValue("node"), ServerID: r.PathValue("id")} }

func write(w http.ResponseWriter, r *http.Request, status int, v any, err error) {
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	httpapi.WriteJSON(w, status, v)
}
