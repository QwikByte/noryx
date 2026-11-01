// Package secrets keeps the secrets in the data of servers from the users of the panel: the
// RCON password, with which clients run console commands, and the forwarding secret of a
// network, with which they can sign in to every server of the network as any player. The
// file manager hides files that only hold secrets and shows the others with the secrets
// replaced, and saving such a file keeps them. Downloads of folders and backups do the same.
// Besides its fixed lists, a server can have files that the agent marked as secret, e.g.
// those of file sets with their secrets filled in.
package secrets

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"io/fs"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"go.yaml.in/yaml/v3"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/datadir"
	"github.com/QwikByte/noryx/internal/agent/network"
	"github.com/QwikByte/noryx/internal/agent/properties"
)

// Placeholder replaces the secrets in the files the panel shows.
const Placeholder = "<hidden>"

// hidden are the files that only hold secrets: those of rcon-cli, which the server image
// writes, the forwarding secret of a proxy, and those of Geyser and Floodgate.
var hidden = append([]string{".rcon-cli.env", ".rcon-cli.yaml", network.ForwardingSecretFile}, network.BedrockSecretFiles...)

var (
	geyserVelocity = network.GeyserFolder(noryxv1.ServerType_SERVER_TYPE_VELOCITY)
	geyserBungee   = network.GeyserFolder(noryxv1.ServerType_SERVER_TYPE_BUNGEECORD)
	// geyserSecrets are the keys of Geyser's configuration for external signaling and HTTPS.
	geyserSecrets = []string{"token", "password", "private-key"}
)

// redacted are the files with secrets among other settings, with the keys of the secrets.
var redacted = map[string]*lines{
	"server.properties": secretLines(slices.Collect(maps.Keys(properties.Secret))),
	// The forwarding secret of a network on game servers: in Paper's configuration or in
	// that of the forwarding mod of Fabric, Quilt, Forge or NeoForge.
	network.PaperGlobalFile:        secretLines([]string{"secret"}),
	network.FabricProxyFile:        secretLines([]string{"secret"}),
	network.ForgeProxyFile:         secretLines([]string{"secret"}),
	geyserVelocity + "/config.yml": secretLines(geyserSecrets),
	geyserBungee + "/config.yml":   secretLines(geyserSecrets),
}

// lines are the lines of a file with a secret: re matches the key with its separator, the
// key, and the value.
type lines struct {
	keys []string
	re   *regexp.Regexp
}

func secretLines(keys []string) *lines {
	quoted := make([]string, len(keys))
	for i, k := range keys {
		quoted[i] = regexp.QuoteMeta(k)
	}
	return &lines{keys, regexp.MustCompile(`(?m)^([ \t]*(` + strings.Join(quoted, "|") + `)[ \t]*[=:][ \t]*)([^\r\n]*[^\s])`)}
}

// ErrExposed refuses a file whose format would read a secret as part of another value, or
// can't be read.
var ErrExposed = errors.New("a secret would become part of another setting")

// Hidden reports whether name, a clean path in the data directory of a server, is one of
// the files that only hold secrets on every server.
func Hidden(name string) bool { return slices.Contains(hidden, name) }

// Redacted reports whether name is a file with secrets that Redact replaces.
func Redacted(name string) bool { return redacted[name] != nil }

// Files are the files with secrets in the data of a server: those of the fixed lists, and
// the files the agent marked on the server, which are hidden.
type Files struct{ marked []string }

// With returns the files with secrets of a server whose agent marked the given files, clean
// paths in its data directory.
func With(marked ...string) Files { return Files{marked} }

// Hidden reports whether name is a file that only holds secrets, or was marked.
func (f Files) Hidden(name string) bool { return Hidden(name) || slices.Contains(f.marked, name) }

// Paths returns the files that only hold secrets, and the marked ones.
func (f Files) Paths() []string { return slices.Concat(hidden, f.marked) }

// Under returns the files that may hold secrets at name or in the folder name.
func (f Files) Under(name string) []string {
	return slices.DeleteFunc(slices.Concat(hidden, f.marked, slices.Collect(maps.Keys(redacted))), func(p string) bool {
		return name != "." && name != p && !strings.HasPrefix(p, name+"/")
	})
}

// Redact returns data, the content of the file name, with its secrets replaced by Placeholder.
func Redact(name string, data []byte) []byte {
	if l := redacted[name]; l != nil {
		return l.re.ReplaceAll(data, []byte("${1}"+Placeholder))
	}
	return data
}

// Restore returns data, which replaces the file name whose content is current, with each
// Placeholder replaced by the secret of the same key in current, or by none. Redact sees
// lines, while the format of the file may read one as part of another value, e.g. after a
// line that ends with a backslash in server.properties, or in a text of several lines in
// YAML, which the panel or the players would see. So Restore fills in random markers first,
// and fails with ErrExposed unless the format reads each as the whole value of a secret key.
func Restore(name string, data, current []byte) ([]byte, error) {
	l := redacted[name]
	if l == nil {
		return data, nil
	}
	secrets := map[string][]byte{}
	for _, m := range l.re.FindAllSubmatch(current, -1) {
		secrets[string(m[2])] = m[3]
	}
	nonce := "noryx" + strings.ToLower(rand.Text())
	var filled [][]byte
	marked := l.re.ReplaceAllFunc(data, func(line []byte) []byte {
		m := l.re.FindSubmatch(line)
		secret := secrets[string(m[2])]
		switch {
		case string(m[3]) != Placeholder:
			return line
		case len(secret) == 0: // a placeholder without a secret never becomes one
			return m[1]
		}
		filled = append(filled, secret)
		return slices.Concat(m[1], marker(name, nonce, len(filled)-1))
	})
	if len(filled) == 0 {
		return marked, nil
	}
	if !l.confines(name, marked, nonce) {
		return nil, ErrExposed
	}
	for i, secret := range filled {
		marked = bytes.Replace(marked, marker(name, nonce, i), secret, 1)
	}
	return marked, nil
}

// marker stands in for the ith secret while Restore checks a file: letters and digits that
// every format reads as they are, quoted in TOML, which knows no text without quotes.
func marker(name, nonce string, i int) []byte {
	m := nonce + "x" + strconv.Itoa(i) + "x"
	if path.Ext(name) == ".toml" {
		m = `"` + m + `"`
	}
	return []byte(m)
}

// confines reports whether the format of the file name reads every value with the nonce as
// a whole marker, set to a key of the secrets: not in another key or value, nor in a list.
func (l *lines) confines(name string, data []byte, nonce string) bool {
	ok := func(key, value string, secret bool) bool {
		return !strings.Contains(key, nonce) && (!strings.Contains(value, nonce) || secret && slices.Contains(l.keys, key) && isMarker(value, nonce))
	}
	switch path.Ext(name) {
	case ".properties":
		for key, value := range properties.Entries(string(data)) {
			if !ok(key, value, true) {
				return false
			}
		}
		return true
	case ".yml":
		dec := yaml.NewDecoder(bytes.NewReader(data))
		for {
			var doc yaml.Node
			if err := dec.Decode(&doc); errors.Is(err, io.EOF) {
				return true
			} else if err != nil || !yamlConfines(&doc, "", false, ok, 0) {
				return false
			}
		}
	case ".toml":
		var doc map[string]any
		return toml.Unmarshal(data, &doc) == nil && tomlConfines(doc, "", false, ok)
	}
	return false
}

// isMarker reports whether value is a marker, apart from the spaces around it, which
// server.properties keeps after a value.
func isMarker(value, nonce string) bool {
	rest, ok := strings.CutPrefix(strings.TrimSpace(value), nonce+"x")
	n, ok2 := strings.CutSuffix(rest, "x")
	_, err := strconv.Atoi(n)
	return ok && ok2 && err == nil
}

// yamlConfines walks a YAML node, the value of key if mapped is set, with what confines checks.
func yamlConfines(n *yaml.Node, key string, mapped bool, ok func(key, value string, secret bool) bool, depth int) bool {
	if depth > 64 {
		return false
	}
	switch n.Kind {
	case yaml.ScalarNode:
		return ok(key, n.Value, mapped)
	case yaml.AliasNode:
		return n.Alias != nil && yamlConfines(n.Alias, "", false, ok, depth+1)
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if !yamlConfines(k, "", false, ok, depth+1) || !yamlConfines(v, k.Value, true, ok, depth+1) {
				return false
			}
		}
		return true
	}
	for _, c := range n.Content { // documents and lists
		if !yamlConfines(c, "", false, ok, depth+1) {
			return false
		}
	}
	return true
}

// tomlConfines walks a decoded TOML value, the value of key if mapped is set, with what
// confines checks.
func tomlConfines(v any, key string, mapped bool, ok func(key, value string, secret bool) bool) bool {
	switch v := v.(type) {
	case string:
		return ok(key, v, mapped)
	case map[string]any:
		for k, x := range v {
			if !ok(k, "", false) || !tomlConfines(x, k, true, ok) {
				return false
			}
		}
	case []any:
		for _, x := range v {
			if !tomlConfines(x, "", false, ok) {
				return false
			}
		}
	}
	return true
}

// Fill puts the secrets of current into the files of restored, e.g. a backup of another
// server whose secrets were hidden, wherever they say Placeholder.
func Fill(current, restored *datadir.Dir) error { return fill(current, restored, false) }

// Take puts the secrets of current into the files of restored in place of all secrets these
// have, e.g. of an archive from elsewhere, or leaves them empty where current has none.
func Take(current, restored *datadir.Dir) error { return fill(current, restored, true) }

func fill(current, restored *datadir.Dir, all bool) error {
	for name := range redacted {
		data, err := restored.ReadFile(name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		now, err := current.ReadOptional(name)
		if err != nil {
			return err
		}
		filled := data
		if all {
			filled = Redact(name, data)
		}
		out, err := Restore(name, filled, now)
		if err != nil { // the archive would show the secrets: it gets none
			out, _ = Restore(name, filled, nil)
		}
		if filled = out; !bytes.Equal(filled, data) {
			if err := restored.WriteFile(name, filled); err != nil {
				return err
			}
		}
	}
	return nil
}

// Censor returns the censor of an archive of the folder dir of a server's data, e.g. "."
// for all of it.
func (f Files) Censor(dir string) datadir.Censor {
	return func(name string) (bool, func([]byte) []byte) {
		name = path.Join(dir, name)
		if f.Hidden(name) || !Redacted(name) {
			return f.Hidden(name), nil
		}
		return false, func(data []byte) []byte { return Redact(name, data) }
	}
}
