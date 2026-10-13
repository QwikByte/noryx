package player

import (
	"cmp"
	"context"
	"database/sql"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/network"
)

const (
	daySeconds = 24 * 60 * 60
	// maxNames is the most players of a server recorded per measurement, as many as agents name.
	maxNames = 1000
	// maxPerDay is the most players and servers of a node recorded on a day, far more than its
	// servers see, so that a compromised agent that makes up names can't fill the database.
	maxPerDay = 10_000
	// pruneInterval is how often sightings older than the retention are deleted.
	pruneInterval = time.Hour
	// MaxSeen is the most players a list of players seen returns.
	MaxSeen = 500
)

// Config provides the settings of the master that concern sightings. They can change at any time.
type Config interface {
	// SeenPlayers tells whether sightings are recorded.
	SeenPlayers() bool
	// SeenRetention is how long they are kept, also while none are recorded.
	SeenRetention() time.Duration
}

// Sightings keeps where players were online and for how long, from the names of the players of
// game servers in the measurements of the agents: per player, server and day, without IP
// addresses or anything else about them.
type Sightings struct {
	db   *sql.DB
	conf Config

	mu     sync.Mutex
	pruned time.Time
	full   map[string]int64 // the day on which a node had maxPerDay, which was logged
}

func NewSightings(db *sql.DB, conf Config) *Sightings {
	return &Sightings{db: db, conf: conf, full: map[string]int64{}}
}

// Record notes the players of the game servers of a node that were online at a measurement,
// which the agent made a minute after the one before, unless the settings turned that off.
// Invalid and repeated names are left out.
func (s *Sightings) Record(ctx context.Context, nodeID string, at time.Time, servers []*noryxv1.ServerStats) error {
	s.prune(ctx, at)
	if !s.conf.SeenPlayers() {
		return nil
	}
	today := at.Unix() / daySeconds
	return database.InTx(ctx, s.db, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM player_sightings WHERE node_id = ? AND day = ?`, nodeID, today).Scan(&count); err != nil {
			return err
		}
		update, err := tx.PrepareContext(ctx, `
			UPDATE player_sightings SET last_seen = ?1, minutes = minutes + 1
			WHERE name = ?2 AND node_id = ?3 AND server_id = ?4 AND day = ?5`)
		if err != nil {
			return err
		}
		defer update.Close()
		insert, err := tx.PrepareContext(ctx, `
			INSERT INTO player_sightings (name, node_id, server_id, day, first_seen, last_seen, minutes) VALUES (?, ?, ?, ?, ?, ?, 1)`)
		if err != nil {
			return err
		}
		defer insert.Close()
		full := false
		for _, srv := range servers {
			if srv.GetProxy() {
				continue // its players are those of the game servers of its network
			}
			for _, name := range names(srv.GetPlayers().GetNames()) {
				res, err := update.ExecContext(ctx, at.Unix(), name, nodeID, srv.GetId(), today)
				if err != nil {
					return err
				}
				switch n, _ := res.RowsAffected(); {
				case n > 0:
				case count >= maxPerDay:
					full = true
				default:
					if _, err := insert.ExecContext(ctx, name, nodeID, srv.GetId(), today, at.Unix(), at.Unix()); err != nil {
						return err
					}
					count++
				}
			}
		}
		if full {
			s.warnFull(nodeID, today)
		}
		return nil
	})
}

// names returns the valid names of players of a server, each once, and at most maxNames.
func names(all []string) []string {
	var valid []string
	seen := map[string]bool{}
	for _, name := range all {
		if key := strings.ToLower(name); noryxv1.ValidPlayerName(name) && !seen[key] && len(valid) < maxNames {
			seen[key] = true
			valid = append(valid, name)
		}
	}
	return valid
}

// warnFull logs once a day that a node named more players than are recorded.
func (s *Sightings) warnFull(nodeID string, today int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.full[nodeID] != today {
		s.full[nodeID] = today
		slog.Warn("A node named more players today than are recorded, so the others are left out", logging.Players,
			logging.KeyNode, nodeID, "limit", maxPerDay)
	}
}

// prune deletes the sightings of the days that ended before the retention began, every
// pruneInterval.
func (s *Sightings) prune(ctx context.Context, now time.Time) {
	s.mu.Lock()
	due := now.Sub(s.pruned) >= pruneInterval
	if due {
		s.pruned = now
	}
	s.mu.Unlock()
	if !due {
		return
	}
	first := now.Add(-s.conf.SeenRetention()).Unix() / daySeconds
	if _, err := s.db.ExecContext(ctx, `DELETE FROM player_sightings WHERE day < ?`, first); err != nil {
		slog.Warn("Can't delete old sightings of players", logging.Players, "err", err)
	}
}

// DeleteAll deletes all sightings, e.g. once they are no longer recorded.
func (s *Sightings) DeleteAll(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM player_sightings`)
	return err
}

// Forget deletes the sightings of a deleted server.
func (s *Sightings) Forget(ctx context.Context, nodeID, serverID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM player_sightings WHERE node_id = ? AND server_id = ?`, nodeID, serverID)
	return err
}

// Move keeps the sightings of a server that moved to another node.
func (s *Sightings) Move(ctx context.Context, serverID, from, to string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE OR REPLACE player_sightings SET node_id = ? WHERE node_id = ? AND server_id = ?`, to, from, serverID)
	return err
}

// Span is when a player was first and last seen, and for how many minutes.
type Span struct {
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
	Minutes   int64     `json:"minutes"`
}

func (s *Span) add(o Span) {
	if s.Minutes == 0 || o.FirstSeen.Before(s.FirstSeen) {
		s.FirstSeen = o.FirstSeen
	}
	if o.LastSeen.After(s.LastSeen) {
		s.LastSeen = o.LastSeen
	}
	s.Minutes += o.Minutes
}

// SeenOn is a server a player was online on.
type SeenOn struct {
	network.Ref
	Span
}

// Player is a player seen on servers, with those seen last first.
type Player struct {
	Name string `json:"name"`
	Span
	Servers []SeenOn `json:"servers"`
}

// include counts sightings on a server into the player's, whose name is spelled as when the
// player was seen last.
func (p *Player) include(name string, on SeenOn) {
	if p.Minutes == 0 || on.LastSeen.After(p.LastSeen) {
		p.Name = name
	}
	p.add(on.Span)
	if i := slices.IndexFunc(p.Servers, func(s SeenOn) bool { return s.Ref == on.Ref }); i >= 0 {
		p.Servers[i].add(on.Span)
	} else {
		p.Servers = append(p.Servers, on)
	}
}

func (p *Player) sort() {
	slices.SortFunc(p.Servers, func(a, b SeenOn) int { return b.LastSeen.Compare(a.LastSeen) })
}

// SeenPlayers returns the players seen on the servers that visible lets through whose name
// contains query, those seen last first and at most limit, and how many there are.
func (s *Sightings) SeenPlayers(ctx context.Context, query string, limit int, visible func(network.Ref) bool) ([]Player, int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT name, node_id, server_id, MIN(first_seen), MAX(last_seen), SUM(minutes) FROM player_sightings
		WHERE name LIKE ? ESCAPE '\' GROUP BY name, node_id, server_id`, "%"+likeEscaper.Replace(query)+"%")
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var players []Player
	index := map[string]int{}
	for rows.Next() {
		var name string
		on, err := scanSeenOn(rows, &name)
		if err != nil {
			return nil, 0, err
		}
		if !visible(on.Ref) {
			continue
		}
		key := strings.ToLower(name)
		i, ok := index[key]
		if !ok {
			i, index[key] = len(players), len(players)
			players = append(players, Player{})
		}
		players[i].include(name, on)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	slices.SortFunc(players, func(a, b Player) int {
		return cmp.Or(b.LastSeen.Compare(a.LastSeen), cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)))
	})
	total := len(players)
	players = players[:min(total, limit)]
	for i := range players {
		players[i].sort()
	}
	return append([]Player{}, players...), total, nil
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// Day is how long a player was online on a day, of the servers a user may see.
type Day struct {
	Day     string `json:"day"` // as 2006-01-02, in UTC
	Minutes int64  `json:"minutes"`
}

// History is where and when a player was online, with the minutes of each day.
type History struct {
	Player
	Days []Day `json:"days"`
}

// History returns where and when a player was online on the servers that visible lets
// through. Of a player who wasn't seen there, it has the name as given and no servers.
func (s *Sightings) History(ctx context.Context, name string, visible func(network.Ref) bool) (History, error) {
	h := History{Player: Player{Name: name, Servers: []SeenOn{}}, Days: []Day{}}
	rows, err := s.db.QueryContext(ctx, `
		SELECT name, node_id, server_id, first_seen, last_seen, minutes, day FROM player_sightings
		WHERE name = ? ORDER BY day`, name)
	if err != nil {
		return h, err
	}
	defer rows.Close()
	for rows.Next() {
		var spelled string
		var d int64
		on, err := scanSeenOn(rows, &spelled, &d)
		if err != nil {
			return h, err
		}
		if !visible(on.Ref) {
			continue
		}
		h.include(spelled, on)
		date := time.Unix(d*daySeconds, 0).UTC().Format(time.DateOnly)
		if n := len(h.Days); n > 0 && h.Days[n-1].Day == date {
			h.Days[n-1].Minutes += on.Minutes
		} else {
			h.Days = append(h.Days, Day{date, on.Minutes})
		}
	}
	h.sort()
	return h, rows.Err()
}

// scanSeenOn scans the name, node, server, first and last time seen, the minutes, and extra
// columns of a row of sightings.
func scanSeenOn(rows *sql.Rows, name *string, extra ...any) (SeenOn, error) {
	var on SeenOn
	var first, last int64
	err := rows.Scan(append([]any{name, &on.NodeID, &on.ServerID, &first, &last, &on.Minutes}, extra...)...)
	on.FirstSeen, on.LastSeen = time.Unix(first, 0), time.Unix(last, 0)
	return on, err
}
