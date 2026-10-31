package app

import (
	"fmt"
	"net/netip"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/runtime/docker"
)

// runtimeFlags choose the runtime of the servers: Docker, or Podman as root. Each node has
// one, which runs all its servers and datastores. The public address is where players reach
// the node, which Bedrock servers tell them.
type runtimeFlags struct {
	name          string
	socket        string
	publicAddress string
}

func (r *runtimeFlags) add(flags *pflag.FlagSet) {
	flags.StringVar(&r.name, "runtime", noryxv1.RuntimeDocker, "runtime of the servers: docker, or podman as root")
	flags.StringVar(&r.socket, "runtime-socket", "", "Unix socket of the runtime (default: Docker's, or "+docker.PodmanSocket+" for Podman)")
	flags.StringVar(&r.publicAddress, "public-address", "", "IPv4 address at which players reach this node, which Bedrock servers tell them, e.g. the public one behind NAT (default: the node's own on its route to the internet)")
}

// options returns the options of the runtime, or why they are wrong.
func (r runtimeFlags) options() (docker.Options, error) {
	opts := docker.Options{Podman: r.name == noryxv1.RuntimePodman, Socket: r.socket}
	if r.name != noryxv1.RuntimeDocker && r.name != noryxv1.RuntimePodman {
		return docker.Options{}, fmt.Errorf("--runtime must be %s or %s, not %q", noryxv1.RuntimeDocker, noryxv1.RuntimePodman, r.name)
	}
	if r.publicAddress != "" {
		addr, err := netip.ParseAddr(r.publicAddress)
		if err != nil || !addr.Is4() || !addr.IsGlobalUnicast() {
			return docker.Options{}, fmt.Errorf("--public-address must be an IPv4 address of a host, not %q", r.publicAddress)
		}
		opts.PublicAddress = addr
	}
	return opts, nil
}

// isolateCommand keeps the servers of Podman apart at boot, before Podman starts them.
func isolateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "isolate",
		Short: "Keep the servers of Podman from reaching each other, e.g. at boot before Podman starts them",
		Args:  cobra.NoArgs,
		RunE:  func(*cobra.Command, []string) error { return isolate() },
	}
}

func isolate() error {
	if err := docker.Isolate(); err != nil {
		return fmt.Errorf("can't keep the servers of Podman apart, which needs nftables with its bridge support (nft_meta_bridge) and its lookups of routes (nft_fib_inet) in the kernel: %w", err)
	}
	return nil
}
