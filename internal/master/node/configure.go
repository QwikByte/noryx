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
// offline when they changed.
func (s *Service) configured(ctx context.Context, n Node, info *noryxv1.GetInfoResponse) {
	if agentOf(info.GetSettings()) == n.Agent {
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
	if code := status.Code(err); code == codes.Unavailable || code == codes.DeadlineExceeded {
		return "The agent of the node gets its new settings once the master reaches it again."
	}
	return "The agent of the node didn't take its new settings: " + httpapi.Message(err)
}
