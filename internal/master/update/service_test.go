package update

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QwikByte/noryx/internal/buildinfo"
)

// config are settings of the master.
type config struct {
	check  bool
	every  time.Duration
	follow bool
}

func (c config) CheckUpdates() bool                 { return c.check }
func (c config) UpdateCheckInterval() time.Duration { return c.every }
func (c config) UpdateAgentsAfterMaster() bool      { return c.follow }

// Without pre-releases, the latest release is the one GitHub calls so. With them, it is the
// highest version among the newest releases, without drafts and other tags, as the installer
// chooses it; while the API limits requests, the release page tells the latest release.
func TestPrereleases(t *testing.T) {
	var limited atomic.Bool
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/page":
			http.Redirect(w, r, buildinfo.Repository+"/releases/tag/v1.2.1", http.StatusFound)
		case limited.Load():
			w.WriteHeader(http.StatusForbidden)
		case r.URL.Path == "/releases/latest":
			fmt.Fprint(w, `{"tag_name": "v1.2.1", "body": "Stable"}`)
		case r.URL.Path == "/releases" && r.URL.RawQuery == "per_page=10":
			fmt.Fprint(w, `[{"tag_name": "v9.0.0", "draft": true}, {"tag_name": "nightly"}, {"tag_name": "v1.3.0-rc.2", "body": "Second"},
				{"tag_name": "v1.3.0-rc.10", "body": "Tenth"}, {"tag_name": "v1.2.1", "body": "Stable"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(github.Close)
	latest := func(prereleases bool) Release {
		s := New(nil, config{}, Options{API: github.URL + "/releases", Page: github.URL + "/page", Prereleases: prereleases})
		s.Check(t.Context())
		if s.latest == nil || s.checkErr != "" {
			t.Fatalf("pre-releases %t: latest %+v, error %q", prereleases, s.latest, s.checkErr)
		}
		return *s.latest
	}
	for prereleases, want := range map[bool]Release{false: {Version: "v1.2.1", Notes: "Stable"}, true: {Version: "v1.3.0-rc.10", Notes: "Tenth"}} {
		if got := latest(prereleases); got.Version != want.Version || got.Notes != want.Notes {
			t.Errorf("pre-releases %t: latest %+v, want %+v", prereleases, got, want)
		}
	}
	limited.Store(true)
	if got := latest(true); got.Version != "v1.2.1" || got.Notes != "" {
		t.Errorf("latest from the release page = %+v", got)
	}
}

// The master looks for releases once the interval of the settings passed since the last
// check, also one by hand, while the settings allow it.
func TestDue(t *testing.T) {
	s := New(nil, config{}, Options{})
	for _, tc := range []struct {
		config
		since time.Duration // since the last check, or never if 0
		want  bool
	}{
		{config{check: true, every: time.Hour}, 0, true},
		{config{check: true, every: time.Hour}, 59 * time.Minute, false},
		{config{check: true, every: time.Hour}, 61 * time.Minute, true},
		{config{check: true, every: 24 * time.Hour}, 61 * time.Minute, false},
		{config{every: time.Hour}, 0, false},
	} {
		s.config, s.checked = tc.config, time.Time{}
		if tc.since > 0 {
			s.checked = time.Now().Add(-tc.since)
		}
		if got := s.due(); got != tc.want {
			t.Errorf("due every %v, %v after the last check = %t", tc.every, tc.since, got)
		}
	}
}

// Without the settings letting the agents follow, the master leaves them as they are after it
// updated itself, and forgets the follow-up, so that a later start doesn't update them either.
func TestAgentsStay(t *testing.T) {
	dir := t.TempDir()
	followUp := filepath.Join(dir, "update-agents")
	if err := os.WriteFile(followUp, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Without nodes, updating the agents would panic. Run returns, as tests aren't a release.
	New(nil, config{check: true, every: time.Hour}, Options{DataDir: dir}).Run(t.Context())
	if _, err := os.Stat(followUp); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("follow-up: %v", err)
	}
}
