package logs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/database"
)

const (
	queueSize  = 4096
	batchSize  = 500
	maxEntries = 1_000_000 // kept at most, however long the retention and large the limit
	// pruneEvery is short, so that agents can't add much beyond the size limit in between.
	pruneEvery = time.Minute
	// Pruning deletes pruneStep entries at a time and pauses in between, so that other writers
	// needn't wait long for the database when it deletes many, e.g. after lowering the limit.
	pruneStep   = 10_000
	prunePause  = 50 * time.Millisecond
	reportEvery = time.Minute // problems of the store itself are logged at most this often
)

const (
	fields  = `time, level, source, category, message, username, node_id, node_name, server_id, server_name, attrs`
	columns = `id, ` + fields
)

// Config tells how much of the log to keep.
type Config interface {
	// LogRetention is how long entries are kept.
	LogRetention() time.Duration
	// LogMaxSize is how many bytes the entries may take in the database.
	LogMaxSize() int64
}

// Store keeps the log in the database. The master's entries are written in the background,
// so logging never waits for the database; if it falls behind, entries are dropped and
// counted. Every minute, entries older than the retention are deleted, and the oldest ones
// beyond the limits.
type Store struct {
	db    *sql.DB
	names *Names
	conf  Config

	queue   chan Entry
	dropped atomic.Int64
	stop    context.CancelFunc
	done    chan struct{}

	mu       sync.Mutex
	changed  chan struct{} // closed when entries were added
	reported time.Time

	// cut tells that pruning deleted entries younger than the retention and warned about it,
	// since entries last lasted the whole retention. Only prune uses it.
	cut bool
}

// NewStore returns the store of the log; conf may be nil for a store that is only read.
func NewStore(db *sql.DB, names *Names, conf Config) *Store {
	return &Store{db: db, names: names, conf: conf, queue: make(chan Entry, queueSize), changed: make(chan struct{})}
}

// Handler returns a slog handler that stores the master's records of at least level.
func (s *Store) Handler(level slog.Leveler) slog.Handler {
	return logging.NewHandler(level, func(e logging.Entry) {
		select {
		case s.queue <- fromLogging(FromMaster, e):
		default:
			s.dropped.Add(1)
		}
	})
}

// Start writes the master's entries and deletes expired ones until Close.
func (s *Store) Start(ctx context.Context) {
	ctx, s.stop = context.WithCancel(ctx)
	s.done = make(chan struct{})
	go s.run(ctx)
}

// Close stops writing in the background and writes the entries that are still queued.
func (s *Store) Close() {
	s.stop()
	<-s.done
	for batch := s.take(nil); len(batch) > 0; batch = s.take(nil) {
		_ = s.write(context.Background(), batch, nil)
	}
}

func (s *Store) run(ctx context.Context) {
	defer close(s.done)
	ticker := time.NewTicker(pruneEvery)
	defer ticker.Stop()
	prune := func() {
		if err := s.prune(ctx); err != nil && ctx.Err() == nil {
			s.report("Can't delete old log entries", "err", err)
		}
	}
	prune()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			prune()
		case e := <-s.queue:
			_ = s.write(ctx, s.take([]Entry{e}), nil)
		}
	}
}

// take adds queued entries to batch, up to batchSize.
func (s *Store) take(batch []Entry) []Entry {
	for len(batch) < batchSize {
		select {
		case e := <-s.queue:
			batch = append(batch, e)
		default:
			return batch
		}
	}
	return batch
}

// cursor is how far the log of a node's agent has been read.
type cursor struct {
	node, boot string
	seq        uint64
}

// write stores entries with the names of their nodes and servers, together with the
// cursor of the agent they come from, if any.
func (s *Store) write(ctx context.Context, batch []Entry, c *cursor) error {
	ctx = context.WithoutCancel(ctx) // entries being written when the master stops are kept
	for i := range batch {
		e := &batch[i]
		e.clamp()
		if e.NodeID != "" && e.NodeName == "" {
			e.NodeName = s.names.Node(ctx, e.NodeID)
		}
		if e.NodeID != "" && e.ServerID != "" && e.ServerName == "" {
			e.ServerName = s.names.Server(ctx, e.NodeID, e.ServerID)
		}
	}
	err := s.insert(ctx, batch, c)
	if err != nil {
		s.report("Can't store log entries", "entries", len(batch), "err", err)
		return err
	}
	s.mu.Lock()
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
	if s.dropped.Load() > 0 {
		s.report("Dropped log entries because the database is too slow", "entries", s.dropped.Swap(0))
	}
	return nil
}

func (s *Store) insert(ctx context.Context, batch []Entry, c *cursor) error {
	return database.InTx(ctx, s.db, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO log_entries (`+fields+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for i, e := range batch {
			attrs, _ := json.Marshal(e.Attrs) // can't fail for strings
			res, err := stmt.ExecContext(ctx, e.Time.UnixMilli(), int(e.Level), e.Source, e.Category, e.Message, e.User,
				e.NodeID, e.NodeName, e.ServerID, e.ServerName, string(attrs))
			if err != nil {
				return err
			}
			batch[i].ID, _ = res.LastInsertId()
		}
		if c == nil {
			return nil
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO log_cursors (node_id, boot, seq) VALUES (?, ?, ?)
			ON CONFLICT (node_id) DO UPDATE SET boot = excluded.boot, seq = excluded.seq`, c.node, c.boot, c.seq)
		return err
	})
}

// report logs a problem of the store, at most once every reportEvery.
func (s *Store) report(msg string, args ...any) {
	s.mu.Lock()
	due := time.Since(s.reported) >= reportEvery
	if due {
		s.reported = time.Now()
	}
	s.mu.Unlock()
	if due {
		slog.Error(msg, args...)
	}
}

// prune deletes the entries older than the retention, then the oldest beyond maxEntries or the
// size limit. It warns when it deletes entries before their retention ends, but only once until
// entries lasted the whole retention again, so that a log that stays full doesn't warn every minute.
func (s *Store) prune(ctx context.Context) error {
	aged, err := s.remove(ctx, `time < ?`, time.Now().Add(-s.conf.LogRetention()).UnixMilli())
	if err != nil {
		return err
	}
	if aged > 0 {
		s.cut = false
	}
	var entries, size int64
	if err := s.db.QueryRowContext(ctx, `SELECT entries, bytes FROM log_size`).Scan(&entries, &size); err != nil {
		return err
	}
	maxSize := s.conf.LogMaxSize()
	if entries <= maxEntries && size <= maxSize {
		return nil
	}
	// The oldest entries up to the first with which enough are deleted for both limits; the
	// window reads no further than that.
	var last int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM (
		SELECT id, COUNT(*) OVER w AS entries, SUM(size) OVER w AS bytes FROM log_entries WINDOW w AS (ORDER BY id)
	) WHERE entries >= ? AND bytes >= ? LIMIT 1`, entries-maxEntries, size-maxSize).Scan(&last); err != nil {
		return err
	}
	cut, err := s.remove(ctx, `id <= ?`, last)
	if err != nil || cut == 0 || s.cut {
		return err
	}
	s.cut = true
	limit := fmt.Sprintf("%d MiB", maxSize>>20)
	if size <= maxSize {
		limit = fmt.Sprintf("%d entries", maxEntries)
	}
	var oldest int64
	_ = s.db.QueryRowContext(ctx, `SELECT COALESCE(MIN(time), 0) FROM log_entries`).Scan(&oldest)
	slog.Warn("The log reached its limit, so entries are deleted before their retention ends", logging.Settings,
		"limit", limit, "deleted", cut, "oldest", time.UnixMilli(oldest).UTC().Format(time.RFC3339))
	return nil
}

// remove deletes the entries that match cond, pruneStep at a time, and returns their number.
func (s *Store) remove(ctx context.Context, cond string, args ...any) (int64, error) {
	var removed int64
	for {
		//nolint:gosec // cond has placeholders for all values
		res, err := s.db.ExecContext(ctx, `DELETE FROM log_entries WHERE id IN (SELECT id FROM log_entries WHERE `+cond+` LIMIT ?)`,
			append(args, pruneStep)...)
		if err != nil {
			return removed, err
		}
		n, _ := res.RowsAffected()
		if removed += n; n < pruneStep {
			return removed, nil
		}
		select {
		case <-ctx.Done():
			return removed, ctx.Err()
		case <-time.After(prunePause):
		}
	}
}

// last returns the ID of the newest entry.
func (s *Store) last(ctx context.Context) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM log_entries`).Scan(&id)
	return id, err
}

// Changed returns a channel that is closed when entries are added.
func (s *Store) Changed() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.changed
}

// Each passes up to limit entries that match the filter and that the grants of ctx allow to
// see to fn: the newest first, or the oldest if oldest is set.
func (s *Store) Each(ctx context.Context, f Filter, oldest bool, limit int, fn func(Entry) error) error {
	where, args := f.where(access.From(ctx))
	order := "DESC"
	if oldest {
		order = "ASC"
	}
	//nolint:gosec // where has placeholders for all values
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM log_entries `+where+` ORDER BY id `+order+` LIMIT ?`, append(args, limit)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			e         Entry
			at, level int64
			attrs     string
		)
		if err := rows.Scan(&e.ID, &at, &level, &e.Source, &e.Category, &e.Message, &e.User,
			&e.NodeID, &e.NodeName, &e.ServerID, &e.ServerName, &attrs); err != nil {
			return err
		}
		e.Time, e.Level = time.UnixMilli(at), slog.Level(level)
		if err := json.Unmarshal([]byte(attrs), &e.Attrs); err != nil {
			return err
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	return rows.Err()
}

// List returns entries like Each.
func (s *Store) List(ctx context.Context, f Filter, oldest bool, limit int) ([]Entry, error) {
	entries := []Entry{}
	err := s.Each(ctx, f, oldest, limit, func(e Entry) error {
		entries = append(entries, e)
		return nil
	})
	return entries, err
}

// Bucket counts the entries of an hour by level.
type Bucket struct {
	Start time.Time `json:"start"`
	Debug int       `json:"debug"`
	Info  int       `json:"info"`
	Warn  int       `json:"warn"`
	Error int       `json:"error"`
}

// Stats counts the entries that match the filter and the grants of ctx in each of the last
// hours, the current one included.
func (s *Store) Stats(ctx context.Context, f Filter, hours int) ([]Bucket, error) {
	start := time.Now().Truncate(time.Hour).Add(-time.Duration(hours-1) * time.Hour)
	buckets := make([]Bucket, hours)
	for i := range buckets {
		buckets[i].Start = start.Add(time.Duration(i) * time.Hour)
	}
	f.Since, f.Before, f.After = start, 0, 0
	where, args := f.where(access.From(ctx))
	//nolint:gosec // where has placeholders for all values
	rows, err := s.db.QueryContext(ctx, `SELECT time / 3600000, level, COUNT(*) FROM log_entries `+where+` GROUP BY 1, 2`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var hour, level int64
		var n int
		if err := rows.Scan(&hour, &level, &n); err != nil {
			return nil, err
		}
		i := int(time.UnixMilli(hour*3_600_000).Sub(start) / time.Hour)
		if i < 0 || i >= hours {
			continue
		}
		b := &buckets[i]
		switch logging.Normalize(slog.Level(level)) {
		case slog.LevelDebug:
			b.Debug += n
		case slog.LevelInfo:
			b.Info += n
		case slog.LevelWarn:
			b.Warn += n
		default:
			b.Error += n
		}
	}
	return buckets, rows.Err()
}

// position returns how far the log of a node's agent has been read.
func (s *Store) position(ctx context.Context, nodeID string) (boot string, seq uint64, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT boot, seq FROM log_cursors WHERE node_id = ?`, nodeID).Scan(&boot, &seq)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return boot, seq, err
}
