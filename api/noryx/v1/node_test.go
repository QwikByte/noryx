package noryxv1

import "testing"

func TestNodeSettingsProblem(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings *NodeSettings
		want     string
	}{
		{"the defaults", DefaultNodeSettings(), ""},
		{"the runtime's logs", &NodeSettings{KeepFreeGb: 1, StatsIntervalSeconds: 5}, ""},
		{"the most", &NodeSettings{LogFileMb: MaxLogFileMB, LogFiles: MaxLogFiles, KeepFreeGb: MaxKeepFreeGB, StatsIntervalSeconds: MaxStatsInterval}, ""},
		{"a size without files", &NodeSettings{LogFileMb: 10, KeepFreeGb: 1, StatsIntervalSeconds: 5}, "Keep 1 to 10 files"},
		{"files without a size", &NodeSettings{LogFiles: 3, KeepFreeGb: 1, StatsIntervalSeconds: 5}, "Keep 1 to 10 files"},
		{"large files", &NodeSettings{LogFileMb: MaxLogFileMB + 1, LogFiles: 3, KeepFreeGb: 1, StatsIntervalSeconds: 5}, "Keep 1 to 10 files"},
		{"many files", &NodeSettings{LogFileMb: 10, LogFiles: MaxLogFiles + 1, KeepFreeGb: 1, StatsIntervalSeconds: 5}, "Keep 1 to 10 files"},
		{"no space kept free", &NodeSettings{StatsIntervalSeconds: 5}, "GiB free"},
		{"too much space kept free", &NodeSettings{KeepFreeGb: MaxKeepFreeGB + 1, StatsIntervalSeconds: 5}, "GiB free"},
		{"no interval", &NodeSettings{KeepFreeGb: 1}, "Measure every"},
		{"a short interval", &NodeSettings{KeepFreeGb: 1, StatsIntervalSeconds: MinStatsInterval - 1}, "Measure every"},
		{"a long interval", &NodeSettings{KeepFreeGb: 1, StatsIntervalSeconds: MaxStatsInterval + 1}, "Measure every"},
	} {
		checkProblem(t, tc.name, tc.settings.Problem(), tc.want)
	}
}

// Older agents only tell "docker <version>", or that they can't reach Docker.
func TestRuntimeOf(t *testing.T) {
	for _, tc := range []struct {
		info          *GetInfoResponse
		name, version string
	}{
		{&GetInfoResponse{RuntimeName: RuntimePodman, RuntimeVersion: "5.4.0", Runtime: "docker 27.1.1"}, RuntimePodman, "5.4.0"},
		{&GetInfoResponse{RuntimeName: RuntimePodman}, RuntimePodman, ""},
		{&GetInfoResponse{Runtime: "docker 27.1.1"}, RuntimeDocker, "27.1.1"},
		{&GetInfoResponse{Runtime: "docker " + RuntimeUnavailable}, RuntimeDocker, ""},
		{&GetInfoResponse{}, RuntimeDocker, ""},
	} {
		if name, version := tc.info.RuntimeOf(); name != tc.name || version != tc.version {
			t.Errorf("RuntimeOf(%v) = %q, %q, want %q, %q", tc.info, name, version, tc.name, tc.version)
		}
	}
	for name, want := range map[string]string{RuntimePodman: "Podman", RuntimeDocker: "Docker", "": "Docker"} {
		if got := RuntimeTitle(name); got != want {
			t.Errorf("RuntimeTitle(%q) = %q", name, got)
		}
	}
}
