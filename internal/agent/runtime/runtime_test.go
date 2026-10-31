package runtime

import (
	"context"
	"errors"
	"slices"
	"testing"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
)

// consoleRuntime records console commands and answers save query as not ready a number of times.
type consoleRuntime struct {
	Runtime
	commands []string
	notReady int
}

func (r *consoleRuntime) SendCommand(_ context.Context, _, command string) (string, error) {
	r.commands = append(r.commands, command)
	if command == "save query" && r.notReady > 0 {
		r.notReady--
		return "", ErrNotReady
	}
	return "", nil
}

// A running Bedrock server holds saving until its files are ready, and resumes it afterwards; a
// Java server turns saving off and flushes its worlds.
func TestPauseSaving(t *testing.T) {
	saveQueryInterval = 0
	for _, tc := range []struct {
		typ      noryxv1.ServerType
		notReady int
		want     []string
		err      bool
	}{
		{noryxv1.ServerType_SERVER_TYPE_PAPER, 0, []string{"save-off", "save-all flush", "save-on"}, false},
		{noryxv1.ServerType_SERVER_TYPE_BEDROCK, 2, []string{"save hold", "save query", "save query", "save query", "save resume"}, false},
		{noryxv1.ServerType_SERVER_TYPE_BEDROCK, saveQueries, append(append([]string{"save hold"}, slices.Repeat([]string{"save query"}, saveQueries)...), "save resume"), true},
	} {
		rt := &consoleRuntime{notReady: tc.notReady}
		resume, err := PauseSaving(t.Context(), rt, Server{Spec: Spec{ID: "s", Type: tc.typ}, State: noryxv1.ServerState_SERVER_STATE_RUNNING})
		if tc.err != errors.Is(err, ErrNotReady) {
			t.Fatalf("%v: %v", tc.typ, err)
		}
		if err == nil {
			resume()
		}
		if !slices.Equal(rt.commands, tc.want) {
			t.Errorf("%v: commands %q, want %q", tc.typ, rt.commands, tc.want)
		}
	}
}
