package modrinth

import (
	"bytes"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
)

func TestEveryServerTypeHasLoaders(t *testing.T) {
	for value, name := range noryxv1.ServerType_name {
		typ := noryxv1.ServerType(value)
		if loads := len(Loaders(typ)) > 0; loads != typ.Addons() {
			t.Errorf("%s: loaders %v, but loads plugins or mods %v", name, Loaders(typ), typ.Addons())
		}
	}
	if !slices.Contains(AllLoaders("mods"), "quilt") || slices.Contains(AllLoaders("plugins"), "quilt") {
		t.Error("Quilt doesn't load mods")
	}
}

func TestProjectOf(t *testing.T) {
	c := New(DefaultAPI, DefaultCDN)
	for url, want := range map[string]string{
		DefaultCDN + "data/AANobbMI/versions/IZskON6d/sodium.jar": "AANobbMI",
		DefaultCDN + "data/AANobbMI/icon.png":                     "",
		DefaultCDN + "data/../versions/IZskON6d/sodium.jar":       "",
		"https://example.com/data/AANobbMI/versions/x/sodium.jar": "",
	} {
		if got := c.ProjectOf(url); got != want {
			t.Errorf("ProjectOf(%q) = %q, want %q", url, got, want)
		}
	}
}

// fake serves recorded answers of Modrinth's API at /v2 and files of its CDN at /cdn/, and
// records the requests.
type fake struct {
	*httptest.Server
	mu       sync.Mutex
	requests []*http.Request
	files    map[string][]byte // answers by method and path, or files by path
}

func newFake(t *testing.T, answers map[string]string) (*fake, *Client) {
	f := &fake{files: map[string][]byte{}}
	for k, v := range answers {
		f.files[k] = []byte(v)
	}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r)
		data, ok := f.files[r.Method+" "+r.URL.Path]
		if !ok {
			data, ok = f.files[r.URL.Path]
		}
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(data) //nolint:errcheck // the client sees it
	}))
	t.Cleanup(f.Close)
	return f, New(f.URL+"/v2", f.URL+"/cdn/")
}

func (f *fake) serve(path string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[path] = data
}

// sent returns the requests so far.
func (f *fake) sent() []*http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

func (f *fake) last() *http.Request {
	sent := f.sent()
	return sent[len(sent)-1]
}

func sha512Hex(data []byte) string {
	sum := sha512.Sum512(data)
	return hex.EncodeToString(sum[:])
}

// Files are only downloaded from the CDN and used if their size fits and their SHA-512 hash
// is the one of the API.
func TestDownload(t *testing.T) {
	f, c := newFake(t, nil)
	jar := []byte("PK\x03\x04 a plugin")
	f.serve("/cdn/data/AANobbMI/versions/IZskON6d/sodium.jar", jar)
	file := func(url, hash string, size int64) File {
		file := File{URL: url, Filename: "sodium.jar", Size: size}
		file.Hashes.SHA512 = hash
		return file
	}
	cdn := f.URL + "/cdn/data/AANobbMI/versions/IZskON6d/sodium.jar"
	for _, hash := range []string{sha512Hex(jar), strings.ToUpper(sha512Hex(jar))} {
		if data, err := c.Download(t.Context(), file(cdn, hash, int64(len(jar)))); err != nil || !bytes.Equal(data, jar) {
			t.Errorf("download = %q, %v", data, err)
		}
	}
	sha256 := sha256.Sum256(jar)
	for _, tc := range []struct {
		name string
		file File
		want string
		sent bool // whether the request reaches the CDN
	}{
		{"a wrong hash", file(cdn, sha512Hex([]byte("other")), int64(len(jar))), "corrupted", true},
		{"a file elsewhere", file("https://example.com/sodium.jar", sha512Hex(jar), int64(len(jar))), "isn't on Modrinth's CDN", false},
		{"a path that starts like the CDN", file(f.URL+"/cdnx/sodium.jar", sha512Hex(jar), int64(len(jar))), "isn't on Modrinth's CDN", false},
		{"a large file", file(cdn, sha512Hex(jar), MaxFileSize+1), "larger than 256 MB", false},
		{"no hash", file(cdn, "", int64(len(jar))), "can't be downloaded", false},
		{"a SHA-256 hash", file(cdn, hex.EncodeToString(sha256[:]), int64(len(jar))), "can't be downloaded", false},
		{"a missing file", file(f.URL+"/cdn/data/AANobbMI/versions/x/gone.jar", sha512Hex(jar), 1), "doesn't exist", true},
	} {
		before := len(f.sent())
		_, err := c.Download(t.Context(), tc.file)
		if sent := len(f.sent()) > before; err == nil || !strings.Contains(err.Error(), tc.want) || sent != tc.sent {
			t.Errorf("%s: %v, sent %v", tc.name, err, sent)
		}
	}
	if c := New(DefaultAPI, DefaultCDN); !c.OnCDN(DefaultCDN+"data/x") || c.OnCDN("https://cdn.modrinth.com.example.com/data/x") {
		t.Error("only files of Modrinth's CDN are on it")
	}
}

// deadlines records how much time requests have left when they are sent.
type deadlines struct {
	mu   sync.Mutex
	left []time.Duration
}

func (d *deadlines) RoundTrip(r *http.Request) (*http.Response, error) {
	if at, ok := r.Context().Deadline(); ok {
		d.mu.Lock()
		d.left = append(d.left, time.Until(at))
		d.mu.Unlock()
	}
	return http.DefaultTransport.RoundTrip(r)
}

// Calls of the API get Timeout. Downloads get a second more for each 256 KiB of the file, so that
// large files only fail on links slower than 256 KiB/s, and files of unknown size the time of the
// largest.
func TestTimeouts(t *testing.T) {
	for size, want := range map[int64]time.Duration{
		0: Timeout + 1024*time.Second, 1: Timeout, 100 << 20: Timeout + 400*time.Second,
		MaxFileSize: Timeout + 1024*time.Second, MaxFileSize + 1: Timeout + 1024*time.Second,
	} {
		if got := DownloadTimeout(size); got != want {
			t.Errorf("DownloadTimeout(%d) = %v, want %v", size, got, want)
		}
	}
	f, c := newFake(t, map[string]string{"GET /v2/tag/game_version": "[]"})
	d := &deadlines{}
	c.http.Transport = d
	jar := []byte("PK\x03\x04 a plugin")
	f.serve("/cdn/data/AANobbMI/versions/IZskON6d/sodium.jar", jar)
	file := File{URL: f.URL + "/cdn/data/AANobbMI/versions/IZskON6d/sodium.jar", Filename: "sodium.jar", Size: 100 << 20}
	file.Hashes.SHA512 = sha512Hex(jar)
	if _, err := c.Download(t.Context(), file); err != nil {
		t.Fatal(err)
	}
	if err := c.call(t.Context(), http.MethodGet, "/tag/game_version", nil, &[]any{}); err != nil {
		t.Fatal(err)
	}
	near := func(left, want time.Duration) bool { return left <= want && left > want-time.Minute }
	if len(d.left) != 2 || !near(d.left[0], DownloadTimeout(100<<20)) || !near(d.left[1], Timeout) {
		t.Fatalf("time left of a download and a call: %v", d.left)
	}
}

// Icons come from the CDN with the media type of their name, which may only be an image that
// can't run scripts, and of up to 1 MB.
func TestIcon(t *testing.T) {
	f, c := newFake(t, nil)
	f.serve("/cdn/data/AANobbMI/icon.png", []byte("\x89PNG"))
	f.serve("/cdn/data/AANobbMI/large.webp", bytes.Repeat([]byte("x"), maxIconBytes+1))
	if data, typ, err := c.Icon(t.Context(), "AANobbMI/icon.png"); err != nil || typ != "image/png" || string(data) != "\x89PNG" {
		t.Errorf("icon = %q, %q, %v", data, typ, err)
	}
	if _, _, err := c.Icon(t.Context(), "AANobbMI/large.webp"); err == nil || !strings.Contains(err.Error(), "larger than 1 MB") {
		t.Errorf("large icon: %v", err)
	}
	for _, name := range []string{"AANobbMI/icon.svg", "AANobbMI/icon.html", "../AANobbMI/icon.png", "AANobbMI/../../icon.png", "icon.png", "AANobbMI/sub/icon.png"} {
		if _, _, err := c.Icon(t.Context(), name); err == nil {
			t.Errorf("icon %q fetched", name)
		}
		if got := c.IconName(f.URL + "/cdn/data/" + name); got != "" {
			t.Errorf("IconName of %q = %q", name, got)
		}
	}
	if got := c.IconName(f.URL + "/cdn/data/AANobbMI/icon.png"); got != "AANobbMI/icon.png" {
		t.Errorf("IconName = %q", got)
	}
	if got := c.IconName("https://example.com/data/AANobbMI/icon.png"); got != "" {
		t.Errorf("IconName of another host = %q", got)
	}
}

func TestAPI(t *testing.T) {
	f, c := newFake(t, map[string]string{
		"GET /v2/project/AANobbMI/version": `[
			{"id": "old", "project_id": "AANobbMI", "version_number": "0.5", "date_published": "2025-01-01T00:00:00Z", "files": []},
			{"id": "new", "project_id": "AANobbMI", "version_number": "0.6", "date_published": "2026-01-01T00:00:00Z",
			 "files": [{"url": "u1", "filename": "a.jar"}, {"url": "u2", "filename": "b.jar", "primary": true}]}]`,
		"GET /v2/tag/game_version": `[{"version": "1.21.10", "version_type": "release"}, {"version": "25w41a", "version_type": "snapshot"},
			{"version": "1.21.9", "version_type": "release"}]`,
		"GET /v2/search":         `{"hits": [{"project_id": "AANobbMI", "slug": "sodium", "title": "Sodium"}], "total_hits": 1}`,
		"GET /v2/version/broken": `<html>`,
	})
	ctx := t.Context()

	// Versions come newest first, with their primary file.
	versions, err := c.Versions(ctx, "AANobbMI", []string{"fabric"}, "1.21.10")
	if err != nil || len(versions) != 2 || versions[0].ID != "new" {
		t.Fatalf("versions = %+v, %v", versions, err)
	}
	if q := f.last().URL.Query(); q.Get("loaders") != `["fabric"]` || q.Get("game_versions") != `["1.21.10"]` || q.Get("include_changelog") != "false" {
		t.Errorf("query of versions = %v", q)
	}
	if file, ok := versions[0].File(); !ok || file.Filename != "b.jar" {
		t.Errorf("primary file = %+v", file)
	}
	if _, ok := versions[1].File(); ok {
		t.Error("a version without files has a file")
	}
	if file, _ := (Version{Files: []File{{Filename: "a.jar"}, {Filename: "b.jar"}}}).File(); file.Filename != "a.jar" {
		t.Errorf("file without a primary one = %+v", file)
	}

	// Malformed IDs never reach the API, and unknown projects are not found.
	before := len(f.sent())
	if _, err := c.Versions(ctx, "../search", nil, ""); !errors.Is(err, errUnknown) {
		t.Errorf("versions of a malformed ID: %v", err)
	}
	if _, err := c.Version(ctx, "a/b"); !errors.Is(err, errUnknown) || len(f.sent()) != before {
		t.Errorf("version of a malformed ID: %v", err)
	}
	if _, err := c.Version(ctx, "gone"); !errors.Is(err, errUnknown) {
		t.Errorf("unknown version: %v", err)
	}
	if _, err := c.Version(ctx, "broken"); err == nil || !strings.Contains(err.Error(), "can't be reached") {
		t.Errorf("an answer that isn't JSON: %v", err)
	}

	// Releases of Minecraft are asked for once an hour.
	for range 2 {
		if releases, err := c.Releases(ctx); err != nil || !slices.Equal(releases, []string{"1.21.10", "1.21.9"}) {
			t.Fatalf("releases = %v, %v", releases, err)
		}
	}
	if latest, err := c.LatestRelease(ctx); err != nil || latest != "1.21.10" {
		t.Errorf("latest release = %q, %v", latest, err)
	}
	if n := len(slices.DeleteFunc(f.sent(), func(r *http.Request) bool { return r.URL.Path != "/v2/tag/game_version" })); n != 1 {
		t.Errorf("asked for releases %d times", n)
	}

	// Searches find server-side projects; without a query, the most downloaded are relevant.
	res, err := c.Search(ctx, Search{Kind: "mods", Loaders: []string{"fabric", "quilt"}, Categories: []string{"optimization"}, ServerOnly: true, Offset: 20})
	if err != nil || res.Total != 1 || res.Hits[0].Slug != "sodium" {
		t.Fatalf("search = %+v, %v", res, err)
	}
	q := f.last().URL.Query()
	facets := `[["categories:fabric","categories:quilt"],["server_side:required","server_side:optional"],["project_type:mod"],` +
		`["categories:optimization"],["client_side:optional","client_side:unsupported"]]`
	if q.Get("facets") != facets || q.Get("index") != "downloads" || q.Get("offset") != "20" || q.Get("limit") != "20" {
		t.Errorf("query of search = %v", q)
	}
	if ua := f.last().Header.Get("User-Agent"); !strings.HasPrefix(ua, "QwikByte/noryx/") {
		t.Errorf("User-Agent %q", ua)
	}
	if _, err := c.Search(ctx, Search{Query: "sodium"}); err != nil || f.last().URL.Query().Get("index") != "relevance" {
		t.Errorf("search with a query: %v, %v", err, f.last().URL.Query())
	}
}

func TestSearchValid(t *testing.T) {
	for _, tc := range []struct {
		search Search
		want   bool
	}{
		{Search{}, true},
		{Search{Kind: "modpacks", Sort: "updated", Categories: []string{"economy", "game-mechanics"}}, true},
		{Search{Kind: "plugin"}, false},
		{Search{Sort: "random"}, false},
		{Search{Categories: []string{"Economy"}}, false},
		{Search{Categories: []string{`economy"]`}}, false},
		{Search{Categories: slices.Repeat([]string{"economy"}, 11)}, false},
	} {
		if got := tc.search.Valid(); got != tc.want {
			t.Errorf("%+v valid = %v", tc.search, got)
		}
	}
}
