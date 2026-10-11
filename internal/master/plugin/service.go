// Package plugin installs plugins and mods from Modrinth, and plugins from Hangar, on
// servers, and lists, updates, turns off and removes the installed ones, also on many servers
// at once. The master downloads each file once, checks its hash and streams it to the agents,
// which write it into the plugin folder of the server.
package plugin

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/geysermc"
	"github.com/QwikByte/noryx/internal/master/hangar"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/modrinth"
	"github.com/QwikByte/noryx/internal/master/node"
	"github.com/QwikByte/noryx/internal/master/operation"
)

const (
	chunkSize = 256 << 10
	// maxProjects limits the projects installed at once, including required ones.
	maxProjects = 50
	iconRoute   = "/api/plugins/icons/"
	// hangarIcons is the folder of Hangar's icons in iconRoute, beside Modrinth's projects.
	hangarIcons = "hangar/"
)

var errVanilla = httpapi.Errorf(http.StatusConflict, "Vanilla servers can't load plugins or mods.")

// Nodes provides the nodes and connections to their agents.
type Nodes interface {
	List(ctx context.Context) ([]node.Node, error)
	Conn(ctx context.Context, nodeID string) (grpc.ClientConnInterface, error)
}

// Ref points to a server on a node.
type Ref struct {
	NodeID   string `json:"nodeId"`
	ServerID string `json:"serverId"`
}

// Project is a Modrinth project as the panel shows it.
type Project struct {
	ID    string `json:"id"`
	Slug  string `json:"slug"`
	Title string `json:"title"`
	// Icon is the path of the project icon in the panel, which proxies Modrinth's CDN.
	Icon string `json:"icon,omitempty"`
}

type Service struct {
	nodes     Nodes
	catalogue catalogue
	pins      pins
}

func NewService(db *sql.DB, nodes Nodes, modrinthClient *modrinth.Client, hangarClient *hangar.Client, geysermcClient *geysermc.Client) *Service {
	return &Service{nodes: nodes, catalogue: catalogue{modrinthClient, hangarClient, geysermcClient}, pins: pins{db}}
}

// Projects looks up projects on Modrinth and Hangar; unknown ones are left out.
func (s *Service) Projects(ctx context.Context, ids []string) ([]modrinth.Project, error) {
	return s.catalogue.Projects(ctx, ids)
}

// Describe returns a project as the panel shows it.
func (s *Service) Describe(p modrinth.Project) Project {
	return Project{ID: p.ID, Slug: p.Slug, Title: p.Title, Icon: s.icon(p.IconURL)}
}

func (s *Service) icon(url string) string {
	if name := s.catalogue.modrinth.IconName(url); name != "" {
		return iconRoute + name
	}
	if name := s.catalogue.hangar.IconName(url); name != "" {
		return iconRoute + hangarIcons + name
	}
	return ""
}

// Plugin is a plugin file on a server, with its Modrinth project if it comes from there.
type Plugin struct {
	FileName string   `json:"fileName"`
	Size     int64    `json:"size"`
	Project  *Project `json:"project,omitempty"`
	Version  string   `json:"version,omitempty"`
	// VersionID is the ID of the version on Modrinth.
	VersionID string `json:"versionId,omitempty"`
	// Channel is beta or alpha for a version that isn't a release.
	Channel string `json:"channel,omitempty"`
	// Update is a newer version of the project for the server in the channel the server takes
	// it in, UpdateID its ID and UpdateChannel beta or alpha if it isn't a release.
	Update        string `json:"update,omitempty"`
	UpdateID      string `json:"updateId,omitempty"`
	UpdateChannel string `json:"updateChannel,omitempty"`
	// Pinned tells that the server keeps the project at its version: updates of all its
	// plugins leave it out.
	Pinned bool `json:"pinned,omitempty"`
	// Tracks is the channel the server takes the project in, beta or alpha, if not only releases.
	Tracks string `json:"tracks,omitempty"`
	// Disabled tells that the file is turned off, so that the server doesn't load it.
	Disabled bool `json:"disabled,omitempty"`
	// Settings is the folder of the plugin's settings in the server's data, e.g. plugins/LuckPerms.
	Settings string `json:"settings,omitempty"`
	// RequiredBy are the titles of the other turned-on projects of the server that require it.
	RequiredBy []string `json:"requiredBy,omitempty"`
}

type Listing struct {
	Folder  string   `json:"folder"` // plugins or mods
	Plugins []Plugin `json:"plugins"`
	// CatalogueError tells why the plugins couldn't be looked up on Modrinth.
	CatalogueError string `json:"catalogueError,omitempty"`
}

// List returns the plugins of a server, the turned-off ones too. Files from Modrinth and Hangar
// are identified by their hash.
func (s *Service) List(ctx context.Context, ref Ref) (Listing, error) {
	conn, srv, err := s.server(ctx, ref)
	if err != nil {
		return Listing{}, err
	}
	res, err := noryxv1.NewPluginServiceClient(conn).ListPlugins(ctx, &noryxv1.ListPluginsRequest{ServerId: ref.ServerID, IncludeDisabled: true})
	if err != nil {
		return Listing{}, err
	}
	k, err := s.pins.of(ctx, ref)
	if err != nil {
		return Listing{}, err
	}
	l := Listing{Folder: res.GetFolder()}
	var c catalogued
	if len(res.GetPlugins()) > 0 {
		t, err := s.target(ctx, srv.GetType(), srv.GetVersion())
		if err == nil {
			c, err = s.identify(ctx, t, res.GetPlugins(), inUse(k))
		}
		if err != nil {
			l.CatalogueError = httpapi.Message(err)
		}
	}
	l.Plugins = c.describe(res.GetFolder(), res.GetPlugins(), k)
	return l, nil
}

// catalogued is what Modrinth and Hangar know of plugin files.
type catalogued struct {
	known    map[string]modrinth.Version // the version of each file, by SHA-512
	newer    updates
	projects map[string]Project // by ID
}

// identify looks up plugin files of servers with the same target on Modrinth and Hangar, with
// their newest versions for the servers in channels, and describes their projects.
func (s *Service) identify(ctx context.Context, t target, files []*noryxv1.PluginFile, channels []string) (catalogued, error) {
	known, newer, err := s.catalogue.identify(ctx, files, t, channels)
	if err != nil {
		return catalogued{}, err
	}
	var ids []string
	for _, v := range known {
		ids = append(ids, v.ProjectID)
	}
	found, err := s.catalogue.Projects(ctx, slices.Compact(slices.Sorted(slices.Values(ids))))
	if err != nil {
		return catalogued{}, err
	}
	c := catalogued{known: known, newer: newer, projects: map[string]Project{}}
	for _, p := range found {
		c.projects[p.ID] = s.Describe(p)
	}
	return c, nil
}

// describe returns the plugins of files in a plugin folder, with the project, version and update
// of those that are known, what the server keeps of them, and the other projects that require them.
func (c catalogued) describe(folder string, files []*noryxv1.PluginFile, k kept) []Plugin {
	// needed are the titles of the turned-on projects that require each project or version.
	needed := map[string][]string{}
	for _, f := range files {
		v, ok := c.known[f.GetSha512()]
		if !ok || f.GetDisabled() {
			continue
		}
		for _, d := range v.Dependencies {
			for _, id := range []string{d.ProjectID, d.VersionID} {
				if d.Type == "required" && id != "" && id != v.ProjectID {
					needed[id] = append(needed[id], cmp.Or(c.projects[v.ProjectID].Title, f.GetFileName()))
				}
			}
		}
	}
	plugins := make([]Plugin, len(files))
	for i, f := range files {
		p := Plugin{FileName: f.GetFileName(), Size: f.GetSize(), Disabled: f.GetDisabled()}
		if noryxv1.ValidPluginFolder(f.GetSettings()) {
			p.Settings = folder + "/" + f.GetSettings()
		}
		v, known := c.known[f.GetSha512()]
		if project, ok := c.projects[v.ProjectID]; known && ok {
			p.Project, p.Version, p.VersionID, p.Channel = &project, v.VersionNumber, v.ID, preRelease(v)
			p.Pinned, p.Tracks = k.pinned[v.ProjectID], k.channels[v.ProjectID]
			if u := c.newer.of(f.GetSha512(), k.channel(v.ProjectID)); isNewer(u, v) {
				p.Update, p.UpdateID, p.UpdateChannel = u.VersionNumber, u.ID, preRelease(u)
			}
			p.RequiredBy = slices.Compact(slices.Sorted(slices.Values(slices.Concat(needed[v.ProjectID], needed[v.ID]))))
		}
		plugins[i] = p
	}
	return plugins
}

// isNewer reports whether update is a newer version than v.
func isNewer(update, v modrinth.Version) bool {
	return update.ID != "" && update.ID != v.ID && update.Published.After(v.Published)
}

// Installed is a plugin that was installed on a server.
type Installed struct {
	ProjectID string `json:"projectId"`
	FileName  string `json:"fileName"`
	Version   string `json:"version"`
	// Channel is beta or alpha for a version that isn't a release.
	Channel  string `json:"channel,omitempty"`
	written  bool   // rather than present already
	disabled bool   // into the folder of turned-off plugins
}

// loads reports whether the server loads the file once it restarts, as it is new and turned on.
func (i Installed) loads() bool { return i.written && !i.disabled }

// Result tells what an action did on a server, installing, updating or removing plugins, and
// why it failed.
type Result struct {
	Ref
	// Installed are the files of the projects installed or updated, also those present already
	// when installing; updates only tell the files they wrote.
	Installed []Installed `json:"installed"`
	// Removed are the files removed.
	Removed []string `json:"removed,omitempty"`
	// Pinned are the titles of the projects with a newer release that the server keeps at their version.
	Pinned []string `json:"pinned,omitempty"`
	Error  string   `json:"error,omitempty"`
	// Restart tells that the server runs without what changed, and Restarted that it was restarted
	// to load it.
	Restart   bool `json:"restart,omitempty"`
	Restarted bool `json:"restarted,omitempty"`
}

// each calls fn for each server, at most operation.PerNode of a node at a time, and returns their
// results: what fn put there and the error it returned. Once the operation of ctx is cancelled,
// the servers it didn't begin tell so.
func each(ctx context.Context, servers []Ref, fn func(ctx context.Context, res *Result) error) []Result {
	results := make([]Result, len(servers))
	nodes := make([]string, len(servers))
	for i, ref := range servers {
		results[i], nodes[i] = Result{Ref: ref, Installed: []Installed{}}, ref.NodeID // a list also if nothing is installed
	}
	failed := func(i int, err error) { results[i].Error = httpapi.Message(err) }
	operation.Each(ctx, nodes, func(ctx context.Context, i int) {
		if err := fn(ctx, &results[i]); err != nil {
			failed(i, err)
		}
	}, failed)
	return results
}

// Install installs on the given servers the newest suitable version of each project in the
// channel chosen for it by project ID, releases unless chosen, or the version chosen for it,
// together with the projects it requires. The servers take the projects in these channels
// from then on. Installed projects are updated, but projects that are only required are left
// as they are. A server it began gets all of them, also if the operation is cancelled.
func (s *Service) Install(ctx context.Context, projects []string, chosen, channels map[string]string, servers []Ref) []Result {
	run := &installation{Service: s, chosen: chosen, channels: map[string]string{}}
	for _, project := range projects {
		run.channels[project] = cmp.Or(channels[project], "release")
	}
	return each(ctx, servers, func(ctx context.Context, res *Result) error { return run.install(ctx, res, projects) })
}

// InstallOn installs projects on a server, e.g. those of a template on a new server, each in
// the version kept for it by project ID, if any, and in its channel like Install. A project
// whose kept version doesn't run on the server is left out rather than installed in another
// version, and the error says so.
func (s *Service) InstallOn(ctx context.Context, projects []string, kept, channels map[string]string, nodeID, serverID string) error {
	if err := checkProjects(projects); err != nil {
		return err
	}
	if err := checkChannels(projects, channels); err != nil {
		return err
	}
	ref, failed := Ref{nodeID, serverID}, []string{}
	if len(kept) > 0 {
		_, srv, err := s.server(ctx, ref)
		var t target
		if err == nil {
			t, err = s.target(ctx, srv.GetType(), srv.GetVersion())
		}
		if err != nil {
			return err
		}
		run := &installation{Service: s, chosen: kept}
		projects = slices.DeleteFunc(slices.Clone(projects), func(project string) bool {
			if kept[project] == "" {
				return false
			}
			_, err := run.pick(ctx, project, t, "release", false)
			if err != nil {
				failed = append(failed, httpapi.Message(err))
			}
			return err != nil
		})
	}
	if len(projects) > 0 {
		if r := s.Install(ctx, projects, kept, channels, []Ref{ref})[0]; r.Error != "" {
			failed = append(failed, r.Error)
		}
	}
	if len(failed) > 0 {
		return httpapi.Errorf(http.StatusConflict, "%s", strings.Join(failed, " "))
	}
	return nil
}

// installation shares lookups and downloads between the servers of one Install or Update call.
type installation struct {
	*Service
	chosen map[string]string // version IDs by project ID
	// channels are the channels chosen for the projects it installs, by project ID. Others keep
	// the channel of the server, and projects new on a server take releases.
	channels map[string]string
	// updates picks only versions of the channels of the projects it installs, as updates do,
	// rather than the newest version of a project without one.
	updates   bool
	versions  memo[[]modrinth.Version]
	downloads memo[[]byte]
	titles    memo[string]
}

// onServer is a server whose plugins an installation changes, with its plugin files.
type onServer struct {
	ref     Ref
	client  noryxv1.PluginServiceClient
	target  target
	running bool
	kept    kept
	files   []*noryxv1.PluginFile
	// known and newer are the versions of the files and their newer ones, by SHA-512.
	known   map[string]modrinth.Version
	newer   updates
	present map[string]installedFile // see present
}

// open lists the plugin files of a server, the turned-off ones too, and identifies them, with
// their newer versions in the channels of the server if updates.
func (r *installation) open(ctx context.Context, ref Ref, updates bool) (*onServer, error) {
	conn, srv, err := r.server(ctx, ref)
	if err != nil {
		return nil, err
	}
	o := &onServer{ref: ref, client: noryxv1.NewPluginServiceClient(conn), running: srv.GetState() == noryxv1.ServerState_SERVER_STATE_RUNNING}
	if o.target, err = r.target(ctx, srv.GetType(), srv.GetVersion()); err != nil {
		return nil, err
	}
	if o.kept, err = r.pins.of(ctx, ref); err != nil {
		return nil, err
	}
	var channels []string
	if updates {
		channels = inUse(o.kept)
	}
	o.files, o.known, o.newer, err = r.listed(ctx, o.client, ref.ServerID, o.target, channels)
	return o, err
}

// listed lists the plugin files of a server and identifies them for a target, with their newer
// versions in channels.
func (r *installation) listed(ctx context.Context, c noryxv1.PluginServiceClient, serverID string, t target, channels []string) (
	files []*noryxv1.PluginFile, known map[string]modrinth.Version, newer updates, err error,
) {
	res, err := c.ListPlugins(ctx, &noryxv1.ListPluginsRequest{ServerId: serverID, IncludeDisabled: true})
	if err != nil || len(res.GetPlugins()) == 0 {
		return nil, map[string]modrinth.Version{}, updates{}, err
	}
	known, newer, err = r.catalogue.identify(ctx, res.GetPlugins(), t, channels)
	return res.GetPlugins(), known, newer, err
}

// install installs projects on the server of res, and tells what it installed.
func (r *installation) install(ctx context.Context, res *Result, projects []string) error {
	o, err := r.open(ctx, res.Ref, false)
	if err == nil {
		o.present, err = r.present(ctx, o.files, o.known, slices.ContainsFunc(projects, onHangar))
	}
	if err != nil {
		return err
	}
	installed, err := r.put(ctx, o, projects)
	res.Installed = append(res.Installed, installed...)
	res.Restart = o.running && slices.ContainsFunc(installed, Installed.loads)
	return err
}

// put installs projects on a server with the projects they require, and keeps the files of the
// server's projects up to date. A project that is turned off stays off. The server takes each
// project in the channel chosen for it, a project new on it in releases, whatever an earlier
// file of it took, and others in theirs.
func (r *installation) put(ctx context.Context, o *onServer, projects []string) ([]Installed, error) {
	versions, err := r.resolve(ctx, o, projects)
	if err != nil {
		return nil, err
	}
	installed := []Installed{}
	for _, v := range versions {
		f, _ := v.File()
		old := o.present[v.ProjectID]
		if !old.is(f) { // otherwise this version is installed already
			data, err := r.downloads.get(f.URL, func() ([]byte, error) { return r.catalogue.Download(ctx, f) })
			if err != nil {
				return installed, err
			}
			header := &noryxv1.InstallPluginHeader{ServerId: o.ref.ServerID, FileName: f.Filename, Replaces: old.GetFileName(), Disabled: old.GetDisabled()}
			file, err := send(ctx, o.client, header, bytes.NewReader(data))
			if err != nil {
				return installed, err
			}
			o.present[v.ProjectID] = installedFile{file}
		}
		if channel, chosen := r.channels[v.ProjectID]; chosen || old.PluginFile == nil {
			if err := r.pins.track(ctx, o.ref, v.ProjectID, cmp.Or(channel, "release")); err != nil {
				return installed, err
			}
		}
		installed = append(installed, Installed{
			ProjectID: v.ProjectID, FileName: f.Filename, Version: v.VersionNumber, Channel: preRelease(v), written: !old.is(f), disabled: old.GetDisabled(),
		})
	}
	return installed, nil
}

// present returns the file of each project of Modrinth or Hangar among files, a turned-on one
// rather than a turned-off one. A file on both is Modrinth's, and also Hangar's with hangarToo,
// so that installing a project of Hangar replaces the file rather than adding another.
func (r *installation) present(ctx context.Context, files []*noryxv1.PluginFile, known map[string]modrinth.Version, hangarToo bool) (map[string]installedFile, error) {
	present := map[string]installedFile{}
	add := func(project string, f *noryxv1.PluginFile) {
		if old, ok := present[project]; !ok || old.GetDisabled() {
			present[project] = installedFile{f}
		}
	}
	for _, f := range files {
		v, ok := known[f.GetSha512()]
		if !ok {
			continue
		}
		add(v.ProjectID, f)
		if hangarToo && !onHangar(v.ProjectID) {
			project, err := r.catalogue.hangar.ProjectByHash(ctx, f.GetSha256())
			if err != nil {
				return present, err
			}
			if project != "" {
				add(project, f)
			}
		}
	}
	return present, nil
}

// installedFile is the file of a project on a server; the zero value is none.
type installedFile struct{ *noryxv1.PluginFile }

// is reports whether the file is the one of a version, by its hash, as many projects keep
// the name of their file in every version.
func (i installedFile) is(f modrinth.File) bool {
	return i.PluginFile != nil && (f.Hashes.SHA512 != "" && i.GetSha512() == f.Hashes.SHA512 || f.Hashes.SHA256 != "" && i.GetSha256() == f.Hashes.SHA256)
}

// resolve picks the version of each project for a server and of the projects they require.
func (r *installation) resolve(ctx context.Context, o *onServer, projects []string) ([]modrinth.Version, error) {
	var picked []modrinth.Version
	queue, seen := slices.Clone(projects), map[string]bool{}
	for ; len(queue) > 0; queue = queue[1:] {
		id := queue[0]
		_, installed := o.present[id]
		if seen[id] || installed && !slices.Contains(projects, id) {
			continue
		}
		seen[id] = true
		if len(seen) > maxProjects {
			return nil, httpapi.Errorf(http.StatusBadRequest, "Choose fewer projects; they require more than %d projects together.", maxProjects)
		}
		channel, ok := r.channels[id]
		if !ok && installed {
			channel = o.kept.channel(id)
		}
		v, err := r.pick(ctx, id, o.target, cmp.Or(channel, "release"), r.updates && slices.Contains(projects, id))
		if err != nil {
			return nil, err
		}
		for _, d := range v.Dependencies {
			if d.Type == "required" && d.ProjectID != "" {
				queue = append(queue, d.ProjectID)
			}
		}
		picked = append(picked, v)
	}
	return picked, nil
}

// pick returns the version chosen for a project if it suits the server, else the newest
// version of the project in a channel for the server, or else, unless strict, its newest
// version, e.g. a beta of a project without releases.
func (r *installation) pick(ctx context.Context, project string, t target, channel string, strict bool) (modrinth.Version, error) {
	versions, err := r.compatible(ctx, project, t)
	if err != nil {
		return modrinth.Version{}, err
	}
	i := slices.IndexFunc(versions, func(v modrinth.Version) bool { return takes(channel, v) })
	if i < 0 && !strict {
		i = 0
	}
	chosen := r.chosen[project]
	if chosen != "" {
		i = slices.IndexFunc(versions, func(v modrinth.Version) bool { return v.ID == chosen })
	}
	if i >= 0 && i < len(versions) && len(versions[i].Files) > 0 {
		return versions[i], nil
	}
	switch title := r.title(ctx, project); {
	case chosen != "":
		return modrinth.Version{}, httpapi.Errorf(http.StatusConflict, "The chosen version of %s doesn't run on %s.", title, t)
	case len(versions) > 0 && strict && channel == "beta":
		return modrinth.Version{}, httpapi.Errorf(http.StatusConflict, "%s has no release or beta for %s.", title, t)
	case len(versions) > 0 && strict && channel == "release":
		return modrinth.Version{}, httpapi.Errorf(http.StatusConflict, "%s has no release for %s.", title, t)
	default:
		return modrinth.Version{}, httpapi.Errorf(http.StatusConflict, "%s has no version for %s.", title, t)
	}
}

// title returns the title of a project, or its ID if it can't be looked up.
func (r *installation) title(ctx context.Context, project string) string {
	title, _ := r.titles.get(project, func() (string, error) {
		projects, err := r.catalogue.Projects(ctx, []string{project})
		if err != nil || len(projects) == 0 {
			return project, err
		}
		return projects[0].Title, nil
	})
	return title
}

// preRelease returns the channel of a version that isn't a release: beta or alpha.
func preRelease(v modrinth.Version) string {
	if v.VersionType == "release" {
		return ""
	}
	return v.VersionType
}

// compatible returns the versions of a project that run on a server, the newest first.
func (r *installation) compatible(ctx context.Context, project string, t target) ([]modrinth.Version, error) {
	key := project + "|" + strings.Join(t.loaders, ",") + "|" + t.gameVersion
	return r.versions.get(key, func() ([]modrinth.Version, error) {
		return r.catalogue.Versions(ctx, project, t.loaders, t.gameVersion)
	})
}

// Version is a version of a project as the panel shows it.
type Version struct {
	ID        string    `json:"id"`
	Number    string    `json:"number"`
	Channel   string    `json:"channel"` // release, beta or alpha
	Published time.Time `json:"published"`
	// GameVersions are the versions of Minecraft it supports, e.g. the one Geyser joins with.
	GameVersions []string `json:"gameVersions,omitempty"`
}

func toVersion(v modrinth.Version) Version {
	return Version{ID: v.ID, Number: v.VersionNumber, Channel: v.VersionType, Published: v.Published, GameVersions: v.GameVersions}
}

// Versions returns the versions of a project that run on servers of a type and
// Minecraft version, the newest first.
func (s *Service) Versions(ctx context.Context, project string, typ noryxv1.ServerType, gameVersion string) ([]Version, error) {
	t, err := s.target(ctx, typ, gameVersion)
	if err != nil {
		return nil, err
	}
	found, err := s.catalogue.Versions(ctx, project, t.loaders, t.gameVersion)
	versions := make([]Version, 0, len(found))
	for _, v := range found {
		if len(v.Files) > 0 {
			versions = append(versions, toVersion(v))
		}
	}
	return versions, err
}

// target is what a plugin must support to run on a server.
type target struct {
	loaders     []string
	gameVersion string // empty for proxies, which run with any version
}

func (t target) String() string {
	name := strings.ToUpper(t.loaders[0][:1]) + t.loaders[0][1:]
	return strings.TrimSpace(name + " " + t.gameVersion)
}

// target returns what plugins must support to run on servers of a type and Minecraft
// version.
func (s *Service) target(ctx context.Context, typ noryxv1.ServerType, gameVersion string) (target, error) {
	t := target{loaders: modrinth.Loaders(typ)}
	switch {
	case len(t.loaders) == 0:
		return t, errVanilla
	case typ.Proxy():
		return t, nil
	case gameVersion == "LATEST":
		var err error
		t.gameVersion, err = s.catalogue.modrinth.LatestRelease(ctx)
		return t, err
	}
	t.gameVersion = gameVersion
	return t, nil
}

// Ensure installs the newest suitable release of a project of Modrinth on a server, together
// with the projects it requires, unless the project is installed already, and turns it on if
// it is turned off.
func (s *Service) Ensure(ctx context.Context, ref Ref, project string) error {
	run := &installation{Service: s}
	present, err := run.presentOn(ctx, ref)
	if file, ok := present[project]; ok || err != nil {
		if file.GetDisabled() {
			return s.Enable(ctx, ref, file.GetFileName(), true)
		}
		return err
	}
	return run.install(ctx, &Result{Ref: ref}, []string{project})
}

// presentOn returns the file of each project of Modrinth on a server, without asking Hangar.
func (r *installation) presentOn(ctx context.Context, ref Ref) (map[string]installedFile, error) {
	conn, err := r.nodes.Conn(ctx, ref.NodeID)
	if err != nil {
		return nil, err
	}
	files, known, _, err := r.listed(ctx, noryxv1.NewPluginServiceClient(conn), ref.ServerID, target{}, nil) // no loaders, no Hangar
	if err != nil {
		return nil, err
	}
	return r.present(ctx, files, known, false)
}

// Provide installs the newest version of projects on a server, also those of GeyserMC, in the
// channel the server takes each in, releases for a new one, unless it has it, and reports
// whether it changed a file. A server loads it when it starts.
func (s *Service) Provide(ctx context.Context, ref Ref, projects []string) (bool, error) {
	res := Result{Ref: ref}
	err := (&installation{Service: s}).install(ctx, &res, projects)
	return slices.ContainsFunc(res.Installed, func(i Installed) bool { return i.written }), err
}

// Uninstall removes the file of a project of Modrinth or GeyserMC from a server, if it has one.
func (s *Service) Uninstall(ctx context.Context, ref Ref, project string) error {
	_, srv, err := s.server(ctx, ref)
	if err != nil {
		return err
	}
	if onGeyserMC(project) {
		// GeyserMC only tells its newest build, but its builds keep the name of their file.
		t, err := s.target(ctx, srv.GetType(), srv.GetVersion())
		var latest []modrinth.Version
		if err == nil {
			latest, err = s.catalogue.geysermc.Latest(ctx, project, t.loaders)
		}
		if err != nil || len(latest) == 0 {
			return err
		}
		if err := s.Remove(ctx, ref, latest[0].Files[0].Filename, false); status.Code(err) != codes.NotFound {
			return err
		}
		return nil
	}
	present, err := (&installation{Service: s}).presentOn(ctx, ref)
	if file, ok := present[project]; ok && err == nil {
		return s.Remove(ctx, ref, file.GetFileName(), file.GetDisabled())
	}
	return err
}

// Remove deletes a plugin file from a server, a turned-off one with disabled.
func (s *Service) Remove(ctx context.Context, ref Ref, fileName string, disabled bool) error {
	conn, err := s.nodes.Conn(ctx, ref.NodeID)
	if err != nil {
		return err
	}
	_, err = noryxv1.NewPluginServiceClient(conn).RemovePlugin(ctx, &noryxv1.RemovePluginRequest{ServerId: ref.ServerID, FileName: fileName, Disabled: disabled})
	return err
}

// Enable turns a plugin file of a server on or off: a turned-off file is kept in a folder of
// the plugin folder that the server doesn't load.
func (s *Service) Enable(ctx context.Context, ref Ref, fileName string, enabled bool) error {
	conn, err := s.nodes.Conn(ctx, ref.NodeID)
	if err != nil {
		return err
	}
	_, err = noryxv1.NewPluginServiceClient(conn).EnablePlugin(ctx, &noryxv1.EnablePluginRequest{ServerId: ref.ServerID, FileName: fileName, Enabled: enabled})
	if status.Code(err) == codes.Unimplemented {
		return httpapi.Errorf(http.StatusNotImplemented, "Update the agent of this node to turn plugins off.")
	}
	return err
}

// Pin keeps a project of a server at its version, so that updates of all its plugins leave it
// out, or lets them update it again.
func (s *Service) Pin(ctx context.Context, ref Ref, project string, pinned bool) error {
	if _, _, err := s.server(ctx, ref); err != nil {
		return err
	}
	return s.pins.set(ctx, ref, project, pinned)
}

// Track sets the channel a server takes a project in, which its updates follow.
func (s *Service) Track(ctx context.Context, ref Ref, project, channel string) error {
	if _, _, err := s.server(ctx, ref); err != nil {
		return err
	}
	return s.pins.track(ctx, ref, project, channel)
}

// Upload installs a plugin file of the administrator on a server.
func (s *Service) Upload(ctx context.Context, ref Ref, fileName string, content io.Reader) (Plugin, error) {
	conn, err := s.nodes.Conn(ctx, ref.NodeID)
	if err != nil {
		return Plugin{}, err
	}
	header := &noryxv1.InstallPluginHeader{ServerId: ref.ServerID, FileName: fileName}
	p, err := send(ctx, noryxv1.NewPluginServiceClient(conn), header, content)
	return Plugin{FileName: p.GetFileName(), Size: p.GetSize()}, err
}

// send streams a plugin file to the agent.
func send(ctx context.Context, c noryxv1.PluginServiceClient, header *noryxv1.InstallPluginHeader, content io.Reader) (*noryxv1.PluginFile, error) {
	stream, err := c.InstallPlugin(ctx)
	if err != nil {
		return nil, err
	}
	err = stream.Send(&noryxv1.InstallPluginRequest{Content: &noryxv1.InstallPluginRequest_Header{Header: header}})
	buf := make([]byte, chunkSize)
	for err == nil {
		n, readErr := io.ReadFull(content, buf)
		if n > 0 {
			err = stream.Send(&noryxv1.InstallPluginRequest{Content: &noryxv1.InstallPluginRequest_Data{Data: buf[:n]}})
		}
		if err == nil {
			err = readErr
		}
	}
	// EOF of the content: everything was sent. EOF of Send: the agent ended the stream,
	// its reply tells why.
	if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	res, err := stream.CloseAndRecv()
	return res.GetPlugin(), err
}

// server looks up a server on its node.
func (s *Service) server(ctx context.Context, ref Ref) (grpc.ClientConnInterface, *noryxv1.Server, error) {
	conn, err := s.nodes.Conn(ctx, ref.NodeID)
	if err != nil {
		return nil, nil, err
	}
	res, err := noryxv1.NewServerServiceClient(conn).ListServers(ctx, &noryxv1.ListServersRequest{})
	if err != nil {
		return nil, nil, err
	}
	i := slices.IndexFunc(res.GetServers(), func(srv *noryxv1.Server) bool { return srv.GetId() == ref.ServerID })
	if i < 0 {
		return nil, nil, httpapi.Errorf(http.StatusNotFound, "Server not found.")
	}
	return conn, res.GetServers()[i], nil
}

// memo runs a function once per key and shares its result, also between goroutines.
type memo[T any] struct {
	mu    sync.Mutex
	funcs map[string]func() (T, error)
}

func (m *memo[T]) get(key string, fn func() (T, error)) (T, error) {
	m.mu.Lock()
	f, ok := m.funcs[key]
	if !ok {
		f = sync.OnceValues(fn)
		if m.funcs == nil {
			m.funcs = map[string]func() (T, error){}
		}
		m.funcs[key] = f
	}
	m.mu.Unlock()
	return f()
}
