package node

import (
	"context"
	"log/slog"
	"maps"
	"sync"
	"time"

	"github.com/QwikByte/noryx/internal/logging"
)

const (
	presenceTimeout = 10 * time.Second
	// offlineAfter is how many checks in a row a node fails before it counts as offline, so
	// that a hiccup, e.g. while its agent restarts after an update, isn't logged.
	offlineAfter = 2
)

// WatchPresence checks the connections to the agents of the enrolled nodes at the given
// interval until ctx is done, and logs when a node goes offline and when it is back. Agents
// that report other settings than their node's get them again.
func (s *Service) WatchPresence(ctx context.Context, interval time.Duration) {
	failed := map[string]int{} // checks failed in a row by node ID
	for {
		s.checkPresence(ctx, failed)
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

func (s *Service) checkPresence(ctx context.Context, failed map[string]int) {
	nodes, err := s.List(ctx)
	if err != nil {
		slog.Debug("Can't list the nodes to check their connections", logging.Nodes, "err", err)
		return
	}
	online := make([]bool, len(nodes))
	var wg sync.WaitGroup
	for i, n := range nodes {
		if n.EnrolledAt != nil {
			wg.Go(func() {
				ctx, cancel := context.WithTimeout(ctx, presenceTimeout)
				defer cancel()
				info, _, err := s.Status(ctx, n.ID)
				online[i] = err == nil
				if err == nil {
					s.configured(ctx, n, info)
				}
			})
		}
	}
	wg.Wait()
	if ctx.Err() != nil {
		return // the master stops
	}
	enrolled := map[string]bool{}
	for i, n := range nodes {
		if n.EnrolledAt != nil {
			enrolled[n.ID] = true
			notePresence(failed, n, online[i])
		}
	}
	maps.DeleteFunc(failed, func(id string, _ int) bool { return !enrolled[id] })
}

// notePresence counts a check of a node and logs when it makes the node go offline or come
// back. Nothing is logged while its state stays the same, nor for a node that is online
// when the master starts.
func notePresence(failed map[string]int, n Node, online bool) {
	attrs := []any{logging.Nodes, logging.KeyNode, n.ID, logging.KeyNodeName, n.Name}
	switch was := failed[n.ID]; {
	case online:
		failed[n.ID] = 0
		if was >= offlineAfter {
			slog.Info("A node is online again", attrs...)
		}
	default:
		failed[n.ID] = was + 1
		if was+1 == offlineAfter {
			slog.Warn("A node went offline: the master can't reach its agent", attrs...)
		}
	}
}
