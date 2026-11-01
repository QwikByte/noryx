package fileset

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/datadir"
	"github.com/QwikByte/noryx/internal/agent/runtime"
	"github.com/QwikByte/noryx/internal/agent/secrets"
)

const (
	server = "aaaaaaaaaaaaaaaaaaaaaaaaaa"
	setA   = "bbbbbbbbbbbbbbbbbbbbbbbbbb"
	setB   = "cccccccccccccccccccccccccc"
)

type fakeRuntime struct {
	runtime.Runtime
	dir string
}

func (f fakeRuntime) List(context.Context) ([]runtime.Server, error) {
	return []runtime.Server{{Spec: runtime.Spec{ID: server, Name: "Lobby"}}}, nil
}

func (f fakeRuntime) Data(_ context.Context, id string) (*datadir.Dir, error) {
	if id != server {
		return nil, runtime.ErrNotFound
	}
	return datadir.Open(f.dir)
}

func setup(t *testing.T) (*Service, string) {
	dir := t.TempDir()
	return NewService(fakeRuntime{dir: dir}), dir
}

func apply(t *testing.T, s *Service, set string, version int64, dry bool, files ...*noryxv1.FileSetFile) map[string]noryxv1.FileSetAction {
	t.Helper()
	res, err := s.ApplyFileSet(t.Context(), &noryxv1.ApplyFileSetRequest{
		ServerId: server, SetId: set, SetName: "Set " + set[:1], Version: version, Revision: "rev", Files: files, DryRun: dry,
		Secrets: map[string]string{"secret:db": "p4ss", "secret:lp": "dbp4ss"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return actions(res.GetChanges())
}

func actions(changes []*noryxv1.FileSetChange) map[string]noryxv1.FileSetAction {
	got := map[string]noryxv1.FileSetAction{}
	for _, c := range changes {
		got[c.GetPath()] = c.GetAction()
	}
	return got
}

func content(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
	if os.IsNotExist(err) {
		return "<none>"
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func write(t *testing.T, dir, name, data string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o640); err != nil {
		t.Fatal(err)
	}
}

func listed(t *testing.T, s *Service) map[string]*noryxv1.AppliedFile {
	t.Helper()
	res, err := s.ListFileSets(t.Context(), &noryxv1.ListFileSetsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]*noryxv1.AppliedFile{}
	for _, srv := range res.GetServers() {
		for _, set := range srv.GetSets() {
			for _, f := range set.GetFiles() {
				files[set.GetSetId()[:1]+":"+f.GetPath()] = f
			}
		}
	}
	return files
}

const (
	created   = noryxv1.FileSetAction_FILE_SET_ACTION_CREATED
	changed   = noryxv1.FileSetAction_FILE_SET_ACTION_CHANGED
	unchanged = noryxv1.FileSetAction_FILE_SET_ACTION_UNCHANGED
	removed   = noryxv1.FileSetAction_FILE_SET_ACTION_REMOVED
	kept      = noryxv1.FileSetAction_FILE_SET_ACTION_KEPT
)

func TestApply(t *testing.T) {
	s, dir := setup(t)
	chat := &noryxv1.FileSetFile{Path: "plugins/Chat/config.yml", Content: "format: '<{player}> {{player}}'\n"}
	lp := &noryxv1.FileSetFile{Path: "/plugins/LuckPerms/config.yml", Content: "password: {{secret:db}}\nother: {{secret:lp}}\n"}
	messages := &noryxv1.FileSetFile{Path: "plugins/Chat/messages.yml", Content: "hi: Hi\n", OnlyIfMissing: true}
	write(t, dir, "plugins/Chat/messages.yml", "hi: Hello\n")

	// A dry run tells what would happen and writes nothing.
	want := map[string]noryxv1.FileSetAction{"plugins/Chat/config.yml": created, "plugins/LuckPerms/config.yml": created, "plugins/Chat/messages.yml": unchanged}
	if got := apply(t, s, setA, 1, true, chat, lp, messages); !maps.Equal(got, want) {
		t.Fatalf("dry run = %v, want %v", got, want)
	}
	if content(t, dir, chat.Path) != "<none>" || content(t, dir, ManifestFile) != "<none>" {
		t.Fatal("a dry run wrote files")
	}
	if got := apply(t, s, setA, 1, false, chat, lp, messages); !maps.Equal(got, want) {
		t.Fatalf("apply = %v, want %v", got, want)
	}
	// Other double braces stay, secrets are filled in, and a file the server has stays.
	if got := content(t, dir, chat.Path); got != chat.Content {
		t.Errorf("chat config = %q", got)
	}
	if got := content(t, dir, "plugins/LuckPerms/config.yml"); got != "password: p4ss\nother: dbp4ss\n" {
		t.Errorf("LuckPerms config = %q", got)
	}
	if got := content(t, dir, messages.Path); got != "hi: Hello\n" {
		t.Errorf("messages = %q, want the server's own", got)
	}

	// The file with secrets is hidden, as is the manifest; the others show their set.
	data, err := datadir.Open(dir)
	check(t, err)
	defer data.Close()
	files := Read(data)
	hidden := files.Secrets()
	if !hidden.Hidden(filepath.FromSlash("plugins/LuckPerms/config.yml")) || !hidden.Hidden(ManifestFile) || hidden.Hidden(filepath.FromSlash(chat.Path)) {
		t.Error("the files with secrets aren't hidden")
	}
	if got := files.Set(filepath.FromSlash(chat.Path)); got != "Set b" {
		t.Errorf("set of the chat config = %q", got)
	}

	// The status tells changed files; files only written if missing never count as written.
	write(t, dir, chat.Path, "changed\n")
	write(t, dir, messages.Path, "hi: Hey\n")
	got := listed(t, s)
	if !got["b:"+chat.Path].GetChanged() || got["b:"+messages.Path].GetChanged() || !got["b:plugins/LuckPerms/config.yml"].GetSecret() {
		t.Errorf("listed = %v", got)
	}

	// A new version replaces changed files, removes what it no longer has if unchanged,
	// keeps it if changed, and always removes files with secrets.
	write(t, dir, "plugins/Chat/messages.yml", "hi: Hello\n")
	if got, want := apply(t, s, setA, 2, false, chat), map[string]noryxv1.FileSetAction{
		chat.Path: changed, "plugins/LuckPerms/config.yml": removed, messages.Path: kept,
	}; !maps.Equal(got, want) {
		t.Fatalf("second apply = %v, want %v", got, want)
	}
	if content(t, dir, "plugins/LuckPerms/config.yml") != "<none>" || content(t, dir, messages.Path) != "hi: Hello\n" {
		t.Error("the files the set left are wrong")
	}
	// The path that held secrets stays hidden.
	if !Read(data).Secrets().Hidden(filepath.FromSlash("plugins/LuckPerms/config.yml")) {
		t.Error("a file that held secrets isn't hidden anymore")
	}

	// A path comes from one set only.
	_, err = s.ApplyFileSet(t.Context(), &noryxv1.ApplyFileSetRequest{ServerId: server, SetId: setB, SetName: "B", Version: 1, Files: []*noryxv1.FileSetFile{chat}})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("second set with the same path: %v", err)
	}
}

func TestApplyRefuses(t *testing.T) {
	s, dir := setup(t)
	write(t, dir, "plugins/Link", "a file where a folder should be")
	for name, f := range map[string]*noryxv1.FileSetFile{
		"unknown secret":      {Path: "a.yml", Content: "x: {{secret:nope}}"},
		"variable":            {Path: "a.yml", Content: "x: {{server.name}}"},
		"managed file":        {Path: "server.properties", Content: "motd=hi"},
		"secret file":         {Path: "plugins/floodgate/key.pem", Content: "key"},
		"manifest":            {Path: ManifestFile, Content: "{}"},
		"plugin":              {Path: "plugins/x.jar", Content: "PK"},
		"binary":              {Path: "a.yml", Content: "\x00"},
		"escape":              {Path: "../a.yml", Content: "x"},
		"file as folder":      {Path: "plugins/Link/config.yml", Content: "x"},
		"agent's temporaries": {Path: ".noryx-x/a.yml", Content: "x"},
	} {
		_, err := s.ApplyFileSet(t.Context(), &noryxv1.ApplyFileSetRequest{
			ServerId: server, SetId: setA, SetName: "A", Version: 1, Files: []*noryxv1.FileSetFile{{Path: "ok.yml", Content: "ok"}, f},
			Secrets: map[string]string{"secret:db": "p4ss"},
		})
		if code := status.Code(err); code != codes.InvalidArgument && code != codes.FailedPrecondition {
			t.Errorf("%s: %v", name, err)
		}
	}
	if content(t, dir, "ok.yml") != "<none>" {
		t.Error("a refused set wrote files")
	}
	_, err := s.ApplyFileSet(t.Context(), &noryxv1.ApplyFileSetRequest{
		ServerId: server, SetId: setA, SetName: "A", Version: 1, Secrets: map[string]string{"secret:db": "two\nlines"},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("secret with a line break: %v", err)
	}
}

// Binary files are written as they are, and passwords of databases are filled in like secrets,
// so that their files are hidden.
func TestBinaryFilesAndPasswords(t *testing.T) {
	s, dir := setup(t)
	const password = "abcdefghijklmnopqrstuvwxyz234567"
	icon := &noryxv1.FileSetFile{Path: "server-icon.png", Data: []byte("\x89PNG\r\n\x1a\n{{secret:db}}\x00")}
	lp := &noryxv1.FileSetFile{Path: "plugins/LuckPerms/config.yml", Content: "password: {{datastore:main.luckperms.password}}\n"}
	key := "datastore:main.luckperms.password"
	req := &noryxv1.ApplyFileSetRequest{
		ServerId: server, SetId: setA, SetName: "A", Version: 1, Files: []*noryxv1.FileSetFile{icon, lp}, Secrets: map[string]string{key: password},
	}
	_, err := s.ApplyFileSet(t.Context(), req)
	check(t, err)
	if got := content(t, dir, icon.Path); got != string(icon.Data) {
		t.Errorf("icon = %q", got)
	}
	if got := content(t, dir, lp.Path); got != "password: "+password+"\n" {
		t.Errorf("LuckPerms config = %q", got)
	}
	data, err := datadir.Open(dir)
	check(t, err)
	defer data.Close()
	if hidden := Read(data).Secrets(); !hidden.Hidden(filepath.FromSlash(lp.Path)) || hidden.Hidden(icon.Path) {
		t.Error("the file with the password isn't hidden, or the icon is")
	}

	for name, bad := range map[string]*noryxv1.FileSetFile{
		"text and binary": {Path: "a.png", Content: "x", Data: []byte("y")},
		"archive":         {Path: "a.png", Data: []byte("PK\x03\x04rest")},
		"class":           {Path: "a.dat", Data: []byte("\xca\xfe\xba\xbe")},
		"program":         {Path: "run", Data: []byte("\x7fELF")},
		"too large":       {Path: "a.png", Data: make([]byte, 1<<20+1)},
		"variable":        {Path: "a.yml", Content: "{{var:role}}"},
	} {
		_, err := s.ApplyFileSet(t.Context(), &noryxv1.ApplyFileSetRequest{ServerId: server, SetId: setB, SetName: "B", Version: 1, Files: []*noryxv1.FileSetFile{bad}})
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, values := range map[string]map[string]string{
		"invalid password":    {key: "two\nlines"},
		"host as a secret":    {"datastore:main.luckperms.host": password},
		"invalid placeholder": {"datastore:main.luckperms": password},
	} {
		_, err := s.ApplyFileSet(t.Context(), &noryxv1.ApplyFileSetRequest{ServerId: server, SetId: setB, SetName: "B", Version: 1, Secrets: values})
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestRemove(t *testing.T) {
	s, dir := setup(t)
	files := []*noryxv1.FileSetFile{
		{Path: "a.yml", Content: "a"}, {Path: "b.yml", Content: "b"}, {Path: "secret.yml", Content: "{{secret:db}}"},
	}
	remove := func(removal noryxv1.FileSetRemoval) map[string]noryxv1.FileSetAction {
		res, err := s.RemoveFileSet(t.Context(), &noryxv1.RemoveFileSetRequest{ServerId: server, SetId: setA, Removal: removal})
		check(t, err)
		return actions(res.GetChanges())
	}
	apply(t, s, setA, 1, false, files...)
	write(t, dir, "b.yml", "changed")

	// Leaving a target removes the files with secrets right away; the others stay in the set,
	// which no longer has the state the master sent.
	if got := remove(noryxv1.FileSetRemoval_FILE_SET_REMOVAL_SECRETS); got["secret.yml"] != removed || got["a.yml"] != unchanged {
		t.Errorf("removing the secrets = %v", got)
	}
	if content(t, dir, "secret.yml") != "<none>" || content(t, dir, "a.yml") != "a" || len(listed(t, s)) != 2 {
		t.Error("the files with secrets weren't removed alone")
	}
	res, err := s.ListFileSets(t.Context(), &noryxv1.ListFileSetsRequest{})
	check(t, err)
	if rev := res.GetServers()[0].GetSets()[0].GetRevision(); rev != "" {
		t.Errorf("revision after removing the secrets = %q", rev)
	}
	// Then the unchanged files go, the changed stay, and those gone already need nothing.
	if got := remove(noryxv1.FileSetRemoval_FILE_SET_REMOVAL_UNCHANGED); got["a.yml"] != removed || got["b.yml"] != kept {
		t.Errorf("removing the set = %v", got)
	}
	if content(t, dir, "a.yml") != "<none>" || content(t, dir, "b.yml") != "changed" || len(listed(t, s)) != 0 {
		t.Error("the set wasn't removed")
	}

	// A deleted set leaves its files without secrets.
	apply(t, s, setA, 1, false, files...)
	remove(noryxv1.FileSetRemoval_FILE_SET_REMOVAL_FORGET)
	if content(t, dir, "a.yml") != "a" || content(t, dir, "secret.yml") != "<none>" || len(listed(t, s)) != 0 {
		t.Error("forgetting the set went wrong")
	}
}

func TestStandalone(t *testing.T) {
	s, dir := setup(t)
	apply(t, s, setA, 1, false, &noryxv1.FileSetFile{Path: "a.yml", Content: "a"}, &noryxv1.FileSetFile{Path: "secret.yml", Content: "{{secret:db}}"})
	copied := filepath.Join(t.TempDir(), "copy")
	check(t, datadir.Copy(t.Context(), dir, copied))
	// A set wrote another file with secrets to the original while it was copied.
	write(t, copied, "late.yml", "password: p4ss")
	original, err := datadir.Open(dir)
	check(t, err)
	defer original.Close()
	m, err := read(original)
	check(t, err)
	m.Marked = append(m.Marked, "late.yml")
	check(t, m.write(original))

	data, err := datadir.Open(copied)
	check(t, err)
	defer data.Close()
	check(t, Standalone(data, original))
	if content(t, copied, "secret.yml") != "<none>" || content(t, copied, "late.yml") != "<none>" || content(t, copied, "a.yml") != "a" {
		t.Error("the copy kept the secrets")
	}
	copy, err := read(data)
	check(t, err)
	if set := copy.Sets[setA]; set == nil || len(set.Files) != 1 || set.Revision != "" || !Read(data).Secrets().Hidden("late.yml") {
		t.Errorf("manifest of the copy = %+v", copy)
	}
}

func TestOnlyIfMissingKeepsTheServersFile(t *testing.T) {
	s, dir := setup(t)
	write(t, dir, "own.yml", "my own config")
	f := &noryxv1.FileSetFile{Path: "own.yml", Content: "password: {{secret:db}}", OnlyIfMissing: true}
	if got := apply(t, s, setA, 1, false, f); got["own.yml"] != unchanged {
		t.Fatalf("apply = %v", got)
	}
	data, err := datadir.Open(dir)
	check(t, err)
	defer data.Close()
	if Read(data).Secrets().Hidden("own.yml") {
		t.Error("the server's own file is hidden")
	}
	for _, removal := range []noryxv1.FileSetRemoval{noryxv1.FileSetRemoval_FILE_SET_REMOVAL_SECRETS, noryxv1.FileSetRemoval_FILE_SET_REMOVAL_UNCHANGED} {
		_, err := s.RemoveFileSet(t.Context(), &noryxv1.RemoveFileSetRequest{ServerId: server, SetId: setA, Removal: removal})
		check(t, err)
	}
	if content(t, dir, "own.yml") != "my own config" {
		t.Error("taking the set off removed the server's own file")
	}
}

func TestWatch(t *testing.T) {
	s, dir := setup(t)
	data, err := datadir.Open(dir)
	check(t, err)
	defer data.Close()
	hidden := Watch(data)
	if hidden().Hidden("secret.yml") {
		t.Fatal("hidden before the set was applied")
	}
	apply(t, s, setA, 1, false, &noryxv1.FileSetFile{Path: "secret.yml", Content: "{{secret:db}}"})
	if !hidden().Hidden("secret.yml") {
		t.Error("a file with secrets written meanwhile isn't hidden")
	}
}

func TestDamagedManifest(t *testing.T) {
	s, dir := setup(t)
	write(t, dir, ManifestFile, `{"sets": {"bad": {"files": {"x": {}}}, "`+setA+`": {"name": "A", "version": 1, "files": {"../x": {}, "a.yml": {}}}}, "marked": ["../etc/passwd", "s.yml"]}`)
	data, err := datadir.Open(dir)
	check(t, err)
	defer data.Close()
	m, err := read(data)
	check(t, err)
	if len(m.Sets) != 1 || len(m.Sets[setA].Files) != 1 || !slices.Equal(m.Marked, []string{"s.yml"}) {
		t.Errorf("manifest = %+v", m)
	}
	// One that isn't JSON refuses changes, so that what it marks isn't lost.
	write(t, dir, ManifestFile, "{")
	_, err = s.ApplyFileSet(t.Context(), &noryxv1.ApplyFileSetRequest{ServerId: server, SetId: setA, SetName: "A", Version: 1})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("apply with a damaged manifest: %v", err)
	}
}

// TestRefusedSecrets checks that the master refuses every file with secrets of servers too,
// which it can't ask this package.
func TestRefusedSecrets(t *testing.T) {
	for _, p := range secrets.With().Under(".") {
		if _, problem := noryxv1.CleanFileSetPath(p); problem == "" {
			t.Errorf("%s isn't refused", p)
		}
	}
}

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// A huge file where a set wrote one, e.g. sparse, as a compromised server could make it, isn't read.
func TestHugeFileIsNotRead(t *testing.T) {
	dir, err := datadir.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	f, err := dir.Create("huge.yml")
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(f.Truncate(1<<40), f.Close()); err != nil {
		t.Fatal(err)
	}
	if sum, exists, err := digest(dir, "huge.yml"); sum != "" || !exists || err != nil {
		t.Fatalf("digest = %q, %v, %v", sum, exists, err)
	}
}
