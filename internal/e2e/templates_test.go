package e2e

import (
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/plugin"
	"github.com/QwikByte/noryx/internal/master/template"
)

func TestTemplates(t *testing.T) {
	m := startMaster(t)
	a := m.startAgent(t, "node-1")
	api := apiClient{t: t, url: m.panel(t).URL}
	input := func(change func(map[string]any)) map[string]any {
		in := map[string]any{
			"name": "Survival", "description": "Paper with permissions", "type": "paper", "version": "", "memoryMb": 4096,
			"java": "21", "restartPolicy": "on_crash", "aikarFlags": true, "jvmOptions": []string{"-Dfile.encoding=UTF-8"}, "cpuLimit": 2,
			"stopTimeout": 300, "timeZone": "Europe/Berlin",
			"properties": map[string]string{"difficulty": "hard", "motd": "§aWelcome"}, "plugins": []string{"luckperms", "vault"},
			"versions": map[string]string{"luckperms": "luckperms10"}, "channels": map[string]string{"vault": "beta"}, "tags": []string{"Survival"},
		}
		change(in)
		return in
	}

	var tmpl template.Template
	api.do("POST", "/api/templates", input(func(map[string]any) {}), http.StatusCreated, &tmpl)
	if tmpl.Version != "LATEST" || len(tmpl.Plugins) != 2 || tmpl.Plugins[0].Title != "LuckPerms" || tmpl.Plugins[0].Icon != "/api/plugins/icons/luckperms/icon.png" ||
		tmpl.Plugins[0].VersionNumber != "1.0" || tmpl.Plugins[1].Version != "" || tmpl.Plugins[1].Channel != "beta" || !slices.Equal(tmpl.Tags, []string{"survival"}) {
		t.Fatalf("template = %+v", tmpl)
	}
	api.do("GET", "/api/templates/"+tmpl.ID, nil, http.StatusOK, &tmpl)
	if tmpl.Properties["difficulty"] != "hard" || tmpl.RestartPolicy != "on_crash" || tmpl.CPULimit != 2 || tmpl.StopTimeout != 300 || tmpl.TimeZone != "Europe/Berlin" {
		t.Fatalf("stored template = %+v", tmpl)
	}

	// Plugins must exist and run on the template's software.
	api.do("POST", "/api/templates", input(func(in map[string]any) {}), http.StatusConflict, nil)
	api.do("POST", "/api/templates", input(func(in map[string]any) { in["name"], in["plugins"] = "Mods", []string{"fabricapi"} }), http.StatusBadRequest, nil)
	api.do("POST", "/api/templates", input(func(in map[string]any) { in["name"], in["plugins"] = "Missing", []string{"missing"} }), http.StatusBadRequest, nil)
	api.do("POST", "/api/templates", input(func(in map[string]any) { in["name"], in["type"] = "Proxy", "velocity" }), http.StatusBadRequest, nil)
	api.do("POST", "/api/templates", input(func(in map[string]any) { in["name"], in["properties"] = "Bad", map[string]string{"a b": "c"} }), http.StatusBadRequest, nil)
	// Stop timeouts and time zones are checked like the agent checks them.
	api.do("POST", "/api/templates", input(func(in map[string]any) { in["name"], in["stopTimeout"] = "Fast", 10 }), http.StatusBadRequest, nil)
	api.do("POST", "/api/templates", input(func(in map[string]any) { in["name"], in["timeZone"] = "Local", "Local" }), http.StatusBadRequest, nil)
	api.do("POST", "/api/templates", input(func(in map[string]any) { in["name"], in["timeZone"] = "Path", "../../etc/passwd" }), http.StatusBadRequest, nil)
	// Kept versions must be of the template's plugins and run on its servers.
	api.do("POST", "/api/templates", input(func(in map[string]any) {
		in["name"], in["versions"] = "Kept", map[string]string{"luckperms": "vault17"}
	}), http.StatusBadRequest, nil)
	api.do("POST", "/api/templates", input(func(in map[string]any) { in["name"], in["versions"] = "Other", map[string]string{"broken": "broken10"} }), http.StatusBadRequest, nil)
	api.do("POST", "/api/templates", input(func(in map[string]any) { in["name"], in["tags"] = "Tags", []string{"<b>"} }), http.StatusBadRequest, nil)
	api.do("POST", "/api/templates", input(func(in map[string]any) {
		in["name"], in["properties"] = "RCON", map[string]string{"rcon.password": "x"}
	}), http.StatusBadRequest, nil)
	api.do("PUT", "/api/templates/"+tmpl.ID, input(func(in map[string]any) {
		in["name"], in["memoryMb"], in["stopTimeout"], in["timeZone"] = "Survival 2", 2048, 0, ""
	}), http.StatusOK, &tmpl)
	if tmpl.Name != "Survival 2" || tmpl.MemoryMB != 2048 || tmpl.StopTimeout != 60 || tmpl.TimeZone != "" {
		t.Fatalf("updated template = %+v", tmpl)
	}
	api.do("PUT", "/api/templates/"+tmpl.ID, input(func(in map[string]any) { in["name"], in["memoryMb"] = "Survival 2", 2048 }), http.StatusOK, &tmpl)

	// An exported template imports as a copy, checked like any other.
	file := api.do("GET", "/api/templates/"+tmpl.ID+"/export", nil, http.StatusOK, nil)
	api.do("POST", "/api/templates/import", []byte(file), http.StatusConflict, nil) // the name is taken
	var imported template.Template
	api.do("POST", "/api/templates/import", []byte(strings.Replace(file, `"Survival 2"`, `"Imported"`, 1)), http.StatusCreated, &imported)
	if imported.Name != "Imported" || !reflect.DeepEqual(imported.Plugins, tmpl.Plugins) || !reflect.DeepEqual(imported.Settings, tmpl.Settings) {
		t.Fatalf("imported %+v, want %+v", imported, tmpl)
	}
	api.do("POST", "/api/templates/import", []byte(strings.Replace(file, `"motd"`, `"server-port": "1", "motd"`, 1)), http.StatusBadRequest, nil)
	api.do("POST", "/api/templates/import", []byte(strings.Replace(file, `"version": 1`, `"version": 2`, 1)), http.StatusBadRequest, nil)
	api.do("DELETE", "/api/templates/"+imported.ID, nil, http.StatusNoContent, nil)

	// A server created with a template's settings gets its server.properties before it starts.
	path := "/api/nodes/" + a.node.ID + "/servers"
	// It gets the template's tags and plugins, in the versions the template keeps, and takes them
	// in their channels.
	server := map[string]any{
		"name": "Survival", "type": tmpl.Type, "version": tmpl.Version, "memoryMb": tmpl.MemoryMB, "port": 25565, "acceptEula": true,
		"java": tmpl.Java, "restartPolicy": tmpl.RestartPolicy, "aikarFlags": tmpl.AikarFlags, "jvmOptions": tmpl.JVMOptions,
		"cpuLimit": tmpl.CPULimit, "stopTimeout": tmpl.StopTimeout, "timeZone": tmpl.TimeZone, "properties": tmpl.Properties,
		"tags": tmpl.Tags, "plugins": []string{"luckperms", "vault"}, "versions": map[string]string{"luckperms": "luckperms10"},
		"channels": map[string]string{"vault": "beta"},
	}
	var created struct {
		ID          string
		Warning     string
		PluginError string
	}
	api.do("POST", path, server, http.StatusCreated, &created)
	spec := a.runtime.spec(created.ID)
	if spec.Java != "21" || spec.RestartPolicy != noryxv1.RestartPolicy_RESTART_POLICY_ON_CRASH || !spec.AikarFlags || spec.CPUMillis != 2000 ||
		spec.StopTimeout != 300 || spec.TimeZone != "Europe/Berlin" || created.Warning != "" || created.PluginError != "" {
		t.Fatalf("spec = %+v, warning %q, plugin error %q", spec, created.Warning, created.PluginError)
	}
	plugins, err := os.ReadDir(filepath.Join(a.runtime.dir, created.ID, "plugins"))
	check(t, err)
	if len(plugins) != 2 || plugins[0].Name() != "luckperms-1.0.jar" || plugins[1].Name() != "vault-1.7.jar" {
		t.Fatalf("plugins = %v", plugins)
	}
	var listing plugin.Listing
	api.do("GET", path+"/"+created.ID+"/plugins", nil, http.StatusOK, &listing)
	if p := listing.Plugins; len(p) != 2 || p[0].Tracks != "" || p[1].Tracks != "beta" {
		t.Fatalf("listing = %+v", listing)
	}
	var servers []struct {
		ID   string
		Tags []string
	}
	api.do("GET", path, nil, http.StatusOK, &servers)
	if len(servers) != 1 || !slices.Equal(servers[0].Tags, []string{"survival"}) {
		t.Fatalf("servers = %+v", servers)
	}
	props, err := os.ReadFile(filepath.Join(a.runtime.dir, created.ID, "server.properties"))
	check(t, err)
	if string(props) != "difficulty=hard\nmotd=\\u00A7aWelcome\n" {
		t.Fatalf("server.properties = %q", props)
	}

	// A kept version that doesn't run on the server leaves out its plugin rather than installing
	// another version of it.
	server["name"], server["port"], server["versions"] = "Kept", 25566, map[string]string{"luckperms": "vault17"}
	api.do("POST", path, server, http.StatusCreated, &created)
	plugins, err = os.ReadDir(filepath.Join(a.runtime.dir, created.ID, "plugins"))
	check(t, err)
	if created.PluginError != "The chosen version of LuckPerms doesn't run on Paper 1.21.4." || len(plugins) != 1 || plugins[0].Name() != "vault-1.7.jar" {
		t.Fatalf("plugin error %q, plugins %v", created.PluginError, plugins)
	}

	// Properties the manager sets itself are refused before anything is created.
	server["port"], server["properties"] = 25567, map[string]string{"server-port": "1"}
	api.do("POST", path, server, http.StatusBadRequest, nil)
	server["type"], server["properties"] = "velocity", map[string]string{"motd": "x"}
	api.do("POST", path, server, http.StatusBadRequest, nil)
	if n := len(a.runtime.servers); n != 2 {
		t.Fatalf("%d servers, want 2", n)
	}

	api.do("DELETE", "/api/templates/"+tmpl.ID, nil, http.StatusNoContent, nil)
	api.do("GET", "/api/templates/"+tmpl.ID, nil, http.StatusNotFound, nil)
	if body := api.do("GET", "/api/templates", nil, http.StatusOK, nil); body != "[]\n" {
		t.Fatalf("templates after delete = %s", body)
	}
}
