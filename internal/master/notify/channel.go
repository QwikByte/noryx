package notify

import (
	"cmp"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	_ "time/tzdata" // channels give times in IANA time zones, which minimal systems don't have
	"unicode"

	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/httpapi"
)

// Kinds of channels.
const (
	Discord = "discord"
	Slack   = "slack"
	Webhook = "webhook"
	Email   = "email"
)

// Security of the connection to a mail server: TLS from the start, e.g. at port 465, or
// STARTTLS, e.g. at port 587. Plain text isn't offered, as it would expose the password.
const (
	ImplicitTLS = "tls"
	StartTLS    = "starttls"
)

const (
	maxChannels   = 50
	maxName       = 64
	maxURL        = 2048
	maxRecipients = 10
	maxPassword   = 1024
)

// A channel sends up to perMinute messages a minute once the first few went, see burst, with up
// to perMessage entries each, and counts the others, unless it chooses others within the bounds,
// which keep a server in a crash loop from flooding it. Discord takes at most 10 embeds, one per
// entry, in a message.
const (
	perMinute, maxPerMinute   = 1, 10
	perMessage, maxPerMessage = 10, 25
	maxDiscordEntries         = 10
)

var (
	errChannelNotFound = httpapi.Errorf(http.StatusNotFound, "Notification channel not found.")
	hostname           = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$`)
)

// Channel sends notifications to a chat, a webhook or by mail. Its secret, the URL of a
// webhook or the password of the mail server, is never shown.
type Channel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Host is where the channel sends to: the host of a webhook's URL, or the mail server.
	Host      string    `json:"host"`
	Email     *Mail     `json:"email,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	// MessagesPerMinute is how many messages it sends a minute at most once the first few went,
	// and EntriesPerMessage how many entries each has at most.
	MessagesPerMinute int `json:"messagesPerMinute"`
	EntriesPerMessage int `json:"entriesPerMessage"`
	// TimeZone is the IANA time zone of the times in its texts; empty is UTC. Webhooks get the
	// times in UTC whatever it is.
	TimeZone string `json:"timeZone"`
	// HasPassword tells whether a mail channel signs in with a password.
	HasPassword bool `json:"hasPassword,omitempty"`
	// SentAt is when the channel last sent a notification, and Problem why the last one failed,
	// since the master started.
	SentAt  *time.Time `json:"sentAt,omitempty"`
	Problem string     `json:"problem,omitempty"`

	secret string
}

// Mail are the settings of a mail channel besides the password.
type Mail struct {
	Host string `json:"host"`
	Port uint16 `json:"port"`
	// Security is ImplicitTLS or StartTLS.
	Security string   `json:"security"`
	Username string   `json:"username"`
	From     string   `json:"from"`
	To       []string `json:"to"`
}

// ChannelInput is a new or changed channel.
type ChannelInput struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	// URL of a webhook; empty keeps the one stored.
	URL   string `json:"url"`
	Email *Mail  `json:"email"`
	// Password of the mail server; empty keeps the one stored, which a channel only keeps while
	// its server and user stay the same, so that it can't be sent to another server.
	Password string `json:"password"`
	// Zero numbers are 1 message a minute and 10 entries a message, and an empty time zone is UTC.
	MessagesPerMinute int    `json:"messagesPerMinute"`
	EntriesPerMessage int    `json:"entriesPerMessage"`
	TimeZone          string `json:"timeZone"`
}

// build validates a channel; old is the stored one when it changes.
func (in ChannelInput) build(old *Channel) (Channel, error) {
	ch := Channel{
		Name: strings.TrimSpace(in.Name), Kind: in.Kind, TimeZone: in.TimeZone,
		MessagesPerMinute: cmp.Or(in.MessagesPerMinute, perMinute), EntriesPerMessage: cmp.Or(in.EntriesPerMessage, perMessage),
	}
	entries := maxPerMessage
	if ch.Kind == Discord {
		entries = maxDiscordEntries
	}
	switch {
	case ch.Name == "" || len(ch.Name) > maxName || strings.ContainsFunc(ch.Name, unicode.IsControl):
		return ch, httpapi.Errorf(http.StatusBadRequest, "Enter a name of up to %d characters.", maxName)
	case old != nil && old.Kind != ch.Kind:
		return ch, httpapi.Errorf(http.StatusBadRequest, "A channel keeps its kind. Create another channel instead.")
	case ch.MessagesPerMinute < 1 || ch.MessagesPerMinute > maxPerMinute:
		return ch, httpapi.Errorf(http.StatusBadRequest, "Choose 1 to %d messages a minute.", maxPerMinute)
	case ch.EntriesPerMessage < 1 || ch.EntriesPerMessage > entries:
		return ch, httpapi.Errorf(http.StatusBadRequest, "Choose 1 to %d entries a message.", entries)
	case !zoneName(ch.TimeZone):
		return ch, httpapi.Errorf(http.StatusBadRequest, "Choose an IANA time zone, e.g. Europe/Berlin, or none for UTC.")
	}
	switch ch.Kind {
	case Discord, Slack, Webhook:
		if in.URL == "" && old != nil {
			ch.Host, ch.secret = old.Host, old.secret
			return ch, nil
		}
		u, err := checkURL(in.URL)
		if err != nil {
			return ch, err
		}
		ch.Host, ch.secret = u.Host, u.String()
	case Email:
		if in.Email == nil {
			return ch, httpapi.Errorf(http.StatusBadRequest, "Enter the mail server and the addresses.")
		}
		m, err := in.Email.check()
		if err != nil {
			return ch, err
		}
		ch.Email, ch.Host, ch.secret = &m, m.Host, in.Password
		same := old != nil && old.Email != nil && old.Email.Host == m.Host && old.Email.Port == m.Port && old.Email.Username == m.Username
		switch {
		case m.Username == "":
			ch.secret = ""
		case in.Password == "" && same:
			ch.secret = old.secret
		case in.Password == "":
			return ch, httpapi.Errorf(http.StatusBadRequest, "Enter the password of the mail server.")
		case len(in.Password) > maxPassword || strings.ContainsFunc(in.Password, unicode.IsControl):
			return ch, httpapi.Errorf(http.StatusBadRequest, "The password can't have more than %d characters or line breaks.", maxPassword)
		}
	default:
		return ch, httpapi.Errorf(http.StatusBadRequest, "Choose the kind discord, slack, webhook or email.")
	}
	return ch, nil
}

// zoneName tells whether a time zone is empty, which is UTC, or an IANA time zone. Local, the
// master's own, isn't one, and IANA names are short, so longer ones aren't looked up at all.
func zoneName(name string) bool {
	if len(name) > 64 || name == "Local" {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}

// zone is the time zone of the times in the channel's texts, which zoneName checked.
func (ch *Channel) zone() *time.Location {
	if loc, err := time.LoadLocation(ch.TimeZone); err == nil {
		return loc
	}
	return time.UTC
}

// checkURL checks the URL of a webhook: HTTPS, and not at an address that isn't public, which
// connections check again for addresses that names resolve to. Errors never repeat the URL.
func checkURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || len(raw) > maxURL || u.Scheme != "https" || u.Host == "" || u.Opaque != "" {
		return nil, httpapi.Errorf(http.StatusBadRequest, "Enter the URL of the webhook, starting with https://.")
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !public(ip) {
		return nil, httpapi.Errorf(http.StatusBadRequest, "%s", (&refusedError{ip}).Error())
	}
	u.Fragment, u.RawFragment = "", ""
	return u, nil
}

func (m Mail) check() (Mail, error) {
	m.Host = strings.TrimSpace(m.Host)
	m.Username = strings.TrimSpace(m.Username)
	if ip, err := netip.ParseAddr(m.Host); err == nil && !public(ip) {
		return m, httpapi.Errorf(http.StatusBadRequest, "%s", (&refusedError{ip}).Error())
	} else if err != nil && !hostname.MatchString(m.Host) {
		return m, httpapi.Errorf(http.StatusBadRequest, "Enter the name or address of the mail server.")
	}
	switch m.Security {
	case ImplicitTLS:
		m.Port = cmp.Or(m.Port, 465)
	case StartTLS:
		m.Port = cmp.Or(m.Port, 587)
	default:
		return m, httpapi.Errorf(http.StatusBadRequest, "Choose TLS or STARTTLS for the mail server.")
	}
	if len(m.Username) > 256 || strings.ContainsFunc(m.Username, unicode.IsControl) {
		return m, httpapi.Errorf(http.StatusBadRequest, "The user name is too long.")
	}
	from, err := address(m.From)
	if err != nil {
		return m, httpapi.Errorf(http.StatusBadRequest, "Enter the address the mails come from, e.g. noryx@example.com.")
	}
	m.From = from
	to := []string{}
	for _, a := range m.To {
		if a = strings.TrimSpace(a); a == "" {
			continue
		}
		addr, err := address(a)
		if err != nil {
			return m, httpapi.Errorf(http.StatusBadRequest, "%q isn't a mail address.", a)
		}
		if !slices.Contains(to, addr) {
			to = append(to, addr)
		}
	}
	if len(to) == 0 || len(to) > maxRecipients {
		return m, httpapi.Errorf(http.StatusBadRequest, "Enter from 1 to %d addresses the mails go to.", maxRecipients)
	}
	m.To = to
	return m, nil
}

// address reads a plain mail address, without a name.
func address(s string) (string, error) {
	a, err := mail.ParseAddress(strings.TrimSpace(s))
	if err != nil || a.Name != "" || len(a.Address) > 254 {
		return "", errors.New("invalid mail address")
	}
	return a.Address, nil
}

const channelColumns = `id, name, kind, email, secret, created_at, messages_per_minute, entries_per_message, time_zone`

func scanChannel(row database.Scanner) (Channel, error) {
	var (
		ch    Channel
		email string
		at    int64
	)
	if err := row.Scan(&ch.ID, &ch.Name, &ch.Kind, &email, &ch.secret, &at, &ch.MessagesPerMinute, &ch.EntriesPerMessage, &ch.TimeZone); err != nil {
		return ch, err
	}
	ch.CreatedAt = time.Unix(at, 0)
	if email != "" {
		ch.Email = &Mail{}
		if err := json.Unmarshal([]byte(email), ch.Email); err != nil {
			return ch, err
		}
		ch.Host, ch.HasPassword = ch.Email.Host, ch.secret != ""
	} else if u, err := url.Parse(ch.secret); err == nil {
		ch.Host = u.Host
	}
	return ch, nil
}

// channels returns the stored channels with their secrets.
func (s *Service) channels(ctx context.Context) ([]Channel, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+channelColumns+` FROM notification_channels ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	channels := []Channel{}
	for rows.Next() {
		ch, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		channels = append(channels, ch)
	}
	return channels, rows.Err()
}

func (s *Service) channel(ctx context.Context, id string) (Channel, error) {
	ch, err := scanChannel(s.db.QueryRowContext(ctx, `SELECT `+channelColumns+` FROM notification_channels WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		err = errChannelNotFound
	}
	return ch, err
}

// CreateChannel stores a new channel.
func (s *Service) CreateChannel(ctx context.Context, in ChannelInput) (Channel, error) {
	ch, err := in.build(nil)
	if err != nil {
		return ch, err
	}
	s.changes.Lock()
	defer s.changes.Unlock()
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notification_channels`).Scan(&n); err != nil {
		return ch, err
	}
	if n >= maxChannels {
		return ch, httpapi.Errorf(http.StatusConflict, "The master keeps at most %d notification channels.", maxChannels)
	}
	ch.ID, ch.CreatedAt = strings.ToLower(rand.Text()), time.Now()
	email, err := ch.emailJSON()
	if err == nil {
		_, err = s.db.ExecContext(ctx, `INSERT INTO notification_channels (`+channelColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			ch.ID, ch.Name, ch.Kind, email, ch.secret, ch.CreatedAt.Unix(), ch.MessagesPerMinute, ch.EntriesPerMessage, ch.TimeZone)
	}
	if err != nil {
		return ch, uniqueName(err)
	}
	return s.saved(ctx, ch.ID)
}

// UpdateChannel changes a channel. It keeps its secret unless the input has another one.
func (s *Service) UpdateChannel(ctx context.Context, id string, in ChannelInput) (Channel, error) {
	s.changes.Lock()
	defer s.changes.Unlock()
	old, err := s.channel(ctx, id)
	if err != nil {
		return old, err
	}
	ch, err := in.build(&old)
	if err != nil {
		return ch, err
	}
	email, err := ch.emailJSON()
	if err == nil {
		_, err = s.db.ExecContext(ctx, `UPDATE notification_channels SET name = ?, email = ?, secret = ?, messages_per_minute = ?, entries_per_message = ?,
			time_zone = ? WHERE id = ?`, ch.Name, email, ch.secret, ch.MessagesPerMinute, ch.EntriesPerMessage, ch.TimeZone, id)
	}
	if err != nil {
		return ch, uniqueName(err)
	}
	s.state.Lock()
	if q := s.senders[id]; q != nil {
		q.fresh()
	}
	s.state.Unlock()
	return s.saved(ctx, id)
}

// DeleteChannel deletes a channel and its rules.
func (s *Service) DeleteChannel(ctx context.Context, id string) (Channel, error) {
	s.changes.Lock()
	defer s.changes.Unlock()
	ch, err := s.channel(ctx, id)
	if err == nil {
		_, err = s.db.ExecContext(ctx, `DELETE FROM notification_channels WHERE id = ?`, id)
	}
	if err == nil {
		err = s.reload(ctx)
	}
	return ch, err
}

// saved applies the stored channels and rules, and returns a channel as the panel sees it.
func (s *Service) saved(ctx context.Context, id string) (Channel, error) {
	if err := s.reload(ctx); err != nil {
		return Channel{}, err
	}
	ch, err := s.channel(ctx, id)
	return s.withStatus(ch), err
}

func (ch Channel) emailJSON() (string, error) {
	if ch.Email == nil {
		return "", nil
	}
	data, err := json.Marshal(ch.Email)
	return string(data), err
}

func uniqueName(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return httpapi.Errorf(http.StatusConflict, "Another notification channel has this name.")
	}
	return err
}
