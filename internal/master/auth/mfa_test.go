package auth

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QwikByte/noryx/internal/master/database"
)

// The test vectors of RFC 6238 for SHA-1, cut to 6 digits.
func TestTOTP(t *testing.T) {
	key := []byte("12345678901234567890")
	for unix, want := range map[int64]string{
		59: "287082", 1111111109: "081804", 1111111111: "050471", 1234567890: "005924", 2000000000: "279037", 20000000000: "353130",
	} {
		if got := totp(key, unix/totpPeriod); got != want {
			t.Errorf("totp at %d = %s, want %s", unix, got, want)
		}
	}

	secret, now := secretEncoding.EncodeToString(key), time.Unix(1111111111, 0)
	step := now.Unix() / totpPeriod
	for offset, ok := range map[int64]bool{-2: false, -1: true, 0: true, 1: true, 2: false} {
		if _, got := matchTOTP(secret, totp(key, step+offset), now, 0); got != ok {
			t.Errorf("code of step %+d accepted: %v", offset, got)
		}
	}
	if _, ok := matchTOTP(secret, totp(key, step), now, step); ok {
		t.Error("used code accepted again")
	}
	if uri := totpURI("Noryx", "alice", secret); uri != "otpauth://totp/Noryx:alice?issuer=Noryx&secret="+secret {
		t.Errorf("uri = %s", uri)
	}
	// The name of the panel is escaped, and only the issuer keeps its colons.
	if uri := totpURI("Test: Noryx · 50% & more?#", "alice", secret); uri != "otpauth://totp/Test%20Noryx%20%C2%B7%2050%25%20&%20more%3F%23:alice?issuer=Test%3A%20Noryx%20%C2%B7%2050%25%20%26%20more%3F%23&secret="+secret {
		t.Errorf("uri = %s", uri)
	}
}

func TestMFA(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc, ctx := NewService(db), t.Context()
	const password = "alices-long-password"
	user, err := svc.CreateUser(ctx, "alice", password)
	if err != nil {
		t.Fatal(err)
	}
	login := func(code string) error {
		_, _, err := svc.Login(ctx, "alice", password, code, time.Hour, Client{})
		return err
	}

	// Two-factor authentication is off until a code of the app confirms the setup.
	_, other, err := svc.Login(ctx, "alice", password, "", time.Hour, Client{})
	if err != nil {
		t.Fatal(err)
	}
	_, keep, err := svc.Login(ctx, "alice", password, "", time.Hour, Client{})
	if err != nil {
		t.Fatal(err)
	}
	setup, err := svc.SetUpMFA(ctx, user, "Noryx")
	if err != nil {
		t.Fatal(err)
	}
	if err := login(""); err != nil {
		t.Fatalf("unfinished setup needs a code: %v", err)
	}
	key, err := secretEncoding.DecodeString(setup.Secret)
	if err != nil {
		t.Fatal(err)
	}
	step := time.Now().Unix() / totpPeriod
	if _, err := svc.EnableMFA(ctx, user.ID, "wrong-password", totp(key, step), keep); err == nil {
		t.Fatal("wrong password accepted")
	}
	if _, err := svc.EnableMFA(ctx, user.ID, password, "000000", keep); err == nil {
		t.Fatal("wrong code accepted")
	}
	codes, err := svc.EnableMFA(ctx, user.ID, password, totp(key, step), keep)
	if err != nil || len(codes) != recoveryCodeCount {
		t.Fatalf("EnableMFA = %v, %v", codes, err)
	}
	if _, err := svc.Authenticate(ctx, other, Client{}); !errors.Is(err, ErrNoSession) {
		t.Fatalf("session without a code still valid: %v", err)
	}
	if _, err := svc.Authenticate(ctx, keep, Client{}); err != nil {
		t.Fatalf("current session ended: %v", err)
	}
	if _, err := svc.SetUpMFA(ctx, user, "Noryx"); !errors.Is(err, errMFAOn) {
		t.Fatalf("setup replaced the secret in use: %v", err)
	}

	// Signing in needs a code that wasn't used yet, or a recovery code once.
	if err := login(""); !errors.Is(err, ErrCodeRequired) {
		t.Fatalf("no code: %v", err)
	}
	if _, _, err := svc.Login(ctx, "alice", "wrong-password", "", time.Hour, Client{}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password: %v", err)
	}
	if err := login(totp(key, step)); !errors.Is(err, errWrongCode) {
		t.Fatalf("code used to set up accepted: %v", err)
	}
	if err := login(totp(key, step+1)); err != nil {
		t.Fatal(err)
	}
	if err := login(strings.ToLower(strings.ReplaceAll(codes[0], "-", ""))); err != nil {
		t.Fatalf("recovery code: %v", err)
	}
	if err := login(codes[0]); !errors.Is(err, errWrongCode) {
		t.Fatalf("recovery code used twice: %v", err)
	}
	if m, err := svc.MFA(ctx, user.ID); err != nil || !m.Enabled || m.RecoveryCodes != recoveryCodeCount-1 {
		t.Fatalf("MFA = %+v, %v", m, err)
	}

	// New recovery codes replace the old ones.
	fresh, err := svc.NewRecoveryCodes(ctx, user.ID, password)
	if err != nil {
		t.Fatal(err)
	}
	if err := login(codes[1]); !errors.Is(err, errWrongCode) {
		t.Fatalf("replaced recovery code accepted: %v", err)
	}
	if err := login(fresh[0]); err != nil {
		t.Fatal(err)
	}

	// Wrong codes in a row lock the code check, also for the right code.
	for range maxCodeFailures - 1 {
		if err := login("123456"); !errors.Is(err, errWrongCode) {
			t.Fatalf("wrong code: %v", err)
		}
	}
	if err := login(fresh[1]); err != nil {
		t.Fatalf("right code after %d wrong ones: %v", maxCodeFailures-1, err)
	}
	for range maxCodeFailures {
		_ = login("123456")
	}
	if err := login(fresh[2]); !errors.Is(err, errCodeLocked) {
		t.Fatalf("right code after %d wrong ones: %v", maxCodeFailures, err)
	}

	// A setup link sets the password, but doesn't sign in without a code.
	link, err := svc.NewSetupLink(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, session, err := svc.Setup(ctx, link.Token, password, time.Hour, Client{}); err != nil || session != "" {
		t.Fatalf("Setup = %q, %v", session, err)
	}

	// Turning it off needs the password, unless an administrator resets it.
	if err := svc.DisableMFA(ctx, user.ID, "wrong-password"); err == nil {
		t.Fatal("wrong password accepted")
	}
	if err := svc.ResetMFA(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	if err := login(""); err != nil {
		t.Fatalf("code needed after reset: %v", err)
	}
	if err := svc.DisableMFA(ctx, user.ID, password); !errors.Is(err, errMFAOff) {
		t.Fatalf("DisableMFA when off: %v", err)
	}
	if accounts, err := svc.Accounts(ctx); err != nil || accounts[0].MFA {
		t.Fatalf("Accounts = %+v, %v", accounts, err)
	}
}

func TestLockout(t *testing.T) {
	for failures, want := range map[int]time.Duration{
		maxCodeFailures - 1: 0, maxCodeFailures: time.Minute, maxCodeFailures + 3: 8 * time.Minute, 1000: 24 * time.Hour,
	} {
		if got := lockout(failures); got != want {
			t.Errorf("lockout(%d) = %v, want %v", failures, got, want)
		}
	}
}
