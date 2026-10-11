package server

import (
	"cmp"
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/network"
	"github.com/QwikByte/noryx/internal/master/operation"
	"github.com/QwikByte/noryx/internal/master/tag"
)

// Warnings tell the players of game servers that their server restarts or stops soon: before
// restarts and stops by hand, before those of schedules and in steps of workflows. They show
// in the chat, as a title or above the hotbar, as the settings say. Proxies get none, as they
// have no say for their players.

const (
	maxWarningMessage = 200
	// maxWarningMinutes is how long before at most a warning starts, as in schedules.
	maxWarningMinutes = 60
	maxWarningSteps   = 5
	// lateWarning is how late a warning may be sent, e.g. when the master just started.
	lateWarning = time.Minute
)

// Warnings are how the players are warned, as the settings of the master say.
type Warnings struct {
	// Restart and Stop warn of restarts and stops that have no warning of their own; {minutes}
	// in them stands for the minutes left.
	Restart string `json:"restart"`
	Stop    string `json:"stop"`
	// Steps are the minutes before at which a warning by hand repeats, the most first.
	Steps []uint32 `json:"steps"`
	// MaxMinutes is how long before at most a warning by hand starts.
	MaxMinutes uint32 `json:"maxMinutes"`
	// Kind is where warnings show: chat, title or actionbar, as with network.Message.
	Kind string `json:"kind"`
}

// DefaultWarnings apply until the settings change them.
func DefaultWarnings() Warnings {
	return Warnings{
		Restart: "The server restarts in {minutes} min.", Stop: "The server stops in {minutes} min.",
		Steps: []uint32{5, 1}, MaxMinutes: 10, Kind: "chat",
	}
}

// Clone returns a copy that shares no steps with w.
func (w Warnings) Clone() Warnings {
	w.Steps = slices.Clone(w.Steps)
	return w
}

// Validate checks the warnings: their texts like a warning of one's own, and up to
// maxWarningSteps steps below the longest lead time of 1 to maxWarningMinutes minutes.
func (w Warnings) Validate() error {
	switch {
	case !slices.Contains([]string{"chat", "title", "actionbar"}, w.Kind):
		return httpapi.Errorf(http.StatusBadRequest, "Show the warnings in the chat, as a title or above the hotbar.")
	case w.MaxMinutes < 1 || w.MaxMinutes > maxWarningMinutes:
		return httpapi.Errorf(http.StatusBadRequest, "Enter a longest lead time of 1 to %d minutes for warnings.", maxWarningMinutes)
	case len(w.Steps) > maxWarningSteps || slices.ContainsFunc(w.Steps, func(m uint32) bool { return m == 0 || m >= w.MaxMinutes }):
		return httpapi.Errorf(http.StatusBadRequest, "Repeat the warnings up to %d times, each at fewer minutes than the longest lead time.", maxWarningSteps)
	}
	_, err := w.Message("restart", "")
	if err == nil {
		_, err = w.Message("stop", "")
	}
	return err
}

// Message returns the message that warns the players of an action, restart or stop: the one
// given without the spaces around it, or the text of the action if it is empty. {minutes} in
// it stands for the minutes left. It fails unless the message fits the console as the warnings
// show.
func (w Warnings) Message(action, message string) (string, error) {
	message = cmp.Or(strings.TrimSpace(message), map[string]string{"restart": w.Restart, "stop": w.Stop}[action])
	err := CheckWarning(message)
	if err == nil {
		_, err = w.Commands(message, maxWarningMinutes) // the longest number of minutes
	}
	return message, err
}

// CheckWarning checks the text of a warning: a single line with up to maxWarningMessage characters.
func CheckWarning(message string) error {
	if message == "" || len(message) > maxWarningMessage || strings.ContainsFunc(message, unicode.IsControl) {
		return httpapi.Errorf(http.StatusBadRequest, "Enter a warning of up to %d characters on one line.", maxWarningMessage)
	}
	return nil
}

// Commands returns the console commands that warn the players that minutes are left, which
// network.Message makes, so that the message never becomes JSON by hand.
func (w Warnings) Commands(message string, minutes uint32) ([]string, error) {
	text := strings.ReplaceAll(message, "{minutes}", strconv.FormatUint(uint64(minutes), 10))
	return network.Message{Kind: w.Kind, Text: text}.Commands(network.Everyone)
}

// Warnable tells whether the players of a server can be warned: it is a running game server.
func Warnable(srv *noryxv1.Server) bool {
	return srv.GetState() == noryxv1.ServerState_SERVER_STATE_RUNNING && !srv.GetType().Proxy()
}

// Countdown calls warn at each of the minutes before at, the most first, and waits until at.
// Warnings more than a minute late are left out, e.g. as the master just started. It ends
// early with the error of ctx.
func Countdown(ctx context.Context, at time.Time, minutes []uint32, warn func(minutes uint32)) error {
	for _, m := range minutes {
		warnAt := at.Add(-time.Duration(m) * time.Minute)
		if time.Until(warnAt) < -lateWarning {
			continue
		}
		if err := sleepUntil(ctx, warnAt); err != nil {
			return err
		}
		warn(m)
	}
	return sleepUntil(ctx, at)
}

func sleepUntil(ctx context.Context, t time.Time) error {
	timer := time.NewTimer(time.Until(t))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// warning warns the players before servers stop or restart by hand: Minutes before, and again
// at the steps of the settings.
type warning struct {
	Minutes uint32 `json:"minutes"`
	// Message has {minutes} for the minutes left; empty is the text of the settings.
	Message string `json:"message"`
	// how is how the settings said to warn when the warning was asked for.
	how Warnings
}

// check validates the warning of an action on servers, if there is one, as the settings of
// conf say to warn now. A message of one's own needs the permission to send console commands,
// as the warning is one.
func (w *warning) check(r *http.Request, action string, servers []tag.Server, conf Config) error {
	if w == nil {
		return nil
	}
	how := conf.Warnings()
	w.Message, w.how = strings.TrimSpace(w.Message), how
	switch {
	case action != "restart" && action != "stop":
		return httpapi.Errorf(http.StatusBadRequest, "Only stopping and restarting warn the players.")
	case w.Minutes < 1 || w.Minutes > how.MaxMinutes:
		return httpapi.Errorf(http.StatusBadRequest, "Warn the players 1 to %d minutes before.", how.MaxMinutes)
	case w.Message != "" && !onAll(access.From(r.Context()), access.ConsoleCommands, servers):
		return access.Denied(access.ConsoleCommands)
	}
	var err error
	w.Message, err = how.Message(action, w.Message)
	if err == nil {
		logging.Note(r.Context(), slog.Uint64("warning_minutes", uint64(w.Minutes)), slog.String("warning", w.Message))
	}
	return err
}

// steps are the minutes before the action at which the players are warned.
func (w *warning) steps() []uint32 {
	return append([]uint32{w.Minutes}, slices.DeleteFunc(slices.Clone(w.how.Steps), func(m uint32) bool { return m >= w.Minutes })...)
}

// duration is how long the warning holds up the action.
func (w *warning) duration() time.Duration {
	if w == nil {
		return 0
	}
	return time.Duration(w.Minutes) * time.Minute
}

// countdown warns the players of the running game servers among servers, as w says, in the
// step warn of the operation of ctx, and waits until the time is up. Without a warning or a
// server to warn, it returns at once. It ends early if the operation is cancelled.
func (h *Handler) countdown(ctx context.Context, w *warning, servers []tag.Server) error {
	if w == nil {
		return nil
	}
	operation.Step(ctx, "warn")
	warned := h.warnable(ctx, servers)
	if len(warned) == 0 {
		return nil
	}
	total := int64(w.Minutes)
	return Countdown(ctx, time.Now().Add(w.duration()), w.steps(), func(minutes uint32) {
		operation.Count(ctx, total-int64(minutes), total, "minutes")
		commands, _ := w.how.Commands(w.Message, minutes) // check made sure they fit
		var wg sync.WaitGroup
		for _, s := range warned {
			wg.Go(func() {
				ctx, cancel := context.WithTimeout(ctx, queryTimeout)
				defer cancel()
				c, err := h.serverClient(ctx, s.NodeID)
				for _, command := range commands {
					if err == nil {
						err = serverActions["command"].call(ctx, c, s.ServerID, command)
					}
				}
				if err != nil { // the server stops anyway
					slog.Debug("Can't warn the players of a server", logging.KeyNode, s.NodeID, logging.KeyServer, s.ServerID, "err", err)
				}
			})
		}
		wg.Wait()
	})
}

// warnable returns the servers among servers whose players can be warned. Nodes that can't be
// reached are left out.
func (h *Handler) warnable(ctx context.Context, servers []tag.Server) []tag.Server {
	var nodes []string
	var out []tag.Server
	for _, s := range servers {
		if !slices.Contains(nodes, s.NodeID) {
			nodes = append(nodes, s.NodeID)
		}
	}
	for _, nodeID := range nodes {
		ctx, cancel := context.WithTimeout(ctx, queryTimeout)
		c, err := h.serverClient(ctx, nodeID)
		var res *noryxv1.ListServersResponse
		if err == nil {
			res, _ = c.ListServers(ctx, &noryxv1.ListServersRequest{})
		}
		cancel()
		for _, srv := range res.GetServers() {
			if s := (tag.Server{NodeID: nodeID, ServerID: srv.GetId()}); Warnable(srv) && slices.Contains(servers, s) {
				out = append(out, s)
			}
		}
	}
	return out
}

// warns needs a permission to do what warns the players: to stop or restart servers somewhere,
// or to see schedules or workflows.
func warns(_ *http.Request, g access.Grants) (access.Permission, bool) {
	for _, p := range []access.Permission{access.ServersRestart, access.ServersStop, access.PoliciesView, access.WorkflowsView} {
		if g.Somewhere(p, "") {
			return p, true
		}
	}
	return access.ServersRestart, false
}

// warnings tells the panel how the players are warned, also those who warn them but may not
// see the settings of the master.
func (h *Handler) warnings(w http.ResponseWriter, _ *http.Request) {
	httpapi.WriteJSON(w, http.StatusOK, h.conf.Warnings())
}
