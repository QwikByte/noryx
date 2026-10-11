package server

import (
	"testing"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/runtime"
)

func TestCheckAdvanced(t *testing.T) {
	valid := runtime.Spec{Name: "Modpack", Type: noryxv1.ServerType_SERVER_TYPE_NEOFORGE, Version: "LATEST", MemoryMB: 8192, Port: 25565}
	for _, tt := range []struct {
		name   string
		change func(*runtime.Spec)
		ok     bool
	}{
		{"defaults", func(*runtime.Spec) {}, true},
		{"shortest start period", func(s *runtime.Spec) { s.HealthStart = 2 }, true},
		{"longest start period", func(s *runtime.Spec) { s.HealthStart = 30 }, true},
		{"too short a start period", func(s *runtime.Spec) { s.HealthStart = 1 }, false},
		{"too long a start period", func(s *runtime.Spec) { s.HealthStart = 31 }, false},
		// As a duration, it would overflow to about 2.5 minutes.
		{"start period that overflows", func(s *runtime.Spec) { s.HealthStart = 307445737 }, false},
		{"default start period of a proxy", func(s *runtime.Spec) { s.Type, s.HealthStart = noryxv1.ServerType_SERVER_TYPE_VELOCITY, 2 }, true},
		{"start period of a proxy", func(s *runtime.Spec) { s.Type, s.HealthStart = noryxv1.ServerType_SERVER_TYPE_VELOCITY, 10 }, false},
		{"crash settings", func(s *runtime.Spec) { s.CrashLimit, s.StableAfter, s.UnhealthyRestart = 1, 120, 60 }, true},
		{"crash settings of a proxy", func(s *runtime.Spec) {
			s.Type, s.CrashLimit, s.StableAfter, s.UnhealthyRestart = noryxv1.ServerType_SERVER_TYPE_VELOCITY, 20, 1, 1
		}, true},
		{"too high a crash limit", func(s *runtime.Spec) { s.CrashLimit = 21 }, false},
		{"too long until a run is stable", func(s *runtime.Spec) { s.StableAfter = 121 }, false},
		{"too long until an unhealthy server restarts", func(s *runtime.Spec) { s.UnhealthyRestart = 61 }, false},
		{"restart of an unhealthy server that overflows", func(s *runtime.Spec) { s.UnhealthyRestart = 307445737 }, false},
	} {
		spec := valid
		tt.change(&spec)
		if msg := checkSettings(spec, 8); (msg == "") != tt.ok {
			t.Errorf("%s: %q", tt.name, msg)
		}
	}
}
