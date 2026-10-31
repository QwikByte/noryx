package plugin

import (
	"errors"
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"testing"

	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/modrinth"
)

// newPins returns the pins of a new database with the nodes n1 and n2, and a service that
// keeps them.
func newPins(t *testing.T) (pins, *Service) {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, id := range []string{"n1", "n2"} {
		if _, err := db.Exec(`INSERT INTO nodes (id, name, address, created_at) VALUES (?, ?, 'host:7443', 0)`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	return pins{db}, &Service{pins: pins{db}}
}

// Servers keep projects at their version, and take others in channels besides releases; a
// server that moves keeps them, and a deleted one forgets them.
func TestPins(t *testing.T) {
	p, s := newPins(t)
	ctx := t.Context()
	lobby, game := Ref{"n1", "lobby"}, Ref{"n1", "game"}
	for _, err := range []error{
		p.set(ctx, lobby, "AANobbMI", true),
		p.set(ctx, lobby, "AANobbMI", true), // twice is once
		p.set(ctx, lobby, "hangar-31", true),
		p.set(ctx, lobby, "P7dR8mSH", false), // not pinned
		p.track(ctx, lobby, "gvQqBUqZ", "beta"),
		p.track(ctx, lobby, "gvQqBUqZ", "alpha"), // replaces beta
		p.track(ctx, lobby, "hangar-31", "beta"),
		p.track(ctx, lobby, "hangar-31", "release"), // the default, not kept
		p.set(ctx, game, "AANobbMI", true),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	k, err := p.of(ctx, lobby)
	if err != nil || !maps.Equal(k.pinned, map[string]bool{"AANobbMI": true, "hangar-31": true}) || !maps.Equal(k.channels, map[string]string{"gvQqBUqZ": "alpha"}) {
		t.Fatalf("lobby keeps %+v, %v", k, err)
	}
	if k.channel("gvQqBUqZ") != "alpha" || k.channel("AANobbMI") != "release" {
		t.Errorf("channels %q, %q", k.channel("gvQqBUqZ"), k.channel("AANobbMI"))
	}
	if all, err := p.all(ctx, nil); err != nil || len(all) != 2 || !all[game].pinned["AANobbMI"] {
		t.Errorf("all = %+v, %v", all, err)
	}

	for name, err := range map[string]error{
		"pin an invalid project":      p.set(ctx, lobby, "../x", true),
		"track an invalid project":    p.track(ctx, lobby, "a b", "beta"),
		"track in an unknown channel": p.track(ctx, lobby, "AANobbMI", "nightly"),
	} {
		var apiErr *httpapi.Error
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
			t.Errorf("%s: %v", name, err)
		}
	}

	// The lobby moves to n2, where it keeps what it kept; the game is deleted.
	if err := s.Move(ctx, "lobby", "n1", "n2"); err != nil {
		t.Fatal(err)
	}
	if err := s.Forget(ctx, "n1", "game"); err != nil {
		t.Fatal(err)
	}
	all, err := p.all(ctx, nil)
	moved := all[Ref{"n2", "lobby"}]
	if err != nil || len(all) != 1 || len(moved.pinned) != 2 || moved.channels["gvQqBUqZ"] != "alpha" {
		t.Errorf("after moving and deleting: %+v, %v", all, err)
	}
}

func TestChannels(t *testing.T) {
	versions := map[string]modrinth.Version{"release": {VersionType: "release"}, "beta": {VersionType: "beta"}, "alpha": {VersionType: "alpha"}}
	for channel, want := range map[string][]string{"release": {"release"}, "beta": {"release", "beta"}, "alpha": {"release", "beta", "alpha"}} {
		var took []string
		for _, typ := range Channels {
			if takes(channel, versions[typ]) {
				took = append(took, typ)
			}
		}
		if !slices.Equal(took, want) || !ValidChannel(channel) {
			t.Errorf("%s takes %v, want %v", channel, took, want)
		}
	}
	if ValidChannel("nightly") || ValidChannel("") {
		t.Error("only release, beta and alpha are channels")
	}
	got := inUse(kept{channels: map[string]string{"a": "beta"}}, kept{channels: map[string]string{"b": "beta", "c": "alpha"}}, kept{})
	if !slices.Equal(got, []string{"alpha", "beta", "release"}) {
		t.Errorf("channels in use = %v", got)
	}
	if err := checkChannels([]string{"a", "b"}, map[string]string{"a": "beta"}); err != nil {
		t.Error(err)
	}
	for name, channels := range map[string]map[string]string{"another project": {"c": "beta"}, "an unknown channel": {"a": "nightly"}} {
		if err := checkChannels([]string{"a", "b"}, channels); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	u := updates{}
	u.add("hash", "beta", modrinth.Version{ID: "v2"})
	if u.of("hash", "beta").ID != "v2" || u.of("hash", "release").ID != "" || u.of("other", "beta").ID != "" {
		t.Errorf("updates = %+v", u)
	}
}
