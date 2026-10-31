package noryxv1

import (
	"regexp"
	"slices"
	"testing"
	"time"
)

func TestNewBackupID(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	id := NewBackupID(time.Date(2026, 10, 2, 6, 0, 0, 0, berlin))
	if !regexp.MustCompile(`^20261002-040000-[a-z2-7]{6}$`).MatchString(id) {
		t.Errorf("NewBackupID = %q, want the time in UTC and 6 random characters", id)
	}
}

// Names of archives of the master name a file in their folder, and nothing else.
func TestMasterBackupName(t *testing.T) {
	at := time.Date(2026, 10, 2, 4, 0, 0, 0, time.UTC)
	plain, encrypted := MasterBackupName(at, false), MasterBackupName(at, true)
	if !regexp.MustCompile(`^noryx-master-20261002-040000-[a-z2-7]{6}\.tar\.gz$`).MatchString(plain) ||
		!regexp.MustCompile(`^noryx-master-20261002-040000-[a-z2-7]{6}\.tar\.gz\.age$`).MatchString(encrypted) {
		t.Errorf("MasterBackupName = %q, %q", plain, encrypted)
	}
	for _, name := range []string{plain, encrypted} {
		if !ValidMasterBackupName(name) {
			t.Errorf("ValidMasterBackupName(%q) = false", name)
		}
	}
	for _, name := range []string{"", "../noryx-master-20261002-040000-abcdef.tar.gz", "noryx-master-20261002-040000-abcdef.tar.gz/x",
		"noryx-master-20261002-040000-abcdef.zip", "20261002-040000-abcdef.json", "noryx-master-20261002-040000-ABCDEF.tar.gz"} {
		if ValidMasterBackupName(name) {
			t.Errorf("ValidMasterBackupName(%q) = true", name)
		}
	}
}

func TestRetention(t *testing.T) {
	if r := Retention(5, nil); r.GetLast() != 5 || r.GetDays() != 0 || r.GetWeeks() != 0 || r.GetMonths() != 0 {
		t.Errorf("Retention(5, nil) = %v", r)
	}
	set := &BackupRetention{Days: 7}
	if r := Retention(5, set); r != set {
		t.Errorf("Retention(5, %v) = %v", set, r)
	}
}

// A retention keeps the newest backups, and the newest of each of the last days, weeks and
// months that have backups, in its time zone.
func TestRetentionKeeps(t *testing.T) {
	at := func(month time.Month, day, hour int) time.Time {
		return time.Date(2026, month, day, hour, 0, 0, 0, time.UTC)
	}
	created := []time.Time{ // newest first
		at(10, 31, 18), // Saturday of ISO week 44
		at(10, 31, 4),
		at(10, 30, 4),
		at(10, 29, 4),
		at(10, 26, 4), // Monday of week 44, after two days without backups
		at(10, 25, 4), // Sunday of week 43
		at(10, 18, 4), // week 42
		at(9, 30, 4),
		at(8, 31, 4),
	}
	for _, tc := range []struct {
		name      string
		retention *BackupRetention
		want      string // x for kept
	}{
		{"all", &BackupRetention{}, "xxxxxxxxx"},
		{"the newest", &BackupRetention{Last: 2}, "xx......."},
		{"more than there are", &BackupRetention{Last: 100}, "xxxxxxxxx"},
		{"days", &BackupRetention{Days: 3}, "x.xx....."},
		{"days with backups", &BackupRetention{Days: 4}, "x.xxx...."},
		{"weeks", &BackupRetention{Weeks: 3}, "x....xx.."},
		{"months", &BackupRetention{Months: 3}, "x......xx"},
		{"all of them", &BackupRetention{Last: 1, Days: 2, Weeks: 2, Months: 2}, "x.x..x.x."},
		{"an unknown time zone as UTC", &BackupRetention{Days: 3, TimeZone: "Mars/Olympus"}, "x.xx....."},
	} {
		got := ""
		for _, kept := range tc.retention.Keeps(created) {
			got += map[bool]string{true: "x", false: "."}[kept]
		}
		if got != tc.want {
			t.Errorf("%s: kept %s, want %s", tc.name, got, tc.want)
		}
	}
	if keep := (&BackupRetention{Last: 3}).Keeps(nil); len(keep) != 0 {
		t.Errorf("kept %v of none", keep)
	}

	// 23:00 and 01:00 UTC are on two days in UTC, but on the same day in New York.
	late := []time.Time{at(10, 31, 1), at(10, 30, 23)}
	for tz, want := range map[string][]bool{"": {true, true}, "America/New_York": {true, false}} {
		if got := (&BackupRetention{Days: 2, TimeZone: tz}).Keeps(late); !slices.Equal(got, want) {
			t.Errorf("days in %q: kept %v, want %v", tz, got, want)
		}
	}
}

func TestRetentionProblem(t *testing.T) {
	for _, tc := range []struct {
		retention *BackupRetention
		want      string
	}{
		{&BackupRetention{}, ""},
		{&BackupRetention{Last: MaxRetention, Days: MaxRetention, Weeks: MaxRetention, Months: MaxRetention, TimeZone: "Europe/Berlin"}, ""},
		{&BackupRetention{Last: MaxRetention + 1}, "at most 1000"},
		{&BackupRetention{Days: MaxRetention + 1}, "at most 1000"},
		{&BackupRetention{Weeks: MaxRetention + 1}, "at most 1000"},
		{&BackupRetention{Months: MaxRetention + 1}, "at most 1000"},
		{&BackupRetention{Days: 7, TimeZone: "Local"}, "time zone"},
		{&BackupRetention{Days: 7, TimeZone: "Mars/Olympus"}, "time zone"},
		{&BackupRetention{Days: 7, TimeZone: "../etc/localtime"}, "time zone"},
	} {
		checkProblem(t, tc.retention.String(), tc.retention.Problem(), tc.want)
	}
}
