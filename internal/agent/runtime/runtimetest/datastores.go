// Package runtimetest has runtimes for tests that keep everything in memory.
package runtimetest

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/runtime"
)

// Datastores runs datastores in memory. A database's dump is its content, which tests set
// with Write; a load needs the database's user, like the runtime's. Tests write the log of a
// datastore with Log.
type Datastores struct {
	mu     sync.Mutex
	stores map[string]*datastore
}

type datastore struct {
	runtime.Datastore
	databases map[string]*database
	log       []runtime.LogLine
}

type database struct{ password, content string }

func (f *Datastores) DatastoreVersions() map[noryxv1.DatastoreEngine][]string {
	return map[noryxv1.DatastoreEngine][]string{
		noryxv1.DatastoreEngine_DATASTORE_ENGINE_MARIADB:  {"11.8", "12.3"},
		noryxv1.DatastoreEngine_DATASTORE_ENGINE_POSTGRES: {"17", "18"},
	}
}

func (f *Datastores) ListDatastores(context.Context) ([]runtime.Datastore, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var list []runtime.Datastore
	for _, id := range slices.Sorted(maps.Keys(f.stores)) {
		list = append(list, f.stores[id].Datastore)
	}
	return list, nil
}

func (f *Datastores) CreateDatastore(_ context.Context, spec runtime.DatastoreSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stores == nil {
		f.stores = map[string]*datastore{}
	}
	if f.stores[spec.ID] != nil {
		return errors.New("exists")
	}
	f.stores[spec.ID] = &datastore{Datastore: runtime.Datastore{DatastoreSpec: spec, State: noryxv1.DatastoreState_DATASTORE_STATE_STOPPED}, databases: map[string]*database{}}
	return nil
}

// with runs fn with a datastore, which must be running if running is set.
func (f *Datastores) with(id string, running bool, fn func(*datastore) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ds := f.stores[id]
	switch {
	case ds == nil:
		return runtime.ErrNotFound
	case running && ds.State != noryxv1.DatastoreState_DATASTORE_STATE_RUNNING:
		return runtime.ErrDatastoreNotRunning
	}
	return fn(ds)
}

func (f *Datastores) StartDatastore(_ context.Context, id string) error {
	return f.with(id, false, func(ds *datastore) error {
		ds.State = noryxv1.DatastoreState_DATASTORE_STATE_RUNNING
		return nil
	})
}

func (f *Datastores) StopDatastore(_ context.Context, id string) error {
	return f.with(id, false, func(ds *datastore) error {
		ds.State = noryxv1.DatastoreState_DATASTORE_STATE_STOPPED
		return nil
	})
}

func (f *Datastores) UpdateDatastore(_ context.Context, spec runtime.DatastoreSpec, _ bool) error {
	return f.with(spec.ID, false, func(ds *datastore) error {
		if spec.Version != ds.Version {
			versions := f.DatastoreVersions()[ds.Engine]
			if slices.Index(versions, spec.Version) <= slices.Index(versions, ds.Version) {
				return fmt.Errorf("%s can only move to a newer version", ds.Version)
			}
			spec.Previous = ds.Version
		}
		spec.Engine, spec.Storage = ds.Engine, ds.Storage
		ds.DatastoreSpec = spec
		return nil
	})
}

func (f *Datastores) RemoveDatastore(_ context.Context, id string) error {
	return f.with(id, false, func(*datastore) error {
		delete(f.stores, id)
		return nil
	})
}

func (f *Datastores) Databases(_ context.Context, id string) (names []string, err error) {
	return names, f.with(id, true, func(ds *datastore) error {
		names = slices.Sorted(maps.Keys(ds.databases))
		return nil
	})
}

func (f *Datastores) EnsureDatabase(_ context.Context, id, name, password string) error {
	return f.with(id, true, func(ds *datastore) error {
		if db := ds.databases[name]; db != nil {
			db.password = password
		} else {
			ds.databases[name] = &database{password: password}
		}
		return nil
	})
}

func (f *Datastores) DropDatabase(_ context.Context, id, name string) error {
	return f.with(id, true, func(ds *datastore) error {
		delete(ds.databases, name)
		return nil
	})
}

func (f *Datastores) Dump(_ context.Context, id, name string, w io.Writer) error {
	return f.with(id, true, func(ds *datastore) error {
		db := ds.databases[name]
		if db == nil {
			return fmt.Errorf("no database %s", name)
		}
		_, err := io.WriteString(w, db.content)
		return err
	})
}

func (f *Datastores) Load(_ context.Context, id, name string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	return f.with(id, true, func(ds *datastore) error {
		db := ds.databases[name]
		if db == nil {
			return fmt.Errorf("the user %s has no password Noryx can keep", name)
		}
		db.content = string(data)
		return nil
	})
}

// Tables returns the one table of a database, content, whose one row is its content.
func (f *Datastores) Tables(_ context.Context, id, name string) (tables []*noryxv1.Table, err error) {
	return tables, f.with(id, true, func(ds *datastore) error {
		db := ds.databases[name]
		if db == nil {
			return fmt.Errorf("no database %s", name)
		}
		tables = []*noryxv1.Table{{Name: "content", Rows: 1, Size: int64(len(db.content))}}
		return nil
	})
}

// Browse shows the row of the table content, if its filter matches it.
func (f *Datastores) Browse(ctx context.Context, id string, req *noryxv1.BrowseTableRequest) (*noryxv1.BrowseTableResponse, error) {
	if _, err := f.Tables(ctx, id, req.GetDatabase()); err != nil || req.GetTable() != "content" {
		return nil, cmp.Or(err, runtime.ErrNoTable)
	}
	filter := req.GetFilter()
	for _, column := range []string{req.GetSort(), filter.GetColumn()} {
		if column != "" && column != "content" {
			return nil, runtime.ErrNoColumn
		}
	}
	content, _ := f.Content(id, req.GetDatabase())
	matches := filter == nil || content == filter.GetValue() ||
		filter.GetContains() && strings.Contains(strings.ToLower(content), strings.ToLower(filter.GetValue()))
	res := &noryxv1.BrowseTableResponse{Columns: []*noryxv1.TableColumn{{Name: "content", Type: "text"}}, SortedAndFiltered: true}
	if req.GetOffset() == 0 && req.GetLimit() > 0 && matches {
		res.Rows = []*noryxv1.TableRow{{Values: []*noryxv1.TableValue{{Text: content}}}}
	}
	return res, nil
}

// DatastoreUsage reports 256 MiB of memory and a connection for each database.
func (f *Datastores) DatastoreUsage(_ context.Context, id string) (u runtime.DatastoreUsage, err error) {
	return u, f.with(id, true, func(ds *datastore) error {
		connections := uint32(0)
		for range ds.databases {
			connections++
		}
		u = runtime.DatastoreUsage{Usage: runtime.Usage{MemoryBytes: 256 << 20, MemoryLimit: uint64(ds.MemoryMB) << 20}, Connections: &connections}
		return nil
	})
}

// DatastoreLogs yields the last tail lines of a datastore's log written after the time after,
// and ends, like the log of a stopped container.
func (f *Datastores) DatastoreLogs(_ context.Context, id string, tail int, after time.Time) iter.Seq2[runtime.LogLine, error] {
	return func(yield func(runtime.LogLine, error) bool) {
		var lines []runtime.LogLine
		if err := f.with(id, false, func(ds *datastore) error {
			lines = slices.Clone(ds.log[max(0, len(ds.log)-tail):])
			return nil
		}); err != nil {
			yield(runtime.LogLine{}, err)
			return
		}
		for _, line := range lines {
			if line.Time.After(after) && !yield(line, nil) {
				return
			}
		}
	}
}

// Log adds lines to the log of a datastore, as its engine would write them, a nanosecond
// apart from the first second of 1970 on.
func (f *Datastores) Log(id string, lines ...string) {
	_ = f.with(id, false, func(ds *datastore) error {
		for _, text := range lines {
			ds.log = append(ds.log, runtime.LogLine{Time: time.Unix(1, int64(len(ds.log)+1)), Text: text})
		}
		return nil
	})
}

// Write sets the content of a database, as its user would.
func (f *Datastores) Write(id, name, content string) error {
	return f.with(id, true, func(ds *datastore) error {
		db := ds.databases[name]
		if db == nil {
			return fmt.Errorf("no database %s", name)
		}
		db.content = content
		return nil
	})
}

// Content returns the content of a database and its user's password.
func (f *Datastores) Content(id, name string) (content, password string) {
	_ = f.with(id, false, func(ds *datastore) error {
		if db := ds.databases[name]; db != nil {
			content, password = db.content, db.password
		}
		return nil
	})
	return content, password
}

// Fail makes a datastore unhealthy, or healthy again.
func (f *Datastores) Fail(id string, failing bool) {
	_ = f.with(id, false, func(ds *datastore) error {
		ds.State = noryxv1.DatastoreState_DATASTORE_STATE_RUNNING
		if failing {
			ds.State = noryxv1.DatastoreState_DATASTORE_STATE_UNHEALTHY
		}
		return nil
	})
}

var _ runtime.Datastores = (*Datastores)(nil)
