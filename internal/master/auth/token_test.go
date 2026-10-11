package auth

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/ratelimit"
)

// Tokens are stored as hashes, need the password and with two-factor authentication a code,
// and stop working once they expire, are revoked, their user's credentials change or their
// user is disabled or deleted.
func TestTokens(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc, ctx := NewService(db), t.Context()
	const password = "alices-long-password"
	alice, err := svc.CreateUser(ctx, "alice", password)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := svc.CreateUser(ctx, "bob", password)
	if err != nil {
		t.Fatal(err)
	}
	create := func(user User, in TokenInput) (Token, string) {
		t.Helper()
		token, secret, err := svc.CreateToken(ctx, user.ID, password, "", in)
		if err != nil {
			t.Fatal(err)
		}
		return token, secret
	}
	works := func(secret string) bool {
		_, err := svc.AuthenticateToken(ctx, secret, "192.0.2.1")
		if err != nil && !errors.Is(err, errNoToken) {
			t.Fatal(err)
		}
		return err == nil
	}

	// Inputs are checked before the password, which is needed.
	for _, in := range []TokenInput{
		{Name: " "}, {Name: strings.Repeat("x", 65)}, {Name: "a\nb"}, {Name: "past", ExpiresAt: time.Now().Add(-time.Minute)},
		{Name: "none", Permissions: []string{}}, {Name: "odd", Permissions: []string{"servers.start; DROP"}},
	} {
		if _, _, err := svc.CreateToken(ctx, alice.ID, password, "", in); err == nil {
			t.Errorf("token %+v created", in)
		}
	}
	if _, _, err := svc.CreateToken(ctx, alice.ID, "wrong-password", "", TokenInput{Name: "script"}); err == nil {
		t.Fatal("wrong password accepted")
	}

	token, secret := create(alice, TokenInput{Name: " backup script ", Permissions: []string{"servers.start", "backups.create", "servers.start"}})
	if !strings.HasPrefix(secret, TokenPrefix) || token.Name != "backup script" || !slices.Equal(token.Permissions, []string{"backups.create", "servers.start"}) ||
		!token.ExpiresAt.IsZero() || token.ID == "" || strings.Contains(secret, token.ID) {
		t.Fatalf("token = %+v, %q", token, secret)
	}
	if _, _, err := svc.CreateToken(ctx, alice.ID, password, "", TokenInput{Name: "backup script"}); err == nil {
		t.Fatal("two tokens of the same name")
	}

	// Only the hash is stored.
	var stored int
	sum := sha256.Sum256([]byte(secret))
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_tokens WHERE token_hash = ? AND id = ?`, sum[:], token.ID).Scan(&stored); err != nil || stored != 1 {
		t.Fatalf("stored %d hashes: %v", stored, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_tokens t WHERE instr(t.id || t.name || COALESCE(t.permissions, '') || t.ip, ?) > 0`, secret[len(TokenPrefix):]).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("the token is stored: %d, %v", stored, err)
	}

	// The token authenticates its user, notes where it was used, and is listed for its user only.
	user, err := svc.AuthenticateToken(ctx, secret, "192.0.2.1")
	if err != nil || user.ID != alice.ID || user.Token == nil || user.Token.ID != token.ID || !slices.Equal(user.Token.Permissions, token.Permissions) {
		t.Fatalf("AuthenticateToken = %+v, %v", user, err)
	}
	if works(secret+"x") || works(TokenPrefix) {
		t.Fatal("wrong tokens work")
	}
	tokens, err := svc.Tokens(ctx, alice.ID)
	if err != nil || len(tokens) != 1 || tokens[0].IP != "192.0.2.1" || tokens[0].LastUsedAt.IsZero() || tokens[0].ID != token.ID {
		t.Fatalf("tokens = %+v, %v", tokens, err)
	}
	if bobs, err := svc.Tokens(ctx, bob.ID); err != nil || len(bobs) != 0 {
		t.Fatalf("bob's tokens = %+v, %v", bobs, err)
	}
	if _, err := svc.RevokeToken(ctx, bob.ID, token.ID); !errors.Is(err, errNoSuchToken) || !works(secret) {
		t.Fatalf("bob revoked alice's token: %v", err)
	}

	// Expired tokens stop working, but stay listed until they are revoked.
	soon, expiring := create(alice, TokenInput{Name: "soon", ExpiresAt: time.Now().Add(time.Hour)})
	if !works(expiring) || soon.ExpiresAt.IsZero() {
		t.Fatalf("expiring token = %+v", soon)
	}
	if _, err := db.ExecContext(ctx, `UPDATE api_tokens SET expires_at = ? WHERE id = ?`, time.Now().Unix()-1, soon.ID); err != nil {
		t.Fatal(err)
	}
	if works(expiring) {
		t.Fatal("expired token works")
	}
	if name, err := svc.RevokeToken(ctx, alice.ID, soon.ID); err != nil || name != "soon" {
		t.Fatalf("RevokeToken = %q, %v", name, err)
	}

	// Changing the password revokes the tokens, as whoever knew the old one may have made them.
	_, other := create(alice, TokenInput{Name: "other"})
	if err := svc.ChangePassword(ctx, alice.ID, password, password+"!", ""); err != nil {
		t.Fatal(err)
	}
	if works(secret) || works(other) {
		t.Fatal("tokens work after the password changed")
	}

	// With two-factor authentication, creating a token needs a code too.
	setup, err := svc.SetUpMFA(ctx, bob, "Noryx")
	if err != nil {
		t.Fatal(err)
	}
	key, _ := secretEncoding.DecodeString(setup.Secret)
	step := time.Now().Unix() / totpPeriod
	if _, err := svc.EnableMFA(ctx, bob.ID, password, totp(key, step), ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CreateToken(ctx, bob.ID, password, "", TokenInput{Name: "script"}); !errors.Is(err, errCodeNeeded) {
		t.Fatalf("without a code: %v", err)
	}
	if _, _, err := svc.CreateToken(ctx, bob.ID, password, totp(key, step), TokenInput{Name: "script"}); !errors.Is(err, errWrongCode) {
		t.Fatalf("with a used code: %v", err)
	}
	_, bobs, err := svc.CreateToken(ctx, bob.ID, password, totp(key, step+1), TokenInput{Name: "script"})
	if err != nil || !works(bobs) {
		t.Fatalf("with a code: %v", err)
	}

	// Disabled users' tokens stop working, also when enabled again, and deleted users' go.
	if err := svc.SetDisabled(ctx, bob.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetDisabled(ctx, bob.ID, false); err != nil {
		t.Fatal(err)
	}
	if works(bobs) {
		t.Fatal("token of a disabled user works")
	}
	carol, err := svc.CreateUser(ctx, "carol", password)
	if err != nil {
		t.Fatal(err)
	}
	_, carols := create(carol, TokenInput{Name: "last"})
	if err := svc.Delete(ctx, carol.ID); err != nil {
		t.Fatal(err)
	}
	if works(carols) {
		t.Fatal("token of a deleted user works")
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_tokens`).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("%d tokens left, %v", stored, err)
	}
}

// Only tokens with their prefix in the Authorization header count; wrong ones take attempts
// like wrong passwords, and they can't use the routes of their user's account.
func TestRequireToken(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc, ctx := NewService(db), t.Context()
	const password = "alices-long-password"
	alice, err := svc.CreateUser(ctx, "alice", password)
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := svc.CreateToken(ctx, alice.ID, password, "", TokenInput{Name: "script"})
	if err != nil {
		t.Fatal(err)
	}
	_, session, err := svc.Login(ctx, "alice", password, "", time.Hour, Client{})
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(svc, config{}, notRequired)
	h.clients = ratelimit.New(clientBurst, time.Hour)
	mux := http.NewServeMux()
	h.Register(mux)
	mux.HandleFunc("GET /api/servers", func(w http.ResponseWriter, r *http.Request) {
		user, _ := UserFrom(r.Context())
		if user.Token != nil {
			fmt.Fprint(w, user.Token.Name)
		}
	})
	proxies, _ := ParseProxies([]string{"127.0.0.1"})
	handler := proxies.Handler(h.Require(mux))
	send := func(client, method, path, authorization, cookie string) (int, string) {
		r := httptest.NewRequest(method, path, strings.NewReader(`{"password":"`+password+`","name":"other"}`))
		r.RemoteAddr = "127.0.0.1:4567"
		r.Header.Set("X-Forwarded-For", client)
		if authorization != "" {
			r.Header.Set("Authorization", authorization)
		}
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		return rec.Code, rec.Body.String()
	}

	if code, body := send("192.0.2.1", "GET", "/api/servers", "Bearer "+secret, ""); code != http.StatusOK || body != "script" {
		t.Fatalf("token: %d %s", code, body)
	}
	if code, _ := send("192.0.2.1", "GET", "/api/servers", "bearer "+secret, ""); code != http.StatusOK {
		t.Fatalf("lower case scheme: %d", code)
	}
	// Other credentials, e.g. of a reverse proxy, leave the session alone, which a token can't be.
	if code, body := send("192.0.2.1", "GET", "/api/servers", "Basic YWxpY2U6c2VjcmV0", session); code != http.StatusOK || body != "" {
		t.Fatalf("basic credentials and a session: %d %s", code, body)
	}
	if code, _ := send("192.0.2.1", "GET", "/api/servers", "Bearer other", ""); code != http.StatusUnauthorized {
		t.Fatalf("other bearer token: %d", code)
	}
	if code, _ := send("192.0.2.1", "GET", "/api/servers", "", secret); code != http.StatusUnauthorized {
		t.Fatalf("token as session: %d", code)
	}

	// Tokens can't use the routes of the account, e.g. to create tokens or change the password.
	for _, route := range []string{"GET /api/auth/me", "POST /api/auth/tokens", "PUT /api/auth/password", "POST /api/auth/mfa/setup", "DELETE /api/auth/sessions", "POST /api/auth/logout"} {
		method, path, _ := strings.Cut(route, " ")
		if code, _ := send("192.0.2.1", method, path, "Bearer "+secret, ""); code != http.StatusForbidden {
			t.Errorf("%s with a token: %d", route, code)
		}
	}
	if code, _ := send("192.0.2.1", "POST", "/api/auth/tokens", "", session); code != http.StatusOK {
		t.Fatalf("token created with a session: %d", code)
	}

	// Wrong tokens use up the client's attempts, after which its tokens don't work either.
	for range clientBurst {
		if code, _ := send("192.0.2.2", "GET", "/api/servers", "Bearer "+TokenPrefix+"WRONG", ""); code != http.StatusUnauthorized {
			t.Fatalf("wrong token: %d", code)
		}
	}
	if code, _ := send("192.0.2.2", "GET", "/api/servers", "Bearer "+secret, ""); code != http.StatusTooManyRequests {
		t.Fatalf("client over its budget: %d", code)
	}
	if code, _ := send("192.0.2.3", "GET", "/api/servers", "Bearer "+secret, ""); code != http.StatusOK {
		t.Fatalf("other client: %d", code)
	}
	for range 2 * clientBurst {
		if code, _ := send("192.0.2.3", "GET", "/api/servers", "Bearer "+secret, ""); code != http.StatusOK {
			t.Fatalf("right tokens take attempts: %d", code)
		}
	}
}
