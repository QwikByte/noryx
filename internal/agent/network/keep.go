package network

import (
	"strings"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/datadir"
	"github.com/QwikByte/noryx/internal/agent/properties"
)

// forwarding returns the settings by file, with paths like those of Settings, that decide
// how a server trusts the players its proxy forwards, or how a proxy forwards them: those
// that WriteBackend and WriteProxy write, and the secret that Velocity 1 kept in its
// configuration, which Velocity moves into the file of the secret when it starts.
func forwarding(typ noryxv1.ServerType) map[string][]string {
	switch {
	case typ == noryxv1.ServerType_SERVER_TYPE_VELOCITY:
		return map[string][]string{velocity.File: {"player-info-forwarding-mode", "forwarding-secret-file", "forwarding-secret"}}
	case typ.Bungee():
		return map[string][]string{bungee.File: {"ip_forward"}}
	case typ.Paper():
		return map[string][]string{
			PaperGlobalFile: {"proxies.velocity.enabled", "proxies.velocity.online-mode", "proxies.velocity.secret"},
			"spigot.yml":    {"settings.bungeecord"},
		}
	case typ.Fabric():
		return map[string][]string{FabricProxyFile: {"secret"}}
	case typ == noryxv1.ServerType_SERVER_TYPE_FORGE, typ == noryxv1.ServerType_SERVER_TYPE_NEOFORGE:
		return map[string][]string{ForgeProxyFile: {"forwarding.enabled", "forwarding.mode", "forwarding.secret"}}
	}
	return nil
}

// leftProperties are the properties of game servers that their network turns off, which
// Leave turns on again, with Minecraft's defaults.
var leftProperties = map[string]string{"online-mode": "true", "enforce-secure-profile": "true"}

// KeepForwarding gives restored, a backup of a server's data about to replace current, the
// forwarding settings and the online mode of current, so that the backup brings back neither
// the forwarding secret of a network the server left since, nor the trust in its proxy, nor
// offline mode. A setting that current lacks, also in a file it can't read, is removed; a
// file that restored lacks stays missing, and one it can't read fails.
func KeepForwarding(current, restored *datadir.Dir, typ noryxv1.ServerType) error {
	for file, keys := range forwarding(typ) {
		now := map[string]any{}
		if data, err := current.ReadFile(file); err == nil {
			if settings, err := parse(file, data); err == nil {
				now = settings
			}
		}
		_, err := edit(restored, file, false, func(settings map[string]any) error {
			for _, key := range keys {
				path := strings.Split(key, ".")
				last := path[len(path)-1]
				if value, ok := holder(now, path, false)[last]; ok {
					holder(settings, path, true)[last] = value
				} else {
					delete(holder(settings, path, false), last)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if typ.Proxy() {
		return nil
	}
	if _, err := restored.Lstat("server.properties"); err != nil {
		return ignoreMissing(err)
	}
	now, _ := properties.Read(current) // unreadable, it has Minecraft's defaults
	then, err := properties.Read(restored)
	if err != nil {
		return err
	}
	changes := map[string]string{}
	for key, def := range leftProperties {
		if typ.Bedrock() && key != "online-mode" {
			continue // Bedrock players have no secure profiles
		}
		value := func(props map[string]string) string {
			if v, ok := props[key]; ok {
				return v
			}
			return def
		}
		if value(then) != value(now) {
			changes[key] = value(now)
		}
	}
	if len(changes) == 0 {
		return nil
	}
	return properties.Write(restored, changes)
}

// holder returns the map of nested settings that holds the last key of path, or nil if it
// doesn't exist and create is false.
func holder(settings map[string]any, path []string, create bool) map[string]any {
	for _, key := range path[:len(path)-1] {
		if create {
			settings = child(settings, key)
			continue
		}
		var ok bool
		if settings, ok = settings[key].(map[string]any); !ok {
			return nil
		}
	}
	return settings
}
