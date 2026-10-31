package app

import (
	"net/netip"
	"testing"

	"github.com/spf13/pflag"

	"github.com/QwikByte/noryx/internal/agent/runtime/docker"
)

func TestRuntimeFlags(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want docker.Options
		ok   bool
	}{
		{nil, docker.Options{}, true},
		{[]string{"--runtime", "podman"}, docker.Options{Podman: true}, true},
		{[]string{"--runtime", "podman", "--runtime-socket", "/run/user/podman.sock"}, docker.Options{Podman: true, Socket: "/run/user/podman.sock"}, true},
		{[]string{"--runtime", "docker", "--runtime-socket", "/srv/docker.sock"}, docker.Options{Socket: "/srv/docker.sock"}, true},
		{[]string{"--runtime", "containerd"}, docker.Options{}, false},
		{[]string{"--public-address", "203.0.113.7"}, docker.Options{PublicAddress: netip.MustParseAddr("203.0.113.7")}, true},
		{[]string{"--public-address", "192.168.1.10"}, docker.Options{PublicAddress: netip.MustParseAddr("192.168.1.10")}, true},
		{[]string{"--public-address", "2001:db8::1"}, docker.Options{}, false},
		{[]string{"--public-address", "play.example.com"}, docker.Options{}, false},
		{[]string{"--public-address", "127.0.0.1"}, docker.Options{}, false},
		{[]string{"--public-address", "0.0.0.0"}, docker.Options{}, false},
	} {
		var r runtimeFlags
		flags := pflag.NewFlagSet("serve", pflag.ContinueOnError)
		r.add(flags)
		if err := flags.Parse(tc.args); err != nil {
			t.Fatal(err)
		}
		got, err := r.options()
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("%v: %+v, %v", tc.args, got, err)
		}
	}
}
