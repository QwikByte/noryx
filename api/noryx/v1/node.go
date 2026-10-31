package noryxv1

import (
	"fmt"
	"slices"
)

// ReasonRuntimeUnavailable is the reason of the ErrorInfo that agents attach to Unavailable
// errors of calls that failed because the container runtime can't be reached. Its metadata
// names the runtime under MetadataRuntime.
const ReasonRuntimeUnavailable = "RUNTIME_UNAVAILABLE"

// MetadataRuntime is the key of the runtime's name in the metadata of ReasonRuntimeUnavailable.
const MetadataRuntime = "runtime"

// The container runtimes an agent can run servers with.
const (
	RuntimeDocker = "docker"
	RuntimePodman = "podman"
)

// RuntimeTitle returns how texts name a runtime, e.g. "Podman".
func RuntimeTitle(name string) string {
	if name == RuntimePodman {
		return "Podman"
	}
	return "Docker"
}

// The bounds of NodeSettings, which master and agent check. Uploads and backups always leave
// at least MinKeepFreeGB free, so that they can't fill a node's disk.
const (
	MaxLogFileMB                       = 100
	MaxLogFiles                        = 10
	MinKeepFreeGB, MaxKeepFreeGB       = 1, 50
	MinStatsInterval, MaxStatsInterval = 5, 60
)

// DefaultNodeSettings are the settings of a node until the master chooses others: its
// containers keep 3 files of 10 MB of their output, far more than the 1000 lines that the
// console loads.
func DefaultNodeSettings() *NodeSettings {
	return &NodeSettings{LogFileMb: 10, LogFiles: 3, KeepFreeGb: MinKeepFreeGB, StatsIntervalSeconds: MinStatsInterval}
}

// Problem returns what is wrong with the settings of a node, or "".
func (s *NodeSettings) Problem() string {
	switch {
	case (s.GetLogFileMb() == 0) != (s.GetLogFiles() == 0), s.GetLogFileMb() > MaxLogFileMB, s.GetLogFiles() > MaxLogFiles:
		return fmt.Sprintf("Keep 1 to %d files of 1 to %d MB of the output of containers, or leave it to the runtime.", MaxLogFiles, MaxLogFileMB)
	case s.GetKeepFreeGb() < MinKeepFreeGB || s.GetKeepFreeGb() > MaxKeepFreeGB:
		return fmt.Sprintf("Keep %d to %d GiB free.", MinKeepFreeGB, MaxKeepFreeGB)
	case s.GetStatsIntervalSeconds() < MinStatsInterval || s.GetStatsIntervalSeconds() > MaxStatsInterval:
		return fmt.Sprintf("Measure every %d to %d seconds.", MinStatsInterval, MaxStatsInterval)
	}
	return ""
}

// Creates reports whether the agent creates servers of the type.
func (r *GetInfoResponse) Creates(t ServerType) bool {
	return slices.Contains(r.GetServerTypes(), t)
}
