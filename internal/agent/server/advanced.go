package server

import (
	"fmt"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/runtime"
)

// checkAdvanced returns a message for the operator if an advanced setting of a server is invalid:
// the start period of its health check, and when the agent stops it after crashes or restarts it
// as it stays unhealthy.
func checkAdvanced(spec runtime.Spec) string {
	switch {
	case !noryxv1.ValidHealthStart(spec.HealthStart):
		return fmt.Sprintf("Give the server %.0f to %.0f minutes to start.", noryxv1.DefaultHealthStart.Minutes(), noryxv1.MaxHealthStart.Minutes())
	case spec.Type.Proxy() && noryxv1.HealthStart(spec.HealthStart) != noryxv1.DefaultHealthStart:
		return "Proxies keep the start period of their health check."
	case !noryxv1.ValidCrashLimit(spec.CrashLimit):
		return fmt.Sprintf("Stop the server after 1 to %d crashes in a row.", noryxv1.MaxCrashLimit)
	case !noryxv1.ValidStableAfter(spec.StableAfter):
		return fmt.Sprintf("Count a run as stable after 1 to %.0f minutes.", noryxv1.MaxStableAfter.Minutes())
	case !noryxv1.ValidUnhealthyRestart(spec.UnhealthyRestart):
		return fmt.Sprintf("Restart an unhealthy server after 1 to %.0f minutes, or never.", noryxv1.MaxUnhealthyRestart.Minutes())
	}
	return ""
}
