package auth

import (
	"context"
	"crypto/rand"
	"net/http"
	"strings"
	"time"

	"github.com/QwikByte/noryx/internal/master/httpapi"
)

// A session notes when and from where it was used at most this often, so that requests don't
// all write to the database.
const touchEvery = time.Minute

var errNoSuchSession = httpapi.Errorf(http.StatusNotFound, "This session has ended already.")

// Session is a sign-in of a user, as the user's account page lists it. Only its own user sees it.
type Session struct {
	// ID names the session. Unlike its token and the token's hash, it can't sign in.
	ID string `json:"id"`
	// CreatedAt is zero for sessions that started before Noryx noted it, LastUsedAt for those
	// not used since.
	CreatedAt  time.Time `json:"createdAt,omitzero"`
	LastUsedAt time.Time `json:"lastUsedAt,omitzero"`
	ExpiresAt  time.Time `json:"expiresAt"`
	Client
	// Current marks the session of the request.
	Current bool `json:"current"`
}

// Client is where a session was used from: the client's address, and the browser and operating
// system its User-Agent names, if the master recognised them. They are only shown, never trusted.
type Client struct {
	IP      string `json:"ip,omitempty"`
	Browser string `json:"browser,omitempty"`
	OS      string `json:"os,omitempty"`
}

// clientOf returns the client of a request.
func clientOf(r *http.Request) Client {
	ua := r.UserAgent()
	return Client{IP: cut(ClientIP(r), 64), Browser: recognise(ua, browsers), OS: recognise(ua, systems)}
}

// browsers and systems are the names of browsers and operating systems by a token of the
// User-Agent. The first one that matches counts, as browsers also name others, e.g. Edge
// Chrome and Safari, and systems too, e.g. Android Linux.
var (
	browsers = [][2]string{
		{"Edg", "Edge"}, {"OPR/", "Opera"}, {"Vivaldi/", "Vivaldi"}, {"SamsungBrowser/", "Samsung Internet"},
		{"Firefox/", "Firefox"}, {"FxiOS/", "Firefox"}, {"Chromium/", "Chromium"}, {"Chrome/", "Chrome"},
		{"CriOS/", "Chrome"}, {"Safari/", "Safari"},
	}
	systems = [][2]string{
		{"Windows", "Windows"}, {"iPhone", "iOS"}, {"iPad", "iPadOS"}, {"Mac OS X", "macOS"}, {"CrOS", "ChromeOS"},
		{"Android", "Android"}, {"Linux", "Linux"},
	}
)

func recognise(userAgent string, names [][2]string) string {
	for _, n := range names {
		if strings.Contains(userAgent, n[0]) {
			return n[1]
		}
	}
	return ""
}

func cut(s string, n int) string { return s[:min(len(s), n)] }

func (s *Service) startSession(ctx context.Context, userID int64, ttl time.Duration, client Client) (string, error) {
	now := time.Now()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ? OR last_used_at <= ?`, now.Unix(), s.unusedSince(now)); err != nil {
		return "", err
	}
	token := rand.Text()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions (token_hash, id, user_id, created_at, last_used_at, expires_at, ip, browser, os) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		hashToken(token), strings.ToLower(rand.Text()), userID, now.Unix(), now.Unix(), now.Add(ttl).Unix(), client.IP, client.Browser, client.OS)
	return token, err
}

// Sessions returns the valid sessions of a user, the one identified by current first, then
// the ones used last.
func (s *Service) Sessions(ctx context.Context, userID int64, current string) ([]Session, error) {
	now := time.Now()
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, created_at, last_used_at, expires_at, ip, browser, os, token_hash = ? AS current FROM sessions
		WHERE user_id = ? AND expires_at > ? AND last_used_at > ? ORDER BY current DESC, last_used_at DESC, created_at DESC`,
		hashToken(current), userID, now.Unix(), s.unusedSince(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sessions := []Session{}
	for rows.Next() {
		var ss Session
		var created, used, expires int64
		if err := rows.Scan(&ss.ID, &created, &used, &expires, &ss.IP, &ss.Browser, &ss.OS, &ss.Current); err != nil {
			return nil, err
		}
		ss.CreatedAt, ss.LastUsedAt, ss.ExpiresAt = unixOrZero(created), unixOrZero(used), time.Unix(expires, 0)
		sessions = append(sessions, ss)
	}
	return sessions, rows.Err()
}

func unixOrZero(sec int64) time.Time {
	if sec == 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}

// EndSession ends a session of a user, e.g. in a lost browser. Sessions of other users
// aren't found.
func (s *Service) EndSession(ctx context.Context, userID int64, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ? AND user_id = ?`, id, userID)
	if err == nil && rowsAffected(res) == 0 {
		err = errNoSuchSession
	}
	return err
}

// EndOtherSessions ends the sessions of a user but the one identified by keep.
func (s *Service) EndOtherSessions(ctx context.Context, userID int64, keep string) error {
	_, err := s.db.ExecContext(ctx, endOtherSessions, userID, hashToken(keep))
	return err
}

// endOtherSessions deletes the sessions of a user but the one with a token hash.
const endOtherSessions = `DELETE FROM sessions WHERE user_id = ? AND token_hash != ?`

// unusedSince is the time, in seconds since 1970, up to which a session's last use means that it
// has been unused for longer than the settings allow; without such a limit, it is before all.
func (s *Service) unusedSince(now time.Time) int64 {
	if idle := s.limits.SessionIdleTimeout(); idle > 0 {
		return now.Add(-idle).Unix()
	}
	return -1
}

// touch notes that a session was used now from client, if it wasn't within touchEvery.
func (s *Service) touch(ctx context.Context, hash []byte, lastUsed int64, client Client) error {
	now := time.Now()
	if now.Sub(time.Unix(lastUsed, 0)) < touchEvery {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET last_used_at = ?, ip = ?, browser = ?, os = ? WHERE token_hash = ?`,
		now.Unix(), client.IP, client.Browser, client.OS, hash)
	return err
}
