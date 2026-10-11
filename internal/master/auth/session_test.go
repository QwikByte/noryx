package auth

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/QwikByte/noryx/internal/master/database"
)

func notRequired(context.Context, int64) (bool, error) { return false, nil }

// config is that of the tests: sessions of an hour, the panel's default name, and the limits
// of sign-ins that the settings have by default, unless a test sets others.
type config struct {
	minPassword               int
	setupLink, maxToken, idle time.Duration
}

func (config) SessionTTL() time.Duration           { return time.Hour }
func (config) PanelName() string                   { return "Noryx" }
func (c config) MinPasswordLength() int            { return cmp.Or(c.minPassword, 12) }
func (c config) SetupLinkTTL() time.Duration       { return cmp.Or(c.setupLink, 72*time.Hour) }
func (c config) MaxTokenLifetime() time.Duration   { return c.maxToken }
func (c config) SessionIdleTimeout() time.Duration { return c.idle }

// Users see and end only their own sessions.
func TestSessions(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc, ctx := NewService(db, config{}), t.Context()
	const password = "a-long-enough-password"
	login := func(username string, client Client) (User, string) {
		t.Helper()
		user, token, err := svc.Login(ctx, username, password, "", time.Hour, client)
		if err != nil {
			t.Fatal(err)
		}
		return user, token
	}
	for _, name := range []string{"alice", "bob"} {
		if _, err := svc.CreateUser(ctx, name, password); err != nil {
			t.Fatal(err)
		}
	}
	alice, laptop := login("alice", Client{IP: "192.0.2.1", Browser: "Firefox", OS: "Linux"})
	_, phone := login("alice", Client{IP: "192.0.2.2", Browser: "Safari", OS: "iOS"})
	_, tablet := login("alice", Client{})
	bob, bobs := login("bob", Client{})

	sessions, err := svc.Sessions(ctx, alice.ID, laptop)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 3 || !sessions[0].Current || sessions[0].Browser != "Firefox" || sessions[0].IP != "192.0.2.1" ||
		sessions[1].Current || sessions[0].CreatedAt.IsZero() || sessions[0].ID == "" {
		t.Fatalf("sessions = %+v", sessions)
	}
	others, err := svc.Sessions(ctx, bob.ID, bobs)
	if err != nil {
		t.Fatal(err)
	}
	if len(others) != 1 {
		t.Fatalf("bob's sessions = %+v", others)
	}

	// Alice can't end Bob's session, only her own.
	if err := svc.EndSession(ctx, alice.ID, others[0].ID); !errors.Is(err, errNoSuchSession) {
		t.Fatalf("ended another user's session: %v", err)
	}
	if _, err := svc.Authenticate(ctx, bobs, Client{}); err != nil {
		t.Fatal(err)
	}
	phoneID := sessions[1].ID
	if sessions[2].Browser == "Safari" {
		phoneID = sessions[2].ID
	}
	if err := svc.EndSession(ctx, alice.ID, phoneID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, phone, Client{}); !errors.Is(err, ErrNoSession) {
		t.Fatalf("ended session still valid: %v", err)
	}

	// Signing out everywhere else keeps the current session, and those of other users.
	if err := svc.EndOtherSessions(ctx, alice.ID, laptop); err != nil {
		t.Fatal(err)
	}
	for token, valid := range map[string]bool{laptop: true, tablet: false, bobs: true} {
		if _, err := svc.Authenticate(ctx, token, Client{}); (err == nil) != valid {
			t.Errorf("session valid: %v, want %v", err == nil, valid)
		}
	}
}

// A session notes when and from where it was used at most once a minute.
func TestSessionLastUsed(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc, ctx := NewService(db, config{}), t.Context()
	user, err := svc.CreateUser(ctx, "alice", "a-long-enough-password")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := svc.Login(ctx, "alice", "a-long-enough-password", "", time.Hour, Client{IP: "192.0.2.1"})
	if err != nil {
		t.Fatal(err)
	}
	usedAt := func(ago time.Duration) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `UPDATE sessions SET last_used_at = ?`, time.Now().Add(-ago).Unix()); err != nil {
			t.Fatal(err)
		}
	}
	use := func() Session {
		t.Helper()
		if _, err := svc.Authenticate(ctx, token, Client{IP: "192.0.2.9", Browser: "Chrome"}); err != nil {
			t.Fatal(err)
		}
		sessions, err := svc.Sessions(ctx, user.ID, token)
		if err != nil || len(sessions) != 1 {
			t.Fatalf("sessions = %+v, %v", sessions, err)
		}
		return sessions[0]
	}

	usedAt(30 * time.Second)
	if s := use(); time.Since(s.LastUsedAt) < 25*time.Second || s.IP != "192.0.2.1" {
		t.Fatalf("noted within a minute: %+v", s)
	}
	usedAt(2 * time.Minute)
	if s := use(); time.Since(s.LastUsedAt) > 5*time.Second || s.IP != "192.0.2.9" || s.Browser != "Chrome" {
		t.Fatalf("not noted after a minute: %+v", s)
	}
}

// A session unused for longer than the settings allow ends for good, an API token doesn't.
func TestIdleSessions(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc, ctx := NewService(db, config{idle: time.Hour}), t.Context()
	const password = "a-long-enough-password"
	user, err := svc.CreateUser(ctx, "alice", password)
	if err != nil {
		t.Fatal(err)
	}
	login := func() string {
		t.Helper()
		_, token, err := svc.Login(ctx, "alice", password, "", 3*time.Hour, Client{})
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	usedAt := func(table string, hash []byte, ago time.Duration) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `UPDATE `+table+` SET last_used_at = ? WHERE token_hash = ?`, time.Now().Add(-ago).Unix(), hash); err != nil {
			t.Fatal(err)
		}
	}
	active, idle := login(), login()
	usedAt("sessions", hashToken(active), 59*time.Minute)
	usedAt("sessions", hashToken(idle), 61*time.Minute)
	if sessions, err := svc.Sessions(ctx, user.ID, active); err != nil || len(sessions) != 1 || !sessions[0].Current {
		t.Fatalf("sessions = %+v, %v", sessions, err)
	}
	if _, err := svc.Authenticate(ctx, active, Client{}); err != nil {
		t.Fatalf("session used within the hour: %v", err)
	}
	if _, err := svc.Authenticate(ctx, idle, Client{}); !errors.Is(err, ErrNoSession) {
		t.Fatalf("unused session accepted: %v", err)
	}
	if _, err := NewService(db, config{}).Authenticate(ctx, idle, Client{}); !errors.Is(err, ErrNoSession) {
		t.Fatalf("unused session came back without the limit: %v", err)
	}

	// Signing in deletes the sessions unused for too long, like those that expired.
	forgotten := login()
	usedAt("sessions", hashToken(forgotten), 2*time.Hour)
	login()
	var left int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE token_hash = ?`, hashToken(forgotten)).Scan(&left); err != nil || left != 0 {
		t.Fatalf("%d unused sessions left: %v", left, err)
	}

	_, secret, err := svc.CreateToken(ctx, user.ID, password, "", TokenInput{Name: "script"})
	if err != nil {
		t.Fatal(err)
	}
	usedAt("api_tokens", hashToken(secret), 2*time.Hour)
	if _, err := svc.AuthenticateToken(ctx, secret, "192.0.2.1"); err != nil {
		t.Fatalf("unused token: %v", err)
	}
}

func TestClientOf(t *testing.T) {
	for ua, want := range map[string][2]string{
		"Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0":                                                                 {"Firefox", "Linux"},
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36 Edg/140.0.0.0":          {"Edge", "Windows"},
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15":                  {"Safari", "macOS"},
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/140.0 Mobile/15E148 Safari/604.1": {"Chrome", "iOS"},
		"Mozilla/5.0 (Linux; Android 15) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Mobile Safari/537.36":                           {"Chrome", "Android"},
		"<script>": {"", ""},
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("User-Agent", ua)
		if c := clientOf(r); c.Browser != want[0] || c.OS != want[1] || c.IP != "192.0.2.1" {
			t.Errorf("%s: %+v, want %v", ua, c, want)
		}
	}
}
