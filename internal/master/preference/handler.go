package preference

import (
	"net/http"

	"github.com/QwikByte/noryx/internal/master/auth"
	"github.com/QwikByte/noryx/internal/master/httpapi"
)

type Handler struct{ store *Store }

func NewHandler(store *Store) *Handler { return &Handler{store: store} }

// Register adds the routes for the signed-in user's own preferences. Like those of the
// user's account, they need a session but no permission, and they aren't in the audit log,
// as they only change how the panel looks for the user. So they go on the API's mux itself
// rather than on access.Mux, which logs every change. API tokens can't use them.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/preferences", auth.SessionOnly(h.get))
	mux.HandleFunc("PUT /api/preferences/dashboard", auth.SessionOnly(h.setDashboard))
	mux.HandleFunc("PUT /api/preferences/pinned", auth.SessionOnly(h.setPinned))
	mux.HandleFunc("PUT /api/preferences/alerts", auth.SessionOnly(h.setAlerts))
	mux.HandleFunc("PATCH /api/preferences/settings", auth.SessionOnly(h.changeSettings))
	mux.HandleFunc("PUT /api/preferences/hidden", auth.SessionOnly(h.setHidden))
	mux.HandleFunc("PUT /api/preferences/folded", auth.SessionOnly(h.setFolded))
	mux.HandleFunc("PUT /api/preferences/views", auth.SessionOnly(h.setViews))
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) { h.reply(w, r, nil) }

// setDashboard stores the layout of the user's overview.
func (h *Handler) setDashboard(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Widgets []Widget `json:"widgets"`
	}
	err := httpapi.ReadJSON(w, r, &req)
	if err == nil {
		user, _ := auth.UserFrom(r.Context())
		err = h.store.SetDashboard(r.Context(), user.ID, req.Widgets)
	}
	h.reply(w, r, err)
}

// setPinned replaces the servers the user pinned.
func (h *Handler) setPinned(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Servers []Server `json:"servers"`
	}
	err := httpapi.ReadJSON(w, r, &req)
	if err == nil {
		user, _ := auth.UserFrom(r.Context())
		err = h.store.SetPinned(r.Context(), user.ID, req.Servers)
	}
	h.reply(w, r, err)
}

// setAlerts stores which new warnings and errors pop up for the user.
func (h *Handler) setAlerts(w http.ResponseWriter, r *http.Request) {
	var alerts Alerts
	err := httpapi.ReadJSON(w, r, &alerts)
	if err == nil {
		user, _ := auth.UserFrom(r.Context())
		err = h.store.SetAlerts(r.Context(), user.ID, alerts)
	}
	h.reply(w, r, err)
}

// changeSettings changes the settings in the body, e.g. {"theme": "dark"}; null removes one.
func (h *Handler) changeSettings(w http.ResponseWriter, r *http.Request) {
	var change map[string]*string
	err := httpapi.ReadJSON(w, r, &change)
	if err == nil {
		user, _ := auth.UserFrom(r.Context())
		err = h.store.ChangeSettings(r.Context(), user.ID, change)
	}
	h.reply(w, r, err)
}

// setHidden stores the items of Needs attention the user hid.
func (h *Handler) setHidden(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Items []HiddenItem `json:"items"`
	}
	err := httpapi.ReadJSON(w, r, &req)
	if err == nil {
		user, _ := auth.UserFrom(r.Context())
		err = h.store.SetHidden(r.Context(), user.ID, req.Items)
	}
	h.reply(w, r, err)
}

// setFolded stores the groups of server lists that the user folded away.
func (h *Handler) setFolded(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Groups []string `json:"groups"`
	}
	err := httpapi.ReadJSON(w, r, &req)
	if err == nil {
		user, _ := auth.UserFrom(r.Context())
		err = h.store.SetFolded(r.Context(), user.ID, req.Groups)
	}
	h.reply(w, r, err)
}

// setViews replaces the views of the servers page that the user saved.
func (h *Handler) setViews(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Views []View `json:"views"`
	}
	err := httpapi.ReadJSON(w, r, &req)
	if err == nil {
		user, _ := auth.UserFrom(r.Context())
		err = h.store.SetViews(r.Context(), user.ID, req.Views)
	}
	h.reply(w, r, err)
}

// reply writes err of a change, or else the preferences of the signed-in user.
func (h *Handler) reply(w http.ResponseWriter, r *http.Request, err error) {
	var p Preferences
	if err == nil {
		user, _ := auth.UserFrom(r.Context())
		p, err = h.store.Get(r.Context(), user.ID)
	}
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, p)
}
