package server

import (
	"fmt"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/runtime"
)

// checkAdvanced returns a message for the operator if an advanced setting of a server is invalid,
// such as the start period of its health check.
func checkAdvanced(spec runtime.Spec) string {
	switch {
	case !noryxv1.ValidHealthStart(spec.HealthStart):
		return fmt.Sprintf("Give the server %.0f to %.0f minutes to start.", noryxv1.DefaultHealthStart.Minutes(), noryxv1.MaxHealthStart.Minutes())
	case spec.Type.Proxy() && noryxv1.HealthStart(spec.HealthStart) != noryxv1.DefaultHealthStart:
		return "Proxies keep the start period of their health check."
	}
	return ""
}
