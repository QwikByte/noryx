package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/datadir"
	"github.com/QwikByte/noryx/internal/agent/fileset"
	"github.com/QwikByte/noryx/internal/agent/network"
	"github.com/QwikByte/noryx/internal/agent/runtime"
	"github.com/QwikByte/noryx/internal/agent/storage"
)

const serverID = "aaaaaaaaaaaaaaaaaaaaaaaaaa"

var paper = noryxv1.ServerType_SERVER_TYPE_PAPER

// paperData creates the data directory of a Paper server.
func paperData(t *testing.T) (string, *datadir.Dir) {
	path := t.TempDir()
	files := map[string]string{
		"server.properties": "motd=hi\n", "bukkit.yml": "a: 1\n", "paper.jar": "jar", ".rcon-cli.env": "secret",
		"world/level.dat": "w", "world/region/r.0.0.mca": "chunks", "world_nether/level.dat": "n",
		"plugins/LuckPerms.jar": "lp", "plugins/LuckPerms/config.yml": "storage: h2\n",
		"config/paper-global.yml": "x: 1\n", "logs/latest.log": "log", "cache/x": "x", fileset.ManifestFile: "{}",
	}
	for name, content := range files {
		write(t, filepath.Join(path, name), content)
	}
	dir, err := datadir.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dir.Close() })
	return path, dir
}

func write(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(path, name string) string {
	data, _ := os.ReadFile(filepath.Join(path, name))
	return string(data)
}

// restore restores paths of a backup, all of it without any, like RestoreBackup does.
func restore(t *testing.T, dir *datadir.Dir, typ noryxv1.ServerType, b Archive, paths ...string) {
	t.Helper()
	zr, err := zip.OpenReader(b.Path())
	check(t, err)
	defer zr.Close()
	paths, err = chosen(&zr.Reader, b, paths)
	check(t, err)
	staged, err := stage(t.Context(), dir, &zr.Reader, paths)
	defer dir.RemoveAll(staged) //nolint:errcheck // a temporary folder
	check(t, err)
	check(t, keep(dir, typ, staged, false))
	check(t, swap(dir, staged, paths, kept(dir, b)))
}

func archived(t *testing.T, b Archive) []string {
	t.Helper()
	zr, err := zip.OpenReader(b.Path())
	check(t, err)
	defer zr.Close()
	var list []string
	for _, f := range zr.File {
		list = append(list, f.Name)
	}
	return list
}

func TestSelected(t *testing.T) {
	path, dir := paperData(t)
	if err := os.Symlink(t.TempDir(), filepath.Join(path, "outside")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name                string
		typ                 noryxv1.ServerType
		sel                 *noryxv1.BackupSelection
		paths, wantExcluded []string
	}{
		{"worlds", paper, &noryxv1.BackupSelection{Worlds: true}, []string{"world", "world_nether"}, nil},
		{"plugins", paper, &noryxv1.BackupSelection{Plugins: true}, []string{"plugins"}, nil},
		{"mods keep their settings in config", noryxv1.ServerType_SERVER_TYPE_FABRIC, &noryxv1.BackupSelection{Plugins: true}, []string{"config"}, nil},
		{"config", paper, &noryxv1.BackupSelection{Config: true}, []string{"bukkit.yml", "config", "server.properties"}, nil},
		{"paths inside others", paper, &noryxv1.BackupSelection{Worlds: true, Paths: []string{"world/region", "/logs/", "missing", "outside", fileset.ManifestFile}}, []string{"logs", "world", "world_nether"}, nil},
		{"everything", paper, &noryxv1.BackupSelection{Everything: true, Worlds: true}, []string{"."}, nil},
		{"root as a path", paper, &noryxv1.BackupSelection{Paths: []string{"/"}}, []string{"."}, nil},
		{
			"left out", paper, &noryxv1.BackupSelection{Everything: true, Exclude: []string{"logs/", "world/region", "world/region/r.0.0.mca", "missing"}},
			[]string{"."}, []string{"logs", "missing", filepath.Join("world", "region")},
		},
		{
			"left out of some", paper, &noryxv1.BackupSelection{Worlds: true, Exclude: []string{"world_nether", "world/region", "plugins/LuckPerms"}},
			[]string{"world"}, []string{filepath.Join("world", "region")},
		},
	} {
		got, excluded, err := selected(dir, tc.typ, tc.sel)
		if err != nil || !slices.Equal(got, tc.paths) || !slices.Equal(excluded, tc.wantExcluded) {
			t.Errorf("%s: got %q, %q, %v; want %q, %q", tc.name, got, excluded, err, tc.paths, tc.wantExcluded)
		}
	}
	for _, sel := range []*noryxv1.BackupSelection{
		{Paths: []string{"../other"}},
		{Everything: true, Exclude: []string{"../other"}},
		{Everything: true, Exclude: []string{"/"}},
		{Everything: true, Exclude: slices.Repeat([]string{"logs"}, noryxv1.MaxBackupPaths+1)},
	} {
		if _, _, err := selected(dir, paper, sel); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%v was accepted: %v", sel, err)
		}
	}
}

func TestBackupAndRestore(t *testing.T) {
	path, dir := paperData(t)
	s := Store{storage.New(t.TempDir())}
	create := func(paths []string, jobID string) Archive {
		t.Helper()
		b, err := s.create(t.Context(), dir, serverID, storage.Default, Details{Label: "test", Created: time.Now(), Paths: paths, JobID: jobID})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	// Restoring worlds replaces them, including files added since, and leaves the rest.
	worlds := create([]string{"world", "world_nether"}, "")
	write(t, filepath.Join(path, "world/region/r.0.0.mca"), "griefed")
	write(t, filepath.Join(path, "world/region/r.1.0.mca"), "new")
	write(t, filepath.Join(path, "server.properties"), "motd=changed\n")
	restore(t, dir, paper, worlds)
	if read(path, "world/region/r.0.0.mca") != "chunks" || read(path, "world/region/r.1.0.mca") != "" || read(path, "server.properties") != "motd=changed\n" {
		t.Fatal("the worlds were not restored, or more than the worlds")
	}

	// Restoring everything brings the data directory back to the state of the backup, except
	// for the manifest of file sets, which isn't backed up: what it marks stays hidden.
	all := create([]string{"."}, "")
	write(t, filepath.Join(path, "plugins/Grief.jar"), "x")
	write(t, filepath.Join(path, fileset.ManifestFile), `{"marked":["bukkit.yml"]}`)
	if err := os.RemoveAll(filepath.Join(path, "world_nether")); err != nil {
		t.Fatal(err)
	}
	restore(t, dir, paper, all)
	if read(path, "plugins/Grief.jar") != "" || read(path, "world_nether/level.dat") != "n" || read(path, ".rcon-cli.env") != "secret" {
		t.Fatal("everything was not restored")
	}
	if read(path, fileset.ManifestFile) != `{"marked":["bukkit.yml"]}` {
		t.Fatal("restoring replaced the manifest of file sets")
	}
	if slices.Contains(archived(t, all), fileset.ManifestFile) {
		t.Fatal("the backup has the manifest of file sets")
	}
	if entries, _ := os.ReadDir(path); slices.ContainsFunc(entries, func(e os.DirEntry) bool { return datadir.IsTemp(e.Name()) }) {
		t.Fatal("temporary files were left behind")
	}

	// Backups are listed newest first; a job keeps its newest backups only, and those marked
	// to keep.
	for range 3 {
		create([]string{"world"}, "job1")
	}
	backups, err := s.List(serverID)
	check(t, err)
	backups[2].Kept = true // the oldest of the job
	check(t, s.Update(backups[2]))
	check(t, s.Prune(serverID, "job1", &noryxv1.BackupRetention{Last: 1}))
	backups, err = s.List(serverID)
	check(t, err)
	var jobs []bool
	for _, b := range backups {
		if b.JobID == "job1" {
			jobs = append(jobs, b.Kept)
		}
	}
	if len(backups) != 4 || !slices.Equal(jobs, []bool{false, true}) || backups[len(backups)-1].ID != worlds.ID || backups[0].Size == 0 {
		t.Fatalf("backups after pruning = %+v", backups)
	}
	if _, err := s.Find(serverID, "../../etc/passwd"); err == nil {
		t.Fatal("found a backup with an invalid ID")
	}
	if err := s.RemoveAll(serverID); err != nil {
		t.Fatal(err)
	}
	if backups, _ := s.List(serverID); len(backups) != 0 {
		t.Fatalf("backups after removing all = %v", backups)
	}
}

// A job keeps its newest backups, and the newest of each of the last days, weeks and months
// with backups in its time zone.
func TestRetained(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	check(t, err)
	// Newest first: one on Sunday, 2026-10-04 at 12:00 in Berlin, and one every day at 00:30
	// from then back to 2026-08-01, but none from 09-20 to 09-24.
	backups := []Archive{{ID: "2026-10-04 noon", Details: Details{Created: time.Date(2026, 10, 4, 12, 0, 0, 0, berlin)}}}
	for day := time.Date(2026, 10, 4, 0, 30, 0, 0, berlin); day.Month() >= time.August; day = day.AddDate(0, 0, -1) {
		if day.Month() != time.September || day.Day() < 20 || day.Day() > 24 {
			backups = append(backups, Archive{ID: day.Format(time.DateOnly), Details: Details{Created: day}})
		}
	}
	kept := func(r *noryxv1.BackupRetention) []string {
		var ids []string
		for i, keep := range retained(backups, r) {
			if keep {
				ids = append(ids, backups[i].ID)
			}
		}
		return ids
	}
	for _, tc := range []struct {
		name string
		r    *noryxv1.BackupRetention
		want []string
	}{
		{"newest", &noryxv1.BackupRetention{Last: 3}, []string{"2026-10-04 noon", "2026-10-04", "2026-10-03"}},
		// 00:30 in Berlin is the day before in UTC, so days begin in the job's time zone.
		{"daily", &noryxv1.BackupRetention{Days: 2, TimeZone: "Europe/Berlin"}, []string{"2026-10-04 noon", "2026-10-03"}},
		{"daily in UTC", &noryxv1.BackupRetention{Days: 2}, []string{"2026-10-04 noon", "2026-10-04"}},
		// Days without backups don't count.
		{
			"daily over a gap", &noryxv1.BackupRetention{Days: 13, TimeZone: "Europe/Berlin"},
			[]string{"2026-10-04 noon", "2026-10-03", "2026-10-02", "2026-10-01", "2026-09-30", "2026-09-29", "2026-09-28", "2026-09-27", "2026-09-26", "2026-09-25", "2026-09-19", "2026-09-18", "2026-09-17"},
		},
		// Weeks begin on Monday; 2026-10-04 is a Sunday.
		{"weekly", &noryxv1.BackupRetention{Weeks: 3, TimeZone: "Europe/Berlin"}, []string{"2026-10-04 noon", "2026-09-27", "2026-09-19"}},
		{"monthly", &noryxv1.BackupRetention{Months: 5, TimeZone: "Europe/Berlin"}, []string{"2026-10-04 noon", "2026-09-30", "2026-08-31"}},
		{
			"7 daily and 4 weekly", &noryxv1.BackupRetention{Days: 7, Weeks: 4, TimeZone: "Europe/Berlin"},
			[]string{"2026-10-04 noon", "2026-10-03", "2026-10-02", "2026-10-01", "2026-09-30", "2026-09-29", "2026-09-28", "2026-09-27", "2026-09-19", "2026-09-13"},
		},
	} {
		if got := kept(tc.r); !slices.Equal(got, tc.want) {
			t.Errorf("%s: kept %q, want %q", tc.name, got, tc.want)
		}
	}
}

// What a backup leaves out is missing from the archive, and restoring it leaves it as it is,
// also what the server didn't have when it was backed up.
func TestExclude(t *testing.T) {
	path, dir := paperData(t)
	write(t, filepath.Join(path, "plugins/dynmap/web/tiles/t.png"), "tile")
	write(t, filepath.Join(path, "plugins/dynmap/configuration.txt"), "old")
	s := Store{storage.New(t.TempDir())}
	paths, exclude, err := selected(dir, paper, &noryxv1.BackupSelection{Everything: true, Exclude: []string{"plugins/dynmap/web/tiles", "logs", "missing"}})
	check(t, err)
	b, err := s.create(t.Context(), dir, serverID, storage.Default, Details{Created: time.Now(), Paths: paths, Exclude: exclude})
	check(t, err)
	left := func(n string) bool {
		return n == "logs/" || n == "plugins/dynmap/web/tiles/" || filepath.Base(n) == "t.png"
	}
	if got := archived(t, b); slices.ContainsFunc(got, left) || !slices.Contains(got, "plugins/dynmap/web/") {
		t.Fatalf("archive = %q", got)
	}

	write(t, filepath.Join(path, "plugins/dynmap/web/tiles/t.png"), "new tile")
	write(t, filepath.Join(path, "plugins/dynmap/configuration.txt"), "new")
	write(t, filepath.Join(path, "logs/latest.log"), "new log")
	write(t, filepath.Join(path, "plugins/Grief.jar"), "x")
	write(t, filepath.Join(path, "missing"), "new")
	restore(t, dir, paper, b)
	if read(path, "plugins/dynmap/web/tiles/t.png") != "new tile" || read(path, "logs/latest.log") != "new log" || read(path, "missing") != "new" {
		t.Error("restoring changed what the backup left out")
	}
	if read(path, "plugins/dynmap/configuration.txt") != "old" || read(path, "plugins/Grief.jar") != "" {
		t.Error("restoring didn't restore what is around what the backup left out")
	}
}

// Restoring chosen paths of a backup restores only these; others aren't accepted.
func TestRestorePaths(t *testing.T) {
	path, dir := paperData(t)
	s := Store{storage.New(t.TempDir())}
	b, err := s.create(t.Context(), dir, serverID, storage.Default, Details{Created: time.Now(), Paths: []string{"world", "plugins"}})
	check(t, err)
	write(t, filepath.Join(path, "world/region/r.0.0.mca"), "griefed")
	write(t, filepath.Join(path, "world/level.dat"), "changed")
	write(t, filepath.Join(path, "plugins/LuckPerms/config.yml"), "changed")
	restore(t, dir, paper, b, "world/region")
	if read(path, "world/region/r.0.0.mca") != "chunks" || read(path, "world/level.dat") != "changed" || read(path, "plugins/LuckPerms/config.yml") != "changed" {
		t.Fatal("more or less than the chosen folder was restored")
	}

	zr, err := zip.OpenReader(b.Path())
	check(t, err)
	defer zr.Close()
	for _, p := range []string{".", "world_nether", "world/missing", "../world", "plugins/../../x"} {
		if _, err := chosen(&zr.Reader, b, []string{p}); err == nil {
			t.Errorf("restoring %q was accepted", p)
		}
	}
	if got, err := chosen(&zr.Reader, b, []string{"/world/", "world/region", "plugins/LuckPerms.jar"}); err != nil ||
		!slices.Equal(got, []string{filepath.Join("plugins", "LuckPerms.jar"), "world"}) {
		t.Errorf("chosen = %q, %v", got, err)
	}
}

// Files that held secrets of file sets stay as they are when a backup is restored: one that
// the server lost as it left a set doesn't come back, and one it has keeps its content.
func TestRestoreKeepsMarkedFiles(t *testing.T) {
	path, dir := paperData(t)
	s := Store{storage.New(t.TempDir())}
	write(t, filepath.Join(path, "plugins/Sync/token.yml"), "token: old")
	b, err := s.create(t.Context(), dir, serverID, storage.Default, Details{Created: time.Now(), Paths: []string{"."}})
	check(t, err)
	check(t, os.Remove(filepath.Join(path, "plugins/LuckPerms/config.yml")))
	write(t, filepath.Join(path, "plugins/Sync/token.yml"), "token: new")
	write(t, filepath.Join(path, fileset.ManifestFile), `{"marked":["plugins/LuckPerms/config.yml","plugins/Sync/token.yml"]}`)

	restore(t, dir, paper, b)
	if _, err := os.Stat(filepath.Join(path, "plugins/LuckPerms/config.yml")); !os.IsNotExist(err) {
		t.Error("restoring brought back a file with secrets the server lost")
	}
	if got := read(path, "plugins/Sync/token.yml"); got != "token: new" {
		t.Errorf("a file with secrets was restored as %q", got)
	}
	if read(path, "bukkit.yml") != "a: 1\n" || read(path, "plugins/LuckPerms.jar") != "lp" {
		t.Error("the other files weren't restored")
	}
}

// Restoring a backup of a server brings back neither the secrets of a network it left since,
// nor the trust in its proxy or offline mode, and keeps the newer secret of its network.
func TestRestoreKeepsNetwork(t *testing.T) {
	velocity := noryxv1.ServerType_SERVER_TYPE_VELOCITY
	joined := func(secret string) runtime.Network {
		return runtime.Network{Forwarding: runtime.ForwardingModern, ForwardingSecret: secret, Backends: []runtime.NetworkBackend{{Name: "lobby", Address: "lobby:25565"}}, Try: []string{"lobby"}}
	}
	proxy := func(n runtime.Network, key string) func(*datadir.Dir) error {
		return func(dir *datadir.Dir) error {
			_, _, err := network.WriteProxy(dir, velocity, n)
			if key == "" {
				return errors.Join(err, dir.RemoveAll(network.FloodgateKeyFile))
			}
			return errors.Join(err, dir.WriteFile(network.FloodgateKeyFile, []byte(key)))
		}
	}
	proxyFiles := append([]string{network.ForwardingSecretFile}, network.BedrockSecretFiles...)
	for _, tc := range []struct {
		name          string
		typ           noryxv1.ServerType
		before, after func(*datadir.Dir) error // the server as it is backed up, and as it is restored
		kept          []string                 // files that stay as they are then, or missing
	}{
		{"proxy that left", velocity, proxy(joined("old"), "old key"), proxy(runtime.Network{}, ""), proxyFiles},
		{"proxy with a newer secret", velocity, proxy(joined("old"), "old key"), proxy(joined("new"), "new key"), proxyFiles},
		{
			"game server that left", paper,
			func(dir *datadir.Dir) error {
				_, err := network.WriteBackend(dir, paper, runtime.ForwardingModern, "old")
				return errors.Join(err, dir.WriteFile("server.properties", []byte("online-mode=false\n")))
			},
			func(dir *datadir.Dir) error {
				_, err := network.WriteBackend(dir, paper, runtime.ForwardingNone, "")
				return errors.Join(err, network.Leave(dir, true, false))
			},
			[]string{network.PaperGlobalFile, "spigot.yml", "server.properties"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := t.TempDir()
			dir, err := datadir.Open(path)
			check(t, err)
			defer dir.Close()
			check(t, dir.MkdirAll("plugins/floodgate"))
			check(t, dir.WriteFile("motd.txt", []byte("then")))
			check(t, tc.before(dir))
			s := Store{storage.New(t.TempDir())}
			b, err := s.create(t.Context(), dir, serverID, storage.Default, Details{Created: time.Now(), Paths: []string{"."}})
			check(t, err)
			check(t, dir.WriteFile("motd.txt", []byte("now")))
			check(t, tc.after(dir))
			now := map[string]string{}
			for _, name := range tc.kept {
				now[name] = read(path, name)
			}

			restore(t, dir, tc.typ, b)
			for name, want := range now {
				if got := read(path, name); got != want {
					t.Errorf("%s was restored as %q, want %q", name, got, want)
				}
			}
			if read(path, "motd.txt") != "then" {
				t.Error("the other files weren't restored")
			}
		})
	}
}

// A backup of another server, downloaded with its secrets hidden, gets the secrets of the
// server it is restored into, and brings none of its own.
func TestRestoreHiddenSecrets(t *testing.T) {
	path, dir := paperData(t)
	write(t, filepath.Join(path, "server.properties"), "motd=hi\nrcon.password=mine\n")
	archive := filepath.Join(t.TempDir(), "other.zip")
	f, err := os.Create(archive)
	check(t, err)
	zw := zip.NewWriter(f)
	for name, content := range map[string]string{
		"server.properties": "motd=other\nrcon.password=<hidden>\n", "world/level.dat": "other", network.ForwardingSecretFile: "theirs",
	} {
		w, err := zw.Create(name)
		check(t, err)
		_, err = w.Write([]byte(content))
		check(t, err)
	}
	check(t, errors.Join(zw.Close(), f.Close()))
	s := Store{storage.New(t.TempDir())}
	in, err := os.Open(archive)
	check(t, err)
	defer in.Close()
	b, err := s.Add(serverID, storage.Default, noryxv1.NewBackupID(time.Now()), Details{Created: time.Now(), Paths: []string{"."}}, 0, func(w io.Writer) error {
		_, err := io.Copy(w, in)
		return err
	})
	check(t, err)

	restore(t, dir, paper, b)
	if read(path, "server.properties") != "motd=other\nrcon.password=mine\n" || read(path, "world/level.dat") != "other" {
		t.Errorf("server.properties = %q", read(path, "server.properties"))
	}
	if read(path, ".rcon-cli.env") != "secret" || read(path, network.ForwardingSecretFile) != "" {
		t.Error("restoring changed the files with secrets")
	}
}

// fakeRuntime has one running Paper server.
type fakeRuntime struct {
	runtime.Runtime
	dir   string
	calls []string
}

func (f *fakeRuntime) List(context.Context) ([]runtime.Server, error) {
	return []runtime.Server{{Spec: runtime.Spec{ID: serverID, Type: paper}, State: noryxv1.ServerState_SERVER_STATE_RUNNING}}, nil
}

func (f *fakeRuntime) Data(context.Context, string) (*datadir.Dir, error) { return datadir.Open(f.dir) }

func (f *fakeRuntime) Stop(context.Context, string) error {
	f.calls = append(f.calls, "stop")
	return nil
}

func (f *fakeRuntime) Start(context.Context, string) error {
	f.calls = append(f.calls, "start")
	return nil
}

func (f *fakeRuntime) SendCommand(_ context.Context, _, command string) (string, error) {
	f.calls = append(f.calls, command)
	return "", nil
}

// Restoring can back up what it replaces first, while the server still runs; changing a
// backup changes its label and whether its job keeps it; and the backup's folders are listed
// without the files that only hold secrets.
func TestService(t *testing.T) {
	path, _ := paperData(t)
	rt := &fakeRuntime{dir: path}
	s := NewService(rt, storage.New(t.TempDir()))
	ctx := t.Context()
	created, err := s.CreateBackup(ctx, &noryxv1.CreateBackupRequest{
		ServerId: serverID, Label: "Nightly", JobId: "job1", Selection: &noryxv1.BackupSelection{Everything: true, Exclude: []string{"logs"}},
	})
	check(t, err)
	b := created.GetBackup()
	if !slices.Equal(b.GetExclude(), []string{"logs"}) {
		t.Fatalf("backup = %v", b)
	}

	files, err := s.ListBackupFiles(ctx, &noryxv1.ListBackupFilesRequest{ServerId: serverID, BackupId: b.GetId()})
	check(t, err)
	var listed []string
	for _, f := range files.GetFiles() {
		listed = append(listed, f.GetName())
	}
	if want := []string{"cache", "config", "plugins", "world", "world_nether", "bukkit.yml", "paper.jar", "server.properties"}; !slices.Equal(listed, want) {
		t.Fatalf("listed %q, want %q", listed, want)
	}
	files, err = s.ListBackupFiles(ctx, &noryxv1.ListBackupFilesRequest{ServerId: serverID, BackupId: b.GetId(), Path: "world"})
	check(t, err)
	if got := files.GetFiles(); len(got) != 2 || got[0].GetName() != "region" || !got[0].GetDirectory() || got[0].GetSize() != 6 ||
		got[1].GetName() != "level.dat" || got[1].GetSize() != 1 {
		t.Fatalf("listed %v", got)
	}
	for _, p := range []string{"logs", "missing", "../x"} {
		if _, err := s.ListBackupFiles(ctx, &noryxv1.ListBackupFilesRequest{ServerId: serverID, BackupId: b.GetId(), Path: p}); err == nil {
			t.Errorf("listed %q", p)
		}
	}

	write(t, filepath.Join(path, "world/region/r.0.0.mca"), "griefed")
	rt.calls = nil
	res, err := s.RestoreBackup(ctx, &noryxv1.RestoreBackupRequest{ServerId: serverID, BackupId: b.GetId(), Paths: []string{"world/region"}, SnapshotFirst: true})
	check(t, err)
	snapshot := res.GetSnapshot()
	if read(path, "world/region/r.0.0.mca") != "chunks" || snapshot.GetLabel() != "Before restoring Nightly" || snapshot.GetJobId() != "" ||
		!slices.Equal(snapshot.GetPaths(), []string{"world/region"}) {
		t.Fatalf("snapshot = %v", snapshot)
	}
	if want := []string{"save-off", "save-all flush", "save-on", "stop", "start"}; !slices.Equal(rt.calls, want) {
		t.Fatalf("calls = %q, want %q", rt.calls, want)
	}
	_, err = s.RestoreBackup(ctx, &noryxv1.RestoreBackupRequest{ServerId: serverID, BackupId: snapshot.GetId()})
	check(t, err)
	if read(path, "world/region/r.0.0.mca") != "griefed" {
		t.Fatal("restoring the snapshot didn't undo the restore")
	}

	label, kept := "Before the update", true
	updated, err := s.UpdateBackup(ctx, &noryxv1.UpdateBackupRequest{ServerId: serverID, BackupId: b.GetId(), Label: &label, Kept: &kept})
	check(t, err)
	want := proto.Clone(b).(*noryxv1.Backup)
	want.Label, want.Kept = label, true
	if !proto.Equal(updated.GetBackup(), want) {
		t.Fatalf("updated = %v, want %v", updated.GetBackup(), want)
	}
	// A kept backup survives its job's pruning until it is no longer kept.
	_, err = s.CreateBackup(ctx, &noryxv1.CreateBackupRequest{
		ServerId: serverID, JobId: "job1", Selection: &noryxv1.BackupSelection{Worlds: true}, Retention: &noryxv1.BackupRetention{Last: 1},
	})
	check(t, err)
	if _, err := s.store.Find(serverID, b.GetId()); err != nil {
		t.Fatal("pruning deleted a kept backup")
	}
	kept = false
	_, err = s.UpdateBackup(ctx, &noryxv1.UpdateBackupRequest{ServerId: serverID, BackupId: b.GetId(), Kept: &kept})
	check(t, err)
	_, err = s.CreateBackup(ctx, &noryxv1.CreateBackupRequest{
		ServerId: serverID, JobId: "job1", Selection: &noryxv1.BackupSelection{Worlds: true}, Retention: &noryxv1.BackupRetention{Last: 1},
	})
	check(t, err)
	if _, err := s.store.Find(serverID, b.GetId()); err == nil {
		t.Fatal("pruning kept a backup that is no longer kept")
	}

	long := string(slices.Repeat([]byte("x"), maxLabel+1))
	if _, err := s.UpdateBackup(ctx, &noryxv1.UpdateBackupRequest{ServerId: serverID, BackupId: snapshot.GetId(), Label: &long}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("a long label was accepted: %v", err)
	}
}

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// A backup whose files don't fit with the space kept free is refused before anything of it
// is extracted, also one that the agent made itself.
func TestRestoreKeepsSpaceFree(t *testing.T) {
	path, dir := paperData(t)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateRaw(&zip.FileHeader{Name: "world/huge.dat", Method: zip.Store, UncompressedSize64: 8 << 40, CompressedSize64: 1})
	check(t, err)
	_, err = w.Write([]byte{0})
	check(t, err)
	check(t, zw.Close())
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	check(t, err)
	staged, err := stage(t.Context(), dir, zr, []string{"world"})
	if !errors.As(err, new(storage.FullError)) || status.Code(toStatus(err)) != codes.ResourceExhausted {
		t.Fatalf("staged an archive of 8 TB: %v", err)
	}
	if _, err := os.Stat(filepath.Join(path, staged)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("extracted something: %v", err)
	}
}
