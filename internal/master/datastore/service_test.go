package datastore

import (
	"testing"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
)

// TestTaken keeps the parameters that the agent took: none on agents of older versions, which
// answer without them, with a warning if it was asked for some.
func TestTaken(t *testing.T) {
	tuned := Datastore{MaxConnections: 300, BufferMB: 512}
	if ds, warning := taken(tuned, &noryxv1.Datastore{Parameters: tuned.parameters()}); ds.MaxConnections != 300 || ds.BufferMB != 512 || warning != "" {
		t.Errorf("a current agent: %+v, %q", ds, warning)
	}
	if ds, warning := taken(tuned, &noryxv1.Datastore{}); ds.MaxConnections != 0 || ds.BufferMB != 0 || warning == "" {
		t.Errorf("an older agent: %+v, %q", ds, warning)
	}
	if _, warning := taken(Datastore{}, &noryxv1.Datastore{}); warning != "" {
		t.Errorf("an older agent without parameters: %q", warning)
	}
}
