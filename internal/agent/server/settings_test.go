package server

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"reflect"
	"slices"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/runtime"
)

// A server keeps all its settings when it is created, moved to another node with the settings
// that the agent lists, and when they are updated: no request and no list leaves one out.
func TestSettingsRoundTrip(t *testing.T) {
	all := &noryxv1.CreateServerRequest{
		Name: "Modpack", Type: noryxv1.ServerType_SERVER_TYPE_FORGE, Version: "1.20.1", MemoryMb: 8192, Port: 25570, AcceptEula: true,
		Storage: "ssd", Java: "17", RestartPolicy: noryxv1.RestartPolicy_RESTART_POLICY_NEVER, JvmOptions: []string{"-XX:+UseZGC"},
		CpuMillis: 2500, LoaderVersion: "1.20.1-47.3.0", StopTimeoutSeconds: 120, TimeZone: "Europe/Berlin",
		ExtraPorts: []*noryxv1.ExtraPort{{Port: 8100, Protocol: "tcp", Label: "Map"}}, HealthStartMinutes: 10, CrashLimit: 3,
		StableAfterMinutes: 20, UnhealthyRestartMinutes: 5, ProcessLimit: 2048, InitMemoryMb: 1024,
	}
	aikar := proto.CloneOf(all) // Aikar's flags take all of the memory as the initial heap
	aikar.AikarFlags, aikar.InitMemoryMb = true, 0
	// Bedrock servers take no settings of Java, but the number of players.
	bedrock := &noryxv1.CreateServerRequest{
		Name: "Bedrock", Type: noryxv1.ServerType_SERVER_TYPE_BEDROCK, Version: "1.26.61.1", MemoryMb: 2048, Port: 19132, AcceptEula: true,
		Storage: "ssd", RestartPolicy: noryxv1.RestartPolicy_RESTART_POLICY_ON_CRASH, CpuMillis: 1500, StopTimeoutSeconds: 90,
		TimeZone: "Europe/Berlin", HealthStartMinutes: 5, CrashLimit: 4, StableAfterMinutes: 15, UnhealthyRestartMinutes: 10,
		ProcessLimit: 2048, MaxPlayers: 20,
	}
	archive := zipped(t, "world/level.dat", "level")
	set := map[string]bool{}
	for _, req := range []*noryxv1.CreateServerRequest{all, aikar, bedrock} {
		from := &slowRuntime{dir: t.TempDir()}
		source := NewService(from, nil, nil)
		if _, err := source.CreateServer(t.Context(), req); err != nil {
			t.Fatal(err)
		}
		created := from.servers[0].Spec
		for f, v := range reflect.ValueOf(created).Fields() {
			set[f.Name] = set[f.Name] || !v.IsZero()
		}

		listed, err := source.ListServers(t.Context(), &noryxv1.ListServersRequest{})
		if err != nil {
			t.Fatal(err)
		}
		to := &slowRuntime{dir: t.TempDir()}
		if err := NewService(to, nil, nil).ImportServer(&importStream{ctx: t.Context(), msgs: []*noryxv1.ImportServerRequest{
			{Content: &noryxv1.ImportServerRequest_Header{Header: listed.GetServers()[0]}},
			{Content: &noryxv1.ImportServerRequest_Data{Data: archive}},
		}}); err != nil {
			t.Fatal(err)
		}
		if got := to.servers[0].Spec; !reflect.DeepEqual(got, created) {
			t.Errorf("imported %+v\nwant     %+v", got, created)
		}

		rt := &slowRuntime{dir: t.TempDir()}
		s := NewService(rt, nil, nil)
		plain, err := s.CreateServer(t.Context(), &noryxv1.CreateServerRequest{
			Name: "Plain", Type: req.GetType(), Storage: req.GetStorage(), MemoryMb: 1024, Port: 25565, AcceptEula: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		update := &noryxv1.UpdateServerRequest{}
		copyFields(update, req)
		update.Id = plain.GetServer().GetId()
		if _, err := s.UpdateServer(t.Context(), update); err != nil {
			t.Fatal(err)
		}
		want := created
		want.ID = update.Id
		if got := rt.servers[0].Spec; !reflect.DeepEqual(got, want) {
			t.Errorf("updated %+v\nwant    %+v", got, want)
		}
	}
	// The rest is the server's role in a network, which ConfigureNetwork sets.
	network := []string{"BehindProxy", "ProxyOnNode", "BedrockPlayers", "BedrockPort", "Overlay"}
	for f := range reflect.TypeFor[runtime.Spec]().Fields() {
		if !set[f.Name] && !slices.Contains(network, f.Name) {
			t.Errorf("the test sets no %s", f.Name)
		}
	}
}

// copyFields sets the fields of to that from has, by their names, which the settings of a
// server share in all messages.
func copyFields(to, from proto.Message) {
	m := to.ProtoReflect()
	from.ProtoReflect().Range(func(f protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if field := m.Descriptor().Fields().ByName(f.Name()); field != nil {
			m.Set(field, v)
		}
		return true
	})
}

// zipped returns a ZIP archive with one file.
func zipped(t *testing.T, name, content string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, content); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// importStream sends the messages of an import.
type importStream struct {
	grpc.ServerStream
	ctx  context.Context
	msgs []*noryxv1.ImportServerRequest
}

func (s *importStream) Context() context.Context { return s.ctx }

func (s *importStream) Recv() (*noryxv1.ImportServerRequest, error) {
	if len(s.msgs) == 0 {
		return nil, io.EOF
	}
	msg := s.msgs[0]
	s.msgs = s.msgs[1:]
	return msg, nil
}

func (s *importStream) SendAndClose(*noryxv1.ImportServerResponse) error { return nil }
