package network

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"path"
	"slices"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/files"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/operation"
	"github.com/QwikByte/noryx/internal/master/plugin"
)

// Swap gives a network another proxy.
type Swap struct {
	Proxy      Ref    `json:"proxy"`
	Forwarding string `json:"forwarding"` // empty is modern for Velocity, legacy otherwise
	Firewalled bool   `json:"firewalled"`
}

// maintenanceFiles are the files of the Maintenance plugin that keep its state.
var maintenanceFiles = []string{"config.yml", "WhitelistedPlayers.yml"}

// SwapProxy replaces the proxy of a network with a free one, e.g. a Waterfall proxy, which
// reached its end of life, with BungeeCord or Velocity. The new proxy takes over the settings
// of the old one if it reads the same file, the Maintenance plugin with its state, and the
// Bedrock port. The old proxy leaves the network and stops; the new one starts if either ran.
func (s *Service) SwapProxy(ctx context.Context, id string, sw Swap) (Network, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.Get(ctx, id)
	if err != nil {
		return current, err
	}
	n, proxy, err := s.swapped(ctx, current, sw)
	if err != nil {
		return current, err
	}
	ctx = context.WithoutCancel(ctx)
	old, err := s.server(ctx, current.Proxy)
	if err != nil {
		return current, err
	}
	stopped := noryxv1.ServerState_SERVER_STATE_STOPPED
	running := old.GetState() != stopped || proxy.GetState() != stopped
	if err := s.takeOver(ctx, current, n, old, proxy); err != nil {
		return current, httpapi.Errorf(http.StatusBadGateway, "The proxy was not changed, as %s could not take over: %s", proxy.GetName(), httpapi.Message(err))
	}
	// The old proxy must not keep sending players to the servers.
	err = s.release(ctx, current, "old-proxy")
	if err == nil && old.GetState() != stopped {
		err = s.each(ctx, []Backend{{Ref: current.Proxy, Name: old.GetName()}}, stop)
	}
	if err != nil {
		return current, httpapi.Errorf(http.StatusBadGateway, "The proxy was not changed: %s Apply the network again to bring the old one back.",
			strings.TrimSuffix(httpapi.Message(err), ".")+".")
	}
	err = database.InTx(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE networks SET proxy_node_id = ?, proxy_server_id = ?, proxy_type = ? WHERE id = ?`,
			n.Proxy.NodeID, n.Proxy.ServerID, n.ProxyType, n.ID); err != nil {
			return err
		}
		return save(ctx, tx, n)
	})
	if err != nil {
		return current, conflict(err, n.Name)
	}
	if err := s.apply(ctx, n, true); err != nil || !running {
		return n, err
	}
	operation.Step(ctx, "proxy-start")
	return n, s.each(ctx, []Backend{{Ref: n.Proxy, Name: proxy.GetName()}}, start)
}

// swapped returns a network with the proxy of sw instead of its own, and that proxy, which
// must be free and run BungeeCord or Velocity.
func (s *Service) swapped(ctx context.Context, current Network, sw Swap) (Network, *noryxv1.Server, error) {
	proxy, err := s.server(ctx, sw.Proxy)
	switch typ := proxy.GetType(); {
	case err != nil:
		return current, nil, err
	case !typ.Proxy():
		return current, nil, errProxyType
	case typ == noryxv1.ServerType_SERVER_TYPE_WATERFALL:
		return current, nil, httpapi.Errorf(http.StatusBadRequest, "Waterfall reached its end of life. Choose a Velocity or BungeeCord proxy.")
	case sw.Proxy == current.Proxy:
		return current, nil, httpapi.Errorf(http.StatusBadRequest, "This proxy serves the network already.")
	}
	if other, err := s.find(ctx, sw.Proxy.ServerID); err != nil || other != nil {
		if err == nil {
			err = httpapi.Errorf(http.StatusConflict, "This proxy is part of the network %q.", other.Name)
		}
		return current, nil, err
	}
	n := current
	n.Proxy, n.ProxyType = sw.Proxy, proxy.GetType().Slug()
	n.Forwarding, n.Firewalled = forwardingOr(sw.Forwarding, proxy.GetType()), sw.Firewalled
	// validate puts them into their canonical form, which current keeps.
	n.Backends, n.ForcedHosts = slices.Clone(current.Backends), slices.Clone(current.ForcedHosts)
	m, err := s.members(ctx)
	if err == nil {
		err = n.validate(m)
	}
	if err != nil {
		return current, nil, err
	}
	if n.Forwarding != current.Forwarding {
		for _, b := range n.Backends {
			if _, err := s.backend(ctx, b.Ref, n.Forwarding); err != nil {
				return current, nil, fmt.Errorf("%s: %w", b.Name, err)
			}
		}
	}
	return n, proxy, s.checkBedrock(ctx, n.Proxy.NodeID, n.BedrockPort, current.Proxy.ServerID)
}

// takeOver prepares the proxy of n to replace that of current. It stops, so that it loads
// all it gets when it starts: the configuration of the old proxy if it reads the same file,
// as BungeeCord reads that of Waterfall, and the Maintenance plugin with its files.
func (s *Service) takeOver(ctx context.Context, current, n Network, old, proxy *noryxv1.Server) error {
	operation.Step(ctx, "settings")
	if proxy.GetState() != noryxv1.ServerState_SERVER_STATE_STOPPED {
		if err := s.each(ctx, []Backend{{Ref: n.Proxy, Name: proxy.GetName()}}, stop); err != nil {
			return err
		}
	}
	m, err := s.Maintenance(ctx, current)
	if err != nil {
		return err
	}
	from, err := s.files(ctx, current.Proxy.NodeID)
	if err != nil {
		return err
	}
	into, err := s.files(ctx, n.Proxy.NodeID)
	if err != nil {
		return err
	}
	var copies [][2]string // from, to
	if old.GetType().Bungee() == proxy.GetType().Bungee() {
		copies = append(copies, [2]string{old.GetType().ConfigFile(), proxy.GetType().ConfigFile()})
	}
	if m.Installed {
		folder := proxy.GetType().MaintenanceFolder()
		_, err := into.CreateDirectory(ctx, &noryxv1.CreateDirectoryRequest{ServerId: n.Proxy.ServerID, Path: folder})
		if status.Code(err) != codes.AlreadyExists && err != nil {
			return err
		}
		for _, f := range maintenanceFiles {
			copies = append(copies, [2]string{path.Join(old.GetType().MaintenanceFolder(), f), path.Join(folder, f)})
		}
	}
	for _, c := range copies {
		err := files.Copy(ctx, from, &noryxv1.ReadFileRequest{ServerId: current.Proxy.ServerID, Path: c[0]},
			into, &noryxv1.WriteFileHeader{ServerId: n.Proxy.ServerID, Path: c[1]})
		if ignoreMissing(err) != nil { // e.g. a configuration the old proxy never created
			return fmt.Errorf("%s could not be copied: %s", c[0], httpapi.Message(err))
		}
	}
	if !m.Installed {
		return nil
	}
	operation.Step(ctx, "plugin")
	return s.mods.Ensure(ctx, plugin.Ref(n.Proxy), maintenanceProject)
}

// files returns the file manager of a node.
func (s *Service) files(ctx context.Context, nodeID string) (noryxv1.FileServiceClient, error) {
	conn, err := s.nodes.Conn(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	return noryxv1.NewFileServiceClient(conn), nil
}
