// Package auth manages the accounts of the panel and their sign-ins: passwords, setup
// links, two-factor authentication and sessions. What a user may do is up to the access
// package.
package auth

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/web"
)

var (
	ErrInvalidCredentials = httpapi.Errorf(http.StatusUnauthorized, "Username or password is incorrect.")
	ErrNoSession          = errors.New("no valid session")

	errNotFound     = httpapi.Errorf(http.StatusNotFound, "User not found.")
	errInvalidSetup = httpapi.Errorf(http.StatusNotFound, "This setup link is invalid, expired or used already. Ask for a new one.")

	usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{3,32}$`)
)

type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	// Language is the language of the panel the user chose, e.g. de or pt-BR; empty follows the browser.
	Language string `json:"language,omitempty"`
	// MFA tells whether signing in needs a code of an authenticator app too.
	MFA bool `json:"mfa"`
	// MustSetUpMFA is set for a signed-in user whom the settings require to use two-factor
	// authentication, but who hasn't set it up: until then, the user may only use the routes
	// of the own account, see access.Mux.
	MustSetUpMFA bool `json:"mustSetUpMfa,omitempty"`
	// Token is the API token that a request acts with; nil for a session of the panel.
	Token *Token `json:"-"`
}

// Account is a user as the user management shows it.
type Account struct {
	User
	Disabled bool `json:"disabled"`
	// PasswordSet is false until an invited user sets a password with the setup link.
	PasswordSet bool      `json:"passwordSet"`
	CreatedAt   time.Time `json:"createdAt"`
}

// SetupLink lets a user set a password once until it expires. Token goes into the link.
type SetupLink struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type Service struct {
	db     *sql.DB
	limits Limits
}

// Limits are what the settings say about sign-ins: how many characters new passwords need,
// how long setup links are valid, how long new API tokens may last and after how long without
// use sessions end; the last two are 0 for no limit.
type Limits interface {
	MinPasswordLength() int
	SetupLinkTTL() time.Duration
	MaxTokenLifetime() time.Duration
	SessionIdleTimeout() time.Duration
}

func NewService(db *sql.DB, limits Limits) *Service { return &Service{db: db, limits: limits} }

// CreateUser adds a user with a password, e.g. the first administrator.
func (s *Service) CreateUser(ctx context.Context, username, password string) (User, error) {
	if err := s.checkPassword(password); err != nil {
		return User{}, err
	}
	return s.insert(ctx, username, hashPassword(password))
}

// Invite adds a user without a password and returns the link to set one.
func (s *Service) Invite(ctx context.Context, username string) (User, SetupLink, error) {
	u, err := s.insert(ctx, username, "")
	if err != nil {
		return u, SetupLink{}, err
	}
	link, err := s.NewSetupLink(ctx, u.ID)
	return u, link, err
}

func (s *Service) insert(ctx context.Context, username, hash string) (User, error) {
	if !usernamePattern.MatchString(username) {
		return User{}, httpapi.Errorf(http.StatusBadRequest, "Enter a username of 3 to 32 letters, digits, '_', '.' or '-'.")
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO users (username, password_hash, created_at) VALUES (?, ?, ?)`,
		username, hash, time.Now().Unix())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			err = httpapi.Errorf(http.StatusConflict, "The username %q is taken already.", username)
		}
		return User{}, err
	}
	id, err := res.LastInsertId()
	return User{ID: id, Username: username}, err
}

// checkPassword checks that a new password has as many characters as the settings say.
// Passwords set before keep working, also if they are shorter.
func (s *Service) checkPassword(password string) error {
	if n := s.limits.MinPasswordLength(); utf8.RuneCountInString(password) < n {
		return httpapi.Errorf(http.StatusBadRequest, "Choose a password with at least %d characters.", n)
	}
	return nil
}

// HasUsers reports whether at least one account exists.
func (s *Service) HasUsers(ctx context.Context) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM users)`).Scan(&exists)
	return exists, err
}

// Accounts returns all users ordered by name.
func (s *Service) Accounts(ctx context.Context) ([]Account, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT u.id, u.username, u.disabled, u.password_hash != '', COALESCE(m.enabled, 0), u.created_at
		FROM users u LEFT JOIN user_mfa m ON m.user_id = u.id ORDER BY u.username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	accounts := []Account{}
	for rows.Next() {
		var a Account
		var created int64
		if err := rows.Scan(&a.ID, &a.Username, &a.Disabled, &a.PasswordSet, &a.MFA, &created); err != nil {
			return nil, err
		}
		a.CreatedAt = time.Unix(created, 0)
		accounts = append(accounts, a)
	}
	return accounts, rows.Err()
}

// SetDisabled disables or enables a user. Disabling ends the user's sessions and revokes the
// user's API tokens.
func (s *Service) SetDisabled(ctx context.Context, id int64, disabled bool) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE users SET disabled = ? WHERE id = ?`, disabled, id)
		if err == nil && rowsAffected(res) == 0 {
			return errNotFound
		}
		if err == nil && disabled {
			_, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id)
		}
		if err == nil && disabled {
			err = revokeTokens(ctx, tx, id)
		}
		return err
	})
}

// Delete deletes a user with its sessions and API tokens.
func (s *Service) Delete(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err == nil && rowsAffected(res) == 0 {
		err = errNotFound
	}
	return err
}

// NewSetupLink replaces the setup link of a user, e.g. to reset a forgotten password. The
// current password stays valid until the user sets a new one.
func (s *Service) NewSetupLink(ctx context.Context, id int64) (SetupLink, error) {
	link := SetupLink{Token: rand.Text(), ExpiresAt: time.Now().Add(s.limits.SetupLinkTTL())}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO setup_tokens (token_hash, user_id, expires_at) VALUES (?, ?, ?)
		ON CONFLICT (user_id) DO UPDATE SET token_hash = excluded.token_hash, expires_at = excluded.expires_at`,
		hashToken(link.Token), id, link.ExpiresAt.Unix())
	if err != nil && strings.Contains(err.Error(), "FOREIGN KEY") {
		err = errNotFound
	}
	return link, err
}

// SetupUser returns the user a valid setup link belongs to.
func (s *Service) SetupUser(ctx context.Context, token string) (User, error) {
	var u User
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.username, u.language FROM setup_tokens t JOIN users u ON u.id = t.user_id
		WHERE t.token_hash = ? AND t.expires_at > ? AND u.disabled = 0`, hashToken(token), time.Now().Unix()).Scan(&u.ID, &u.Username, &u.Language)
	if errors.Is(err, sql.ErrNoRows) {
		err = errInvalidSetup
	}
	return u, err
}

// Setup sets the password of a user with a setup link, which is used up. All sessions of
// the user end, the user's API tokens are revoked, and a new session starts for client,
// which lasts for ttl, unless signing in needs a code too: then the returned token is
// empty, so that a setup link can't replace the code.
func (s *Service) Setup(ctx context.Context, token, password string, ttl time.Duration, client Client) (User, string, error) {
	if err := s.checkPassword(password); err != nil {
		return User{}, "", err
	}
	u, err := s.SetupUser(ctx, token)
	if err != nil {
		return u, "", err
	}
	hash := hashPassword(password) // before the transaction, which keeps other writers waiting
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM setup_tokens WHERE token_hash = ?`, hashToken(token))
		if err == nil && rowsAffected(res) == 0 {
			err = errInvalidSetup // used by a concurrent request
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, hash, u.ID)
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, u.ID)
		}
		if err == nil {
			err = revokeTokens(ctx, tx, u.ID)
		}
		return err
	})
	if err != nil {
		return u, "", err
	}
	if m, err := s.MFA(ctx, u.ID); err != nil || m.Enabled {
		return u, "", err
	}
	session, err := s.startSession(ctx, u.ID, ttl, client)
	return u, session, err
}

// ChangePassword replaces the password of a user who knows the current one. Other
// sessions than the one identified by keep end, and the user's API tokens are revoked.
func (s *Service) ChangePassword(ctx context.Context, id int64, current, next, keep string) error {
	if err := s.confirmPassword(ctx, id, current); err != nil {
		return err
	}
	if err := s.checkPassword(next); err != nil {
		return err
	}
	// A setup link would set the password again, and other sessions may belong to whoever
	// knew the old one.
	hash := hashPassword(next)
	return s.inTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, hash, id)
		if err == nil {
			_, err = tx.ExecContext(ctx, `DELETE FROM setup_tokens WHERE user_id = ?`, id)
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, endOtherSessions, id, hashToken(keep))
		}
		if err == nil {
			err = revokeTokens(ctx, tx, id)
		}
		return err
	})
}

// Login verifies the credentials and starts a session for client that lasts for ttl,
// identified by the returned token. Disabled users and invited users without a password
// can't sign in. With two-factor authentication, code is a code of the user's app or a
// recovery code; if it is empty, Login returns ErrCodeRequired once the password is right.
func (s *Service) Login(ctx context.Context, username, password, code string, ttl time.Duration, client Client) (User, string, error) {
	user := User{Username: username}
	var hash string
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.username, u.language, u.password_hash, COALESCE(m.enabled, 0) FROM users u LEFT JOIN user_mfa m ON m.user_id = u.id
		WHERE u.username = ? AND u.disabled = 0`, username).Scan(&user.ID, &user.Username, &user.Language, &hash, &user.MFA)
	if errors.Is(err, sql.ErrNoRows) || err == nil && hash == "" {
		verifyPassword(dummyHash(), password) // takes as long as for existing users
		return User{}, "", ErrInvalidCredentials
	}
	if err != nil {
		return User{}, "", err
	}
	switch {
	case !verifyPassword(hash, password):
		err = ErrInvalidCredentials
	case !user.MFA:
	case code == "":
		err = ErrCodeRequired
	default:
		err = s.checkCode(ctx, user.ID, code)
	}
	if err != nil {
		return User{}, "", err
	}
	token, err := s.startSession(ctx, user.ID, ttl, client)
	return user, token, err
}

// Authenticate returns the enabled user owning a valid session token, and notes that client
// uses the session. A session unused for longer than the settings allow ends.
func (s *Service) Authenticate(ctx context.Context, token string, client Client) (User, error) {
	var user User
	var lastUsed int64
	now, hash := time.Now(), hashToken(token)
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.username, u.language, COALESCE(m.enabled, 0), s.last_used_at
		FROM sessions s JOIN users u ON u.id = s.user_id LEFT JOIN user_mfa m ON m.user_id = u.id
		WHERE s.token_hash = ? AND s.expires_at > ? AND u.disabled = 0`, hash, now.Unix()).
		Scan(&user.ID, &user.Username, &user.Language, &user.MFA, &lastUsed)
	if errors.Is(err, sql.ErrNoRows) {
		return user, ErrNoSession
	}
	if err == nil && lastUsed <= s.unusedSince(now) {
		// It ends for good, also if the settings allow longer later.
		return User{}, cmp.Or(s.Logout(ctx, token), ErrNoSession)
	}
	if err == nil {
		err = s.touch(ctx, hash, lastUsed, client)
	}
	return user, err
}

// SetLanguage stores the language of the panel a user chose, one of web.Languages, or empty for the browser's.
func (s *Service) SetLanguage(ctx context.Context, id int64, language string) error {
	if language != "" && !slices.Contains(web.Languages, language) {
		return httpapi.Errorf(http.StatusBadRequest, "Choose one of the panel's languages, e.g. de, or none to follow the browser.")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE users SET language = ? WHERE id = ?`, language, id)
	return err
}

// Logout ends the session identified by token.
func (s *Service) Logout(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hashToken(token))
	return err
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func rowsAffected(res sql.Result) int64 {
	n, _ := res.RowsAffected()
	return n
}
