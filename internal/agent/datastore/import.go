package datastore

import (
	"archive/zip"
	"cmp"
	"errors"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/archive"
	"github.com/QwikByte/noryx/internal/agent/backup"
	"github.com/QwikByte/noryx/internal/agent/storage"
)

// Limits of an uploaded ZIP archive, so that it can't unpack to far more than it holds.
const (
	maxEntries  = 1000     // files and folders
	maxUnpacked = 64 << 30 // bytes of its SQL files together
	maxRatio    = 100      // of these to the bytes of the archive, beyond its first MiB
)

// dumpLimits are checked before archive/zip reads the directory of an uploaded archive, which
// has about 100 bytes for each entry.
var dumpLimits = archive.Limits{Entries: maxEntries, Size: maxUnpacked, Directory: 1 << 20}

var errTooLarge = status.Errorf(codes.InvalidArgument, "A dump can have up to %d GB.", noryxv1.MaxImportedDump>>30)

// ImportDump adds a dump made elsewhere: a ZIP archive with a <database>.sql for each database,
// or the SQL of one database. An archive is checked before the dump shows up, and only its SQL
// files are kept, so that restoring it finds what it would in a dump made here.
func (s *Service) ImportDump(stream noryxv1.DatastoreService_ImportDumpServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	h := first.GetHeader()
	label := strings.TrimSpace(h.GetLabel())
	switch {
	case h == nil:
		return status.Error(codes.InvalidArgument, "the first message must describe the dump")
	case h.GetSize() < 0 || h.GetSize() > noryxv1.MaxImportedDump:
		return errTooLarge
	case h.GetDatabase() != "" && noryxv1.DatabaseNameProblem(h.GetDatabase()) != "":
		return status.Error(codes.InvalidArgument, noryxv1.DatabaseNameProblem(h.GetDatabase()))
	}
	if err := backup.CheckDetails(label, "", nil); err != nil {
		return err
	}
	ds, release, err := s.lock(stream.Context(), h.GetId())
	if err != nil {
		return err
	}
	defer release()
	data := &received{stream: stream, left: noryxv1.MaxImportedDump}
	d := backup.Details{Label: label, Created: time.Now(), Paths: []string{h.GetDatabase()}}
	location, id := cmp.Or(h.GetLocation(), storage.Default), noryxv1.NewBackupID(d.Created)
	var b backup.Archive
	// An upload without a size, e.g. of a chunked request, stops where it would leave too little
	// free space.
	if h.GetDatabase() != "" {
		b, err = s.dumps.AddFile(owner(ds.ID), location, id, d, h.GetSize(), func(f *os.File, _ *backup.Details) error {
			zw := zip.NewWriter(storage.Guard(f, f))
			w, err := zw.CreateHeader(&zip.FileHeader{Name: h.GetDatabase() + ".sql", Method: zip.Deflate, Modified: d.Created})
			if err == nil {
				_, err = io.Copy(w, data)
			}
			return cmp.Or(err, zw.Close())
		})
	} else {
		b, err = s.importArchive(ds.ID, location, id, d, h.GetSize(), data)
	}
	if err != nil {
		return toStatus(err)
	}
	return stream.SendAndClose(&noryxv1.ImportDumpResponse{Dump: b.Proto()})
}

// importArchive keeps an uploaded ZIP archive in a temporary file to check it, and adds its
// SQL files as a dump, as they are compressed.
func (s *Service) importArchive(datastoreID, location, id string, d backup.Details, size int64, data io.Reader) (backup.Archive, error) {
	tmp, err := s.dumps.Temp(owner(datastoreID), location, size)
	if err != nil {
		return backup.Archive{}, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	n, err := io.Copy(storage.Guard(tmp, tmp), data)
	if err != nil {
		return backup.Archive{}, err
	}
	zr, err := openDump(tmp, n)
	if err != nil {
		return backup.Archive{}, err
	}
	files, err := sqlFiles(zr, n)
	if err != nil {
		return backup.Archive{}, err
	}
	d.Paths = slices.Sorted(maps.Keys(files))
	return s.dumps.Add(owner(datastoreID), location, id, d, n, func(w io.Writer) error {
		zw := zip.NewWriter(w)
		for _, name := range d.Paths {
			f := files[name]
			out, err := zw.CreateRaw(&zip.FileHeader{
				Name: name + ".sql", Method: f.Method, Modified: d.Created,
				CRC32: f.CRC32, CompressedSize64: f.CompressedSize64, UncompressedSize64: f.UncompressedSize64,
			})
			if err != nil {
				return err
			}
			raw, err := f.OpenRaw()
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, raw); err != nil {
				return err
			}
		}
		return zw.Close()
	})
}

// openDump opens an uploaded ZIP archive of size bytes. Its end is checked first, so that one
// that announces more entries than a dump may have is refused before archive/zip reserves
// memory for them.
func openDump(r io.ReaderAt, size int64) (*zip.Reader, error) {
	zr, err := archive.OpenZip(r, size, dumpLimits)
	if status.Code(err) == codes.InvalidArgument {
		return nil, status.Error(codes.InvalidArgument, "Upload a ZIP archive, or the SQL file of a database.")
	}
	return zr, err
}

// sqlFiles returns the SQL files of the databases in an uploaded ZIP archive of size bytes, which
// it reads to their ends: archive/zip checks their sizes and checksums then. Folders and those
// that macOS adds are left out; any other file fails the archive.
func sqlFiles(zr *zip.Reader, size int64) (map[string]*zip.File, error) {
	invalid := func(format string, args ...any) error { return status.Errorf(codes.InvalidArgument, format, args...) }
	if len(zr.File) > maxEntries {
		return nil, invalid("The archive has more than %d files.", maxEntries)
	}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "/") || strings.HasPrefix(f.Name, "__MACOSX/") {
			continue
		}
		name, ok := strings.CutSuffix(f.Name, ".sql")
		switch {
		case !ok || noryxv1.DatabaseNameProblem(name) != "":
			return nil, invalid("The archive may only have a <database>.sql for each database, named like databases, unlike %q.", f.Name)
		case files[name] != nil:
			return nil, invalid("The archive has %s twice.", f.Name)
		case f.Method != zip.Store && f.Method != zip.Deflate:
			return nil, invalid("%s is compressed in a way that ZIP archives rarely are. Use Deflate.", f.Name)
		}
		files[name] = f
	}
	switch {
	case len(files) == 0:
		return nil, invalid("The archive has no <database>.sql.")
	case len(files) > noryxv1.MaxDatabases:
		return nil, invalid("The archive has more than %d databases.", noryxv1.MaxDatabases)
	}
	limit := min(maxUnpacked, max(size, 1<<20)*maxRatio)
	for name, f := range files {
		r, err := f.Open()
		if err == nil {
			var n int64
			n, err = io.Copy(io.Discard, io.LimitReader(r, limit+1))
			err = errors.Join(err, r.Close())
			if limit -= n; limit < 0 {
				return nil, invalid("The archive unpacks to more than %d GB, or more than %d times its size.", maxUnpacked>>30, maxRatio)
			}
		}
		if err != nil {
			return nil, invalid("%s.sql in the archive is damaged: %v", name, err)
		}
	}
	return files, nil
}

// received reads the data of an upload, up to left bytes.
type received struct {
	stream noryxv1.DatastoreService_ImportDumpServer
	buf    []byte
	left   int64
}

func (r *received) Read(p []byte) (int, error) {
	for len(r.buf) == 0 {
		msg, err := r.stream.Recv()
		if err != nil {
			return 0, err // io.EOF at the end
		}
		if r.left -= int64(len(msg.GetData())); r.left < 0 {
			return 0, errTooLarge
		}
		r.buf = msg.GetData()
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}
