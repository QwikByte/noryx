package notify

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPublic(t *testing.T) {
	for addr, want := range map[string]bool{
		"1.1.1.1":                  true,
		"8.8.8.8":                  true,
		"2606:4700:4700::1111":     true,
		"64:ff9b::808:808":         true, // NAT64 of 8.8.8.8
		"::ffff:1.1.1.1":           true,
		"0.0.0.0":                  false,
		"10.1.2.3":                 false,
		"10.213.0.5":               false, // the private network of the nodes by default
		"100.64.0.1":               false,
		"127.0.0.1":                false,
		"169.254.169.254":          false, // metadata of cloud providers
		"172.17.0.1":               false, // Docker
		"192.0.0.8":                false,
		"192.168.1.1":              false,
		"198.18.0.1":               false,
		"203.0.113.9":              false,
		"224.0.0.1":                false,
		"255.255.255.255":          false,
		"::":                       false,
		"::1":                      false,
		"::ffff:127.0.0.1":         false,
		"::ffff:10.0.0.1":          false,
		"::10.0.0.1":               false,
		"64:ff9b::a00:1":           false, // NAT64 of 10.0.0.1
		"64:ff9b::7f00:1":          false,
		"64:ff9b:1::a00:1":         false, // NAT64 for local use
		"2002:a00:1::1":            false, // 6to4 of 10.0.0.1
		"2001::1":                  false, // Teredo
		"2001:db8::1":              false,
		"fc00::1":                  false,
		"fd12:3456::1":             false,
		"fe80::1":                  false,
		"ff02::1":                  false,
		"2606:4700::1111%eth0":     false,
		"3fff::1":                  false,
		"5f00::1":                  false,
		"2a01:4f8:c17:b8f::2":      true,
		"2001:4860:4860::8888":     true,
		"::ffff:100.64.0.1":        false,
		"0:0:0:0:0:ffff:a9fe:a9fe": false,
	} {
		if got := public(netip.MustParseAddr(addr)); got != want {
			t.Errorf("public(%s) = %v, want %v", addr, got, want)
		}
	}
}

// Connections are checked after names are resolved, so a name of a private address is refused
// like the address itself, and nothing is sent to it.
func TestDialRefusesPrivateAddresses(t *testing.T) {
	var reached atomic.Bool
	target, roots := localServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached.Store(true) }))
	s := New(nil, nil, panel("Noryx"), Options{Roots: roots})
	for _, url := range []string{target + "/hook/secret-token", strings.Replace(target, "localhost", "127.0.0.1", 1) + "/hook/secret-token"} {
		err := s.post(t.Context(), url, map[string]string{})
		if err == nil || !strings.Contains(err.Error(), "isn't a public address") || strings.Contains(err.Error(), "secret-token") {
			t.Errorf("%s: %v", url, err)
		}
	}
	if _, err := newDialer(nil).DialContext(context.Background(), "tcp", strings.TrimPrefix(target, "https://")); !errors.As(err, new(*refusedError)) {
		t.Errorf("dial = %v", err)
	}
	if reached.Load() {
		t.Error("the server was reached")
	}
	// Tests may allow addresses.
	s = New(nil, nil, panel("Noryx"), Options{Roots: roots, Allow: loopback})
	if err := s.post(t.Context(), target+"/hook", map[string]string{}); err != nil || !reached.Load() {
		t.Errorf("allowed: %v", err)
	}
}

func TestCheckURL(t *testing.T) {
	for _, raw := range []string{"http://example.com/secret", "https:///secret", "ftp://example.com/secret", "https://10.0.0.1/secret", "https://[::1]/secret", "example.com/secret"} {
		if _, err := checkURL(raw); err == nil || strings.Contains(err.Error(), "secret") {
			t.Errorf("%s: %v", raw, err)
		}
	}
	if u, err := checkURL(" https://discord.com/api/webhooks/1/abc#x "); err != nil || u.String() != "https://discord.com/api/webhooks/1/abc" {
		t.Errorf("valid: %v, %v", u, err)
	}
}
