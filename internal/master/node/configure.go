package node

import (
	"context"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/httpapi"
)

// Configure gives the agent of a node the settings that it applies itself.
func (s *Service) Configure(ctx context.Context, n Node) error {
	conn, err := s.Conn(ctx, n.ID)
	if err == nil {
		_, err = noryxv1.NewNodeServiceClient(conn).Configure(ctx, &noryxv1.ConfigureRequest{Settings: n.proto()})
	}
	return err
}

// configured gives the agent of a node its settings again if it reports others, e.g. as it was
// offline when they changed, or was updated from a version that didn't know them. Agents of
// older versions report none.
func (s *Service) configured(ctx context.Context, n Node, info *noryxv1.GetInfoResponse) {
	if info.GetSettings() == nil || agentOf(info.GetSettings()) == n.Agent {
		return
	}
	attrs := []any{logging.Nodes, logging.KeyNode, n.ID, logging.KeyNodeName, n.Name}
	if err := s.Configure(ctx, n); err != nil {
		slog.Warn("Can't give the agent of a node its settings", append(attrs, "err", err)...)
		return
	}
	slog.Info("Give the agent of a node its settings", attrs...)
}

// configureWarning tells why the agent of a node didn't get its new settings.
func configureWarning(err error) string {
	switch status.Code(err) {
	case codes.Unimplemented:
		return "The agent of the node is too old for the output of containers, the space kept free and the interval of measurements. Update the agent, which then gets them."
	case codes.Unavailable, codes.DeadlineExceeded:
		return "The agent of the node gets its new settings once the master reaches it again."
	}
	return "The agent of the node didn't take its new settings: " + httpapi.Message(err)
}
