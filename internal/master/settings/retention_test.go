package settings

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/QwikByte/noryx/internal/master/database"
)

// Players seen are kept as long as the log until they get a retention of their own, which
// stays within its bounds like the others.
func TestRetention(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	load := func() *Service {
		s, err := Load(t.Context(), db, Master{PanelDefaultAddr: "127.0.0.1:0"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if r := load().Get().Retention; r != (Retention{UsageDays: 30, SeenPlayers: true, SeenDays: 30, OperationHours: 1}) {
		t.Fatalf("defaults = %+v", r)
	}
	if _, err := db.Exec(`INSERT INTO settings (id, value) VALUES (1, '{"logDays": 90}')`); err != nil {
		t.Fatal(err)
	}
	s := load()
	if r := s.Get().Retention; r.SeenDays != 90 || !r.SeenPlayers {
		t.Fatalf("settings of a master that kept the log for 90 days = %+v", r)
	}

	set := func(change func(r *Retention)) error {
		next := s.Get()
		change(&next.Retention)
		_, err := s.Update(t.Context(), next)
		return err
	}
	for name, bad := range map[string]func(r *Retention){
		"usage for 29 days":       func(r *Retention) { r.UsageDays = 29 },
		"usage for 366 days":      func(r *Retention) { r.UsageDays = 366 },
		"players for no days":     func(r *Retention) { r.SeenDays = 0 },
		"players for 366 days":    func(r *Retention) { r.SeenDays = 366 },
		"operations for no hours": func(r *Retention) { r.OperationHours = 0 },
		"operations for 25 hours": func(r *Retention) { r.OperationHours = 25 },
	} {
		if err := set(bad); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := set(func(r *Retention) { *r = Retention{UsageDays: 365, SeenDays: 1, OperationHours: 24} }); err != nil {
		t.Fatal(err)
	}
	s = load()
	if s.UsageRetention() != 365*24*time.Hour || s.SeenPlayers() || s.SeenRetention() != 24*time.Hour || s.OperationsKept() != 24*time.Hour {
		t.Fatalf("retention after a restart = %+v", s.Get().Retention)
	}
}
