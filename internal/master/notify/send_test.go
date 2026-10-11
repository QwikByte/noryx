package notify

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"math/big"
	"mime"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/netip"
	"net/textproto"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/QwikByte/noryx/internal/master/logs"
)

// localTLS returns the TLS configuration of a server at localhost and the pool that trusts it.
func localTLS(t *testing.T) (*tls.Config, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), DNSNames: []string{"localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}, pool
}

// localServer serves h over HTTPS; its URL names localhost.
func localServer(t *testing.T, h http.Handler) (url string, roots *x509.CertPool) {
	t.Helper()
	cfg, pool := localTLS(t)
	srv := httptest.NewUnstartedServer(h)
	srv.TLS = cfg
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return strings.Replace(srv.URL, "127.0.0.1", "localhost", 1), pool
}

func loopback(ip netip.Addr) bool { return ip.IsLoopback() }

// panel is the name of the panel in tests.
type panel string

func (p panel) PanelName() string { return string(p) }

func entry(level slog.Level, message string) logs.Entry {
	return logs.Entry{
		ID: 1, Time: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC), Level: level, Category: "servers", Message: message,
		NodeName: "node-1", ServerName: "Lobby", Attrs: map[string]string{"err": "exit code 1"},
	}
}

// Agents write the texts of entries, so chats show them as they are: without mentions,
// links or formatting.
func TestChatsShowEntriesAsText(t *testing.T) {
	m := message{entries: []logs.Entry{entry(slog.LevelWarn, "@everyone <@123> **bold** [link](https://evil.example) # head\nline")}, more: 3}
	data, _ := json.Marshal(discord(m))
	var d struct {
		Content string `json:"content"`
		Embeds  []struct {
			Title, Description string
		} `json:"embeds"`
		AllowedMentions struct {
			Parse []string `json:"parse"`
		} `json:"allowed_mentions"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		t.Fatal(err)
	}
	unescaped := regexp.MustCompile(`(^|[^\\])[@<*\[#]`)
	title := d.Embeds[0].Title
	if len(d.Embeds) != 1 || unescaped.MatchString(title) || strings.Contains(title, "\n") || !strings.Contains(title, `\@everyone`) ||
		d.AllowedMentions.Parse == nil || len(d.AllowedMentions.Parse) != 0 || !strings.Contains(d.Content, "3 more") {
		t.Errorf("discord = %s", data)
	}

	data, _ = json.Marshal(slack(m, "<!channel> Noryx"))
	var s struct {
		Text   string `json:"text"`
		Blocks []struct {
			Type string `json:"type"`
			Text struct {
				Type  string `json:"type"`
				Text  string `json:"text"`
				Emoji bool   `json:"emoji"`
			} `json:"text"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	text := s.Blocks[0].Text
	if strings.Contains(s.Text, "everyone") || s.Text != "&lt;!channel&gt; Noryx: new entries of the log" || text.Type != "plain_text" || text.Emoji || strings.ContainsAny(text.Text, "<>") ||
		!strings.Contains(text.Text, "&lt;@123&gt;") || len(s.Blocks) != 2 {
		t.Errorf("slack = %s", data)
	}

	data, _ = json.Marshal(webhook(m))
	var p struct {
		Test    bool `json:"test"`
		NotSent int  `json:"notSent"`
		Entries []struct {
			Level, Message, ServerName string
		} `json:"entries"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	if p.Test || p.NotSent != 3 || len(p.Entries) != 1 || p.Entries[0].Level != "warn" || p.Entries[0].ServerName != "Lobby" {
		t.Errorf("webhook = %s", data)
	}
}

// Messages stay within the limits of Discord and Slack, also with long texts that escaping makes longer.
func TestChatsStayWithinLimits(t *testing.T) {
	long := entry(slog.LevelError, strings.Repeat("<@*", 1000))
	long.ServerName, long.Attrs["err"] = strings.Repeat("_", 128), strings.Repeat(">", 2000)
	m := message{entries: slices.Repeat([]logs.Entry{long}, maxEntries), more: 99}
	total := 0
	for _, e := range discord(m)["embeds"].([]map[string]any) {
		title, description := e["title"].(string), e["description"].(string)
		if utf8.RuneCountInString(title) > 256 {
			t.Errorf("title of %d characters", utf8.RuneCountInString(title))
		}
		total += utf8.RuneCountInString(title) + utf8.RuneCountInString(description)
	}
	if total > 6000 {
		t.Errorf("embeds of %d characters", total)
	}
	for _, b := range slack(m, "Noryx")["blocks"].([]map[string]any) {
		if text, ok := b["text"].(map[string]any); ok && utf8.RuneCountInString(text["text"].(string)) > 3000 {
			t.Errorf("block of %d characters", utf8.RuneCountInString(text["text"].(string)))
		}
	}
}

// The subject quotes an entry, which can't add headers. It and the sender name the panel.
func TestMailHeadersCantBeInjected(t *testing.T) {
	cfg := &Mail{From: "noryx@example.com", To: []string{"ops@example.com", "dev@example.com"}}
	const name = `Noryx · "Test" <ops@example.com>`
	raw := mailMessage(cfg, message{entries: []logs.Entry{entry(slog.LevelError, "Disk full\r\nBcc: evil@example.com\r\n\r\nInjected")}}, name)
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil || msg.Header.Get("Bcc") != "" || strings.ContainsAny(subject, "\r\n") || !strings.HasPrefix(subject, name+": Disk full") {
		t.Errorf("headers = %v, subject %q", msg.Header, subject)
	}
	if from, err := msg.Header.AddressList("From"); err != nil || len(from) != 1 || from[0].Name != name || from[0].Address != cfg.From {
		t.Errorf("from = %v, %v", from, err)
	}
	if to, err := msg.Header.AddressList("To"); err != nil || len(to) != 2 {
		t.Errorf("to = %v, %v", to, err)
	}
}

// Webhooks don't follow redirects, and errors never show their URL.
func TestPostFollowsNoRedirect(t *testing.T) {
	var redirected atomic.Bool
	target, roots := localServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			redirected.Store(true)
			return
		}
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	s := New(nil, nil, panel("Noryx"), Options{Allow: loopback, Roots: roots})
	err := s.post(t.Context(), target+"/hook/secret-token", map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "302") || strings.Contains(err.Error(), "secret-token") || redirected.Load() {
		t.Errorf("post = %v, redirected %v", err, redirected.Load())
	}
	// A certificate that isn't trusted is refused.
	s = New(nil, nil, panel("Noryx"), Options{Allow: loopback})
	if err := s.post(t.Context(), target+"/hook/secret-token", map[string]string{}); err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Errorf("untrusted: %v", err)
	}
}

// fakeSMTP is a mail server at localhost that records the commands it gets, and whether they
// came encrypted.
type fakeSMTP struct {
	port     string
	cfg      *tls.Config
	implicit bool // TLS from the start, or else STARTTLS if offered
	offer    bool
	mu       sync.Mutex
	lines    []string
}

func startSMTP(t *testing.T, cfg *tls.Config, implicit, offer bool) *fakeSMTP {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	f := &fakeSMTP{port: port, cfg: cfg, implicit: implicit, offer: offer}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	return f
}

func (f *fakeSMTP) record(line string, secure bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !secure {
		line = "PLAIN " + line
	}
	f.lines = append(f.lines, line)
}

func (f *fakeSMTP) got() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.lines, "\n")
}

func (f *fakeSMTP) serve(conn net.Conn) {
	defer conn.Close()
	secure := f.implicit
	if secure {
		conn = tls.Server(conn, f.cfg)
	}
	tp := textproto.NewConn(conn)
	_ = tp.PrintfLine("220 localhost ESMTP")
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		f.record(line, secure)
		switch cmd, _, _ := strings.Cut(strings.ToUpper(line), " "); cmd {
		case "EHLO":
			_ = tp.PrintfLine("250-localhost")
			if !secure && f.offer {
				_ = tp.PrintfLine("250-STARTTLS")
			}
			if secure {
				_ = tp.PrintfLine("250-AUTH PLAIN LOGIN")
			}
			_ = tp.PrintfLine("250 8BITMIME")
		case "STARTTLS":
			_ = tp.PrintfLine("220 Go ahead")
			conn = tls.Server(conn, f.cfg)
			tp, secure = textproto.NewConn(conn), true
		case "AUTH":
			_ = tp.PrintfLine("235 Accepted")
		case "DATA":
			_ = tp.PrintfLine("354 Go ahead")
			lines, _ := tp.ReadDotLines()
			f.record(strings.Join(lines, "\n"), secure)
			_ = tp.PrintfLine("250 Queued")
		case "QUIT":
			_ = tp.PrintfLine("221 Bye")
			return
		default:
			_ = tp.PrintfLine("250 OK")
		}
	}
}

// Mails go over TLS from the start or after STARTTLS, and never sign in without it.
func TestMailNeedsTLS(t *testing.T) {
	cfg, roots := localTLS(t)
	s := New(nil, nil, panel("Noryx"), Options{Allow: loopback, Roots: roots})
	m := message{entries: []logs.Entry{entry(slog.LevelError, "A server crashed")}}
	send := func(f *fakeSMTP, security string) error {
		port, _ := net.LookupPort("tcp", f.port)
		ch := &Channel{Kind: Email, secret: "the-password", Email: &Mail{
			Host: "localhost", Port: uint16(port), Security: security, Username: "noryx", From: "noryx@example.com", To: []string{"ops@example.com"},
		}}
		return s.send(t.Context(), ch, m)
	}
	plain := base64.StdEncoding.EncodeToString([]byte("\x00noryx\x00the-password"))
	for _, implicit := range []bool{true, false} {
		f := startSMTP(t, cfg, implicit, true)
		security := StartTLS
		if implicit {
			security = ImplicitTLS
		}
		if err := send(f, security); err != nil {
			t.Fatalf("%s: %v", security, err)
		}
		got := f.got()
		if !strings.Contains(got, "AUTH PLAIN "+plain) || strings.Contains(got, "PLAIN AUTH") || strings.Contains(got, "PLAIN MAIL") ||
			!strings.Contains(got, "RCPT TO:<ops@example.com>") || !strings.Contains(got, "A server crashed") {
			t.Errorf("%s: the server got %s", security, got)
		}
	}
	f := startSMTP(t, cfg, false, false)
	if err := send(f, StartTLS); err == nil || !strings.Contains(err.Error(), "STARTTLS") || strings.Contains(f.got(), "AUTH") || strings.Contains(f.got(), "MAIL") {
		t.Errorf("without STARTTLS: %v, the server got %s", err, f.got())
	}
	// A mail server at a private address is refused.
	s = New(nil, nil, panel("Noryx"), Options{Roots: roots})
	if err := send(startSMTP(t, cfg, true, false), ImplicitTLS); err == nil || !strings.Contains(err.Error(), "public address") {
		t.Errorf("private: %v", err)
	}
}
