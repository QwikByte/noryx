package datastore

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/storage"
)

// zipOf returns a ZIP archive with files of the given names and contents, deflated if
// deflate is set, otherwise stored.
func zipOf(t *testing.T, deflate bool, files ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := 0; i < len(files); i += 2 {
		method := zip.Store
		if deflate {
			method = zip.Deflate
		}
		w, err := zw.CreateHeader(&zip.FileHeader{Name: files[i], Method: method})
		must(t, err)
		_, err = io.WriteString(w, files[i+1])
		must(t, err)
	}
	must(t, zw.Close())
	return buf.Bytes()
}

// An uploaded archive may only hold a <database>.sql for each database, besides folders and
// what macOS adds, and must be complete and unpack to a sensible size.
func TestSQLFiles(t *testing.T) {
	read := func(data []byte) (map[string]*zip.File, error) {
		zr, err := openDump(bytes.NewReader(data), int64(len(data)))
		must(t, err)
		return sqlFiles(zr, int64(len(data)))
	}
	files, err := read(zipOf(t, true, "luckperms.sql", "CREATE TABLE a;", "litebans.sql", "", "__MACOSX/._luckperms.sql", "junk", "folder/", ""))
	must(t, err)
	if names := slices.Sorted(maps.Keys(files)); !slices.Equal(names, []string{"litebans", "luckperms"}) {
		t.Fatalf("files %v", names)
	}

	damaged := zipOf(t, false, "luckperms.sql", "CREATE TABLE a;")
	damaged[bytes.Index(damaged, []byte("CREATE"))] = 'X'
	many := []string{}
	for i := range maxEntries + 1 {
		many = append(many, fmt.Sprintf("db%d.sql", i), "")
	}
	tooManyDatabases := []string{}
	for i := range noryxv1.MaxDatabases + 1 {
		tooManyDatabases = append(tooManyDatabases, fmt.Sprintf("db%d.sql", i), "")
	}
	for name, data := range map[string][]byte{
		"a path":          zipOf(t, true, "../luckperms.sql", ""),
		"a folder":        zipOf(t, true, "dump/luckperms.sql", ""),
		"upper case":      zipOf(t, true, "LuckPerms.sql", ""),
		"a reserved name": zipOf(t, true, "mysql.sql", ""),
		"another file":    zipOf(t, true, "luckperms.sql", "", "readme.txt", ""),
		"twice":           zipOf(t, true, "luckperms.sql", "", "luckperms.sql", ""),
		"nothing":         zipOf(t, true, "folder/", ""),
		"damaged":         damaged,
		"too many files":  zipOf(t, false, many...),
		"too many":        zipOf(t, false, tooManyDatabases...),
		"far more inside": zipOf(t, true, "luckperms.sql", strings.Repeat("\x00", 101<<20)),
	} {
		if _, err := read(data); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// An archive whose ZIP64 end announces more entries than a dump may have is refused before
// archive/zip reserves memory for them, as is a file that is no ZIP archive.
func TestOpenDumpChecksEnd(t *testing.T) {
	const size = 64 << 10
	le := binary.LittleEndian
	end := le.AppendUint32(nil, 0x06064b50) // the ZIP64 end
	end = le.AppendUint64(end, 44)
	end = append(end, 45, 0, 45, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	end = le.AppendUint64(end, maxEntries+1) // entries on this disk
	end = le.AppendUint64(end, maxEntries+1) // and in all
	end = le.AppendUint64(end, 46)           // the size of the directory
	end = le.AppendUint64(end, size-56-20-22-46)
	end = le.AppendUint32(end, 0x07064b50) // the locator of the ZIP64 end
	end = le.AppendUint32(end, 0)
	end = le.AppendUint64(end, size-56-20-22)
	end = le.AppendUint32(end, 1)
	end = le.AppendUint32(end, 0x06054b50) // the end, which leaves the numbers to the ZIP64 end
	end = append(end, 0, 0, 0, 0, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0, 0)
	data := append(make([]byte, size-len(end)), end...)
	if _, err := openDump(bytes.NewReader(data), size); status.Code(err) != codes.ResourceExhausted {
		t.Errorf("announced entries: %v", err)
	}
	if _, err := openDump(strings.NewReader("CREATE TABLE a;"), 15); status.Code(err) != codes.InvalidArgument {
		t.Errorf("no archive: %v", err)
	}
}

// upload sends a dump of unknown size to ImportDump: header, then size bytes that don't
// compress.
type upload struct {
	grpc.ServerStream
	ctx    context.Context
	header *noryxv1.ImportDumpHeader
	block  []byte
	size   int
}

func (u *upload) Context() context.Context { return u.ctx }

func (u *upload) Recv() (*noryxv1.ImportDumpRequest, error) {
	switch {
	case u.header != nil:
		h := u.header
		u.header = nil
		return &noryxv1.ImportDumpRequest{Content: &noryxv1.ImportDumpRequest_Header{Header: h}}, nil
	case u.size <= 0:
		return nil, io.EOF
	}
	u.size -= len(u.block)
	return &noryxv1.ImportDumpRequest{Content: &noryxv1.ImportDumpRequest_Data{Data: u.block}}, nil
}

func (u *upload) SendAndClose(*noryxv1.ImportDumpResponse) error { return nil }

// An upload without a size stops before it leaves less than storage.MinFree free, both the
// SQL of a database and an archive. It needs root, to mount a small file system.
func TestImportKeepsSpaceFree(t *testing.T) {
	dir := t.TempDir()
	if err := unix.Mount("tmpfs", dir, "tmpfs", 0, fmt.Sprintf("size=%d", storage.MinFree+32<<20)); err != nil {
		t.Skipf("can't mount a file system: %v", err)
	}
	t.Cleanup(func() { must(t, unix.Unmount(dir, 0)) })
	s, _, _, id := newServiceIn(t, dir)
	block := make([]byte, 1<<20)
	_, _ = rand.Read(block)
	for _, database := range []string{"", "shop"} {
		u := &upload{ctx: t.Context(), header: &noryxv1.ImportDumpHeader{Id: id, Database: database}, block: block, size: 256 << 20}
		if err := s.ImportDump(u); status.Code(err) != codes.ResourceExhausted {
			t.Errorf("database %q: %v", database, err)
		}
	}
	if dumps, err := s.dumps.List(owner(id)); err != nil || len(dumps) > 0 {
		t.Errorf("dumps %v, %v", dumps, err)
	}
}
