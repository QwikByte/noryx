// Package player manages the players of game servers with the commands and lists of
// Minecraft: kicks, bans, the whitelist and operators. A server that doesn't run gets a
// change once it runs, so that a ban also reaches the stopped servers of a network, and a
// temporary ban ends with a pardon that waits until its time.
package player

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/datadir"
	"github.com/QwikByte/noryx/internal/agent/grpcerr"
	"github.com/QwikByte/noryx/internal/agent/properties"
	"github.com/QwikByte/noryx/internal/agent/runtime"
)

const (
	running  = noryxv1.ServerState_SERVER_STATE_RUNNING
	interval = 5 * time.Second
	// changeTimeout bounds each waiting change, so that a server that doesn't answer its
	// console keeps the change for the next attempt.
	changeTimeout = 15 * time.Second
)

// commands are the console commands of the actions, which take the player and the reason.
var commands = map[noryxv1.PlayerAction]string{
	noryxv1.PlayerAction_PLAYER_ACTION_KICK:             "kick",
	noryxv1.PlayerAction_PLAYER_ACTION_BAN:              "ban",
	noryxv1.PlayerAction_PLAYER_ACTION_PARDON:           "pardon",
	noryxv1.PlayerAction_PLAYER_ACTION_WHITELIST_ADD:    "whitelist add",
	noryxv1.PlayerAction_PLAYER_ACTION_WHITELIST_REMOVE: "whitelist remove",
	noryxv1.PlayerAction_PLAYER_ACTION_OP:               "op",
	noryxv1.PlayerAction_PLAYER_ACTION_DEOP:             "deop",
	noryxv1.PlayerAction_PLAYER_ACTION_WHITELIST_ON:     "whitelist on",
	noryxv1.PlayerAction_PLAYER_ACTION_WHITELIST_OFF:    "whitelist off",
}

var formatting = regexp.MustCompile(`§.`)

type Service struct {
	noryxv1.UnimplementedPlayerServiceServer
	rt    runtime.Runtime
	locks serverLocks    // guard the file of waiting changes and the whitelist of each server
	wg    sync.WaitGroup // the attempts at the waiting changes of servers

	mu sync.Mutex // guards waiting and applying; never held while a server is asked
	// waiting tells when the next waiting change of each server is due, the zero time if none
	// waits; servers the agent didn't look at yet are missing.
	waiting map[string]time.Time
	// applying are the servers whose waiting changes are being attempted.
	applying map[string]bool
}

func NewService(rt runtime.Runtime) *Service {
	return &Service{rt: rt, locks: serverLocks{byID: map[string]*serverLock{}}, waiting: map[string]time.Time{}, applying: map[string]bool{}}
}

func (s *Service) GetPlayerLists(ctx context.Context, req *noryxv1.GetPlayerListsRequest) (*noryxv1.GetPlayerListsResponse, error) {
	_, dir, err := s.open(ctx, req.GetServerId())
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	res := &noryxv1.GetPlayerListsResponse{}
	var errs [5]error
	res.Banned, errs[0] = readList(dir, bannedFile)
	res.Whitelisted, errs[1] = readList(dir, whitelistFile)
	res.Operators, errs[2] = readList(dir, opsFile)
	var props map[string]string
	props, errs[3] = properties.Read(dir)
	res.WhitelistEnabled = props["white-list"] == "true"
	res.Pending, errs[4] = readPending(dir) // its file is replaced at once, so it needs no lock
	if err := errors.Join(errs[:]...); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	// The server writes its cache of players, so a broken one leaves out only the players who joined.
	res.Joined, _ = readJoined(dir)
	// Temporary bans end with a pardon that waits.
	for _, p := range res.GetPending() {
		for _, b := range res.GetBanned() {
			if p.GetDueUnix() > 0 && b.GetExpiresUnix() == 0 && strings.EqualFold(b.GetName(), p.GetName()) {
				b.ExpiresUnix = p.GetDueUnix()
			}
		}
	}
	return res, nil
}

func (s *Service) ChangePlayer(ctx context.Context, req *noryxv1.ChangePlayerRequest) (*noryxv1.ChangePlayerResponse, error) {
	if msg := problem(req.GetChange()); msg != "" {
		return nil, status.Error(codes.InvalidArgument, msg)
	}
	srv, dir, err := s.open(ctx, req.GetServerId())
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	return s.change(ctx, srv, dir, req.GetChange())
}

func (s *Service) ChangePlayers(ctx context.Context, req *noryxv1.ChangePlayersRequest) (*noryxv1.ChangePlayersResponse, error) {
	changes := req.GetChanges()
	if len(changes) == 0 || len(changes) > noryxv1.MaxPlayerChanges {
		return nil, status.Errorf(codes.InvalidArgument, "Change 1 to %d players at once.", noryxv1.MaxPlayerChanges)
	}
	for _, c := range changes {
		if msg := problem(c); msg != "" {
			return nil, status.Error(codes.InvalidArgument, msg)
		}
	}
	srv, dir, err := s.open(ctx, req.GetServerId())
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	res := &noryxv1.ChangePlayersResponse{}
	for _, c := range changes {
		r, err := s.change(ctx, srv, dir, c)
		res.Results = append(res.Results, &noryxv1.PlayerChangeResult{Pending: r.GetPending(), Output: r.GetOutput(), Error: status.Convert(err).Message()})
	}
	return res, nil
}

// problem returns why a change of a request is invalid: changes only wait for a time that the
// agent sets itself.
func problem(c *noryxv1.PlayerChange) string {
	if c.GetDueUnix() != 0 {
		return "Only bans take a time, at which they end."
	}
	return c.Problem()
}

// change makes a valid change on a server: at once if it runs, otherwise once it does.
func (s *Service) change(ctx context.Context, srv runtime.Server, dir *datadir.Dir, c *noryxv1.PlayerChange) (*noryxv1.ChangePlayerResponse, error) {
	if srv.State == running {
		return s.run(ctx, srv.ID, dir, c)
	}
	if c.GetAction() == noryxv1.PlayerAction_PLAYER_ACTION_KICK {
		return nil, status.Error(codes.FailedPrecondition, "The server doesn't run.")
	}
	unlock, err := s.locks.lock(ctx, srv.ID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := s.note(srv.ID, dir, c, true); err != nil {
		return nil, err
	}
	return &noryxv1.ChangePlayerResponse{Pending: true}, nil
}

// run changes a player on a running server. Changes of the whitelist hold the server's lock,
// as those of Bedrock players replace its file, which the server overwrites when its list
// changes; bans and pardons hold it to note the pardons that end temporary bans.
func (s *Service) run(ctx context.Context, id string, dir *datadir.Dir, change *noryxv1.PlayerChange) (*noryxv1.ChangePlayerResponse, error) {
	switch change.GetAction() {
	case noryxv1.PlayerAction_PLAYER_ACTION_WHITELIST_ADD, noryxv1.PlayerAction_PLAYER_ACTION_WHITELIST_REMOVE,
		noryxv1.PlayerAction_PLAYER_ACTION_BAN, noryxv1.PlayerAction_PLAYER_ACTION_PARDON:
		unlock, err := s.locks.lock(ctx, id)
		if err != nil {
			return nil, err
		}
		defer unlock()
	}
	if bedrockWhitelist(change) {
		return s.whitelistBedrock(ctx, id, dir, change)
	}
	out, err := s.rt.SendCommand(ctx, id, command(change))
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	res := &noryxv1.ChangePlayerResponse{Output: strings.TrimSpace(formatting.ReplaceAllString(out, ""))}
	if err := s.note(id, dir, change, false); err != nil {
		return nil, status.Errorf(codes.Internal, "%s, but the pardons that wait for the end of temporary bans can't be updated: %s", res.GetOutput(), status.Convert(err).Message())
	}
	return res, nil
}

// note notes a change in the waiting changes of a server, with the server's lock held: the
// change itself if it waits for the server to run, and for a ban or pardon, the pardon that
// ends a temporary ban instead of one of an earlier ban.
func (s *Service) note(id string, dir *datadir.Dir, c *noryxv1.PlayerChange, waits bool) error {
	a := c.GetAction()
	if !waits && a != noryxv1.PlayerAction_PLAYER_ACTION_BAN && a != noryxv1.PlayerAction_PLAYER_ACTION_PARDON {
		return nil
	}
	pending, err := notePending(dir, c, waits)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.waiting[id] = next(pending, time.Now())
	return nil
}

// Run runs the waiting changes of servers once they run and are due, until ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(interval)
	defer t.Stop()
	defer s.wg.Wait()
	for {
		s.applyWaiting(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// applyWaiting attempts the due changes of the servers that run, and looks for those of
// servers it doesn't know yet. Each server is attempted on its own, and not again while its
// last attempt runs, so that one that doesn't answer its console holds up only itself.
func (s *Service) applyWaiting(ctx context.Context) {
	servers, err := s.rt.List(ctx)
	if err != nil {
		return
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	known := s.waiting
	s.waiting = make(map[string]time.Time, len(servers))
	for _, srv := range servers {
		due, ok := known[srv.ID]
		if ok {
			s.waiting[srv.ID] = due
		}
		if !srv.Type.Proxy() && !s.applying[srv.ID] && (!ok || !due.IsZero() && !due.After(now) && srv.State == running) {
			s.applying[srv.ID] = true
			s.wg.Go(func() { s.apply(ctx, srv) })
		}
	}
}

// apply attempts the due changes of a server and notes when changes are due next. It notes it
// before it unlocks the server, so that no change added in between is forgotten.
func (s *Service) apply(ctx context.Context, srv runtime.Server) {
	due := time.Now() // to try again
	if unlock, err := s.locks.lock(ctx, srv.ID); err == nil {
		defer unlock()
		due = s.applyLocked(ctx, srv)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.waiting[srv.ID] = due
	delete(s.applying, srv.ID)
}

// applyLocked runs the due changes of a running server, each until changeTimeout, and returns
// when the next change is due, the zero time if none waits.
func (s *Service) applyLocked(ctx context.Context, srv runtime.Server) time.Time {
	now := time.Now()
	dir, err := s.rt.Data(ctx, srv.ID)
	if err != nil {
		return now
	}
	defer dir.Close()
	pending, err := readPending(dir)
	if err != nil {
		return now
	}
	if srv.State != running {
		return next(pending, now)
	}
	var left []*noryxv1.PlayerChange
	for i, change := range pending {
		if change.GetDueUnix() > now.Unix() {
			left = append(left, change)
			continue
		}
		changeCtx, cancel := context.WithTimeout(ctx, changeTimeout)
		var err error
		if bedrockWhitelist(change) {
			_, err = s.whitelistBedrock(changeCtx, srv.ID, dir, change)
		} else {
			_, err = s.rt.SendCommand(changeCtx, srv.ID, command(change))
		}
		cancel()
		if err != nil {
			slog.Warn("Can't change a player on a server that started", "server", srv.ID, "err", err)
			left = append(left, pending[i:]...)
			_ = writePending(dir, left) // if it fails, the done changes run again, which changes nothing
			return now
		}
		slog.Info("Changed a player on a server", "server", srv.ID, "action", change.GetAction().Slug(), "player", change.GetName())
	}
	if len(left) < len(pending) && writePending(dir, left) != nil {
		return now
	}
	return next(left, now)
}

// next returns when the first of the changes is due: now for those without a time, the zero
// time without changes.
func next(pending []*noryxv1.PlayerChange, now time.Time) time.Time {
	var first time.Time
	for _, c := range pending {
		due := now
		if c.GetDueUnix() > now.Unix() {
			due = time.Unix(c.GetDueUnix(), 0)
		}
		if first.IsZero() || due.Before(first) {
			first = due
		}
	}
	return first
}

// open finds a game server and opens its data directory.
func (s *Service) open(ctx context.Context, id string) (runtime.Server, *datadir.Dir, error) {
	if !runtime.ValidID(id) {
		return runtime.Server{}, nil, status.Error(codes.InvalidArgument, "invalid server ID")
	}
	srv, err := runtime.Find(ctx, s.rt, id)
	switch {
	case err != nil:
		return srv, nil, grpcerr.Status(err)
	case srv.Type.Proxy():
		return srv, nil, status.Error(codes.FailedPrecondition, "Players are managed on the game servers of a network.")
	}
	dir, err := s.rt.Data(ctx, id)
	if err != nil {
		return srv, nil, status.Error(codes.Internal, err.Error())
	}
	return srv, dir, nil
}

// command returns the console command of a valid change. Minecraft's own commands keep
// the lists in their files, also where plugins replace them.
func command(c *noryxv1.PlayerChange) string {
	return strings.TrimSpace("minecraft:" + commands[c.GetAction()] + " " + c.GetName() + " " + c.GetReason())
}
