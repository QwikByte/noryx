package server

import (
	"context"
	"net/http"
	"slices"
	"strings"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/httpapi"
)

// ExtraPort is a port of a plugin of a server, e.g. of a web map, which its node publishes on all
// its addresses at the same port of the container; see noryxv1.ExtraPort.
type ExtraPort struct {
	Port     uint32 `json:"port"`
	Protocol string `json:"protocol"` // tcp or udp
	Label    string `json:"label"`
}

// ProtoPorts returns extra ports as the agents take them.
func ProtoPorts(ports []ExtraPort) []*noryxv1.ExtraPort {
	var extra []*noryxv1.ExtraPort
	for _, p := range ports {
		extra = append(extra, &noryxv1.ExtraPort{Port: p.Port, Protocol: p.Protocol, Label: strings.TrimSpace(p.Label)})
	}
	return extra
}

// extraPortsOf returns the extra ports of a server for the panel.
func extraPortsOf(s *noryxv1.Server) []ExtraPort {
	ports := []ExtraPort{}
	for _, p := range s.GetExtraPorts() {
		ports = append(ports, ExtraPort{Port: p.GetPort(), Protocol: p.GetProtocol(), Label: p.GetLabel()})
	}
	return ports
}

// checkExtraPorts checks the extra ports of a server with port and bedrockPort on a node by the
// rules the agent checks too, and that they lie in the node's port range. Unless servers is nil,
// none of them may use the ports, e.g. on the node a server moves to, where its agent would only
// refuse them once the server stopped.
func (h *Handler) checkExtraPorts(ctx context.Context, nodeID string, servers []*noryxv1.Server, ports []*noryxv1.ExtraPort, port, bedrockPort uint32) error {
	if msg := noryxv1.CheckExtraPorts(ports, port, bedrockPort); msg != "" {
		return httpapi.Errorf(http.StatusBadRequest, "%s", msg)
	}
	if len(ports) == 0 {
		return nil
	}
	n, err := h.nodes.Get(ctx, nodeID)
	if err != nil {
		return err
	}
	for _, p := range ports {
		if n.PortMin != nil && (p.GetPort() < *n.PortMin || p.GetPort() > *n.PortMax) {
			return httpapi.Errorf(http.StatusBadRequest, "Choose extra ports from %d to %d, the port range of %s.", *n.PortMin, *n.PortMax, n.Name)
		}
		if i := slices.IndexFunc(servers, func(s *noryxv1.Server) bool { return s.Uses(p.GetPort()) }); i >= 0 {
			return httpapi.Errorf(http.StatusConflict, "Port %d of the server is used by %q on %s. Change its extra ports first.", p.GetPort(), servers[i].GetName(), n.Name)
		}
	}
	return nil
}

// extraPortsWarning returns a warning if a node's agent answered without the extra ports it was
// asked to publish, as agents of earlier versions don't know them.
func extraPortsWarning(want int, got *noryxv1.Server) string {
	if len(got.GetExtraPorts()) == want {
		return ""
	}
	return "The agent of the node is too old for extra ports, so the server publishes none. Update the agent first, then add them in the server's settings."
}

// usedBy returns the first of servers that uses a port from first to last, and that port; nil
// if none does.
func usedBy(servers []*noryxv1.Server, first, last uint32) (*noryxv1.Server, uint32) {
	for port := first; port <= last; port++ {
		if i := slices.IndexFunc(servers, func(s *noryxv1.Server) bool { return s.Uses(port) }); i >= 0 {
			return servers[i], port
		}
	}
	return nil, 0
}

// checkType fails unless the agent of a node creates servers of the type, which agents of
// older versions don't for Bedrock servers.
func (h *Handler) checkType(ctx context.Context, nodeID string, typ noryxv1.ServerType) error {
	conn, err := h.nodes.Conn(ctx, nodeID)
	if err != nil {
		return err
	}
	info, err := noryxv1.NewNodeServiceClient(conn).GetInfo(ctx, &noryxv1.GetInfoRequest{})
	if err != nil || info.Creates(typ) || typ == noryxv1.ServerType_SERVER_TYPE_UNSPECIFIED {
		return err
	}
	return httpapi.Errorf(http.StatusConflict, "The agent of the node doesn't know %s servers yet. Update it first.", typeName(typ))
}

// typeName names a type of server in a message, e.g. Bedrock.
func typeName(typ noryxv1.ServerType) string {
	slug := typ.Slug()
	return strings.ToUpper(slug[:1]) + slug[1:]
}
