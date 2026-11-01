package secrets

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/QwikByte/noryx/internal/agent/datadir"
)

func TestRedactAndRestore(t *testing.T) {
	for _, tc := range []struct{ name, data, redacted string }{
		{"server.properties", "motd=hi\nrcon.password=s3cret\r\nrcon.port=25575\nmanagement-server-secret = abc def \n",
			"motd=hi\nrcon.password=<hidden>\r\nrcon.port=25575\nmanagement-server-secret = <hidden> \n"},
		{"server.properties", "rcon.password=\n", "rcon.password=\n"}, // nothing to hide
		{"config/paper-global.yml", "proxies:\n  velocity:\n    enabled: true\n    secret: 'F0rward1ngS3cret'\n",
			"proxies:\n  velocity:\n    enabled: true\n    secret: <hidden>\n"},
		{"config/FabricProxy-Lite.toml", "hackOnlineMode = true\nsecret = 'F0rward1ngS3cret'\n", "hackOnlineMode = true\nsecret = <hidden>\n"},
		{"config/proxy-compatible-forge.toml", "[forwarding]\nenabled = true\nsecret = 'F0rward1ngS3cret'\n", "[forwarding]\nenabled = true\nsecret = <hidden>\n"},
		{"plugins/Example/config.yml", "secret: kept\n", "secret: kept\n"},
		{"plugins/Geyser-Velocity/config.yml", "bedrock:\n  port: 19132\n  signaling:\n    nxs:\n      token: 'abc'\n", "bedrock:\n  port: 19132\n  signaling:\n    nxs:\n      token: <hidden>\n"},
	} {
		if got := string(Redact(tc.name, []byte(tc.data))); got != tc.redacted {
			t.Errorf("Redact(%s) = %q, want %q", tc.name, got, tc.redacted)
		}
		// Saving what the panel showed keeps the secrets.
		if got, err := Restore(tc.name, []byte(tc.redacted), []byte(tc.data)); err != nil || string(got) != tc.data {
			t.Errorf("Restore(%s) = %q, %v, want %q", tc.name, got, err, tc.data)
		}
	}
	// A new secret is taken as is, and a placeholder without a secret never becomes one.
	got, err := Restore("server.properties", []byte("rcon.password=n3w\nmanagement-server-secret=<hidden>\n"), []byte("rcon.password=old\n"))
	if want := "rcon.password=n3w\nmanagement-server-secret=\n"; err != nil || string(got) != want {
		t.Errorf("Restore = %q, %v, want %q", got, err, want)
	}
	// Lines with several secrets of the same key each get theirs.
	many := strings.Repeat("rcon.password=<hidden>\n", 12)
	if got, err := Restore("server.properties", []byte(many), []byte("rcon.password=s3cret\n")); err != nil || string(got) != strings.ReplaceAll(many, Placeholder, "s3cret") {
		t.Errorf("Restore = %q, %v", got, err)
	}
}

// A secret is never filled in where the format of its file reads it as part of another
// value, which the panel or the players see, e.g. the message of the day.
func TestRestoreRefusesExposedSecrets(t *testing.T) {
	for _, tc := range []struct{ name, data, current string }{
		{"server.properties", "motd=\\\nrcon.password=<hidden>\n", "rcon.password=s3cret\n"},
		{"server.properties", "motd=a\\\n  \\\n rcon.password=<hidden>\n", "rcon.password=s3cret\n"},
		{"server.properties", "=\\\nrcon.password=<hidden>\n", "rcon.password=s3cret\n"},
		{"config/paper-global.yml", "messages:\n  no-permission: |\n    secret: <hidden>\n", "secret: 'F0rward'\n"},
		{"config/paper-global.yml", "messages:\n  no-permission: \"a\n  secret: <hidden>\n  \"\n", "secret: 'F0rward'\n"},
		{"config/paper-global.yml", "secret: <hidden>\nmotd: \"unclosed\n", "secret: 'F0rward'\n"}, // not YAML
		{"plugins/Geyser-Velocity/config.yml", "bedrock:\n  motd1: >\n    password: <hidden>\n", "password: 'abc'\n"},
		{"config/FabricProxy-Lite.toml", "motd = \"\"\"\nsecret = <hidden>\n\"\"\"\n", "secret = 'F0rward'\n"},
		{"config/FabricProxy-Lite.toml", "names = [\nsecret = <hidden>\n]\n", "secret = 'F0rward'\n"},
	} {
		got, err := Restore(tc.name, []byte(tc.data), []byte(tc.current))
		if !errors.Is(err, ErrExposed) {
			t.Errorf("Restore(%s, %q) = %q, %v", tc.name, tc.data, got, err)
		}
	}
	// Secrets in nested mappings stay secrets of their own.
	yml := "proxies:\n  velocity:\n    secret: <hidden>\nother:\n  secret: <hidden>\n"
	if got, err := Restore("config/paper-global.yml", []byte(yml), []byte("secret: 'F0rward'\n")); err != nil || strings.Count(string(got), "'F0rward'") != 2 {
		t.Errorf("Restore = %q, %v", got, err)
	}
}

func TestFiles(t *testing.T) {
	for name, want := range map[string][3]bool{ // hidden, redacted, holds secrets
		".rcon-cli.env":                        {true, false, true},
		"forwarding.secret":                    {true, false, true},
		"server.properties":                    {false, true, true},
		"config":                               {false, false, true},
		"config/paper-global.yml":              {false, true, true},
		"plugins":                              {false, false, true}, // Floodgate's key
		"plugins/floodgate/key.pem":            {true, false, true},
		"plugins/Geyser-BungeeCord/config.yml": {false, true, true},
		"plugins/Geyser-Velocity/saved-auth-chains.json":      {true, false, true},
		"plugins/Geyser-BungeeCord/saved-refresh-tokens.json": {true, false, true},
		"plugins/Example":         {false, false, false},
		"world/server.properties": {false, false, false},
	} {
		if got := [3]bool{Hidden(name), Redacted(name), len(With().Under(name)) > 0}; got != want {
			t.Errorf("%s: hidden, redacted, holds secrets = %v, want %v", name, got, want)
		}
	}
	if got := slices.Sorted(slices.Values(With().Under("config"))); !slices.Equal(got, []string{"config/FabricProxy-Lite.toml", "config/paper-global.yml", "config/proxy-compatible-forge.toml"}) {
		t.Errorf("Under(config) = %q", got)
	}

	// Files the agent marked on a server are hidden there, in archives too.
	marked := With("plugins/LuckPerms/config.yml")
	if !marked.Hidden("plugins/LuckPerms/config.yml") || Hidden("plugins/LuckPerms/config.yml") || !slices.Contains(marked.Under("plugins/LuckPerms"), "plugins/LuckPerms/config.yml") {
		t.Error("a marked file isn't hidden")
	}
	if omit, _ := marked.Censor("plugins")("LuckPerms/config.yml"); !omit {
		t.Error("an archive keeps a marked file")
	}
	if omit, edit := marked.Censor(".")("server.properties"); omit || edit == nil {
		t.Error("an archive doesn't redact server.properties")
	}
}

// Fill only fills in placeholders, Take replaces all secrets of an archive from elsewhere.
func TestFillAndTake(t *testing.T) {
	for _, tc := range []struct {
		take             bool
		restored, filled string
	}{
		{false, "motd=a\nrcon.password=<hidden>\n", "motd=a\nrcon.password=ours\n"},
		{false, "motd=a\nrcon.password=theirs\n", "motd=a\nrcon.password=theirs\n"},
		{true, "motd=a\nrcon.password=theirs\nmanagement-server-secret=x\n", "motd=a\nrcon.password=ours\nmanagement-server-secret=\n"},
	} {
		current, restored := dir(t), dir(t)
		err := errors.Join(current.WriteFile("server.properties", []byte("rcon.password=ours\n")), restored.WriteFile("server.properties", []byte(tc.restored)))
		if err != nil {
			t.Fatal(err)
		}
		fill := Fill
		if tc.take {
			fill = Take
		}
		if err := fill(current, restored); err != nil {
			t.Fatal(err)
		}
		if got, _ := restored.ReadFile("server.properties"); string(got) != tc.filled {
			t.Errorf("take %v: %q, want %q", tc.take, got, tc.filled)
		}
	}
}

func dir(t *testing.T) *datadir.Dir {
	d, err := datadir.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}
