package server

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/datadir"
	"github.com/QwikByte/noryx/internal/agent/runtime"
)

func TestPlainRemovesFormatting(t *testing.T) {
	for in, want := range map[string]string{
		"\x1b[0;32m[12:00:00 INFO]: Done (3.2s)!\x1b[m\r": "[12:00:00 INFO]: Done (3.2s)!",
		"§6There are §c2§6 of a max of §A20§r players":    "There are 2 of a max of 20 players",
		"plain line":   "plain line",
		"caf\xe9 \xff": "caf� �",
		// Other sequences, e.g. that write the clipboard or the title, lose their control characters.
		"\x1b]52;c;aGk=\a\x1b]0;title\a text\x1bc\tend\nnext": "]52;c;aGk=]0;title textc\tend\nnext",
	} {
		if got := plain(in); got != want {
			t.Errorf("plain(%q) = %q, want %q", in, got, want)
		}
	}
}

// Colours of terminals and Minecraft's codes both become Minecraft's codes for the panel.
func TestFormattedKeepsColours(t *testing.T) {
	for in, want := range map[string]string{
		"plain line":           "",
		"\x1b[2K\rplain again": "",
		// Paper writes Minecraft's light colours as bright ones and resets after each line.
		"\x1b[0;32;1m[12:00:00 INFO]: Done\x1b[m\r":               "§a[12:00:00 INFO]: Done",
		"\x1b[0;31m[ERROR]\x1b[m: crashed":                        "§4[ERROR]§r: crashed",
		"\x1b[1m\x1b[33mwarn\x1b[22m ok\x1b[39m.":                 "§ewarn§6 ok§r.",
		"\x1b[21;4mbold\x1b[24m only":                             "§l§nbold§r§l only",
		"\x1b[38;5;203mred\x1b[48;5;21m \x1b[38;2;85;85;255mblue": "§cred §9blue",
		"§6There are §c2§6 of §lmax§r players":                    "§6There are §c2§6 of §lmax§r players",
		"§x§F§F§5§5§5§5Hex §kmagic":                               "§cHex magic",
		"§zno code":                                               "",
	} {
		if got := formatted(in); got != want {
			t.Errorf("formatted(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCheckSettings(t *testing.T) {
	valid := runtime.Spec{Name: "Lobby", Type: noryxv1.ServerType_SERVER_TYPE_PAPER, Version: "LATEST", MemoryMB: 2048, Port: 25565}
	tests := []struct {
		name   string
		change func(*runtime.Spec)
		ok     bool
	}{
		{"defaults", func(*runtime.Spec) {}, true},
		{"all settings", func(s *runtime.Spec) {
			s.Java, s.AikarFlags, s.CPUMillis = "17", true, 2500
			s.RestartPolicy = noryxv1.RestartPolicy_RESTART_POLICY_ON_CRASH
			s.JVMOptions = []string{
				"-Dfile.encoding=UTF-8", "-XX:+UseZGC", "-XX:+HeapDumpOnOutOfMemoryError", "-Dcom.example.agent=x", "-Djava.awt.headless=true",
				"-Dlog4j2.formatMsgNoLookups=true", "-XX:+PrintFlagsFinal", "--add-modules=jdk.incubator.vector", "--enable-native-access=ALL-UNNAMED",
				"-Dusing.aikars.flags=https://mcflags.emc.gs", "-Xss4M",
			}
		}, true},
		{"unknown Java", func(s *runtime.Spec) { s.Java = "22" }, false},
		{"Java for a proxy", func(s *runtime.Spec) { s.Type, s.Java = noryxv1.ServerType_SERVER_TYPE_VELOCITY, "21" }, false},
		{"option with a space", func(s *runtime.Spec) { s.JVMOptions = []string{"-Dx=a b"} }, false},
		{"shell syntax", func(s *runtime.Spec) { s.JVMOptions = []string{"-Dx=$(id)"} }, false},
		{"glob", func(s *runtime.Spec) { s.JVMOptions = []string{"-cp*"} }, false},
		{"no dash", func(s *runtime.Spec) { s.JVMOptions = []string{"evil.jar"} }, false},
		{"memory override", func(s *runtime.Spec) { s.JVMOptions = []string{"-Xmx64G"} }, false},
		{"more CPUs than the node", func(s *runtime.Spec) { s.CPUMillis = 8001 }, false},
		{"tiny CPU limit", func(s *runtime.Spec) { s.CPUMillis = 50 }, false},
		{"unknown restart policy", func(s *runtime.Spec) { s.RestartPolicy = 9 }, false},
		{"loader version", func(s *runtime.Spec) { s.Type, s.LoaderVersion = noryxv1.ServerType_SERVER_TYPE_FORGE, "1.20.1-47.3.0" }, true},
		{"loader version without loader", func(s *runtime.Spec) { s.LoaderVersion = "0.16.10" }, false},
		{"loader version with shell syntax", func(s *runtime.Spec) { s.Type, s.LoaderVersion = noryxv1.ServerType_SERVER_TYPE_FABRIC, "$(id)" }, false},
		{"longest stop timeout", func(s *runtime.Spec) { s.StopTimeout = 600 }, true},
		{"shortest stop timeout", func(s *runtime.Spec) { s.StopTimeout = 30 }, true},
		{"too short a stop timeout", func(s *runtime.Spec) { s.StopTimeout = 29 }, false},
		{"too long a stop timeout", func(s *runtime.Spec) { s.StopTimeout = 601 }, false},
		{"time zone", func(s *runtime.Spec) { s.TimeZone = "America/Argentina/Buenos_Aires" }, true},
		{"time zone with an offset", func(s *runtime.Spec) { s.TimeZone = "Etc/GMT+5" }, true},
		{"UTC", func(s *runtime.Spec) { s.TimeZone = "UTC" }, true},
		{"unknown time zone", func(s *runtime.Spec) { s.TimeZone = "Mars/Olympus_Mons" }, false},
		{"time zone of the agent", func(s *runtime.Spec) { s.TimeZone = "Local" }, false},
		{"time zone file outside the database", func(s *runtime.Spec) { s.TimeZone = "../../../etc/localtime" }, false},
		{"time zone with shell syntax", func(s *runtime.Spec) { s.TimeZone = "Europe/Berlin;id" }, false},
		{"Bedrock", func(s *runtime.Spec) {
			s.Type, s.Port, s.MaxPlayers = noryxv1.ServerType_SERVER_TYPE_BEDROCK, 19132, 30
		}, true},
		{"Bedrock at a version", func(s *runtime.Spec) { s.Type, s.Version = noryxv1.ServerType_SERVER_TYPE_BEDROCK, "1.26.61.1" }, true},
		{"Bedrock preview", func(s *runtime.Spec) { s.Type, s.Version = noryxv1.ServerType_SERVER_TYPE_BEDROCK, "PREVIEW" }, true},
		{"Bedrock at a version of Java", func(s *runtime.Spec) { s.Type, s.Version = noryxv1.ServerType_SERVER_TYPE_BEDROCK, "1.21.4" }, false},
		{"Bedrock with its binary in the data", func(s *runtime.Spec) { s.Type, s.Version = noryxv1.ServerType_SERVER_TYPE_BEDROCK, "EXISTING" }, false},
		{"Bedrock with Java", func(s *runtime.Spec) { s.Type, s.Java = noryxv1.ServerType_SERVER_TYPE_BEDROCK, "21" }, false},
		{"Bedrock with Aikar's flags", func(s *runtime.Spec) { s.Type, s.AikarFlags = noryxv1.ServerType_SERVER_TYPE_BEDROCK, true }, false},
		{"Bedrock with JVM options", func(s *runtime.Spec) {
			s.Type, s.JVMOptions = noryxv1.ServerType_SERVER_TYPE_BEDROCK, []string{"-Xss4M"}
		}, false},
		{"Bedrock with an initial heap", func(s *runtime.Spec) { s.Type, s.InitMemoryMB = noryxv1.ServerType_SERVER_TYPE_BEDROCK, 1024 }, false},
		{"Bedrock with an extra port", func(s *runtime.Spec) {
			s.Type, s.ExtraPorts = noryxv1.ServerType_SERVER_TYPE_BEDROCK, []runtime.ExtraPort{{Port: 8100, Protocol: "tcp"}}
		}, false},
		{"Bedrock with too many players", func(s *runtime.Spec) { s.Type, s.MaxPlayers = noryxv1.ServerType_SERVER_TYPE_BEDROCK, 101 }, false},
		{"Bedrock with ports beyond 65535", func(s *runtime.Spec) { s.Type, s.Port = noryxv1.ServerType_SERVER_TYPE_BEDROCK, 65530 }, false},
		{"players of a Paper server", func(s *runtime.Spec) { s.MaxPlayers = 20 }, false},
	}
	for _, tt := range tests {
		spec := valid
		tt.change(&spec)
		if msg := checkSettings(spec, 8); (msg == "") != tt.ok {
			t.Errorf("%s: checkSettings() = %q, want ok = %v", tt.name, msg, tt.ok)
		}
	}
	// Changing settings can't run code: no commands, agents or debugging and management ports,
	// and no options, classes, libraries or configurations loaded from files or URLs.
	for _, option := range []string{
		"-javaagent:/data/agent.jar", "-agentpath:/data/libx.so", "-agentlib:jdwp=transport=dt_socket,server=y,address=5005",
		"-Xrunjdwp:server=y", "-Xbootclasspath/a:/data/x.jar", "-XX:OnOutOfMemoryError=/data/x.sh", "-XX:OnError=/data/x.sh",
		"-Dcom.sun.management.jmxremote.port=9010", "-Dcom.sun.management.config.file=/data/x",
		"-XX:VMOptionsFile=/data/x", "-XX:Flags=/data/x", "-XX:SharedArchiveFile=/data/x.jsa", "-XX:AOTCache=/data/x.aot",
		"-XX:+EnableJVMCI", "-XX:JVMCILibPath=/data", "-XX:CRaCRestoreFrom=/data/cr",
		"-Dlog4j2.configurationFile=https://example.com/x.xml", "-Dlog4j.configurationFile=/data/x.xml", "-DLOG4J2_configurationFile=/data/x",
		"-Dlogback.configurationFile=/data/x.xml", "-Djava.system.class.loader=x.Loader", "-Djava.library.path=/data",
		"-Djava.util.logging.config.class=x.Config", "-Djna.library.path=/data", "-Djdk.module.patch.0=java.base=/data/x",
		"-cp", "--class-path=/data/x.jar", "--module-path=/data/mods", "--patch-module=java.base=/data/x", "-jar",
	} {
		spec := valid
		spec.JVMOptions = []string{option}
		if checkSettings(spec, 8) == "" {
			t.Errorf("%s accepted", option)
		}
	}
}

func TestNetworkOf(t *testing.T) {
	const id, secret = "abcdefghijklmnopqrstuvwxyz", "S3cretS3cretS3cret"
	backends := []*noryxv1.NetworkBackend{
		{Name: "lobby", Target: &noryxv1.NetworkBackend_ServerId{ServerId: "bcdefghijklmnopqrstuvwxyz2"}},
		{Name: "survival", Target: &noryxv1.NetworkBackend_Address{Address: "203.0.113.7:25566"}, Restricted: true, Motd: "&aSurvival"},
	}
	valid := func() *noryxv1.ConfigureNetworkRequest {
		return &noryxv1.ConfigureNetworkRequest{
			Id: id, ForwardingSecret: secret, Forwarding: noryxv1.Forwarding_FORWARDING_MODERN, Backends: backends,
			Try: []string{"lobby", "survival"}, ForcedHosts: []*noryxv1.ForcedHost{{Host: "survival.example.com", Servers: []string{"survival"}}},
		}
	}
	tests := []struct {
		name   string
		change func(*noryxv1.ConfigureNetworkRequest)
		ok     bool
	}{
		{"valid", func(*noryxv1.ConfigureNetworkRequest) {}, true},
		{"legacy without secret", func(r *noryxv1.ConfigureNetworkRequest) {
			r.Forwarding, r.ForwardingSecret = noryxv1.Forwarding_FORWARDING_LEGACY, ""
		}, true},
		{"modern without secret", func(r *noryxv1.ConfigureNetworkRequest) { r.ForwardingSecret = "" }, false},
		{"unknown forwarding", func(r *noryxv1.ConfigureNetworkRequest) { r.Forwarding = 9 }, false},
		{"no forwarding chosen", func(r *noryxv1.ConfigureNetworkRequest) { r.Forwarding = noryxv1.Forwarding_FORWARDING_UNSPECIFIED }, false},
		{"try names an unknown server", func(r *noryxv1.ConfigureNetworkRequest) { r.Try = []string{"nope"} }, false},
		{"try names a server twice", func(r *noryxv1.ConfigureNetworkRequest) { r.Try = []string{"lobby", "lobby"} }, false},
		{"forced host with a port", func(r *noryxv1.ConfigureNetworkRequest) { r.ForcedHosts[0].Host = "survival.example.com:25565" }, false},
		{"forced host in capitals", func(r *noryxv1.ConfigureNetworkRequest) { r.ForcedHosts[0].Host = "Survival.example.com" }, false},
		{"forced host without servers", func(r *noryxv1.ConfigureNetworkRequest) { r.ForcedHosts[0].Servers = nil }, false},
		{"duplicate forced host", func(r *noryxv1.ConfigureNetworkRequest) { r.ForcedHosts = append(r.ForcedHosts, r.ForcedHosts[0]) }, false},
		{"MOTD with control characters", func(r *noryxv1.ConfigureNetworkRequest) {
			r.Backends = []*noryxv1.NetworkBackend{{Name: "lobby", Target: backends[0].Target, Motd: "\x1b[31m"}}
			r.Try, r.ForcedHosts = []string{"lobby"}, nil
		}, false},
	}
	for _, tt := range tests {
		req := valid()
		tt.change(req)
		if _, msg := networkOf(req); (msg == "") != tt.ok {
			t.Errorf("%s: networkOf() = %q, want ok = %v", tt.name, msg, tt.ok)
		}
	}

	if n, _ := networkOf(&noryxv1.ConfigureNetworkRequest{Id: id, Forwarding: noryxv1.Forwarding_FORWARDING_NONE, ProxyOnNode: true}); n.Forwarding != runtime.ForwardingNone || n.ProxyOnNode {
		t.Fatalf("leaving a network = %+v", n)
	}
}

// slowRuntime creates servers slowly, as Docker does while it pulls an image, and keeps their
// data in dir.
type slowRuntime struct {
	runtime.Runtime
	dir     string
	mu      sync.Mutex
	servers []runtime.Server
}

func (r *slowRuntime) Info(context.Context) (runtime.Info, error) { return runtime.Info{CPUs: 4}, nil }

func (r *slowRuntime) ListDatastores(context.Context) ([]runtime.Datastore, error) { return nil, nil }

func (r *slowRuntime) List(context.Context) ([]runtime.Server, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.servers), nil
}

func (r *slowRuntime) Create(_ context.Context, spec runtime.Spec) error {
	time.Sleep(50 * time.Millisecond)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.servers = append(r.servers, runtime.Server{Spec: spec})
	return nil
}

func (r *slowRuntime) Update(_ context.Context, spec runtime.Spec) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	i := slices.IndexFunc(r.servers, func(srv runtime.Server) bool { return srv.ID == spec.ID })
	if i < 0 {
		return runtime.ErrNotFound
	}
	r.servers[i].Spec = spec
	return nil
}

func (r *slowRuntime) Data(_ context.Context, id string) (*datadir.Dir, error) {
	path := filepath.Join(r.dir, id)
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, err
	}
	return datadir.Open(path)
}

// Of concurrent requests for the same port, e.g. after a double click, only one creates a
// server.
func TestCreateServerReservesThePort(t *testing.T) {
	rt := &slowRuntime{}
	s := NewService(rt, nil, nil)
	errs := make(chan error, 3)
	for range cap(errs) {
		go func() {
			_, err := s.CreateServer(t.Context(), &noryxv1.CreateServerRequest{
				Name: "Lobby", Type: noryxv1.ServerType_SERVER_TYPE_PAPER, MemoryMb: 1024, Port: 25565, AcceptEula: true,
			})
			errs <- err
		}()
	}
	var refused int
	for range cap(errs) {
		if err := <-errs; status.Code(err) == codes.AlreadyExists {
			refused++
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if servers, _ := rt.List(t.Context()); refused != 2 || len(servers) != 1 {
		t.Fatalf("%d servers created, %d refused", len(servers), refused)
	}
	// Once the server exists, the port is free to reserve for it again, e.g. to change it.
	if len(s.reserved) != 0 {
		t.Fatalf("ports still reserved: %v", s.reserved)
	}
}

// Extra ports are ports of plugins from 1024 on that neither the server nor its container uses
// otherwise, each listed once, with a label of plain text.
func TestCheckExtraPorts(t *testing.T) {
	port := func(n uint32, protocol, label string) runtime.ExtraPort {
		return runtime.ExtraPort{Port: n, Protocol: protocol, Label: label}
	}
	six := make([]runtime.ExtraPort, 6)
	for i := range six {
		six[i] = port(8101+uint32(i), "tcp", "")
	}
	tests := []struct {
		name  string
		ports []runtime.ExtraPort
		ok    bool
	}{
		{"none", nil, true},
		{"map, voice chat and metrics", []runtime.ExtraPort{
			port(8100, "tcp", "Map"), port(24454, "udp", "Voice chat"), port(8100, "udp", ""), port(9225, "tcp", "Métriques ✓"),
		}, true},
		{"five", six[:5], true},
		{"six", six, false},
		{"privileged", []runtime.ExtraPort{port(80, "tcp", "")}, false},
		{"beyond 65535", []runtime.ExtraPort{port(65536, "tcp", "")}, false},
		{"another protocol", []runtime.ExtraPort{port(8100, "sctp", "")}, false},
		{"protocol in capitals", []runtime.ExtraPort{port(8100, "TCP", "")}, false},
		{"the server's port", []runtime.ExtraPort{port(25570, "udp", "")}, false},
		{"the Bedrock port", []runtime.ExtraPort{port(19132, "udp", "")}, false},
		{"the game port inside the container", []runtime.ExtraPort{port(25565, "tcp", "")}, false},
		{"the console port", []runtime.ExtraPort{port(25575, "tcp", "")}, false},
		{"BungeeCord's port", []runtime.ExtraPort{port(25577, "udp", "")}, false},
		{"twice", []runtime.ExtraPort{port(8100, "tcp", "Map"), port(8100, "tcp", "Map 2")}, false},
		{"label with a line break", []runtime.ExtraPort{port(8100, "tcp", "Map\nfake")}, false},
		{"label turning text around", []runtime.ExtraPort{port(8100, "tcp", "\u202epaM")}, false},
		{"long label", []runtime.ExtraPort{port(8100, "tcp", strings.Repeat("ä", 33))}, false},
	}
	for _, tt := range tests {
		spec := runtime.Spec{
			Name: "Proxy", Type: noryxv1.ServerType_SERVER_TYPE_VELOCITY, Version: "LATEST", MemoryMB: 1024, Port: 25570, BedrockPort: 19132, ExtraPorts: tt.ports,
		}
		if msg := checkSettings(spec, 8); (msg == "") != tt.ok {
			t.Errorf("%s: checkSettings() = %q, want ok = %v", tt.name, msg, tt.ok)
		}
	}
}

// A server can't take the port of another one as its own or as an extra port, whether the other
// uses it as its own or as an extra port.
func TestExtraPortsAreTaken(t *testing.T) {
	rt := &slowRuntime{servers: []runtime.Server{{Spec: runtime.Spec{
		ID: runtime.NewID(), Name: "Survival", Port: 25570, ExtraPorts: []runtime.ExtraPort{{Port: 8100, Protocol: "tcp", Label: "Map"}},
	}}}}
	s := NewService(rt, nil, nil)
	create := func(port uint32, extra ...*noryxv1.ExtraPort) (*noryxv1.CreateServerResponse, error) {
		return s.CreateServer(t.Context(), &noryxv1.CreateServerRequest{
			Name: "Lobby", Type: noryxv1.ServerType_SERVER_TYPE_PAPER, MemoryMb: 1024, Port: port, AcceptEula: true, ExtraPorts: extra,
		})
	}
	for _, port := range []uint32{8100, 25570} {
		if _, err := create(port); status.Code(err) != codes.AlreadyExists {
			t.Errorf("port %d: %v", port, err)
		}
		if _, err := create(25566, &noryxv1.ExtraPort{Port: port, Protocol: "udp"}); status.Code(err) != codes.AlreadyExists {
			t.Errorf("extra port %d: %v", port, err)
		}
	}
	res, err := create(25566, &noryxv1.ExtraPort{Port: 8101, Protocol: "tcp", Label: "Map"})
	if got := res.GetServer().GetExtraPorts(); err != nil || len(got) != 1 || got[0].GetPort() != 8101 || got[0].GetLabel() != "Map" {
		t.Fatalf("extra ports = %v, %v", got, err)
	}
	if len(s.reserved) != 0 {
		t.Fatalf("ports still reserved: %v", s.reserved)
	}
}

// A server keeps JVM options set before the agent refused them, and the list reports them.
func TestRefusedJVMOptions(t *testing.T) {
	options := []string{"-Dfile.encoding=UTF-8", "-XX:VMOptionsFile=/data/opts", "-Djava.system.class.loader=Evil"}
	rt := &slowRuntime{servers: []runtime.Server{{Spec: runtime.Spec{ID: runtime.NewID(), JVMOptions: options}}}}
	res, err := NewService(rt, nil, nil).ListServers(t.Context(), &noryxv1.ListServersRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.GetServers()[0].GetRefusedJvmOptions(); !slices.Equal(got, options[1:]) {
		t.Fatalf("refused options = %q, want %q", got, options[1:])
	}
	if got := res.GetServers()[0].GetJvmOptions(); !slices.Equal(got, options) {
		t.Fatalf("options = %q, want all of them", got)
	}
}

// Game servers run Minecraft, so they need the EULA accepted; proxies don't.
func TestCreateServerNeedsTheEULAForGameServers(t *testing.T) {
	s := NewService(&slowRuntime{}, nil, nil)
	create := func(typ noryxv1.ServerType, port uint32) error {
		_, err := s.CreateServer(t.Context(), &noryxv1.CreateServerRequest{Name: "Server", Type: typ, MemoryMb: 1024, Port: port})
		return err
	}
	if err := create(noryxv1.ServerType_SERVER_TYPE_PAPER, 25565); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("game server without the EULA: %v", err)
	}
	if err := create(noryxv1.ServerType_SERVER_TYPE_VELOCITY, 25577); err != nil {
		t.Fatalf("proxy without the EULA: %v", err)
	}
}

// The players of a Bedrock server take the ports after its own, which no other server can take,
// and it can't take theirs; a server.properties of its own comes with its first start.
func TestBedrockPorts(t *testing.T) {
	rt := &slowRuntime{servers: []runtime.Server{{Spec: runtime.Spec{ID: runtime.NewID(), Name: "Survival", Port: 25570}}}}
	s := NewService(rt, nil, nil)
	create := func(typ noryxv1.ServerType, port, players uint32) error {
		_, err := s.CreateServer(t.Context(), &noryxv1.CreateServerRequest{Name: "Server", Type: typ, MemoryMb: 1024, Port: port, AcceptEula: true, MaxPlayers: players})
		return err
	}
	bedrock, paper := noryxv1.ServerType_SERVER_TYPE_BEDROCK, noryxv1.ServerType_SERVER_TYPE_PAPER
	if err := create(bedrock, 25561, 10); status.Code(err) != codes.AlreadyExists {
		t.Errorf("players at the port of another server: %v", err)
	}
	if err := create(bedrock, 19132, 5); err != nil {
		t.Fatal(err)
	}
	for _, port := range []uint32{19132, 19136} {
		if err := create(paper, port, 0); status.Code(err) != codes.AlreadyExists {
			t.Errorf("port %d of a player: %v", port, err)
		}
	}
	if err := create(paper, 19137, 0); err != nil {
		t.Errorf("port after the players: %v", err)
	}
	_, err := s.CreateServer(t.Context(), &noryxv1.CreateServerRequest{
		Name: "World", Type: bedrock, MemoryMb: 1024, Port: 19200, AcceptEula: true, Properties: map[string]string{"gamemode": "creative"},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("properties before the first start: %v", err)
	}
}
