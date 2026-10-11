package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/QwikByte/noryx/internal/buildinfo"
	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/logs"
)

const (
	sendTimeout = 30 * time.Second
	// maxEntries are sent in a message at most; the message counts the others.
	maxEntries = 10
)

// message is what a channel sends at once: entries of the log, and how many others it
// couldn't send, as there were too many or sending failed. A test has neither.
type message struct {
	entries []logs.Entry
	more    int
	test    bool
}

const testText = "This is a test of Noryx. The entries of the log that the rules choose for this channel arrive here."

func (m message) summary() string {
	if m.more == 0 {
		return ""
	}
	return fmt.Sprintf("%d more entries weren't sent here. The log in the panel has them all.", m.more)
}

// send sends a message through a channel.
func (s *Service) send(ctx context.Context, ch *Channel, m message) error {
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	switch ch.Kind {
	case Discord:
		return s.post(ctx, ch.secret, discord(m))
	case Slack:
		return s.post(ctx, ch.secret, slack(m, s.conf.PanelName()))
	case Webhook:
		return s.post(ctx, ch.secret, webhook(m))
	case Email:
		return s.mail(ctx, ch, m)
	}
	return failed("Unknown kind of channel %q.", ch.Kind)
}

// post sends JSON to a webhook over HTTPS. Redirects aren't followed and proxies aren't used,
// so the request goes to the checked address only.
func (s *Service) post(ctx context.Context, target string, payload any) error {
	if _, err := checkURL(target); err != nil {
		return err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return failed("The URL of the webhook is invalid.")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Noryx/"+buildinfo.Version)
	res, err := s.client.Do(req)
	if err != nil {
		return reason(err, target)
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	if res.StatusCode < 200 || res.StatusCode > 299 {
		// The status alone, as the target writes the rest of its answer.
		return failed("The target answered %s.", strings.TrimSpace(fmt.Sprint(res.StatusCode, " ", http.StatusText(res.StatusCode))))
	}
	return nil
}

// Agents write messages, names and attributes of entries, so chats get them as text that can't
// mention anyone or format anything.

// details are what a chat or mail shows about an entry besides its message: its server and
// node, category, user and error.
func details(e logs.Entry) string {
	var parts []string
	switch {
	case e.ServerName != "" && e.NodeName != "":
		parts = append(parts, e.ServerName+" on "+e.NodeName)
	case e.NodeName != "":
		parts = append(parts, e.NodeName)
	}
	parts = append(parts, e.Category)
	if e.User != "" {
		parts = append(parts, "by "+e.User)
	}
	if err := e.Attrs["err"]; err != "" {
		parts = append(parts, err)
	}
	return strings.Join(parts, " · ")
}

// plain makes text a single line without control characters, of at most n bytes.
func plain(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(s, "�"))
	if s = strings.TrimSpace(s); len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// levels name the levels in messages.
var levels = map[string]string{"debug": "Debug", "info": "Info", "warn": "Warning", "error": "Error"}

func levelName(e logs.Entry) string { return levels[logging.LevelName(e.Level)] }

// discordEscape keeps Discord from formatting text or making mentions of it: a backslash
// before a punctuation character shows the character as it is.
func discordEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune("\\*_~`|>#-+.[]()<:@", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

var discordColours = map[string]int{"info": 0x3b82f6, "warn": 0xf59e0b, "error": 0xef4444}

// discord is the payload of a Discord webhook: an embed per entry. allowed_mentions turns
// mentions off besides. Texts are cut before they are escaped, which at most doubles them, so
// that a message stays within Discord's limits of 256 characters for a title and 6000 for all
// embeds.
func discord(m message) map[string]any {
	embeds := []map[string]any{}
	for _, e := range m.entries {
		embeds = append(embeds, map[string]any{
			"title":       discordEscape(plain(levelName(e)+": "+e.Message, 100)),
			"description": discordEscape(plain(details(e), 160)),
			"color":       discordColours[logging.LevelName(e.Level)],
			"timestamp":   e.Time.UTC().Format(time.RFC3339),
		})
	}
	content := m.summary()
	if m.test {
		content = testText
	}
	return map[string]any{"content": discordEscape(content), "embeds": embeds, "allowed_mentions": map[string]any{"parse": []string{}}}
}

// slackEscape escapes the characters with which Slack links and mentions.
var slackEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace

// slackText is plain text of Slack, which isn't formatted, without emoji codes.
func slackText(s string) map[string]any {
	return map[string]any{"type": "plain_text", "text": slackEscape(s), "emoji": false}
}

// slack is the payload of a Slack webhook: a block of plain text per entry, and a fallback text
// for notifications that doesn't quote any entry but names the panel. Texts are cut before they
// are escaped, which can make them four times as long, so that a block stays within Slack's
// 3000 characters.
func slack(m message, panel string) map[string]any {
	blocks := []map[string]any{}
	for _, e := range m.entries {
		text := plain(levelName(e)+": "+e.Message, 350) + "\n" + plain(details(e)+" · "+e.Time.UTC().Format(time.DateTime)+" UTC", 350)
		blocks = append(blocks, map[string]any{"type": "section", "text": slackText(text)})
	}
	fallback := slackEscape(panel) + ": new entries of the log"
	if summary := m.summary(); summary != "" {
		blocks = append(blocks, map[string]any{"type": "context", "elements": []any{slackText(summary)}})
	}
	if m.test {
		fallback = slackEscape(panel) + ": test"
		blocks = append(blocks, map[string]any{"type": "section", "text": slackText(testText)})
	}
	return map[string]any{"text": fallback, "blocks": blocks}
}

// Payload is what a generic webhook gets, as JSON: the entries of the log, oldest first, in
// the form the API returns them, and how many others weren't sent. A test has no entries.
type Payload struct {
	Test    bool         `json:"test"`
	Entries []logs.Entry `json:"entries"`
	NotSent int          `json:"notSent"`
}

func webhook(m message) Payload {
	return Payload{Test: m.test, Entries: append([]logs.Entry{}, m.entries...), NotSent: m.more}
}

// mail sends a message by mail, over TLS from the start or after STARTTLS, never in plain text.
func (s *Service) mail(ctx context.Context, ch *Channel, m message) error {
	cfg := ch.Email
	if cfg == nil {
		return failed("The channel has no mail server.")
	}
	conn, err := s.dialer.DialContext(ctx, "tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port))))
	if err != nil {
		return reason(err, "")
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	tlsConfig := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12, RootCAs: s.roots}
	if cfg.Security == ImplicitTLS {
		conn = tls.Client(conn, tlsConfig)
	}
	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return failed("The mail server didn't greet: %v", err)
	}
	defer c.Close()
	host, _ := os.Hostname()
	if err := c.Hello(cmpHost(host)); err != nil {
		return failed("The mail server refused the greeting: %v", err)
	}
	if cfg.Security == StartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return failed("The mail server doesn't offer STARTTLS, so the mail would go unencrypted.")
		}
		if err := c.StartTLS(tlsConfig); err != nil {
			return failed("The mail server's TLS failed: %v", err)
		}
	}
	if cfg.Username != "" {
		if err := c.Auth(&auth{user: cfg.Username, password: ch.secret}); err != nil {
			return failed("The mail server refused to sign in: %v", err)
		}
	}
	if err := c.Mail(cfg.From); err != nil {
		return failed("The mail server refused the sender: %v", err)
	}
	for _, to := range cfg.To {
		if err := c.Rcpt(to); err != nil {
			return failed("The mail server refused %s: %v", to, err)
		}
	}
	w, err := c.Data()
	if err == nil {
		_, err = w.Write(mailMessage(cfg, m, s.conf.PanelName()))
	}
	if err == nil {
		err = w.Close()
	}
	if err != nil {
		return failed("The mail server didn't take the mail: %v", err)
	}
	_ = c.Quit()
	return nil
}

// cmpHost is the name the master greets mail servers with: its host name, if it is one.
func cmpHost(host string) string {
	if hostname.MatchString(host) {
		return host
	}
	return "localhost"
}

// auth signs in to a mail server with PLAIN, or with LOGIN at servers that only offer that,
// and only over TLS.
type auth struct {
	user, password string
	login          bool
}

func (a *auth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if !server.TLS {
		return "", nil, errors.New("the connection isn't encrypted")
	}
	if !slices.Contains(server.Auth, "PLAIN") && slices.Contains(server.Auth, "LOGIN") {
		a.login = true
		return "LOGIN", nil, nil
	}
	return "PLAIN", []byte("\x00" + a.user + "\x00" + a.password), nil
}

func (a *auth) Next(challenge []byte, more bool) ([]byte, error) {
	switch prompt := strings.ToLower(strings.TrimSpace(string(challenge))); {
	case !more:
		return nil, nil
	case a.login && prompt == "username:":
		return []byte(a.user), nil
	case a.login && prompt == "password:":
		return []byte(a.password), nil
	}
	return nil, errors.New("unexpected challenge of the mail server")
}

// mailMessage is a plain text mail from the panel. Its subject quotes an entry, which can't add
// headers: it is a single line and encoded, as is the panel's name in it and as the sender.
func mailMessage(cfg *Mail, m message, panel string) []byte {
	subject, body := panel+": test", testText+"\n"
	if !m.test {
		subject = panel + ": " + plain(m.entries[0].Message, 150)
		if n := len(m.entries) + m.more - 1; n > 0 {
			subject += fmt.Sprintf(" (and %d more)", n)
		}
		var b strings.Builder
		for _, e := range m.entries {
			fmt.Fprintf(&b, "%s, %s UTC\n%s\n%s\n\n", levelName(e), e.Time.UTC().Format(time.DateTime), plain(e.Message, 2000), plain(details(e), 2000))
		}
		if summary := m.summary(); summary != "" {
			b.WriteString(summary + "\n")
		}
		body = b.String()
	}
	to := make([]string, len(cfg.To))
	for i, a := range cfg.To {
		to[i] = (&mail.Address{Address: a}).String()
	}
	_, domain, _ := strings.Cut(cfg.From, "@")
	var b bytes.Buffer
	for _, h := range [][2]string{
		{"From", (&mail.Address{Name: panel, Address: cfg.From}).String()},
		{"To", strings.Join(to, ", ")},
		{"Subject", mime.QEncoding.Encode("utf-8", subject)},
		{"Date", time.Now().Format(time.RFC1123Z)},
		{"Message-ID", "<" + strings.ToLower(rand.Text()) + "@" + domain + ">"},
		{"MIME-Version", "1.0"},
		{"Content-Type", "text/plain; charset=utf-8"},
		{"Content-Transfer-Encoding", "quoted-printable"},
		{"Auto-Submitted", "auto-generated"},
	} {
		fmt.Fprintf(&b, "%s: %s\r\n", h[0], h[1])
	}
	b.WriteString("\r\n")
	qp := quotedprintable.NewWriter(&b)
	_, _ = qp.Write([]byte(body))
	_ = qp.Close()
	return b.Bytes()
}
