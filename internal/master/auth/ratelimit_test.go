package auth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/ratelimit"
)

// Behind a trusted proxy, clients have budgets of their own, and guessing the password of
// one account from many addresses ends with that account's budget.
func TestSignInLimits(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := NewService(db)
	for _, name := range []string{"alice", "bob"} {
		if _, err := svc.CreateUser(t.Context(), name, name+"s-long-password"); err != nil {
			t.Fatal(err)
		}
	}
	h := NewHandler(svc, config{}, notRequired)
	// No attempt comes back during the test, however slow hashing passwords is.
	h.clients, h.usernames = ratelimit.New(clientBurst, time.Hour), ratelimit.New(usernameBurst, time.Hour)
	mux := http.NewServeMux()
	h.RegisterPublic(mux)
	proxies, _ := ParseProxies([]string{"127.0.0.1"})
	login := func(client, username, password string) int {
		r := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(fmt.Sprintf(`{"username":%q,"password":%q}`, username, password)))
		r.RemoteAddr = "127.0.0.1:4567"
		r.Header.Set("X-Forwarded-For", client)
		rec := httptest.NewRecorder()
		proxies.Handler(mux).ServeHTTP(rec, r)
		return rec.Code
	}

	// A single client can't keep a user out.
	for range clientBurst {
		if code := login("192.0.2.1", "alice", "wrong"); code != http.StatusUnauthorized {
			t.Fatalf("wrong password: %d", code)
		}
	}
	if code := login("192.0.2.1", "alice", "alices-long-password"); code != http.StatusTooManyRequests {
		t.Fatalf("client over its budget: %d", code)
	}
	if code := login("192.0.2.2", "alice", "alices-long-password"); code != http.StatusOK {
		t.Fatalf("user after a single client guessed: %d", code)
	}

	// Many clients can't guess the password of an account for long.
	for i := range usernameBurst - clientBurst - 1 {
		if code := login(fmt.Sprint("203.0.113.", i), "ALICE", "wrong"); code != http.StatusUnauthorized {
			t.Fatalf("wrong password %d: %d", i, code)
		}
	}
	if code := login("198.51.100.1", "alice", "alices-long-password"); code != http.StatusTooManyRequests {
		t.Fatalf("guessed account: %d", code)
	}
	if code := login("198.51.100.1", "bob", "bobs-long-password"); code != http.StatusOK {
		t.Fatalf("other account: %d", code)
	}

	// The addresses of an IPv6 network share a budget.
	for i := range clientBurst {
		if code := login(fmt.Sprintf("2001:db8::%d", i+1), fmt.Sprint("nobody", i), "wrong"); code != http.StatusUnauthorized {
			t.Fatalf("wrong username %d: %d", i, code)
		}
	}
	if code := login("2001:db8::ff", "bob", "bobs-long-password"); code != http.StatusTooManyRequests {
		t.Fatalf("same network: %d", code)
	}
	if code := login("2001:db8:0:1::1", "bob", "bobs-long-password"); code != http.StatusOK {
		t.Fatalf("other network: %d", code)
	}
}
