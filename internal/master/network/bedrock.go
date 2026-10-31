package network

import (
	"context"
	"net/http"
	"slices"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/geysermc"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/operation"
	"github.com/QwikByte/noryx/internal/master/plugin"
)

// bedrockPlugins let Bedrock players join a network through its proxy: Geyser, from
// Modrinth, translates their game, and Floodgate, from GeyserMC, lets them join without a
// Java account. Both only run on the proxy, which keeps Floodgate's key to itself.
var bedrockPlugins = []string{"wKkoqHrH", geysermc.Floodgate}

const minBedrockPort, maxBedrockPort = 1024, 65535

// checkBedrock checks that the Bedrock port of a network is free on a node for its proxy:
// in the node's port range and used by no server, also not as its own port, unless as the
// Bedrock port of one of the owners, such as the proxy itself.
func (s *Service) checkBedrock(ctx context.Context, nodeID string, port uint32, owners ...string) error {
	if port == 0 {
		return nil
	}
	n, err := s.nodes.Get(ctx, nodeID)
	if err != nil {
		return err
	}
	switch {
	case port < minBedrockPort || port > maxBedrockPort:
		return httpapi.Errorf(http.StatusBadRequest, "Choose a Bedrock port from %d to %d.", minBedrockPort, maxBedrockPort)
	case n.PortMin != nil && (port < *n.PortMin || port > *n.PortMax):
		return httpapi.Errorf(http.StatusBadRequest, "Choose a Bedrock port from %d to %d, the port range of %s.", *n.PortMin, *n.PortMax, n.Name)
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	conn, err := s.nodes.Conn(ctx, nodeID)
	if err != nil {
		return err
	}
	res, err := noryxv1.NewServerServiceClient(conn).ListServers(ctx, &noryxv1.ListServersRequest{})
	if err != nil {
		return err
	}
	if i := slices.IndexFunc(res.GetServers(), func(srv *noryxv1.Server) bool {
		return srv.Uses(port) && (srv.GetBedrockPort() != port || !slices.Contains(owners, srv.GetId()))
	}); i >= 0 {
		return httpapi.Errorf(http.StatusConflict, "Port %d is already used by %q on %s. Choose another Bedrock port.", port, res.GetServers()[i].GetName(), n.Name)
	}
	return nil
}

// provideBedrock installs the newest Geyser and Floodgate on the proxy of a network and
// reports whether that changed a file, which the proxy loads when it starts.
func (s *Service) provideBedrock(ctx context.Context, n Network) (bool, error) {
	operation.Step(ctx, "bedrock")
	changed, err := s.mods.Provide(ctx, plugin.Ref(n.Proxy), bedrockPlugins)
	if err != nil {
		return false, httpapi.Errorf(http.StatusBadGateway, "Geyser and Floodgate could not be installed: %s", httpapi.Message(err))
	}
	return changed, nil
}

// removeBedrock removes Geyser and Floodgate from the proxy of a network, unless it is gone.
// Their settings and Floodgate's key stay, in case Bedrock players are let in again.
func (s *Service) removeBedrock(ctx context.Context, n Network) error {
	operation.Step(ctx, "bedrock-remove")
	if _, err := s.server(ctx, n.Proxy); gone(err) {
		return nil
	}
	for _, project := range bedrockPlugins {
		if err := ignoreMissing(s.mods.Uninstall(ctx, plugin.Ref(n.Proxy), project)); err != nil {
			return httpapi.Errorf(http.StatusBadGateway, "Geyser and Floodgate could not be removed from the proxy: %s", httpapi.Message(err))
		}
	}
	return nil
}
