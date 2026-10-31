package server

import (
	"cmp"
	"time"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
)

// advanced are the advanced settings of a server: the start period of its health check, when the
// agent stops it after crashes or restarts it as it stays unhealthy, the limit of its processes
// and its initial heap.
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
	// ProcessLimit is how many processes and threads the server's container may run; 0 means
	// the default.
	ProcessLimit uint32 `json:"processLimit"`
	// InitMemoryMB is the initial heap, below the memory; 0 means as large as the memory.
	InitMemoryMB uint32 `json:"initMemoryMb"`
}

// advancedOf returns the advanced settings of a server, with the defaults for those that are 0.
func advancedOf(s *noryxv1.Server) advanced {
	return advanced{
		HealthStart:      cmp.Or(s.GetHealthStartMinutes(), uint32(noryxv1.DefaultHealthStart/time.Minute)),
		CrashLimit:       cmp.Or(s.GetCrashLimit(), noryxv1.DefaultCrashLimit),
		StableAfter:      cmp.Or(s.GetStableAfterMinutes(), uint32(noryxv1.DefaultStableAfter/time.Minute)),
		UnhealthyRestart: s.GetUnhealthyRestartMinutes(),
		ProcessLimit:     cmp.Or(s.GetProcessLimit(), noryxv1.DefaultProcessLimit),
		InitMemoryMB:     s.GetInitMemoryMb(),
	}
}
