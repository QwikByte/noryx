package auth

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/QwikByte/noryx/internal/master/database"
)

func TestPasswordHash(t *testing.T) {
	hash := hashPassword("correct horse battery staple")
	if !verifyPassword(hash, "correct horse battery staple") {
		t.Fatal("correct password rejected")
	}
	if verifyPassword(hash, "wrong password") || verifyPassword("garbage", "x") {
		t.Fatal("wrong password accepted")
	}
}

func TestLoginSessionLifecycle(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc, ctx := NewService(db, config{}), t.Context()

	if _, err := svc.CreateUser(ctx, "admin", "short"); err == nil {
		t.Fatal("short password accepted")
	}
	if _, err := svc.CreateUser(ctx, "admin", "a-long-enough-password"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Login(ctx, "admin", "wrong-password!", "", time.Hour, Client{}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password: got %v", err)
	}
	if _, _, err := svc.Login(ctx, "nobody", "a-long-enough-password", "", time.Hour, Client{}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown user: got %v", err)
	}

	_, token, err := svc.Login(ctx, "ADMIN", "a-long-enough-password", "", time.Hour, Client{})
	if err != nil {
		t.Fatal(err)
	}
	// Sessions last as long as the settings said when signing in.
	_, expired, err := svc.Login(ctx, "admin", "a-long-enough-password", "", -time.Second, Client{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, expired, Client{}); !errors.Is(err, ErrNoSession) {
		t.Fatalf("expired session accepted: %v", err)
	}
	if user, err := svc.Authenticate(ctx, token, Client{}); err != nil || user.Username != "admin" {
		t.Fatalf("Authenticate = %+v, %v", user, err)
	}
	if err := svc.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, token, Client{}); !errors.Is(err, ErrNoSession) {
		t.Fatalf("session still valid after logout: %v", err)
	}
}

func TestInviteDisableAndChangePassword(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc, ctx := NewService(db, config{}), t.Context()

	// An invited user can't sign in until the password is set with the setup link.
	user, link, err := svc.Invite(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Login(ctx, "alice", "", "", time.Hour, Client{}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("invited user signed in without a password: %v", err)
	}
	if _, _, err := svc.Setup(ctx, link.Token, "short", time.Hour, Client{}); err == nil {
		t.Fatal("short password accepted")
	}
	_, session, err := svc.Setup(ctx, link.Token, "alices-long-password", time.Hour, Client{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Setup(ctx, link.Token, "another-long-password", time.Hour, Client{}); err == nil {
		t.Fatal("setup link used twice")
	}

	// Changing the password keeps the current session and ends the others.
	_, other, err := svc.Login(ctx, "alice", "alices-long-password", "", time.Hour, Client{})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ChangePassword(ctx, user.ID, "wrong-password-here", "a-brand-new-password", session); err == nil {
		t.Fatal("wrong current password accepted")
	}
	if err := svc.ChangePassword(ctx, user.ID, "alices-long-password", "a-brand-new-password", session); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, session, Client{}); err != nil {
		t.Fatalf("current session ended: %v", err)
	}
	if _, err := svc.Authenticate(ctx, other, Client{}); !errors.Is(err, ErrNoSession) {
		t.Fatalf("other session still valid: %v", err)
	}

	// A setup link no longer works once the user changed the password.
	stale, err := svc.NewSetupLink(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ChangePassword(ctx, user.ID, "a-brand-new-password", "the-newest-password", session); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetupUser(ctx, stale.Token); err == nil {
		t.Fatal("setup link outlived a password change")
	}

	// Disabled users lose their sessions and can't sign in or use setup links.
	reset, err := svc.NewSetupLink(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetDisabled(ctx, user.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, session, Client{}); !errors.Is(err, ErrNoSession) {
		t.Fatalf("session of a disabled user still valid: %v", err)
	}
	if _, _, err := svc.Login(ctx, "alice", "the-newest-password", "", time.Hour, Client{}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("disabled user signed in: %v", err)
	}
	if _, err := svc.SetupUser(ctx, reset.Token); err == nil {
		t.Fatal("setup link of a disabled user accepted")
	}
	if _, _, err := svc.Invite(ctx, "Alice"); err == nil {
		t.Fatal("username taken twice")
	}
}

// New passwords need as many characters as the settings say, also with setup links, which are
// valid as long as the settings say, while passwords set before keep working.
func TestPasswordLength(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := t.Context()
	if _, err := NewService(db, config{}).CreateUser(ctx, "alice", "twelve-chars"); err != nil {
		t.Fatal(err)
	}
	svc := NewService(db, config{minPassword: 16, setupLink: time.Hour})
	// Characters count, not bytes.
	for _, short := range []string{"fifteen-chars!!", "äöüäöüäöüäöüäöü"} {
		if _, err := svc.CreateUser(ctx, "bob", short); err == nil {
			t.Errorf("%q accepted", short)
		}
	}
	if _, err := svc.CreateUser(ctx, "bob", "äöüäöüäöüäöüäöüß"); err != nil {
		t.Fatal(err)
	}

	alice, session, err := svc.Login(ctx, "alice", "twelve-chars", "", time.Hour, Client{})
	if err != nil {
		t.Fatalf("password set before: %v", err)
	}
	if err := svc.ChangePassword(ctx, alice.ID, "twelve-chars", "fifteen-chars!!", session); err == nil {
		t.Fatal("short new password accepted")
	}
	link, err := svc.NewSetupLink(ctx, alice.ID)
	if valid := time.Until(link.ExpiresAt); err != nil || valid > time.Hour || valid < 59*time.Minute {
		t.Fatalf("setup link = %+v, %v", link, err)
	}
	if _, _, err := svc.Setup(ctx, link.Token, "fifteen-chars!!", time.Hour, Client{}); err == nil {
		t.Fatal("short password set with a setup link")
	}
	if _, _, err := svc.Setup(ctx, link.Token, "sixteen-chars!!!", time.Hour, Client{}); err != nil {
		t.Fatal(err)
	}
}

// The language a user chose comes with each sign-in and session, to every browser.
func TestLanguage(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc, ctx := NewService(db, config{}), t.Context()
	user, err := svc.CreateUser(ctx, "admin", "a-long-enough-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"deu", "DE", "de-XX", "xx", "../en"} {
		if err := svc.SetLanguage(ctx, user.ID, bad); err == nil {
			t.Errorf("language %q accepted", bad)
		}
	}
	if err := svc.SetLanguage(ctx, user.ID, "de"); err != nil {
		t.Fatal(err)
	}
	user, token, err := svc.Login(ctx, "admin", "a-long-enough-password", "", time.Hour, Client{})
	if err != nil || user.Language != "de" {
		t.Fatalf("sign-in: %+v, %v", user, err)
	}
	if user, err := svc.Authenticate(ctx, token, Client{}); err != nil || user.Language != "de" {
		t.Fatalf("session: %+v, %v", user, err)
	}
	if err := svc.SetLanguage(ctx, user.ID, ""); err != nil {
		t.Fatal(err)
	}
	if user, err := svc.Authenticate(ctx, token, Client{}); err != nil || user.Language != "" {
		t.Fatalf("after following the browser again: %+v, %v", user, err)
	}
}
