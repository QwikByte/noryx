package backup

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A backup of what can't be backed up, and a restore of paths outside of a server, are refused
// before the agent is asked, naming the field of the request.
func TestRefusalsNameTheField(t *testing.T) {
	h := NewHandler(nil, nil, nil, nil, nil)
	for _, tc := range []struct {
		handler     http.HandlerFunc
		body, field string
	}{
		{h.create, `{"selection": {}}`, "selection"},
		{h.create, `{"selection": {"paths": ["plugins", "../etc"]}}`, "selection.paths"},
		{h.create, `{"selection": {"worlds": true, "exclude": ["/"]}}`, "selection.exclude"},
		{h.restore, `{"paths": ["world/../../etc"]}`, "paths"},
	} {
		rec := httptest.NewRecorder()
		tc.handler(rec, httptest.NewRequest(http.MethodPost, "/api/nodes/n1/servers/s1/backups", strings.NewReader(tc.body)))
		var res struct{ Error, Field string }
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || rec.Code != http.StatusBadRequest || res.Field != tc.field {
			t.Errorf("%s: status %d: %s", tc.body, rec.Code, rec.Body)
		}
	}
}
