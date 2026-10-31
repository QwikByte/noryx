package backup

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/QwikByte/noryx/internal/master/schedule"
)

// A job no longer copies to a node that was removed, nor dumps datastores that were deleted.
func TestJobsPrune(t *testing.T) {
	exists := schedule.Exists{Nodes: map[string]bool{"n1": true}, Datastores: map[string]bool{"d1": true}}
	in, _ := json.Marshal(JobSettings{Keep: 3, Datastores: []string{"d1", "d2"}, Copy: &CopyTo{Node: "n2"}})
	out, removed := Jobs{}.Prune(in, exists)
	var got JobSettings
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got.Copy != nil || !slices.Equal(got.Datastores, []string{"d1"}) || got.Keep != 3 || len(removed) != 2 {
		t.Fatalf("pruned = %s, removed %q", out, removed)
	}
	for _, kept := range []JobSettings{{Copy: &CopyTo{Node: "n1"}}, {Copy: &CopyTo{Storage: "s1"}}, {Datastores: []string{"d1"}}} {
		in, _ := json.Marshal(kept)
		if out, removed := (Jobs{}).Prune(in, exists); string(out) != string(in) || len(removed) != 0 {
			t.Fatalf("pruned %s to %s", in, out)
		}
	}
}

// Agents of older versions only keep the newest backups, so they keep all of a job that keeps
// backups by age.
func TestJobRetention(t *testing.T) {
	s := JobSettings{Keep: 3, KeepDays: 7, KeepWeeks: 4, KeepMonths: 12}
	if r := s.retention("Europe/Berlin"); r.GetLast() != 3 || r.GetDays() != 7 || r.GetWeeks() != 4 || r.GetMonths() != 12 || r.GetTimeZone() != "Europe/Berlin" {
		t.Errorf("retention = %v", r)
	}
	for _, tc := range []struct {
		settings JobSettings
		want     uint32
	}{
		{JobSettings{Keep: 3}, 3},
		{JobSettings{Keep: 3, KeepDays: 7}, 0},
		{JobSettings{Keep: 3, KeepWeeks: 1}, 0},
		{JobSettings{KeepMonths: 1}, 0},
		{JobSettings{}, 0},
	} {
		if got := tc.settings.keep(); got != tc.want {
			t.Errorf("keep of %+v = %d, want %d", tc.settings, got, tc.want)
		}
	}
}
