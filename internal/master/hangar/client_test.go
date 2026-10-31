package hangar

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QwikByte/noryx/internal/master/modrinth"
)

var jar = []byte("PK\x03\x04 a plugin of Hangar")

// fake answers as Hangar's API at /api/v1 and its CDN at /cdn/ do, with recorded answers, and
// records the requests.
type fake struct {
	*httptest.Server
	mu       sync.Mutex
	requests []*url.URL
}

const viaVersion = `{"id":31,"name":"ViaVersion","namespace":{"owner":"ViaVersion","slug":"ViaVersion"},
	"stats":{"downloads":1200000,"stars":900},"category":"protocols","description":"Newer clients on older servers",
	"lastUpdated":"2025-09-01T10:00:00Z","avatarUrl":"https://hangarcdn.papermc.io/avatars/project/31.webp?v=1",
	"supportedPlatforms":{"PAPER":["1.8-1.21.10"],"VELOCITY":["3.4"],"WATERFALL":["1.21"]}}`

func newFake(t *testing.T) (*fake, *Client) {
	f := &fake{}
	sum := sha256.Sum256(jar)
	var cdn string
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.URL)
		f.mu.Unlock()
		var answer string
		switch r.URL.Path {
		case "/api/v1/projects":
			answer = `{"pagination":{"limit":20,"offset":0,"count":1},"result":[` + viaVersion + `]}`
		case "/api/v1/projects/31":
			answer = viaVersion
		case "/api/v1/versions/hash/" + hex.EncodeToString(sum[:]):
			answer = viaVersion
		case "/api/v1/projects/31/versions":
			answer = `{"result":[
				{"id":500,"projectId":31,"name":"5.4.0","description":"Fixes","createdAt":"2025-08-01T00:00:00Z","channel":{"name":"Release"},
				 "downloads":{"PAPER":{"fileInfo":{"name":"ViaVersion-5.4.0.jar","sizeBytes":` + strconv.Itoa(len(jar)) + `,"sha256Hash":"` + hex.EncodeToString(sum[:]) + `"},
				   "downloadUrl":"` + cdn + `plugins/ViaVersion/5.4.0/PAPER/ViaVersion-5.4.0.jar"}},
				 "pluginDependencies":{"PAPER":[{"name":"ViaBackwards","projectId":32,"required":true},{"name":"Elsewhere","required":true},
				   {"name":"Optional","projectId":33,"required":false}]},
				 "platformDependencies":{"PAPER":["1.21.9","1.21.10"]}},
				{"id":501,"projectId":31,"name":"5.5.0-SNAPSHOT","createdAt":"2025-09-01T00:00:00Z","channel":{"name":"Snapshot"},
				 "downloads":{"PAPER":{"fileInfo":null,"externalUrl":"https://example.com/via.jar"}}},
				{"id":499,"projectId":31,"name":"5.3.0-beta","createdAt":"2025-07-01T00:00:00Z","channel":{"name":"Beta"},"downloads":{}}]}`
		case "/cdn/plugins/ViaVersion/5.4.0/PAPER/ViaVersion-5.4.0.jar":
			answer = string(jar)
		case "/cdn/avatars/project/31.webp":
			answer = "RIFF webp"
		default:
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(answer)) //nolint:errcheck // the client sees it
	}))
	cdn = f.URL + "/cdn/"
	t.Cleanup(f.Close)
	return f, New(f.URL+"/api/v1", cdn)
}

func (f *fake) asked() []*url.URL {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

// Versions of Hangar take the shape of Modrinth's for the first platform of the loaders, the
// newest first, with the files only Hangar's CDN has and the dependencies on Hangar's projects.
func TestVersions(t *testing.T) {
	f, c := newFake(t)
	versions, err := c.Versions(t.Context(), "hangar-31", []string{"purpur", "paper"}, "1.21.10")
	if err != nil || len(versions) != 3 {
		t.Fatalf("versions = %+v, %v", versions, err)
	}
	if q := f.asked()[0].Query(); q.Get("platform") != "PAPER" || q.Get("platformVersion") != "1.21.10" || q.Get("limit") != "25" {
		t.Errorf("query = %v", q)
	}
	var got []string
	for _, v := range versions {
		got = append(got, v.ID+" "+v.VersionNumber+" "+v.VersionType+" "+strings.Join(v.Loaders, ","))
	}
	if want := []string{"hangar-501 5.5.0-SNAPSHOT alpha paper", "hangar-500 5.4.0 release paper", "hangar-499 5.3.0-beta beta paper"}; !slices.Equal(got, want) {
		t.Errorf("versions = %q, want %q", got, want)
	}
	release := versions[1]
	file, ok := release.File()
	sum := sha256.Sum256(jar)
	if !ok || file.Filename != "ViaVersion-5.4.0.jar" || file.Size != int64(len(jar)) || file.Hashes.SHA256 != hex.EncodeToString(sum[:]) ||
		!c.OnCDN(file.URL) || release.ProjectID != "hangar-31" || release.Changelog != "Fixes" || !slices.Equal(release.GameVersions, []string{"1.21.9", "1.21.10"}) {
		t.Errorf("release = %+v", release)
	}
	if len(release.Dependencies) != 1 || release.Dependencies[0] != (modrinth.Dependency{ProjectID: "hangar-32", Type: "required"}) {
		t.Errorf("dependencies = %+v", release.Dependencies)
	}
	if len(versions[0].Files) != 0 {
		t.Errorf("a version elsewhere has files: %+v", versions[0].Files)
	}

	// Proxies have no Minecraft versions to narrow by, and unknown projects and platforms
	// don't reach Hangar.
	if _, err := c.Versions(t.Context(), "hangar-31", []string{"velocity"}, "1.21.10"); err != nil || f.asked()[1].Query().Get("platformVersion") != "" {
		t.Errorf("versions for Velocity: %v, %v", err, f.asked()[1].Query())
	}
	for _, tc := range []struct {
		project string
		loaders []string
	}{
		{"hangar-31", []string{"fabric"}},
		{"31", []string{"paper"}},
		{"hangar-../31", []string{"paper"}},
	} {
		if _, err := c.Versions(t.Context(), tc.project, tc.loaders, ""); !errors.Is(err, errUnknown) {
			t.Errorf("versions of %s for %v: %v", tc.project, tc.loaders, err)
		}
	}
	if n := len(f.asked()); n != 2 {
		t.Errorf("asked Hangar %d times", n)
	}
}

func TestSearchAndProjects(t *testing.T) {
	f, c := newFake(t)
	ctx := t.Context()
	res, err := c.Search(ctx, modrinth.Search{Loaders: []string{"paper", "spigot"}, GameVersion: "1.21.10", Offset: 20})
	if err != nil || res.Total != 1 || len(res.Hits) != 1 {
		t.Fatalf("search = %+v, %v", res, err)
	}
	hit := res.Hits[0]
	if hit.ProjectID != "hangar-31" || hit.Slug != "ViaVersion/ViaVersion" || hit.Author != "ViaVersion" || hit.Follows != 900 ||
		!slices.Equal(hit.Categories, []string{"bungeecord", "paper", "velocity", "waterfall"}) || hit.ClientSide != "unsupported" ||
		!hit.Updated.Equal(time.Date(2025, 9, 1, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("hit = %+v", hit)
	}
	if q := f.asked()[0].Query(); q.Get("platform") != "PAPER" || q.Get("version") != "1.21.10" || q.Get("sort") != "-downloads" || q.Get("offset") != "20" {
		t.Errorf("query = %v", q)
	}
	// Several platforms aren't narrowed to one; a query is sorted by relevance unless asked.
	if _, err := c.Search(ctx, modrinth.Search{Query: "via", Loaders: []string{"velocity", "waterfall"}}); err != nil {
		t.Fatal(err)
	}
	if q := f.asked()[1].Query(); q.Has("platform") || q.Has("sort") || q.Get("query") != "via" {
		t.Errorf("query = %v", q)
	}
	if _, err := c.Search(ctx, modrinth.Search{Query: "via", Loaders: []string{"velocity"}, Sort: "follows", GameVersion: "3.4"}); err != nil {
		t.Fatal(err)
	}
	if q := f.asked()[2].Query(); q.Get("sort") != "-stars" || q.Has("version") {
		t.Errorf("query = %v", q)
	}
	// Hangar has no mods, nor plugins of other loaders.
	for _, s := range []modrinth.Search{{Kind: "mods", Loaders: []string{"paper"}}, {Kind: "modpacks", Loaders: []string{"paper"}}, {Loaders: []string{"fabric"}}} {
		if res, err := c.Search(ctx, s); err != nil || len(res.Hits) != 0 {
			t.Errorf("search %+v = %+v, %v", s, res, err)
		}
	}
	if n := len(f.asked()); n != 3 {
		t.Errorf("asked Hangar %d times", n)
	}

	// Projects are asked for once, and unknown ones are left out.
	for range 2 {
		projects, err := c.Projects(ctx, []string{"hangar-31", "hangar-404", "modrinth-id"})
		if err != nil || len(projects) != 1 || projects[0].Title != "ViaVersion" || !slices.Equal(projects[0].Loaders, []string{"bungeecord", "paper", "velocity", "waterfall"}) {
			t.Fatalf("projects = %+v, %v", projects, err)
		}
	}
	if n := len(slices.DeleteFunc(f.asked(), func(u *url.URL) bool { return u.Path != "/api/v1/projects/31" })); n != 1 {
		t.Errorf("asked for the project %d times", n)
	}
	sum := sha256.Sum256(jar)
	if id, err := c.ProjectByHash(ctx, hex.EncodeToString(sum[:])); err != nil || id != "hangar-31" {
		t.Errorf("ProjectByHash = %q, %v", id, err)
	}
	for _, hash := range []string{strings.Repeat("0", 64), "short", ""} {
		if id, err := c.ProjectByHash(ctx, hash); err != nil || id != "" {
			t.Errorf("ProjectByHash(%q) = %q, %v", hash, id, err)
		}
	}
}

// Files are only downloaded from the CDN and used if their size fits and their SHA-256 hash
// is the one of the API.
func TestDownloadAndIcon(t *testing.T) {
	f, c := newFake(t)
	ctx := t.Context()
	versions, err := c.Versions(ctx, "hangar-31", []string{"paper"}, "")
	if err != nil {
		t.Fatal(err)
	}
	file, _ := versions[1].File()
	if data, err := c.Download(ctx, file); err != nil || !bytes.Equal(data, jar) {
		t.Fatalf("download = %q, %v", data, err)
	}
	other := file
	other.Hashes.SHA256 = strings.Repeat("0", 64)
	if _, err := c.Download(ctx, other); err == nil || !strings.Contains(err.Error(), "corrupted") {
		t.Errorf("a file of another hash: %v", err)
	}
	before := len(f.asked())
	for name, edit := range map[string]func(f *modrinth.File){
		"elsewhere":  func(f *modrinth.File) { f.URL = "https://example.com/via.jar" },
		"too large":  func(f *modrinth.File) { f.Size = modrinth.MaxFileSize + 1 },
		"no hash":    func(f *modrinth.File) { f.Hashes.SHA256 = "" },
		"a SHA-512":  func(f *modrinth.File) { f.Hashes.SHA256, f.Hashes.SHA512 = "", strings.Repeat("0", 128) },
		"a CDN path": func(f *modrinth.File) { f.URL = strings.Replace(f.URL, "/cdn/", "/cdnx/", 1) },
	} {
		file := file
		edit(&file)
		if _, err := c.Download(ctx, file); err == nil || !strings.Contains(err.Error(), "can't be downloaded") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if asked := f.asked()[before:]; len(asked) != 0 {
		t.Errorf("asked for %v", asked)
	}

	if name := c.IconName(f.URL + "/cdn/avatars/project/31.webp?v=1"); name != "31.webp" {
		t.Errorf("IconName = %q", name)
	}
	if data, typ, err := c.Icon(ctx, "31.webp"); err != nil || typ != "image/webp" || string(data) != "RIFF webp" {
		t.Errorf("icon = %q, %q, %v", data, typ, err)
	}
	for _, name := range []string{"31.svg", "../31.webp", "x.png", "31.webp/x"} {
		if _, _, err := c.Icon(ctx, name); err == nil {
			t.Errorf("icon %q fetched", name)
		}
		if got := c.IconName(f.URL + "/cdn/avatars/project/" + name); got != "" {
			t.Errorf("IconName of %q = %q", name, got)
		}
	}
	if got := c.IconName("https://example.com/avatars/project/31.webp"); got != "" {
		t.Errorf("IconName of another host = %q", got)
	}
}

func TestPlatformAndIDs(t *testing.T) {
	for _, tc := range []struct {
		loaders []string
		want    string
	}{
		{[]string{"purpur", "paper", "spigot"}, "PAPER"},
		{[]string{"bungeecord"}, "WATERFALL"},
		{[]string{"velocity", "paper"}, "VELOCITY"},
		{[]string{"fabric", "folia"}, ""},
	} {
		if got := Platform(tc.loaders); got != tc.want {
			t.Errorf("Platform(%v) = %q", tc.loaders, got)
		}
	}
	for id, want := range map[string]bool{"hangar-31": true, "hangar-": false, "hangar-x": false, "31": false, "hangar-1234567890123": false} {
		if ValidID(id) != want {
			t.Errorf("ValidID(%q) = %v", id, !want)
		}
	}
}
