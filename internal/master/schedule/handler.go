package schedule

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/auth"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/tag"
)

const (
	timeout = 30 * time.Second
	// agenda is how far ahead the upcoming runs of tasks are listed.
	agenda = 7 * 24 * time.Hour
)

// Handler serves the tasks of one kind under a path, e.g. /api/backup-jobs, to the users that
// view and manage allow. Tasks run on any server, so their permissions apply everywhere.
type Handler struct {
	svc          *Service
	kind         string
	view, manage access.Need
}

func NewHandler(svc *Service, kind string, view, manage access.Need) *Handler {
	return &Handler{svc: svc, kind: kind, view: view, manage: manage}
}

func (h *Handler) Register(mux access.Mux, base string) {
	view, manage := h.view, h.manage
	// With ?node=…&server=…, only the tasks that cover the server.
	mux.Handle("GET "+base, view, func(w http.ResponseWriter, r *http.Request) {
		var tasks []Task
		var err error
		if q := r.URL.Query(); q.Has("node") || q.Has("server") {
			tasks, err = h.svc.Covering(r.Context(), h.kind, tag.Server{NodeID: q.Get("node"), ServerID: q.Get("server")})
		} else {
			tasks, err = h.svc.List(r.Context(), h.kind)
		}
		write(w, r, http.StatusOK, tasks, err)
	})
	mux.Handle("GET "+base+"/upcoming", view, func(w http.ResponseWriter, r *http.Request) {
		write(w, r, http.StatusOK, h.svc.Upcoming(h.kind, time.Now().Add(agenda)), nil)
	})
	mux.Handle("POST "+base, manage, h.save(http.StatusCreated, func(ctx context.Context, _ string, in Input, by Author) (Task, error) {
		return h.svc.Create(ctx, h.kind, in, by)
	}))
	mux.Handle("GET "+base+"/{id}", view, func(w http.ResponseWriter, r *http.Request) {
		t, err := h.svc.Get(r.Context(), h.kind, r.PathValue("id"))
		write(w, r, http.StatusOK, t, err)
	})
	mux.Handle("GET "+base+"/{id}/runs", view, func(w http.ResponseWriter, r *http.Request) {
		runs, err := h.svc.Runs(r.Context(), h.kind, r.PathValue("id"))
		write(w, r, http.StatusOK, runs, err)
	})
	mux.Handle("PUT "+base+"/{id}", manage, h.save(http.StatusOK, func(ctx context.Context, id string, in Input, by Author) (Task, error) {
		return h.svc.Update(ctx, h.kind, id, in, by)
	}))
	mux.Handle("DELETE "+base+"/{id}", manage, func(w http.ResponseWriter, r *http.Request) {
		write(w, r, http.StatusNoContent, nil, h.svc.Delete(r.Context(), h.kind, r.PathValue("id")))
	})
	// The run continues in the background; the task shows when it is done.
	mux.Handle("POST "+base+"/{id}/run", manage, func(w http.ResponseWriter, r *http.Request) {
		t, err := h.svc.RunNow(r.Context(), h.kind, r.PathValue("id"), author(r))
		write(w, r, http.StatusAccepted, t, err)
	})
}

// author is the user of a request, with its permissions.
func author(r *http.Request) Author {
	user, _ := auth.UserFrom(r.Context())
	return Author{ID: user.ID, Name: user.Username, Grants: access.From(r.Context())}
}

func (h *Handler) save(status int, op func(ctx context.Context, id string, in Input, by Author) (Task, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in Input
		if err := httpapi.ReadJSON(w, r, &in); err != nil {
			httpapi.WriteError(w, r, err)
			return
		}
		logging.Note(r.Context(), slog.String("name", in.Name))
		// A secret is never logged, only that it changes.
		if in.Secret != nil {
			change := "set"
			if *in.Secret == "" {
				change = "removed"
			}
			logging.Note(r.Context(), slog.String("secret", change))
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		t, err := op(ctx, r.PathValue("id"), in, author(r))
		write(w, r, status, t, err)
	}
}

func write(w http.ResponseWriter, r *http.Request, status int, v any, err error) {
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	httpapi.WriteJSON(w, status, v)
}
