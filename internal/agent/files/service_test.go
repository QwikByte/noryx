package files

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"io"
	"maps"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/datadir"
	"github.com/QwikByte/noryx/internal/agent/runtime"
)

// An editor's save doesn't replace a file that changed since it was opened, unless asked to.
func TestWriteExpectedVersion(t *testing.T) {
	rt := dataRuntime{dir: t.TempDir()}
	svc, id := NewService(rt), runtime.NewID()
	file := filepath.Join(rt.dir, id, "server.properties")
	if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("motd=A\nrcon.password=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	opened := read(t, svc, id, "server.properties")
	if opened.GetSize() == 0 || opened.GetModifiedUnixNano() == 0 {
		t.Fatalf("version = %v", opened)
	}
	saved, err := write(svc, id, "server.properties", opened, "motd=B\nrcon.password=<hidden>\n")
	if err != nil {
		t.Fatal(err)
	}
	if saved.GetVersion().GetSize() != int64(len("motd=B\nrcon.password=secret\n")) {
		t.Fatalf("version after saving = %v", saved.GetVersion())
	}

	// Another save based on the first version fails, also once the file is gone.
	if _, err := write(svc, id, "server.properties", opened, "motd=C\n"); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("save of an old version: %v", err)
	}
	modified := time.Unix(0, saved.GetVersion().GetModifiedUnixNano()).Add(time.Second)
	if err := os.Chtimes(file, modified, modified); err != nil {
		t.Fatal(err)
	}
	if _, err := write(svc, id, "server.properties", saved.GetVersion(), "motd=C\n"); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("save after the file was touched: %v", err)
	}
	if _, err := write(svc, id, "gone.yml", opened, "a: b\n"); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("save of a file that is gone: %v", err)
	}
	if got, _ := os.ReadFile(file); string(got) != "motd=B\nrcon.password=secret\n" {
		t.Fatalf("file = %q", got)
	}

	// Without an expected version, e.g. as an upload replaces a file, it is replaced.
	if _, err := write(svc, id, "server.properties", nil, "motd=D\nrcon.password=<hidden>\n"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(file); string(got) != "motd=D\nrcon.password=secret\n" {
		t.Fatalf("file = %q", got)
	}
}

// An archive of chosen files and folders holds only them, without secrets and links.
func TestArchiveChosen(t *testing.T) {
	rt := dataRuntime{dir: t.TempDir()}
	svc, id := NewService(rt), runtime.NewID()
	data := filepath.Join(rt.dir, id)
	for name, content := range map[string]string{
		"server.properties": "motd=A\nrcon.password=secret\n",
		".rcon-cli.env":     "password=secret\n",
		"plugins/a.yml":     "a: 1\n",
		"plugins/b.yml":     "b: 1\n",
		"world/level.dat":   "level",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(data, name)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(data, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("server.properties", filepath.Join(data, "link")); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		dir   string
		paths []string
		want  map[string]string
	}{
		{"", []string{"plugins", "plugins/a.yml", "server.properties", ".rcon-cli.env", "link", "plugins"}, map[string]string{
			"plugins/": "", "plugins/a.yml": "a: 1\n", "plugins/b.yml": "b: 1\n", "server.properties": "motd=A\nrcon.password=<hidden>\n",
		}},
		{"plugins", []string{"b.yml"}, map[string]string{"b.yml": "b: 1\n"}},
		{"world", nil, map[string]string{"level.dat": "level"}},
	} {
		stream := &archiveStream{ctx: t.Context()}
		err := svc.ArchiveDirectory(&noryxv1.ArchiveDirectoryRequest{ServerId: id, Path: c.dir, Paths: c.paths, HideSecrets: true}, stream)
		if err != nil {
			t.Fatalf("archive of %q in %q: %v", c.paths, c.dir, err)
		}
		if got := unzip(t, stream.data.Bytes()); !maps.Equal(got, c.want) {
			t.Errorf("archive of %q in %q = %q, want %q", c.paths, c.dir, got, c.want)
		}
	}

	for p, code := range map[string]codes.Code{"../x": codes.InvalidArgument, "missing": codes.NotFound} {
		err := svc.ArchiveDirectory(&noryxv1.ArchiveDirectoryRequest{ServerId: id, Paths: []string{"plugins", p}}, &archiveStream{ctx: t.Context()})
		if status.Code(err) != code {
			t.Errorf("archive of %q: %v, want %v", p, err, code)
		}
	}
}

// A folder can't be moved into itself, which the kernel would refuse with a less clear error.
func TestMoveIntoItself(t *testing.T) {
	rt := dataRuntime{dir: t.TempDir()}
	svc, id := NewService(rt), runtime.NewID()
	if err := os.MkdirAll(filepath.Join(rt.dir, id, "plugins", "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	_, err := svc.MoveFile(t.Context(), &noryxv1.MoveFileRequest{ServerId: id, From: "plugins", To: "plugins/sub/plugins"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("move into itself: %v", err)
	}
	if _, err := svc.MoveFile(t.Context(), &noryxv1.MoveFileRequest{ServerId: id, From: "plugins/sub", To: "sub"}); err != nil {
		t.Fatal(err)
	}
}

// Only regular files without secrets are hashed, and paths stay in the server's folder.
func TestHashFiles(t *testing.T) {
	rt := dataRuntime{dir: t.TempDir()}
	svc, id := NewService(rt), runtime.NewID()
	data := filepath.Join(rt.dir, id)
	for name, content := range map[string]string{
		"mods/a.jar": "a", "server.properties": "rcon.password=secret\n", ".rcon-cli.env": "password=secret\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(data, name)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(data, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("a.jar", filepath.Join(data, "mods", "link.jar")); err != nil {
		t.Fatal(err)
	}
	paths := []string{"mods/a.jar", "mods/link.jar", "mods", "server.properties", ".rcon-cli.env", "mods/gone.jar"}
	res, err := svc.HashFiles(t.Context(), &noryxv1.HashFilesRequest{ServerId: id, Paths: paths})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha512.Sum512([]byte("a"))
	want := map[string]string{"mods/a.jar": hex.EncodeToString(sum[:]), "mods/link.jar": "", "mods": "", "server.properties": "", ".rcon-cli.env": ""}
	if !maps.Equal(res.GetSha512(), want) {
		t.Fatalf("hashes = %v", res.GetSha512())
	}
	for _, paths := range [][]string{{"../other/a.jar"}, make([]string, noryxv1.MaxHashPaths+1)} {
		if _, err := svc.HashFiles(t.Context(), &noryxv1.HashFilesRequest{ServerId: id, Paths: paths}); status.Code(err) != codes.InvalidArgument {
			t.Errorf("hash %d paths, the first %q: %v", len(paths), paths[0], err)
		}
	}
}

func unzip(t *testing.T, data []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		files[f.Name] = string(content)
	}
	return files
}

// A part of a file can be read, e.g. the end of a large log, also of a file with secrets.
func TestReadPart(t *testing.T) {
	rt := dataRuntime{dir: t.TempDir()}
	svc, id := NewService(rt), runtime.NewID()
	if err := os.MkdirAll(filepath.Join(rt.dir, id, "logs"), 0o750); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"logs/latest.log": "0123456789", "server.properties": "rcon.password=secret\nmotd=A\n"} {
		if err := os.WriteFile(filepath.Join(rt.dir, id, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name          string
		offset, limit int64
		want          string
		start, total  int64
	}{
		{"logs/latest.log", 0, 0, "0123456789", 0, 0}, // the whole file, without a range
		{"logs/latest.log", -4, 0, "6789", 6, 10},
		{"logs/latest.log", -40, 0, "0123456789", 0, 10},
		{"logs/latest.log", 2, 3, "234", 2, 10},
		{"logs/latest.log", 8, 5, "89", 8, 10},
		{"logs/latest.log", 12, 0, "", 10, 10},
		{"server.properties", -7, 0, "motd=A\n", 23, 30}, // counted with the secret as <hidden>
	} {
		stream := &readStream{ctx: t.Context()}
		if err := svc.ReadFile(&noryxv1.ReadFileRequest{ServerId: id, Path: tc.name, Offset: tc.offset, Limit: tc.limit}, stream); err != nil {
			t.Fatal(err)
		}
		if got := stream.first; string(stream.data) != tc.want || got.GetSize() != int64(len(tc.want)) ||
			got.GetOffset() != tc.start || got.GetFileSize() != tc.total {
			t.Errorf("read %s from %d, %d bytes: %q, size %d at %d of %d", tc.name, tc.offset, tc.limit, stream.data,
				got.GetSize(), got.GetOffset(), got.GetFileSize())
		}
	}
	err := svc.ReadFile(&noryxv1.ReadFileRequest{ServerId: id, Path: "logs/latest.log", Limit: -1}, &readStream{ctx: t.Context()})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("negative limit: %v", err)
	}
}

// The temporary files and folders of the agent, e.g. a backup staged with the server's
// forwarding secret while it is restored, are neither listed nor reachable by any path.
func TestTemporaryFiles(t *testing.T) {
	rt := dataRuntime{dir: t.TempDir()}
	svc, id := NewService(rt), runtime.NewID()
	data := filepath.Join(rt.dir, id)
	files(t, data, map[string]string{".noryx-staged/forwarding.secret": "secret", "plugins/.noryx-written": "token: t", "world/level.dat": "level"})
	ctx := t.Context()
	list, err := svc.ListFiles(ctx, &noryxv1.ListFilesRequest{ServerId: id, Path: "."})
	if err != nil || len(list.GetFiles()) != 2 {
		t.Fatalf("listed %v, %v", list.GetFiles(), err)
	}
	for name, call := range map[string]func(string) error{
		"list": func(p string) error {
			_, err := svc.ListFiles(ctx, &noryxv1.ListFilesRequest{ServerId: id, Path: p})
			return err
		},
		"read": func(p string) error {
			return svc.ReadFile(&noryxv1.ReadFileRequest{ServerId: id, Path: p}, &readStream{ctx: ctx})
		},
		"write": func(p string) error { _, err := write(svc, id, p, nil, "x"); return err },
		"delete": func(p string) error {
			_, err := svc.DeleteFile(ctx, &noryxv1.DeleteFileRequest{ServerId: id, Path: p})
			return err
		},
		"move from": func(p string) error {
			_, err := svc.MoveFile(ctx, &noryxv1.MoveFileRequest{ServerId: id, From: p, To: "out"})
			return err
		},
		"move to": func(p string) error {
			_, err := svc.MoveFile(ctx, &noryxv1.MoveFileRequest{ServerId: id, From: "world/level.dat", To: p})
			return err
		},
		"copy from": func(p string) error {
			_, err := svc.CopyFile(ctx, &noryxv1.CopyFileRequest{ServerId: id, From: p, To: "out"})
			return err
		},
		"download": func(p string) error {
			return svc.ArchiveDirectory(&noryxv1.ArchiveDirectoryRequest{ServerId: id, Path: p}, &archiveStream{ctx: ctx})
		},
		"archive": func(p string) error {
			return svc.ArchiveDirectory(&noryxv1.ArchiveDirectoryRequest{ServerId: id, Paths: []string{"world", p}}, &archiveStream{ctx: ctx})
		},
		"extract into": func(p string) error {
			_, err := svc.ExtractArchive(ctx, &noryxv1.ExtractArchiveRequest{ServerId: id, Path: "world/level.dat", Destination: p})
			return err
		},
		"search": func(p string) error {
			_, err := svc.SearchFiles(ctx, &noryxv1.SearchFilesRequest{ServerId: id, Path: p, Query: "secret"})
			return err
		},
	} {
		for _, p := range []string{".noryx-staged", ".noryx-staged/forwarding.secret", "plugins/.noryx-written"} {
			if err := call(p); status.Code(err) != codes.InvalidArgument {
				t.Errorf("%s %s: %v", name, p, err)
			}
		}
	}
	if got, _ := os.ReadFile(filepath.Join(data, ".noryx-staged/forwarding.secret")); string(got) != "secret" {
		t.Fatalf("forwarding.secret = %q", got)
	}
}

func read(t *testing.T, svc *Service, id, name string) *noryxv1.FileVersion {
	t.Helper()
	stream := &readStream{ctx: t.Context()}
	if err := svc.ReadFile(&noryxv1.ReadFileRequest{ServerId: id, Path: name}, stream); err != nil {
		t.Fatal(err)
	}
	return stream.first.GetVersion()
}

func write(svc *Service, id, name string, expected *noryxv1.FileVersion, content string) (*noryxv1.WriteFileResponse, error) {
	stream := &writeStream{ctx: context.Background(), msgs: []*noryxv1.WriteFileRequest{
		{Content: &noryxv1.WriteFileRequest_Header{Header: &noryxv1.WriteFileHeader{ServerId: id, Path: name, Overwrite: true, Expected: expected}}},
		{Content: &noryxv1.WriteFileRequest_Data{Data: []byte(content)}},
	}}
	err := svc.WriteFile(stream)
	return stream.res, err
}

// dataRuntime only has the data of servers, each in a folder named after it.
type dataRuntime struct {
	runtime.Runtime
	dir string
}

func (r dataRuntime) Data(_ context.Context, id string) (*datadir.Dir, error) {
	return datadir.Open(filepath.Join(r.dir, id))
}

type readStream struct {
	grpc.ServerStream
	ctx   context.Context
	first *noryxv1.ReadFileResponse
	data  []byte
}

func (s *readStream) Context() context.Context { return s.ctx }

func (s *readStream) Send(res *noryxv1.ReadFileResponse) error {
	if s.first == nil {
		s.first = res
	}
	s.data = append(s.data, res.GetData()...)
	return nil
}

type archiveStream struct {
	grpc.ServerStream
	ctx  context.Context
	data bytes.Buffer
}

func (s *archiveStream) Context() context.Context { return s.ctx }

func (s *archiveStream) Send(res *noryxv1.ArchiveDirectoryResponse) error {
	s.data.Write(res.GetData())
	return nil
}

type writeStream struct {
	grpc.ServerStream
	ctx  context.Context
	msgs []*noryxv1.WriteFileRequest
	res  *noryxv1.WriteFileResponse
}

func (s *writeStream) Context() context.Context { return s.ctx }

func (s *writeStream) Recv() (*noryxv1.WriteFileRequest, error) {
	if len(s.msgs) == 0 {
		return nil, io.EOF
	}
	msg := s.msgs[0]
	s.msgs = s.msgs[1:]
	return msg, nil
}

func (s *writeStream) SendAndClose(res *noryxv1.WriteFileResponse) error {
	s.res = res
	return nil
}
