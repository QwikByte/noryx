package noryxv1

import (
	"slices"
	"strings"
	"testing"
)

// Files that Noryx writes itself, that hold secrets of the server, and the agent's temporary
// files are off limits for file sets and archives.
func TestNoryxFile(t *testing.T) {
	checkValid(t, NoryxFile,
		[]string{
			"server.properties", "eula.txt", "velocity.toml", "config.yml", "forwarding.secret", ".rcon-cli.env",
			"plugins/floodgate/key.pem", "config/paper-global.yml", "plugins/Geyser-Velocity/config.yml",
			"noryx-filesets.json", "noryx-anything", "banned-players.json", "banned-ips.json",
			".noryx-restore", ".noryx-upload/plugin.jar", "plugins/.noryx-tmp", "world/region/.noryx-x/r.0.0.mca",
		},
		[]string{
			"", "noryx", "plugins/noryx-x.yml", "plugins/banned-words.yml", "config/server.properties", "Server.properties",
			"plugins/Essentials/config.yml", "plugins/LuckPerms/config.yml", "world/level.dat", "noryx.txt", "plugins/x.noryx-",
		})
}

func TestCleanFileSetPath(t *testing.T) {
	for _, tc := range []struct {
		path, clean, want string
	}{
		{"plugins/LuckPerms/config.yml", "plugins/LuckPerms/config.yml", ""},
		{" plugins/LuckPerms/config.yml ", "plugins/LuckPerms/config.yml", ""},
		{"/plugins//LuckPerms/./config.yml", "plugins/LuckPerms/config.yml", ""},
		{"plugins/Essentials/config.yml", "plugins/Essentials/config.yml", ""},
		{"server-icon.png", "server-icon.png", ""},
		{"plugins/x.jar.yml", "plugins/x.jar.yml", ""},
		{strings.Repeat("a", 512), strings.Repeat("a", 512), ""},

		{"", "", "Enter the path of a file"},
		{"/", "", "Enter the path of a file"},
		{"plugins/LuckPerms/", "plugins/LuckPerms", "Enter the path of a file"},
		{"../server.properties", "server.properties", "no valid path"},
		{"plugins/../../etc/passwd", "etc/passwd", "no valid path"},
		{`plugins\LuckPerms\config.yml`, `plugins\LuckPerms\config.yml`, "no valid path"},
		{"plugins/x\n.yml", "plugins/x\n.yml", "no valid path"},
		{"plugins/\xff.yml", "plugins/\xff.yml", "no valid path"},
		{strings.Repeat("a", 513), strings.Repeat("a", 513), "no valid path"},
		{"server.properties", "server.properties", "Noryx manages server.properties"},
		{"/./eula.txt", "eula.txt", "Noryx manages eula.txt"},
		{"plugins/floodgate/key.pem", "plugins/floodgate/key.pem", "Noryx manages"},
		{"noryx-filesets.json", "noryx-filesets.json", "Noryx manages"},
		{"plugins/.noryx-upload/x.yml", "plugins/.noryx-upload/x.yml", "Noryx manages"},
		{"plugins/LuckPerms.jar", "plugins/LuckPerms.jar", "holds code"},
		{"mods/fabric-api.JAR", "mods/fabric-api.JAR", "holds code"},
		{"plugins/pack.zip", "plugins/pack.zip", "holds code"},
		{"Main.class", "Main.class", "holds code"},
	} {
		clean, problem := CleanFileSetPath(tc.path)
		if clean != tc.clean {
			t.Errorf("%q: clean %q, want %q", tc.path, clean, tc.clean)
		}
		checkProblem(t, tc.path, problem, tc.want)
	}
}

func TestFileSetContent(t *testing.T) {
	for _, tc := range []struct {
		name, content, want string
	}{
		{"empty", "", ""},
		{"text", "motd: Welcome {{server.name}}\n", ""},
		{"UTF-8", "Grüße", ""},
		{"as large as allowed", strings.Repeat("a", MaxFileSetFileSize), ""},
		{"too large", strings.Repeat("a", MaxFileSetFileSize+1), "larger than 1 MiB"},
		{"NUL", "a\x00b", "isn't a text file"},
		{"not UTF-8", "Gr\xfc\xdfe", "isn't a text file"},
	} {
		checkProblem(t, tc.name, FileSetContentProblem("x.yml", tc.content), tc.want)
	}
	for _, tc := range []struct {
		name, data, want string
	}{
		{"an image", "\x89PNG\r\n\x1a\n", ""},
		{"text", "PK is no archive", ""},
		{"too large", strings.Repeat("a", MaxFileSetFileSize+1), "larger than 1 MiB"},
		{"a jar", "PK\x03\x04rest", "archive or a program"},
		{"an empty ZIP", "PK\x05\x06", "archive or a program"},
		{"a spanned ZIP", "PK\x07\x08", "archive or a program"},
		{"a Java class", "\xca\xfe\xba\xbe", "archive or a program"},
		{"a Linux program", "\x7fELF\x02\x01", "archive or a program"},
	} {
		checkProblem(t, tc.name, FileSetDataProblem("x.png", []byte(tc.data)), tc.want)
	}
}

// Secrets are a single line of printable characters, so that they can't add lines to a
// configuration.
func TestSecretValueProblem(t *testing.T) {
	for _, value := range []string{"s3cr3t", "pässwörd", "with spaces", `quotes"and'more{}`, strings.Repeat("a", MaxSecretValue)} {
		checkProblem(t, value, SecretValueProblem(value), "")
	}
	for _, value := range []string{"", strings.Repeat("a", MaxSecretValue+1), "a\nb", "a\rb", "a\tb", "a\x00b", "\xff", "a\u00a0b", "a\u2028b"} {
		checkProblem(t, value, SecretValueProblem(value), "single line")
	}
}

func TestFileSetPatterns(t *testing.T) {
	checkValid(t, SecretName.MatchString,
		[]string{"a", "db-pass", "0token", "api_key", strings.Repeat("a", 64)},
		[]string{"", "-a", "_a", "DB", "db pass", "db.pass", "db:pass", strings.Repeat("a", 65)})

	for text, want := range map[string][]string{
		"{{server.name}} on {{server.port}}":           {"server.name", "server.port"},
		"{{network.server}}":                           {"network.server"},
		"{{var:motd}}{{secret:db-pass}}":               {"var:motd", "secret:db-pass"},
		"{{datastore:main.luckperms.password}}":        {"datastore:main.luckperms.password"},
		"{{player}} {{ server.name }} {{servers.x}}":   nil, // used by plugins themselves
		"{{server.na me}} {{secret:a\nb}} {secret:a}}": nil,
	} {
		var got []string
		for _, m := range Placeholder.FindAllStringSubmatch(text, -1) {
			got = append(got, m[1])
		}
		if !slices.Equal(got, want) {
			t.Errorf("placeholders of %q = %q, want %q", text, got, want)
		}
	}

	if m := DatastorePlaceholder.FindStringSubmatch("datastore:main.luckperms.password"); !slices.Equal(m, []string{"datastore:main.luckperms.password", "main", "luckperms", "password"}) {
		t.Errorf("datastore placeholder = %q", m)
	}
	checkValid(t, DatastorePlaceholder.MatchString,
		[]string{"datastore:main.luckperms.host", "datastore:0-db.lp_2.port", "datastore:main.luckperms.database", "datastore:main.luckperms.user"},
		[]string{"datastore:main.luckperms", "datastore:main.luckperms.secret", "datastore:Main.luckperms.host", "datastore:main.2lp.host",
			"datastore:-main.lp.host", "datastore:main.lp.host.x", "datastore:main.lp.password "})
}
