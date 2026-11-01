package datastore

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/operation"
)

// operationTimeout covers pulling an image, dumping and loading large databases for an
// upgrade, and restarting the servers that use them.
const operationTimeout = 2 * time.Hour

type Handler struct {
	svc *Service
	ops *operation.Operations
	// tidy removes the references to what was deleted, e.g. the datastores that a backup job dumps.
	tidy func(context.Context)
}

func NewHandler(svc *Service, ops *operation.Operations, tidy func(context.Context)) *Handler {
	return &Handler{svc: svc, ops: ops, tidy: tidy}
}

// Register adds the routes. Passwords, tables, dumps and the log, which shows the statements
// that failed, give away the data of the databases, so only those who manage datastores may see
// them.
func (h *Handler) Register(mux access.Mux) {
	view, manage := access.Everywhere(access.DatastoresView), access.Everywhere(access.DatastoresManage)
	mux.Handle("GET /api/datastores", view, func(w http.ResponseWriter, r *http.Request) {
		list, err := h.svc.List(r.Context(), "")
		write(w, r, http.StatusOK, list, err)
	})
	mux.Handle("GET /api/networks/{id}/datastores", view, func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, err := h.svc.networks.Get(r.Context(), id); err != nil {
			httpapi.WriteError(w, r, err)
			return
		}
		list, err := h.svc.List(r.Context(), id)
		write(w, r, http.StatusOK, list, err)
	})
	mux.Handle("POST /api/networks/{id}/datastores", manage, func(w http.ResponseWriter, r *http.Request) {
		var in Input
		if !read(w, r, &in) {
			return
		}
		logging.Note(r.Context(), slog.String("name", in.Name), slog.String("engine", in.Engine), slog.String("version", in.Version))
		h.run(w, r, "datastore.create", in.Name, []string{"image", "container", "start", "network"}, http.StatusCreated, func(ctx context.Context) (any, error) {
			return h.svc.Create(ctx, r.PathValue("id"), in)
		})
	})
	mux.Handle("PATCH /api/datastores/{id}", manage, func(w http.ResponseWriter, r *http.Request) {
		var c Change
		if !read(w, r, &c) {
			return
		}
		logging.Note(r.Context(), slog.String("version", c.Version), slog.Bool("update_image", c.UpdateImage), slog.Bool("remove_previous", c.RemovePrevious))
		steps := []string{"container"}
		if c.Version != "" {
			steps = []string{"image", "dump", "container", "load"}
		}
		h.run(w, r, "datastore.update", "", steps, http.StatusOK, func(ctx context.Context) (any, error) {
			return h.svc.Update(ctx, r.PathValue("id"), c)
		})
	})
	for action, start := range map[string]bool{"start": true, "stop": false} {
		mux.Handle("POST /api/datastores/{id}/"+action, manage, func(w http.ResponseWriter, r *http.Request) {
			write(w, r, http.StatusNoContent, nil, h.svc.Start(r.Context(), r.PathValue("id"), start))
		})
	}
	mux.Handle("DELETE /api/datastores/{id}", manage, func(w http.ResponseWriter, r *http.Request) {
		err := h.svc.Delete(r.Context(), r.PathValue("id"))
		if err == nil {
			h.tidy(r.Context())
		}
		write(w, r, http.StatusNoContent, nil, err)
	})
	mux.Handle("GET /api/datastores/{id}/logs", manage, func(w http.ResponseWriter, r *http.Request) {
		h.svc.Log(w, r, r.PathValue("id"))
	})
	mux.Handle("POST /api/datastores/{id}/databases", manage, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
		}
		if read(w, r, &req) {
			logging.Note(r.Context(), slog.String("name", req.Name))
			db, err := h.svc.AddDatabase(r.Context(), r.PathValue("id"), req.Name)
			write(w, r, http.StatusCreated, db, err)
		}
	})
	mux.Handle("DELETE /api/datastores/{id}/databases/{name}", manage, func(w http.ResponseWriter, r *http.Request) {
		write(w, r, http.StatusNoContent, nil, h.svc.DropDatabase(r.Context(), r.PathValue("id"), r.PathValue("name")))
	})
	mux.Handle("GET /api/datastores/{id}/databases/{name}/password", manage, func(w http.ResponseWriter, r *http.Request) {
		password, err := h.svc.Password(r.Context(), r.PathValue("id"), r.PathValue("name"))
		w.Header().Set("Cache-Control", "no-store")
		write(w, r, http.StatusOK, map[string]string{"password": password}, err)
	})
	mux.Handle("POST /api/datastores/{id}/databases/{name}/rotate", manage, func(w http.ResponseWriter, r *http.Request) {
		write(w, r, http.StatusNoContent, nil, h.svc.Rotate(r.Context(), r.PathValue("id"), r.PathValue("name")))
	})
	mux.Handle("GET /api/datastores/{id}/databases/{name}/tables", manage, func(w http.ResponseWriter, r *http.Request) {
		tables, err := h.svc.Tables(r.Context(), r.PathValue("id"), r.PathValue("name"))
		write(w, r, http.StatusOK, tables, err)
	})
	// Rows from offset on, sorted by the column sort, descending with descending=true, and
	// those whose column filter equals value, or contains it with contains=true.
	mux.Handle("GET /api/datastores/{id}/databases/{name}/tables/{table}", manage, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		offset, _ := strconv.ParseUint(q.Get("offset"), 10, 64)
		req := &noryxv1.BrowseTableRequest{
			Database: r.PathValue("name"), Schema: q.Get("schema"), Table: r.PathValue("table"), Offset: offset,
			Sort: q.Get("sort"), Descending: q.Get("descending") == "true",
		}
		if q.Has("filter") {
			req.Filter = &noryxv1.TableFilter{Column: q.Get("filter"), Value: q.Get("value"), Contains: q.Get("contains") == "true"}
		}
		page, err := h.svc.Browse(r.Context(), r.PathValue("id"), req)
		w.Header().Set("Cache-Control", "no-store")
		write(w, r, http.StatusOK, page, err)
	})
	mux.Handle("GET /api/datastores/{id}/backups", view, func(w http.ResponseWriter, r *http.Request) {
		dumps, err := h.svc.Dumps(r.Context(), r.PathValue("id"))
		write(w, r, http.StatusOK, dumps, err)
	})
	mux.Handle("POST /api/datastores/{id}/backups", manage, func(w http.ResponseWriter, r *http.Request) {
		var req DumpRequest
		if read(w, r, &req) {
			h.run(w, r, "datastore.backup", "", []string{"dump"}, http.StatusCreated, func(ctx context.Context) (any, error) {
				return h.svc.Dump(ctx, r.PathValue("id"), req)
			})
		}
	})
	// The body is a ZIP archive with a <database>.sql for each database, or the SQL of the
	// database that the query names.
	mux.Handle("POST /api/datastores/{id}/backups/upload", manage, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		logging.Note(r.Context(), slog.String("database", q.Get("database")), slog.String("label", q.Get("label")), slog.String("location", q.Get("location")))
		dump, err := h.svc.Upload(r.Context(), r.PathValue("id"), q.Get("database"), q.Get("label"), q.Get("location"), r.ContentLength,
			http.MaxBytesReader(w, r.Body, noryxv1.MaxImportedDump))
		if errors.As(err, new(*http.MaxBytesError)) {
			err = errTooLarge
		}
		write(w, r, http.StatusCreated, dump, err)
	})
	mux.Handle("POST /api/datastores/{id}/backups/{backup}/restore", manage, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Databases []string `json:"databases"`
		}
		if read(w, r, &req) {
			h.run(w, r, "datastore.restore", "", []string{"check", "load"}, http.StatusNoContent, func(ctx context.Context) (any, error) {
				return nil, h.svc.Restore(ctx, r.PathValue("id"), r.PathValue("backup"), req.Databases)
			})
		}
	})
	mux.Handle("DELETE /api/datastores/{id}/backups/{backup}", manage, func(w http.ResponseWriter, r *http.Request) {
		write(w, r, http.StatusNoContent, nil, h.svc.DeleteDump(r.Context(), r.PathValue("id"), r.PathValue("backup")))
	})
	mux.Handle("GET /api/datastores/{id}/backups/{backup}/download", manage, func(w http.ResponseWriter, r *http.Request) {
		h.svc.Download(w, r, r.PathValue("id"), r.PathValue("backup"))
	})
}

// run runs a long action on the datastore of a request, or on a new one of the network of
// the request named subject, as an operation, which those see who may see datastores.
func (h *Handler) run(w http.ResponseWriter, r *http.Request, kind, subject string, steps []string, status int, task operation.Task) {
	networkID := r.PathValue("id")
	if ds, err := h.svc.store.get(r.Context(), r.PathValue("id")); err == nil {
		subject, networkID = ds.Name, ds.NetworkID
		if db := r.PathValue("name"); db != "" {
			subject += "." + db
		}
	}
	h.ops.Run(w, r, operation.Spec{
		Kind: kind, Subject: subject, NetworkID: networkID, Steps: steps, Status: status, Timeout: operationTimeout, Category: logging.Databases,
		Visible: func(g access.Grants) bool { return g.Has(access.DatastoresView) },
	}, task)
}

// read reads the JSON body of a request into v, or answers with the error.
func read(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := httpapi.ReadJSON(w, r, v); err != nil {
		httpapi.WriteError(w, r, err)
		return false
	}
	return true
}

func write(w http.ResponseWriter, r *http.Request, status int, v any, err error) {
	switch {
	case err != nil:
		httpapi.WriteError(w, r, err)
	case v == nil:
		w.WriteHeader(status)
	default:
		httpapi.WriteJSON(w, status, v)
	}
}
