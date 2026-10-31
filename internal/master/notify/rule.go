package notify

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/logs"
	"github.com/QwikByte/noryx/internal/master/schedule"
	"github.com/QwikByte/noryx/internal/master/tag"
)

const (
	maxRules      = 200
	maxCategories = 32
)

var (
	errRuleNotFound = httpapi.Errorf(http.StatusNotFound, "Notification rule not found.")
	categoryPattern = regexp.MustCompile(`^[a-z][a-z-]{0,31}$`)
	idPattern       = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
)

// Rule sends the new entries of the log of at least a level to a channel: those of its
// categories, or all if it has none, and of its node and server, or of the servers with its
// tag or of its network, if it names one of them.
type Rule struct {
	ID        string `json:"id"`
	ChannelID string `json:"channelId"`
	Enabled   bool   `json:"enabled"`
	// Level is info, warn or error.
	Level      string   `json:"level"`
	Categories []string `json:"categories"`
	NodeID     string   `json:"nodeId"`
	ServerID   string   `json:"serverId"`
	// Tag and NetworkID choose the servers that have the tag, or the proxy and game servers of
	// the network, when an entry comes.
	Tag       string    `json:"tag"`
	NetworkID string    `json:"networkId"`
	CreatedAt time.Time `json:"createdAt"`

	level slog.Level
}

// RuleInput is a new or changed rule.
type RuleInput struct {
	ChannelID  string   `json:"channelId"`
	Enabled    bool     `json:"enabled"`
	Level      string   `json:"level"`
	Categories []string `json:"categories"`
	NodeID     string   `json:"nodeId"`
	ServerID   string   `json:"serverId"`
	Tag        string   `json:"tag"`
	NetworkID  string   `json:"networkId"`
}

func (in RuleInput) build() (Rule, error) {
	r := Rule{
		ChannelID: in.ChannelID, Enabled: in.Enabled, Level: in.Level, NodeID: in.NodeID, ServerID: in.ServerID, NetworkID: in.NetworkID,
		Categories: []string{},
	}
	var err error
	if r.level, err = logging.ParseLevel(r.Level); err != nil || r.level < slog.LevelInfo {
		return r, httpapi.Errorf(http.StatusBadRequest, "Choose the level info, warn or error.")
	}
	if in.Tag != "" {
		tags, err := tag.Normalize([]string{in.Tag})
		if err != nil {
			return r, err
		}
		r.Tag = tags[0]
	}
	for _, c := range in.Categories {
		if !categoryPattern.MatchString(c) {
			return r, httpapi.Errorf(http.StatusBadRequest, "%q isn't a category of the log.", c)
		}
		if !slices.Contains(r.Categories, c) {
			r.Categories = append(r.Categories, c)
		}
	}
	switch {
	case len(r.Categories) > maxCategories:
		return r, httpapi.Errorf(http.StatusBadRequest, "Choose at most %d categories.", maxCategories)
	case r.NodeID != "" && !idPattern.MatchString(r.NodeID), r.ServerID != "" && !idPattern.MatchString(r.ServerID),
		r.NetworkID != "" && !idPattern.MatchString(r.NetworkID):
		return r, httpapi.Errorf(http.StatusBadRequest, "The node, server or network is invalid.")
	case r.ServerID != "" && r.NodeID == "":
		return r, httpapi.Errorf(http.StatusBadRequest, "Choose the node of the server.")
	case r.NodeID != "" && (r.Tag != "" || r.NetworkID != "") || r.Tag != "" && r.NetworkID != "":
		return r, httpapi.Errorf(http.StatusBadRequest, "Choose a node or server, a tag or a network, only one of them.")
	}
	return r, nil
}

// group is the tag or the network of a rule as a target, if it names one.
func (r Rule) group() (schedule.Target, bool) {
	switch {
	case r.Tag != "":
		return schedule.Target{Kind: schedule.KindTag, Value: r.Tag}, true
	case r.NetworkID != "":
		return schedule.Target{Kind: schedule.KindNetwork, Value: r.NetworkID}, true
	}
	return schedule.Target{}, false
}

// matches reports whether the rule sends an entry; groups tell which servers have the tags and
// are in the networks of the rules.
func (r Rule) matches(e logs.Entry, groups map[schedule.Target]func(nodeID, serverID string) bool) bool {
	if !r.Enabled || e.Level < r.level || len(r.Categories) > 0 && !slices.Contains(r.Categories, e.Category) ||
		r.NodeID != "" && r.NodeID != e.NodeID || r.ServerID != "" && r.ServerID != e.ServerID {
		return false
	}
	g, ok := r.group()
	return !ok || e.ServerID != "" && groups[g] != nil && groups[g](e.NodeID, e.ServerID)
}

const ruleColumns = `id, channel_id, enabled, level, categories, node_id, server_id, tag, network_id, created_at`

func scanRule(row database.Scanner) (Rule, error) {
	var (
		r          Rule
		categories string
		at         int64
	)
	if err := row.Scan(&r.ID, &r.ChannelID, &r.Enabled, &r.Level, &categories, &r.NodeID, &r.ServerID, &r.Tag, &r.NetworkID, &at); err != nil {
		return r, err
	}
	r.CreatedAt = time.Unix(at, 0)
	r.level, _ = logging.ParseLevel(r.Level)
	return r, json.Unmarshal([]byte(categories), &r.Categories)
}

// Rules returns the rules, ordered by channel.
func (s *Service) Rules(ctx context.Context) ([]Rule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+ruleColumns+` FROM notification_rules ORDER BY channel_id, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rules := []Rule{}
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		rules = append(rules, r)
	}
	return rules, rows.Err()
}

func (s *Service) rule(ctx context.Context, id string) (Rule, error) {
	r, err := scanRule(s.db.QueryRowContext(ctx, `SELECT `+ruleColumns+` FROM notification_rules WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		err = errRuleNotFound
	}
	return r, err
}

// CreateRule stores a new rule.
func (s *Service) CreateRule(ctx context.Context, in RuleInput) (Rule, error) {
	r, err := in.build()
	if err != nil {
		return r, err
	}
	s.changes.Lock()
	defer s.changes.Unlock()
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notification_rules`).Scan(&n); err != nil {
		return r, err
	}
	if n >= maxRules {
		return r, httpapi.Errorf(http.StatusConflict, "The master keeps at most %d notification rules.", maxRules)
	}
	if err := s.exist(ctx, r); err != nil {
		return r, err
	}
	r.ID, r.CreatedAt = strings.ToLower(rand.Text()), time.Now()
	categories, _ := json.Marshal(r.Categories) // can't fail for strings
	if _, err := s.db.ExecContext(ctx, `INSERT INTO notification_rules (`+ruleColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.ChannelID, r.Enabled, r.Level, string(categories), r.NodeID, r.ServerID, r.Tag, r.NetworkID, r.CreatedAt.Unix()); err != nil {
		return r, err
	}
	if err := s.reload(ctx); err != nil {
		return r, err
	}
	return s.rule(ctx, r.ID)
}

// UpdateRule changes a rule.
func (s *Service) UpdateRule(ctx context.Context, id string, in RuleInput) (Rule, error) {
	r, err := in.build()
	if err != nil {
		return r, err
	}
	s.changes.Lock()
	defer s.changes.Unlock()
	if err := s.exist(ctx, r); err != nil {
		return r, err
	}
	categories, _ := json.Marshal(r.Categories) // can't fail for strings
	res, err := s.db.ExecContext(ctx, `UPDATE notification_rules SET channel_id = ?, enabled = ?, level = ?, categories = ?, node_id = ?, server_id = ?,
		tag = ?, network_id = ? WHERE id = ?`, r.ChannelID, r.Enabled, r.Level, string(categories), r.NodeID, r.ServerID, r.Tag, r.NetworkID, id)
	if err != nil {
		return r, err
	}
	if database.RowsAffected(res) == 0 {
		return r, errRuleNotFound
	}
	if err := s.reload(ctx); err != nil {
		return r, err
	}
	return s.rule(ctx, id)
}

// exist checks that the channel and the network of a rule exist.
func (s *Service) exist(ctx context.Context, r Rule) error {
	if _, err := s.channel(ctx, r.ChannelID); err != nil || r.NetworkID == "" {
		return err
	}
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM networks WHERE id = ?`, r.NetworkID).Scan(&n)
	if err == nil && n == 0 {
		err = httpapi.Errorf(http.StatusBadRequest, "Choose a network that exists.")
	}
	return err
}

// DeleteRule deletes a rule.
func (s *Service) DeleteRule(ctx context.Context, id string) error {
	s.changes.Lock()
	defer s.changes.Unlock()
	res, err := s.db.ExecContext(ctx, `DELETE FROM notification_rules WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if database.RowsAffected(res) == 0 {
		return errRuleNotFound
	}
	return s.reload(ctx)
}

// Forget deletes the rules of a deleted server, which would send nothing anymore; a rule without
// its server would send the entries of the whole node instead.
func (s *Service) Forget(ctx context.Context, nodeID, serverID string) error {
	return s.prune(ctx, `DELETE FROM notification_rules WHERE node_id = ? AND server_id = ?`, nodeID, serverID)
}

// Move keeps the rules of a server that moved to another node at its new place.
func (s *Service) Move(ctx context.Context, serverID, from, to string) error {
	s.changes.Lock()
	defer s.changes.Unlock()
	res, err := s.db.ExecContext(ctx, `UPDATE notification_rules SET node_id = ? WHERE node_id = ? AND server_id = ?`, to, from, serverID)
	if err != nil {
		return err
	}
	if database.RowsAffected(res) == 0 {
		return nil
	}
	return s.reload(ctx)
}

// Prune deletes the rules of nodes that were removed, with their servers, and of networks that
// were deleted. Rules of tags stay, also while no server has the tag.
func (s *Service) Prune(ctx context.Context) error {
	return s.prune(ctx, `DELETE FROM notification_rules WHERE node_id != '' AND node_id NOT IN (SELECT id FROM nodes)
		OR network_id != '' AND network_id NOT IN (SELECT id FROM networks)`)
}

// prune deletes the rules that a statement chooses, and logs how many.
func (s *Service) prune(ctx context.Context, query string, args ...any) error {
	s.changes.Lock()
	defer s.changes.Unlock()
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	n := database.RowsAffected(res)
	if n == 0 {
		return nil
	}
	slog.Info("Deleted notification rules of servers, nodes or networks that are gone", logging.Notifications, "rules", n)
	return s.reload(ctx)
}
