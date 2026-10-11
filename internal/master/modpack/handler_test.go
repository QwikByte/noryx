package modpack

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/httpapi"
)

// An update backs up as before unless the request chooses, which needs the permission to back
// up the server and is checked like Back up first of schedules; it only skips the backup when
// the request asks for that.
func TestBackupFirst(t *testing.T) {
	admin := access.WithGrants(t.Context(), access.Admin())
	read := func(body string) updateRequest {
		var req updateRequest
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatal(err)
		}
		return req
	}
	status := func(err error) int {
		var apiErr *httpapi.Error
		if !errors.As(err, &apiErr) {
			return 0
		}
		return apiErr.Status
	}

	for _, body := range []string{`{"version":"x"}`, `{"version":"x","noBackup":false}`, `{"version":"x","backup":null}`} {
		b, err := read(body).backupFirst(t.Context(), "n", "s") // needs no permission of its own
		if err != nil || b == nil || !b.Selection.Worlds || !b.Selection.Plugins || !b.Selection.Config || b.Selection.Everything || b.Location != "" {
			t.Errorf("%s: backup = %+v, %v", body, b, err)
		}
	}
	if b, err := read(`{"version":"x","noBackup":true}`).backupFirst(t.Context(), "n", "s"); b != nil || err != nil {
		t.Errorf("no backup = %+v, %v", b, err)
	}

	// A backup made by hand belongs to no job: it neither keeps nor copies, nor dumps datastores.
	chosen := `{"version":"x","backup":{"selection":{"config":true,"paths":["kubejs"]},"location":"archive",` +
		`"keep":3,"keepDays":7,"datastores":["aaaaaaaaaaaaaaaaaaaaaaaaaa"],"copy":{"node":"n"}}}`
	b, err := read(chosen).backupFirst(admin, "n", "s")
	if err != nil || b.Selection.Worlds || !b.Selection.Config || !slices.Equal(b.Selection.Paths, []string{"kubejs"}) || b.Location != "archive" ||
		b.Keep != 0 || b.KeepDays != 0 || len(b.Datastores) != 0 || b.Copy != nil {
		t.Errorf("chosen = %+v, %v", b, err)
	}
	if _, err := read(chosen).backupFirst(t.Context(), "n", "s"); status(err) != http.StatusForbidden {
		t.Errorf("chosen without the permission to back up: %v", err)
	}
	for _, body := range []string{
		`{"version":"x","backup":{"selection":{}}}`,
		`{"version":"x","backup":{"selection":{"paths":["../other"]}}}`,
		`{"version":"x","backup":{"selection":{"everything":true,"exclude":["/"]}}}`,
		`{"version":"x","backup":{"selection":{"everything":true},"location":"Not a location"}}`,
		`{"version":"x","backup":{"selection":{"everything":true}},"noBackup":true}`,
	} {
		if _, err := read(body).backupFirst(admin, "n", "s"); status(err) != http.StatusBadRequest {
			t.Errorf("%s: %v", body, err)
		}
	}
}
