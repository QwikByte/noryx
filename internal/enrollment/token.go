// Package enrollment defines the join token that connects a new agent to its master.
package enrollment

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

const tokenPrefix = "noryx1_"

// Token carries everything an agent needs to enroll securely: where the master is,
// which CA to trust and the single-use secret.
type Token struct {
	Master string `json:"m"` // host:port of the master's enrollment endpoint
	NodeID string `json:"n"`
	Secret string `json:"s"`
	// CA is the master's CA certificate (DER), the only one the agent trusts as it enrolls.
	CA []byte `json:"c"`
}

func (t Token) String() string {
	data, _ := json.Marshal(t) //nolint:gosec // carrying the secret is the purpose of the token
	return tokenPrefix + base64.RawURLEncoding.EncodeToString(data)
}

// ParseToken decodes a token created by Token.String.
func ParseToken(s string) (Token, error) {
	var t Token
	invalid := errors.New("invalid join token")
	encoded, ok := strings.CutPrefix(strings.TrimSpace(s), tokenPrefix)
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if !ok || err != nil || json.Unmarshal(data, &t) != nil {
		return t, invalid
	}
	if t.Master == "" || t.NodeID == "" || t.Secret == "" || len(t.CA) == 0 {
		return t, invalid
	}
	return t, nil
}
