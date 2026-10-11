package noryxv1

import (
	"regexp"
	"slices"
	"time"
	_ "time/tzdata" // so that time zones are known on systems without a database of them
)

// The memory of a server's heap, in MB.
const (
	MinMemoryMB = 512
	MaxMemoryMB = 64 << 10
)

// javaVersions are those of the images of game servers; empty is the newest.
var javaVersions = []string{"", "8", "11", "17", "21", "25"}

// ValidJava reports whether java is a Java version of the images of game servers, or empty for
// the newest.
func ValidJava(java string) bool { return slices.Contains(javaVersions, java) }

// ContainerMemoryMB is the hard memory limit of the container of a server whose heap is
// heapMB: Java needs memory beyond its heap, e.g. for its code and threads. The master
// counts it against the memory of a node.
func ContainerMemoryMB(heapMB uint32) int64 { return int64(heapMB)*5/4 + 256 }

// MaxHeapMB is the largest heap whose container fits in containerMB, the inverse of
// ContainerMemoryMB.
func MaxHeapMB(containerMB int64) int64 { return max(0, (containerMB-256)*4/5) }

// A server gets its stop timeout to stop gracefully, e.g. to save its worlds, before it is
// killed. Whoever waits for a stop allows for the longest.
const (
	DefaultStopTimeout = time.Minute
	MinStopTimeout     = 30 * time.Second
	MaxStopTimeout     = 10 * time.Minute
)

// StopTimeout is the stop timeout of a server with stop_timeout_seconds; 0, as older masters
// and agents send it, means DefaultStopTimeout.
func StopTimeout(seconds uint32) time.Duration {
	if seconds == 0 {
		return DefaultStopTimeout
	}
	return time.Duration(seconds) * time.Second
}

// ValidStopTimeout reports whether stop_timeout_seconds is 0, for the default, or from
// MinStopTimeout to MaxStopTimeout.
func ValidStopTimeout(seconds uint32) bool {
	d := StopTimeout(seconds)
	return d >= MinStopTimeout && d <= MaxStopTimeout
}

// Names of IANA time zones, e.g. Europe/Berlin, America/Port-au-Prince or Etc/GMT+5.
var timeZonePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+/-]{0,63}$`)

// ValidTimeZone reports whether tz is empty, for UTC, or names an IANA time zone. It ends up
// in a variable of the image, so it has no other characters than the names of zones.
func ValidTimeZone(tz string) bool {
	if tz == "" {
		return true
	}
	if tz == "Local" || !timeZonePattern.MatchString(tz) {
		return false
	}
	_, err := time.LoadLocation(tz)
	return err == nil
}
