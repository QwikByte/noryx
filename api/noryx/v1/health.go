package noryxv1

import "time"

// The health check of a game server gives it a start period, in which failures don't count, e.g.
// while a large modpack loads. Proxies keep the start period of their image.
const (
	DefaultHealthStart = 2 * time.Minute
	MaxHealthStart     = 30 * time.Minute
)

// HealthStart is the start period of a game server with health_start_minutes; 0, as older
// masters and agents send it, means DefaultHealthStart.
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

// within reports whether minutes are from least to most, without overflowing a duration.
func within(minutes uint32, least, most time.Duration) bool {
	return time.Duration(minutes) >= least/time.Minute && time.Duration(minutes) <= most/time.Minute
}
