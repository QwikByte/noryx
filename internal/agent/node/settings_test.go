package node

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/runtime"
	"github.com/QwikByte/noryx/internal/agent/storage"
)

// applied records what the settings applied to the runtime and the measurements.
type applied struct {
	runtime.Runtime
	fileMB, files uint32
	interval      time.Duration
}

func (a *applied) LimitLogs(fileMB, files uint32)     { a.fileMB, a.files = fileMB, files }
func (a *applied) SetInterval(interval time.Duration) { a.interval = interval }

// The agent applies only settings within their bounds, keeps them in its data directory and
// applies them again when it starts, or the defaults without them.
func TestSettings(t *testing.T) {
	t.Cleanup(func() { storage.KeepFree(storage.MinFree) })
	dir := t.TempDir()
	a := &applied{}
	s := &Service{settings: LoadSettings(dir, a, a)}
	if a.fileMB != 10 || a.files != 3 || a.interval != 5*time.Second {
		t.Fatalf("defaults applied %+v", a)
	}
	for _, bad := range []*noryxv1.NodeSettings{
		nil,
		{LogFileMb: 10, LogFiles: 3, KeepFreeGb: 0, StatsIntervalSeconds: 5},
		{LogFileMb: 10, LogFiles: 3, KeepFreeGb: 51, StatsIntervalSeconds: 5},
		{LogFileMb: 10, LogFiles: 3, KeepFreeGb: 1, StatsIntervalSeconds: 4},
		{LogFileMb: 10, LogFiles: 3, KeepFreeGb: 1, StatsIntervalSeconds: 61},
		{LogFileMb: 0, LogFiles: 3, KeepFreeGb: 1, StatsIntervalSeconds: 5},
		{LogFileMb: 10, LogFiles: 0, KeepFreeGb: 1, StatsIntervalSeconds: 5},
		{LogFileMb: 101, LogFiles: 3, KeepFreeGb: 1, StatsIntervalSeconds: 5},
		{LogFileMb: 10, LogFiles: 11, KeepFreeGb: 1, StatsIntervalSeconds: 5},
	} {
		if _, err := s.Configure(t.Context(), &noryxv1.ConfigureRequest{Settings: bad}); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%v: %v", bad, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "settings.json")); err == nil || a.fileMB != 10 {
		t.Fatalf("kept or applied settings out of bounds: %+v", a)
	}

	// Without a limit of the output, the runtime's configuration applies.
	want := &noryxv1.NodeSettings{KeepFreeGb: 50, StatsIntervalSeconds: 60}
	if _, err := s.Configure(t.Context(), &noryxv1.ConfigureRequest{Settings: want}); err != nil {
		t.Fatal(err)
	}
	if a.fileMB != 0 || a.files != 0 || a.interval != time.Minute || s.settings.Get().GetKeepFreeGb() != 50 {
		t.Fatalf("applied %+v and %v", a, s.settings.Get())
	}
	again := &applied{}
	if got := LoadSettings(dir, again, again).Get(); got.GetKeepFreeGb() != 50 || again.files != 0 || again.interval != time.Minute {
		t.Fatalf("after a restart: %v, applied %+v", got, again)
	}

	// Settings changed by hand to something out of bounds give way to the defaults.
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"keepFreeGb": 0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := LoadSettings(dir, again, again).Get(); got.GetKeepFreeGb() != 1 || again.files != 3 {
		t.Fatalf("settings out of bounds: %v, applied %+v", got, again)
	}
}
