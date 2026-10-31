package schedule

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/tag"
)

// secretKind keeps a secret, which its runs report as a step that concerns no server.
type secretKind struct {
	fakeKind
	secrets chan string
}

func (secretKind) CheckSecret(secret string) error {
	if len(secret) < 8 {
		return httpapi.Errorf(http.StatusBadRequest, "Enter at least 8 characters.")
	}
	return nil
}

func (secretKind) TargetsOptional(json.RawMessage) bool { return true }

func (k secretKind) Run(_ context.Context, t Task, _ Servers, _ time.Time) error {
	k.secrets <- t.Secret()
	return t.ReportChange(logging.System, "Keep the secret", "kept it", nil)
}

// A kind's secret is kept apart from the settings, never returned, kept by changes that don't
// send one and removed by an empty one. A kind of one task at most refuses a second.
func TestSecrets(t *testing.T) {
	db := openDB(t)
	kind := secretKind{secrets: make(chan string, 1)}
	// The database allows one task of this kind.
	const single = "master-backup"
	s := NewService(db, &fakeNodes{}, tag.NewStore(db), fakeNetworks{}, access.NewService(db),
		map[string]Kind{single: kind, "fake": fakeKind{}}, func(string) bool { return false })
	secret := func(v string) *string { return &v }
	in := Input{Name: "Secretive", Schedule: Schedule{Times: []string{"04:00"}, TimeZone: "UTC"}, Settings: json.RawMessage(`{}`), Secret: secret("s3cr3t-value")}

	if _, err := s.Create(t.Context(), "fake", Input{Name: "Other", Schedule: in.Schedule, Targets: []Target{{NodeID: "n1"}}, Settings: in.Settings, Secret: secret("s3cr3t-value")},
		Author{}); err == nil || !strings.Contains(err.Error(), "keeps no secret") {
		t.Errorf("a secret for a kind without: %v", err)
	}
	if _, err := s.Create(t.Context(), single, Input{Name: in.Name, Schedule: in.Schedule, Settings: in.Settings, Secret: secret("short")}, Author{}); err == nil {
		t.Error("a secret its kind refuses was saved")
	}
	task, err := s.Create(t.Context(), single, in, Author{})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(task)
	if !task.HasSecret || strings.Contains(string(data), "s3cr3t") {
		t.Fatalf("task = %s", data)
	}
	if _, err := s.Create(t.Context(), single, Input{Name: "Second", Schedule: in.Schedule, Settings: in.Settings}, Author{}); err == nil || !strings.Contains(err.Error(), "There is one already") {
		t.Errorf("a second task of a kind of one: %v", err)
	}

	in.Secret = nil // keeps it
	if task, err = s.Update(t.Context(), single, task.ID, in, Author{}); err != nil || !task.HasSecret {
		t.Fatalf("change without a secret: %+v, %v", task, err)
	}
	if _, err := s.RunNow(t.Context(), single, task.ID, Author{}); err != nil {
		t.Fatal(err)
	}
	if got := <-kind.secrets; got != "s3cr3t-value" {
		t.Errorf("the run got the secret %q", got)
	}
	var runs []Run
	for deadline := time.Now().Add(5 * time.Second); len(runs) == 0 && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if runs, err = s.Runs(t.Context(), single, task.ID); err != nil {
			t.Fatal(err)
		}
	}
	if len(runs) != 1 || len(runs[0].Steps) != 1 || runs[0].Steps[0] != (Step{Action: "Keep the secret", Outcome: Succeeded, Change: "kept it"}) {
		t.Fatalf("runs = %+v", runs)
	}

	in.Secret = secret("")
	if task, err = s.Update(t.Context(), single, task.ID, in, Author{}); err != nil || task.HasSecret || task.Secret() != "" {
		t.Fatalf("change that removes the secret: %+v, %v", task, err)
	}
}
