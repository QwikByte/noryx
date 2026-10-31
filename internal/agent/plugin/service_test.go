package plugin

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/datadir"
	"github.com/QwikByte/noryx/internal/agent/runtime"
)

func TestEveryServerTypeHasAFolder(t *testing.T) {
	for value, name := range noryxv1.ServerType_name {
		typ := noryxv1.ServerType(value)
		if _, ok := Folder(typ); ok != typ.Addons() {
			t.Errorf("%s: folder for plugins or mods %v, but loads them %v", name, ok, typ.Addons())
		}
	}
}

// Turned-off plugins move to a folder that servers don't load and back, are listed only for
// masters that ask, and are updated and removed where they are.
func TestTurnOff(t *testing.T) {
	svc, id, data := start(t, noryxv1.ServerType_SERVER_TYPE_PAPER)
	write(t, filepath.Join(data, "plugins", "LuckPerms.jar"), jar(t, map[string]string{"plugin.yml": "main: me.lucko.Main\nname: LuckPerms\n"}))
	check(t, os.MkdirAll(filepath.Join(data, "plugins", "LuckPerms"), 0o750))
	write(t, filepath.Join(data, "plugins", "Other.jar"), []byte("not a zip"))
	enable := func(name string, on bool) error {
		_, err := svc.EnablePlugin(t.Context(), &noryxv1.EnablePluginRequest{ServerId: id, FileName: name, Enabled: on})
		return err
	}
	listing := func(disabled bool) []string {
		res, err := svc.ListPlugins(t.Context(), &noryxv1.ListPluginsRequest{ServerId: id, IncludeDisabled: disabled})
		check(t, err)
		var names []string
		for _, p := range res.GetPlugins() {
			names = append(names, fmt.Sprintf("%s off=%t settings=%s", p.GetFileName(), p.GetDisabled(), p.GetSettings()))
		}
		return names
	}

	check(t, enable("LuckPerms.jar", false))
	if _, err := os.Stat(filepath.Join(data, "plugins", Disabled, "LuckPerms.jar")); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(listing(false)); got != "[Other.jar off=false settings=]" {
		t.Fatalf("listing without disabled files = %s", got)
	}
	if got := fmt.Sprint(listing(true)); got != "[Other.jar off=false settings= LuckPerms.jar off=true settings=LuckPerms]" {
		t.Fatalf("listing = %s", got)
	}
	// Nothing is replaced, and only plain .jar names are accepted.
	write(t, filepath.Join(data, "plugins", "LuckPerms.jar"), []byte("another"))
	for name, want := range map[string]codes.Code{"LuckPerms.jar": codes.AlreadyExists, "Missing.jar": codes.NotFound, "../server.jar": codes.InvalidArgument} {
		if err := enable(name, true); status.Code(err) != want {
			t.Errorf("turning %s on: %v, want %s", name, err, want)
		}
	}
	check(t, os.Remove(filepath.Join(data, "plugins", "LuckPerms.jar")))
	check(t, enable("LuckPerms.jar", true))
	if got := fmt.Sprint(listing(true)); got != "[LuckPerms.jar off=false settings=LuckPerms Other.jar off=false settings=]" {
		t.Fatalf("listing = %s", got)
	}

	// An update of a turned-off plugin stays off, and removing finds it there.
	check(t, enable("Other.jar", false))
	stream := &installStream{ctx: t.Context(), msgs: []*noryxv1.InstallPluginRequest{
		{Content: &noryxv1.InstallPluginRequest_Header{Header: &noryxv1.InstallPluginHeader{ServerId: id, FileName: "Other-2.jar", Replaces: "Other.jar", Disabled: true}}},
		{Content: &noryxv1.InstallPluginRequest_Data{Data: []byte("other 2")}},
	}}
	check(t, svc.InstallPlugin(stream))
	if got := fmt.Sprint(listing(true)); got != "[LuckPerms.jar off=false settings=LuckPerms Other-2.jar off=true settings=]" {
		t.Fatalf("listing after the update = %s", got)
	}
	if _, err := svc.RemovePlugin(t.Context(), &noryxv1.RemovePluginRequest{ServerId: id, FileName: "Other-2.jar"}); status.Code(err) != codes.NotFound {
		t.Fatalf("removing a turned-off file as if it were on: %v", err)
	}
	_, err := svc.RemovePlugin(t.Context(), &noryxv1.RemovePluginRequest{ServerId: id, FileName: "Other-2.jar", Disabled: true})
	check(t, err)

	// The server owns the folder, but a link there isn't followed.
	check(t, os.Remove(filepath.Join(data, "plugins", Disabled)))
	check(t, os.Symlink("../world", filepath.Join(data, "plugins", Disabled)))
	if err := enable("LuckPerms.jar", false); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("turning off into a link: %v", err)
	}
	if got := fmt.Sprint(listing(true)); got != "[LuckPerms.jar off=false settings=LuckPerms]" {
		t.Fatalf("listing with a link = %s", got)
	}
}

// The name of a plugin comes from the descriptor its server reads, and only becomes the
// folder of its settings if it is a plain name.
func TestPluginName(t *testing.T) {
	for _, tc := range []struct {
		typ   noryxv1.ServerType
		files map[string]string
		want  string
	}{
		{noryxv1.ServerType_SERVER_TYPE_PAPER, map[string]string{"plugin.yml": "name: 'Essentials' # the core\nversion: 2\n"}, "Essentials"},
		{noryxv1.ServerType_SERVER_TYPE_PAPER, map[string]string{"plugin.yml": "name: Old", "paper-plugin.yml": "\uFEFFname: \"New\"\n"}, "New"},
		{noryxv1.ServerType_SERVER_TYPE_PAPER, map[string]string{"plugin.yml": "authors:\n  name: Someone\n"}, ""},
		{noryxv1.ServerType_SERVER_TYPE_PAPER, map[string]string{"plugin.yml": "name: ../../world\n"}, ""},
		{noryxv1.ServerType_SERVER_TYPE_PAPER, map[string]string{"plugin.yml": "name: .disabled\n"}, ""},
		{noryxv1.ServerType_SERVER_TYPE_WATERFALL, map[string]string{"plugin.yml": "name: Bukkit", "bungee.yml": "name: Bungee"}, "Bungee"},
		{noryxv1.ServerType_SERVER_TYPE_VELOCITY, map[string]string{"plugin.yml": "name: Bukkit", "velocity-plugin.json": `{"id":"luckperms"}`}, "luckperms"},
		{noryxv1.ServerType_SERVER_TYPE_FABRIC, map[string]string{"plugin.yml": "name: Mod"}, ""},
	} {
		data := jar(t, tc.files)
		if got := pluginName(bytes.NewReader(data), int64(len(data)), descriptors(tc.typ)); got != tc.want {
			t.Errorf("name of %v on %s = %q, want %q", tc.files, tc.typ, got, tc.want)
		}
	}

	// A huge index or descriptor isn't read.
	files := map[string]string{"plugin.yml": "name: Big\n"}
	for i := range maxJarRead >> 12 {
		files[fmt.Sprintf("%s/Class%d.class", bytes.Repeat([]byte("a"), 4096), i)] = ""
	}
	huge := jar(t, files)
	if got := pluginName(bytes.NewReader(huge), int64(len(huge)), descriptors(noryxv1.ServerType_SERVER_TYPE_PAPER)); got != "" {
		t.Errorf("name in a jar with a huge index = %q", got)
	}
	large := jar(t, map[string]string{"plugin.yml": "name: Large\ndescription: " + string(bytes.Repeat([]byte("x"), maxDescriptor)) + "\n"})
	if got := pluginName(bytes.NewReader(large), int64(len(large)), descriptors(noryxv1.ServerType_SERVER_TYPE_PAPER)); got != "" {
		t.Errorf("name in a huge plugin.yml = %q", got)
	}
}

// start returns the plugin service of a node with one server of a type, and its data.
func start(t *testing.T, typ noryxv1.ServerType) (*Service, string, string) {
	rt := dataRuntime{dir: t.TempDir(), server: runtime.Server{Spec: runtime.Spec{ID: runtime.NewID(), Type: typ}}}
	data := filepath.Join(rt.dir, rt.server.ID)
	check(t, os.MkdirAll(filepath.Join(data, "plugins"), 0o750))
	return NewService(rt), rt.server.ID, data
}

type dataRuntime struct {
	runtime.Runtime
	dir    string
	server runtime.Server
}

func (r dataRuntime) List(context.Context) ([]runtime.Server, error) {
	return []runtime.Server{r.server}, nil
}

func (r dataRuntime) Data(_ context.Context, id string) (*datadir.Dir, error) {
	return datadir.Open(filepath.Join(r.dir, id))
}

type installStream struct {
	noryxv1.PluginService_InstallPluginServer
	ctx  context.Context
	msgs []*noryxv1.InstallPluginRequest
}

func (s *installStream) Context() context.Context { return s.ctx }

func (s *installStream) Recv() (*noryxv1.InstallPluginRequest, error) {
	if len(s.msgs) == 0 {
		return nil, io.EOF
	}
	msg := s.msgs[0]
	s.msgs = s.msgs[1:]
	return msg, nil
}

func (s *installStream) SendAndClose(*noryxv1.InstallPluginResponse) error { return nil }

func jar(t *testing.T, files map[string]string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		check(t, err)
		_, err = w.Write([]byte(content))
		check(t, err)
	}
	check(t, zw.Close())
	return buf.Bytes()
}

func write(t *testing.T, name string, data []byte) {
	check(t, os.MkdirAll(filepath.Dir(name), 0o750))
	check(t, os.WriteFile(name, data, 0o600))
}

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
