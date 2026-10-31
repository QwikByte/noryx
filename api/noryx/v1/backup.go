package noryxv1

import (
	"crypto/rand"
	"regexp"
	"strings"
	"time"
)

// NewBackupID returns the ID of a backup created at t, e.g. 20261002-040000-k3x7qa.
func NewBackupID(t time.Time) string {
	return t.UTC().Format("20060102-150405") + "-" + strings.ToLower(rand.Text()[:6])
}

var masterBackupName = regexp.MustCompile(`^noryx-master-\d{8}-\d{6}-[a-z2-7]{6}\.tar\.gz(\.age)?$`)

// MasterBackupName returns the name of an archive of the master made at t, e.g.
// noryx-master-20261002-040000-k3x7qa.tar.gz, with .age at its end if it is encrypted.
func MasterBackupName(t time.Time, encrypted bool) string {
	name := "noryx-master-" + NewBackupID(t) + ".tar.gz"
	if encrypted {
		name += ".age"
	}
	return name
}

// ValidMasterBackupName reports whether name is one that MasterBackupName returns, so that it
// names a file in the folder of the archives of the master and nothing else.
func ValidMasterBackupName(name string) bool { return masterBackupName.MatchString(name) }

// MaxBackupPaths limits the further paths of a backup's selection, those it leaves out, and
// the paths of a restore.
const MaxBackupPaths = 20

// MaxRetention limits each count of a BackupRetention.
const MaxRetention = 1000

// Retention returns which backups of a job a request with keep and retention keeps:
// retention if it is set, otherwise the newest keep.
func Retention(keep uint32, retention *BackupRetention) *BackupRetention {
	if retention != nil {
		return retention
	}
	return &BackupRetention{Last: keep}
}

// KeepsAll reports whether a retention keeps every backup.
func (r *BackupRetention) KeepsAll() bool {
	return r.GetLast() == 0 && r.GetDays() == 0 && r.GetWeeks() == 0 && r.GetMonths() == 0
}

// Keeps tells which of backups created at the given times, newest first, a retention keeps:
// the newest ones, and the newest of each of the last days, weeks and months that have
// backups. Backups that are kept anyway, e.g. marked to keep, don't count.
func (r *BackupRetention) Keeps(created []time.Time) []bool {
	keep := make([]bool, len(created))
	if r.KeepsAll() {
		for i := range keep {
			keep[i] = true
		}
		return keep
	}
	loc, err := time.LoadLocation(r.GetTimeZone())
	if err != nil {
		loc = time.UTC
	}
	for i := range min(int(r.GetLast()), len(created)) {
		keep[i] = true
	}
	for _, rule := range []struct {
		count  uint32
		period func(time.Time) [3]int
	}{
		{r.GetDays(), func(t time.Time) [3]int { y, m, d := t.Date(); return [3]int{y, int(m), d} }},
		{r.GetWeeks(), func(t time.Time) [3]int { y, w := t.ISOWeek(); return [3]int{y, w} }},
		{r.GetMonths(), func(t time.Time) [3]int { y, m, _ := t.Date(); return [3]int{y, int(m)} }},
	} {
		seen := map[[3]int]bool{}
		for i, t := range created {
			period := rule.period(t.In(loc))
			if seen[period] {
				continue
			}
			if len(seen) == int(rule.count) {
				break
			}
			seen[period], keep[i] = true, true
		}
	}
	return keep
}

// Problem returns what is wrong with a retention, or "".
func (r *BackupRetention) Problem() string {
	switch {
	case max(r.GetLast(), r.GetDays(), r.GetWeeks(), r.GetMonths()) > MaxRetention:
		return "Keep at most 1000 backups, days, weeks or months per server."
	case !ValidTimeZone(r.GetTimeZone()):
		return "Choose a time zone."
	}
	return ""
}
