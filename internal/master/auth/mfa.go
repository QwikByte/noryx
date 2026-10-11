package auth

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/QwikByte/noryx/internal/master/httpapi"
)

// Two-factor authentication is off until a user sets it up. Then signing in also needs a
// code of the user's authenticator app, or one of the user's recovery codes.

const (
	recoveryCodeCount = 10
	maxCodeFailures   = 5 // wrong codes in a row before the code check locks
)

var (
	// ErrCodeRequired means that the password is right, but the user needs to enter a code too.
	ErrCodeRequired = errors.New("code required")

	errWrongCode  = httpapi.Errorf(http.StatusUnauthorized, "The code is incorrect.")
	errCodeLocked = httpapi.Errorf(http.StatusTooManyRequests, "Too many wrong codes. Try again later, or ask an administrator to turn off two-factor authentication for you.")
	errMFAOn      = httpapi.Errorf(http.StatusConflict, "Two-factor authentication is on already.")
	errMFAOff     = httpapi.Errorf(http.StatusConflict, "Two-factor authentication is off.")
	errNoSetup    = httpapi.Errorf(http.StatusConflict, "This setup is over. Start it again.")
)

// MFA tells whether a user's sign-ins need a code.
type MFA struct {
	Enabled bool `json:"enabled"`
	// RecoveryCodes is the number of recovery codes left.
	RecoveryCodes int `json:"recoveryCodes"`
	// Required tells whether the settings require two-factor authentication of the user.
	Required bool `json:"required"`
}

// MFASetup is a new secret for an authenticator app. It applies once a code confirms it.
type MFASetup struct {
	Secret string `json:"secret"`
	// URI is the otpauth:// URI of the secret, which a QR code passes to the app.
	URI string `json:"uri"`
}

// MFA returns whether two-factor authentication is on for a user.
func (s *Service) MFA(ctx context.Context, id int64) (MFA, error) {
	var m MFA
	err := s.db.QueryRowContext(ctx, `
		SELECT enabled, (SELECT COUNT(*) FROM recovery_codes WHERE user_id = m.user_id) FROM user_mfa m WHERE user_id = ?`, id).
		Scan(&m.Enabled, &m.RecoveryCodes)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return m, err
}

// SetUpMFA starts setting up two-factor authentication with a new secret, which replaces
// the one of an unfinished setup. The authenticator app shows it under issuer, the name of
// the panel, which it keeps when the name changes later.
func (s *Service) SetUpMFA(ctx context.Context, user User, issuer string) (MFASetup, error) {
	secret := newSecret()
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO user_mfa (user_id, secret) VALUES (?, ?)
		ON CONFLICT (user_id) DO UPDATE SET secret = excluded.secret WHERE enabled = 0`, user.ID, secret)
	if err == nil && rowsAffected(res) == 0 {
		err = errMFAOn
	}
	return MFASetup{Secret: secret, URI: totpURI(issuer, user.Username, secret)}, err
}

// EnableMFA turns on two-factor authentication once a code shows that the app has the
// secret of the setup, and returns the recovery codes. Other sessions than the one
// identified by keep end and the API tokens are revoked, as they started without a code.
func (s *Service) EnableMFA(ctx context.Context, id int64, password, code, keep string) ([]string, error) {
	if err := s.confirmPassword(ctx, id, password); err != nil {
		return nil, err
	}
	var secret string
	err := s.db.QueryRowContext(ctx, `SELECT secret FROM user_mfa WHERE user_id = ? AND enabled = 0`, id).Scan(&secret)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNoSetup
	}
	if err != nil {
		return nil, err
	}
	step, ok := matchTOTP(secret, code, time.Now(), 0)
	if !ok {
		return nil, httpapi.Errorf(http.StatusBadRequest, "The code is incorrect. Check that the time on your device is right.")
	}
	var codes []string
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE user_mfa SET enabled = 1, last_step = ? WHERE user_id = ? AND secret = ? AND enabled = 0`, step, id, secret)
		if err == nil && rowsAffected(res) == 0 {
			err = errNoSetup // replaced by a concurrent request
		}
		if err == nil {
			codes, err = replaceRecoveryCodes(ctx, tx, id)
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, endOtherSessions, id, hashToken(keep))
		}
		if err == nil {
			err = revokeTokens(ctx, tx, id)
		}
		return err
	})
	return codes, err
}

// NewRecoveryCodes replaces the recovery codes of a user who knows the password.
func (s *Service) NewRecoveryCodes(ctx context.Context, id int64, password string) ([]string, error) {
	if m, err := s.MFA(ctx, id); err != nil || !m.Enabled {
		return nil, cmp.Or(err, errMFAOff)
	}
	if err := s.confirmPassword(ctx, id, password); err != nil {
		return nil, err
	}
	var codes []string
	err := s.inTx(ctx, func(tx *sql.Tx) (err error) {
		codes, err = replaceRecoveryCodes(ctx, tx, id)
		return err
	})
	return codes, err
}

// DisableMFA turns off two-factor authentication for a user who knows the password.
func (s *Service) DisableMFA(ctx context.Context, id int64, password string) error {
	if err := s.confirmPassword(ctx, id, password); err != nil {
		return err
	}
	return s.ResetMFA(ctx, id)
}

// ResetMFA turns off two-factor authentication, e.g. for a user who lost the app and the
// recovery codes.
func (s *Service) ResetMFA(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM user_mfa WHERE user_id = ?`, id)
	if err == nil && rowsAffected(res) == 0 {
		err = errMFAOff
	}
	return err
}

func replaceRecoveryCodes(ctx context.Context, tx *sql.Tx, id int64) ([]string, error) {
	if _, err := tx.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, id); err != nil {
		return nil, err
	}
	codes := make([]string, recoveryCodeCount)
	for i := range codes {
		codes[i] = newRecoveryCode()
		if _, err := tx.ExecContext(ctx, `INSERT INTO recovery_codes (user_id, code_hash) VALUES (?, ?)`, id, recoveryHash(codes[i])); err != nil {
			return nil, err
		}
	}
	return codes, nil
}

// checkCode verifies a code of the user's app, or uses up a recovery code. Each attempt
// counts as wrong until it turns out right, so that parallel guesses can't slip past the
// lock.
func (s *Service) checkCode(ctx context.Context, id int64, code string) error {
	now := time.Now()
	var secret string
	var last int64
	var failures int
	err := s.db.QueryRowContext(ctx, `
		UPDATE user_mfa SET failures = failures + 1 WHERE user_id = ? AND enabled = 1 AND locked_until <= ?
		RETURNING secret, last_step, failures`, id, now.Unix()).Scan(&secret, &last, &failures)
	if errors.Is(err, sql.ErrNoRows) {
		return errCodeLocked
	}
	if err != nil {
		return err
	}

	var res sql.Result
	if step, ok := matchTOTP(secret, code, now, last); ok {
		// Of concurrent sign-ins with the same code, only one gets through.
		res, err = s.db.ExecContext(ctx, `UPDATE user_mfa SET last_step = ? WHERE user_id = ? AND last_step < ?`, step, id, step)
	} else {
		res, err = s.db.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ? AND code_hash = ?`, id, recoveryHash(code))
	}
	if err != nil {
		return err
	}
	if rowsAffected(res) == 1 {
		_, err = s.db.ExecContext(ctx, `UPDATE user_mfa SET failures = 0 WHERE user_id = ?`, id)
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE user_mfa SET locked_until = MAX(locked_until, ?) WHERE user_id = ?`, now.Add(lockout(failures)).Unix(), id)
	return cmp.Or(err, errWrongCode)
}

// lockout is how long the code check locks after the nth wrong code in a row: from the
// fifth on, for a minute that doubles with each further one, up to a day.
func lockout(failures int) time.Duration {
	if failures < maxCodeFailures {
		return 0
	}
	return min(time.Minute<<min(failures-maxCodeFailures, 11), 24*time.Hour)
}

// confirmPassword verifies the password of a signed-in user before a change to the account.
func (s *Service) confirmPassword(ctx context.Context, id int64, password string) error {
	var hash string
	if err := s.db.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id = ?`, id).Scan(&hash); err != nil {
		return err
	}
	if !verifyPassword(hash, password) {
		return httpapi.Errorf(http.StatusBadRequest, "The current password is incorrect.")
	}
	return nil
}

// inTx runs fn in a transaction, which is committed if fn succeeds.
func (s *Service) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // a no-op after Commit
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
