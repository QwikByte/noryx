package mojang

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const (
	notch = "069a79f444e94726a5befca90e38aaf5"
	skin  = "292009a4925b58f02c77dadc3ecef07ea4c7472f64e0fdc32ce5522489362680"
	// The textures of profiles, encoded as Mojang encodes them: Notch's skin, one on another
	// host, and none.
	notchTextures = "eyJ0aW1lc3RhbXAiOjE3MzAwMDAwMDAwMDAsInByb2ZpbGVJZCI6IjA2OWE3OWY0NDRlOTQ3MjZhNWJlZmNhOTBlMzhhYWY1IiwicHJvZmlsZU5hbWUiOiJOb3RjaCIs" +
		"InRleHR1cmVzIjp7IlNLSU4iOnsidXJsIjoiaHR0cDovL3RleHR1cmVzLm1pbmVjcmFmdC5uZXQvdGV4dHVyZS8yOTIwMDlhNDkyNWI1OGYwMmM3N2RhZGMzZWNlZjA3ZWE0" +
		"Yzc0NzJmNjRlMGZkYzMyY2U1NTIyNDg5MzYyNjgwIn19fQ=="
	elsewhereTextures = "eyJ0ZXh0dXJlcyI6eyJTS0lOIjp7InVybCI6Imh0dHBzOi8vZXZpbC5leGFtcGxlL3RleHR1cmUvMjkyMDA5YTQifX19"
	noTextures        = "eyJ0ZXh0dXJlcyI6e319"
)

// fake answers as Mojang's API, session server and textures server do, and records the paths
// asked for.
type fake struct {
	*httptest.Server
	mu    sync.Mutex
	paths []string
}

func newFake(t *testing.T) (*fake, *Client) {
	f := &fake{}
	session := func(id, textures string) string {
		return `{"id":"` + id + `","name":"x","properties":[{"name":"textures","value":"` + textures + `"}]}`
	}
	answers := map[string]string{
		"/api/users/profiles/minecraft/Notch":     `{"id":"` + notch + `","name":"Notch"}`,
		"/api/users/profiles/minecraft/Default":   `{"id":"00000000000000000000000000000001","name":"Default"}`,
		"/api/users/profiles/minecraft/Elsewhere": `{"id":"00000000000000000000000000000002","name":"Elsewhere"}`,
		"/api/users/profiles/minecraft/Broken":    `{"id":"00000000000000000000000000000003","name":"Broken"}`,
		"/api/users/profiles/minecraft/Odd":       `{"id":"../../x","name":"Odd"}`,

		"/session/session/minecraft/profile/" + notch:                         session(notch, notchTextures),
		"/session/session/minecraft/profile/00000000000000000000000000000001": session("1", noTextures),
		"/session/session/minecraft/profile/00000000000000000000000000000002": session("2", elsewhereTextures),
		"/session/session/minecraft/profile/00000000000000000000000000000003": session("3", "not base64!"),

		"/textures/" + skin: "\x89PNG skin",
		"/textures/abcdef":  strings.Repeat("x", MaxSkinBytes+1),
	}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.EscapedPath())
		f.mu.Unlock()
		switch answer, ok := answers[r.URL.Path]; {
		case r.URL.Path == "/api/users/profiles/minecraft/Gone":
			w.WriteHeader(http.StatusNoContent) // as Mojang answered unknown names before
		case r.URL.Path == "/api/users/profiles/minecraft/Down":
			w.WriteHeader(http.StatusServiceUnavailable)
		case !ok || r.Header.Get("User-Agent") == "":
			http.NotFound(w, r)
		default:
			w.Write([]byte(answer)) //nolint:errcheck // the client sees it
		}
	}))
	t.Cleanup(f.Close)
	return f, New(f.URL+"/api", f.URL+"/session", f.URL+"/textures/")
}

func (f *fake) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.paths...)
}

// The skin of a Java player is the texture of their profile, and only one of the textures
// server.
func TestSkinTexture(t *testing.T) {
	f, c := newFake(t)
	for _, tc := range []struct {
		name, want string
		fails      bool
	}{
		{"Notch", skin, false},
		{"Default", "", false},
		{"Elsewhere", "", false},
		{"Unknown", "", false},
		{"Gone", "", false},
		{"Odd", "", false},
		{"Broken", "", true},
		{"Down", "", true},
	} {
		got, err := c.SkinTexture(t.Context(), tc.name)
		if got != tc.want || (err != nil) != tc.fails {
			t.Errorf("SkinTexture(%s) = %q, %v", tc.name, got, err)
		}
	}
	for _, path := range f.asked() {
		if strings.Contains(path, "../") {
			t.Errorf("asked for %s", path)
		}
	}
	before := len(f.asked())
	if _, err := c.SkinTexture(t.Context(), "a/../b"); err != nil || f.asked()[before] != "/api/users/profiles/minecraft/a%2F..%2Fb" {
		t.Errorf("a name with slashes: %v, asked for %q", err, f.asked()[before:])
	}
}

func TestSkin(t *testing.T) {
	f, c := newFake(t)
	if data, err := c.Skin(t.Context(), skin); err != nil || !bytes.Equal(data, []byte("\x89PNG skin")) {
		t.Errorf("skin = %q, %v", data, err)
	}
	before := len(f.asked())
	for _, id := range []string{"", "../api/users", "ABC", strings.Repeat("a", 65)} {
		if _, err := c.Skin(t.Context(), id); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("skin %q: %v", id, err)
		}
	}
	if len(f.asked()) != before {
		t.Errorf("invalid IDs were asked for: %q", f.asked()[before:])
	}
	if _, err := c.Skin(t.Context(), "abc"); err == nil {
		t.Error("a missing skin was found")
	}
	if _, err := c.Skin(t.Context(), "abcdef"); err == nil || !strings.Contains(err.Error(), "larger than 256 KiB") {
		t.Errorf("a large skin: %v", err)
	}
	if !ValidTexture(skin) || ValidTexture("Z") {
		t.Error("ValidTexture")
	}
}
