package notify

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/logs"
	"github.com/QwikByte/noryx/internal/master/schedule"
	"github.com/QwikByte/noryx/internal/master/tag"
)

type limits struct{}

func (limits) LogRetention() time.Duration { return 24 * time.Hour }
func (limits) LogMaxSize() int64           { return 1 << 30 }

// hook is a webhook that records the payloads it gets, and answers with status.
type hook struct {
	mu       sync.Mutex
	payloads []Payload
	status   int
}

func (h *hook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var p Payload
	_ = json.NewDecoder(r.Body).Decode(&p)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.payloads = append(h.payloads, p)
	if h.status != 0 {
		w.WriteHeader(h.status)
	}
}

func (h *hook) got() []Payload {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Payload{}, h.payloads...)
}

// groups are the servers of tags and networks in tests, which they change.
type groups struct {
	mu      sync.Mutex
	servers map[schedule.Target][]tag.Server
}

func (g *groups) Matcher(_ context.Context, targets []schedule.Target) (func(nodeID, serverID string) bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var servers []tag.Server
	for _, t := range targets {
		servers = append(servers, g.servers[t]...)
	}
	return func(nodeID, serverID string) bool {
		return slices.Contains(servers, tag.Server{NodeID: nodeID, ServerID: serverID})
	}, nil
}

func (g *groups) set(t schedule.Target, servers ...tag.Server) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.servers[t] = servers
}

// setup starts the log, a webhook and the service with a channel to it, and returns a logger
// whose entries reach the log, as do those of the service. Its targets are groups.
func setup(t *testing.T, h *hook) (*Service, *slog.Logger, *logs.Store, Channel) {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "master.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store := logs.NewStore(db, logs.NewNames(nil), limits{})
	ctx, cancel := context.WithCancel(context.Background())
	store.Start(ctx)
	prev := slog.Default()
	slog.SetDefault(slog.New(store.Handler(slog.LevelInfo)))
	target, roots := localServer(t, h)
	targets := &groups{servers: map[schedule.Target][]tag.Server{}}
	s := New(db, store, targets, panel("Noryx"), Options{Allow: loopback, Roots: roots, Wait: 300 * time.Millisecond})
	s.retry = 50 * time.Millisecond
	ch, err := s.CreateChannel(ctx, ChannelInput{Name: "Hook", Kind: Webhook, URL: target + "/hook/secret-token"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		s.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		slog.SetDefault(prev)
		store.Close()
	})
	// Entries count from when Run started.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		s.state.Lock()
		running := s.senders[ch.ID] != nil
		s.state.Unlock()
		if running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the service didn't start")
		}
	}
	return s, slog.New(store.Handler(slog.LevelDebug)), store, ch
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("waited in vain for %s", what)
		}
	}
}

// Entries that come at once go in one message, which counts those that don't fit, and a
// channel sends few messages.
func TestRulesBatchAndLimit(t *testing.T) {
	h := &hook{}
	s, log, _, ch := setup(t, h)
	s.burst, s.every = 1, time.Hour
	s.state.Lock()
	delete(s.senders, ch.ID) // started again with the new limits
	s.state.Unlock()
	if _, err := s.CreateRule(t.Context(), RuleInput{ChannelID: ch.ID, Enabled: true, Level: "warn", Categories: []string{"servers", "servers"}}); err != nil {
		t.Fatal(err)
	}
	disabled, err := s.CreateRule(t.Context(), RuleInput{ChannelID: ch.ID, Level: "info"})
	if err != nil || disabled.Enabled {
		t.Fatal(disabled, err)
	}
	log.Info("Started server", logging.Servers)
	log.Warn("A node went offline", logging.Nodes)
	for range 12 {
		log.Warn("A server crashed", logging.Servers, logging.KeyNode, "n1", logging.KeyNodeName, "node-1", logging.KeyServer, "s1", logging.KeyServerName, "Lobby")
	}
	waitFor(t, "a message", func() bool { return len(h.got()) > 0 })
	p := h.got()[0]
	if len(p.Entries) != perMessage || p.NotSent != 2 || p.Test || p.Entries[0].Message != "A server crashed" || p.Entries[0].ServerName != "Lobby" {
		t.Errorf("payload = %+v", p)
	}
	log.Error("A server crashed again", logging.Servers)
	time.Sleep(time.Second)
	if n := len(h.got()); n != 1 {
		t.Errorf("%d messages despite the limit", n)
	}
}

// A channel that fails is tried once more, and logged once, but doesn't get the entry about
// itself.
func TestFailingChannel(t *testing.T) {
	h := &hook{status: http.StatusInternalServerError}
	s, log, store, ch := setup(t, h)
	if _, err := s.CreateRule(t.Context(), RuleInput{ChannelID: ch.ID, Enabled: true, Level: "warn"}); err != nil {
		t.Fatal(err)
	}
	log.Warn("A server crashed", logging.Servers)
	waitFor(t, "the problem", func() bool {
		channels, _ := s.Channels(t.Context())
		return len(channels) == 1 && channels[0].Problem != ""
	})
	time.Sleep(time.Second)
	if n := len(h.got()); n != 2 {
		t.Errorf("%d attempts", n)
	}
	entries, err := store.List(access.WithGrants(t.Context(), access.Admin()), logs.Filter{Category: "notifications"}, true, 10)
	if err != nil || len(entries) != 1 || entries[0].Attrs["channel_id"] != ch.ID || !strings.Contains(entries[0].Attrs["err"], "500") ||
		strings.Contains(entries[0].Attrs["err"], "secret-token") {
		t.Errorf("entries = %+v, %v", entries, err)
	}
	channels, _ := s.Channels(t.Context())
	if channels[0].Problem != "The target answered 500 Internal Server Error." {
		t.Errorf("problem = %q", channels[0].Problem)
	}
	// It counts what it couldn't send in the next message.
	h.mu.Lock()
	h.status = 0
	h.mu.Unlock()
	log.Warn("A server crashed again", logging.Servers)
	waitFor(t, "a message", func() bool { return len(h.got()) == 3 })
	if p := h.got()[2]; len(p.Entries) != 1 || p.NotSent != 1 {
		t.Errorf("payload = %+v", p)
	}
}

// The API never returns the secrets of channels, and keeps them unless they are replaced; a
// mail password stays with its server and user.
func TestSecretsStayHidden(t *testing.T) {
	s, _, _, ch := setup(t, &hook{})
	ctx := t.Context()
	mailIn := ChannelInput{Name: "Mail", Kind: Email, Password: "the-mail-password", Email: &Mail{
		Host: "smtp.example.com", Security: StartTLS, Username: "noryx", From: "noryx@example.com", To: []string{" ops@example.com", "ops@example.com"},
	}}
	mailCh, err := s.CreateChannel(ctx, mailIn)
	if err != nil {
		t.Fatal(err)
	}
	if mailCh.Email.Port != 587 || len(mailCh.Email.To) != 1 || !mailCh.HasPassword || mailCh.MessagesPerMinute != perMinute ||
		mailCh.EntriesPerMessage != perMessage || mailCh.TimeZone != "" {
		t.Errorf("mail channel = %+v", mailCh)
	}
	if _, err := s.UpdateChannel(ctx, ch.ID, ChannelInput{Name: "Renamed", Kind: Webhook}); err != nil {
		t.Fatal(err)
	}
	mailIn.Password = ""
	if _, err := s.UpdateChannel(ctx, mailCh.ID, mailIn); err != nil {
		t.Fatal(err)
	}
	mailIn.Email.Host = "smtp.elsewhere.example"
	if _, err := s.UpdateChannel(ctx, mailCh.ID, mailIn); err == nil || !strings.Contains(err.Error(), "password") {
		t.Errorf("another server got the password: %v", err)
	}
	if _, err := s.UpdateChannel(ctx, mailCh.ID, ChannelInput{Name: "Mail", Kind: Webhook, URL: "https://example.com/hook"}); err == nil {
		t.Error("the kind changed")
	}
	channels, err := s.Channels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(channels)
	if strings.Contains(string(data), "secret-token") || strings.Contains(string(data), "the-mail-password") || !strings.Contains(string(data), `"host":"localhost:`) {
		t.Errorf("channels = %s", data)
	}
	stored, _ := s.channel(ctx, ch.ID)
	mailStored, _ := s.channel(ctx, mailCh.ID)
	if stored.Name != "Renamed" || !strings.HasSuffix(stored.secret, "/hook/secret-token") || mailStored.secret != "the-mail-password" {
		t.Errorf("secrets = %q, %q", stored.secret, mailStored.secret)
	}
	for _, in := range []ChannelInput{
		{Name: "Plain", Kind: Webhook, URL: "http://example.com/hook"},
		{Name: "Local", Kind: Discord, URL: "https://127.0.0.1/hook"},
		{Name: "Mail", Kind: Slack, URL: "https://example.com/hook"},
		{Name: "No TLS", Kind: Email, Email: &Mail{Host: "smtp.example.com", Security: "none", From: "a@example.com", To: []string{"b@example.com"}}},
		{Name: "Private", Kind: Email, Email: &Mail{Host: "10.0.0.1", Security: ImplicitTLS, From: "a@example.com", To: []string{"b@example.com"}}},
		{Name: "Header", Kind: Email, Email: &Mail{Host: "smtp.example.com", Security: ImplicitTLS, From: "a@example.com", To: []string{"b@example.com\r\nBcc: c@example.com"}}},
		{Name: "Flood", Kind: Webhook, URL: "https://example.com/hook", MessagesPerMinute: maxPerMinute + 1},
		{Name: "Long", Kind: Slack, URL: "https://example.com/hook", EntriesPerMessage: maxPerMessage + 1},
		{Name: "Embeds", Kind: Discord, URL: "https://example.com/hook", EntriesPerMessage: maxDiscordEntries + 1},
		{Name: "None", Kind: Webhook, URL: "https://example.com/hook", EntriesPerMessage: -1},
		{Name: "Local", Kind: Webhook, URL: "https://example.com/hook", TimeZone: "Local"},
		{Name: "Mars", Kind: Webhook, URL: "https://example.com/hook", TimeZone: "Mars/Olympus_Mons"},
	} {
		if _, err := s.CreateChannel(ctx, in); err == nil {
			t.Errorf("%s was created", in.Name)
		}
	}
	// Deleting a channel deletes its rules.
	if _, err := s.CreateRule(ctx, RuleInput{ChannelID: mailCh.ID, Level: "error"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteChannel(ctx, mailCh.ID); err != nil {
		t.Fatal(err)
	}
	if rules, err := s.Rules(ctx); err != nil || len(rules) != 0 {
		t.Errorf("rules = %+v, %v", rules, err)
	}
	for _, in := range []RuleInput{
		{ChannelID: ch.ID, Level: "debug"}, {ChannelID: "missing", Level: "warn"}, {ChannelID: ch.ID, Level: "warn", ServerID: "s1"},
		{ChannelID: ch.ID, Level: "warn", Categories: []string{"Not one"}}, {ChannelID: ch.ID, Level: "warn", Tag: "no tag!"},
		{ChannelID: ch.ID, Level: "warn", NodeID: "n1", Tag: "lobby"}, {ChannelID: ch.ID, Level: "warn", Tag: "lobby", NetworkID: "n"},
		{ChannelID: ch.ID, Level: "warn", NetworkID: "missing"},
	} {
		if _, err := s.CreateRule(ctx, in); err == nil {
			t.Errorf("rule %+v was created", in)
		}
	}
}

// Rules follow a server that moves, and leave with a server or node that is gone, rather than
// sending the entries of its node or of all nodes. Rules of tags stay.
func TestRulesFollowServers(t *testing.T) {
	s, _, _, ch := setup(t, &hook{})
	for _, in := range []RuleInput{{NodeID: "n1", ServerID: "s1"}, {NodeID: "n1"}, {}, {Tag: "lobby"}} {
		in.ChannelID, in.Enabled, in.Level = ch.ID, true, "warn"
		if _, err := s.CreateRule(t.Context(), in); err != nil {
			t.Fatal(err)
		}
	}
	where := func() []string {
		rules, err := s.Rules(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, r := range rules {
			got = append(got, r.NodeID+"/"+r.ServerID+r.Tag)
		}
		slices.Sort(got)
		return got
	}
	if err := s.Move(t.Context(), "s1", "n1", "n2"); err != nil {
		t.Fatal(err)
	}
	if got := where(); !slices.Equal(got, []string{"/", "/lobby", "n1/", "n2/s1"}) {
		t.Fatalf("after the move = %q", got)
	}
	if err := s.Forget(t.Context(), "n2", "s1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Prune(t.Context()); err != nil { // n1 doesn't exist
		t.Fatal(err)
	}
	if got := where(); !slices.Equal(got, []string{"/", "/lobby"}) {
		t.Fatalf("after deleting the server and node = %q", got)
	}
}

// Rules of tags and networks send the entries about the servers that have the tag or are in
// the network as the entries come, also once servers moved or tags changed, and never those
// about nodes alone.
func TestRulesOfTagsAndNetworks(t *testing.T) {
	h := &hook{}
	s, log, _, ch := setup(t, h)
	g, lobby := s.targets.(*groups), schedule.Target{Kind: schedule.KindTag, Value: "lobby"}
	g.set(lobby, tag.Server{NodeID: "n1", ServerID: "s1"})
	if r, err := s.CreateRule(t.Context(), RuleInput{ChannelID: ch.ID, Enabled: true, Level: "warn", Tag: " Lobby "}); err != nil || r.Tag != "lobby" {
		t.Fatal(r, err)
	}
	crash := func(nodeID, serverID string) {
		log.Warn("A server crashed", logging.Servers, logging.KeyNode, nodeID, logging.KeyNodeName, nodeID, logging.KeyServer, serverID,
			logging.KeyServerName, serverID)
	}
	sent := func(p Payload) (got []string) {
		for _, e := range p.Entries {
			got = append(got, e.NodeID+"/"+e.ServerID)
		}
		return got
	}
	log.Warn("A node went offline", logging.Nodes, logging.KeyNode, "n1", logging.KeyNodeName, "n1")
	crash("n1", "s2")
	crash("n1", "s1")
	waitFor(t, "a message", func() bool { return len(h.got()) == 1 })
	if got := sent(h.got()[0]); !slices.Equal(got, []string{"n1/s1"}) {
		t.Fatalf("sent %q", got)
	}
	// The server moved, another one got the tag, and the rule finds them once its servers are
	// looked up again.
	g.set(lobby, tag.Server{NodeID: "n2", ServerID: "s1"}, tag.Server{NodeID: "n1", ServerID: "s2"})
	s.state.Lock()
	s.resolved = time.Time{}
	s.state.Unlock()
	crash("n1", "s1")
	crash("n2", "s1")
	crash("n1", "s2")
	waitFor(t, "another message", func() bool { return len(h.got()) == 2 })
	if got := sent(h.got()[1]); !slices.Equal(got, []string{"n2/s1", "n1/s2"}) {
		t.Fatalf("sent %q", got)
	}

	// A network has its proxy and game servers; a rule of a network that isn't resolved sends nothing.
	main := schedule.Target{Kind: schedule.KindNetwork, Value: "main"}
	groups := map[schedule.Target]func(nodeID, serverID string) bool{main: func(_, serverID string) bool { return serverID == "proxy" }}
	r := Rule{Enabled: true, level: slog.LevelWarn, NetworkID: "main"}
	e := logs.Entry{Level: slog.LevelWarn, NodeID: "n1", ServerID: "proxy"}
	if !r.matches(e, groups) || r.matches(e, nil) || r.matches(logs.Entry{Level: slog.LevelWarn, NodeID: "n1", ServerID: "s1"}, groups) {
		t.Error("a rule of a network matches other servers")
	}
}

// Each channel sends as many messages a minute and entries a message as it chose, and counts
// the others.
func TestChannelRates(t *testing.T) {
	h := &hook{}
	s, log, _, ch := setup(t, h)
	s.burst, s.every = 1, 10*time.Second
	s.state.Lock()
	delete(s.senders, ch.ID) // started again with the new limits
	s.state.Unlock()
	in := ChannelInput{Name: ch.Name, Kind: Webhook, MessagesPerMinute: 10, EntriesPerMessage: 2}
	if _, err := s.UpdateChannel(t.Context(), ch.ID, in); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRule(t.Context(), RuleInput{ChannelID: ch.ID, Enabled: true, Level: "warn"}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		log.Warn("A server crashed", logging.Servers)
	}
	waitFor(t, "a message", func() bool { return len(h.got()) == 1 })
	if p := h.got()[0]; len(p.Entries) != 2 || p.NotSent != 1 {
		t.Errorf("payload = %+v", p)
	}
	// 10 messages in 10 seconds: the next one goes after a second.
	log.Warn("A server crashed again", logging.Servers)
	waitFor(t, "the next message", func() bool { return len(h.got()) == 2 })
	// One message in 10 seconds: the next one waits.
	in.MessagesPerMinute = 1
	if _, err := s.UpdateChannel(t.Context(), ch.ID, in); err != nil {
		t.Fatal(err)
	}
	log.Warn("A server crashed once more", logging.Servers)
	time.Sleep(2 * time.Second)
	if n := len(h.got()); n != 2 {
		t.Errorf("%d messages despite the limit", n)
	}
}
