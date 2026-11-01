// Package network writes the configuration that joins servers into a network behind a
// proxy, the way Velocity, BungeeCord and the game servers document it, and
// edits the other settings of a proxy's configuration for the panel.
package network

import (
	"cmp"
	"errors"
	"io/fs"
	"net"
	"slices"
	"strconv"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/datadir"
	"github.com/QwikByte/noryx/internal/agent/runtime"
)

const (
	// velocityConfigVersion is the config version Velocity 4 writes. Velocity migrates
	// older versions on start, but treats a missing version as a legacy config.
	velocityConfigVersion = "2.9"
	// ForwardingSecretFile is the file next to velocity.toml that holds the secret.
	ForwardingSecretFile = "forwarding.secret"
)

// Proxy describes the configuration of a type of proxy.
type Proxy struct {
	File string
	// managed are the settings that the manager or the network decides, with the reason;
	// * stands for any position in a list.
	managed map[string]string
	// render writes a network into the settings of the file.
	render func(settings map[string]any, n runtime.Network)
	bind   func(settings map[string]any, address string)
	// Reload is the console command that reloads the configuration, and Reloaded and
	// Failed are parts of the lines it answers with.
	Reload, Reloaded, Failed string
}

const (
	inNetwork = "The servers of the proxy are chosen in its network."
	bound     = "Inside its container, the proxy always listens on this port. Change the port in the server's settings."
)

var velocity = Proxy{
	File: noryxv1.ServerType_SERVER_TYPE_VELOCITY.ConfigFile(),
	managed: map[string]string{
		"config-version":              "Velocity manages the version of its configuration.",
		"bind":                        bound,
		"servers":                     inNetwork,
		"forced-hosts":                inNetwork,
		"player-info-forwarding-mode": "The forwarding is chosen in the proxy's network.",
		"forwarding-secret-file":      "The network keeps its forwarding secret in this file.",
		// Velocity 1 kept the secret itself in its configuration, which never reaches the panel.
		"forwarding-secret": "The forwarding secret stays on the node.",
	},
	render:   renderVelocity,
	bind:     func(s map[string]any, address string) { s["bind"] = address },
	Reload:   "velocity reload",
	Reloaded: "successfully reloaded",
	Failed:   "Unable to reload your Velocity configuration",
}

var bungee = Proxy{
	File: noryxv1.ServerType_SERVER_TYPE_BUNGEECORD.ConfigFile(),
	managed: map[string]string{
		"servers":                  inNetwork,
		"ip_forward":               "The forwarding is chosen in the proxy's network.",
		"listeners.*.host":         bound,
		"listeners.*.priorities":   inNetwork,
		"listeners.*.forced_hosts": inNetwork,
	},
	render: renderBungee,
	bind: func(s map[string]any, address string) {
		for _, l := range listeners(s) {
			l["host"] = address
		}
	},
	Reload:   "greload",
	Reloaded: "has been reloaded",
	Failed:   "Error in dispatching command",
}

// ProxyOf returns the configuration of a type of proxy.
func ProxyOf(typ noryxv1.ServerType) (Proxy, bool) {
	switch {
	case typ == noryxv1.ServerType_SERVER_TYPE_VELOCITY:
		return velocity, true
	case typ.Bungee():
		return bungee, true
	}
	return Proxy{}, false
}

// WriteProxy writes a network into the configuration of a proxy, with the backends at the
// addresses the proxy reaches them. It reports whether that changed the configuration, and
// whether it removed or renamed backends, which BungeeCord can't reload.
func WriteProxy(dir *datadir.Dir, typ noryxv1.ServerType, n runtime.Network) (changed, removed bool, err error) {
	p, ok := ProxyOf(typ)
	if !ok {
		return false, false, runtime.ErrUnsupported
	}
	// A proxy that leaves its network before it ever started has nothing to change.
	changed, err = edit(dir, p.File, n.Forwarding != runtime.ForwardingNone, func(settings map[string]any) error {
		old, _ := settings["servers"].(map[string]any)
		for name := range old {
			removed = removed || name != "try" && !slices.ContainsFunc(n.Backends, func(b runtime.NetworkBackend) bool { return b.Name == name })
		}
		p.render(settings, n)
		return nil
	})
	if err != nil || p.File != velocity.File || n.Forwarding == runtime.ForwardingLegacy {
		return changed, removed, err
	}
	// Velocity reads the secret of modern forwarding from a file next to its configuration. A
	// proxy that leaves its network loses it, as the network may keep it with another proxy;
	// Velocity creates a new one if it needs one.
	if n.Forwarding == runtime.ForwardingNone {
		if err := dir.Remove(ForwardingSecretFile); !errors.Is(err, fs.ErrNotExist) {
			return changed || err == nil, removed, err
		}
		return changed, removed, nil
	}
	secret, err := dir.ReadOptional(ForwardingSecretFile)
	if err != nil || string(secret) == n.ForwardingSecret {
		return changed, removed, err
	}
	return true, removed, dir.WriteFile(ForwardingSecretFile, []byte(n.ForwardingSecret))
}

// ProxyBind makes a proxy listen on all addresses at port, as the container publishes
// that port. A proxy without configuration yet gets one, except BungeeCord, whose image
// downloads a configuration that listens there already. It reports whether it changed.
func ProxyBind(dir *datadir.Dir, typ noryxv1.ServerType, port int) (bool, error) {
	p, ok := ProxyOf(typ)
	if !ok {
		return false, nil
	}
	return edit(dir, p.File, !typ.Bungee(), func(settings map[string]any) error {
		if len(settings) == 0 && p.File == velocity.File {
			settings["config-version"] = velocityConfigVersion
		}
		p.bind(settings, net.JoinHostPort("0.0.0.0", strconv.Itoa(port)))
		return nil
	})
}

var forwardingModes = map[runtime.Forwarding]string{
	runtime.ForwardingNone: "NONE", runtime.ForwardingModern: "MODERN", runtime.ForwardingLegacy: "LEGACY",
}

// renderVelocity writes the servers, the list players try, the forced hosts and the
// forwarding into velocity.toml.
func renderVelocity(s map[string]any, n runtime.Network) {
	if len(s) == 0 {
		s["config-version"] = velocityConfigVersion
	}
	servers := map[string]any{"try": append([]string{}, n.Try...)}
	for _, b := range n.Backends {
		servers[b.Name] = b.Address
	}
	hosts := map[string]any{} // the defaults point to example servers
	for _, h := range n.ForcedHosts {
		hosts[h.Host] = h.Servers
	}
	s["servers"], s["forced-hosts"] = servers, hosts
	s["player-info-forwarding-mode"] = forwardingModes[n.Forwarding]
	s["forwarding-secret-file"] = ForwardingSecretFile
}

// renderBungee writes the servers, their priorities, the forced hosts and the forwarding
// into config.yml of BungeeCord. It needs at least one server, so a
// proxy that leaves its network keeps its servers and only stops forwarding.
func renderBungee(s map[string]any, n runtime.Network) {
	s["ip_forward"] = n.Forwarding != runtime.ForwardingNone
	if len(n.Backends) == 0 {
		return
	}
	if len(listeners(s)) == 0 {
		s["listeners"] = []any{map[string]any{}}
	}
	motd, _ := listeners(s)[0]["motd"].(string)
	servers := map[string]any{}
	for _, b := range n.Backends {
		servers[b.Name] = map[string]any{"address": b.Address, "motd": cmp.Or(b.Motd, motd), "restricted": b.Restricted}
	}
	hosts := map[string]any{}
	for _, h := range n.ForcedHosts {
		hosts[h.Host] = h.Servers[0] // BungeeCord sends a host name to one server
	}
	s["servers"] = servers
	for _, l := range listeners(s) {
		l["priorities"], l["forced_hosts"] = append([]string{}, n.Try...), hosts
	}
}

// listeners returns the listeners of a BungeeCord configuration.
func listeners(s map[string]any) []map[string]any {
	list, _ := s["listeners"].([]any)
	var found []map[string]any
	for _, l := range list {
		if m, ok := l.(map[string]any); ok {
			found = append(found, m)
		}
	}
	return found
}
