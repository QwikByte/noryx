// Package update tells administrators about new releases of Noryx and installs
// them. The master may not install anything itself: it asks a systemd unit that runs as root
// to install the latest release, restarts on it and then updates the agents through their
// connections, unless the settings keep them as they are.
package update

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/semver"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/buildinfo"
	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/node"
)

const (
	// DefaultAPI lists the releases, the newest first; at /latest, it describes the latest
	// release, which GitHub chooses among those that aren't pre-releases.
	DefaultAPI = "https://api.github.com/repos/QwikByte/noryx/releases"
	// DefaultPage redirects to the latest release. It only tells its version, but GitHub
	// doesn't limit how often an IP address asks for it, unlike the API.
	DefaultPage = buildinfo.Repository + "/releases/latest"
	// DefaultUnit makes systemd install the latest release when the master asks for it.
	DefaultUnit = "/usr/lib/systemd/system/noryx-master-update.path"

	// listed is how many of the newest releases the master looks at for pre-releases, like the
	// installer.
	listed = 10
	// wake is how often the master looks whether a check is due, so that a new interval in the
	// settings applies soon.
	wake         = time.Minute
	probeTimeout = 5 * time.Second
	maxResponse  = 1 << 20
	// pending is how long an update may take before the panel reports it as failed.
	pending = 5 * time.Minute
)

// Release is a published release of Noryx.
type Release struct {
	Version string `json:"version"`
	// Notes and PublishedAt are empty if the version comes from the release page.
	Notes       string    `json:"notes"`
	URL         string    `json:"url"`
	PublishedAt time.Time `json:"publishedAt,omitzero"`
}

// Status is what administrators see about updates.
type Status struct {
	Version    string     `json:"version"`          // of the master
	Latest     *Release   `json:"latest,omitempty"` // the latest release, if newer than the master
	CheckedAt  *time.Time `json:"checkedAt,omitempty"`
	CheckError string     `json:"checkError,omitempty"`
	// Updatable tells whether the master can update itself; otherwise it is updated on its host.
	Updatable bool `json:"updatable"`
	// Prereleases tells whether the master offers pre-releases too, as --prereleases on its host
	// says. AgentsFollow tells whether it updates the agents once it runs the new release.
	Prereleases  bool      `json:"prereleases"`
	AgentsFollow bool      `json:"agentsFollow"`
	Master       *Progress `json:"master,omitempty"`
	// Agents are the online agents that are older than the master.
	Agents []Agent `json:"agents"`
}

// Progress is an update that was asked for and hasn't finished yet, or failed.
type Progress struct {
	Since time.Time `json:"since"`
	Error string    `json:"error,omitempty"`
}

// Agent is an agent that is older than the master.
type Agent struct {
	NodeID  string    `json:"nodeId"`
	Name    string    `json:"name"`
	Version string    `json:"version"`
	Update  *Progress `json:"update,omitempty"`
}

// Config tells whether and how often to look for new releases, and whether the agents follow
// an update of the master from the panel.
type Config interface {
	CheckUpdates() bool
	UpdateCheckInterval() time.Duration
	UpdateAgentsAfterMaster() bool
}

// Options locate what the service uses.
type Options struct {
	// DataDir is the master's; noryx-master-update.path watches the file update-request in it.
	DataDir string
	API     string // DefaultAPI if empty
	Page    string // DefaultPage if empty
	Unit    string // DefaultUnit if empty
	// Prereleases offers pre-releases too. It is an option of the master's host, never a
	// setting, as the installer that updates the master reads it there too.
	Prereleases bool
}

type Service struct {
	nodes  *node.Service
	config Config
	client *http.Client
	api    string
	page   string
	unit   string
	// prereleases offers pre-releases too, as the master's host says.
	prereleases bool
	// request makes systemd update the master, followUp makes the master update the
	// agents once it runs the new release.
	request, followUp string

	mu       sync.Mutex
	latest   *Release
	checked  time.Time
	checkErr string
	master   time.Time           // when the update of the master was asked for
	agents   map[string]Progress // by node ID
}

func New(nodes *node.Service, config Config, o Options) *Service {
	return &Service{
		nodes: nodes, config: config, api: cmp.Or(o.API, DefaultAPI), page: cmp.Or(o.Page, DefaultPage), unit: cmp.Or(o.Unit, DefaultUnit),
		prereleases: o.Prereleases,
		client: &http.Client{
			Timeout: 30 * time.Second,
			// The release page's redirect tells the version, so it isn't followed.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		request: filepath.Join(o.DataDir, "update-request"), followUp: filepath.Join(o.DataDir, "update-agents"),
		agents: map[string]Progress{},
	}
}

// Run updates the agents if the master was just updated from the panel and the settings let
// them follow, then looks for new releases as often as the settings say. Builds that aren't a
// release don't.
func (s *Service) Run(ctx context.Context) {
	if os.Remove(s.followUp) == nil && s.config.UpdateAgentsAfterMaster() {
		_ = s.UpdateAgents(ctx) // logged for each agent
	}
	if !buildinfo.IsRelease(buildinfo.Version) {
		return
	}
	for {
		if s.due() {
			s.Check(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wake):
		}
	}
}

// due tells whether the settings let the master look for new releases now: once their interval
// passed since the last check, also one by hand.
func (s *Service) due() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.config.CheckUpdates() && time.Since(s.checked) >= s.config.UpdateCheckInterval()
}

// Check looks for the latest release now. A failure shows in the status.
func (s *Service) Check(ctx context.Context) {
	latest, err := s.fetch(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checked = time.Now()
	if err != nil {
		s.checkErr = err.Error()
		slog.Warn("Check for updates failed", logging.System, "err", err)
		return
	}
	known := s.latest
	s.latest, s.checkErr = latest, ""
	if s.available() && (known == nil || known.Version != latest.Version) {
		slog.Info("Update available", logging.System, "version", latest.Version)
	}
}

// fetch returns the latest release, or nil if there is none yet; with pre-releases, the highest
// version among the newest releases, as the installer chooses it. While GitHub's API limits the
// requests of this IP address, it reads the version from the release page instead, which leaves
// pre-releases out.
func (s *Service) fetch(ctx context.Context) (*Release, error) {
	url := s.api + "/latest"
	if s.prereleases {
		url = s.api + "?per_page=" + strconv.Itoa(listed)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, nil
	case http.StatusForbidden, http.StatusTooManyRequests:
		return s.fromPage(ctx)
	default:
		return nil, fmt.Errorf("GitHub answered %s", res.Status)
	}
	dec, r := json.NewDecoder(io.LimitReader(res.Body, maxResponse)), &github{}
	if s.prereleases {
		var list []github
		if err := dec.Decode(&list); err != nil {
			return nil, fmt.Errorf("read the releases: %w", err)
		}
		if r = highest(list); r == nil {
			return nil, nil
		}
	} else if err := dec.Decode(r); err != nil {
		return nil, fmt.Errorf("read the latest release: %w", err)
	}
	return newRelease(r.Tag, r.Body, r.Published)
}

// github is a release as GitHub's API describes it.
type github struct {
	Tag       string    `json:"tag_name"`
	Body      string    `json:"body"`
	Published time.Time `json:"published_at"`
	Draft     bool      `json:"draft"`
}

// highest returns the published release with the highest version, or nil if list has none.
// It leaves out other tags than versions of releases, as the installer does.
func highest(list []github) *github {
	var top *github
	for i, r := range list {
		if !r.Draft && buildinfo.IsRelease(r.Tag) && (top == nil || semver.Compare(r.Tag, top.Tag) > 0) {
			top = &list[i]
		}
	}
	return top
}

// fromPage reads the version of the latest release from where the release page redirects.
func (s *Service) fromPage(ctx context.Context) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.page, nil)
	if err != nil {
		return nil, err
	}
	res, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	res.Body.Close()
	if res.StatusCode/100 != 3 {
		return nil, fmt.Errorf("GitHub answered %s", res.Status)
	}
	// Without a release, the page redirects to the list of releases.
	if tag, ok := strings.CutPrefix(res.Header.Get("Location"), buildinfo.Repository+"/releases/tag/"); ok {
		return newRelease(tag, "", time.Time{})
	}
	return nil, nil
}

func newRelease(tag, notes string, published time.Time) (*Release, error) {
	if !buildinfo.IsRelease(tag) {
		return nil, fmt.Errorf("the latest release has the unexpected version %q", tag)
	}
	return &Release{tag, notes, buildinfo.Repository + "/releases/tag/" + tag, published}, nil
}

// available tells whether the latest release is newer than the master. Callers hold mu.
func (s *Service) available() bool {
	return s.latest != nil && semver.Compare(s.latest.Version, buildinfo.Version) > 0
}

// updatable tells whether systemd installs releases when the master asks for it.
func (s *Service) updatable() bool {
	_, err := os.Stat(s.unit)
	return err == nil
}

// Status returns what administrators see about updates.
func (s *Service) Status(ctx context.Context) (Status, error) {
	agents, err := s.outdated(ctx)
	if err != nil {
		return Status{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{
		Version: buildinfo.Version, CheckError: s.checkErr, Updatable: s.updatable(), Agents: agents,
		Prereleases: s.prereleases, AgentsFollow: s.config.UpdateAgentsAfterMaster(),
	}
	if s.available() {
		st.Latest = s.latest
	}
	if checked := s.checked; !checked.IsZero() {
		st.CheckedAt = &checked
	}
	if !s.master.IsZero() {
		st.Master = &Progress{Since: s.master}
		if time.Since(s.master) > pending {
			st.Master.Error = "The update didn't finish. See: journalctl -u noryx-master-update"
			if _, err := os.Stat(s.request); err == nil {
				st.Master.Error = "The update didn't start. See: systemctl status noryx-master-update.path"
			}
		}
	}
	outdated := map[string]bool{}
	for i, a := range st.Agents {
		outdated[a.NodeID] = true
		if p, ok := s.agents[a.NodeID]; ok {
			if p.Error == "" && time.Since(p.Since) > pending {
				p.Error = "The update didn't finish. See on the node: journalctl -u noryx-agent-update"
			}
			st.Agents[i].Update = &p
		}
	}
	for id := range s.agents {
		if !outdated[id] { // updated, or offline
			delete(s.agents, id)
		}
	}
	return st, nil
}

// UpdateMaster asks systemd to install the latest release. The master restarts on it and
// then updates the agents, if the settings let them follow.
func (s *Service) UpdateMaster(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case !s.updatable():
		return httpapi.Errorf(http.StatusConflict, "This master wasn't installed from a package. Update it on its host with: "+
			"curl -fsSL %s/releases/latest/download/install.sh | sudo bash -s -- update", buildinfo.Repository)
	case !s.available():
		return httpapi.Errorf(http.StatusConflict, "There is no newer release.")
	}
	// The follow-up comes first: the master may restart as soon as the request exists.
	if err := os.WriteFile(s.followUp, nil, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(s.request, nil, 0o600); err != nil {
		return err
	}
	s.master = time.Now()
	logging.Note(ctx, slog.String("version", s.latest.Version))
	return nil
}

// UpdateAgents updates the online agents that are older than the master to its version,
// except those already updating.
func (s *Service) UpdateAgents(ctx context.Context) error {
	agents, err := s.outdated(ctx)
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	for _, a := range agents {
		if s.claim(a.NodeID) {
			wg.Go(func() { _ = s.updateAgent(ctx, a) })
		}
	}
	wg.Wait()
	return nil
}

// UpdateAgent updates the agent of one node to the master's version, e.g. to try a release on
// one node first or to update a node that was offline. The agent must be online and older than
// the master, and not updating already.
func (s *Service) UpdateAgent(ctx context.Context, nodeID string) error {
	n, err := s.nodes.Get(ctx, nodeID)
	if err != nil {
		return err
	}
	probe, cancel := context.WithTimeout(ctx, probeTimeout)
	info, _, err := s.nodes.Status(probe, n.ID)
	cancel()
	switch {
	case err != nil:
		return err
	case semver.Compare(info.GetAgentVersion(), buildinfo.Version) >= 0:
		return httpapi.Errorf(http.StatusConflict, "The agent of %s isn't older than the master.", n.Name)
	case !s.claim(n.ID):
		return httpapi.Errorf(http.StatusConflict, "The agent of %s is updating already.", n.Name)
	}
	return s.updateAgent(ctx, Agent{NodeID: n.ID, Name: n.Name, Version: info.GetAgentVersion()})
}

// claim marks the agent of a node as updating, unless it already is.
func (s *Service) claim(nodeID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.agents[nodeID]; ok && p.Error == "" && time.Since(p.Since) <= pending {
		return false
	}
	s.agents[nodeID] = Progress{Since: time.Now()}
	return true
}

// updateAgent asks a claimed agent to install the master's version. The agent installs only
// releases newer than itself, from its package, which checks their signature.
func (s *Service) updateAgent(ctx context.Context, a Agent) error {
	p := Progress{Since: time.Now()}
	conn, err := s.nodes.Conn(ctx, a.NodeID)
	if err == nil {
		_, err = noryxv1.NewNodeServiceClient(conn).Update(ctx, &noryxv1.UpdateRequest{Version: buildinfo.Version})
	}
	attrs := []any{logging.Nodes, logging.KeyNode, a.NodeID, logging.KeyNodeName, a.Name, "version", buildinfo.Version}
	if err != nil {
		p.Error = status.Convert(err).Message()
		slog.Warn("Update agent failed", append(attrs, "err", err)...)
	} else {
		slog.Info("Update agent", attrs...)
	}
	s.mu.Lock()
	s.agents[a.NodeID] = p
	s.mu.Unlock()
	return err
}

// outdated returns the online agents that are older than the master.
func (s *Service) outdated(ctx context.Context) ([]Agent, error) {
	nodes, err := s.nodes.List(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	found := make([]*Agent, len(nodes))
	var wg sync.WaitGroup
	for i, n := range nodes {
		if n.EnrolledAt == nil {
			continue
		}
		wg.Go(func() {
			info, _, err := s.nodes.Status(ctx, n.ID)
			if err == nil && semver.Compare(info.GetAgentVersion(), buildinfo.Version) < 0 {
				found[i] = &Agent{NodeID: n.ID, Name: n.Name, Version: info.GetAgentVersion()}
			}
		})
	}
	wg.Wait()
	agents := []Agent{}
	for _, a := range found {
		if a != nil {
			agents = append(agents, *a)
		}
	}
	return agents, nil
}
