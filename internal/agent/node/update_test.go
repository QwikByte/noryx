package node

import (
	"os"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/buildinfo"
)

// An agent installs pre-releases newer than itself only with --prereleases, and then, like
// releases, only from its package, which it lacks here.
func TestUpdatePrereleases(t *testing.T) {
	if _, err := os.Stat(installer); err == nil {
		t.Skip("the agent's package is installed, so the test would update this machine")
	}
	defer func(v string) { buildinfo.Version = v }(buildinfo.Version)
	buildinfo.Version = "v1.2.0"
	for _, tc := range []struct {
		version     string
		prereleases bool
		want        codes.Code
	}{
		{"v1.3.0-rc.1", false, codes.InvalidArgument},
		{"v1.3.0-rc.1", true, codes.FailedPrecondition},
		{"v1.2.0-rc.1", true, codes.InvalidArgument}, // older than v1.2.0
		{"v1.2.1", false, codes.FailedPrecondition},
	} {
		_, err := (&Service{Prereleases: tc.prereleases}).Update(t.Context(), &noryxv1.UpdateRequest{Version: tc.version})
		if got := status.Code(err); got != tc.want {
			t.Errorf("update to %s with pre-releases %t: got %v, want %v", tc.version, tc.prereleases, got, tc.want)
		}
	}
}
