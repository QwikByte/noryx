package noryxv1

import (
	"strings"
	"testing"
)

// The name of a plugin, which the agent reads from its jar, becomes a folder in the plugin
// folder only if it can't lead elsewhere.
func TestValidPluginFolder(t *testing.T) {
	checkValid(t, ValidPluginFolder,
		[]string{"LuckPerms", "Simple Voice Chat", "_internal", "My.Plugin-1", "velocity-plugin_id", "a" + strings.Repeat("b", 63)},
		[]string{"", " LuckPerms", ".", "..", ".hidden", "-x", "../LuckPerms", "a/b", `a\b`, "x\n", "Lück", "a" + strings.Repeat("b", 64)})
}
