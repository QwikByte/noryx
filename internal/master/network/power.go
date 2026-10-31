package network

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/operation"
)

// Actions on all servers of a network.
const (
	Start   = "start"
	Stop    = "stop"
	Restart = "restart"
)

type serverCall func(ctx context.Context, c noryxv1.ServerServiceClient, id string) error

var (
	start = func(ctx context.Context, c noryxv1.ServerServiceClient, id string) error {
		_, err := c.StartServer(ctx, &noryxv1.StartServerRequest{Id: id})
		return err
	}
	stop = func(ctx context.Context, c noryxv1.ServerServiceClient, id string) error {
		_, err := c.StopServer(ctx, &noryxv1.StopServerRequest{Id: id})
		return err
	}
	restart = func(ctx context.Context, c noryxv1.ServerServiceClient, id string) error {
		_, err := c.RestartServer(ctx, &noryxv1.RestartServerRequest{Id: id})
		return err
	}
)

// Power starts, stops or restarts all servers of a network in the order that suits its
// players: the game servers start before the proxy, so that players find them, and the
// proxy stops first, so that players leave the network at once instead of server by server.
func (s *Service) Power(ctx context.Context, n Network, action string) error {
	ctx = context.WithoutCancel(ctx)
	proxy := []Backend{{Ref: n.Proxy, Name: "the proxy"}}
	// The steps of the action, as they are named in its operation.
	steps := map[string][]struct {
		name    string
		servers []Backend
		call    serverCall
	}{
		Start:   {{"servers", n.Backends, start}, {"proxy-start", proxy, start}},
		Stop:    {{"proxy-stop", proxy, stop}, {"servers", n.Backends, stop}},
		Restart: {{"proxy-stop", proxy, stop}, {"servers", n.Backends, restart}, {"proxy-start", proxy, start}},
	}[action]
	if steps == nil {
		return httpapi.Errorf(http.StatusBadRequest, "Choose start, stop or restart.")
	}
	var errs []error
	for _, step := range steps {
		operation.Step(ctx, step.name)
		errs = append(errs, s.each(ctx, step.servers, step.call))
	}
	return errors.Join(errs...)
}

// Broadcast shows a message to the players of all running game servers of a network.
func (s *Service) Broadcast(ctx context.Context, n Network, m Message) error {
	commands, err := m.Commands(Everyone)
	if err != nil {
		return err
	}
	return s.each(ctx, n.Backends, func(ctx context.Context, c noryxv1.ServerServiceClient, id string) error {
		for _, command := range commands {
			_, err := c.SendCommand(ctx, &noryxv1.SendCommandRequest{Id: id, Command: command})
			if status.Code(err) == codes.FailedPrecondition {
				return nil // a stopped server has no players
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// each calls the agents of the given servers at the same time.
func (s *Service) each(ctx context.Context, servers []Backend, call serverCall) error {
	errs := make([]error, len(servers))
	var wg sync.WaitGroup
	var finished atomic.Int64
	total := int64(len(servers))
	operation.Count(ctx, 0, total, "servers")
	for i, b := range servers {
		wg.Go(func() {
			defer func() { operation.Count(ctx, finished.Add(1), total, "servers") }()
			ctx, cancel := context.WithTimeout(ctx, configureTimeout)
			defer cancel()
			conn, err := s.nodes.Conn(ctx, b.NodeID)
			if err == nil {
				err = call(ctx, noryxv1.NewServerServiceClient(conn), b.ServerID)
			}
			if err != nil {
				errs[i] = fmt.Errorf("%s: %s", b.Name, httpapi.Message(err))
			}
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return httpapi.Errorf(http.StatusBadGateway, "%s", strings.ReplaceAll(err.Error(), "\n", " · "))
	}
	return nil
}
