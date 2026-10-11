package plugin

import (
	"cmp"
	"slices"
	"testing"
	"time"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/modrinth"
)

func TestPick(t *testing.T) {
	version := func(id, channel string) modrinth.Version {
		return modrinth.Version{ID: id, VersionType: channel, Files: []modrinth.File{{Filename: id + ".jar"}}}
	}
	release, beta, alpha := version("release", "release"), version("beta", "beta"), version("alpha", "alpha")
	paper := target{loaders: []string{"paper"}, gameVersion: "26.2"}
	for _, tc := range []struct {
		name     string
		versions []modrinth.Version // the newest first
		chosen   string
		in       string // the channel, release if empty
		strict   bool
		want     string // the ID of the version, or the error
		channel  string
	}{
		{name: "newest release", versions: []modrinth.Version{beta, release}, want: "release"},
		{name: "newest pre-release without a release", versions: []modrinth.Version{alpha, beta}, want: "alpha", channel: "alpha"},
		{name: "chosen pre-release", versions: []modrinth.Version{beta, release}, chosen: "beta", want: "beta", channel: "beta"},
		{name: "chosen version for another server", versions: []modrinth.Version{release}, chosen: "other", want: "The chosen version of ViaVersion doesn't run on Paper 26.2."},
		{name: "no version", want: "ViaVersion has no version for Paper 26.2."},
		{name: "update to the newest release", versions: []modrinth.Version{beta, release}, strict: true, want: "release"},
		{name: "update without a release", versions: []modrinth.Version{alpha, beta}, strict: true, want: "ViaVersion has no release for Paper 26.2."},
		{name: "newest beta", versions: []modrinth.Version{alpha, beta, release}, in: "beta", want: "beta", channel: "beta"},
		{name: "release newer than the betas", versions: []modrinth.Version{release, beta}, in: "beta", want: "release"},
		{name: "newest alpha", versions: []modrinth.Version{alpha, beta, release}, in: "alpha", want: "alpha", channel: "alpha"},
		{name: "pre-release without a beta", versions: []modrinth.Version{alpha}, in: "beta", want: "alpha", channel: "alpha"},
		{name: "update without a beta", versions: []modrinth.Version{alpha}, in: "beta", strict: true, want: "ViaVersion has no release or beta for Paper 26.2."},
		{name: "unknown channel", versions: []modrinth.Version{release}, in: "nightly", strict: true, want: "ViaVersion has no version for Paper 26.2."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &installation{chosen: map[string]string{"via": tc.chosen}}
			// Looked up once per installation; set here, the catalogue isn't asked.
			_, _ = r.versions.get("via|paper|26.2", func() ([]modrinth.Version, error) { return tc.versions, nil })
			_, _ = r.titles.get("via", func() (string, error) { return "ViaVersion", nil })
			v, err := r.pick(t.Context(), "via", paper, cmp.Or(tc.in, "release"), tc.strict)
			got := v.ID
			if err != nil {
				got = httpapi.Message(err)
			}
			if got != tc.want || preRelease(v) != tc.channel {
				t.Fatalf("pick = %q (channel %q), want %q (channel %q)", got, preRelease(v), tc.want, tc.channel)
			}
		})
	}
}

// A listing tells which projects require a plugin, from the dependencies of the turned-on
// ones, links to the folder of its settings only if the name is a plain folder, and offers the
// update of the channel the server takes it in.
func TestDescribe(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 1, d, 0, 0, 0, 0, time.UTC) }
	c := catalogued{
		known: map[string]modrinth.Version{
			"vault": {ID: "vault1", ProjectID: "vault", VersionNumber: "1.7", VersionType: "release", Published: day(1)},
			"lp":    {ID: "lp1", ProjectID: "lp", VersionNumber: "1.0", VersionType: "beta", Published: day(1), Dependencies: []modrinth.Dependency{{ProjectID: "vault", Type: "required"}}},
			"eco":   {ID: "eco1", ProjectID: "eco", VersionNumber: "3", Published: day(1), Dependencies: []modrinth.Dependency{{VersionID: "vault1", Type: "required"}, {ProjectID: "lp", Type: "optional"}}},
			"off":   {ID: "off1", ProjectID: "off", Published: day(1), Dependencies: []modrinth.Dependency{{ProjectID: "lp", Type: "required"}}},
		},
		newer: updates{
			"lp": {
				"release": {ID: "lp2", ProjectID: "lp", VersionNumber: "2.0", VersionType: "release", Published: day(2)},
				"beta":    {ID: "lp3", ProjectID: "lp", VersionNumber: "2.1-beta", VersionType: "beta", Published: day(3)},
			},
			"vault": {"release": {ID: "vault1", ProjectID: "vault", VersionNumber: "1.7", Published: day(1)}},
		},
		projects: map[string]Project{"vault": {ID: "vault", Title: "Vault"}, "lp": {ID: "lp", Title: "LuckPerms"}, "eco": {ID: "eco", Title: "Economy"}, "off": {ID: "off", Title: "Off"}},
	}
	files := []*noryxv1.PluginFile{
		{FileName: "Vault.jar", Sha512: "vault", Settings: "Vault"},
		{FileName: "LuckPerms.jar", Sha512: "lp", Settings: "../world"},
		{FileName: "Economy.jar", Sha512: "eco"},
		{FileName: "Off.jar", Sha512: "off", Disabled: true},
		{FileName: "Custom.jar", Sha512: "custom", Settings: "Custom"},
	}
	plugins := c.describe("plugins", files, kept{pinned: map[string]bool{"lp": true}, channels: map[string]string{"lp": "beta"}})
	vault, lp, off, custom := plugins[0], plugins[1], plugins[3], plugins[4]
	switch {
	case !slices.Equal(vault.RequiredBy, []string{"Economy", "LuckPerms"}) || vault.Update != "" || vault.Settings != "plugins/Vault" || vault.Pinned || vault.Tracks != "":
		t.Fatalf("Vault = %+v", vault)
	case len(lp.RequiredBy) != 0 || lp.Update != "2.1-beta" || lp.UpdateID != "lp3" || lp.UpdateChannel != "beta" || lp.Channel != "beta" || !lp.Pinned ||
		lp.Tracks != "beta" || lp.Settings != "":
		t.Fatalf("LuckPerms = %+v", lp)
	case !off.Disabled || off.Project == nil:
		t.Fatalf("turned-off plugin = %+v", off)
	case custom.Project != nil || custom.Settings != "plugins/Custom":
		t.Fatalf("file not from a catalogue = %+v", custom)
	}
}
