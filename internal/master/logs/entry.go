// Package logs is the log of the master and its agents: it stores what the master logs,
// including every request of the panel that changes something, collects the logs of the
// agents, and shows them in the panel, the terminal and the command line. Users only see
// the entries about the nodes and servers of their scope.
package logs

import (
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/httpapi"
)

// Sources of entries.
const (
	FromMaster = "master"
	FromAgent  = "agent"
)

// Limits of entries, which keep agents from filling the database with large ones.
const (
	maxMessage = 2000
	maxName    = 128
	maxAttrs   = 32
	maxValue   = 2000
	// maxAttrsSize limits the names and values of all attributes together.
	maxAttrsSize = 8000
)

var categoryPattern = regexp.MustCompile(`^[a-z][a-z-]{0,31}$`)

// Entry is an entry of the log.
type Entry struct {
	ID         int64             `json:"id"`
	Time       time.Time         `json:"time"`
	Level      slog.Level        `json:"-"`
	Source     string            `json:"source"`
	Category   string            `json:"category"`
	Message    string            `json:"message"`
	User       string            `json:"user,omitempty"`
	NodeID     string            `json:"nodeId,omitempty"`
	NodeName   string            `json:"nodeName,omitempty"`
	ServerID   string            `json:"serverId,omitempty"`
	ServerName string            `json:"serverName,omitempty"`
	Attrs      map[string]string `json:"attrs"`
}

// MarshalJSON adds the name of the level, e.g. "warn".
func (e Entry) MarshalJSON() ([]byte, error) {
	type entry Entry
	return json.Marshal(struct {
		entry
		Level string `json:"level"`
	}{entry(e), logging.LevelName(e.Level)})
}

func fromLogging(source string, e logging.Entry) Entry {
	return Entry{
		Time: e.Time, Level: e.Level, Source: source, Category: e.Category, Message: e.Message, User: e.User,
		NodeID: e.Node, NodeName: e.NodeName, ServerID: e.Server, ServerName: e.ServerName, Attrs: e.Attrs,
	}
}

// clamp cuts an entry down to the limits and fixes what can't be stored as it is.
func (e *Entry) clamp() {
	e.Level = logging.Normalize(e.Level)
	if !categoryPattern.MatchString(e.Category) {
		e.Category = logging.System.Value.String()
	}
	e.Message = database.Clip(e.Message, maxMessage)
	for _, s := range []*string{&e.User, &e.NodeID, &e.NodeName, &e.ServerID, &e.ServerName} {
		*s = database.Clip(*s, maxName)
	}
	attrs, left := map[string]string{}, maxAttrsSize
	for _, k := range slices.Sorted(maps.Keys(e.Attrs))[:min(len(e.Attrs), maxAttrs)] {
		name := database.Clip(k, maxName)
		if left -= len(name); left <= 0 {
			break
		}
		v := database.Clip(e.Attrs[k], min(maxValue, left))
		attrs[name] = v
		left -= len(v)
	}
	e.Attrs = attrs
}

// size is about the space an entry takes in the database, with its attributes encoded.
func (e *Entry) size() int {
	attrs, _ := json.Marshal(e.Attrs) // can't fail for strings
	return len(e.Message) + len(attrs)
}

// Filter selects entries; empty fields select all.
type Filter struct {
	// Level is the least important level, e.g. "warn".
	Level    string
	Source   string
	Category string
	User     string
	NodeID   string
	ServerID string
	// Search finds text in the message, the user, the names and the attributes.
	Search       string
	Since, Until time.Time
	// Before and After select entries with smaller or greater IDs, to page and to follow.
	Before, After int64
}

// FilterFrom reads a filter from the query of a request.
func FilterFrom(r *http.Request) (Filter, error) {
	q := r.URL.Query()
	f := Filter{
		Level: q.Get("level"), Source: q.Get("source"), Category: q.Get("category"), User: q.Get("user"),
		NodeID: q.Get("node"), ServerID: q.Get("server"), Search: strings.TrimSpace(q.Get("search")),
	}
	var err error
	if f.Level != "" {
		_, err = logging.ParseLevel(f.Level)
	}
	if v := q.Get("since"); v != "" && err == nil {
		f.Since, err = time.Parse(time.RFC3339, v)
	}
	if v := q.Get("until"); v != "" && err == nil {
		f.Until, err = time.Parse(time.RFC3339, v)
	}
	if err != nil {
		return f, httpapi.Errorf(http.StatusBadRequest, "The filter is invalid: %v.", err)
	}
	return f, nil
}

// where returns the condition for the entries that match the filter and that the grants
// allow to see: those about the nodes and servers of the scope of the logs permission;
// entries about nothing in particular, or nodes that were removed, need it everywhere.
func (f Filter) where(g access.Grants) (string, []any) {
	var (
		conds []string
		args  []any
	)
	add := func(cond string, a ...any) {
		conds = append(conds, cond)
		args = append(args, a...)
	}
	if all, nodes, servers := g.Scope(access.LogsView); !all {
		scope := []string{"0"}
		for _, n := range nodes {
			scope, args = append(scope, "node_id = ?"), append(args, n)
		}
		for _, t := range servers {
			scope, args = append(scope, "(node_id = ? AND server_id = ?)"), append(args, t.NodeID, t.ServerID)
		}
		conds = append(conds, "("+strings.Join(scope, " OR ")+")")
	}
	if l, err := logging.ParseLevel(f.Level); f.Level != "" && err == nil {
		add("level >= ?", int(l))
	}
	for _, c := range []struct{ column, value string }{
		{"source", f.Source}, {"category", f.Category}, {"username", f.User}, {"node_id", f.NodeID}, {"server_id", f.ServerID},
	} {
		if c.value != "" {
			add(c.column+" = ?", c.value)
		}
	}
	if f.Search != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(f.Search) + "%"
		add(`message || ' ' || username || ' ' || node_name || ' ' || server_name || ' ' || attrs LIKE ? ESCAPE '\'`, like)
	}
	if !f.Since.IsZero() {
		add("time >= ?", f.Since.UnixMilli())
	}
	if !f.Until.IsZero() {
		add("time < ?", f.Until.UnixMilli())
	}
	if f.Before > 0 {
		add("id < ?", f.Before)
	}
	if f.After > 0 {
		add("id > ?", f.After)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return "WHERE " + strings.Join(conds, " AND "), args
}
