package node

import (
	"bytes"
	"cmp"
	"context"
	"log/slog"
	"os"
	"os/exec"

	"golang.org/x/mod/semver"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/buildinfo"
	"github.com/QwikByte/noryx/internal/logging"
)

// installer is the installer that the agent's package ships.
const installer = "/usr/lib/noryx-agent/install.sh"

// Update implements noryxv1.NodeServiceServer. Only releases newer than the agent are
// installed, so even a compromised master can't downgrade a node to a vulnerable version, and
// pre-releases only where the node's administrator allows them.
// The installer runs in a unit of its own, which outlives the restart of the agent.
func (s *Service) Update(ctx context.Context, req *noryxv1.UpdateRequest) (*noryxv1.UpdateResponse, error) {
	v := req.GetVersion()
	if !buildinfo.IsRelease(v) || semver.Compare(v, buildinfo.Version) <= 0 {
		return nil, status.Errorf(codes.InvalidArgument, "%q is no release newer than the agent's version %s", v, buildinfo.Version)
	}
	if semver.Prerelease(v) != "" && !s.Prereleases {
		return nil, status.Errorf(codes.InvalidArgument, "%s is a pre-release, which this node only installs with --prereleases in /etc/noryx/agent.env", v)
	}
	if _, err := os.Stat(installer); err != nil {
		return nil, status.Error(codes.FailedPrecondition, "The agent wasn't installed from a package. Update it on the node with install.sh.")
	}
	//nolint:gosec // v is a release version, passed as an argument without a shell
	out, err := exec.CommandContext(ctx, "systemd-run", "--unit=noryx-agent-update", "--collect", "--quiet",
		"--", installer, "update", "--only", "agent", "--version", v).CombinedOutput()
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "Can't start the update: %s", cmp.Or(string(bytes.TrimSpace(out)), err.Error()))
	}
	slog.Info("Update the node", logging.Nodes, "version", v)
	return &noryxv1.UpdateResponse{}, nil
}
