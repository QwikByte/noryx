package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // HMAC-SHA1 is what RFC 6238 and authenticator apps use by default
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Codes of authenticator apps as they expect them by default (TOTP, RFC 6238): HMAC-SHA1,
// 6 digits and a new code every 30 seconds.
const totpPeriod = 30 // seconds

var secretEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// newSecret returns a random secret of 160 bits, the length RFC 4226 recommends.
func newSecret() string {
	key := make([]byte, 20)
	rand.Read(key)
	return secretEncoding.EncodeToString(key)
}

// totpURI is what the QR code of a secret contains, for authenticator apps to add it under
// issuer. A colon ends the issuer in the label, so the label leaves out the issuer's own.
func totpURI(issuer, username, secret string) string {
	u := url.URL{Scheme: "otpauth", Host: "totp", Path: "/" + strings.ReplaceAll(issuer, ":", "") + ":" + username}
	// Some apps show a "+" as is, so spaces are encoded as "%20".
	u.RawQuery = strings.ReplaceAll(url.Values{"secret": {secret}, "issuer": {issuer}}.Encode(), "+", "%20")
	return u.String()
}

// totp returns the code of key for a time step.
func totp(key []byte, step int64) string {
	mac := hmac.New(sha1.New, key)
	mac.Write(binary.BigEndian.AppendUint64(nil, uint64(step))) //nolint:gosec // steps since 1970 aren't negative
	sum := mac.Sum(nil)
	n := binary.BigEndian.Uint32(sum[sum[len(sum)-1]&0xf:]) & 0x7fffffff
	return fmt.Sprintf("%06d", n%1_000_000)
}

// matchTOTP returns the time step of code if it is the code of the current step or of one
// next to it, which allows for clocks that are a bit off. Steps up to last are used up.
func matchTOTP(secret, code string, now time.Time, last int64) (int64, bool) {
	key, err := secretEncoding.DecodeString(secret)
	if err != nil {
		return 0, false
	}
	current := now.Unix() / totpPeriod
	for step := max(current-1, last+1); step <= current+1; step++ {
		if subtle.ConstantTimeCompare([]byte(totp(key, step)), []byte(code)) == 1 {
			return step, true
		}
	}
	return 0, false
}

// newRecoveryCode returns 50 random bits that are easy to type, e.g. "7KQ2M-PX4ZD".
func newRecoveryCode() string {
	t := rand.Text()
	return t[:5] + "-" + t[5:10]
}

// recoveryHash is the stored hash of a recovery code, which may be typed in lower case and
// without the dash.
func recoveryHash(code string) []byte {
	return hashToken(strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(code)))
}
