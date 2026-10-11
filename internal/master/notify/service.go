// Package notify sends new entries of the log elsewhere: to Discord, Slack, a webhook or by
// mail. Rules choose the entries by their level, category, node and server, and send them to
// a channel. Entries that come at once are sent together, and each channel sends a few messages
// at once and then one a minute at most, which count what they leave out. Channels only
// connect to public addresses over TLS, and their secrets, the URLs of webhooks and the
// passwords of mail servers, never leave the master but to their target.
package notify

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/logs"
	"github.com/QwikByte/noryx/internal/master/ratelimit"
)

const (
	// pageSize entries of the log are read at once.
	pageSize = 500
	// A channel sends burst messages at once, then one every interval.
	burst    = 5
	interval = time.Minute
	// retryAfter is when a message that failed is sent once more.
	retryAfter = 30 * time.Second
	// readRetry is when the log is read again after reading it failed.
	readRetry = 10 * time.Second
)

// Config tells the name of the panel, which messages name as where they come from: in the
// subjects and as the sender of mails, and in the notifications of Slack.
type Config interface{ PanelName() string }

// Options are for tests: production uses none.
type Options struct {
	// Allow lets channels connect to the addresses it reports besides public ones, e.g. to
	// servers of tests on the loopback interface.
	Allow func(netip.Addr) bool
	// Roots are the certificate authorities to trust instead of the system's.
	Roots *x509.CertPool
	// Wait is how long entries wait for others to be sent with; 0 means 5 seconds.
	Wait time.Duration
}

// Service follows the log and sends what the rules choose through their channels.
type Service struct {
	db     *sql.DB
	logs   *logs.Store
	conf   Config
	dialer *net.Dialer
	client *http.Client
	roots  *x509.CertPool
	tests  *ratelimit.Limiter
	// wait is how long entries wait for others, and retry how long a message that failed waits
	// to be sent again; a channel sends burst messages at once, then one every interval.
	wait, retry, every time.Duration
	burst              int

	changes sync.Mutex // one change of channels and rules at a time

	state   sync.Mutex // guards what follows
	ctx     context.Context
	rules   []Rule
	senders map[string]*sender // by channel ID
}

// New returns the service of notifications, which Run starts.
func New(db *sql.DB, store *logs.Store, conf Config, o Options) *Service {
	dialer := newDialer(o.Allow)
	transport := &http.Transport{
		Proxy:                 nil, // a proxy would connect elsewhere than to the checked address
		DialContext:           dialer.DialContext,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: o.Roots},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       time.Minute,
	}
	wait := o.Wait
	if wait == 0 {
		wait = 5 * time.Second
	}
	return &Service{
		db: db, logs: store, conf: conf, dialer: dialer, roots: o.Roots, tests: ratelimit.New(3, 20*time.Second),
		wait: wait, retry: retryAfter, every: interval, burst: burst,
		client: &http.Client{Transport: transport, Timeout: sendTimeout, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		senders: map[string]*sender{},
	}
}

// Run sends the entries that are added to the log from now on, until ctx is done.
func (s *Service) Run(ctx context.Context) {
	ctx = access.WithGrants(ctx, access.Admin()) // rules apply to every entry
	s.state.Lock()
	s.ctx = ctx
	s.state.Unlock()
	var after int64
	for {
		newest, err := s.logs.List(ctx, logs.Filter{}, false, 1)
		if err == nil {
			if len(newest) > 0 {
				after = newest[0].ID
			}
			err = s.reload(ctx)
		}
		if err == nil {
			break
		}
		slog.Error("Can't start sending notifications", logging.Notifications, "err", err)
		if sleep(ctx, readRetry) != nil {
			return
		}
	}
	failing := false
	for {
		changed := s.logs.Changed()
		entries, err := s.logs.List(ctx, logs.Filter{Level: s.level(), After: after}, true, pageSize)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			if !failing {
				slog.Error("Can't read the log to send notifications", logging.Notifications, "err", err)
			}
			failing = true
			if sleep(ctx, readRetry) != nil {
				return
			}
			continue
		}
		failing = false
		for _, e := range entries {
			after = e.ID
			s.dispatch(e)
		}
		if len(entries) == pageSize {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-changed:
		}
	}
}

// level is the least important level of the enabled rules, so that the others aren't read.
func (s *Service) level() string {
	s.state.Lock()
	defer s.state.Unlock()
	least := slog.LevelError
	for _, r := range s.rules {
		if r.Enabled {
			least = min(least, r.level)
		}
	}
	return logging.LevelName(least)
}

// dispatch hands an entry to the channels of the rules that match it, to each once. A channel
// doesn't get the entries about itself, e.g. that it failed, so that it can't keep itself busy.
func (s *Service) dispatch(e logs.Entry) {
	s.state.Lock()
	defer s.state.Unlock()
	given := map[string]bool{}
	for _, r := range s.rules {
		q := s.senders[r.ChannelID]
		if q == nil || given[r.ChannelID] || !r.matches(e) || e.Category == logging.Notifications.Value.String() && e.Attrs["channel_id"] == r.ChannelID {
			continue
		}
		given[r.ChannelID] = true
		q.add(e)
	}
}

// reload applies the stored channels and rules. The changes lock is held, or Run starts.
func (s *Service) reload(ctx context.Context) error {
	channels, err := s.channels(ctx)
	if err != nil {
		return err
	}
	rules, err := s.Rules(ctx)
	if err != nil {
		return err
	}
	s.state.Lock()
	defer s.state.Unlock()
	s.rules = rules
	kept := map[string]bool{}
	for _, ch := range channels {
		kept[ch.ID] = true
		q := s.senders[ch.ID]
		switch {
		case q != nil:
			q.channel.Store(&ch)
		case s.ctx != nil:
			q = &sender{svc: s, limit: rate.NewLimiter(rate.Every(s.every), s.burst), wake: make(chan struct{}, 1)}
			q.channel.Store(&ch)
			var qctx context.Context
			qctx, q.stop = context.WithCancel(s.ctx)
			s.senders[ch.ID] = q
			go q.run(qctx)
		}
	}
	for id, q := range s.senders {
		if !kept[id] {
			q.stop()
			delete(s.senders, id)
		}
	}
	return nil
}

// Channels returns the channels as the panel sees them, without their secrets.
func (s *Service) Channels(ctx context.Context) ([]Channel, error) {
	channels, err := s.channels(ctx)
	for i := range channels {
		channels[i] = s.withStatus(channels[i])
	}
	return channels, err
}

// withStatus adds when a channel last sent and why it last failed.
func (s *Service) withStatus(ch Channel) Channel {
	s.state.Lock()
	q := s.senders[ch.ID]
	s.state.Unlock()
	if q != nil {
		q.mu.Lock()
		if !q.sentAt.IsZero() {
			ch.SentAt = new(q.sentAt)
		}
		ch.Problem = q.problem
		q.mu.Unlock()
	}
	return ch
}

// Test sends a test message through a channel right away. A channel can't be tested often, so
// that the master can't be used to send many messages.
func (s *Service) Test(ctx context.Context, id string) (Channel, error) {
	ch, err := s.channel(ctx, id)
	if err != nil {
		return ch, err
	}
	if !s.tests.Allow(id) {
		return ch, failed("Wait a moment before you send another test.")
	}
	return ch, s.send(ctx, &ch, message{test: true})
}

// Post sends an entry through a channel, as if a rule chose it from the log, e.g. a message
// of a workflow: with the entries that come at once, and only as often as the channel sends.
func (s *Service) Post(channelID string, e logs.Entry) error {
	s.state.Lock()
	q := s.senders[channelID]
	s.state.Unlock()
	if q == nil {
		return errChannelNotFound
	}
	q.add(e)
	return nil
}

// Fetch sends a request, e.g. of a workflow, as channels send theirs: over HTTPS to a public
// address, without a proxy and without following redirects. Its error tells why it failed in
// words for the panel.
func (s *Service) Fetch(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || req.URL.Host == "" {
		return nil, failed("Enter a URL starting with https://.")
	}
	if ip, err := netip.ParseAddr(req.URL.Hostname()); err == nil && !public(ip) {
		return nil, failed("%s", (&refusedError{ip}).Error())
	}
	res, err := s.client.Do(req) //nolint:gosec // G704: its dialer only connects to public addresses
	if err != nil {
		return nil, reason(err, "")
	}
	return res, nil
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// sender sends the entries of a channel: those that come at once in one message, and few
// messages, which count the entries they leave out.
type sender struct {
	svc     *Service
	channel atomic.Pointer[Channel]
	limit   *rate.Limiter
	wake    chan struct{}
	stop    context.CancelFunc

	mu      sync.Mutex
	queue   []logs.Entry
	more    int // entries left out, which the next message counts
	sentAt  time.Time
	problem string
}

// add queues an entry, or counts it if the next message is full.
func (q *sender) add(e logs.Entry) {
	q.mu.Lock()
	if len(q.queue) < maxEntries {
		q.queue = append(q.queue, e)
	} else {
		q.more++
	}
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *sender) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-q.wake:
		}
		if sleep(ctx, q.svc.wait) != nil {
			return
		}
		q.mu.Lock()
		empty := len(q.queue) == 0
		q.mu.Unlock()
		if empty || q.limit.Wait(ctx) != nil {
			continue // the entries went with the message before, or the channel is gone
		}
		q.mu.Lock()
		m := message{entries: q.queue, more: q.more}
		q.queue, q.more = nil, 0
		q.mu.Unlock()
		err := q.svc.send(ctx, q.channel.Load(), m)
		if err != nil && sleep(ctx, q.svc.retry) == nil {
			err = q.svc.send(ctx, q.channel.Load(), m)
		}
		if ctx.Err() != nil {
			return
		}
		q.note(err, len(m.entries)+m.more)
	}
}

// note records how sending n entries went, and logs when the channel starts to fail and when
// it works again, with the reason, which never holds the channel's secret.
func (q *sender) note(err error, n int) {
	ch := q.channel.Load()
	q.mu.Lock()
	was := q.problem
	if err == nil {
		q.sentAt, q.problem = time.Now(), ""
	} else {
		q.problem, q.more = err.Error(), q.more+n
	}
	q.mu.Unlock()
	attrs := []any{logging.Notifications, "channel", ch.Name, "channel_id", ch.ID}
	switch {
	case err != nil && was == "":
		slog.Warn("A notification channel can't send", append(attrs, "err", err.Error())...)
	case err == nil && was != "":
		slog.Info("A notification channel sends again", attrs...)
	}
}

// fresh forgets why the channel failed, as it changed.
func (q *sender) fresh() {
	q.mu.Lock()
	q.problem = ""
	q.mu.Unlock()
}
