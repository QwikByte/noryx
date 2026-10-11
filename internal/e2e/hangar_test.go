package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/plugin"
)

// fakeHangar serves the parts of Hangar's API (/api/v1) and CDN (/cdn) the master uses:
//
//   - 1: Chunky, a Paper plugin with the releases 1.0 and 2.0; 2.0 requires 2
//   - 2: ChunkyBorder, a Paper plugin
//   - 3: Tampered, a Paper plugin whose download doesn't match its hash
//   - 4: Proxied, a Velocity plugin
//   - 5: luckperms, a Paper plugin whose 1.0 is the same file as on Modrinth, and 2.5
type fakeHangar struct {
	*httptest.Server
	projects []map[string]any
	versions []map[string]any // the newest last
	files    map[string][]byte
}

func startHangar(t *testing.T) *fakeHangar {
	f := &fakeHangar{files: map[string][]byte{"/cdn/avatars/project/1.webp": []byte("RIFF")}}
	mux := http.NewServeMux()
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	f.project(1, "Chunky", "PAPER")
	f.version(1, "1.0", "PAPER")
	f.version(1, "2.0", "PAPER", 2)
	f.project(2, "ChunkyBorder", "PAPER")
	f.version(2, "1.0", "PAPER")
	f.project(3, "Tampered", "PAPER")
	f.version(3, "1.0", "PAPER")
	f.files["/cdn/plugins/3/1.0/PAPER/Tampered.jar"] = []byte("tampered")
	f.project(4, "Proxied", "VELOCITY")
	f.version(4, "1.0", "VELOCITY")
	f.project(5, "luckperms", "PAPER")
	f.version(5, "1.0", "PAPER")
	f.version(5, "2.5", "PAPER")

	mux.HandleFunc("GET /api/v1/projects", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		found := slices.DeleteFunc(slices.Clone(f.projects), func(p map[string]any) bool {
			_, supported := p["supportedPlatforms"].(map[string]any)[q.Get("platform")]
			return q.Get("platform") != "" && !supported || !strings.Contains(strings.ToLower(p["name"].(string)), strings.ToLower(q.Get("query")))
		})
		writeJSON(w, map[string]any{"pagination": map[string]any{"count": len(found)}, "result": found})
	})
	mux.HandleFunc("GET /api/v1/projects/{id}", func(w http.ResponseWriter, r *http.Request) {
		if p := f.find(r.PathValue("id")); p != nil {
			writeJSON(w, p)
			return
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("GET /api/v1/projects/{id}/versions", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var found []map[string]any
		for _, v := range slices.Backward(f.versions) {
			platforms := v["platformDependencies"].(map[string]any)
			game, ok := platforms[q.Get("platform")].([]string)
			if fmt.Sprint(v["projectId"]) == r.PathValue("id") && ok && (q.Get("platformVersion") == "" || slices.Contains(game, q.Get("platformVersion"))) {
				found = append(found, v)
			}
		}
		writeJSON(w, map[string]any{"result": found})
	})
	mux.HandleFunc("GET /api/v1/versions/hash/{hash}", func(w http.ResponseWriter, r *http.Request) {
		for _, v := range f.versions {
			for _, d := range v["downloads"].(map[string]any) {
				if d.(map[string]any)["fileInfo"].(map[string]any)["sha256Hash"] == r.PathValue("hash") {
					writeJSON(w, f.find(fmt.Sprint(v["projectId"])))
					return
				}
			}
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("GET /cdn/", func(w http.ResponseWriter, r *http.Request) {
		data, ok := f.files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	})
	return f
}

func (f *fakeHangar) project(id int, name, platform string) {
	f.projects = append(f.projects, map[string]any{
		"id": id, "name": name, "namespace": map[string]string{"owner": "Owner", "slug": name}, "description": name + " for tests",
		"stats": map[string]int{"downloads": 100 - id, "stars": id}, "category": "world_management", "lastUpdated": time.Now(),
		"supportedPlatforms": map[string]any{platform: []string{"1.21.4"}}, "avatarUrl": fmt.Sprintf("%s/cdn/avatars/project/%d.webp?v=1", f.URL, id),
	})
}

// version adds a version, newer than the ones before, with a file <name>-<number>.jar for one
// platform, in the channel Release.
func (f *fakeHangar) version(project int, number, platform string, requires ...int) {
	f.versionIn("Release", project, number, platform, requires...)
}

// versionIn adds a version like version in a channel, e.g. Beta or Snapshot.
func (f *fakeHangar) versionIn(channel string, project int, number, platform string, requires ...int) {
	name := f.find(strconv.Itoa(project))["name"].(string)
	path := fmt.Sprintf("/cdn/plugins/%d/%s/%s/%s.jar", project, number, platform, name)
	content := []byte(name + " " + number)
	f.files[path] = content
	sum := sha256.Sum256(content)
	var deps []map[string]any
	for _, id := range requires {
		deps = append(deps, map[string]any{"name": "dependency", "projectId": id, "required": true})
	}
	deps = append(deps, map[string]any{"name": "Elsewhere", "projectId": nil, "required": true, "externalUrl": "https://example.com"})
	f.versions = append(f.versions, map[string]any{
		"id": len(f.versions) + 100, "projectId": project, "name": number, "createdAt": time.Date(2026, 2, len(f.versions)+1, 0, 0, 0, 0, time.UTC),
		"channel": map[string]string{"name": channel},
		"downloads": map[string]any{platform: map[string]any{
			"fileInfo":    map[string]any{"name": name + "-" + number + ".jar", "sizeBytes": len(content), "sha256Hash": hex.EncodeToString(sum[:])},
			"downloadUrl": f.URL + path,
		}},
		"pluginDependencies":   map[string]any{platform: deps},
		"platformDependencies": map[string]any{platform: []string{"1.21.3", "1.21.4"}},
	})
}

func (f *fakeHangar) find(id string) map[string]any {
	i := slices.IndexFunc(f.projects, func(p map[string]any) bool { return fmt.Sprint(p["id"]) == id })
	if i < 0 {
		return nil
	}
	return f.projects[i]
}

func TestHangar(t *testing.T) {
	m := startMaster(t)
	a := m.startAgent(t, "node-1")
	lobby := plugin.Ref(m.createServer(t, a, "Lobby", noryxv1.ServerType_SERVER_TYPE_PAPER, 25565))
	api := apiClient{t: t, url: m.panel(t).URL}
	file := func(name string) string {
		data, _ := os.ReadFile(filepath.Join(a.runtime.dir, lobby.ServerID, "plugins", name))
		return string(data)
	}

	// The search finds plugins of the server type, with icons through the master.
	var found struct {
		Hits []struct {
			ID      string   `json:"id"`
			Icon    string   `json:"icon"`
			Loaders []string `json:"loaders"`
		} `json:"hits"`
	}
	api.do("GET", "/api/plugins/search?source=hangar&type=paper&version=LATEST", nil, http.StatusOK, &found)
	if len(found.Hits) != 4 || found.Hits[0].ID != "hangar-1" || found.Hits[0].Icon != "/api/plugins/icons/hangar/1.webp" || found.Hits[0].Loaders[0] != "paper" {
		t.Fatalf("search = %+v", found)
	}
	res, err := http.Get(api.url + found.Hits[0].Icon)
	check(t, err)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/webp" {
		t.Fatalf("icon: %d %v", res.StatusCode, res.Header)
	}
	api.do("GET", "/api/plugins/search?source=hangar&type=velocity", nil, http.StatusOK, &found)
	if len(found.Hits) != 1 || found.Hits[0].ID != "hangar-4" {
		t.Fatalf("search for Velocity = %+v", found)
	}
	api.do("GET", "/api/plugins/search?source=hangar&kind=mods", nil, http.StatusOK, &found)
	if len(found.Hits) != 0 {
		t.Fatalf("search for mods = %+v", found)
	}

	// A chosen version is installed and recognised by its hash, with the newer release.
	install := func(projects []string, versions map[string]string) plugin.Result {
		t.Helper()
		var installed struct{ Results []plugin.Result }
		api.do("POST", "/api/plugins/install", map[string]any{"projects": projects, "versions": versions, "servers": []plugin.Ref{lobby}}, http.StatusOK, &installed)
		return installed.Results[0]
	}
	if r := install([]string{"hangar-1"}, map[string]string{"hangar-1": "hangar-100"}); r.Error != "" || file("Chunky-1.0.jar") != "Chunky 1.0" {
		t.Fatalf("result = %+v", r)
	}
	var listing plugin.Listing
	api.do("GET", "/api/nodes/"+lobby.NodeID+"/servers/"+lobby.ServerID+"/plugins", nil, http.StatusOK, &listing)
	if p := listing.Plugins; len(p) != 1 || p[0].Project == nil || p[0].Project.Title != "Chunky" || p[0].Version != "1.0" || p[0].Update != "2.0" {
		t.Fatalf("plugins = %+v", listing)
	}
	// Updating replaces the file and installs the plugins it requires from Hangar.
	if r := install([]string{"hangar-1"}, nil); r.Error != "" || len(r.Installed) != 2 || r.Installed[1].ProjectID != "hangar-2" {
		t.Fatalf("result = %+v", r)
	}
	if file("Chunky-1.0.jar") != "" || file("Chunky-2.0.jar") != "Chunky 2.0" || file("ChunkyBorder-1.0.jar") == "" {
		t.Fatal("the update didn't replace the old version or install the required plugin")
	}
	// Corrupted downloads are refused.
	if r := install([]string{"hangar-3"}, nil); !strings.Contains(r.Error, "corrupted") || file("Tampered-1.0.jar") != "" {
		t.Fatalf("result = %+v", r)
	}

	// A file on Modrinth and Hangar is replaced by a version from Hangar rather than kept beside it.
	if r := install([]string{"luckperms"}, map[string]string{"luckperms": "luckperms10"}); r.Error != "" || file("luckperms-1.0.jar") == "" {
		t.Fatalf("result = %+v", r)
	}
	if r := install([]string{"hangar-5"}, nil); r.Error != "" || file("luckperms-1.0.jar") != "" || file("luckperms-2.5.jar") != "luckperms 2.5" {
		t.Fatalf("result = %+v", r)
	}

	// Templates can hold plugins of Hangar.
	var tmpl struct {
		Plugins []plugin.Project `json:"plugins"`
	}
	api.do("POST", "/api/templates", map[string]any{"name": "Lobby", "type": "paper", "version": "LATEST", "memoryMb": 1024, "plugins": []string{"hangar-1"}},
		http.StatusCreated, &tmpl)
	if len(tmpl.Plugins) != 1 || tmpl.Plugins[0].Title != "Chunky" || tmpl.Plugins[0].Slug != "Owner/Chunky" {
		t.Fatalf("template = %+v", tmpl)
	}
}
