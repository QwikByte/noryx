package modpack

import (
	"archive/zip"
	"bytes"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"testing"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/backup"
	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/modrinth"
)

func hash(content string) string {
	sum := sha512.Sum512([]byte(content))
	return hex.EncodeToString(sum[:])
}

// An update changes only what the next version changes. Mods follow the pack, also over
// versions installed by hand, unless the administrator removed them; other files the
// administrator changed are kept and listed.
func TestDiff(t *testing.T) {
	mod := func(project, content string) written { return written{hash(content), project} }
	old := map[string]written{
		"mods/a-1.jar":  mod("A", "a1"),       // updated by the pack
		"mods/b.jar":    mod("B", "b1"),       // unchanged by the pack, removed by the administrator
		"mods/c-1.jar":  mod("C", "c1"),       // updated by hand to c-1.5.jar, and by the pack
		"mods/d.jar":    mod("D", "d1"),       // removed by the administrator, updated by the pack
		"mods/e.jar":    mod("E", "e1"),       // updated in place by hand, removed from the pack
		"config/x.toml": {SHA512: hash("x1")}, // unchanged on the server, changed by the pack
		"config/y.toml": {SHA512: hash("y1")}, // changed on the server and by the pack
		"config/z.toml": {SHA512: hash("z1")}, // changed on the server, removed from the pack
		"config/w.toml": {SHA512: hash("w1")}, // unchanged on the server, removed from the pack
		"config/v.toml": {SHA512: hash("v1")}, // removed on the server, changed by the pack
		"config/u.toml": {SHA512: hash("u1")}, // changed on the server, unchanged by the pack
	}
	of := func(path, project, content string) file {
		return file{path: path, sha512: hash(content), project: project}
	}
	next := []file{
		of("mods/a-2.jar", "A", "a2"),
		of("mods/b.jar", "B", "b1"),
		of("mods/c-2.jar", "C", "c2"),
		of("mods/d-2.jar", "D", "d2"),
		of("mods/f.jar", "F", "f1"), // a new mod, where the server has a folder
		of("config/x.toml", "", "x2"),
		of("config/y.toml", "", "y2"),
		of("config/v.toml", "", "v2"),
		of("config/u.toml", "", "u1"),
		of("config/new.toml", "", "new"),    // missing on the server
		of("config/gen.toml", "", "pack's"), // written by a mod meanwhile
		of("kubejs/s.js", "", "script"),     // the server has it already
	}
	current := map[string]string{
		"mods/a-1.jar": hash("a1"), "mods/e.jar": hash("e by hand"), "mods/f.jar": "",
		"config/x.toml": hash("x1"), "config/y.toml": hash("y by admin"), "config/z.toml": hash("z by admin"),
		"config/w.toml": hash("w1"), "config/u.toml": hash("u by admin"), "config/gen.toml": hash("mod's"),
		"kubejs/s.js": hash("script"),
	}
	byHand := map[string]string{"mods/c-1.5.jar": "C", "mods/g.jar": "G"}

	p := diff(old, next, current, byHand)
	var paths []string
	for _, f := range p.write {
		paths = append(paths, f.path)
	}
	if want := []string{"config/new.toml", "config/v.toml", "config/x.toml", "mods/a-2.jar", "mods/c-2.jar"}; !slices.Equal(paths, want) {
		t.Errorf("written = %q, want %q", paths, want)
	}
	if want := []string{"config/w.toml", "mods/a-1.jar", "mods/c-1.5.jar", "mods/e.jar"}; !slices.Equal(p.remove, want) {
		t.Errorf("removed = %q, want %q", p.remove, want)
	}
	if want := []string{"config/gen.toml", "config/y.toml", "config/z.toml", "mods/d-2.jar", "mods/f.jar"}; !slices.Equal(p.kept, want) {
		t.Errorf("kept = %q, want %q", p.kept, want)
	}
	// The record has the files of the next version, also those kept.
	if got := slices.Sorted(maps.Keys(p.files)); len(got) != len(next) || p.files["mods/a-2.jar"] != mod("A", "a2") {
		t.Errorf("record = %v", p.files)
	}
}

// The backup before an update has what was chosen and the files and folders with the files of
// the pack, within the limit of paths, and is a step of the update only if it is made.
func TestSelection(t *testing.T) {
	pack := topLevel(map[string]written{"mods/a.jar": {}, "config/a.toml": {}}, []file{{path: "mods/b.jar"}, {path: "options.txt"}})
	sel, err := selection(backup.Selection{Worlds: true, Paths: []string{"kubejs", "mods"}, Exclude: []string{"config/big"}}, pack)
	if err != nil || !sel.GetWorlds() || sel.GetPlugins() || !slices.Equal(sel.GetPaths(), []string{"config", "kubejs", "mods", "options.txt"}) ||
		!slices.Equal(sel.GetExclude(), []string{"config/big"}) {
		t.Errorf("selection = %v, %v", sel, err)
	}
	if sel, err := selection(backup.Selection{Everything: true}, pack); err != nil || !sel.GetEverything() || len(sel.GetPaths()) != 0 {
		t.Errorf("everything = %v, %v", sel, err)
	}
	many := make([]string, noryxv1.MaxBackupPaths-1)
	for i := range many {
		many[i] = fmt.Sprintf("folder-%d", i)
	}
	if _, err := selection(backup.Selection{Paths: many}, pack); err == nil {
		t.Error("a selection with more paths than allowed was accepted")
	}

	u := update{server: &noryxv1.Server{State: noryxv1.ServerState_SERVER_STATE_STOPPED}}
	if steps := u.steps(); !slices.Equal(steps, []string{"modpack", "mods"}) {
		t.Errorf("steps without a backup = %q", steps)
	}
	u.server.State, u.backup = noryxv1.ServerState_SERVER_STATE_RUNNING, &defaultBackup
	if steps := u.steps(); !slices.Equal(steps, []string{"modpack", "stop", "archive", "mods"}) {
		t.Errorf("steps = %q", steps)
	}
}

// A pack is refused if a file leaves the server's folder, comes from elsewhere than Modrinth's
// CDN or has no valid hash. Its files are named once, those of server-overrides last.
func TestParse(t *testing.T) {
	s := NewService(nil, nil, modrinth.New(modrinth.DefaultAPI, modrinth.DefaultCDN))
	jar := modrinth.DefaultCDN + "data/AANobbMI/versions/IZskON6d/sodium.jar"
	pack := func(path, url, sha string, overrides map[string]string) (*Pack, error) {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		idx, _ := json.Marshal(map[string]any{
			"formatVersion": 1, "game": "minecraft", "dependencies": map[string]string{"minecraft": "1.21.4", "fabric-loader": "0.16.10"},
			"files": []map[string]any{{"path": path, "hashes": map[string]string{"sha512": sha}, "downloads": []string{url}}},
		})
		w, _ := zw.Create(indexFile)
		_, _ = w.Write(idx)
		for _, name := range slices.Sorted(maps.Keys(overrides)) {
			w, _ := zw.Create(name)
			_, _ = w.Write([]byte(overrides[name]))
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		archive, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
		if err != nil {
			t.Fatal(err)
		}
		return s.parse(archive)
	}

	p, err := pack("mods/sodium.jar", jar, hash("sodium"), map[string]string{
		"server-overrides/config/a.toml": "server", "overrides/config/a.toml": "everyone", "overrides/mods/sodium.jar": "own",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprint(p.record())
	if want := fmt.Sprint(map[string]written{"mods/sodium.jar": {hash("own"), ""}, "config/a.toml": {hash("server"), ""}}); got != want {
		t.Errorf("files = %s, want %s", got, want)
	}
	if p, _ := pack("mods/sodium.jar", jar, hash("sodium"), map[string]string{}); p.record()["mods/sodium.jar"].Project != "AANobbMI" {
		t.Errorf("files = %v, want the project of the mod", p.record())
	}
	for name, tc := range map[string]struct {
		path, url, sha string
		overrides      map[string]string
	}{
		"path outside":     {"../escape.jar", jar, hash("x"), nil},
		"override outside": {"mods/x.jar", jar, hash("x"), map[string]string{"overrides/../escape.txt": "x"}},
		"elsewhere":        {"mods/x.jar", "https://example.com/x.jar", hash("x"), nil},
		"invalid hash":     {"mods/x.jar", jar, "abc", nil},
	} {
		if _, err := pack(tc.path, tc.url, tc.sha, tc.overrides); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The record of a pack follows its server to another node and to a copy, and goes with it.
func TestRecord(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, id := range []string{"n1", "n2"} {
		if _, err := db.Exec(`INSERT INTO nodes (id, name, address, created_at) VALUES (?, ?, 'host:7443', 0)`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	s, ctx := NewService(db, nil, nil), t.Context()
	files := map[string]written{"mods/a.jar": {hash("a"), "A"}}
	v1 := &Pack{Project: "pack", Version: "v1", Number: "1.0", GameVersion: "1.21.4", LoaderVersion: "0.16.10"}
	if err := s.remember(ctx, "n1", "s1", v1, files); err != nil {
		t.Fatal(err)
	}
	if err := s.Copy(ctx, "n1", "s1", "s2"); err != nil {
		t.Fatal(err)
	}
	if err := s.Move(ctx, "s1", "n1", "n2"); err != nil {
		t.Fatal(err)
	}
	if err := s.remember(ctx, "n2", "s1", &Pack{Project: "pack", Version: "v2", Number: "2.0"}, map[string]written{}); err != nil {
		t.Fatal(err)
	}
	if i, ok, err := s.installed(ctx, "n2", "s1"); err != nil || !ok || i.version != "v2" || len(i.files) != 0 {
		t.Errorf("moved = %+v, %v, %v", i, ok, err)
	}
	if i, ok, err := s.installed(ctx, "n1", "s2"); err != nil || !ok || i.number != "1.0" || i.loader != "0.16.10" || !maps.Equal(i.files, files) {
		t.Errorf("copy = %+v, %v, %v", i, ok, err)
	}
	if err := s.Forget(ctx, "n1", "s2"); err != nil {
		t.Fatal(err)
	}
	for _, ref := range [][2]string{{"n1", "s1"}, {"n1", "s2"}} {
		if _, ok, err := s.installed(ctx, ref[0], ref[1]); ok || err != nil {
			t.Errorf("%s/%s has a pack: %v", ref[0], ref[1], err)
		}
	}
}
