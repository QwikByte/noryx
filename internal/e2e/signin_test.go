package e2e

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	masterapp "github.com/QwikByte/noryx/internal/master/app"
	"github.com/QwikByte/noryx/internal/master/auth"
)

// The settings limit the length of new passwords, the validity of setup links, the lifetime of
// new API tokens and how long sessions may stay unused; the panel learns them where it needs them.
func TestSignInLimits(t *testing.T) {
	m := startMaster(t)
	svc := m.services(t)
	srv := httptest.NewTLSServer(masterapp.Handler(svc))
	t.Cleanup(srv.Close)
	const password = "the-admins-password"
	admin, err := svc.Users.CreateUser(t.Context(), "admin", password)
	check(t, err)
	check(t, svc.Access.MakeAdmin(t.Context(), admin.ID))
	root, visitor := browser(t, srv), browser(t, srv)
	root.do("POST", "/api/auth/login", map[string]string{"username": "admin", "password": password}, http.StatusOK, nil)
	root.do("POST", "/api/auth/tokens", map[string]any{"name": "forever", "password": password}, http.StatusOK, nil)

	root.do("PUT", "/api/settings", map[string]any{"minPasswordLength": 11}, http.StatusBadRequest, nil)
	root.do("PUT", "/api/settings", map[string]any{"sessionIdleHours": 13}, http.StatusBadRequest, nil)
	var view struct{ TokensOverLimit int }
	root.do("PUT", "/api/settings", map[string]any{"minPasswordLength": 24, "setupLinkHours": 1, "maxTokenDays": 30, "sessionIdleHours": 1}, http.StatusOK, &view)
	if view.TokensOverLimit != 1 {
		t.Fatalf("%d tokens over the limit", view.TokensOverLimit)
	}
	type limits struct{ MinPasswordLength, SetupLinkHours, MaxTokenDays, SessionIdleHours int }
	var public, own limits
	visitor.do("GET", "/api/panel", nil, http.StatusOK, &public)
	root.do("GET", "/api/auth/limits", nil, http.StatusOK, &own)
	if public != (limits{MinPasswordLength: 24, SetupLinkHours: 1}) || own != (limits{MinPasswordLength: 24, MaxTokenDays: 30, SessionIdleHours: 1}) {
		t.Fatalf("public = %+v, own = %+v", public, own)
	}

	root.do("POST", "/api/auth/tokens", map[string]any{"name": "forever too", "password": password}, http.StatusBadRequest, nil)
	root.do("POST", "/api/auth/tokens", map[string]any{"name": "month", "expiresAt": time.Now().Add(30 * 24 * time.Hour), "password": password}, http.StatusOK, nil)

	// Invited users set a password of 24 characters within an hour; the administrator's stays.
	var invited struct {
		SetupLink auth.SetupLink `json:"setupLink"`
	}
	root.do("POST", "/api/users", map[string]any{"username": "mod"}, http.StatusCreated, &invited)
	if valid := time.Until(invited.SetupLink.ExpiresAt); valid > time.Hour || valid < 59*time.Minute {
		t.Fatalf("setup link valid for %v", valid)
	}
	mod := browser(t, srv)
	mod.do("POST", "/api/auth/setup", map[string]string{"token": invited.SetupLink.Token, "password": "the-mods-password"}, http.StatusBadRequest, nil)
	mod.do("POST", "/api/auth/setup", map[string]string{"token": invited.SetupLink.Token, "password": "the-mods-password-is-long"}, http.StatusOK, nil)
	visitor.do("POST", "/api/auth/login", map[string]string{"username": "admin", "password": password}, http.StatusOK, nil)

	// A session unused for an hour ends.
	_, err = m.db.ExecContext(t.Context(), `UPDATE sessions SET last_used_at = ? WHERE user_id = ?`, time.Now().Add(-time.Hour).Unix(), admin.ID)
	check(t, err)
	root.do("GET", "/api/auth/me", nil, http.StatusUnauthorized, nil)
	mod.do("GET", "/api/auth/me", nil, http.StatusOK, nil)
}
