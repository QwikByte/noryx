// Package terminal runs the commands of the master and of the agents from the panel. It
// offers the same commands as the agent's local CLI, run over the master's mutually
// authenticated connection to the agent, and commands about the master and its nodes.
// Nothing runs in a shell, and commands that only the node's administrator may run,
// such as adding storage locations, stay in the local CLI.
package terminal

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agentcli"
	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/logs"
	"github.com/QwikByte/noryx/internal/master/node"
	"github.com/QwikByte/noryx/internal/master/settings"
)

// MasterTarget is the target of the master's own commands; other targets are node IDs.
const MasterTarget = "master"

const (
	maxCommandLen = 1000
	// keepAlive is how often a running command sends an empty line, so that a reverse proxy
	// in front of the master, which waits 60 seconds for data by default, doesn't give up on
	// one that writes nothing, e.g. a stop that takes the server's stop timeout.
	keepAlive = 20 * time.Second
)

// Nodes provides the nodes and the connections to their agents.
type Nodes interface {
	List(ctx context.Context) ([]node.Node, error)
	Get(ctx context.Context, id string) (node.Node, error)
	Conn(ctx context.Context, id string) (grpc.ClientConnInterface, error)
	Status(ctx context.Context, id string) (*noryxv1.GetInfoResponse, *x509.Certificate, error)
	RenewCertificate(ctx context.Context, id string) (*x509.Certificate, error)
}

// Master describes the running master.
type Master interface {
	Master() settings.Master
	EnrollAddr() string
}

type Handler struct {
	nodes  Nodes
	master Master
	logs   *logs.Store
	moving func(serverID string) error
}

// NewHandler returns the handler of the terminal. moving refuses changes to a server while
// it moves to another node.
func NewHandler(nodes Nodes, master Master, logs *logs.Store, moving func(serverID string) error) *Handler {
	return &Handler{nodes: nodes, master: master, logs: logs, moving: moving}
}

// Register adds the routes; besides the terminal permission, every command needs its own.
func (h *Handler) Register(mux access.Mux) {
	mux.Handle("POST /api/terminal", access.Everywhere(access.Terminal), h.run)
	mux.Handle("GET /api/terminal/commands", access.Everywhere(access.Terminal), h.commands)
}

// run runs a command line on a target and streams its output as JSON lines: {"output": …}
// whenever the command writes, {} every keepAlive, and finally {"done": true}, with "error"
// if it failed. Cancelling the request stops the command, e.g. one that follows a console.
func (h *Handler) run(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Target  string `json:"target"`
		Command string `json:"command"`
	}
	if err := httpapi.ReadJSON(w, r, &req); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	args, err := parse(req.Command)
	var root *cobra.Command
	if err == nil {
		root, _, err = h.root(r.Context(), req.Target)
	}
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	// Commands may be copied from the documentation, with the program's name.
	if len(args) > 0 && args[0] == root.Name() {
		args = args[1:]
	}
	logging.Note(r.Context(), logging.Command(req.Command))
	if req.Target != MasterTarget {
		logging.Note(r.Context(), slog.String(logging.KeyNode, req.Target))
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no") // keeps nginx from buffering the output
	out := &events{enc: json.NewEncoder(w), flusher: http.NewResponseController(w)}
	root.SetArgs(args)
	root.SetIn(strings.NewReader(""))
	root.SetOut(out)
	root.SetErr(out)
	stop := out.keepAlive(keepAlive)
	err = root.ExecuteContext(r.Context())
	stop()
	done := event{Done: true}
	if err != nil {
		done.Error = err.Error()
		if st, ok := status.FromError(err); ok {
			done.Error = st.Message() // errors of agents, e.g. of node renew, without the gRPC prefix
		}
		logging.Note(r.Context(), slog.String("err", done.Error))
	}
	_ = out.send(done) // fails only if the browser went away
}

func parse(line string) ([]string, error) {
	if len(line) > maxCommandLen || strings.ContainsFunc(line, unicode.IsControl) {
		return nil, httpapi.Errorf(http.StatusBadRequest, "Enter a command on one line with up to %d characters.", maxCommandLen)
	}
	args, err := splitArgs(line)
	if err != nil {
		return nil, httpapi.Errorf(http.StatusBadRequest, "The command can't be read: %v.", err)
	}
	return args, nil
}

// root returns the commands of a target, the master or the agent of a node, and their checks.
// Each command checks the permission it needs before it runs.
func (h *Handler) root(ctx context.Context, target string) (*cobra.Command, map[string]check, error) {
	if target == MasterTarget {
		checks := h.masterChecks()
		root := guarded("noryx-master", "Commands of the master. Choose a node to run the commands of its agent.", checks)
		root.AddCommand(h.masterCommands()...)
		return root, checks, nil
	}
	n, err := h.nodes.Get(ctx, target)
	if err == nil && !access.From(ctx).SeesNode(n.ID) {
		err = access.Denied(access.ServersView)
	}
	if err != nil {
		return nil, nil, err
	}
	checks := agentChecks(n.ID, h.moving)
	root := guarded("noryx-agent", fmt.Sprintf("Commands of the agent of %s. Storage locations and the private network are only allowed on the node itself.", n.Name), checks)
	root.AddCommand(agentcli.Commands(func(ctx context.Context, fn func(grpc.ClientConnInterface) error) error {
		conn, err := h.nodes.Conn(ctx, n.ID)
		if err != nil {
			return err
		}
		return fn(conn)
	})...)
	return root, checks, nil
}

type event struct {
	Output string `json:"output,omitempty"`
	Done   bool   `json:"done,omitempty"`
	Error  string `json:"error,omitempty"`
}

// events sends the output of a command to the browser as soon as it is written.
type events struct {
	mu      sync.Mutex
	enc     *json.Encoder
	flusher *http.ResponseController
	err     error // the first error, after which nothing is sent anymore
}

func (e *events) Write(p []byte) (int, error) {
	return len(p), e.send(event{Output: string(p)})
}

func (e *events) send(ev event) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err == nil {
		e.err = e.enc.Encode(ev)
	}
	if e.err == nil {
		e.err = e.flusher.Flush()
	}
	return e.err
}

// keepAlive sends an empty event every interval until stop is called, which returns once it
// sends no more.
func (e *events) keepAlive(interval time.Duration) (stop func()) {
	ticker, done, stopped := time.NewTicker(interval), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				_ = e.send(event{})
			}
		}
	}()
	return func() {
		ticker.Stop()
		close(done)
		<-stopped
	}
}
