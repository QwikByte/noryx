package network

import (
	"testing"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
)

func TestWriteGeyser(t *testing.T) {
	// Before Geyser's first start, a new file has the current version, so Geyser takes it as is.
	dir := dataDir(t, nil)
	if changed, err := WriteGeyser(dir, noryxv1.ServerType_SERVER_TYPE_VELOCITY, 19133); !changed || err != nil {
		t.Fatalf("changed = %v, err = %v", changed, err)
	}
	want(t, read(t, dir, "plugins/Geyser-Velocity/config.yml"), map[string]any{
		"config-version": 8, "bedrock/port": 19133, "advanced/bedrock/broadcast-port": 19133, "java/auth-type": "floodgate",
	})

	// Geyser's own file keeps its other settings and version, and the same port changes nothing.
	dir = dataDir(t, map[string]string{"plugins/Geyser-BungeeCord/config.yml": "bedrock:\n  port: 19132\n  address: 0.0.0.0\njava:\n  auth-type: online\nconfig-version: 9\n"})
	for _, want := range []bool{true, false} {
		if changed, err := WriteGeyser(dir, noryxv1.ServerType_SERVER_TYPE_BUNGEECORD, 19140); changed != want || err != nil {
			t.Fatalf("changed = %v, err = %v, want changed = %v", changed, err, want)
		}
	}
	want(t, read(t, dir, "plugins/Geyser-BungeeCord/config.yml"), map[string]any{
		"config-version": 9, "bedrock/port": 19140, "bedrock/address": "0.0.0.0", "java/auth-type": "floodgate",
	})

	// Without Bedrock, nothing is written.
	if changed, err := WriteGeyser(dataDir(t, nil), noryxv1.ServerType_SERVER_TYPE_VELOCITY, 0); changed || err != nil {
		t.Fatalf("changed = %v, err = %v", changed, err)
	}
}
