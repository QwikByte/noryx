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

func TestCreates(t *testing.T) {
	info := &GetInfoResponse{ServerTypes: []ServerType{ServerType_SERVER_TYPE_PAPER, ServerType_SERVER_TYPE_BEDROCK}}
	if !info.Creates(ServerType_SERVER_TYPE_BEDROCK) || info.Creates(ServerType_SERVER_TYPE_VELOCITY) || (&GetInfoResponse{}).Creates(ServerType_SERVER_TYPE_PAPER) {
		t.Error("Creates doesn't follow the types the agent tells")
	}
}
