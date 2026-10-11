package settings

import (
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/httpapi"
)

type Handler struct {
	svc     *Service
	restart func() error
}

// NewHandler returns the handler of the settings. restart restarts the master; it is nil if
// the master can't restart itself.
func NewHandler(svc *Service, restart func() error) *Handler {
	return &Handler{svc: svc, restart: restart}
}

// Register adds the routes. Only administrators may change the panel's address and HTTPS,
// which can expose the panel to other networks, and restart the master. The texts that warn
// the players go to the console of every server they warn, so changing them also needs the
// permission to send console commands to all servers.
func (h *Handler) Register(mux access.Mux) {
	mux.Handle("GET /api/settings", access.Everywhere(access.SettingsView), h.get)
	mux.Handle("PUT /api/settings", access.Everywhere(access.SettingsEdit), h.update)
	mux.Handle("POST /api/master/restart", access.AdminsOnly, h.restartMaster)
}

// RegisterPublic adds the route that works without a session: what the panel shows before
// anyone signs in, which the settings say is public.
func (h *Handler) RegisterPublic(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/panel", h.public)
}

// public is what the panel shows before anyone signs in: its name, the notice of the sign-in
// page, and the language and look it has for users who haven't chosen them.
type public struct {
	Name     string       `json:"name"`
	Notice   string       `json:"notice"`
	Defaults UserDefaults `json:"defaults"`
}

func (h *Handler) public(w http.ResponseWriter, _ *http.Request) {
	s := h.svc.Get()
	httpapi.WriteJSON(w, http.StatusOK, public{h.svc.PanelName(), s.SignInNotice, s.UserDefaults})
}

type view struct {
	Settings Settings `json:"settings"`
	Master   Master   `json:"master"`
}

func (h *Handler) get(w http.ResponseWriter, _ *http.Request) {
	httpapi.WriteJSON(w, http.StatusOK, view{h.svc.Get(), h.svc.Master()})
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	req := h.svc.Get() // settings the request leaves out stay as they are
	if err := httpapi.ReadJSON(w, r, &req); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	cur := h.svc.Get()
	if p, ok := access.AdminsOnly(r, access.From(r.Context())); !ok &&
		(strings.TrimSpace(req.PanelAddr) != cur.PanelAddr || req.PanelHTTPS != cur.PanelHTTPS || strings.TrimSpace(req.PanelDomain) != cur.PanelDomain) {
		httpapi.WriteError(w, r, access.Denied(p))
		return
	}
	texts := strings.TrimSpace(req.Warnings.Restart) != cur.Warnings.Restart || strings.TrimSpace(req.Warnings.Stop) != cur.Warnings.Stop
	if texts && !access.From(r.Context()).Has(access.ConsoleCommands) {
		httpapi.WriteError(w, r, access.Denied(access.ConsoleCommands))
		return
	}
	updated, err := h.svc.Update(r.Context(), req)
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	if m := updated.RequireMFA; m.All != cur.RequireMFA.All || !slices.Equal(m.Groups, cur.RequireMFA.Groups) {
		logging.Note(r.Context(), slog.Bool("mfa_required_all", m.All), slog.Any("mfa_required_groups", m.Groups))
	}
	if texts {
		logging.Note(r.Context(), slog.String("warning_restart", updated.Warnings.Restart), slog.String("warning_stop", updated.Warnings.Stop))
	}
	httpapi.WriteJSON(w, http.StatusOK, view{updated, h.svc.Master()})
}

// restartMaster restarts the master once the response is sent.
func (h *Handler) restartMaster(w http.ResponseWriter, r *http.Request) {
	err := httpapi.Errorf(http.StatusConflict, "This master can't restart itself. Restart it on its host, e.g. with: systemctl restart noryx-master")
	if h.restart != nil {
		err = h.restart()
	}
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
