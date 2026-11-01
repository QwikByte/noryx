package docker

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	mcnet "github.com/QwikByte/noryx/internal/agent/network"
	"github.com/QwikByte/noryx/internal/agent/properties"
	"github.com/QwikByte/noryx/internal/agent/runtime"
)

const (
	// reloadTimeout is how long a proxy may take to answer its reload command.
	reloadTimeout = 30 * time.Second
	// sendTimeout is how long BungeeCord may take to answer its send command, which it
	// answers right away.
	sendTimeout = 5 * time.Second
	// velocityWait is how long Velocity may take to answer its send command, which it only
	// answers if it can't send the player.
	velocityWait = time.Second
)

// errQuiet ends reading Velocity's console after a send command that it didn't answer.
var errQuiet = errors.New("the proxy didn't answer")

// notSent are parts of the answers of BungeeCord to a send command that can't send anyone.
var notSent = []string{"That user is not online", "The specified server does not exist", "Only in game players"}

// SendCommand runs the command of a game server on its console over RCON, keeping the
// connection for the next one. Proxies and Bedrock servers have no RCON and get the command on
// their console instead, whose output follows in the logs; see command and bedrockCommand.
func (d *Docker) SendCommand(ctx context.Context, id, command string) (string, error) {
	c, spec, err := d.inspect(ctx, id)
	switch {
	case err != nil:
		return "", err
	case !c.State.Running:
		return "", runtime.ErrNotRunning
	case spec.Type.Proxy():
		return "", d.command(ctx, id, spec.Type, command)
	case spec.Type.Bedrock():
		return "", d.bedrockCommand(ctx, id, command)
	}
	host := hostOf(c)
	out, err := d.consoles.Command(ctx, id, host+" "+c.State.StartedAt, func(ctx context.Context) (string, string, error) {
		return d.rconTarget(ctx, id, host)
	}, command)
	if err != nil && c.State.Health != nil && c.State.Health.Status == container.Starting {
		err = fmt.Errorf("%w: %w", runtime.ErrNotReady, err) // its console opens once it started
	}
	return out, err
}

// rconTarget returns the address and password of the console of a game server, which the
// server image sets in its server.properties.
func (d *Docker) rconTarget(ctx context.Context, id, host string) (addr, password string, err error) {
	if host == "" {
		return "", "", errors.New("the server's ports can't be reached")
	}
	dir, err := d.Data(ctx, id)
	if err != nil {
		return "", "", err
	}
	props, err := properties.Read(dir)
	if err = errors.Join(err, dir.Close()); err != nil {
		return "", "", err
	}
	if props["enable-rcon"] != "true" || props["rcon.password"] == "" {
		return "", "", errors.New("the server's console port is off")
	}
	return net.JoinHostPort(host, cmp.Or(props["rcon.port"], "25575")), props["rcon.password"], nil
}

// hostOf returns the address at which the agent reaches the ports of a container, in any of
// its networks; empty if it can't.
func hostOf(c container.InspectResponse) string {
	for _, ep := range c.NetworkSettings.Networks {
		if ep != nil && ep.IPAddress.IsValid() {
			return ep.IPAddress.String()
		}
	}
	return ""
}

// Reload sends the reload command of a running proxy to its console and waits for its
// answer.
func (d *Docker) Reload(ctx context.Context, id string) error {
	c, spec, err := d.inspect(ctx, id)
	if err != nil {
		return err
	}
	p, ok := mcnet.ProxyOf(spec.Type)
	switch {
	case !ok:
		return runtime.ErrUnsupported
	case !c.State.Running:
		return runtime.ErrNotRunning
	}
	ctx, cancel := context.WithTimeout(ctx, reloadTimeout)
	defer cancel()
	return d.console(ctx, id, p.Reload, reloaded(p))
}

// reloaded returns what reads the answer of a proxy to its reload command, line by line,
// until it tells whether the proxy reloaded. Velocity logs why it couldn't before it
// answers, BungeeCord the exception after.
func reloaded(p mcnet.Proxy) func(line string) (bool, error) {
	var reason string
	failed := false
	return func(line string) (bool, error) {
		line = plainLine(line)
		if _, message, ok := strings.Cut(line, "Exception: "); ok {
			reason = message
		} else if _, message, ok := strings.Cut(line, "ERROR]: "); ok {
			reason = message
		}
		switch {
		case strings.Contains(line, p.Reloaded):
			return true, nil
		case strings.Contains(line, p.Failed):
			if failed = true; reason == "" {
				return false, nil
			}
		case !failed:
			return false, nil
		}
		return true, fmt.Errorf("%w: %s", runtime.ErrReload, cmp.Or(reason, "its console tells why"))
	}
}

// command writes a command to the console of a proxy. Its answer to sending a player tells
// whether it could, so it is awaited. BungeeCord always answers: without its module cmd_send,
// e.g. if it couldn't download it, it has no send command. Velocity only answers if it
// can't send, so its console is read for a moment, unless runtime.NoWait says not to.
func (d *Docker) command(ctx context.Context, id string, typ noryxv1.ServerType, command string) error {
	args := strings.Fields(command)
	switch {
	case len(args) != 3 || !strings.EqualFold(args[0], "send"):
		return d.console(ctx, id, command, nil)
	case typ.Bungee():
		ctx, cancel := context.WithTimeout(ctx, sendTimeout)
		defer cancel()
		return d.console(ctx, id, command, sent)
	case !runtime.Waits(ctx):
		return d.console(ctx, id, command, nil)
	}
	ctx, cancel := context.WithTimeoutCause(ctx, velocityWait, errQuiet)
	defer cancel()
	if err := d.console(ctx, id, command, velocitySent(args[1], args[2])); !errors.Is(context.Cause(ctx), errQuiet) {
		return err
	}
	return nil // quiet means sent
}

// bedrockCommand writes a command to the console of a Bedrock server. Its answer to save query
// tells whether the server is ready for its worlds to be copied, so it is awaited.
func (d *Docker) bedrockCommand(ctx context.Context, id, command string) error {
	if command != "save query" {
		return d.console(ctx, id, command, nil)
	}
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	err := d.console(ctx, id, command, func(line string) (bool, error) {
		switch line = plainLine(line); {
		case strings.Contains(line, "Files are now ready to be copied"):
			return true, nil
		case strings.Contains(line, "previous save has not been completed"):
			return true, runtime.ErrNotReady
		}
		return false, nil
	})
	if err != nil && ctx.Err() != nil {
		return fmt.Errorf("%w: the server didn't answer save query", runtime.ErrNotReady)
	}
	return err
}

// sent reads the answer of BungeeCord to a send command, line by line, until it tells whether
// BungeeCord sends the player.
func sent(line string) (bool, error) {
	line = plainLine(line)
	switch {
	case strings.Contains(line, "Attempting to send"):
		return true, nil
	case strings.Contains(line, "Command not found"):
		return true, runtime.ErrNoSend
	}
	for _, answer := range notSent {
		if i := strings.Index(line, answer); i >= 0 {
			return true, fmt.Errorf("%w: %s", runtime.ErrNotSent, line[i:])
		}
	}
	return false, nil
}

// velocitySent reads the answer of Velocity to a send command, which only comes if it can't
// send the player. It names the player or the server, so that sends at the same time can tell
// their answers apart.
func velocitySent(player, server string) func(line string) (bool, error) {
	answers := []string{"The specified player " + player + " does not exist", "The specified server " + server + " does not exist"}
	return func(line string) (bool, error) {
		line = plainLine(line)
		for _, answer := range answers {
			if i := strings.Index(line, answer); i >= 0 {
				return true, fmt.Errorf("%w: %s", runtime.ErrNotSent, line[i:])
			}
		}
		return false, nil
	}
}

// console writes a command to the standard input of a proxy, which reads its console
// commands from there. If answer is set, it reads the console's output until answer accepts
// a line, and returns the error answer returns for it.
func (d *Docker) console(ctx context.Context, id, command string, answer func(line string) (bool, error)) error {
	if d.podman != nil {
		return d.podmanConsole(ctx, id, command, answer)
	}
	res, err := d.cli.ContainerAttach(ctx, containerName(id), client.ContainerAttachOptions{
		Stream: true, Stdin: true, Stdout: answer != nil, Stderr: answer != nil,
	})
	if err != nil {
		return notFound(err)
	}
	defer res.Close()
	stop := context.AfterFunc(ctx, res.Close) // unblocks reading once ctx is done
	defer stop()
	if _, err := io.WriteString(res.Conn, command+"\n"); err != nil || answer == nil {
		return err
	}
	r, w := io.Pipe()
	defer r.Close()
	go func() {
		_, err := stdcopy.StdCopy(w, w, res.Reader)
		w.CloseWithError(err)
	}()
	var done bool
	readErr := eachLine(r, func(line string) bool {
		done, err = answer(line)
		return !done
	})
	if done {
		return err
	}
	return cmp.Or(ctx.Err(), readErr, errors.New("the proxy's console closed"))
}

// escapes are the terminal escape sequences that colour the output of BungeeCord.
var escapes = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// plainLine removes colours and the prompt from a line of a proxy's console.
func plainLine(line string) string {
	return strings.TrimLeft(escapes.ReplaceAllString(line, ""), "> ")
}
