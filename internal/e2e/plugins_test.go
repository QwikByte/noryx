package e2e

import (
	"archive/zip"
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/plugin"
)

func TestPlugins(t *testing.T) {
	m := startMaster(t)
	a := m.startAgent(t, "node-1")
	lobby := m.createServer(t, a, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565)
	survival := m.createServer(t, a, "Survival", noryxv1.ServerType_SERVER_TYPE_PAPER, 25566)
	vanilla := m.createServer(t, a, "Vanilla", noryxv1.ServerType_SERVER_TYPE_VANILLA, 25567)
	api := apiClient{t: t, url: m.panel(t).URL}
	pluginsOf := func(ref plugin.Ref) plugin.Listing {
		t.Helper()
		var l plugin.Listing
		api.do("GET", "/api/nodes/"+ref.NodeID+"/servers/"+ref.ServerID+"/plugins", nil, http.StatusOK, &l)
		return l
	}
	file := func(ref plugin.Ref, name string) string {
		data, _ := os.ReadFile(filepath.Join(a.runtime.dir, ref.ServerID, "plugins", name))
		return string(data)
	}
	lobbyRef, survivalRef, vanillaRef := plugin.Ref(lobby), plugin.Ref(survival), plugin.Ref(vanilla)

	// The search only finds plugins for the server type, and icons come through the master.
	var found struct {
		Hits []struct {
			ID         string    `json:"id"`
			Icon       string    `json:"icon"`
			Categories *[]string `json:"categories"`
		} `json:"hits"`
	}
	api.do("GET", "/api/plugins/search?type=paper&version=LATEST", nil, http.StatusOK, &found)
	if len(found.Hits) != 3 || found.Hits[0].ID != "luckperms" || found.Hits[0].Icon != "/api/plugins/icons/luckperms/icon.png" ||
		found.Hits[0].Categories == nil { // a list, although the fake leaves them out
		t.Fatalf("search = %+v", found)
	}
	res, err := http.Get(api.url + found.Hits[0].Icon)
	check(t, err)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/png" || res.Header.Get("Content-Security-Policy") != "sandbox" {
		t.Fatalf("icon: %d %v", res.StatusCode, res.Header)
	}
	api.do("GET", "/api/plugins/icons/luckperms/icon.svg", nil, http.StatusNotFound, nil)

	// Filters, the sort and the Minecraft version go to Modrinth as facets; others are refused.
	api.do("GET", "/api/plugins/search?type=fabric&version=LATEST&category=economy&category=utility&sort=follows&serverOnly=true", nil, http.StatusOK, &found)
	q := *m.modrinth.search.Load()
	for _, facet := range []string{`["categories:economy"]`, `["categories:utility"]`, `["versions:1.21.4"]`, `["client_side:optional","client_side:unsupported"]`} {
		if !strings.Contains(q.Get("facets"), facet) || q.Get("index") != "follows" {
			t.Fatalf("search query = %v, want facet %s", q, facet)
		}
	}
	// A kind limits the search to plugins or mods and the loaders of its server types.
	api.do("GET", "/api/plugins/search?kind=mods", nil, http.StatusOK, &found)
	q = *m.modrinth.search.Load()
	if len(found.Hits) != 2 || found.Hits[0].ID != "fabricapi" || !strings.Contains(q.Get("facets"), `["project_type:mod"]`) {
		t.Fatalf("search = %+v, query = %v", found, q)
	}
	api.do("GET", "/api/plugins/search?kind=datapacks", nil, http.StatusBadRequest, nil)
	api.do("GET", "/api/plugins/search?sort=random", nil, http.StatusBadRequest, nil)
	api.do("GET", "/api/plugins/search?category=Eco+nomy", nil, http.StatusBadRequest, nil)
	var releases []string
	api.do("GET", "/api/plugins/game-versions", nil, http.StatusOK, &releases)
	if !slices.Equal(releases, []string{"1.21.4"}) {
		t.Fatalf("game versions = %q", releases)
	}
	var versions []plugin.Version
	api.do("GET", "/api/plugins/projects/luckperms/versions?type=paper&version=LATEST", nil, http.StatusOK, &versions)
	if len(versions) != 2 || versions[0].Number != "2.0" || versions[1].ID != "luckperms10" || versions[1].Channel != "release" {
		t.Fatalf("versions = %+v", versions)
	}

	// Installing on several servers adds the required projects; vanilla servers have no plugins.
	var installed struct{ Results []plugin.Result }
	api.do("POST", "/api/plugins/install", map[string]any{"projects": []string{"luckperms"}, "servers": []plugin.Ref{lobbyRef, survivalRef, vanillaRef}},
		http.StatusOK, &installed)
	for i, r := range installed.Results[:2] {
		if r.Error != "" || len(r.Installed) != 2 || r.Installed[0].Version != "2.0" || r.Installed[1].ProjectID != "vault" {
			t.Fatalf("result %d = %+v", i, r)
		}
	}
	// The panel reads the list of what was installed also where nothing was.
	if r := installed.Results[2]; r.Error != "Vanilla and Bedrock servers can't load plugins or mods." || r.Installed == nil {
		t.Fatalf("vanilla result = %+v", r)
	}
	if got := file(lobbyRef, "luckperms-2.0.jar"); got != "luckperms 2.0" {
		t.Fatalf("installed file = %q", got)
	}
	l := pluginsOf(lobbyRef)
	if l.Folder != "plugins" || len(l.Plugins) != 2 || l.Plugins[0].Project.Title != "LuckPerms" || l.Plugins[0].Version != "2.0" || l.Plugins[0].Update != "" {
		t.Fatalf("listing = %+v", l)
	}

	// An older version is recognised by its hash; updating replaces its file.
	base := "/api/nodes/" + survival.NodeID + "/servers/" + survival.ServerID + "/plugins/"
	api.do("DELETE", base+"luckperms-2.0.jar", nil, http.StatusNoContent, nil)
	api.do("PUT", base+"LuckPerms-old.jar", m.modrinth.content("luckperms", "1.0"), http.StatusCreated, nil)
	api.do("PUT", base+"Custom Plugin.jar", []byte("not from Modrinth"), http.StatusCreated, nil)
	l = pluginsOf(survivalRef)
	if p := l.Plugins[1]; p.FileName != "LuckPerms-old.jar" || p.Version != "1.0" || p.Update != "2.0" || l.Plugins[0].Project != nil {
		t.Fatalf("listing = %+v", l)
	}
	api.do("POST", "/api/plugins/install", map[string]any{"projects": []string{"luckperms"}, "servers": []plugin.Ref{survivalRef}}, http.StatusOK, &installed)
	if file(survivalRef, "LuckPerms-old.jar") != "" || file(survivalRef, "luckperms-2.0.jar") == "" {
		t.Fatal("the old version was not replaced")
	}
	names := []string{}
	for _, p := range pluginsOf(survivalRef).Plugins {
		names = append(names, p.FileName)
	}
	if want := []string{"Custom Plugin.jar", "luckperms-2.0.jar", "vault-1.7.jar"}; !slices.Equal(names, want) {
		t.Fatalf("plugins = %q, want %q", names, want)
	}

	// A chosen version replaces the installed one, as long as it runs on the server.
	install := func(versions map[string]string) plugin.Result {
		api.do("POST", "/api/plugins/install", map[string]any{"projects": []string{"luckperms"}, "versions": versions, "servers": []plugin.Ref{lobbyRef}},
			http.StatusOK, &installed)
		return installed.Results[0]
	}
	if r := install(map[string]string{"luckperms": "luckperms10"}); r.Error != "" || file(lobbyRef, "luckperms-2.0.jar") != "" {
		t.Fatalf("result = %+v", r)
	}
	if p := pluginsOf(lobbyRef).Plugins[0]; p.Version != "1.0" || p.VersionID != "luckperms10" || p.Update != "2.0" {
		t.Fatalf("plugin = %+v", p)
	}
	if r := install(map[string]string{"luckperms": "vault17"}); r.Error != "The chosen version of LuckPerms doesn't run on Paper 1.21.4." {
		t.Fatalf("result = %+v", r)
	}
	api.do("POST", "/api/plugins/install", map[string]any{"projects": []string{"luckperms"}, "versions": map[string]string{"vault": "vault17"}, "servers": []plugin.Ref{lobbyRef}},
		http.StatusBadRequest, nil)

	// Mods for other loaders, corrupted downloads and invalid file names are refused.
	api.do("POST", "/api/plugins/install", map[string]any{"projects": []string{"fabricapi", "broken"}, "servers": []plugin.Ref{lobbyRef}}, http.StatusOK, &installed)
	if r := installed.Results[0]; r.Error != "Fabric API has no version for Paper 1.21.4." {
		t.Fatalf("result = %+v", r)
	}
	api.do("POST", "/api/plugins/install", map[string]any{"projects": []string{"broken"}, "servers": []plugin.Ref{lobbyRef}}, http.StatusOK, &installed)
	if r := installed.Results[0]; r.Error != "The download of broken-1.0.jar is corrupted. Try again." || file(lobbyRef, "broken-1.0.jar") != "" {
		t.Fatalf("result = %+v", r)
	}
	api.do("POST", "/api/plugins/install", map[string]any{"projects": []string{"../x"}, "servers": []plugin.Ref{lobbyRef}}, http.StatusBadRequest, nil)
	api.do("POST", "/api/plugins/install", map[string]any{"projects": []string{"vault"}, "servers": []plugin.Ref{lobbyRef, lobbyRef}}, http.StatusBadRequest, nil)
	base = "/api/nodes/" + lobby.NodeID + "/servers/" + lobby.ServerID + "/plugins/"
	api.do("PUT", base+"notes.txt", []byte("x"), http.StatusBadRequest, nil)
	api.do("PUT", base+".hidden.jar", []byte("x"), http.StatusBadRequest, nil)
	api.do("PUT", base+"empty.jar", []byte{}, http.StatusBadRequest, nil)
	api.do("DELETE", base+"missing.jar", nil, http.StatusNotFound, nil)
	api.do("GET", "/api/nodes/"+vanilla.NodeID+"/servers/"+vanilla.ServerID+"/plugins", nil, http.StatusConflict, nil)
}

// Projects that keep the name of their file in every version, e.g. Geyser, are updated too.
func TestPluginWithTheSameFileName(t *testing.T) {
	m := startMaster(t)
	a := m.startAgent(t, "node-1")
	lobby := plugin.Ref(m.createServer(t, a, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565))
	paper := []string{"paper", "spigot", "bukkit"}
	m.modrinth.project("same", "Same", paper)
	old := m.modrinth.release("same", "1.0", "Same.jar", []byte("same 1.0"), paper)
	m.modrinth.release("same", "2.0", "Same.jar", []byte("same 2.0"), paper)
	api := apiClient{t: t, url: m.panel(t).URL}
	install := func(versions map[string]string) {
		t.Helper()
		var installed struct{ Results []plugin.Result }
		api.do("POST", "/api/plugins/install", map[string]any{"projects": []string{"same"}, "versions": versions, "servers": []plugin.Ref{lobby}}, http.StatusOK, &installed)
		if r := installed.Results[0]; r.Error != "" {
			t.Fatalf("result = %+v", r)
		}
	}
	install(map[string]string{"same": old.ID})
	install(nil)
	if data, _ := os.ReadFile(filepath.Join(a.runtime.dir, lobby.ServerID, "plugins", "Same.jar")); string(data) != "same 2.0" {
		t.Fatalf("Same.jar = %q", data)
	}
}

// A plugin can be kept at its version and turned off, which updates of all plugins leave out,
// and tells what changed in its versions, where its settings are and what requires it.
func TestPluginsOfAServer(t *testing.T) {
	m := startMaster(t)
	a := m.startAgent(t, "node-1")
	lobby := plugin.Ref(m.createServer(t, a, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565))
	api := apiClient{t: t, url: m.panel(t).URL}
	base := "/api/nodes/" + lobby.NodeID + "/servers/" + lobby.ServerID + "/plugins"
	folder := filepath.Join(a.runtime.dir, lobby.ServerID, "plugins")
	plugins := func() map[string]plugin.Plugin {
		t.Helper()
		var l plugin.Listing
		api.do("GET", base, nil, http.StatusOK, &l)
		byFile := map[string]plugin.Plugin{}
		for _, p := range l.Plugins {
			byFile[p.FileName] = p
		}
		return byFile
	}
	update := func() plugin.Result {
		t.Helper()
		var updated struct{ Results []plugin.Result }
		api.do("POST", "/api/plugins/update", map[string]any{"servers": []plugin.Ref{lobby}}, http.StatusOK, &updated)
		return updated.Results[0]
	}

	// What changed: in the versions after the installed one up to the update, or in all.
	var changes []plugin.Change
	api.do("GET", "/api/plugins/projects/luckperms/changes?type=paper&version=LATEST&from=luckperms10&to=luckperms20", nil, http.StatusOK, &changes)
	if len(changes) != 1 || changes[0].Number != "2.0" || changes[0].Changelog != "Changes in 2.0" {
		t.Fatalf("changes = %+v", changes)
	}
	api.do("GET", "/api/plugins/projects/luckperms/changes?type=paper&version=LATEST", nil, http.StatusOK, &changes)
	if len(changes) != 2 || changes[1].Changelog != "Changes in 1.0" {
		t.Fatalf("all changes = %+v", changes)
	}
	api.do("GET", "/api/plugins/projects/luckperms/changes?type=paper&from=../x", nil, http.StatusBadRequest, nil)

	// LuckPerms 2.0 requires Vault, until it is turned off.
	api.do("POST", "/api/plugins/install", map[string]any{"projects": []string{"luckperms"}, "servers": []plugin.Ref{lobby}}, http.StatusOK, nil)
	if p := plugins()["vault-1.7.jar"]; !slices.Equal(p.RequiredBy, []string{"LuckPerms"}) {
		t.Fatalf("Vault = %+v", p)
	}
	api.do("POST", base+"/luckperms-2.0.jar/disable", nil, http.StatusNoContent, nil)
	api.do("POST", base+"/luckperms-2.0.jar/disable", nil, http.StatusNotFound, nil)
	if _, err := os.Stat(filepath.Join(folder, ".disabled", "luckperms-2.0.jar")); err != nil {
		t.Fatal(err)
	}
	if listed := plugins(); !listed["luckperms-2.0.jar"].Disabled || len(listed["vault-1.7.jar"].RequiredBy) != 0 {
		t.Fatalf("plugins = %+v", listed)
	}
	api.do("POST", base+"/luckperms-2.0.jar/enable", nil, http.StatusNoContent, nil)

	// The folder of a plugin's settings, as its plugin.yml names it.
	api.do("PUT", base+"/Example.jar", jarOf(t, map[string]string{"plugin.yml": "name: Example\nmain: me.Example\n"}), http.StatusCreated, nil)
	check(t, os.MkdirAll(filepath.Join(folder, "Example"), 0o750))
	if p := plugins()["Example.jar"]; p.Settings != "plugins/Example" || p.Project != nil {
		t.Fatalf("Example = %+v", p)
	}
	api.do("POST", base+"/Example.jar/disable", nil, http.StatusNoContent, nil)
	api.do("DELETE", base+"/Example.jar", nil, http.StatusNotFound, nil)
	api.do("DELETE", base+"/Example.jar?disabled=true", nil, http.StatusNoContent, nil)

	// A pinned project stays at its version when all plugins are updated, and so does a
	// turned-off one.
	api.do("POST", "/api/plugins/install", map[string]any{"projects": []string{"luckperms"}, "versions": map[string]string{"luckperms": "luckperms10"}, "servers": []plugin.Ref{lobby}},
		http.StatusOK, nil)
	api.do("PUT", base+"/pins/luckperms", nil, http.StatusNoContent, nil)
	api.do("PUT", base+"/pins/bad.id", nil, http.StatusBadRequest, nil)
	if p := plugins()["luckperms-1.0.jar"]; !p.Pinned || p.Update != "2.0" || p.UpdateID != "luckperms20" {
		t.Fatalf("LuckPerms = %+v", p)
	}
	if r := update(); r.Error != "" || len(r.Installed) != 0 || !slices.Equal(r.Pinned, []string{"LuckPerms"}) {
		t.Fatalf("update with a pin = %+v", r)
	}
	api.do("DELETE", base+"/pins/luckperms", nil, http.StatusNoContent, nil)
	api.do("POST", base+"/luckperms-1.0.jar/disable", nil, http.StatusNoContent, nil)
	if r := update(); r.Error != "" || len(r.Installed) != 0 || len(r.Pinned) != 0 {
		t.Fatalf("update of a turned-off plugin = %+v", r)
	}
	api.do("POST", base+"/luckperms-1.0.jar/enable", nil, http.StatusNoContent, nil)
	if r := update(); r.Error != "" || len(r.Installed) != 1 || r.Installed[0].Version != "2.0" || r.Restart {
		t.Fatalf("update = %+v", r)
	}
	if _, ok := plugins()["luckperms-2.0.jar"]; !ok {
		t.Fatal("LuckPerms wasn't updated")
	}

	// Pins go with their server.
	api.do("PUT", base+"/pins/luckperms", nil, http.StatusNoContent, nil)
	api.do("DELETE", "/api/nodes/"+lobby.NodeID+"/servers/"+lobby.ServerID, nil, http.StatusNoContent, nil)
	var pins int
	check(t, m.db.QueryRow(`SELECT COUNT(*) FROM plugin_pins`).Scan(&pins))
	if pins != 0 {
		t.Fatalf("%d pins left", pins)
	}
}

// A server takes a project in a channel, releases unless installing chose betas or alphas too,
// which updates follow, on Modrinth and Hangar, and which goes with the server.
func TestPluginChannels(t *testing.T) {
	m := startMaster(t)
	paper := []string{"paper", "spigot", "bukkit"}
	m.modrinth.project("via", "ViaVersion", paper)
	m.modrinth.version("via", "5.0", paper)
	m.modrinth.preRelease("beta", "via", "5.1b1", paper)
	m.modrinth.preRelease("alpha", "via", "5.2a1", paper)
	m.modrinth.project("addon", "ViaAddon", paper)
	m.modrinth.version("addon", "1.0", paper, "via")
	m.hangar.project(6, "Squaremap", "PAPER")
	m.hangar.version(6, "1.0", "PAPER")
	m.hangar.versionIn("Beta", 6, "1.1", "PAPER")
	m.hangar.versionIn("Snapshot", 6, "1.2", "PAPER")
	a := m.startAgent(t, "node-1")
	lobby := plugin.Ref(m.createServer(t, a, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565))
	api := apiClient{t: t, url: m.panel(t).URL}
	base := "/api/nodes/" + lobby.NodeID + "/servers/" + lobby.ServerID + "/plugins"
	listed := func(project string) plugin.Plugin {
		t.Helper()
		var l plugin.Listing
		api.do("GET", base, nil, http.StatusOK, &l)
		i := slices.IndexFunc(l.Plugins, func(p plugin.Plugin) bool { return p.Project != nil && p.Project.ID == project })
		if i < 0 {
			t.Fatalf("%s isn't listed: %+v", project, l)
		}
		return l.Plugins[i]
	}
	install := func(projects []string, channels map[string]string) plugin.Result {
		t.Helper()
		var installed struct{ Results []plugin.Result }
		api.do("POST", "/api/plugins/install", map[string]any{"projects": projects, "channels": channels, "servers": []plugin.Ref{lobby}}, http.StatusOK, &installed)
		return installed.Results[0]
	}
	track := func(project, channel string, status int) {
		t.Helper()
		api.do("PUT", base+"/channels/"+project, map[string]string{"channel": channel}, status, nil)
	}

	// Installing in the beta channel takes the newest beta, and updates follow the channel.
	if r := install([]string{"via"}, map[string]string{"via": "beta"}); r.Error != "" || len(r.Installed) != 1 || r.Installed[0].Version != "5.1b1" {
		t.Fatalf("result = %+v", r)
	}
	if p := listed("via"); p.Tracks != "beta" || p.Channel != "beta" || p.Update != "" {
		t.Fatalf("ViaVersion = %+v", p)
	}
	track("via", "alpha", http.StatusNoContent)
	if p := listed("via"); p.Update != "5.2a1" || p.UpdateChannel != "alpha" {
		t.Fatalf("ViaVersion = %+v", p)
	}
	var updated struct{ Results []plugin.Result }
	api.do("POST", "/api/plugins/update", map[string]any{"servers": []plugin.Ref{lobby}}, http.StatusOK, &updated)
	if r := updated.Results[0]; r.Error != "" || len(r.Installed) != 1 || r.Installed[0].Version != "5.2a1" || r.Installed[0].Channel != "alpha" {
		t.Fatalf("update = %+v", r)
	}
	// Back in releases, the older release is no update.
	track("via", "release", http.StatusNoContent)
	if p := listed("via"); p.Tracks != "" || p.Version != "5.2a1" || p.Update != "" {
		t.Fatalf("ViaVersion = %+v", p)
	}
	track("via", "nightly", http.StatusBadRequest)
	track("bad.id", "beta", http.StatusBadRequest)
	for _, channels := range []map[string]string{{"via": "nightly"}, {"vault": "beta"}} {
		api.do("POST", "/api/plugins/install", map[string]any{"projects": []string{"via"}, "channels": channels, "servers": []plugin.Ref{lobby}}, http.StatusBadRequest, nil)
	}

	// A project new on a server takes releases, whatever an earlier file of it took.
	track("via", "alpha", http.StatusNoContent)
	api.do("DELETE", base+"/via-5.2a1.jar", nil, http.StatusNoContent, nil)
	if r := install([]string{"addon"}, nil); r.Error != "" || len(r.Installed) != 2 || r.Installed[1].Version != "5.0" {
		t.Fatalf("result = %+v", r)
	}
	if p := listed("via"); p.Tracks != "" || p.Update != "" {
		t.Fatalf("ViaVersion = %+v", p)
	}

	// Hangar's channels other than Release and Beta count as alpha.
	if r := install([]string{"hangar-6"}, map[string]string{"hangar-6": "beta"}); r.Error != "" || len(r.Installed) != 1 || r.Installed[0].Version != "1.1" {
		t.Fatalf("result = %+v", r)
	}
	if p := listed("hangar-6"); p.Tracks != "beta" || p.Channel != "beta" || p.Update != "" {
		t.Fatalf("Squaremap = %+v", p)
	}
	track("hangar-6", "alpha", http.StatusNoContent)
	if p := listed("hangar-6"); p.Update != "1.2" || p.UpdateChannel != "alpha" {
		t.Fatalf("Squaremap = %+v", p)
	}

	// Channels go with their server.
	api.do("DELETE", "/api/nodes/"+lobby.NodeID+"/servers/"+lobby.ServerID, nil, http.StatusNoContent, nil)
	var channels int
	check(t, m.db.QueryRow(`SELECT COUNT(*) FROM plugin_channels`).Scan(&channels))
	if channels != 0 {
		t.Fatalf("%d channels left", channels)
	}
}

func jarOf(t *testing.T, files map[string]string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		check(t, err)
		_, err = w.Write([]byte(content))
		check(t, err)
	}
	check(t, zw.Close())
	return buf.Bytes()
}
