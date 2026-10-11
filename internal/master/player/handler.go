package player

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/network"
	"github.com/QwikByte/noryx/internal/master/operation"
)

const (
	maxServers = 500
	// MaxPlayers is the most players of an action or message, each of which can go to
	// hundreds of servers.
	MaxPlayers    = 50
	maxBanYears   = 10
	changeTimeout = 10 * time.Minute
	moveTimeout   = time.Minute
)

type Handler struct {
	svc   *Service
	seen  *Sightings
	faces *Faces
	ops   *operation.Operations
}

func NewHandler(svc *Service, seen *Sightings, faces *Faces, ops *operation.Operations) *Handler {
	return &Handler{svc: svc, seen: seen, faces: faces, ops: ops}
}

func (h *Handler) Register(mux access.Mux) {
	mux.Handle("POST /api/players/actions", access.SignedIn, h.change)
	mux.Handle("POST /api/players/message", access.SignedIn, h.message)
	mux.Handle("GET /api/players/lists", access.SignedIn, h.lists)
	// Only the sightings on the servers the user may see count. Deleting all of them needs the
	// permission to change the settings, which can stop recording them and keep them for a day.
	mux.Handle("GET /api/players/seen", access.SignedIn, h.seenPlayers)
	mux.Handle("DELETE /api/players/seen", access.Everywhere(access.SettingsEdit), h.deleteSeen)
	mux.Handle("GET /api/players/seen/{name}", access.SignedIn, h.history)
	mux.Handle("GET /api/players/faces/{name}", access.SignedIn, h.face)
	mux.Handle("POST /api/networks/{id}/players/send", access.Everywhere(access.NetworksView), h.send)
	mux.Handle("POST /api/networks/{id}/players/move", access.Everywhere(access.NetworksView), h.move)
}

// change kicks, bans, pardons, whitelists or makes operator up to MaxPlayers players on
// servers, or turns their whitelist on or off, as an operation that tells how it ended on
// each. A ban with an end (until) is pardoned then.
func (h *Handler) change(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string `json:"action"`
		// Name is one player, Names several.
		Name    string        `json:"name"`
		Names   []string      `json:"names"`
		Reason  string        `json:"reason"`
		Until   *time.Time    `json:"until"`
		Servers []network.Ref `json:"servers"`
	}
	if err := httpapi.ReadJSON(w, r, &req); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	action := noryxv1.ParsePlayerAction(req.Action)
	names, err := playerNames(append(req.Names, req.Name), !action.Global())
	var ends int64
	switch {
	case err != nil:
	case req.Until != nil && action != noryxv1.PlayerAction_PLAYER_ACTION_BAN:
		err = httpapi.Errorf(http.StatusBadRequest, "Only bans end at a time.")
	case req.Until != nil && (req.Until.Before(time.Now().Add(time.Minute)) || req.Until.After(time.Now().AddDate(maxBanYears, 0, 0))):
		err = httpapi.Errorf(http.StatusBadRequest, "A ban ends between a minute and %d years from now.", maxBanYears)
	case req.Until != nil:
		ends = req.Until.Unix()
	}
	changes := make([]*noryxv1.PlayerChange, len(names))
	for i, name := range names {
		changes[i] = &noryxv1.PlayerChange{Action: action, Name: name, Reason: strings.TrimSpace(req.Reason), EndsUnix: ends}
		if msg := changes[i].Problem(); msg != "" && err == nil {
			err = httpapi.Errorf(http.StatusBadRequest, "%s", msg)
		}
	}
	// Operators may run any command in the game.
	need := []access.Permission{access.PlayersManage}
	if action == noryxv1.PlayerAction_PLAYER_ACTION_OP || action == noryxv1.PlayerAction_PLAYER_ACTION_DEOP {
		need = append(need, access.ConsoleCommands)
	}
	if err == nil {
		err = check(r, req.Servers, need...)
	}
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	logging.Note(r.Context(), slog.String("action", req.Action), slog.Any("players", names), slog.Int("servers", len(req.Servers)))
	if req.Until != nil {
		logging.Note(r.Context(), slog.Time("until", *req.Until))
	}
	subject := subjectOf(names)
	if action.Global() {
		subject = strconv.Itoa(len(req.Servers))
	}
	h.ops.Run(w, r, operation.Spec{
		Kind: "players." + action.Slug(), Subject: subject, Steps: []string{"servers"},
		Status: http.StatusOK, Timeout: changeTimeout, Category: logging.Players,
		Visible: func(g access.Grants) bool { return onAll(g, access.ServersView, req.Servers) },
		Cancel: func(_ *http.Request, g access.Grants) (access.Permission, bool) {
			for _, p := range need {
				if !onAll(g, p, req.Servers) {
					return p, false
				}
			}
			return "", true
		},
	}, func(ctx context.Context) (any, error) {
		for _, c := range changes {
			if err := h.svc.identify(ctx, c); err != nil {
				return nil, err
			}
		}
		return map[string]any{"results": h.svc.Change(ctx, changes, req.Servers)}, nil
	})
}

// playerNames returns the valid names of up to MaxPlayers players, each once regardless of
// case, leaving out empty ones; none, or one empty name, if wanted is false.
func playerNames(given []string, wanted bool) ([]string, error) {
	if len(given) > MaxPlayers+1 { // with the one of name
		return nil, httpapi.Errorf(http.StatusBadRequest, "Enter 1 to %d players.", MaxPlayers)
	}
	var names []string
	for _, name := range given {
		if name = strings.TrimSpace(name); name != "" && !slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(n, name) }) {
			names = append(names, name)
		}
	}
	switch {
	case !wanted && len(names) > 0:
		return nil, httpapi.Errorf(http.StatusBadRequest, "Turning the whitelist on or off takes no player.")
	case !wanted:
		return []string{""}, nil
	case len(names) == 0 || len(names) > MaxPlayers:
		return nil, httpapi.Errorf(http.StatusBadRequest, "Enter 1 to %d players.", MaxPlayers)
	}
	for _, name := range names {
		if !noryxv1.ValidPlayerName(name) {
			return nil, httpapi.Errorf(http.StatusBadRequest, "%q isn't the name of a player: up to 16 letters, digits and underscores.", name)
		}
	}
	return names, nil
}

// subjectOf names the players of an operation, the first few of many.
func subjectOf(names []string) string {
	const shown = 3
	if len(names) <= shown {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:shown], ", ") + " +" + strconv.Itoa(len(names)-shown)
}

// lists returns the lists of the game servers of a network (?network=), of one server
// (?node=&server=), or of all that the user may see.
func (h *Handler) lists(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	grants, q := access.From(ctx), r.URL.Query()
	visible := func(ref network.Ref) bool { return grants.On(access.ServersView, ref.NodeID, ref.ServerID) }
	var servers []network.Ref
	var err error
	switch {
	case q.Has("network"):
		if !grants.Has(access.NetworksView) {
			err = access.Denied(access.NetworksView)
			break
		}
		var n network.Network
		if n, err = h.svc.networks.Get(ctx, q.Get("network")); err == nil {
			for _, b := range n.Backends {
				if visible(b.Ref) {
					servers = append(servers, b.Ref)
				}
			}
		}
	case q.Has("server"):
		ref := network.Ref{NodeID: q.Get("node"), ServerID: q.Get("server")}
		switch {
		case ref.NodeID == "" || ref.ServerID == "":
			err = httpapi.Errorf(http.StatusBadRequest, "Choose a node and a server.")
		case !visible(ref):
			err = access.Denied(access.ServersView)
		}
		servers = []network.Ref{ref}
	default:
		servers, err = h.svc.GameServers(ctx, visible)
	}
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	lists := h.svc.Lists(ctx, servers)
	if !q.Has("joined") {
		lists.Joined = nil // only asked for, as servers can have many
	}
	httpapi.WriteJSON(w, http.StatusOK, lists)
}

// message shows a message to players on servers: in the chat, as a title or above the hotbar.
// Like the message to a network, it needs the permission to send console commands to each.
func (h *Handler) message(w http.ResponseWriter, r *http.Request) {
	var req struct {
		network.Message
		Names   []string      `json:"names"`
		Servers []network.Ref `json:"servers"`
	}
	err := httpapi.ReadJSON(w, r, &req)
	var names []string
	if err == nil {
		names, err = playerNames(req.Names, true)
	}
	if err == nil {
		err = check(r, req.Servers, access.ConsoleCommands)
	}
	var results []Result
	if err == nil {
		logging.Note(r.Context(), slog.String("kind", req.Kind), slog.Any("players", names), slog.Int("servers", len(req.Servers)))
		results, err = h.svc.Message(r.Context(), req.Message, names, req.Servers)
	}
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"results": results})
}

// face serves the face of a player as a PNG image, so that the browser contacts neither
// Mojang nor GeyserMC.
func (h *Handler) face(w http.ResponseWriter, r *http.Request) {
	data, err := h.faces.Face(r.Context(), r.PathValue("name"))
	switch {
	case errors.Is(err, ErrNoFace):
		w.Header().Set("Cache-Control", "private, max-age=3600")
	case err != nil:
		w.Header().Set("Cache-Control", "no-store")
	}
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	_, _ = w.Write(data) //nolint:gosec // a PNG image the master drew itself, sandboxed
}

// seenPlayers returns the players seen on the servers the user may see, of a network
// (?network=), of one server (?node=&server=) or all, whose name contains ?q=, those seen last
// first and up to ?limit=, and whether the master records them.
func (h *Handler) seenPlayers(w http.ResponseWriter, r *http.Request) {
	ctx, q := r.Context(), r.URL.Query()
	visible, err := h.visible(ctx, q.Get("network"))
	if server := (network.Ref{NodeID: q.Get("node"), ServerID: q.Get("server")}); err == nil && q.Has("server") {
		all := visible
		visible = func(ref network.Ref) bool { return ref == server && all(ref) }
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > MaxSeen {
		limit = MaxSeen
	}
	var players []Player
	var total int
	if err == nil {
		players, total, err = h.seen.SeenPlayers(ctx, strings.TrimSpace(q.Get("q")), limit, visible)
	}
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"players": players, "total": total, "recording": h.seen.conf.SeenPlayers()})
}

// deleteSeen deletes where and when all players were seen.
func (h *Handler) deleteSeen(w http.ResponseWriter, r *http.Request) {
	if err := h.seen.DeleteAll(r.Context()); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// history returns where and when a player was online on the servers the user may see.
func (h *Handler) history(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !noryxv1.ValidPlayerName(name) {
		httpapi.WriteError(w, r, httpapi.Errorf(http.StatusBadRequest, "Enter the name of a player: up to 16 letters, digits and underscores."))
		return
	}
	visible, _ := h.visible(r.Context(), "")
	history, err := h.seen.History(r.Context(), name, visible)
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, history)
}

// visible returns whether the user may see a server, of the network with the given ID unless
// it is empty.
func (h *Handler) visible(ctx context.Context, networkID string) (func(network.Ref) bool, error) {
	grants := access.From(ctx)
	visible := func(ref network.Ref) bool { return grants.On(access.ServersView, ref.NodeID, ref.ServerID) }
	if networkID == "" {
		return visible, nil
	}
	if !grants.Has(access.NetworksView) {
		return nil, access.Denied(access.NetworksView)
	}
	n, err := h.svc.networks.Get(ctx, networkID)
	return func(ref network.Ref) bool {
		return visible(ref) && slices.ContainsFunc(n.Backends, func(b network.Backend) bool { return b.Ref == ref })
	}, err
}

// send sends a player to another server of a network.
func (h *Handler) send(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		Server string `json:"server"`
	}
	n, err := h.sender(w, r, &req)
	if err == nil {
		logging.Note(r.Context(), slog.String("name", n.Name), slog.String("player", req.Name), slog.String("server", req.Server))
		err = h.svc.Send(r.Context(), n, req.Name, req.Server)
	}
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// move sends the players of a game server of a network to another server of it, e.g. before
// the server restarts, and tells how many it sent.
func (h *Handler) move(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Server network.Ref `json:"server"`
	}
	n, err := h.sender(w, r, &req)
	var moved int
	if err == nil {
		logging.Note(r.Context(), slog.String("name", n.Name), slog.String(logging.KeyNode, req.Server.NodeID), slog.String(logging.KeyServer, req.Server.ServerID))
		ctx, cancel := context.WithTimeout(r.Context(), moveTimeout)
		defer cancel()
		moved, err = h.svc.networks.MovePlayers(ctx, n, req.Server)
	}
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]int{"players": moved})
}

// sender reads a request to send players through the proxy of a network into req and returns
// the network, if the user may manage the players of its proxy.
func (h *Handler) sender(w http.ResponseWriter, r *http.Request, req any) (network.Network, error) {
	err := httpapi.ReadJSON(w, r, req)
	var n network.Network
	if err == nil {
		n, err = h.svc.networks.Get(r.Context(), r.PathValue("id"))
	}
	if err == nil && !access.From(r.Context()).On(access.PlayersManage, n.Proxy.NodeID, n.Proxy.ServerID) {
		err = access.Denied(access.PlayersManage)
	}
	return n, err
}

// check checks that a request names up to maxServers different servers, on each of which
// the user has the permissions.
func check(r *http.Request, servers []network.Ref, need ...access.Permission) error {
	if len(servers) == 0 || len(servers) > maxServers {
		return httpapi.Errorf(http.StatusBadRequest, "Choose up to %d servers.", maxServers)
	}
	grants, seen := access.From(r.Context()), map[network.Ref]bool{}
	for _, s := range servers {
		if s.NodeID == "" || s.ServerID == "" || seen[s] {
			return httpapi.Errorf(http.StatusBadRequest, "Choose up to %d different servers.", maxServers)
		}
		seen[s] = true
		for _, p := range need {
			if !grants.On(p, s.NodeID, s.ServerID) {
				return access.Denied(p)
			}
		}
	}
	return nil
}

// onAll reports whether the grants allow p on all servers.
func onAll(g access.Grants, p access.Permission, servers []network.Ref) bool {
	return !slices.ContainsFunc(servers, func(s network.Ref) bool { return !g.On(p, s.NodeID, s.ServerID) })
}
