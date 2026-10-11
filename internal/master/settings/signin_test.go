package settings

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/QwikByte/noryx/internal/master/database"
)

// noTokens has no API tokens that expire later than the settings allow.
type noTokens struct{}

func (noTokens) TokensOverLimit(context.Context) (int, error) { return 0, nil }

// The limits of sign-ins never make them weaker than they always were, but setup links may be
// valid for up to a week. The page of setup links reads the minimum length of passwords and the
// validity of the links without a session.
func TestSignInLimits(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := Load(t.Context(), db, Master{PanelDefaultAddr: "127.0.0.1:0"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.MinPasswordLength() != 12 || s.SetupLinkTTL() != 72*time.Hour || s.MaxTokenLifetime() != 0 || s.SessionIdleTimeout() != 0 {
		t.Fatalf("defaults = %+v", s.Get())
	}
	update := func(change func(*Settings)) error {
		next := s.Get()
		change(&next)
		_, err := s.Update(t.Context(), next)
		return err
	}
	for name, change := range map[string]func(*Settings){
		"passwords of 11 characters":        func(s *Settings) { s.MinPasswordLength = 11 },
		"passwords of 65 characters":        func(s *Settings) { s.MinPasswordLength = 65 },
		"setup links valid for no time":     func(s *Settings) { s.SetupLinkHours = 0 },
		"setup links valid for 8 days":      func(s *Settings) { s.SetupLinkHours = 8 * 24 },
		"tokens of -1 days":                 func(s *Settings) { s.MaxTokenDays = -1 },
		"tokens of 3651 days":               func(s *Settings) { s.MaxTokenDays = maxTokenDays + 1 },
		"sessions unused for -1 hours":      func(s *Settings) { s.SessionIdleHours = -1 },
		"sessions unused for longer at all": func(s *Settings) { s.SessionIdleHours = s.SessionHours + 1 },
	} {
		if err := update(change); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	err = update(func(s *Settings) {
		s.MinPasswordLength, s.SetupLinkHours, s.MaxTokenDays, s.SessionHours, s.SessionIdleHours = 64, 1, 90, 8, 8
	})
	if err != nil || s.MinPasswordLength() != 64 || s.SetupLinkTTL() != time.Hour || s.MaxTokenLifetime() != 90*24*time.Hour || s.SessionIdleTimeout() != 8*time.Hour {
		t.Fatalf("update: %v, settings = %+v", err, s.Get())
	}

	mux := http.NewServeMux()
	NewHandler(s, noTokens{}, nil).RegisterPublic(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/panel", nil))
	var public struct{ MinPasswordLength, SetupLinkHours int }
	if err := json.Unmarshal(rec.Body.Bytes(), &public); err != nil || public.MinPasswordLength != 64 || public.SetupLinkHours != 1 {
		t.Fatalf("public = %+v, %v", public, err)
	}
}
