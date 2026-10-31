package workflow

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/auth"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/ratelimit"
)

const (
	timeout = 30 * time.Second
	// agenda is how far ahead the runs of schedules and intervals are listed.
	agenda = 7 * 24 * time.Hour
	// maxDraft is the largest workflow the panel may send.
	maxDraft = 2 << 20
	// maxHookBody is the largest body a call of a webhook may have.
	maxHookBody = 64 << 10
	// HookPath is where webhooks are called, with the token after it.
	HookPath = "/api/hooks/"
)

// Handler serves the workflows to the panel, and their webhooks to anyone with a token.
// Workflows act on any server, so their permissions apply everywhere.
type Handler struct {
	svc *Service
	// misses are the calls of webhooks with a token that no workflow has, per client.
	misses *ratelimit.Limiter
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc, misses: ratelimit.New(10, time.Minute)}
}

func (h *Handler) Register(mux access.Mux) {
	view, manage := access.Everywhere(access.WorkflowsView), access.Everywhere(access.WorkflowsManage)
	mux.Handle("GET /api/workflows", view, func(w http.ResponseWriter, r *http.Request) {
		list, err := h.svc.List(r.Context())
		write(w, r, http.StatusOK, list, err)
	})
	mux.Handle("GET /api/workflows/upcoming", view, func(w http.ResponseWriter, r *http.Request) {
		write(w, r, http.StatusOK, h.svc.Upcoming(time.Now().Add(agenda)), nil)
	})
	mux.Handle("POST /api/workflows", manage, h.save(http.StatusCreated, func(ctx context.Context, _ string, d Draft, by Author) (Workflow, error) {
		return h.svc.Create(ctx, d, by)
	}))
	mux.Handle("GET /api/workflows/{id}", view, func(w http.ResponseWriter, r *http.Request) {
		wf, err := h.svc.Get(r.Context(), r.PathValue("id"))
		write(w, r, http.StatusOK, wf, err)
	})
	mux.Handle("PUT /api/workflows/{id}", manage, h.save(http.StatusOK, func(ctx context.Context, id string, d Draft, by Author) (Workflow, error) {
		return h.svc.Update(ctx, id, d, by)
	}))
	mux.Handle("DELETE /api/workflows/{id}", manage, func(w http.ResponseWriter, r *http.Request) {
		write(w, r, http.StatusNoContent, nil, h.svc.Delete(r.Context(), r.PathValue("id")))
	})
	// The run goes on in the background; its page follows it.
	mux.Handle("POST /api/workflows/{id}/run", manage, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Inputs map[string]any `json:"inputs"`
		}
		if err := httpapi.ReadJSON(w, r, &req); err != nil {
			httpapi.WriteError(w, r, err)
			return
		}
		run, err := h.svc.RunNow(r.Context(), r.PathValue("id"), req.Inputs, author(r))
		logging.Note(r.Context(), slog.Int64("run", run.ID))
		write(w, r, http.StatusAccepted, run, err)
	})
	mux.Handle("GET /api/workflows/{id}/runs", view, func(w http.ResponseWriter, r *http.Request) {
		runs, err := h.svc.Runs(r.Context(), r.PathValue("id"))
		write(w, r, http.StatusOK, runs, err)
	})
	mux.Handle("GET /api/workflows/{id}/runs/{run}", view, func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("run"), 10, 64)
		var run Run
		if err == nil {
			run, err = h.svc.Run(r.Context(), r.PathValue("id"), id)
		} else {
			err = httpapi.Errorf(http.StatusNotFound, "Run not found.")
		}
		write(w, r, http.StatusOK, run, err)
	})
	mux.Handle("POST /api/workflows/{id}/runs/{run}/cancel", manage, func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("run"), 10, 64)
		if err == nil {
			err = h.svc.Cancel(r.PathValue("id"), id)
		} else {
			err = httpapi.Errorf(http.StatusNotFound, "Run not found.")
		}
		write(w, r, http.StatusNoContent, nil, err)
	})
	// The token is only shown now; the master keeps its hash.
	mux.Handle("POST /api/workflows/{id}/hook", manage, func(w http.ResponseWriter, r *http.Request) {
		token, err := h.svc.NewHook(r.Context(), r.PathValue("id"))
		write(w, r, http.StatusOK, map[string]string{"path": HookPath + token}, err)
	})
	mux.Handle("DELETE /api/workflows/{id}/hook", manage, func(w http.ResponseWriter, r *http.Request) {
		write(w, r, http.StatusNoContent, nil, h.svc.DeleteHook(r.Context(), r.PathValue("id")))
	})
}

// RegisterPublic adds the route of webhooks, which needs no session: the token in its path
// chooses the workflow and is all a caller needs. Clients that call with unknown tokens are
// slowed down, and each workflow starts at most as often as its triggers may.
func (h *Handler) RegisterPublic(mux *http.ServeMux) {
	mux.HandleFunc("POST "+HookPath+"{token}", func(w http.ResponseWriter, r *http.Request) {
		client := ratelimit.Client(auth.ClientIP(r))
		if h.misses.Blocked(client) {
			httpapi.WriteError(w, r, httpapi.Errorf(http.StatusTooManyRequests, "Too many calls. Wait a minute."))
			return
		}
		data, err := hookData(w, r)
		if err != nil {
			httpapi.WriteError(w, r, err)
			return
		}
		started, err := h.svc.Hook(r.PathValue("token"), data)
		if err != nil {
			h.misses.Allow(client)
			httpapi.WriteError(w, r, err)
			return
		}
		httpapi.WriteJSON(w, http.StatusAccepted, map[string]string{"status": started})
	})
}

// hookData are the data of a call of a webhook as templates see them: its body, as JSON if it
// is, else as text, and the values of its query.
func hookData(w http.ResponseWriter, r *http.Request) (map[string]any, error) {
	lift := httpapi.LimitBodyTime(w)
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxHookBody))
	lift()
	if err != nil {
		return nil, httpapi.Errorf(http.StatusRequestEntityTooLarge, "Send up to %d KB.", maxHookBody>>10)
	}
	var body any
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt == "application/json" || json.Valid(raw) {
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, httpapi.Errorf(http.StatusBadRequest, "The body isn't JSON.")
		}
	} else if utf8.Valid(raw) {
		body = string(raw)
	}
	query := map[string]any{}
	for k, v := range r.URL.Query() {
		query[k] = v[0]
	}
	return map[string]any{"kind": OnWebhook, "time": time.Now().Format(time.RFC3339), "body": body, "query": query}, nil
}

// author is the user of a request, with its permissions.
func author(r *http.Request) Author {
	user, _ := auth.UserFrom(r.Context())
	return Author{ID: user.ID, Name: user.Username, Grants: access.From(r.Context())}
}

func (h *Handler) save(status int, op func(ctx context.Context, id string, d Draft, by Author) (Workflow, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var d Draft
		if err := httpapi.ReadJSONUpTo(w, r, &d, maxDraft); err != nil {
			httpapi.WriteError(w, r, err)
			return
		}
		logging.Note(r.Context(), slog.String("name", d.Name))
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		wf, err := op(ctx, r.PathValue("id"), d, author(r))
		write(w, r, status, wf, err)
	}
}

func write(w http.ResponseWriter, r *http.Request, status int, v any, err error) {
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	httpapi.WriteJSON(w, status, v)
}
