package template

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/modrinth"
	"github.com/QwikByte/noryx/internal/master/plugin"
	"github.com/QwikByte/noryx/internal/master/server"
)

// fakePlugins knows LuckPerms for Paper, in the versions 2.0 and 1.0 for Minecraft 1.21.4,
// and 1.0 for Minecraft 1.20.1.
type fakePlugins struct{}

func (fakePlugins) Projects(_ context.Context, ids []string) ([]modrinth.Project, error) {
	lp := modrinth.Project{ID: "luckperms", Slug: "luckperms", Title: "LuckPerms", Loaders: []string{"paper"}}
	return slices.DeleteFunc([]modrinth.Project{lp}, func(p modrinth.Project) bool { return !slices.Contains(ids, p.ID) }), nil
}

func (fakePlugins) Describe(p modrinth.Project) plugin.Project {
	return plugin.Project{ID: p.ID, Slug: p.Slug, Title: p.Title}
}

func (fakePlugins) Versions(_ context.Context, _ string, _ noryxv1.ServerType, gameVersion string) ([]plugin.Version, error) {
	old := plugin.Version{ID: "lp1", Number: "1.0"}
	if gameVersion == "1.20.1" {
		return []plugin.Version{old}, nil
	}
	return []plugin.Version{{ID: "lp2", Number: "2.0"}, old}, nil
}

func newService(t *testing.T) *Service {
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewService(db, fakePlugins{})
}

func input(change func(*Input)) Input {
	in := Input{
		Name: "Bedwars", Settings: Settings{
			Type: "paper", Version: "1.21.4", MemoryMB: 2048, Properties: map[string]string{"motd": "Bedwars"},
			ExtraPorts: []server.ExtraPort{{Port: 8100, Protocol: "tcp", Label: "Map"}},
		},
		Tags: []string{"Bedwars", "minigames"}, Plugins: []string{"luckperms"}, Versions: map[string]string{"luckperms": "lp1"},
		Channels: map[string]string{"luckperms": "beta"},
	}
	change(&in)
	return in
}

// Templates keep tags, versions and channels of their plugins, which must suit their servers,
// and leave the properties Noryx sets alone.
func TestSave(t *testing.T) {
	s := newService(t)
	tmpl, err := s.Create(t.Context(), input(func(*Input) {}))
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err = s.Get(t.Context(), tmpl.ID)
	want := []Plugin{{Project: plugin.Project{ID: "luckperms", Slug: "luckperms", Title: "LuckPerms"}, Version: "lp1", VersionNumber: "1.0", Channel: "beta"}}
	if err != nil || !slices.Equal(tmpl.Tags, []string{"bedwars", "minigames"}) || !reflect.DeepEqual(tmpl.Plugins, want) || len(tmpl.ExtraPorts) != 1 {
		t.Fatalf("template = %+v, %v", tmpl, err)
	}
	// Releases are the channel of plugins without one.
	release, err := s.Update(t.Context(), tmpl.ID, input(func(in *Input) { in.Channels = map[string]string{"luckperms": "release"} }))
	if err != nil || release.Plugins[0].Channel != "" {
		t.Fatalf("template = %+v, %v", release, err)
	}
	for _, tc := range []struct {
		name   string
		change func(*Input)
		want   string
	}{
		{"invalid tag", func(in *Input) { in.Tags = []string{"two words"} }, "Tags have up to 24 letters, digits, - and _."},
		{"too many tags", func(in *Input) { in.Tags = strings.Fields("a b c d e f g h i j k") }, "Use at most 10 tags."},
		{"port", func(in *Input) { in.Properties = map[string]string{"server-port": "25566"} }, "server-port can't be part of a template: " + noryxv1.ManagedProperties["server-port"]},
		{"RCON", func(in *Input) { in.Properties = map[string]string{"enable-rcon": "false"} }, "enable-rcon can't be part of a template: " + noryxv1.ManagedProperties["enable-rcon"]},
		{"secret", func(in *Input) { in.Properties = map[string]string{"management-server-secret": "x"} }, "management-server-secret is a secret and can't be part of a template."},
		{"version of another plugin", func(in *Input) { in.Versions = map[string]string{"vault": "lp1"} }, "Choose versions only for the plugins of the template."},
		{"invalid version", func(in *Input) { in.Versions = map[string]string{"luckperms": "../x"} }, "Choose versions only for the plugins of the template."},
		{"version for other servers", func(in *Input) { in.Versions = map[string]string{"luckperms": "vault1"} }, "The chosen version of LuckPerms doesn't run on the servers of the template."},
		{"version for newer Minecraft", func(in *Input) { in.Version = "1.20.1"; in.Versions = map[string]string{"luckperms": "lp2"} }, "The chosen version of LuckPerms doesn't run on the servers of the template."},
		{"channel of another plugin", func(in *Input) { in.Channels = map[string]string{"vault": "beta"} }, "Choose the channel release, beta or alpha, and only for the plugins of the template."},
		{"invalid channel", func(in *Input) { in.Channels = map[string]string{"luckperms": "snapshot"} }, "Choose the channel release, beta or alpha, and only for the plugins of the template."},
		{"console port", func(in *Input) { in.ExtraPorts = []server.ExtraPort{{Port: 25575, Protocol: "tcp"}} }, "The server's container uses port 25575 itself. Choose another extra port."},
	} {
		if _, err := s.Create(t.Context(), input(func(in *Input) { in.Name = tc.name; tc.change(in) })); err == nil || httpapi.Message(err) != tc.want {
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
		}
	}
}

// An exported template imports into the same template on another master, under another name.
func TestExportImport(t *testing.T) {
	s := newService(t)
	tmpl, err := s.Create(t.Context(), input(func(*Input) {}))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(Export(tmpl))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), tmpl.ID) || strings.Contains(string(data), "createdAt") {
		t.Fatalf("export has the template's ID or creation: %s", data)
	}
	in, err := parseFile(data)
	if err != nil {
		t.Fatal(err)
	}
	in.Name = "Bedwars 2"
	imported, err := newService(t).Create(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	imported.Name = tmpl.Name
	if !reflect.DeepEqual(imported.Content, tmpl.Content) {
		t.Fatalf("imported %+v, want %+v", imported.Content, tmpl.Content)
	}
	// Files of the first format, whose plugins have no channel, still import.
	in, err = parseFile([]byte(`{"format": "noryx-template", "version": 1, "template": {"name": "Old", "plugins": [{"id": "luckperms", "version": "lp1"}]}}`))
	if err != nil || !slices.Equal(in.Plugins, []string{"luckperms"}) || in.Versions["luckperms"] != "lp1" || len(in.Channels) != 0 {
		t.Fatalf("file of version 1: %+v, %v", in, err)
	}
}

// Files of other formats, versions or with unknown fields are refused with a message, as are
// files above 1 MiB.
func TestImportChecksFile(t *testing.T) {
	for file, want := range map[string]string{
		`not JSON`: "This isn't a template exported from Noryx.",
		`[]`:       "This isn't a template exported from Noryx.",
		`{"format": "noryx-fileset", "version": 1, "template": {}}`:                     "This isn't a template exported from Noryx.",
		`{"format": "noryx-template", "version": 0, "template": {}}`:                    "This isn't a template exported from Noryx.",
		`{"format": "noryx-template", "version": 4, "template": {"new": true}}`:         "The template comes from a newer version of Noryx. Update Noryx to import it.",
		`{"format": "noryx-template", "version": 1, "template": {"id": "x"}}`:           `The template file is invalid: json: unknown field "id"`,
		`{"format": "noryx-template", "version": 1, "template": {"name": "x"}, "x": 1}`: `The template file is invalid: json: unknown field "x"`,
	} {
		if _, err := parseFile([]byte(file)); err == nil || httpapi.Message(err) != want {
			t.Errorf("%s: %v, want %q", file, err, want)
		}
	}
	big := `{"format": "noryx-template", "version": 1, "template": {"description": "` + strings.Repeat("x", maxFile) + `"}}`
	_, err := readFile(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/templates/import", strings.NewReader(big)))
	if httpErr := new(httpapi.Error); !errors.As(err, &httpErr) || httpErr.Status != http.StatusRequestEntityTooLarge {
		t.Errorf("a big file: %v", err)
	}
}
