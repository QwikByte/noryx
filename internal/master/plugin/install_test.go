package plugin

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/geysermc"
	"github.com/QwikByte/noryx/internal/master/hangar"
	"github.com/QwikByte/noryx/internal/master/modrinth"
	"github.com/QwikByte/noryx/internal/master/node/nodetest"
)

// fakeModrinth serves the parts of Modrinth's API (/v2) and CDN (/cdn/) that the master uses,
// with the projects and versions of a test. Hangar and GeyserMC know nothing there.
type fakeModrinth struct {
	*httptest.Server

	mu        sync.Mutex
	projects  []modrinth.Project
	versions  []modrinth.Version // the oldest first
	files     map[string][]byte  // by path on the CDN
	downloads int
	down      bool // the API answers 503
}

func newFakeModrinth(t *testing.T) *fakeModrinth {
	f := &fakeModrinth{files: map[string][]byte{}}
	mux := http.NewServeMux()
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	api := func(pattern string, answer func(r *http.Request) any) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.down {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(answer(r))
		})
	}
	list := func(r *http.Request, name string) []string {
		var values []string
		_ = json.Unmarshal([]byte(r.URL.Query().Get(name)), &values)
		return values
	}
	api("GET /v2/projects", func(r *http.Request) any {
		ids := list(r, "ids")
		return slices.DeleteFunc(slices.Clone(f.projects), func(p modrinth.Project) bool { return !slices.Contains(ids, p.ID) })
	})
	api("GET /v2/project/{id}/version", func(r *http.Request) any {
		loaders, games := list(r, "loaders"), list(r, "game_versions")
		return f.find(func(v modrinth.Version) bool {
			return v.ProjectID == r.PathValue("id") && overlaps(v.Loaders, loaders) && (len(games) == 0 || overlaps(v.GameVersions, games))
		})
	})
	byHash := func(r *http.Request, newest bool) any {
		var req struct {
			Hashes  []string `json:"hashes"`
			Loaders []string `json:"loaders"`
			Types   []string `json:"version_types"`
			Games   []string `json:"game_versions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		found := map[string]modrinth.Version{}
		for _, hash := range req.Hashes {
			of := f.find(func(v modrinth.Version) bool { return v.Files[0].Hashes.SHA512 == hash })
			if len(of) > 0 && newest {
				of = f.find(func(v modrinth.Version) bool {
					return v.ProjectID == of[0].ProjectID && overlaps(v.Loaders, req.Loaders) && slices.Contains(req.Types, v.VersionType) &&
						(len(req.Games) == 0 || overlaps(v.GameVersions, req.Games))
				})
			}
			if len(of) > 0 {
				found[hash] = of[0]
			}
		}
		return found
	}
	api("POST /v2/version_files", func(r *http.Request) any { return byHash(r, false) })
	api("POST /v2/version_files/update", func(r *http.Request) any { return byHash(r, true) })
	api("GET /v2/tag/game_version", func(*http.Request) any {
		return []map[string]string{{"version": "26.1-snapshot-2", "version_type": "snapshot"}, {"version": "1.21.4", "version_type": "release"}}
	})
	mux.HandleFunc("GET /cdn/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.downloads++
		data, ok := f.files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	})
	return f
}

func overlaps(a, b []string) bool {
	return slices.ContainsFunc(a, func(s string) bool { return slices.Contains(b, s) })
}

// find returns the matching versions, the newest first.
func (f *fakeModrinth) find(match func(modrinth.Version) bool) []modrinth.Version {
	found := slices.DeleteFunc(slices.Clone(f.versions), func(v modrinth.Version) bool { return !match(v) })
	slices.Reverse(found)
	return found
}

var nonAlphanumeric = regexp.MustCompile(`[^A-Za-z0-9]`)

// add adds a version of a project, newer than the ones before, in a channel, for loaders and
// Minecraft 1.21.4, which requires other projects. Its file is <project>-<number>.jar on the
// CDN, and the project is added with the title of its ID unless it exists.
func (f *fakeModrinth) add(project, number, channel string, loaders []string, requires ...string) modrinth.Version {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.ContainsFunc(f.projects, func(p modrinth.Project) bool { return p.ID == project }) {
		f.projects = append(f.projects, modrinth.Project{ID: project, Slug: project, Title: strings.ToUpper(project[:1]) + project[1:], Loaders: loaders})
	}
	id := nonAlphanumeric.ReplaceAllString(project+number, "")
	name := project + "-" + number + ".jar"
	path := "/cdn/data/" + project + "/versions/" + id + "/" + name
	content := content(project, number)
	file := modrinth.File{URL: f.URL + path, Filename: name, Primary: true, Size: int64(len(content))}
	file.Hashes.SHA512 = hash(sha512.New(), content)
	f.files[path] = content
	v := modrinth.Version{
		ID: id, ProjectID: project, VersionNumber: number, VersionType: channel, Published: time.Date(2026, 1, len(f.versions)+1, 0, 0, 0, 0, time.UTC),
		Files: []modrinth.File{file}, Loaders: loaders, GameVersions: []string{"1.21.4"}, Changelog: "Changes in " + number,
	}
	for _, p := range requires {
		v.Dependencies = append(v.Dependencies, modrinth.Dependency{ProjectID: p, Type: "required"})
	}
	f.versions = append(f.versions, v)
	return v
}

// content is the file of a version of a project.
func content(project, number string) []byte { return []byte(project + " " + number) }

func hash(h interface {
	io.Writer
	Sum([]byte) []byte
}, data []byte) string {
	_, _ = h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// agent is the agent of a node with servers and their plugin files, which it hashes.
type agent struct {
	noryxv1.UnimplementedServerServiceServer
	noryxv1.UnimplementedPluginServiceServer

	mu        sync.Mutex
	servers   []*noryxv1.Server
	files     map[string]map[string][]byte // by server and name, those turned off as off/<name>
	refuse    error                        // of installs
	installed []*noryxv1.InstallPluginHeader
}

// put puts a plugin file on a server.
func (a *agent) put(serverID, name string, data []byte, disabled bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.files[serverID] == nil {
		a.files[serverID] = map[string][]byte{}
	}
	if disabled {
		name = "off/" + name
	}
	a.files[serverID][name] = data
}

// plugins returns the names of the plugin files of a server, sorted, with their content.
func (a *agent) plugins(serverID string) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var list []string
	for name, data := range a.files[serverID] {
		list = append(list, name+": "+string(data))
	}
	slices.Sort(list)
	return list
}

func (a *agent) ListServers(context.Context, *noryxv1.ListServersRequest) (*noryxv1.ListServersResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return &noryxv1.ListServersResponse{Servers: a.servers}, nil
}

func (a *agent) ListPlugins(_ context.Context, req *noryxv1.ListPluginsRequest) (*noryxv1.ListPluginsResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	res := &noryxv1.ListPluginsResponse{Folder: "plugins"}
	for name, data := range a.files[req.GetServerId()] {
		name, disabled := strings.CutPrefix(name, "off/")
		if disabled && !req.GetIncludeDisabled() {
			continue
		}
		settings, _, _ := strings.Cut(name, "-")
		res.Plugins = append(res.Plugins, &noryxv1.PluginFile{
			FileName: name, Size: int64(len(data)), Sha512: hash(sha512.New(), data), Sha256: hash(sha256.New(), data), Disabled: disabled, Settings: settings,
		})
	}
	slices.SortFunc(res.Plugins, func(a, b *noryxv1.PluginFile) int { return strings.Compare(a.GetFileName(), b.GetFileName()) })
	return res, nil
}

func (a *agent) InstallPlugin(stream grpc.ClientStreamingServer[noryxv1.InstallPluginRequest, noryxv1.InstallPluginResponse]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	h := first.GetHeader()
	var data []byte
	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		data = append(data, msg.GetData()...)
	}
	a.mu.Lock()
	refuse := a.refuse
	a.installed = append(a.installed, h)
	a.mu.Unlock()
	if refuse != nil {
		return refuse
	}
	if h.GetReplaces() != "" {
		a.remove(h.GetServerId(), h.GetReplaces(), h.GetDisabled())
	}
	a.put(h.GetServerId(), h.GetFileName(), data, h.GetDisabled())
	return stream.SendAndClose(&noryxv1.InstallPluginResponse{Plugin: &noryxv1.PluginFile{FileName: h.GetFileName(), Size: int64(len(data))}})
}

func (a *agent) remove(serverID, name string, disabled bool) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if disabled {
		name = "off/" + name
	}
	_, ok := a.files[serverID][name]
	delete(a.files[serverID], name)
	return ok
}

func (a *agent) RemovePlugin(_ context.Context, req *noryxv1.RemovePluginRequest) (*noryxv1.RemovePluginResponse, error) {
	if !a.remove(req.GetServerId(), req.GetFileName(), req.GetDisabled()) {
		return nil, status.Error(codes.NotFound, "plugin not found")
	}
	return &noryxv1.RemovePluginResponse{}, nil
}

func (a *agent) EnablePlugin(_ context.Context, req *noryxv1.EnablePluginRequest) (*noryxv1.EnablePluginResponse, error) {
	a.mu.Lock()
	data, ok := a.files[req.GetServerId()][map[bool]string{true: "off/", false: ""}[req.GetEnabled()]+req.GetFileName()]
	a.mu.Unlock()
	if !ok {
		return nil, status.Error(codes.NotFound, "plugin not found")
	}
	a.remove(req.GetServerId(), req.GetFileName(), req.GetEnabled())
	a.put(req.GetServerId(), req.GetFileName(), data, !req.GetEnabled())
	return &noryxv1.EnablePluginResponse{}, nil
}

// plugins is a master with Modrinth's projects luckperms (1.0 and 2.0, which requires vault),
// vault, via (a release and a newer beta), broken (whose 2.0 isn't what Modrinth says) for
// Paper, and maintenance for Velocity. The agent of node n1 runs the Paper servers lobby, which
// runs, and hub, the proxy and vanilla; the agent of n2 doesn't answer.
type plugins struct {
	*Service
	modrinth *fakeModrinth
	agent    *agent
}

var (
	lobby, hub, proxy, vanilla = Ref{"n1", "lobby"}, Ref{"n1", "hub"}, Ref{"n1", "proxy"}, Ref{"n1", "vanilla"}
	paper                      = []string{"paper", "spigot", "bukkit"}
)

func newPlugins(t *testing.T) *plugins {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	nodes := nodetest.New(t, db)
	a := &agent{files: map[string]map[string][]byte{}, servers: []*noryxv1.Server{
		{Id: "lobby", Name: "Lobby", Type: noryxv1.ServerType_SERVER_TYPE_PAPER, Version: "1.21.4", State: noryxv1.ServerState_SERVER_STATE_RUNNING},
		{Id: "hub", Name: "Hub", Type: noryxv1.ServerType_SERVER_TYPE_PAPER, Version: "LATEST", State: noryxv1.ServerState_SERVER_STATE_STOPPED},
		{Id: "proxy", Name: "Proxy", Type: noryxv1.ServerType_SERVER_TYPE_VELOCITY, State: noryxv1.ServerState_SERVER_STATE_RUNNING},
		{Id: "vanilla", Name: "Vanilla", Type: noryxv1.ServerType_SERVER_TYPE_VANILLA, Version: "1.21.4"},
	}}
	nodes.Add(t, "n1", func(s *grpc.Server) {
		noryxv1.RegisterServerServiceServer(s, a)
		noryxv1.RegisterPluginServiceServer(s, a)
	})
	nodes.Add(t, "n2", nil)
	f := newFakeModrinth(t)
	f.add("luckperms", "1.0", "release", paper)
	f.add("luckperms", "2.0", "release", paper, "vault")
	f.add("vault", "1.7", "release", paper)
	f.add("via", "4.0", "release", paper)
	f.add("via", "5.0-beta", "beta", paper)
	f.add("broken", "1.0", "release", paper)
	f.add("broken", "2.0", "release", paper)
	f.files["/cdn/data/broken/versions/broken20/broken-2.0.jar"] = []byte("tampered")
	f.add("maintenance", "5.1.0", "release", []string{"velocity"})
	return &plugins{
		Service: NewService(db, nodes, modrinth.New(f.URL+"/v2", f.URL+"/cdn/"), hangar.New(f.URL+"/hangar", f.URL+"/hangar/cdn/"),
			geysermc.New(f.URL+"/geysermc", f.URL+"/geysermc", 0)),
		modrinth: f, agent: a,
	}
}

// installed describes what results installed, removed and why they failed, by server.
func installed(results []Result) []string {
	var out []string
	for _, r := range results {
		desc := r.ServerID + ":"
		for _, i := range r.Installed {
			desc += " " + i.FileName
			if i.Channel != "" {
				desc += " (" + i.Channel + ")"
			}
			if i.written {
				desc += " written"
			}
		}
		for _, name := range r.Removed {
			desc += " -" + name
		}
		if len(r.Pinned) > 0 {
			desc += " pinned " + strings.Join(r.Pinned, ", ")
		}
		if r.Restart {
			desc += ", restart"
		}
		if r.Error != "" {
			desc += ", " + r.Error
		}
		out = append(out, desc)
	}
	return out
}

func (p *plugins) downloads() int {
	p.modrinth.mu.Lock()
	defer p.modrinth.mu.Unlock()
	return p.modrinth.downloads
}

// Installing puts the newest suitable release of a project on each server with the projects it
// requires, downloading each file once; servers that run need a restart for what is new. A
// project installed already stays, one in another version is replaced, and the servers take a
// project in the channel chosen for it. What can't be installed fails on its server alone.
func TestInstall(t *testing.T) {
	p := newPlugins(t)
	results := p.Install(t.Context(), []string{"luckperms"}, nil, nil, []Ref{lobby, hub, vanilla, {"n2", "lobby"}})
	if got := installed(results); !slices.Equal(got, []string{
		"lobby: luckperms-2.0.jar written vault-1.7.jar written, restart",
		"hub: luckperms-2.0.jar written vault-1.7.jar written",
		"vanilla:, Vanilla and Bedrock servers can't load plugins or mods.",
		"lobby:, n2 can't be reached. Check that its agent is running and that the master can connect to it.",
	}) {
		t.Fatalf("installed %q", got)
	}
	if got := p.agent.plugins("lobby"); !slices.Equal(got, []string{"luckperms-2.0.jar: luckperms 2.0", "vault-1.7.jar: vault 1.7"}) || p.downloads() != 2 {
		t.Fatalf("lobby has %q after %d downloads", got, p.downloads())
	}
	// The projects it requires are present, and only those to install are listed.
	if got := installed(p.Install(t.Context(), []string{"luckperms"}, nil, nil, []Ref{lobby})); !slices.Equal(got, []string{"lobby: luckperms-2.0.jar"}) ||
		p.downloads() != 2 {
		t.Errorf("installed again %q", got)
	}

	// A chosen version replaces the file; one that doesn't run on the server isn't installed.
	results = p.Install(t.Context(), []string{"luckperms"}, map[string]string{"luckperms": "luckperms10"}, nil, []Ref{lobby})
	if got := installed(results); !slices.Equal(got, []string{"lobby: luckperms-1.0.jar written, restart"}) {
		t.Errorf("installed the chosen version %q", got)
	}
	p.agent.mu.Lock()
	if last := p.agent.installed[len(p.agent.installed)-1]; last.GetReplaces() != "luckperms-2.0.jar" {
		t.Errorf("installed %v", last)
	}
	p.agent.mu.Unlock()
	results = p.Install(t.Context(), []string{"luckperms"}, map[string]string{"luckperms": "via40"}, nil, []Ref{lobby})
	if got := installed(results); !slices.Equal(got, []string{"lobby:, The chosen version of Luckperms doesn't run on Paper 1.21.4."}) {
		t.Errorf("installed another project's version %q", got)
	}

	// Betas, which the server takes from then on.
	if got := installed(p.Install(t.Context(), []string{"via"}, nil, map[string]string{"via": "beta"}, []Ref{lobby})); !slices.Equal(got, []string{"lobby: via-5.0-beta.jar (beta) written, restart"}) {
		t.Errorf("installed the beta %q", got)
	}
	if k, err := p.pins.of(t.Context(), lobby); err != nil || k.channel("via") != "beta" || k.channel("luckperms") != "release" {
		t.Errorf("lobby keeps %+v, %v", k, err)
	}
	// Hub runs the newest release of Minecraft.
	if got := installed(p.Install(t.Context(), []string{"via"}, nil, nil, []Ref{hub})); !slices.Equal(got, []string{"hub: via-4.0.jar written"}) {
		t.Errorf("installed the release %q", got)
	}

	if got := installed(p.Install(t.Context(), []string{"broken"}, map[string]string{"broken": "broken20"}, nil, []Ref{lobby})); !slices.Equal(got, []string{
		"lobby:, The download of broken-2.0.jar is corrupted. Try again.",
	}) {
		t.Errorf("installed a file that isn't Modrinth's %q", got)
	}
	p.agent.mu.Lock()
	p.agent.refuse = status.Error(codes.ResourceExhausted, "The node has no space left.")
	p.agent.mu.Unlock()
	if got := installed(p.Install(t.Context(), []string{"maintenance"}, nil, nil, []Ref{proxy})); !slices.Equal(got, []string{"proxy:, The node has no space left."}) {
		t.Errorf("installed on a full node %q", got)
	}
	if got := p.agent.plugins("proxy"); len(got) != 0 {
		t.Errorf("proxy has %q", got)
	}
	if err := p.InstallOn(t.Context(), []string{"maintenance"}, nil, nil, "n1", "proxy"); err == nil || !strings.Contains(err.Error(), "The node has no space left.") {
		t.Errorf("InstallOn on a full node = %v", err)
	}
}

// Updating replaces the files of a server's projects with the newest version of the channel the
// server takes each in, with the projects they newly require. Turned-off files and projects the
// server keeps at their version stay, and one that fails holds up none of the others.
func TestUpdate(t *testing.T) {
	p := newPlugins(t)
	p.agent.put("lobby", "luckperms-1.0.jar", content("luckperms", "1.0"), false)
	p.agent.put("lobby", "via-4.0.jar", content("via", "4.0"), true)
	p.agent.put("lobby", "broken-1.0.jar", content("broken", "1.0"), false)
	p.agent.put("lobby", "custom.jar", []byte("my own"), false)
	p.agent.put("hub", "luckperms-1.0.jar", content("luckperms", "1.0"), false)
	p.agent.put("hub", "via-4.0.jar", content("via", "4.0"), false)
	if err := p.Pin(t.Context(), hub, "luckperms", true); err != nil {
		t.Fatal(err)
	}
	if err := p.Track(t.Context(), hub, "via", "beta"); err != nil {
		t.Fatal(err)
	}
	results := p.Update(t.Context(), []Ref{lobby, hub}, nil)
	if got := installed(results); !slices.Equal(got, []string{
		"lobby: luckperms-2.0.jar written vault-1.7.jar written, restart, Broken: The download of broken-2.0.jar is corrupted. Try again.",
		"hub: via-5.0-beta.jar (beta) written pinned Luckperms",
	}) {
		t.Fatalf("updated %q", got)
	}
	if got := p.agent.plugins("lobby"); !slices.Equal(got, []string{
		"broken-1.0.jar: broken 1.0", "custom.jar: my own", "luckperms-2.0.jar: luckperms 2.0", "off/via-4.0.jar: via 4.0", "vault-1.7.jar: vault 1.7",
	}) {
		t.Errorf("lobby has %q", got)
	}
	if got := p.agent.plugins("hub"); !slices.Equal(got, []string{"luckperms-1.0.jar: luckperms 1.0", "via-5.0-beta.jar: via 5.0-beta"}) {
		t.Errorf("hub has %q", got)
	}

	// Only one project, and nothing to do.
	if err := p.Pin(t.Context(), hub, "luckperms", false); err != nil {
		t.Fatal(err)
	}
	if got := installed(p.Update(t.Context(), []Ref{hub}, []string{"via"})); !slices.Equal(got, []string{"hub:"}) {
		t.Errorf("updated via %q", got)
	}
	if got := installed(p.Update(t.Context(), []Ref{hub}, []string{"luckperms"})); !slices.Equal(got, []string{"hub: luckperms-2.0.jar written vault-1.7.jar written"}) {
		t.Errorf("updated luckperms %q", got)
	}
}

// A listing tells the project of each file Modrinth knows, its update and the projects that
// require it; without Modrinth, it lists the files and tells why.
func TestList(t *testing.T) {
	p := newPlugins(t)
	p.agent.put("lobby", "luckperms-2.0.jar", content("luckperms", "2.0"), false)
	p.agent.put("lobby", "vault-1.7.jar", content("vault", "1.7"), false)
	p.agent.put("lobby", "via-4.0.jar", content("via", "4.0"), true)
	p.agent.put("lobby", "custom.jar", []byte("my own"), false)
	if err := p.Track(t.Context(), lobby, "via", "beta"); err != nil {
		t.Fatal(err)
	}
	l, err := p.List(t.Context(), lobby)
	describe := func(l Listing) (out []string) {
		for _, p := range l.Plugins {
			desc := p.FileName + " " + p.Version
			if p.Project != nil {
				desc += " of " + p.Project.Title
			}
			if p.Update != "" {
				desc += " to " + p.Update
			}
			if len(p.RequiredBy) > 0 {
				desc += " for " + strings.Join(p.RequiredBy, ", ")
			}
			if p.Disabled {
				desc += " off"
			}
			out = append(out, desc)
		}
		return out
	}
	if got := describe(l); err != nil || l.Folder != "plugins" || l.CatalogueError != "" || !slices.Equal(got, []string{
		"custom.jar ", "luckperms-2.0.jar 2.0 of Luckperms", "vault-1.7.jar 1.7 of Vault for Luckperms", "via-4.0.jar 4.0 of Via to 5.0-beta off",
	}) {
		t.Fatalf("listing %q, %+v, %v", got, l, err)
	}
	p.modrinth.mu.Lock()
	p.modrinth.down = true
	p.modrinth.mu.Unlock()
	l, err = p.List(t.Context(), lobby)
	if got := describe(l); err != nil || !strings.HasPrefix(l.CatalogueError, "Modrinth can't be reached") || len(got) != 4 || got[1] != "luckperms-2.0.jar " {
		t.Errorf("listing without Modrinth %q, %+v, %v", got, l, err)
	}
	if _, err := p.List(t.Context(), Ref{"n1", "nope"}); err == nil {
		t.Error("listing of an unknown server")
	}
}

// Networks ensure the plugins of their proxies, provide the newest ones and uninstall them.
func TestEnsureProvideUninstall(t *testing.T) {
	p := newPlugins(t)
	for range 2 {
		if err := p.Ensure(t.Context(), proxy, "maintenance"); err != nil {
			t.Fatal(err)
		}
	}
	if got := p.agent.plugins("proxy"); !slices.Equal(got, []string{"maintenance-5.1.0.jar: maintenance 5.1.0"}) || len(p.agent.installed) != 1 {
		t.Fatalf("proxy has %q", got)
	}
	// A turned-off file is turned on again.
	p.agent.remove("proxy", "maintenance-5.1.0.jar", false)
	p.agent.put("proxy", "maintenance-5.1.0.jar", content("maintenance", "5.1.0"), true)
	if err := p.Ensure(t.Context(), proxy, "maintenance"); err != nil {
		t.Fatal(err)
	}
	if got := p.agent.plugins("proxy"); !slices.Equal(got, []string{"maintenance-5.1.0.jar: maintenance 5.1.0"}) {
		t.Fatalf("proxy has %q", got)
	}
	if changed, err := p.Provide(t.Context(), proxy, []string{"maintenance"}); changed || err != nil {
		t.Errorf("Provide of the newest = %t, %v", changed, err)
	}
	p.modrinth.add("maintenance", "5.2.0", "release", []string{"velocity"})
	if changed, err := p.Provide(t.Context(), proxy, []string{"maintenance"}); !changed || err != nil {
		t.Errorf("Provide of a newer one = %t, %v", changed, err)
	}
	for range 2 {
		if err := p.Uninstall(t.Context(), proxy, "maintenance"); err != nil {
			t.Fatal(err)
		}
	}
	if got := p.agent.plugins("proxy"); len(got) != 0 {
		t.Errorf("proxy has %q", got)
	}
}

// Removing a project removes its files from the servers, also turned-off ones; servers that run
// with them need a restart.
func TestRemoveProject(t *testing.T) {
	p := newPlugins(t)
	p.agent.put("lobby", "luckperms-2.0.jar", content("luckperms", "2.0"), false)
	p.agent.put("lobby", "vault-1.7.jar", content("vault", "1.7"), false)
	p.agent.put("hub", "luckperms-1.0.jar", content("luckperms", "1.0"), true)
	results := p.RemoveProject(t.Context(), "luckperms", []Ref{lobby, hub, {"n2", "lobby"}})
	if got := installed(results); !slices.Equal(got, []string{
		"lobby: -luckperms-2.0.jar, restart", "hub: -luckperms-1.0.jar",
		"lobby:, n2 can't be reached. Check that its agent is running and that the master can connect to it.",
	}) {
		t.Errorf("removed %q", got)
	}
	if got := p.agent.plugins("lobby"); !slices.Equal(got, []string{"vault-1.7.jar: vault 1.7"}) {
		t.Errorf("lobby has %q", got)
	}
}

// The versions of a project that run on servers of a type, and what changed in them.
func TestVersionsAndChanges(t *testing.T) {
	p := newPlugins(t)
	paper := noryxv1.ServerType_SERVER_TYPE_PAPER
	versions, err := p.Versions(t.Context(), "via", paper, "LATEST")
	if err != nil || len(versions) != 2 || versions[0].Number != "5.0-beta" || versions[0].Channel != "beta" || versions[1].Number != "4.0" {
		t.Fatalf("Versions = %+v, %v", versions, err)
	}
	if _, err := p.Versions(t.Context(), "via", noryxv1.ServerType_SERVER_TYPE_VANILLA, "1.21.4"); !errors.Is(err, errVanilla) {
		t.Errorf("Versions for vanilla = %v", err)
	}
	if versions, err := p.Versions(t.Context(), "via", paper, "1.20.1"); err != nil || len(versions) != 0 {
		t.Errorf("Versions for another Minecraft = %+v, %v", versions, err)
	}
	for _, tc := range []struct {
		from, to string
		want     []string
	}{
		{"", "", []string{"5.0-beta", "4.0"}},
		{"via40", "", []string{"5.0-beta"}},
		{"via40", "via50beta", []string{"5.0-beta"}},
		{"", "via40", []string{"4.0"}},
		{"unknown", "", []string{"5.0-beta"}},
	} {
		changes, err := p.Changes(t.Context(), "via", paper, "1.21.4", tc.from, tc.to)
		var got []string
		for _, c := range changes {
			got = append(got, c.Number)
			if c.Changelog != "Changes in "+c.Number {
				t.Errorf("changelog of %s: %q", c.Number, c.Changelog)
			}
		}
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("Changes from %q to %q = %q, %v", tc.from, tc.to, got, err)
		}
	}
}
