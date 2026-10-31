package geysermc

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QwikByte/noryx/internal/master/modrinth"
)

var jar = []byte("PK\x03\x04 floodgate")

// fake answers as GeyserMC's download server at /v2 and its global API at /global do, and
// records the paths asked for.
type fake struct {
	*httptest.Server
	mu    sync.Mutex
	paths []string
}

func newFake(t *testing.T, cache time.Duration) (*fake, *Client) {
	sum := sha256.Sum256(jar)
	answers := map[string]string{
		"/v2/projects/floodgate/versions/latest/builds/latest": `{"project_id":"floodgate","version":"2.2.4","build":116,"time":"2025-06-01T12:00:00Z",
			"channel":"default","promoted":false,"changes":[],"downloads":{
			"velocity":{"name":"floodgate-velocity.jar","sha256":"` + hex.EncodeToString(sum[:]) + `"},
			"bungee":{"name":"floodgate-bungee.jar","sha256":"` + strings.Repeat("0", 64) + `"}}}`,
		"/v2/projects/floodgate/versions/2.2.4/builds/116/downloads/velocity": string(jar),
		"/v2/projects/floodgate/versions/2.2.4/builds/116/downloads/bungee":   string(jar),
		"/global/xbox/xuid/Bedrock Steve":                                     `{"xuid":2535432196048835}`,
		"/global/skin/2535432196048835":                                       `{"hash":"x","is_steve":false,"texture_id":"a1b2c3","value":"x","signature":"x"}`,
		"/global/xbox/xuid/Odd Skin":                                          `{"xuid":2535432196048836}`,
		"/global/skin/2535432196048836":                                       `{"texture_id":"../../x"}`,
	}
	f := &fake{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.Path)
		f.mu.Unlock()
		answer, ok := answers[r.URL.Path]
		switch {
		case strings.HasPrefix(r.URL.Path, "/global/xbox/xuid/") && !ok:
			// GeyserMC answers players it doesn't know with an error.
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"message":"Unable to find user in our cache. Please try again later"}`)) //nolint:errcheck // the client sees it
		case !ok:
			http.NotFound(w, r)
		default:
			w.Write([]byte(answer)) //nolint:errcheck // the client sees it
		}
	}))
	t.Cleanup(f.Close)
	return f, New(f.URL+"/v2", f.URL+"/global", cache)
}

func (f *fake) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.paths...)
}

// The newest build of Floodgate takes the shape of a Modrinth version for the proxy's platform.
func TestLatest(t *testing.T) {
	f, c := newFake(t, time.Hour)
	ctx := t.Context()
	versions, err := c.Latest(ctx, Floodgate, []string{"paper", "velocity"})
	if err != nil || len(versions) != 1 {
		t.Fatalf("latest = %+v, %v", versions, err)
	}
	v := versions[0]
	file, _ := v.File()
	sum := sha256.Sum256(jar)
	if v.ID != "geysermc-floodgate-2.2.4-b116" || !ValidID(v.ID) || v.ProjectID != Floodgate || v.VersionNumber != "2.2.4-b116" || v.VersionType != "release" ||
		len(v.Loaders) != 1 || v.Loaders[0] != "velocity" || !v.Published.Equal(time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)) ||
		file.URL != f.URL+"/v2/projects/floodgate/versions/2.2.4/builds/116/downloads/velocity" || file.Filename != "floodgate-velocity.jar" ||
		file.Hashes.SHA256 != hex.EncodeToString(sum[:]) || file.Hashes.SHA512 != "" {
		t.Errorf("version = %+v", v)
	}
	// Waterfall runs the build for BungeeCord, which is known for a while.
	if versions, err := c.Latest(ctx, Floodgate, []string{"waterfall"}); err != nil || versions[0].Loaders[0] != "waterfall" || !strings.HasSuffix(versions[0].Files[0].URL, "/bungee") {
		t.Errorf("latest for Waterfall = %+v, %v", versions, err)
	}
	if n := len(f.asked()); n != 1 {
		t.Errorf("asked %d times for the newest build", n)
	}
	for _, tc := range []struct {
		project string
		loaders []string
	}{
		{Prefix + "geyser", []string{"velocity"}},
		{"floodgate", []string{"velocity"}},
		{Floodgate, []string{"paper", "fabric"}},
	} {
		if versions, err := c.Latest(ctx, tc.project, tc.loaders); versions != nil || err != nil {
			t.Errorf("latest of %s for %v = %+v, %v", tc.project, tc.loaders, versions, err)
		}
	}
	if n := len(f.asked()); n != 1 {
		t.Errorf("asked GeyserMC %d times", n)
	}

	// Without a cache, every call asks again.
	f, c = newFake(t, 0)
	for range 2 {
		if _, err := c.Latest(ctx, Floodgate, []string{"velocity"}); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(f.asked()); n != 2 {
		t.Errorf("asked %d times without a cache", n)
	}
}

// Files are only downloaded from the download server and used if their SHA-256 hash is the
// one of the build.
func TestDownload(t *testing.T) {
	f, c := newFake(t, time.Hour)
	versions, err := c.Latest(t.Context(), Floodgate, []string{"velocity"})
	if err != nil {
		t.Fatal(err)
	}
	file, _ := versions[0].File()
	if data, err := c.Download(t.Context(), file); err != nil || !bytes.Equal(data, jar) {
		t.Fatalf("download = %q, %v", data, err)
	}
	bungee, err := c.Latest(t.Context(), Floodgate, []string{"bungeecord"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Download(t.Context(), bungee[0].Files[0]); err == nil || !strings.Contains(err.Error(), "corrupted") {
		t.Errorf("a file of another hash: %v", err)
	}
	before := len(f.asked())
	for name, edit := range map[string]func(f *modrinth.File){
		"elsewhere":           func(f *modrinth.File) { f.URL = "https://example.com/floodgate.jar" },
		"a similar prefix":    func(f *modrinth.File) { f.URL = strings.Replace(f.URL, "/v2/", "/v2x/", 1) },
		"no hash":             func(f *modrinth.File) { f.Hashes.SHA256 = "" },
		"a hash of Modrinth":  func(f *modrinth.File) { f.Hashes.SHA256, f.Hashes.SHA512 = "", strings.Repeat("0", 128) },
		"a hash of 63 digits": func(f *modrinth.File) { f.Hashes.SHA256 = f.Hashes.SHA256[1:] },
	} {
		file := file
		edit(&file)
		if _, err := c.Download(t.Context(), file); err == nil || !strings.Contains(err.Error(), "can't be downloaded") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if asked := f.asked()[before:]; len(asked) != 0 {
		t.Errorf("asked for %q", asked)
	}
}

// Floodgate gives Bedrock players their XUID as ID, which GeyserMC knows of players who joined
// a server with Geyser before.
func TestPlayers(t *testing.T) {
	_, c := newFake(t, time.Hour)
	ctx := t.Context()
	if id, err := c.PlayerID(ctx, "Bedrock Steve"); err != nil || id != "00000000-0000-0000-0009-01f64f65c7c3" {
		t.Errorf("PlayerID = %q, %v", id, err)
	}
	if _, err := c.PlayerID(ctx, "Nobody"); err == nil || !strings.Contains(err.Error(), "doesn't know the Bedrock player Nobody") {
		t.Errorf("PlayerID of an unknown player: %v", err)
	}
	for gamertag, want := range map[string]string{"Bedrock Steve": "a1b2c3", "Odd Skin": "", "Nobody": ""} {
		if got, err := c.SkinTexture(ctx, gamertag); err != nil || got != want {
			t.Errorf("SkinTexture(%q) = %q, %v", gamertag, got, err)
		}
	}
}

func TestPlatform(t *testing.T) {
	for _, tc := range []struct {
		loaders           []string
		platform, matched string
	}{
		{[]string{"velocity"}, "velocity", "velocity"},
		{[]string{"paper", "bungeecord", "velocity"}, "bungee", "bungeecord"},
		{[]string{"waterfall"}, "bungee", "waterfall"},
		{[]string{"paper", "fabric"}, "", ""},
	} {
		if p, l := Platform(tc.loaders); p != tc.platform || l != tc.matched {
			t.Errorf("Platform(%v) = %q, %q", tc.loaders, p, l)
		}
	}
	for id, want := range map[string]bool{Floodgate: true, "geysermc-floodgate-2.2.4-b116": true, "floodgate": false, "geysermc-Floodgate": false, "geysermc-floodgate-a/b": false} {
		if ValidID(id) != want {
			t.Errorf("ValidID(%q) = %v", id, !want)
		}
	}
}
