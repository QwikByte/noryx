package e2e

import (
	"archive/zip"
	"bytes"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QwikByte/noryx/internal/master/modrinth"
)

// fakeModrinth serves the parts of Modrinth's API (/v2) and CDN (/cdn) the master uses:
//
//   - luckperms: a Paper plugin with the releases 1.0 and 2.0; 2.0 requires vault
//   - vault: a Paper plugin
//   - fabricapi: a Fabric mod
//   - 8dI2tmqs: FabricProxy-Lite, a Fabric mod that requires fabricapi
//   - broken: a Paper plugin whose download doesn't match its hash
//   - VCAqN1ln: Maintenance, a plugin for proxies
//
// Tests add modpacks with modpack.
type fakeModrinth struct {
	*httptest.Server
	projects []modrinth.Project
	packs    map[string]bool // the projects that are modpacks
	versions []modrinth.Version
	files    map[string][]byte          // by CDN path
	search   atomic.Pointer[url.Values] // the query of the last search
}

func startModrinth(t *testing.T) *fakeModrinth {
	f := &fakeModrinth{files: map[string][]byte{"/cdn/data/luckperms/icon.png": []byte("\x89PNG")}, packs: map[string]bool{}}
	mux := http.NewServeMux()
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	paper := []string{"paper", "spigot", "bukkit"}
	f.project("luckperms", "LuckPerms", paper)
	f.version("luckperms", "1.0", paper)
	f.version("luckperms", "2.0", paper, "vault")
	f.project("vault", "Vault", paper)
	f.version("vault", "1.7", paper)
	f.project("fabricapi", "Fabric API", []string{"fabric"})
	f.version("fabricapi", "0.119", []string{"fabric"})
	f.project("8dI2tmqs", "FabricProxy-Lite", []string{"quilt", "fabric"}) // found by searches for Quilt, not Fabric
	f.version("8dI2tmqs", "2.10", []string{"fabric"}, "fabricapi")
	f.project("broken", "Broken", paper)
	f.version("broken", "1.0", paper)
	f.files["/cdn/data/broken/versions/broken10/broken-1.0.jar"] = []byte("tampered")
	proxies := []string{"velocity", "bungeecord", "waterfall"}
	f.project("VCAqN1ln", "Maintenance", proxies)
	f.version("VCAqN1ln", "5.1.0", proxies)
	// Geyser keeps the name of its file, and each proxy has its own version on Modrinth.
	f.project("wKkoqHrH", "Geyser", proxies)
	f.release("wKkoqHrH", "2.11.3-velocity", "Geyser-Velocity.jar", []byte("geyser velocity"), []string{"velocity"})
	f.release("wKkoqHrH", "2.11.3-bungeecord", "Geyser-BungeeCord.jar", []byte("geyser bungeecord"), []string{"bungeecord"})

	mux.HandleFunc("GET /v2/search", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f.search.Store(&q)
		hits := []modrinth.SearchHit{}
		for _, p := range f.projects {
			facets := r.URL.Query().Get("facets")
			if strings.Contains(facets, "categories:"+p.Loaders[0]) && strings.Contains(facets, "project_type:modpack") == f.packs[p.ID] {
				hits = append(hits, modrinth.SearchHit{ProjectID: p.ID, Slug: p.Slug, Title: p.Title, IconURL: p.IconURL, Categories: p.Loaders})
			}
		}
		writeJSON(w, map[string]any{"hits": hits, "total_hits": len(hits)})
	})
	mux.HandleFunc("GET /v2/projects", func(w http.ResponseWriter, r *http.Request) {
		var ids []string
		_ = json.Unmarshal([]byte(r.URL.Query().Get("ids")), &ids)
		writeJSON(w, slices.DeleteFunc(slices.Clone(f.projects), func(p modrinth.Project) bool { return !slices.Contains(ids, p.ID) }))
	})
	mux.HandleFunc("GET /v2/project/{id}/version", func(w http.ResponseWriter, r *http.Request) {
		var loaders []string
		_ = json.Unmarshal([]byte(r.URL.Query().Get("loaders")), &loaders)
		found := f.find(func(v modrinth.Version) bool { return v.ProjectID == r.PathValue("id") && overlaps(v.Loaders, loaders) })
		if r.URL.Query().Get("include_changelog") != "true" {
			for i := range found {
				found[i].Changelog = ""
			}
		}
		writeJSON(w, found)
	})
	mux.HandleFunc("GET /v2/version/{id}", func(w http.ResponseWriter, r *http.Request) {
		found := f.find(func(v modrinth.Version) bool { return v.ID == r.PathValue("id") })
		if len(found) == 0 {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, found[0])
	})
	mux.HandleFunc("POST /v2/version_files", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, f.byHash(r, func(found modrinth.Version, _, _ []string) (modrinth.Version, bool) { return found, true }))
	})
	mux.HandleFunc("POST /v2/version_files/update", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, f.byHash(r, func(found modrinth.Version, loaders, types []string) (modrinth.Version, bool) {
			newer := f.find(func(v modrinth.Version) bool {
				return v.ProjectID == found.ProjectID && overlaps(v.Loaders, loaders) && slices.Contains(types, v.VersionType)
			})
			if len(newer) == 0 {
				return found, false
			}
			return newer[0], true
		}))
	})
	mux.HandleFunc("GET /v2/tag/game_version", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, []map[string]string{{"version": "1.22-pre1", "version_type": "snapshot"}, {"version": "1.21.4", "version_type": "release"}})
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

func (f *fakeModrinth) project(id, title string, loaders []string) {
	f.projects = append(f.projects, modrinth.Project{ID: id, Slug: id, Title: title, Loaders: loaders, IconURL: f.URL + "/cdn/data/" + id + "/icon.png"})
}

// version adds a version, newer than the ones before, with a file named <project>-<number>.jar.
func (f *fakeModrinth) version(project, number string, loaders []string, requires ...string) {
	f.release(project, number, project+"-"+number+".jar", []byte(project+" "+number), loaders, requires...)
}

// preRelease adds a version like version in a channel, beta or alpha.
func (f *fakeModrinth) preRelease(channel, project, number string, loaders []string) {
	f.version(project, number, loaders)
	f.versions[len(f.versions)-1].VersionType = channel
}

// release adds a version, newer than the ones before, with a file on the CDN at
// /cdn/data/<project>/versions/<version>/<name>, like Modrinth's.
func (f *fakeModrinth) release(project, number, name string, content []byte, loaders []string, requires ...string) modrinth.Version {
	id := project + strings.ReplaceAll(number, ".", "")
	path := "/cdn/data/" + project + "/versions/" + id + "/" + name
	file := modrinth.File{URL: f.URL + path, Filename: name, Primary: true, Size: int64(len(content))}
	file.Hashes.SHA512 = sha512Hex(content)
	f.files[path] = content
	v := modrinth.Version{
		ID: id, ProjectID: project, VersionNumber: number, VersionType: "release",
		Published: time.Date(2026, 1, len(f.versions)+1, 0, 0, 0, 0, time.UTC), Files: []modrinth.File{file}, Loaders: loaders,
		GameVersions: []string{"1.21.4"}, Changelog: "Changes in " + number,
	}
	for _, dep := range requires {
		v.Dependencies = append(v.Dependencies, modrinth.Dependency{ProjectID: dep, Type: "required"})
	}
	f.versions = append(f.versions, v)
	return v
}

// modpack adds a Fabric modpack with a version whose .mrpack holds the index and the files.
func (f *fakeModrinth) modpack(t *testing.T, project string, index any, files map[string]string) modrinth.Version {
	return f.modpackVersion(t, project, "1.0", index, files)
}

// modpackVersion adds a version of a Fabric modpack, and the modpack unless it exists.
func (f *fakeModrinth) modpackVersion(t *testing.T, project, number string, index any, files map[string]string) modrinth.Version {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	data, err := json.Marshal(index)
	check(t, err)
	files["modrinth.index.json"] = string(data)
	for name, content := range files {
		w, err := zw.Create(name)
		check(t, err)
		_, err = w.Write([]byte(content))
		check(t, err)
	}
	check(t, zw.Close())
	if !f.packs[project] {
		f.project(project, project, []string{"fabric"})
		f.packs[project] = true
	}
	return f.release(project, number, project+"-"+number+".mrpack", buf.Bytes(), []string{"fabric"})
}

func sha512Hex(data []byte) string {
	sum := sha512.Sum512(data)
	return hex.EncodeToString(sum[:])
}

// content returns the file of a version, e.g. content("luckperms", "1.0").
func (f *fakeModrinth) content(project, number string) []byte {
	id := project + strings.ReplaceAll(number, ".", "")
	return f.files["/cdn/data/"+project+"/versions/"+id+"/"+project+"-"+number+".jar"]
}

// find returns the matching versions, the newest first.
func (f *fakeModrinth) find(match func(modrinth.Version) bool) []modrinth.Version {
	found := slices.DeleteFunc(slices.Clone(f.versions), func(v modrinth.Version) bool { return !match(v) })
	slices.Reverse(found)
	return found
}

// byHash answers a lookup of files by hash with the version that pick chooses.
func (f *fakeModrinth) byHash(r *http.Request, pick func(found modrinth.Version, loaders, types []string) (modrinth.Version, bool)) map[string]modrinth.Version {
	var req struct {
		Hashes  []string `json:"hashes"`
		Loaders []string `json:"loaders"`
		Types   []string `json:"version_types"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	res := map[string]modrinth.Version{}
	for _, hash := range req.Hashes {
		found := f.find(func(v modrinth.Version) bool { return v.Files[0].Hashes.SHA512 == hash })
		if len(found) == 0 {
			continue
		}
		if v, ok := pick(found[0], req.Loaders, req.Types); ok {
			res[hash] = v
		}
	}
	return res
}

func overlaps(a, b []string) bool {
	return slices.ContainsFunc(a, func(s string) bool { return slices.Contains(b, s) })
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
