package docker

import (
	"net/netip"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/runtime"
)

// A Bedrock server gets the hardening of the other images, its version, players and time zone
// as the only variables, its TCP port and a UDP port for each player published, and its
// console on its standard input.
func TestBedrockContainer(t *testing.T) {
	spec := runtime.Spec{
		ID: "server", Type: noryxv1.ServerType_SERVER_TYPE_BEDROCK, Version: "1.26.61.1", MemoryMB: 2048, Port: 19140,
		MaxPlayers: 3, TimeZone: "Europe/Berlin", HealthStart: 5,
	}
	opts, err := containerOptions(spec, "/srv/noryx/server", sharedNetwork)
	if err != nil {
		t.Fatal(err)
	}
	c, h := opts.Config, opts.HostConfig
	wantEnv := []string{"VERSION=1.26.61.1", "EULA=TRUE", "TRANSPORT=nethernet", "MAX_PLAYERS=3", "TZ=Europe/Berlin"}
	if c.Image != bedrockImage+":latest" || !slices.Equal(c.Env, wantEnv) || !c.OpenStdin || c.User != imageUser {
		t.Errorf("image %s, env %v, stdin %v, user %q", c.Image, c.Env, c.OpenStdin, c.User)
	}
	if len(h.CapAdd) != 0 || !slices.Equal(h.CapDrop, []string{"ALL"}) || !slices.Equal(h.SecurityOpt, []string{"no-new-privileges:true"}) ||
		*h.PidsLimit != noryxv1.DefaultProcessLimit || h.Memory != noryxv1.ContainerMemoryMB(2048)<<20 ||
		!slices.Equal(h.Binds, []string{"/srv/noryx/server:/data"}) {
		t.Errorf("capabilities +%v -%v, security %v, processes %d, memory %d, binds %v", h.CapAdd, h.CapDrop, h.SecurityOpt, *h.PidsLimit, h.Memory, h.Binds)
	}
	want := map[string]string{"19132/tcp": "19140", "19140/udp": "19140", "19141/udp": "19141", "19142/udp": "19142"}
	if len(h.PortBindings) != len(want) || len(c.ExposedPorts) != len(want) {
		t.Errorf("published %v, exposed %v", h.PortBindings, c.ExposedPorts)
	}
	for port, host := range want {
		b := h.PortBindings[network.MustParsePort(port)]
		if len(b) != 1 || b[0].HostPort != host || b[0].HostIP.IsValid() {
			t.Errorf("%s published at %+v, want %s", port, b, host)
		}
	}
	check := &container.HealthConfig{
		Test:     []string{"CMD", "mc-monitor", "status-bedrock", "--host", "127.0.0.1", "--port", "19132"},
		Interval: 30 * time.Second, Timeout: 30 * time.Second, StartPeriod: 5 * time.Minute, Retries: 2,
	}
	if !reflect.DeepEqual(c.Healthcheck, check) {
		t.Errorf("health check %+v", c.Healthcheck)
	}
}

// A Bedrock server tells players the node's address with the UDP ports of its players, which
// are the same in its container.
func TestUDPPorts(t *testing.T) {
	bedrock := noryxv1.ServerType_SERVER_TYPE_BEDROCK
	addr := netip.MustParseAddr("203.0.113.7")
	for _, tc := range []struct {
		spec runtime.Spec
		addr netip.Addr
		want string
	}{
		{runtime.Spec{Type: bedrock, Port: 19132}, addr, "203.0.113.7:19132-19141:19132-19141"},
		{runtime.Spec{Type: bedrock, Port: 19140, MaxPlayers: 1}, addr, "203.0.113.7:19140:19140"},
		{runtime.Spec{Type: bedrock, Port: 19132}, netip.Addr{}, "19132-19141"},
	} {
		if got := udpPorts(tc.spec, tc.addr); got != tc.want {
			t.Errorf("%+v at %v: %q, want %q", tc.spec, tc.addr, got, tc.want)
		}
	}
	if d := (&Docker{public: addr}); d.publicAddress() != addr {
		t.Errorf("public address %v, want %v", d.publicAddress(), addr)
	}
}
