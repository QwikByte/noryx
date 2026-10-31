package fileset

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/QwikByte/noryx/internal/master/datastore"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/tag"
)

const (
	net1    = "nnnnnnnnnnnnnnnnnnnnnnnnnn"
	gone    = "gggggggggggggggggggggggggg"
	server1 = "ssssssssssssssssssssssssss"
)

func TestCheck(t *testing.T) {
	in := Input{
		Name: " LuckPerms ",
		Files: []File{
			{Path: "/plugins/LuckPerms/config.yml", Content: "server: {{network.server}}\npassword: {{secret:db}}\n"},
			{Path: "bukkit.yml", Content: "{{player}} stays as it is, {{var:role}} and {{datastore:main.lp.host}} are filled in"},
			{Path: "server-icon.png", Data: []byte("\x89PNG\r\n\x1a\n")},
		},
		Targets: []Target{
			{Kind: KindTag, Value: " Lobby "}, {Kind: KindTag, Value: "lobby"},
			{Kind: KindNetwork, Value: net1, Role: RoleProxy}, {Kind: KindNetwork, Value: gone, Role: RoleServers},
		},
		Variables: []Variable{{Name: "role", Values: []Value{
			{Kind: KindAll, Scope: "ignored", Value: "game"}, {Kind: KindTag, Scope: "Lobby", Value: " lobby "},
			{Kind: KindNetwork, Scope: gone, Value: "x"}, {Kind: KindServer, Scope: server1, Value: "hub"}, {Kind: KindNetwork, Scope: net1, Value: "proxy"},
		}}},
	}
	exists := func(id string) bool { return id == net1 }
	if err := in.check(exists); err != nil {
		t.Fatal(err)
	}
	if in.Name != "LuckPerms" || in.Files[0].Path != "bukkit.yml" || in.Files[1].Path != "plugins/LuckPerms/config.yml" || in.Files[2].Data == nil {
		t.Errorf("input = %+v", in)
	}
	// Tags are normalized, and targets of deleted networks left out.
	if len(in.Targets) != 2 || in.Targets[0] != (Target{Kind: KindNetwork, Value: net1, Role: RoleProxy}) || in.Targets[1] != (Target{Kind: KindTag, Value: "lobby"}) {
		t.Errorf("targets = %+v", in.Targets)
	}
	// Values are sorted by precedence, those of deleted networks left out.
	if want := []Value{
		{Kind: KindServer, Scope: server1, Value: "hub"}, {Kind: KindNetwork, Scope: net1, Value: "proxy"},
		{Kind: KindTag, Scope: "lobby", Value: "lobby"}, {Kind: KindAll, Value: "game"},
	}; !slices.Equal(in.Variables[0].Values, want) {
		t.Errorf("values = %+v", in.Variables[0].Values)
	}

	for name, f := range map[string]File{
		"managed":             {Path: "spigot.yml"},
		"banned players":      {Path: "banned-players.json"},
		"unknown variable":    {Path: "a.yml", Content: "{{server.ip}}"},
		"bad secret":          {Path: "a.yml", Content: "{{secret:Not Valid}}"},
		"bad variable":        {Path: "a.yml", Content: "{{var:Role}}"},
		"bad database":        {Path: "a.yml", Content: "{{datastore:main.lp}}"},
		"bad database field":  {Path: "a.yml", Content: "{{datastore:main.lp.url}}"},
		"binary as text":      {Path: "a.yml", Content: "\x00"},
		"class":               {Path: "Plugin.CLASS"},
		"folder":              {Path: "plugins/"},
		"text and binary":     {Path: "a.png", Content: "x", Data: []byte("y")},
		"archive by its name": {Path: "a.zip", Data: []byte("data")},
		"archive in disguise": {Path: "a.png", Data: []byte("PK\x03\x04data")},
		"program":             {Path: "a.png", Data: []byte("\x7fELF")},
		"large binary":        {Path: "a.png", Data: make([]byte, 1<<20+1)},
	} {
		bad := Input{Name: "x", Files: []File{f}}
		if err := bad.check(exists); fieldOf(err) != "files" {
			t.Errorf("%s: %v", name, err)
		}
	}
	twice := Input{Name: "x", Files: []File{{Path: "a.yml"}, {Path: "/a.yml"}}}
	if err := twice.check(exists); fieldOf(err) != "files" {
		t.Errorf("a path twice: %v", err)
	}
	for field, bad := range map[string]Input{
		"name":        {Name: " "},
		"description": {Name: "x", Description: strings.Repeat("a", maxDescription+1)},
		"targets":     {Name: "x", Targets: []Target{{Kind: KindTag, Value: "two words"}}},
	} {
		if err := bad.check(exists); fieldOf(err) != field {
			t.Errorf("%s: %v", field, err)
		}
	}

	for name, v := range map[string]Variable{
		"bad name":              {Name: "Role"},
		"line break":            {Name: "role", Values: []Value{{Kind: KindAll, Value: "a\nb: c"}}},
		"quote":                 {Name: "role", Values: []Value{{Kind: KindAll, Value: `a", "b`}}},
		"backslash":             {Name: "role", Values: []Value{{Kind: KindAll, Value: `a\nb`}}},
		"placeholder":           {Name: "role", Values: []Value{{Kind: KindAll, Value: "{{secret:db}}"}}},
		"empty":                 {Name: "role", Values: []Value{{Kind: KindAll, Value: " "}}},
		"long":                  {Name: "role", Values: []Value{{Kind: KindAll, Value: strings.Repeat("a", 129)}}},
		"unknown kind":          {Name: "role", Values: []Value{{Kind: "node", Scope: net1, Value: "a"}}},
		"invalid server":        {Name: "role", Values: []Value{{Kind: KindServer, Scope: "../x", Value: "a"}}},
		"twice for the servers": {Name: "role", Values: []Value{{Kind: KindTag, Scope: "a", Value: "1"}, {Kind: KindTag, Scope: "A", Value: "2"}}},
	} {
		bad := Input{Name: "x", Variables: []Variable{v}}
		if err := bad.check(exists); fieldOf(err) != "variables" {
			t.Errorf("variable with %s: %v", name, err)
		}
	}
	if err := (&Input{Name: "x", Variables: []Variable{{Name: "a"}, {Name: "a"}}}).check(exists); err == nil {
		t.Error("a variable twice was accepted")
	}
	if err := (&Input{Name: "x", Variables: []Variable{{Name: "motd", Values: []Value{{Kind: KindAll, Value: "<green>Welcome & have fun: #1!"}}}}}).check(exists); err != nil {
		t.Errorf("a value with punctuation: %v", err)
	}
}

// fieldOf returns the field of the request that err is about, or "".
func fieldOf(err error) string {
	var apiErr *httpapi.Error
	if !errors.As(err, &apiErr) {
		return ""
	}
	return apiErr.Field
}

func TestRender(t *testing.T) {
	set := Set{ID: "set"}
	files := []File{
		{Path: "a.yml", Content: "name: {{server.name}} ({{server.id}}:{{server.port}}) in {{network.server}}\npassword: {{secret:db}}\n"},
	}
	m := member{Server: tag.Server{NodeID: "n1", ServerID: "s1"}, Name: "Lobby 1", Port: 25565, Network: "lobby-1"}
	r, err := render(set, files, map[string]string{"db": "pw", "other": "unused"}, m)
	if err != nil {
		t.Fatal(err)
	}
	// The master fills in the variables, the agent the secrets, of which it gets those used.
	if got := r.file("a.yml").GetContent(); got != "name: Lobby 1 (s1:25565) in lobby-1\npassword: {{secret:db}}\n" {
		t.Errorf("rendered = %q", got)
	}
	if len(r.secrets) != 1 || r.secrets["secret:db"] != "pw" || r.passwords() {
		t.Errorf("secrets = %v", r.secrets)
	}
	// The revision of files without the new placeholders stays as it was, so that servers
	// don't seem outdated after an update of the master.
	if r.revision != "2e5e675c2bbf695cb990e062448c51bda9c71e0409c3227bf31de6a6cf99fe50" {
		t.Errorf("revision = %s", r.revision)
	}

	// Another secret or another server is another revision; the same input the same.
	same, _ := render(set, files, map[string]string{"db": "pw"}, m)
	rotated, _ := render(set, files, map[string]string{"db": "new"}, m)
	renamed, _ := render(set, files, map[string]string{"db": "pw"}, member{Server: m.Server, Name: "Lobby 2", Port: 25565, Network: "lobby-1"})
	if same.revision != r.revision || rotated.revision == r.revision || renamed.revision == r.revision {
		t.Error("revisions don't tell the states apart")
	}

	// A missing secret, or a network variable on a server outside networks, is a problem.
	if _, err := render(set, files, nil, m); err == nil || !strings.Contains(err.Error(), "db") {
		t.Errorf("missing secret: %v", err)
	}
	m.Network = ""
	if _, err := render(set, files, map[string]string{"db": "pw"}, m); err == nil {
		t.Error("network.server outside of a network was filled in")
	}
}

func TestRenderVariables(t *testing.T) {
	set := Set{ID: "set", Variables: []Variable{
		{Name: "role", Values: []Value{
			{Kind: KindServer, Scope: server1, Value: "hub"}, {Kind: KindNetwork, Scope: net1, Value: "network"},
			{Kind: KindTag, Scope: "a", Value: "tag a"}, {Kind: KindTag, Scope: "b", Value: "tag b"},
		}},
		{Name: "motd", Values: []Value{{Kind: KindAll, Value: "Welcome"}}},
	}}
	files := []File{{Path: "a.yml", Content: "role: {{var:role}}, motd: {{var:motd}}"}}
	for name, c := range map[string]struct {
		m    member
		want string
	}{
		"server over network and tags": {member{Server: tag.Server{ServerID: server1}, NetworkID: net1, Tags: []string{"a"}}, "role: hub, motd: Welcome"},
		"network over tags":            {member{Server: tag.Server{ServerID: "other"}, NetworkID: net1, Tags: []string{"a"}}, "role: network, motd: Welcome"},
		"first tag alphabetically":     {member{Tags: []string{"b", "a"}}, "role: tag a, motd: Welcome"},
		"another tag":                  {member{Tags: []string{"b"}}, "role: tag b, motd: Welcome"},
	} {
		r, err := render(set, files, nil, c.m)
		if got := r.file("a.yml").GetContent(); err != nil || got != c.want {
			t.Errorf("%s: %q, %v", name, got, err)
		}
	}
	// A server without a value, or a variable the set doesn't have, is a problem.
	if _, err := render(set, files, nil, member{Name: "Lobby", Tags: []string{"c"}}); err == nil || !strings.Contains(err.Error(), "role") {
		t.Errorf("without a value: %v", err)
	}
	if _, err := render(set, []File{{Path: "a.yml", Content: "{{var:nope}}"}}, nil, member{}); err == nil {
		t.Error("an unknown variable was filled in")
	}
	// Values that grow the files beyond the limits of a set are a problem too.
	huge := []File{{Path: "a.yml", Content: strings.Repeat("{{var:motd}}", 50_000)}}
	set.Variables[1].Values[0].Value = strings.Repeat("x", 128)
	if _, err := render(set, huge, nil, member{}); err == nil {
		t.Error("a file larger than 1 MiB once filled in was accepted")
	}
}

func TestRenderDatastores(t *testing.T) {
	files := []File{
		{Path: "lp.yml", Content: "address: {{datastore:main.lp.host}}:{{datastore:main.lp.port}}\ndatabase: {{datastore:main.lp.database}}\n" +
			"username: {{datastore:main.lp.user}}\npassword: {{datastore:main.lp.password}}\n"},
		{Path: "server-icon.png", Data: []byte("{{datastore:main.lp.password}}")},
	}
	m := member{Name: "Lobby", NetworkID: net1, connect: func(name, database string) (datastore.Connection, error) {
		if name != "main" || database != "lp" {
			return datastore.Connection{}, httpapi.Errorf(409, "The network of the server has no datastore %s.", name)
		}
		return datastore.Connection{Host: "noryx-db-x", Port: 3306, Database: "lp", Password: "s3cr3t"}, nil
	}}
	r, err := render(Set{ID: "set"}, files, nil, m)
	if err != nil {
		t.Fatal(err)
	}
	// The master fills in where the server reaches the database; the password only the agent.
	if got := r.file("lp.yml").GetContent(); got != "address: noryx-db-x:3306\ndatabase: lp\nusername: lp\npassword: {{datastore:main.lp.password}}\n" {
		t.Errorf("rendered = %q", got)
	}
	if !r.passwords() || r.secrets["datastore:main.lp.password"] != "s3cr3t" || len(r.secrets) != 1 {
		t.Errorf("secrets = %v", r.secrets)
	}
	// Binary files stay as they are.
	if got := r.file("server-icon.png"); string(got.GetData()) != "{{datastore:main.lp.password}}" || got.GetContent() != "" {
		t.Errorf("binary file = %+v", got)
	}
	// A rotated password is another revision.
	rotated := m
	rotated.connect = func(string, string) (datastore.Connection, error) {
		return datastore.Connection{Host: "noryx-db-x", Port: 3306, Database: "lp", Password: "n3w"}, nil
	}
	if again, _ := render(Set{ID: "set"}, files, nil, rotated); again.revision == r.revision {
		t.Error("the revision doesn't tell the passwords apart")
	}

	// A datastore the server can't use, or one outside networks, is a problem.
	if _, err := render(Set{ID: "set"}, []File{{Path: "a.yml", Content: "{{datastore:other.lp.host}}"}}, nil, m); err == nil || !strings.Contains(err.Error(), "no datastore other") {
		t.Errorf("unknown datastore: %v", err)
	}
	m.NetworkID = ""
	if _, err := render(Set{ID: "set"}, files, nil, m); err == nil {
		t.Error("a server outside networks got a database")
	}
}

func TestPasswords(t *testing.T) {
	before := []File{
		{Path: "lp.yml", Content: "password: {{datastore:main.lp.password}}\nhost: {{datastore:main.lp.host}}"},
		{Path: "other.yml", Content: "x"},
	}
	// Unchanged files with passwords, and other placeholders, need no permission.
	if got := passwords(before, before); len(got) != 0 {
		t.Errorf("unchanged = %v", got)
	}
	changed := slices.Clone(before)
	changed[0].Content += "\nurl: https://example.com/?{{datastore:main.lp.password}}{{datastore:main.bans.password}}"
	if got := passwords(changed, before); !slices.Equal(got, []string{"{{datastore:main.bans.password}}", "{{datastore:main.lp.password}}"}) {
		t.Errorf("changed = %v", got)
	}
	moved := slices.Clone(before)
	moved[0].Path = "plugins/x.yml"
	if got := passwords(moved, before); len(got) != 1 {
		t.Errorf("moved = %v", got)
	}
	if got := passwords(before, nil); len(got) != 1 {
		t.Errorf("new = %v", got)
	}
}
