package server

import (
	"cmp"
	"time"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
)

// advanced are the advanced settings of a server: the start period of its health check, and when
// the agent stops it after crashes or restarts it as it stays unhealthy.
type advanced struct {
	// HealthStart is how many minutes a game server gets to start before its failed health
	// checks count; 0 means the default.
	HealthStart uint32 `json:"healthStart"`
	// CrashLimit is after how many crashes in a row the agent stops the server, each within
	// StableAfter minutes of its start; 0 means the default of each.
	CrashLimit  uint32 `json:"crashLimit"`
	StableAfter uint32 `json:"stableAfter"`
	// UnhealthyRestart is after how many minutes the agent restarts a running server that stays
	// unhealthy, which counts as a crash; 0 means never.
	UnhealthyRestart uint32 `json:"unhealthyRestart"`
}

// advancedOf returns the advanced settings of a server, with the defaults for those that older
// agents leave out.
func advancedOf(s *noryxv1.Server) advanced {
	return advanced{
		HealthStart:      cmp.Or(s.GetHealthStartMinutes(), uint32(noryxv1.DefaultHealthStart/time.Minute)),
		CrashLimit:       cmp.Or(s.GetCrashLimit(), noryxv1.DefaultCrashLimit),
		StableAfter:      cmp.Or(s.GetStableAfterMinutes(), uint32(noryxv1.DefaultStableAfter/time.Minute)),
		UnhealthyRestart: s.GetUnhealthyRestartMinutes(),
	}
}

// olderAgentAdvanced returns a warning if a node's agent answered without the advanced settings
// it was asked to give a server, as agents of earlier versions don't know them.
func olderAgentAdvanced(want advanced, got *noryxv1.Server) string {
	asked := &noryxv1.Server{
		HealthStartMinutes: want.HealthStart, CrashLimit: want.CrashLimit, StableAfterMinutes: want.StableAfter,
		UnhealthyRestartMinutes: want.UnhealthyRestart,
	}
	if advancedOf(asked) == advancedOf(got) {
		return ""
	}
	return "The agent of the node is too old for the start period of the health check and the settings of crashes, so the server keeps their defaults. Update the agent first, then set them in the server's settings."
}
