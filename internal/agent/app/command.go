// Package app wires the agent together and provides its command line interface.
// Besides the master, only the local CLI can control the agent, through a Unix
// socket inside the data directory that is accessible to the agent's user only.
package app

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/QwikByte/noryx/internal/agent/enroll"
	"github.com/QwikByte/noryx/internal/agentcli"
	"github.com/QwikByte/noryx/internal/buildinfo"
	"github.com/QwikByte/noryx/internal/logging"
)

type config struct {
	dataDir     string
	socketPath  string
	listenAddr  string
	prereleases bool
	runtime     runtimeFlags
	log         logging.Options
}

func (c config) pkiDir() string { return filepath.Join(c.dataDir, "pki") }
func (c config) socket() string { return cmp.Or(c.socketPath, filepath.Join(c.dataDir, "agent.sock")) }

// Command returns the root command of noryx-agent.
func Command() *cobra.Command {
	var cfg config
	root := &cobra.Command{
		Use:          "noryx-agent",
		Short:        "Node agent of Noryx: runs the Minecraft servers of this machine",
		Version:      buildinfo.Version,
		SilenceUsage: true,
		PersistentPreRunE: func(*cobra.Command, []string) (err error) {
			if cfg.dataDir, err = filepath.Abs(cfg.dataDir); err != nil || cfg.socketPath == "" {
				return err
			}
			cfg.socketPath, err = filepath.Abs(cfg.socketPath)
			return err
		},
	}
	root.PersistentFlags().StringVar(&cfg.dataDir, "data-dir", "/var/lib/noryx-agent", "directory for credentials and server data")
	root.PersistentFlags().StringVar(&cfg.socketPath, "socket", "", "Unix socket for the local CLI, at most 107 bytes long (default <data-dir>/agent.sock)")

	serve := &cobra.Command{
		Use:   "serve",
		Short: "Run the agent",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return serve(cmd.Context(), cfg) },
	}
	serve.Flags().StringVar(&cfg.listenAddr, "listen", ":7443", "listen address for connections from the master")
	serve.Flags().BoolVar(&cfg.prereleases, "prereleases", false, "let the master update this node to pre-releases too, e.g. a test node")
	cfg.runtime.add(serve.Flags())
	cfg.log.AddFlags(serve.Flags())

	enrollCmd := &cobra.Command{
		Use:   "enroll <join-token>",
		Short: "Connect this agent to a master using a join token from the admin panel",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := ensureDataDir(cfg); err != nil {
				return err
			}
			if err := enroll.Run(cmd.Context(), args[0], cfg.pkiDir()); err != nil {
				return err
			}
			fmt.Println("Enrolled. Start the agent, or restart it if it runs: systemctl restart noryx-agent (or noryx-agent serve)")
			return nil
		},
	}

	root.AddCommand(serve, enrollCmd, storageCommand(&cfg), isolateCommand())
	for _, c := range agentcli.Commands(cfg.local) {
		if c.Name() == "overlay" {
			c.AddCommand(overlayCommands(&cfg)...)
		}
		root.AddCommand(c)
	}
	return root
}

func ensureDataDir(cfg config) error {
	if err := os.MkdirAll(cfg.dataDir, 0o700); err != nil {
		return err
	}
	return os.Chmod(cfg.dataDir, 0o700) //nolint:gosec // directories need the execute bit
}
