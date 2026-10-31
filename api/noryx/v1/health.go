package noryxv1

import "time"

// The health check of a game server gives it a start period, in which failures don't count, e.g.
// while a large modpack loads. Proxies keep the start period of their image.
const (
	DefaultHealthStart = 2 * time.Minute
	MaxHealthStart     = 30 * time.Minute
)

// HealthStart is the start period of a game server with health_start_minutes; 0 means
// DefaultHealthStart.
func HealthStart(minutes uint32) time.Duration {
	if minutes == 0 {
		return DefaultHealthStart
	}
	return time.Duration(minutes) * time.Minute
}

// ValidHealthStart reports whether health_start_minutes is 0, for the default, or from
// DefaultHealthStart to MaxHealthStart.
func ValidHealthStart(minutes uint32) bool {
	return minutes == 0 || within(minutes, DefaultHealthStart, MaxHealthStart)
}

// The runtime starts a crashed server again and again. The agent stops it after its crash limit
// of crashes in a row, each within its stable time of its start, and restarts a running server
// that stays unhealthy for its minutes, if it has any, which counts as a crash.
const (
	DefaultCrashLimit   = 5
	MaxCrashLimit       = 20
	DefaultStableAfter  = 10 * time.Minute
	MaxStableAfter      = 2 * time.Hour
	MaxUnhealthyRestart = time.Hour
)

// CrashLimit is the crash limit of a server with crash_limit; 0 means DefaultCrashLimit.
func CrashLimit(limit uint32) int {
	if limit == 0 {
		return DefaultCrashLimit
	}
	return int(limit)
}

// StableAfter is how long a run of a server with stable_after_minutes must last to end a row of
// crashes; 0 means DefaultStableAfter.
func StableAfter(minutes uint32) time.Duration {
	if minutes == 0 {
		return DefaultStableAfter
	}
	return time.Duration(minutes) * time.Minute
}

// ValidCrashLimit reports whether crash_limit is 0, for the default, or at most MaxCrashLimit.
func ValidCrashLimit(limit uint32) bool { return limit <= MaxCrashLimit }

// ValidStableAfter reports whether stable_after_minutes is 0, for the default, or at most
// MaxStableAfter.
func ValidStableAfter(minutes uint32) bool { return within(minutes, 0, MaxStableAfter) }

// ValidUnhealthyRestart reports whether unhealthy_restart_minutes is 0, for never, or at most
// MaxUnhealthyRestart.
func ValidUnhealthyRestart(minutes uint32) bool { return within(minutes, 0, MaxUnhealthyRestart) }

// within reports whether minutes are from least to most, without overflowing a duration.
func within(minutes uint32, least, most time.Duration) bool {
	return time.Duration(minutes) >= least/time.Minute && time.Duration(minutes) <= most/time.Minute
}
