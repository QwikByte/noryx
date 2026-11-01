package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/httpapi"
)

// API tokens let scripts use the API as their user, with all of the user's permissions or
// only some, which the access package applies. Only routes registered with access.Mux take
// them; those of the user's own account are the panel's, see SessionOnly.

// TokenPrefix starts every API token, so that secret scanners recognise them.
const TokenPrefix = "noryx_"

const (
	maxTokens     = 100 // of a user
	maxTokenName  = 64
	maxTokenPerms = 100 // permissions of a token
	// A token may be asked to expire up to tokenLeeway later than the settings allow, as the
	// clock of the browser may be ahead; it then expires as late as they allow.
	tokenLeeway = 24 * time.Hour
)

var (
	errNoToken     = httpapi.Errorf(http.StatusUnauthorized, "The API token is invalid, expired or revoked, or its user is disabled.")
	errNoSuchToken = httpapi.Errorf(http.StatusNotFound, "This API token was revoked already.")
	errSessionOnly = httpapi.Errorf(http.StatusForbidden, "API tokens can't use the routes of your account. Sign in to the panel for them.")
	errCodeNeeded  = httpapi.Errorf(http.StatusBadRequest, "Enter a code of your authenticator app too.")
	// errWrongConfirmation is a wrong code of a signed-in user.
	errWrongConfirmation = httpapi.Errorf(http.StatusBadRequest, "The code is incorrect.")

	permissionPattern = regexp.MustCompile(`^[a-z]+\.[a-z]+$`)
)

// Token is an API token as its user's account page lists it. Only its own user sees it.
type Token struct {
	// ID names the token. Unlike the token and its hash, it can't authenticate.
	ID   string `json:"id"`
	Name string `json:"name"`
	// Permissions are those of the user's that the token may use, besides those they
	// require; nil for all of them.
	Permissions []string  `json:"permissions,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	// ExpiresAt is zero for tokens that don't expire, LastUsedAt for those never used.
	ExpiresAt  time.Time `json:"expiresAt,omitzero"`
	LastUsedAt time.Time `json:"lastUsedAt,omitzero"`
	// IP is the address the token was used from last.
	IP string `json:"ip,omitempty"`
}

// TokenInput is a new API token.
type TokenInput struct {
	Name string `json:"name"`
	// ExpiresAt is zero for a token that doesn't expire.
	ExpiresAt time.Time `json:"expiresAt,omitzero"`
	// Permissions are those of the user's that it may use; nil for all of them.
	Permissions []string `json:"permissions"`
}

// check checks a new token, which may last for maxLifetime at most, or for ever if it is 0.
func (in *TokenInput) check(maxLifetime time.Duration) error {
	in.Name = strings.TrimSpace(in.Name)
	now := time.Now()
	latest := now.Add(maxLifetime)
	switch {
	case in.Name == "" || len(in.Name) > maxTokenName || strings.ContainsFunc(in.Name, unicode.IsControl):
		return httpapi.Errorf(http.StatusBadRequest, "Enter a name with up to %d characters.", maxTokenName)
	case !in.ExpiresAt.IsZero() && !in.ExpiresAt.After(now):
		return httpapi.Errorf(http.StatusBadRequest, "Choose an expiry in the future, or none.")
	case maxLifetime > 0 && (in.ExpiresAt.IsZero() || in.ExpiresAt.After(latest.Add(tokenLeeway))):
		return httpapi.Errorf(http.StatusBadRequest, "Choose an expiry within %d days, the longest the settings allow.", int(maxLifetime.Hours()/24))
	case in.Permissions != nil && (len(in.Permissions) == 0 || len(in.Permissions) > maxTokenPerms):
		return httpapi.Errorf(http.StatusBadRequest, "Choose at least one permission, or all of yours.")
	}
	for _, p := range in.Permissions {
		if !permissionPattern.MatchString(p) {
			return httpapi.Errorf(http.StatusBadRequest, "The permission %q doesn't exist.", p)
		}
	}
	slices.Sort(in.Permissions)
	in.Permissions = slices.Compact(in.Permissions)
	if maxLifetime > 0 && in.ExpiresAt.After(latest) {
		in.ExpiresAt = latest
	}
	return nil
}

// CreateToken creates an API token for a user who confirms it with the password and, with
// two-factor authentication, a code. It returns the token itself, which is shown only now.
func (s *Service) CreateToken(ctx context.Context, userID int64, password, code string, in TokenInput) (Token, string, error) {
	if err := in.check(s.limits.MaxTokenLifetime()); err != nil {
		return Token{}, "", err
	}
	if err := s.confirmPassword(ctx, userID, password); err != nil {
		return Token{}, "", err
	}
	if err := s.confirmCode(ctx, userID, code); err != nil {
		return Token{}, "", err
	}
	var perms any // NULL for all of the user's
	if in.Permissions != nil {
		data, _ := json.Marshal(in.Permissions)
		perms = string(data)
	}
	var expires int64 // 0 for never
	if !in.ExpiresAt.IsZero() {
		expires = in.ExpiresAt.Unix()
	}
	now, secret := time.Now().Unix(), TokenPrefix+rand.Text()
	t := Token{ID: strings.ToLower(rand.Text()), Name: in.Name, Permissions: in.Permissions, CreatedAt: time.Unix(now, 0), ExpiresAt: unixOrZero(expires)}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO api_tokens (token_hash, id, user_id, name, permissions, created_at, expires_at)
		SELECT ?, ?, ?, ?, ?, ?, ? WHERE (SELECT COUNT(*) FROM api_tokens WHERE user_id = ?) < ?`,
		hashToken(secret), t.ID, userID, t.Name, perms, now, expires, userID, maxTokens)
	switch {
	case err != nil && strings.Contains(err.Error(), "UNIQUE"):
		err = httpapi.Errorf(http.StatusConflict, "You have a token named %q already.", t.Name)
	case err == nil && database.RowsAffected(res) == 0:
		err = httpapi.Errorf(http.StatusConflict, "You have %d tokens already. Revoke those you don't need.", maxTokens)
	}
	if err != nil {
		return Token{}, "", err
	}
	return t, secret, nil
}

// Tokens returns the API tokens of a user, the newest first, also those that expired.
func (s *Service) Tokens(ctx context.Context, userID int64) ([]Token, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+apiColumns+` FROM api_tokens t WHERE user_id = ? ORDER BY created_at DESC, name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tokens := []Token{}
	for rows.Next() {
		t, err := scanToken(rows.Scan)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

// TokensOverLimit counts the API tokens of all users that expire later than new ones may, or
// never; none while the settings allow tokens of any lifetime.
func (s *Service) TokensOverLimit(ctx context.Context) (int, error) {
	maxLifetime := s.limits.MaxTokenLifetime()
	if maxLifetime == 0 {
		return 0, nil
	}
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_tokens WHERE expires_at = 0 OR expires_at > ?`,
		time.Now().Add(maxLifetime).Unix()).Scan(&n)
	return n, err
}

// RevokeToken deletes an API token of a user and returns its name. Tokens of other users
// aren't found.
func (s *Service) RevokeToken(ctx context.Context, userID int64, id string) (string, error) {
	var name string
	err := s.db.QueryRowContext(ctx, `DELETE FROM api_tokens WHERE id = ? AND user_id = ? RETURNING name`, id, userID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		err = errNoSuchToken
	}
	return name, err
}

// AuthenticateToken returns the enabled user owning a valid API token, with the token, and
// notes that it is used from ip.
func (s *Service) AuthenticateToken(ctx context.Context, secret, ip string) (User, error) {
	var user User
	now, hash := time.Now(), hashToken(secret)
	t, err := scanToken(s.db.QueryRowContext(ctx, `
		SELECT `+apiColumns+`, u.id, u.username, u.language, COALESCE(m.enabled, 0)
		FROM api_tokens t JOIN users u ON u.id = t.user_id LEFT JOIN user_mfa m ON m.user_id = u.id
		WHERE t.token_hash = ? AND (t.expires_at = 0 OR t.expires_at > ?) AND u.disabled = 0`, hash, now.Unix()).Scan,
		&user.ID, &user.Username, &user.Language, &user.MFA)
	if errors.Is(err, sql.ErrNoRows) {
		return user, errNoToken
	}
	if err == nil && now.Sub(t.LastUsedAt) >= touchEvery {
		_, err = s.db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ?, ip = ? WHERE token_hash = ?`, now.Unix(), ip, hash)
	}
	user.Token = &t
	return user, err
}

// confirmCode verifies a code of a signed-in user who uses two-factor authentication. A wrong
// one is a bad request rather than unauthorized, which would tell the panel that the session
// ended.
func (s *Service) confirmCode(ctx context.Context, id int64, code string) error {
	m, err := s.MFA(ctx, id)
	switch {
	case err != nil || !m.Enabled:
		return err
	case code == "":
		return errCodeNeeded
	}
	if err := s.checkCode(ctx, id, code); err != errWrongCode { //nolint:errorlint // checkCode returns it as it is
		return err
	}
	return errWrongConfirmation
}

// revokeTokens deletes the API tokens of a user, whenever a change of the user's credentials
// ends the other sessions: they may belong to whoever knew the old ones.
func revokeTokens(ctx context.Context, tx *sql.Tx, userID int64) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM api_tokens WHERE user_id = ?`, userID)
	return err
}

// apiColumns are the columns of api_tokens t that scanToken scans.
const apiColumns = `t.id, t.name, t.permissions, t.created_at, t.expires_at, t.last_used_at, t.ip`

// scanToken scans the apiColumns, and then more.
func scanToken(scan func(...any) error, more ...any) (Token, error) {
	var t Token
	var perms sql.NullString
	var created, expires, used int64
	err := scan(append([]any{&t.ID, &t.Name, &perms, &created, &expires, &used, &t.IP}, more...)...)
	if err == nil && perms.Valid {
		err = json.Unmarshal([]byte(perms.String), &t.Permissions)
	}
	t.CreatedAt, t.ExpiresAt, t.LastUsedAt = time.Unix(created, 0), unixOrZero(expires), unixOrZero(used)
	return t, err
}

// LogAttrs name the user in the log, and the API token if the user acts with one.
func (u User) LogAttrs() []slog.Attr {
	attrs := []slog.Attr{slog.String(logging.KeyUser, u.Username)}
	if u.Token != nil {
		attrs = append(attrs, slog.String("token", u.Token.Name), slog.String("token_id", u.Token.ID))
	}
	return attrs
}

// SessionOnly refuses requests with an API token, for the routes of the user's own account:
// tokens can't change the password, two-factor authentication, sessions or tokens.
func SessionOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if user, _ := UserFrom(r.Context()); user.Token != nil {
			httpapi.WriteError(w, r, errSessionOnly)
			return
		}
		next(w, r)
	}
}
