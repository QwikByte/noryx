package network

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/operation"
	"github.com/QwikByte/noryx/internal/master/tag"
)

// ErrOnNode is the code of the conflict of a node that runs proxies or game servers of
// networks, which the client may confirm to take them out of their networks first.
const ErrOnNode = "networks-on-node"

// CheckRemoveNode fails if a node can't be removed yet, as servers of other nodes would keep
// trusting its proxy, or proxies would keep sending players to its servers: while servers of
// networks run on it, unless release confirms that they leave their networks first, with a
// conflict that names the networks; and while a network that ends with it has datastores on
// other nodes, which would be lost.
func (s *Service) CheckRemoveNode(ctx context.Context, nodeID string, release bool) error {
	networks, err := s.onNode(ctx, nodeID)
	if err != nil {
		return err
	}
	return s.checkRemove(ctx, networks, nodeID, release)
}

// RemoveNode removes a node with remove once its networks let go of it, if release confirms
// it (see CheckRemoveNode). A network that ends with the node is deleted: its game servers on
// other nodes become standalone and a proxy on another node stops forwarding. The others forget
// the node's game servers, their proxies first. The node itself isn't contacted, as it may be
// lost, and stays if a server of another node can't be configured. RemoveNode returns the
// servers of other nodes that left a network.
func (s *Service) RemoveNode(ctx context.Context, nodeID string, release bool, remove func() error) ([]tag.Server, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	networks, err := s.onNode(ctx, nodeID)
	if err == nil {
		err = s.checkRemove(ctx, networks, nodeID, release)
	}
	var m members
	if err == nil {
		m, err = s.members(ctx)
	}
	if err != nil {
		return nil, err
	}
	ctx = context.WithoutCancel(ctx)
	var left []tag.Server
	var kept []Network // those whose datastores may still be published for the node
	for _, n := range networks {
		if err := s.releaseNode(ctx, n, nodeID, m); err != nil {
			return left, httpapi.Errorf(http.StatusBadGateway, "The node was not removed, as the network %q could not let go of it: %s", n.Name, httpapi.Message(err))
		}
		switch {
		case n.endsWith(nodeID):
			for _, ref := range append([]Ref{n.Proxy}, refs(n.Backends)...) {
				if ref.NodeID != nodeID {
					left = append(left, tag.Server(ref))
				}
			}
		case m[nodeID].Address != "":
			kept = append(kept, n.without(nodeID))
		}
	}
	operation.Step(ctx, "node")
	if err := remove(); err != nil {
		return left, err
	}
	// Datastores on other nodes are published in the private network for the nodes of the
	// network's servers, which the node no longer is.
	for _, n := range kept {
		if placed, err := s.datastores.Placed(ctx, n.ID); err != nil || len(placed) > 0 {
			if err == nil {
				err = s.apply(ctx, n, false)
			}
			if err != nil {
				slog.Warn("A network could not be applied again without a removed node", logging.Networks, "network", n.ID, "err", httpapi.Message(err))
			}
		}
	}
	return left, nil
}

// onNode returns the networks whose proxy or game servers run on a node.
func (s *Service) onNode(ctx context.Context, nodeID string) ([]Network, error) {
	networks, err := s.List(ctx)
	return slices.DeleteFunc(networks, func(n Network) bool {
		return n.Proxy.NodeID != nodeID && !slices.ContainsFunc(n.Backends, func(b Backend) bool { return b.NodeID == nodeID })
	}), err
}

// checkRemove fails if a node can't be removed yet, see CheckRemoveNode.
func (s *Service) checkRemove(ctx context.Context, networks []Network, nodeID string, release bool) error {
	names := make([]string, len(networks))
	for i, n := range networks {
		names[i] = strconv.Quote(n.Name)
		if !n.endsWith(nodeID) {
			continue
		}
		placed, err := s.datastores.Placed(ctx, n.ID)
		if err != nil {
			return err
		}
		if delete(placed, nodeID); len(placed) > 0 {
			return httpapi.Errorf(http.StatusConflict, "The network %q would be deleted with this node, but has datastores on other nodes, whose data would be lost. Delete them first.", n.Name)
		}
	}
	if len(networks) == 0 || release {
		return nil
	}
	return httpapi.Confirm(ErrOnNode, "Servers of the networks %s run on this node. Confirm to take them out first: a network left without its proxy or game servers is deleted, and its servers on other nodes become standalone; the others forget the servers on this node.",
		strings.Join(names, ", "))
}

// releaseNode lets a network go of a node, without contacting the node: one that ends with it
// is deleted once its servers on other nodes are standalone, and the others forget its game
// servers, the proxy first.
func (s *Service) releaseNode(ctx context.Context, n Network, nodeID string, m members) error {
	kept := n.without(nodeID)
	if !n.endsWith(nodeID) {
		var err error
		if kept.placed, err = s.datastores.Placed(ctx, n.ID); err != nil {
			return err
		}
		if err := s.configureServers(ctx, kept, m, false); err != nil {
			return err
		}
		return database.InTx(ctx, s.db, func(tx *sql.Tx) error { return save(ctx, tx, kept) })
	}
	operation.Step(ctx, "servers")
	for i, b := range kept.Backends {
		operation.Count(ctx, int64(i), int64(len(kept.Backends)), "servers")
		if err := s.leave(ctx, b); err != nil {
			return fmt.Errorf("%s could not be made standalone again: %w", b.Name, err)
		}
	}
	if n.Proxy.NodeID != nodeID {
		if err := s.release(ctx, n, "proxy"); err != nil {
			return err
		}
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM networks WHERE id = ?`, n.ID)
	return err
}

// endsWith reports whether a network can't go on without a node: its proxy runs there, or all
// its game servers do.
func (n *Network) endsWith(nodeID string) bool {
	return n.Proxy.NodeID == nodeID || !slices.ContainsFunc(n.Backends, func(b Backend) bool { return b.NodeID != nodeID })
}

// without returns the network without its game servers on a node, which players then neither
// join nor reach through host names. If none of the servers players join is left, they join
// the first one left.
func (n *Network) without(nodeID string) Network {
	kept := *n
	kept.Backends = slices.DeleteFunc(slices.Clone(n.Backends), func(b Backend) bool { return b.NodeID == nodeID })
	gone := func(name string) bool {
		return !slices.ContainsFunc(kept.Backends, func(b Backend) bool { return b.Name == name })
	}
	kept.Try = slices.DeleteFunc(slices.Clone(n.Try), gone)
	if len(kept.Try) == 0 && len(kept.Backends) > 0 {
		kept.Try = []string{kept.Backends[0].Name}
	}
	kept.ForcedHosts = []ForcedHost{}
	for _, h := range n.ForcedHosts {
		if h.Servers = slices.DeleteFunc(slices.Clone(h.Servers), gone); len(h.Servers) > 0 {
			kept.ForcedHosts = append(kept.ForcedHosts, h)
		}
	}
	return kept
}
