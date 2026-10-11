// Package runtime abstracts how Minecraft servers are executed on a node: in containers of
// Docker or Podman, whose differences the package docker keeps to itself.
package runtime

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/datadir"
	"github.com/QwikByte/noryx/internal/agent/progress"
	"github.com/QwikByte/noryx/internal/logging"
)

// idPattern matches server IDs, which also name directories and containers.
var idPattern = regexp.MustCompile(`^[a-z2-7]{26}$`)

// NewID returns a random server ID.
func NewID() string { return strings.ToLower(rand.Text()) }

// ValidID reports whether id is a well-formed server ID.
func ValidID(id string) bool { return idPattern.MatchString(id) }

var (
	// ErrNotFound is returned for operations on unknown servers.
	ErrNotFound = errors.New("server not found")
	// ErrNotRunning is returned for console commands to a stopped server.
	ErrNotRunning = errors.New("server is not running")
	// ErrUnsupported is returned for operations the server type does not support.
	ErrUnsupported = errors.New("not supported for this server type")
	// ErrNotReady is returned when a server that is starting can't save its worlds yet.
	ErrNotReady = errors.New("the server is starting")
	// ErrReload is returned when a proxy answers that it couldn't reload its configuration.
	ErrReload = errors.New("the proxy couldn't reload its configuration")
	// ErrNoSend is returned when a proxy has no send command, as it couldn't load its module.
	ErrNoSend = errors.New("the proxy has no send command")
	// ErrNotSent is returned when a proxy answers that it couldn't send a player.
	ErrNotSent = errors.New("the proxy couldn't send the player")
)

type noWaitKey struct{}

// NoWait makes SendCommand not wait for an answer to a send command that only comes if the
// proxy can't send the player, as Velocity's, e.g. to send many players at once.
func NoWait(ctx context.Context) context.Context { return context.WithValue(ctx, noWaitKey{}, true) }

// Waits reports whether SendCommand waits for an answer that only comes on failure.
func Waits(ctx context.Context) bool { return ctx.Value(noWaitKey{}) == nil }

// Spec describes a server.
type Spec struct {
	ID       string             `json:"id"`
	Name     string             `json:"name"`
	Type     noryxv1.ServerType `json:"type"`
	Version  string             `json:"version"`
	MemoryMB uint32             `json:"memoryMb"`
	Port     uint32             `json:"port"`
	// BehindProxy makes a game server accept only players forwarded by its proxy.
	BehindProxy bool `json:"behindProxy,omitempty"`
	// ProxyOnNode tells that the proxy of a backend runs on the same node, which reaches it
	// without a published port.
	ProxyOnNode bool `json:"proxyOnNode,omitempty"`
	// BedrockPlayers tells that Bedrock players join a game server through its proxy. They
	// can't sign their chat messages, so the server doesn't demand it.
	BedrockPlayers bool `json:"bedrockPlayers,omitempty"`
	// Storage is the storage location of the server's data; empty means the default.
	Storage string `json:"storage,omitempty"`
	// Java selects the Java version of a game server, e.g. "21"; empty means the newest.
	Java          string                `json:"java,omitempty"`
	RestartPolicy noryxv1.RestartPolicy `json:"restartPolicy,omitempty"`
	AikarFlags    bool                  `json:"aikarFlags,omitempty"`
	JVMOptions    []string              `json:"jvmOptions,omitempty"`
	// CPUMillis limits the CPU time in thousandths of a core; 0 means no limit.
	CPUMillis uint32 `json:"cpuMillis,omitempty"`
	// LoaderVersion selects the version of the mod loader of a modded server, e.g. one a
	// modpack needs; empty means the newest.
	LoaderVersion string `json:"loaderVersion,omitempty"`
	// StopTimeout is how many seconds the server gets to stop gracefully; 0 means the default.
	StopTimeout uint32 `json:"stopTimeout,omitempty"`
	// TimeZone is the IANA time zone of the server, e.g. Europe/Berlin; empty means UTC.
	TimeZone string `json:"timeZone,omitempty"`
	// BedrockPort is the UDP port of Geyser on a proxy whose network lets Bedrock players
	// join; 0 for none. The network sets it.
	BedrockPort uint32 `json:"bedrockPort,omitempty"`
	// Overlay is the node's address in the private network of the nodes, where a backend
	// publishes its port for its proxy on another node; empty publishes it on all addresses.
	Overlay string `json:"overlay,omitempty"`
	// ExtraPorts are ports of plugins, e.g. of a web map, which the node publishes on all its
	// addresses, also for a backend.
	ExtraPorts []ExtraPort `json:"extraPorts,omitempty"`
	// HealthStart is how many minutes a game server gets to start before its failed health
	// checks count; 0 means the default.
	HealthStart uint32 `json:"healthStart,omitempty"`
	// CrashLimit is after how many crashes in a row the agent stops the server, each within
	// StableAfter minutes of its start; 0 means the default of each.
	CrashLimit  uint32 `json:"crashLimit,omitempty"`
	StableAfter uint32 `json:"stableAfter,omitempty"`
	// UnhealthyRestart is after how many minutes the agent restarts a running server that stays
	// unhealthy, which counts as a crash; 0 means never.
	UnhealthyRestart uint32 `json:"unhealthyRestart,omitempty"`
	// ProcessLimit is how many processes and threads the container may run; 0 means the default.
	ProcessLimit uint32 `json:"processLimit,omitempty"`
	// InitMemoryMB is the initial heap, below MemoryMB; 0 means as large as MemoryMB.
	InitMemoryMB uint32 `json:"initMemoryMb,omitempty"`
}

// ExtraPort is a port of a plugin, published at the same port of the container, where the
// plugin listens.
type ExtraPort struct {
	Port     uint32 `json:"port"`
	Protocol string `json:"protocol"` // tcp or udp
	Label    string `json:"label,omitempty"`
}

// Uses reports whether a server uses a port of the node: its own, the UDP port at which
// Bedrock players join a proxy, or one of its extra ports.
func (s Spec) Uses(port uint32) bool {
	return s.Port == port || s.BedrockPort != 0 && s.BedrockPort == port ||
		slices.ContainsFunc(s.ExtraPorts, func(p ExtraPort) bool { return p.Port == port })
}

// Server is a server managed by a runtime.
type Server struct {
	Spec
	State noryxv1.ServerState
	// Crashes counts the crashes since the server was last started, and ExitCode is the
	// exit code of the latest, if known. Both are only set while it crashes, or after it
	// stopped because of a crash.
	Crashes  int
	ExitCode int
	// Unhealthy is set while a running server's health check fails, e.g. as it hangs.
	Unhealthy bool
}

// Forwarding is how a proxy forwards its players to the game servers of its network.
type Forwarding int

const (
	// ForwardingNone means that a game server isn't part of a network.
	ForwardingNone Forwarding = iota
	// ForwardingModern is Velocity's modern forwarding, signed with the forwarding secret.
	ForwardingModern
	// ForwardingLegacy is BungeeCord's forwarding, which doesn't prove that players come
	// through the proxy, so only the proxy may reach the game servers.
	ForwardingLegacy
)

// Network is the role of a server in a network behind a proxy.
type Network struct {
	Forwarding Forwarding
	// ForwardingSecret lets backends verify the players of modern forwarding.
	ForwardingSecret string
	// ProxyOnNode is set for a backend whose proxy runs on the same node.
	ProxyOnNode bool
	// BedrockPlayers is set for the backends of a network that lets Bedrock players join.
	BedrockPlayers bool
	// Backends, Try and ForcedHosts are set for the proxy. Players join the backends of
	// Try and fall back to them in this order, or to those of a forced host if they
	// connect through its host name.
	Backends    []NetworkBackend
	Try         []string
	ForcedHosts []ForcedHost
	// BedrockPort is set for the proxy of a network that lets Bedrock players join through
	// Geyser at this UDP port.
	BedrockPort uint32
	// Overlay is set for a backend whose proxy reaches it over the private network of the
	// nodes: the node's address there, where it publishes its port.
	Overlay string
	// Datastores are the datastores of the network on this node, which the server reaches by
	// the name of their containers.
	Datastores []string
}

// NetworkBackend is a server behind the proxy, either on the same node (ServerID) or
// on another node (Address).
type NetworkBackend struct {
	Name     string
	ServerID string
	Address  string
	// Restricted and Motd are settings of BungeeCord: only players with the permission
	// bungeecord.server.<name> may join a restricted server, and the MOTD is shown for host
	// names that lead to it.
	Restricted bool
	Motd       string
}

// ForcedHost sends players who connect through a host name to certain backends.
type ForcedHost struct {
	Host    string
	Servers []string
}

// Usage is what a running server uses. CPU time and network traffic count up from the
// start of the server.
type Usage struct {
	CPUTime     time.Duration
	MemoryBytes uint64
	// MemoryLimit is the most memory the server may use; 0 if it isn't limited.
	MemoryLimit uint64
	NetRxBytes  uint64
	NetTxBytes  uint64
}

// LogLine is a line of a server's console or a datastore's log and when it was written; Time
// is zero if unknown.
type LogLine struct {
	Time time.Time
	Text string
}

// Info describes the runtime and the machine it runs on.
type Info struct {
	Version     string // of the runtime, e.g. "29.0.0"
	OS          string
	CPUs        uint32
	MemoryBytes uint64
}

// Runtime executes Minecraft servers and the datastores of their networks. Both keep running
// when the agent restarts.
type Runtime interface {
	Datastores
	// Name names the runtime, e.g. "docker" (see noryxv1.RuntimeDocker), also while it can't
	// be reached.
	Name() string
	Info(ctx context.Context) (Info, error)
	List(ctx context.Context) ([]Server, error)
	Create(ctx context.Context, spec Spec) error
	Start(ctx context.Context, id string) error
	// Stop stops a server gracefully, and kills it once its stop timeout is over.
	Stop(ctx context.Context, id string) error
	Remove(ctx context.Context, id string) error
	// Logs yields the last tail console lines written after the time after, if it isn't
	// zero, then follows the console until the server stops or ctx is cancelled.
	Logs(ctx context.Context, id string, tail int, after time.Time) iter.Seq2[LogLine, error]
	// SendCommand runs a console command and returns its output. Proxies read commands
	// from their console, whose output follows in the logs.
	SendCommand(ctx context.Context, id, command string) (string, error)
	// Reload makes a running proxy read its configuration again, through its console.
	Reload(ctx context.Context, id string) error
	// Usage returns what a running server uses, or ErrNotRunning.
	Usage(ctx context.Context, id string) (Usage, error)
	// Update replaces the settings of a server, keeping its type, data and network role.
	// A running server restarts.
	Update(ctx context.Context, spec Spec) error
	// UpdateImage pulls the image of a server again and, if it changed, creates the container
	// again with it. A running server restarts. It tells whether the image changed.
	UpdateImage(ctx context.Context, id string) (bool, error)
	// Restart stops a server gracefully and starts it again.
	Restart(ctx context.Context, id string) error
	// Configure gives a server its role in a network. A running game server restarts if
	// that changed it, a running proxy reloads its configuration or restarts if it must. It
	// reports whether the server restarted.
	Configure(ctx context.Context, id string, network Network) (restarted bool, err error)
	// Data opens the data directory of a server; the caller closes it.
	Data(ctx context.Context, id string) (*datadir.Dir, error)
	// Duplicate creates a stopped server with a copy of the data of the server from, in
	// the same storage location. The copy is standalone: whatever ties the original to a
	// network, such as the forwarding secret, is reset or left out.
	Duplicate(ctx context.Context, from string, spec Spec) error
	// LimitLogs limits the output that the runtime keeps of each container of a server or
	// datastore created from now on to files files of up to fileMB MB each; 0 leaves it to
	// the runtime's configuration.
	LimitLogs(fileMB, files uint32)
}

// Find returns the server with the given ID, or ErrNotFound.
func Find(ctx context.Context, rt Runtime, id string) (Server, error) {
	servers, err := rt.List(ctx)
	if err != nil {
		return Server{}, err
	}
	i := slices.IndexFunc(servers, func(s Server) bool { return s.ID == id })
	if i < 0 {
		return Server{}, ErrNotFound
	}
	return servers[i], nil
}

// PauseSaving makes a running game server write its worlds to disk and stop saving, so
// that its data can be copied consistently while players stay connected. resume turns
// saving on again. Stopped servers and proxies need nothing.
func PauseSaving(ctx context.Context, rt Runtime, srv Server) (resume func(), err error) {
	if srv.State == noryxv1.ServerState_SERVER_STATE_STOPPED || srv.Type.Proxy() {
		return func() {}, nil
	}
	progress.Step(ctx, "save", 0)
	resume = func() {
		if _, err := rt.SendCommand(context.WithoutCancel(ctx), srv.ID, "save-on"); err != nil {
			slog.Warn("Can't turn saving on again", logging.Servers, logging.KeyServer, srv.ID, "err", err)
		}
	}
	if _, err := rt.SendCommand(ctx, srv.ID, "save-off"); err != nil {
		if ctx.Err() != nil {
			resume() // the server may have got it before the call was cancelled
		}
		return nil, fmt.Errorf("%w: %w", ErrNotReady, err)
	}
	if _, err := rt.SendCommand(ctx, srv.ID, "save-all flush"); err != nil {
		resume()
		return nil, err
	}
	return resume, nil
}
