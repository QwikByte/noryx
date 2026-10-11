package auth

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/QwikByte/noryx/internal/buildinfo"
	"github.com/QwikByte/noryx/internal/master/database"
)

// Signed-in users learn the master's version with every answer, others don't.
func TestRequireTellsVersion(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc, ctx := NewService(db), t.Context()
	if _, err := svc.CreateUser(ctx, "admin", "a-long-enough-password"); err != nil {
		t.Fatal(err)
	}
	_, token, err := svc.Login(ctx, "admin", "a-long-enough-password", "", time.Hour, Client{})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(svc, config{}, notRequired).Require(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	for cookie, want := range map[string]string{token: buildinfo.Version, "wrong": "", "": ""} {
		req := httptest.NewRequest(http.MethodGet, "/api/servers", nil)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if got := rec.Header().Get(VersionHeader); got != want {
			t.Errorf("cookie %q: version %q, want %q", cookie, got, want)
		}
	}
}
