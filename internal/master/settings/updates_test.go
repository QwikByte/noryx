package settings

import (
	"testing"
	"time"
)

// The master looks for updates every 1 to 24 hours, 6 by default, and updates all agents or
// none after itself, all by default.
func TestUpdates(t *testing.T) {
	s := loadWithCert(t)
	if s.UpdateCheckInterval() != 6*time.Hour || !s.UpdateAgentsAfterMaster() {
		t.Fatalf("defaults: every %v, agents follow %t", s.UpdateCheckInterval(), s.UpdateAgentsAfterMaster())
	}
	set := func(hours int, agents string) error {
		next := s.Get()
		next.UpdateCheckHours, next.UpdateAgents = hours, agents
		_, err := s.Update(t.Context(), next)
		return err
	}
	for _, bad := range []struct {
		hours  int
		agents string
	}{{0, "all"}, {25, "all"}, {-6, "all"}, {6, ""}, {6, "some"}, {6, "ALL"}} {
		if set(bad.hours, bad.agents) == nil {
			t.Errorf("every %d hours, %q agents accepted", bad.hours, bad.agents)
		}
	}
	if err := set(1, "none"); err != nil || s.UpdateCheckInterval() != time.Hour || s.UpdateAgentsAfterMaster() {
		t.Fatalf("every hour, no agents: %v, every %v, agents follow %t", err, s.UpdateCheckInterval(), s.UpdateAgentsAfterMaster())
	}
	if err := set(24, "all"); err != nil || s.UpdateCheckInterval() != 24*time.Hour || !s.UpdateAgentsAfterMaster() {
		t.Fatalf("every day, all agents: %v, every %v, agents follow %t", err, s.UpdateCheckInterval(), s.UpdateAgentsAfterMaster())
	}
}
