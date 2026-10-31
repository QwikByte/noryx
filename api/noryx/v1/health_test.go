package noryxv1

import (
	"math"
	"testing"
	"time"
)

// 0 stands for the default; other values are minutes or counts within their bounds, which
// can't overflow a duration.
func TestHealth(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  any
		want any
	}{
		{"HealthStart(0)", HealthStart(0), DefaultHealthStart},
		{"HealthStart(5)", HealthStart(5), 5 * time.Minute},
		{"CrashLimit(0)", CrashLimit(0), DefaultCrashLimit},
		{"CrashLimit(3)", CrashLimit(3), 3},
		{"StableAfter(0)", StableAfter(0), DefaultStableAfter},
		{"StableAfter(15)", StableAfter(15), 15 * time.Minute},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
	checkValid(t, ValidHealthStart, []uint32{0, 2, 10, 30}, []uint32{1, 31, math.MaxUint32})
	checkValid(t, ValidCrashLimit, []uint32{0, 1, 20}, []uint32{21, math.MaxUint32})
	checkValid(t, ValidStableAfter, []uint32{0, 1, 120}, []uint32{121, math.MaxUint32})
	checkValid(t, ValidUnhealthyRestart, []uint32{0, 1, 60}, []uint32{61, math.MaxUint32})
}
