package server

import (
	"context"
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
	"github.com/QwikByte/noryx/internal/master/operation"
	"github.com/QwikByte/noryx/internal/master/tag"
)

const maxBulk = 500

// serverAction is an action on a server, alone or among many, and the permission it needs.
type serverAction struct {
	need access.Permission
	call func(ctx context.Context, c noryxv1.ServerServiceClient, id, command string) error
}

var serverActions = map[string]serverAction{
	"start": {access.ServersStart, func(ctx context.Context, c noryxv1.ServerServiceClient, id, _ string) error {
		_, err := c.StartServer(ctx, &noryxv1.StartServerRequest{Id: id})
		return err
	}},
	"stop": {access.ServersStop, func(ctx context.Context, c noryxv1.ServerServiceClient, id, _ string) error {
		_, err := c.StopServer(ctx, &noryxv1.StopServerRequest{Id: id})
		return err
	}},
	"restart": {access.ServersRestart, func(ctx context.Context, c noryxv1.ServerServiceClient, id, _ string) error {
		_, err := c.RestartServer(ctx, &noryxv1.RestartServerRequest{Id: id})
		return err
	}},
	"command": {access.ConsoleCommands, func(ctx context.Context, c noryxv1.ServerServiceClient, id, command string) error {
		_, err := c.SendCommand(ctx, &noryxv1.SendCommandRequest{Id: id, Command: command})
		return err
	}},
}

// bulkResult tells how an action ended on one server.
type bulkResult struct {
	tag.Server
	Error string `json:"error,omitempty"`
}

// bulk starts, stops or restarts servers, or sends them a console command, as an operation
// that tells how it ended on each. Stops and restarts can warn the players first.
func (h *Handler) bulk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action  string       `json:"action"`
		Command string       `json:"command"`
		Servers []tag.Server `json:"servers"`
		// Warning warns the players before a stop or restart.
		Warning *warning `json:"warning"`
	}
	if err := httpapi.ReadJSON(w, r, &req); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	action, ok := serverActions[req.Action]
	switch {
	case !ok:
		httpapi.WriteError(w, r, httpapi.Errorf(http.StatusBadRequest, "Choose start, stop, restart or command."))
		return
	case req.Action == "command" && strings.TrimSpace(req.Command) == "":
		httpapi.WriteError(w, r, httpapi.Errorf(http.StatusBadRequest, "Enter a command."))
		return
	}
	err := checkServers(r, req.Servers, action.need)
	if err == nil {
		err = req.Warning.check(r, req.Action, req.Servers, h.conf)
	}
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	logging.Note(r.Context(), slog.String("action", req.Action), slog.String("command", req.Command), slog.Int("servers", len(req.Servers)))
	nodes := make([]string, len(req.Servers))
	for i, s := range req.Servers {
		nodes[i] = s.NodeID
	}
	steps := []string{"servers"}
	if req.Warning != nil {
		steps = []string{"warn", "servers"}
	}
	h.ops.Run(w, r, operation.Spec{
		Kind: "servers." + req.Action, Subject: strconv.Itoa(len(req.Servers)), Steps: steps,
		Status: http.StatusOK, Timeout: bulkTimeout(nodes) + req.Warning.duration(), Category: logging.Servers,
		Visible: func(g access.Grants) bool { return onAll(g, access.ServersView, req.Servers) },
		Cancel: func(_ *http.Request, g access.Grants) (access.Permission, bool) {
			return action.need, onAll(g, action.need, req.Servers)
		},
	}, func(ctx context.Context) (any, error) {
		if err := h.countdown(ctx, req.Warning, req.Servers); err != nil {
			return nil, err
		}
		operation.Step(ctx, "servers")
		results := make([]bulkResult, len(req.Servers))
		failed := func(i int, err error) { results[i] = bulkResult{Server: req.Servers[i], Error: httpapi.Message(err)} }
		operation.Each(ctx, nodes, func(ctx context.Context, i int) {
			s := req.Servers[i]
			results[i].Server = s
			err := h.moves.Check(s.ServerID)
			if err == nil {
				var conn noryxv1.ServerServiceClient
				if conn, err = h.serverClient(ctx, s.NodeID); err == nil {
					err = action.call(ctx, conn, s.ServerID, req.Command)
				}
			}
			if err != nil {
				failed(i, err)
			}
		}, failed)
		return map[string]any{"results": results}, nil
	})
}

// bulkTimeout is how long an action on the servers of nodes may take, 20 minutes at least:
// each node handles operation.PerNode of its servers at a time, and each round may take as
// long as a graceful stop with the longest stop timeout.
func bulkTimeout(nodes []string) time.Duration {
	count, rounds := map[string]int{}, 0
	for _, n := range nodes {
		count[n]++
		rounds = max(rounds, (count[n]+operation.PerNode-1)/operation.PerNode)
	}
	return max(20*time.Minute, time.Duration(rounds)*actionTimeout)
}

// onAll reports whether the grants allow p on all servers.
func onAll(g access.Grants, p access.Permission, servers []tag.Server) bool {
	return !slices.ContainsFunc(servers, func(s tag.Server) bool { return !g.On(p, s.NodeID, s.ServerID) })
}

// changeTags adds and removes tags of servers, all or none of them.
func (h *Handler) changeTags(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Servers []tag.Server `json:"servers"`
		Add     []string     `json:"add"`
		Remove  []string     `json:"remove"`
	}
	if err := httpapi.ReadJSON(w, r, &req); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	if len(req.Add)+len(req.Remove) > 2*tag.MaxPerServer {
		httpapi.WriteError(w, r, httpapi.Errorf(http.StatusBadRequest, "A server can have up to %d tags.", tag.MaxPerServer))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	err := checkServers(r, req.Servers, access.ServersSettings)
	if err == nil {
		err = h.checkExist(ctx, req.Servers)
	}
	if err == nil {
		err = h.tags.Change(ctx, req.Servers, req.Add, req.Remove)
	}
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	logging.Note(r.Context(), slog.Int("servers", len(req.Servers)), slog.Any("add", req.Add), slog.Any("remove", req.Remove))
	if len(req.Remove) > 0 {
		// In the background, as nodes that can't be reached would hold up the answer.
		go h.sets.Left(context.WithoutCancel(r.Context()), req.Servers)
	}
	w.WriteHeader(http.StatusNoContent)
}

// checkServers checks that a bulk request names up to maxBulk different servers, on each of
// which the user has the permission.
func checkServers(r *http.Request, servers []tag.Server, need access.Permission) error {
	if len(servers) == 0 || len(servers) > maxBulk {
		return httpapi.Errorf(http.StatusBadRequest, "Choose up to %d servers.", maxBulk)
	}
	grants, seen := access.From(r.Context()), map[tag.Server]bool{}
	for _, s := range servers {
		if s.NodeID == "" || s.ServerID == "" || seen[s] {
			return httpapi.Errorf(http.StatusBadRequest, "Choose up to %d different servers.", maxBulk)
		}
		seen[s] = true
		if !grants.On(need, s.NodeID, s.ServerID) {
			return access.Denied(need)
		}
	}
	return nil
}

// checkExist checks that the servers exist and don't move, as what refers to them would be lost.
func (h *Handler) checkExist(ctx context.Context, servers []tag.Server) error {
	exist, listed := map[tag.Server]bool{}, map[string]bool{}
	for _, s := range servers {
		if err := h.moves.Check(s.ServerID); err != nil {
			return err
		}
		if listed[s.NodeID] {
			continue
		}
		listed[s.NodeID] = true
		c, err := h.serverClient(ctx, s.NodeID)
		var res *noryxv1.ListServersResponse
		if err == nil {
			res, err = c.ListServers(ctx, &noryxv1.ListServersRequest{})
		}
		if err != nil {
			return err
		}
		for _, srv := range res.GetServers() {
			exist[tag.Server{NodeID: s.NodeID, ServerID: srv.GetId()}] = true
		}
	}
	for _, s := range servers {
		if !exist[s] {
			return httpapi.Errorf(http.StatusNotFound, "Server not found.")
		}
	}
	return nil
}
