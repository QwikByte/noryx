package docker

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/QwikByte/noryx/internal/agent/runtime"
)

// TestOutputLive checks with Docker or Podman that the output of a container stays within its
// limit, that following it goes on once it rotated, and that Docker still tells its last 1000
// lines; Podman's single file may hold fewer right after it started again. It runs with
// NORYX_DOCKER_TEST.
func TestOutputLive(t *testing.T) {
	d := live(t)
	ctx := t.Context()
	d.LimitLogs(1, 2)
	id := runtime.NewID()
	// After a second, more than 3 MB of numbered lines, then a line every second.
	script := `sleep 1; i=0; while [ $i -lt 30000 ]; do printf 'line %05d %0100d\n' $i 0; i=$((i+1)); done; while true; do echo tick; sleep 1; done`
	liveContainer(t, d, id, &container.Config{Cmd: []string{"sh", "-c", script}}, &container.HostConfig{LogConfig: d.logConfig()})

	follow, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	last := ""
	for line, err := range d.Logs(follow, id, 0, time.Time{}) {
		must(t, err)
		if line.Text == "tick" {
			break
		}
		last = line.Text
	}
	if !strings.HasPrefix(last, "line 29999 ") {
		t.Fatalf("followed up to %.20q", last)
	}

	res, err := d.cli.ContainerInspect(ctx, containerName(id), client.ContainerInspectOptions{})
	must(t, err)
	if info, err := os.Stat(res.Container.LogPath); err != nil || info.Size() > 2e6 {
		t.Errorf("the output in %q: %v, %v", res.Container.LogPath, info, err)
	}

	tail, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	lines := 0
	for _, err := range d.Logs(tail, id, 1000, time.Time{}) {
		must(t, err)
		lines++
	}
	t.Logf("the last lines are %d", lines)
	if d.podman == nil && lines < 1000 {
		t.Errorf("Docker tells %d of the last 1000 lines", lines)
	}
}
