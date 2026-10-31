// Package agentcli contains the commands of noryx-agent that control a running agent
// through its gRPC API. The agent's local CLI runs them over its Unix socket, the
// terminal of the admin panel over the master's mutually authenticated connection.
// Commands that change what the master may do, such as adding storage locations,
// are not part of it: they only exist in the local CLI.
package agentcli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
)

// timeout limits commands that answer right away, and stopTimeout those that stop a server,
// which gets its stop timeout to save its worlds. Following logs, backing up and restoring
// take as long as they need.
const (
	timeout     = 2 * time.Minute
	stopTimeout = timeout + noryxv1.MaxStopTimeout
)

// Agent calls fn with a connection to the running agent.
type Agent func(ctx context.Context, fn func(grpc.ClientConnInterface) error) error

// Commands returns the commands that control the agent. They print to the command's output.
func Commands(agent Agent) []*cobra.Command {
	c := cli{agent}
	return []*cobra.Command{c.status(), c.server(), c.backup(), c.datastore(), c.logs(), c.overlay()}
}

type cli struct{ agent Agent }

// errNoRuntime tells the node's administrator what to do while the runtime is down.
func errNoRuntime(name string) error {
	unit := "docker"
	if name == noryxv1.RuntimePodman {
		unit = "podman.socket" // the service starts on demand
	}
	return fmt.Errorf("can't reach %s on this node, see: systemctl status %s", noryxv1.RuntimeTitle(name), unit)
}

// call runs fn with the time limit of commands that answer right away.
func (c cli) call(cmd *cobra.Command, fn func(context.Context, grpc.ClientConnInterface) error) error {
	return c.within(cmd, timeout, fn)
}

// within runs fn with a time limit.
func (c cli) within(cmd *cobra.Command, limit time.Duration, fn func(context.Context, grpc.ClientConnInterface) error) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), limit)
	defer cancel()
	return message(c.agent(ctx, func(conn grpc.ClientConnInterface) error { return fn(ctx, conn) }))
}

// long runs fn without a time limit; it ends when the command's context is done.
func (c cli) long(cmd *cobra.Command, fn func(context.Context, grpc.ClientConnInterface) error) error {
	return message(c.agent(cmd.Context(), func(conn grpc.ClientConnInterface) error { return fn(cmd.Context(), conn) }))
}

// message returns errors of the agent without the gRPC prefix.
func message(err error) error {
	if st, ok := status.FromError(err); ok && err != nil {
		return errors.New(st.Message())
	}
	return err
}

func (c cli) status() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show this node and its servers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return c.call(cmd, func(ctx context.Context, conn grpc.ClientConnInterface) error {
				info, err := noryxv1.NewNodeServiceClient(conn).GetInfo(ctx, &noryxv1.GetInfoRequest{})
				if err != nil {
					return err
				}
				out := cmd.OutOrStdout()
				name, version := info.GetRuntimeName(), info.GetRuntimeVersion()
				running := version != ""
				fmt.Fprintf(out, "Node     %s\nAgent    %s\nRuntime  %s %s\n", info.GetHostname(), info.GetAgentVersion(), noryxv1.RuntimeTitle(name), cmp.Or(version, "unavailable"))
				if running {
					fmt.Fprintf(out, "System   %s, %d CPUs, %.1f GiB\n", info.GetOs(), info.GetCpuCount(), float64(info.GetMemoryBytes())/(1<<30))
				}
				if notAfter := info.GetCertificateNotAfterUnix(); notAfter != 0 {
					fmt.Fprintf(out, "Cert     valid until %s, renewed by the master\n", time.Unix(notAfter, 0).Local().Format(time.DateOnly))
				}
				fmt.Fprintln(out)
				if !running {
					return errNoRuntime(name)
				}
				return printServers(ctx, conn, out)
			})
		},
	}
}

func (c cli) server() *cobra.Command {
	// lifecycle runs a request that only needs the server's ID, within limit.
	lifecycle := func(use, short string, limit time.Duration, run func(context.Context, noryxv1.ServerServiceClient, string) error) *cobra.Command {
		return &cobra.Command{
			Use:   use + " <id>",
			Short: short,
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return c.within(cmd, limit, func(ctx context.Context, conn grpc.ClientConnInterface) error {
					return run(ctx, noryxv1.NewServerServiceClient(conn), args[0])
				})
			},
		}
	}
	cmd := &cobra.Command{Use: "server", Short: "Manage the servers of this node"}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List all servers",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return c.call(cmd, func(ctx context.Context, conn grpc.ClientConnInterface) error {
					return printServers(ctx, conn, cmd.OutOrStdout())
				})
			},
		},
		lifecycle("start", "Start a server", timeout, func(ctx context.Context, s noryxv1.ServerServiceClient, id string) error {
			_, err := s.StartServer(ctx, &noryxv1.StartServerRequest{Id: id})
			return err
		}),
		lifecycle("stop", "Stop a server gracefully", stopTimeout, func(ctx context.Context, s noryxv1.ServerServiceClient, id string) error {
			_, err := s.StopServer(ctx, &noryxv1.StopServerRequest{Id: id})
			return err
		}),
		lifecycle("restart", "Stop a server gracefully and start it again", stopTimeout, func(ctx context.Context, s noryxv1.ServerServiceClient, id string) error {
			_, err := s.RestartServer(ctx, &noryxv1.RestartServerRequest{Id: id})
			return err
		}),
		&cobra.Command{
			Use:   "logs <id>",
			Short: "Follow the console of a server until Ctrl+C",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return c.long(cmd, func(ctx context.Context, conn grpc.ClientConnInterface) error {
					return followLogs(ctx, conn, args[0], cmd.OutOrStdout())
				})
			},
		},
		&cobra.Command{
			Use:   "command <id> <command>...",
			Short: "Run a console command, e.g.: server command <id> say Hello",
			Args:  cobra.MinimumNArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				return c.call(cmd, func(ctx context.Context, conn grpc.ClientConnInterface) error {
					req := &noryxv1.SendCommandRequest{Id: args[0], Command: strings.Join(args[1:], " ")}
					res, err := noryxv1.NewServerServiceClient(conn).SendCommand(ctx, req)
					if err == nil {
						fmt.Fprintln(cmd.OutOrStdout(), strings.TrimRight(res.GetOutput(), "\n"))
					}
					return err
				})
			},
		},
	)
	return cmd
}

// backup backs up and restores servers, locally e.g. while the master is unreachable.
func (c cli) backup() *cobra.Command {
	cmd := &cobra.Command{Use: "backup", Short: "Back up and restore the servers of this node"}
	var label string
	create := &cobra.Command{
		Use:   "create <server-id>",
		Short: "Back up all data of a server; a running server keeps running",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.long(cmd, func(ctx context.Context, conn grpc.ClientConnInterface) error {
				req := &noryxv1.CreateBackupRequest{ServerId: args[0], Label: label, Selection: &noryxv1.BackupSelection{Everything: true}}
				res, err := noryxv1.NewBackupServiceClient(conn).CreateBackup(ctx, req)
				if err == nil {
					fmt.Fprintf(cmd.OutOrStdout(), "Created backup %s (%.1f MiB).\n", res.GetBackup().GetId(), float64(res.GetBackup().GetSize())/(1<<20))
				}
				return err
			})
		},
	}
	create.Flags().StringVar(&label, "label", "", "describes the backup, e.g. before an update")
	cmd.AddCommand(
		&cobra.Command{
			Use:   "list <server-id>",
			Short: "List the backups of a server",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return c.call(cmd, func(ctx context.Context, conn grpc.ClientConnInterface) error {
					res, err := noryxv1.NewBackupServiceClient(conn).ListBackups(ctx, &noryxv1.ListBackupsRequest{ServerId: args[0]})
					if err != nil {
						return err
					}
					w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
					fmt.Fprintln(w, "ID\tCREATED\tSIZE\tLOCATION\tCONTENT\tLABEL")
					for _, b := range res.GetBackups() {
						fmt.Fprintf(w, "%s\t%s\t%.1f MiB\t%s\t%s\t%s\n", b.GetId(), time.Unix(b.GetCreatedUnix(), 0).Format(time.DateTime),
							float64(b.GetSize())/(1<<20), b.GetLocation(), strings.Join(b.GetPaths(), " "), b.GetLabel())
					}
					return w.Flush()
				})
			},
		},
		create,
		&cobra.Command{
			Use:   "restore <server-id> <backup-id>",
			Short: "Replace the backed up data of a server; a running server restarts",
			Args:  cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				return c.long(cmd, func(ctx context.Context, conn grpc.ClientConnInterface) error {
					req := &noryxv1.RestoreBackupRequest{ServerId: args[0], BackupId: args[1]}
					if _, err := noryxv1.NewBackupServiceClient(conn).RestoreBackup(ctx, req); err != nil {
						return err
					}
					fmt.Fprintf(cmd.OutOrStdout(), "Restored backup %s.\n", args[1])
					return nil
				})
			},
		},
	)
	return cmd
}

func followLogs(ctx context.Context, conn grpc.ClientConnInterface, id string, out io.Writer) error {
	stream, err := noryxv1.NewServerServiceClient(conn).StreamLogs(ctx, &noryxv1.StreamLogsRequest{Id: id, Tail: 100})
	if err != nil {
		return err
	}
	return printLines(stream, out)
}

// printLines prints the lines of a stream until it ends or the command is stopped.
func printLines[T interface{ GetLine() string }](stream interface{ Recv() (T, error) }, out io.Writer) error {
	for {
		res, err := stream.Recv()
		if errors.Is(err, io.EOF) || status.Code(err) == codes.Canceled {
			return nil
		}
		if err != nil {
			return err
		}
		fmt.Fprintln(out, res.GetLine())
	}
}

func printServers(ctx context.Context, conn grpc.ClientConnInterface, out io.Writer) error {
	res, err := noryxv1.NewServerServiceClient(conn).ListServers(ctx, &noryxv1.ListServersRequest{})
	if err != nil {
		return err
	}
	if len(res.GetServers()) == 0 {
		fmt.Fprintln(out, "No servers on this node yet.")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tTYPE\tVERSION\tPORT\tMEMORY\tSTATE")
	for _, s := range res.GetServers() {
		state := s.GetState().Slug()
		if s.GetCrashes() > 0 {
			state += fmt.Sprintf(", %d crashes", s.GetCrashes())
		}
		if s.GetExitCode() != 0 {
			state += fmt.Sprintf(", exit code %d", s.GetExitCode())
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%d MB\t%s\n", s.GetId(), s.GetName(), s.GetType().Slug(),
			s.GetVersion(), s.GetPort(), s.GetMemoryMb(), state)
	}
	return w.Flush()
}
