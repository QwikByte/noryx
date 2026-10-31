package backup

import (
	"slices"
	"testing"
	"time"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
)

// The copies a job keeps: all that are marked to keep, and of the others those its retention
// keeps, newest first.
func TestKeeps(t *testing.T) {
	copy := func(day int, id string, kept bool) Copy {
		return Copy{BackupID: id, CreatedAt: time.Date(2026, 10, day, 4, 0, 0, 0, time.UTC), Kept: kept}
	}
	copies := []Copy{copy(28, "c", false), copy(31, "a", false), copy(30, "kept", true), copy(31, "b", false), copy(29, "d", false), copy(1, "old", true)}
	for _, tc := range []struct {
		name      string
		retention *noryxv1.BackupRetention
		want      []bool
	}{
		{"all", &noryxv1.BackupRetention{}, []bool{true, true, true, true, true, true}},
		{"the newest", &noryxv1.BackupRetention{Last: 2}, []bool{true, true, true, false, false, true}},
		{"days", &noryxv1.BackupRetention{Days: 2}, []bool{true, false, true, true, false, true}},
		{"the newest one", &noryxv1.BackupRetention{Last: 1}, []bool{true, false, true, false, false, true}},
	} {
		keep := keeps(copies, tc.retention)
		// keeps sorts the copies newest first, those of the same time by their backups' IDs.
		var order []string
		for _, cp := range copies {
			order = append(order, cp.BackupID)
		}
		if !slices.Equal(order, []string{"b", "a", "kept", "d", "c", "old"}) || !slices.Equal(keep, tc.want) {
			t.Errorf("%s: kept %v of %q, want %v", tc.name, keep, order, tc.want)
		}
	}
}
