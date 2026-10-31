package datastore

import (
	"cmp"
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"time"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/httpapi"
)

// Dump is a ZIP archive with an SQL dump of each of its databases, which the agent of the
// datastore's node keeps like the backups of servers.
type Dump struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	CreatedAt time.Time `json:"createdAt"`
	Size      int64     `json:"size"`
	Location  string    `json:"location"`
	Databases []string  `json:"databases"`
	JobID     string    `json:"jobId,omitempty"`
}

func toDump(b *noryxv1.Backup) Dump {
	return Dump{b.GetId(), b.GetLabel(), time.Unix(b.GetCreatedUnix(), 0), b.GetSize(), b.GetLocation(), append([]string{}, b.GetPaths()...), b.GetJobId()}
}

func (s *Service) Dumps(ctx context.Context, id string) ([]Dump, error) {
	ds, err := s.store.get(ctx, id)
	if err != nil {
		return nil, err
	}
	dumps := []Dump{}
	err = s.call(ctx, ds.NodeID, func(ctx context.Context, c noryxv1.DatastoreServiceClient) error {
		res, err := c.ListDumps(ctx, &noryxv1.ListDumpsRequest{Id: id})
		for _, b := range res.GetDumps() {
			dumps = append(dumps, toDump(b))
		}
		return err
	})
	return dumps, err
}

// DumpRequest dumps databases of a datastore, or all of them.
type DumpRequest struct {
	Label     string   `json:"label"`
	Location  string   `json:"location"`
	Databases []string `json:"databases"`
}

func (s *Service) Dump(ctx context.Context, id string, req DumpRequest) (Dump, error) {
	ds, err := s.store.get(ctx, id)
	if err != nil {
		return Dump{}, err
	}
	var dump Dump
	err = s.agent(context.WithoutCancel(ctx), ds.NodeID, func(ctx context.Context, c noryxv1.DatastoreServiceClient) error {
		res, err := c.CreateDump(ctx, &noryxv1.CreateDumpRequest{Id: id, Label: req.Label, Location: req.Location, Databases: req.Databases})
		dump = toDump(res.GetDump())
		return err
	})
	return dump, err
}

// Restore creates databases again from a dump, all in it or those named. Their users get the
// passwords the master keeps, so a database must still be known to be restored.
func (s *Service) Restore(ctx context.Context, id, dumpID string, databases []string) error {
	ds, err := s.store.get(ctx, id)
	if err != nil {
		return err
	}
	dumps, err := s.Dumps(ctx, id)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(dumps, func(d Dump) bool { return d.ID == dumpID })
	if i < 0 {
		return httpapi.Errorf(http.StatusNotFound, "Dump not found.")
	}
	if len(databases) == 0 {
		databases = dumps[i].Databases
	}
	for _, name := range databases {
		j := slices.IndexFunc(ds.Databases, func(db Database) bool { return db.Name == name })
		switch {
		case !slices.Contains(dumps[i].Databases, name):
			return httpapi.Errorf(http.StatusBadRequest, "The dump has no database %q.", name)
		case j < 0:
			return httpapi.Errorf(http.StatusConflict, "Add the database %s again before you restore it.", name)
		}
		if err := s.ensure(ctx, ds, ds.Databases[j]); err != nil {
			return err
		}
	}
	return s.agent(context.WithoutCancel(ctx), ds.NodeID, func(ctx context.Context, c noryxv1.DatastoreServiceClient) error {
		_, err := c.RestoreDump(ctx, &noryxv1.RestoreDumpRequest{Id: id, DumpId: dumpID, Databases: databases})
		return err
	})
}

// uploadChunk is how many bytes of an upload a message to the agent carries.
const uploadChunk = 256 << 10

var errTooLarge = httpapi.Errorf(http.StatusRequestEntityTooLarge, "A dump can have up to %d GB.", noryxv1.MaxImportedDump>>30)

// Upload adds a dump made elsewhere to a datastore, e.g. of a database that moves to Noryx:
// a ZIP archive with a <database>.sql for each database, or the SQL of the database named.
// The agent checks it, and restoring it loads it like a dump made there.
func (s *Service) Upload(ctx context.Context, id, database, label string, size int64, content io.Reader) (Dump, error) {
	ds, err := s.store.get(ctx, id)
	if err != nil {
		return Dump{}, err
	}
	switch {
	case size > noryxv1.MaxImportedDump:
		return Dump{}, errTooLarge
	case database != "" && !slices.ContainsFunc(ds.Databases, func(db Database) bool { return db.Name == database }):
		return Dump{}, httpapi.Errorf(http.StatusNotFound, "Add the database %s first.", database)
	}
	conn, err := s.nodes.Conn(ctx, ds.NodeID)
	if err != nil {
		return Dump{}, err
	}
	stream, err := noryxv1.NewDatastoreServiceClient(conn).ImportDump(ctx)
	if err != nil {
		return Dump{}, err
	}
	header := &noryxv1.ImportDumpHeader{Id: id, Label: label, Database: database, Size: max(size, 0)}
	err = stream.Send(&noryxv1.ImportDumpRequest{Content: &noryxv1.ImportDumpRequest_Header{Header: header}})
	buf := make([]byte, uploadChunk)
	for err == nil {
		var n int
		if n, err = io.ReadFull(content, buf); n > 0 {
			err = cmp.Or(stream.Send(&noryxv1.ImportDumpRequest{Content: &noryxv1.ImportDumpRequest_Data{Data: buf[:n]}}), err)
		}
	}
	// The content ended, or the agent ended the stream (io.EOF), whose reply tells why.
	if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return Dump{}, err
	}
	res, err := stream.CloseAndRecv()
	return toDump(res.GetDump()), err
}

func (s *Service) DeleteDump(ctx context.Context, id, dumpID string) error {
	ds, err := s.store.get(ctx, id)
	if err != nil {
		return err
	}
	return s.call(ctx, ds.NodeID, func(ctx context.Context, c noryxv1.DatastoreServiceClient) error {
		_, err := c.DeleteDump(ctx, &noryxv1.DeleteDumpRequest{Id: id, DumpId: dumpID})
		return err
	})
}

// Download streams a dump from the agent of its datastore's node to w.
func (s *Service) Download(w http.ResponseWriter, r *http.Request, id, dumpID string) {
	ds, err := s.store.get(r.Context(), id)
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	conn, err := s.nodes.Conn(r.Context(), ds.NodeID)
	var first *noryxv1.DownloadDumpResponse
	var stream interface {
		Recv() (*noryxv1.DownloadDumpResponse, error)
	}
	if err == nil {
		stream, err = noryxv1.NewDatastoreServiceClient(conn).DownloadDump(r.Context(), &noryxv1.DownloadDumpRequest{Id: id, DumpId: dumpID})
	}
	if err == nil {
		first, err = stream.Recv() // errors such as an unknown dump arrive here
	}
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	httpapi.Attachment(w, ds.Name+"-"+dumpID+".zip")
	w.Header().Set("Content-Length", strconv.FormatInt(first.GetSize(), 10))
	httpapi.Relay(w, first, stream)
}
