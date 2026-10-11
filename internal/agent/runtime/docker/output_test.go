package docker

import (
	"maps"
	"testing"
)

// Docker keeps rotated files of the output of containers, Podman one file that takes as much
// space at most; without a limit, the runtime's configuration applies.
func TestLogConfig(t *testing.T) {
	for _, tc := range []struct {
		podman        bool
		fileMB, files uint32
		set           bool
		want          map[string]string // nil for the runtime's configuration
	}{
		{want: map[string]string{"max-size": "10m", "max-file": "3"}}, // until the settings apply
		{podman: true, want: map[string]string{"max-size": "30m"}},
		{fileMB: 50, files: 10, set: true, want: map[string]string{"max-size": "50m", "max-file": "10"}},
		{podman: true, fileMB: 100, files: 10, set: true, want: map[string]string{"max-size": "1000m"}},
		{set: true},
		{podman: true, set: true},
	} {
		d := &Docker{}
		if tc.podman {
			d.podman = &podman{}
		}
		if tc.set {
			d.LimitLogs(tc.fileMB, tc.files)
		}
		got := d.logConfig()
		if tc.want == nil && (got.Type != "" || got.Config != nil) || tc.want != nil && (got.Type != "json-file" || !maps.Equal(got.Config, tc.want)) {
			t.Errorf("podman %v, %d files of %d MB: %+v, want %v", tc.podman, tc.files, tc.fileMB, got, tc.want)
		}
	}
}
