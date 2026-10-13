package access

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QwikByte/noryx/internal/master/database"
)

// Saving and deleting a group wait for the changes of users and groups before them, so that
// the check of what the group grants and its change can't interleave with another change.
func TestGroupChangesWait(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := NewHandler(NewService(db), nil, nil)
	call := func(handle http.HandlerFunc, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(WithGrants(t.Context(), Admin()), method, path, strings.NewReader(body))
		if id, ok := strings.CutPrefix(path, "/api/groups/"); ok {
			r.SetPathValue("id", id)
		}
		w := httptest.NewRecorder()
		handle(w, r)
		return w
	}
	waits := func(name string, change func() *httptest.ResponseRecorder, want int) *httptest.ResponseRecorder {
		t.Helper()
		h.changes.Lock() // another change runs
		done := make(chan *httptest.ResponseRecorder)
		go func() { done <- change() }()
		select {
		case <-done:
			t.Fatalf("%s didn't wait for the change before it", name)
		case <-time.After(50 * time.Millisecond):
		}
		h.changes.Unlock()
		w := <-done
		if w.Code != want {
			t.Fatalf("%s: status %d, want %d: %s", name, w.Code, want, w.Body)
		}
		return w
	}

	w := waits("saving", func() *httptest.ResponseRecorder {
		return call(h.saveGroup, "POST", "/api/groups", `{"name":"Moderators","permissions":["servers.restart"],"allServers":true}`)
	}, http.StatusCreated)
	var group Group
	if err := json.Unmarshal(w.Body.Bytes(), &group); err != nil {
		t.Fatal(err)
	}
	waits("deleting", func() *httptest.ResponseRecorder {
		return call(h.deleteGroup, "DELETE", "/api/groups/"+group.ID, "")
	}, http.StatusNoContent)
}
