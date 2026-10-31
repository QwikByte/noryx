package network

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/operation"
)

const (
	// readyTimeout is how long a server may take to run again after it restarted.
	readyTimeout = 5 * time.Minute
	pollInterval = 2 * time.Second
	// moveWait gives players a moment to move to another server before theirs restarts.
	moveWait = 3 * time.Second
	// MaxBatch is the most servers that restart at a time in a rolling restart.
	MaxBatch = 50
	// groupAllowance is what a group of servers needs in a rolling restart besides the longest
	// stop timeout among them: a minute to move the players and ask the nodes, two to start, as
	// configureTimeout allows, and readyTimeout to run again.
	groupAllowance = time.Minute + 2*time.Minute + readyTimeout
	// minRollingTimeout is the least a rolling restart gets as a whole.
	minRollingTimeout = time.Hour
)

var running = noryxv1.ServerState_SERVER_STATE_RUNNING

// RollingRestart restarts the running game servers of a network a few at a time, so that the
// network stays open: the players of a server move to another one first, and the next
// servers restart once these run again. The servers that players join first restart last
// and one at a time, so that players always find one. If only names servers, only these
// restart, e.g. those whose files changed. Once it began, it can't be cancelled, and it takes
// as long as its servers need, also beyond the deadline of ctx; see rollingTimeout.
func (s *Service) RollingRestart(ctx context.Context, n Network, batch int, only ...Ref) error {
	if batch < 1 || batch > MaxBatch {
		return httpapi.Errorf(http.StatusBadRequest, "Restart from 1 to %d servers at a time.", MaxBatch)
	}
	ctx = context.WithoutCancel(ctx)
	operation.Step(ctx, "servers")
	listed, err := s.listed(ctx, append(refs(n.Backends), n.Proxy))
	if err != nil {
		return err
	}
	var first, last, others []Backend
	for _, b := range n.Backends {
		switch {
		case listed[b.Ref].GetState() != running:
		case len(only) > 0 && !slices.Contains(only, b.Ref):
			others = append(others, b) // players can move there
		case slices.Contains(n.Try, b.Name):
			last = append(last, b)
		default:
			first = append(first, b)
		}
	}
	if len(first)+len(last) == 0 {
		return httpapi.Errorf(http.StatusConflict, "No server of the network runs.")
	}
	// The server that players join first restarts last.
	slices.SortFunc(last, func(a, b Backend) int { return cmp.Compare(slices.Index(n.Try, b.Name), slices.Index(n.Try, a.Name)) })
	restarting := slices.Concat(first, last)
	groups := slices.Collect(slices.Chunk(first, batch))
	for _, b := range last {
		groups = append(groups, []Backend{b})
	}
	ctx, cancel := context.WithTimeout(ctx, rollingTimeout(groups, listed))
	defer cancel()
	total, done := int64(len(restarting)), int64(0)
	operation.Count(ctx, 0, total, "servers")
	for _, group := range groups {
		if listed[n.Proxy].GetState() == running {
			if _, err := s.moveOff(ctx, n, group, slices.Concat(restarting, others)); err != nil {
				return err
			}
		}
		if err := s.restart(ctx, group); err != nil {
			return err
		}
		done += int64(len(group))
		operation.Count(ctx, done, total, "servers")
	}
	return nil
}

// rollingTimeout is how long a rolling restart of groups of servers, one group after the
// other, may take: for each group the longest stop timeout among its servers and
// groupAllowance, and minRollingTimeout at least.
func rollingTimeout(groups [][]Backend, listed map[Ref]*noryxv1.Server) time.Duration {
	total := time.Duration(0)
	for _, group := range groups {
		longest := time.Duration(0)
		for _, b := range group {
			longest = max(longest, noryxv1.StopTimeout(listed[b.Ref].GetStopTimeoutSeconds()))
		}
		total += longest + groupAllowance
	}
	return max(total, minRollingTimeout)
}

// MovePlayers sends the players of a running game server of a network to another running
// server of it through the proxy, as before a restart, and returns how many it sent.
func (s *Service) MovePlayers(ctx context.Context, n Network, from Ref) (int, error) {
	if err := n.checkBackends([]Ref{from}); err != nil {
		return 0, err
	}
	listed, err := s.listed(ctx, append(refs(n.Backends), n.Proxy))
	if err != nil {
		return 0, err
	}
	var group, up []Backend
	for _, b := range n.Backends {
		switch {
		case listed[b.Ref].GetState() != running:
		case b.Ref == from:
			group = []Backend{b}
		default:
			up = append(up, b)
		}
	}
	switch {
	case listed[n.Proxy].GetState() != running:
		return 0, httpapi.Errorf(http.StatusConflict, "The proxy doesn't run.")
	case len(group) == 0:
		return 0, httpapi.Errorf(http.StatusConflict, "The server doesn't run.")
	case len(up) == 0:
		return 0, httpapi.Errorf(http.StatusConflict, "No other server of the network runs, so the players have nowhere to go.")
	}
	return s.moveOff(ctx, n, group, up)
}

// moveOff sends the players of servers to another running server through the proxy: to the
// first that players join, if it can, and gives them a moment to move. It fails if the proxy
// can't send anyone, e.g. without a send command or while its node can't be reached, so that
// the servers don't restart. It returns how many players it sent.
func (s *Service) moveOff(ctx context.Context, n Network, group, up []Backend) (int, error) {
	others := slices.DeleteFunc(slices.Clone(up), func(b Backend) bool { return slices.ContainsFunc(group, b.same) })
	if len(others) == 0 {
		return 0, nil // the players have nowhere to go
	}
	target := others[0].Name
	for _, name := range n.Try {
		if slices.ContainsFunc(others, func(b Backend) bool { return b.Name == name }) {
			target = name
			break
		}
	}
	players := s.players(ctx, group)
	if len(players) == 0 {
		return 0, nil
	}
	conn, err := s.nodes.Conn(ctx, n.Proxy.NodeID)
	if err != nil {
		return 0, err
	}
	proxy := noryxv1.NewServerServiceClient(conn)
	sent := 0
	for _, name := range players {
		command, ok := n.SendCommand(name, target)
		if !ok {
			continue // it would send other players too
		}
		// Velocity only answers if it can't send, which would cost a moment per player.
		_, err := proxy.SendCommand(ctx, &noryxv1.SendCommandRequest{Id: n.Proxy.ServerID, Command: command, NoWait: true})
		switch {
		case status.Code(err) == codes.FailedPrecondition:
			return sent, httpapi.Errorf(http.StatusConflict, "The players can't move to another server. %s", status.Convert(err).Message())
		case err != nil:
			slog.Debug("Can't move a player to another server", "player", name, "err", err)
		default:
			sent++
		}
	}
	select {
	case <-ctx.Done():
	case <-time.After(moveWait):
	}
	return sent, nil
}

// players returns the names of the players on servers, as their agents measured them last.
func (s *Service) players(ctx context.Context, servers []Backend) []string {
	var names []string
	for _, nodeID := range nodesOf(servers) {
		ctx, cancel := context.WithTimeout(ctx, queryTimeout)
		conn, err := s.nodes.Conn(ctx, nodeID)
		var stats *noryxv1.GetStatsResponse
		if err == nil {
			stats, _ = noryxv1.NewStatsServiceClient(conn).GetStats(ctx, &noryxv1.GetStatsRequest{}) // without, players get kicked instead
		}
		cancel()
		for _, srv := range stats.GetServers() {
			if slices.ContainsFunc(servers, func(b Backend) bool { return b.Ref == Ref{nodeID, srv.GetId()} }) {
				// Names come from the server, so they must not add to the command.
				names = append(names, slices.DeleteFunc(srv.GetPlayers().GetNames(), func(n string) bool { return !noryxv1.ValidPlayerName(n) })...)
			}
		}
	}
	return names
}

// restart restarts servers at the same time and waits until they run again.
func (s *Service) restart(ctx context.Context, servers []Backend) error {
	errs := make([]error, len(servers))
	var wg sync.WaitGroup
	for i, b := range servers {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(ctx, configureTimeout)
			defer cancel()
			conn, err := s.nodes.Conn(ctx, b.NodeID)
			if err == nil {
				_, err = noryxv1.NewServerServiceClient(conn).RestartServer(ctx, &noryxv1.RestartServerRequest{Id: b.ServerID})
			}
			if err != nil {
				errs[i] = fmt.Errorf("%s: %s", b.Name, httpapi.Message(err))
			}
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return httpapi.Errorf(http.StatusBadGateway, "%s", err)
	}
	err := waitFor(ctx, readyTimeout, func(ctx context.Context) (bool, error) {
		listed, err := s.listed(ctx, refs(servers))
		if err != nil {
			return false, err
		}
		for _, b := range servers {
			switch listed[b.Ref].GetState() {
			case running:
			case noryxv1.ServerState_SERVER_STATE_STARTING:
				return false, nil
			default:
				return false, httpapi.Errorf(http.StatusBadGateway, "%s didn't start again. Its console tells why.", b.Name)
			}
		}
		return true, nil
	})
	return timedOut(err, "The servers didn't start again within 5 minutes.")
}

// listed returns servers as their nodes list them, e.g. with their states, with one listing
// per node.
func (s *Service) listed(ctx context.Context, servers []Ref) (map[Ref]*noryxv1.Server, error) {
	listed := map[Ref]*noryxv1.Server{}
	for _, nodeID := range nodesOf(servers) {
		ctx, cancel := context.WithTimeout(ctx, queryTimeout)
		conn, err := s.nodes.Conn(ctx, nodeID)
		var res *noryxv1.ListServersResponse
		if err == nil {
			res, err = noryxv1.NewServerServiceClient(conn).ListServers(ctx, &noryxv1.ListServersRequest{})
		}
		cancel()
		if err != nil {
			return nil, err
		}
		for _, srv := range res.GetServers() {
			listed[Ref{nodeID, srv.GetId()}] = srv
		}
	}
	return listed, nil
}

// waitFor asks done every few seconds until it is done, fails, or the time is up.
func waitFor(ctx context.Context, timeout time.Duration, done func(ctx context.Context) (bool, error)) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		ok, err := done(ctx)
		if ok || err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// timedOut replaces the error of a wait whose time is up with a message for the operator.
func timedOut(err error, msg string) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return httpapi.Errorf(http.StatusGatewayTimeout, "%s", msg)
	}
	return err
}

// nodesOf returns the nodes of servers, each once.
func nodesOf[T interface{ node() string }](servers []T) []string {
	var nodes []string
	for _, s := range servers {
		if !slices.Contains(nodes, s.node()) {
			nodes = append(nodes, s.node())
		}
	}
	return nodes
}

func (r Ref) node() string { return r.NodeID }
