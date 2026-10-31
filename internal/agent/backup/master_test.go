package backup

import (
	"cmp"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/storage"
)

// masterStream sends an archive of the master in chunks, and then fails if err is set.
type masterStream struct {
	grpc.ServerStream
	msgs []*noryxv1.ImportMasterBackupRequest
	err  error
	res  *noryxv1.ImportMasterBackupResponse
}

func importMaster(header *noryxv1.MasterBackup, chunks ...string) *masterStream {
	s := &masterStream{msgs: []*noryxv1.ImportMasterBackupRequest{{Content: &noryxv1.ImportMasterBackupRequest_Header{Header: header}}}}
	for _, c := range chunks {
		s.msgs = append(s.msgs, &noryxv1.ImportMasterBackupRequest{Content: &noryxv1.ImportMasterBackupRequest_Data{Data: []byte(c)}})
	}
	return s
}

func (s *masterStream) Context() context.Context { return context.Background() }

func (s *masterStream) Recv() (*noryxv1.ImportMasterBackupRequest, error) {
	if len(s.msgs) == 0 {
		return nil, cmp.Or(s.err, io.EOF)
	}
	msg := s.msgs[0]
	s.msgs = s.msgs[1:]
	return msg, nil
}

func (s *masterStream) SendAndClose(res *noryxv1.ImportMasterBackupResponse) error {
	s.res = res
	return nil
}

// The agent keeps archives of the master as they are, where only it can read them, and deletes
// them again; names that could reach elsewhere are refused.
func TestMasterBackups(t *testing.T) {
	dataDir := t.TempDir()
	s := NewService(&fakeRuntime{}, storage.New(dataDir))
	name := noryxv1.MasterBackupName(time.Now(), true)
	in := importMaster(&noryxv1.MasterBackup{Name: name}, "age-encryption.org/v1\n", "the rest")
	check(t, s.ImportMasterBackup(in))
	path := filepath.Join(dataDir, "backups", "master", name)
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "age-encryption.org/v1\nthe rest" || in.res.GetSize() != int64(len(data)) {
		t.Fatalf("archive = %q, %v, size %d", data, err, in.res.GetSize())
	}
	for p, perm := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700} {
		if info, err := os.Stat(p); err != nil || info.Mode().Perm() != perm {
			t.Errorf("mode of %s = %v, %v, want %v", p, info.Mode().Perm(), err, perm)
		}
	}
	if err := s.ImportMasterBackup(importMaster(&noryxv1.MasterBackup{Name: name}, "other")); status.Code(err) != codes.AlreadyExists {
		t.Errorf("import over an archive: %v", err)
	}

	for _, header := range []*noryxv1.MasterBackup{
		{Name: "../" + name}, {Name: "master.db"}, {Name: name + "/x"}, {},
	} {
		if err := s.ImportMasterBackup(importMaster(header, "data")); status.Code(err) != codes.InvalidArgument {
			t.Errorf("import as %q: %v", header.GetName(), err)
		}
		if _, err := s.DeleteMasterBackup(t.Context(), &noryxv1.DeleteMasterBackupRequest{Backup: header}); status.Code(err) != codes.InvalidArgument {
			t.Errorf("delete %q: %v", header.GetName(), err)
		}
	}
	other := noryxv1.MasterBackupName(time.Now(), false)
	if err := s.ImportMasterBackup(importMaster(&noryxv1.MasterBackup{Name: other, Location: "elsewhere"}, "data")); status.Code(err) != codes.InvalidArgument {
		t.Errorf("import into an unknown location: %v", err)
	}
	// An upload that breaks off leaves nothing behind.
	broken := importMaster(&noryxv1.MasterBackup{Name: other}, "half")
	broken.err = errors.New("the master went away")
	if err := s.ImportMasterBackup(broken); err == nil {
		t.Error("broken upload succeeded")
	}
	if entries, err := os.ReadDir(filepath.Dir(path)); err != nil || len(entries) != 1 {
		t.Errorf("entries after a broken upload = %v, %v", entries, err)
	}

	if _, err := s.DeleteMasterBackup(t.Context(), &noryxv1.DeleteMasterBackupRequest{Backup: &noryxv1.MasterBackup{Name: name}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("deleted archive: %v", err)
	}
	if _, err := s.DeleteMasterBackup(t.Context(), &noryxv1.DeleteMasterBackupRequest{Backup: &noryxv1.MasterBackup{Name: name}}); status.Code(err) != codes.NotFound {
		t.Errorf("delete a deleted archive: %v", err)
	}
}
