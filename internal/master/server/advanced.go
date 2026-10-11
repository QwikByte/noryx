package server

import (
	"cmp"
	"time"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
)

// healthStart is the start period of a server's health check in minutes; 0, as older agents send
// it, means the default.
func healthStart(minutes uint32) uint32 {
	return cmp.Or(minutes, uint32(noryxv1.DefaultHealthStart/time.Minute))
}

// olderAgentAdvanced returns a warning if a node's agent answered without the advanced settings
// it was asked to give a server, as agents of earlier versions don't know them.
func olderAgentAdvanced(want settings, got *noryxv1.Server) string {
	if healthStart(got.GetHealthStartMinutes()) == healthStart(want.HealthStart) {
		return ""
	}
	return "The agent of the node is too old for the start period of the health check, so the server keeps 2 minutes. Update the agent first, then set it in the server's settings."
}
