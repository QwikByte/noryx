package logs

import (
	"cmp"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/httpapi"
)

const (
	pageSize  = 100
	maxPage   = 500
	maxExport = 100_000
	keepAlive = 25 * time.Second // shorter than the timeouts of common reverse proxies
	// A stream ends after maxStream; the browser connects again right away, which checks
	// the session and the permissions again.
	maxStream = 5 * time.Minute
)

// Handler serves the log to the panel.
type Handler struct{ store *Store }

func NewHandler(store *Store) *Handler { return &Handler{store: store} }

// seesLogs needs the logs permission anywhere; users only get the entries of its scope.
func seesLogs(_ *http.Request, g access.Grants) (access.Permission, bool) {
	return access.LogsView, g.Somewhere(access.LogsView, "")
}

func (h *Handler) Register(mux access.Mux) {
	mux.Handle("GET /api/logs", seesLogs, h.list)
	mux.Handle("GET /api/logs/stats", seesLogs, h.stats)
	mux.Handle("GET /api/logs/stream", seesLogs, h.stream)
	mux.Handle("GET /api/logs/export", seesLogs, h.export)
}

// list returns a page of entries, the newest first; before continues with older ones.
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	f, err := FilterFrom(r)
	limit := pageSize
	if err == nil {
		f.Before, err = number[int64](r, "before", 0, 1<<62)
	}
	if err == nil {
		limit, err = number(r, "limit", pageSize, maxPage)
	}
	var entries []Entry
	if err == nil {
		entries, err = h.store.List(r.Context(), f, false, limit)
	}
	write(w, r, entries, err)
}

// stats counts the entries per hour and level, for the last 24 hours unless hours says otherwise.
func (h *Handler) stats(w http.ResponseWriter, r *http.Request) {
	f, err := FilterFrom(r)
	hours := 24
	if err == nil {
		hours, err = number(r, "hours", hours, MaxStatsHours)
	}
	var buckets []Bucket
	if err == nil {
		buckets, err = h.store.Stats(r.Context(), f, max(hours, 1))
	}
	write(w, r, buckets, err)
}

// stream sends the entries that match the filter as Server-Sent Events as they are added,
// oldest first. Each event carries the ID of its entry, so that a browser that reconnects
// continues after the last one it got; without one, the stream starts with new entries.
func (h *Handler) stream(w http.ResponseWriter, r *http.Request) {
	f, err := FilterFrom(r)
	if err == nil {
		f.After, err = strconv.ParseInt(cmp.Or(r.Header.Get("Last-Event-ID"), r.URL.Query().Get("after"), "0"), 10, 64)
	}
	if err == nil && f.After <= 0 {
		f.After, err = h.store.last(r.Context())
	}
	if err != nil {
		httpapi.WriteError(w, r, httpapi.Errorf(http.StatusBadRequest, "The stream can't start: %v.", err))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no") // keeps nginx from buffering the stream
	flusher := http.NewResponseController(w)
	fmt.Fprint(w, "retry: 5000\n\n")
	ticker, end := time.NewTicker(keepAlive), time.After(maxStream)
	defer ticker.Stop()
	for {
		changed := h.store.Changed()
		entries, err := h.store.List(r.Context(), f, true, batchSize)
		if err != nil {
			return
		}
		for _, e := range entries {
			data, _ := json.Marshal(e) // escapes line breaks, so an entry can't forge events
			fmt.Fprintf(w, "id: %d\ndata: %s\n\n", e.ID, data)
			f.After = e.ID
		}
		if flusher.Flush() != nil {
			return
		}
		if len(entries) == batchSize {
			continue
		}
		select {
		case <-r.Context().Done():
			return
		case <-end:
			return
		case <-changed:
		case <-ticker.C:
			fmt.Fprint(w, ": keep-alive\n\n")
		}
	}
}

// export downloads the entries that match the filter, oldest first, as CSV or JSON lines.
// CSV separates cells with commas, or with semicolons for spreadsheets in languages whose
// decimal separator is a comma (separator=semicolon), and starts with a byte order mark for
// Excel if bom=on.
func (h *Handler) export(w http.ResponseWriter, r *http.Request) {
	f, err := FilterFrom(r)
	q := r.URL.Query()
	if err == nil && (!slices.Contains([]string{"", "comma", "semicolon"}, q.Get("separator")) || !slices.Contains([]string{"", "off", "on"}, q.Get("bom"))) {
		err = httpapi.Errorf(http.StatusBadRequest, "Choose the separator comma or semicolon, and the byte order mark on or off.")
	}
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	name := "noryx-log-" + time.Now().Format("2006-01-02-150405")
	switch q.Get("format") {
	case "csv":
		httpapi.Attachment(w, name+".csv")
		if q.Get("bom") == "on" {
			_, _ = io.WriteString(w, "\uFEFF")
		}
		out := csv.NewWriter(w)
		if q.Get("separator") == "semicolon" {
			out.Comma = ';' // encoding/csv quotes the cells that hold it
		}
		_ = out.Write([]string{"time", "level", "source", "category", "message", "user", "node", "node_id", "server", "server_id", "attributes"})
		_ = h.store.Each(r.Context(), f, true, maxExport, func(e Entry) error {
			attrs, _ := json.Marshal(e.Attrs)
			row := []string{e.Time.Format(time.RFC3339Nano), logging.LevelName(e.Level), e.Source, e.Category, e.Message, e.User,
				e.NodeName, e.NodeID, e.ServerName, e.ServerID, string(attrs)}
			for i := range row {
				row[i] = spreadsheetSafe(row[i])
			}
			return out.Write(row)
		})
		out.Flush()
	case "jsonl":
		httpapi.Attachment(w, name+".jsonl")
		enc := json.NewEncoder(w)
		_ = h.store.Each(r.Context(), f, true, maxExport, func(e Entry) error { return enc.Encode(e) })
	default:
		httpapi.WriteError(w, r, httpapi.Errorf(http.StatusBadRequest, "Choose the format csv or jsonl."))
	}
}

// spreadsheetSafe keeps spreadsheets from running a cell as a formula, which a user or an
// agent could otherwise slip into an entry.
func spreadsheetSafe(cell string) string {
	if cell != "" && strings.ContainsRune("=+-@\t\r", rune(cell[0])) {
		return "'" + cell
	}
	return cell
}

// number reads a non-negative number of the query, or returns def if there is none.
func number[T int | int64](r *http.Request, key string, def, maximum T) (T, error) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 || T(n) > maximum {
		return def, httpapi.Errorf(http.StatusBadRequest, "Enter %s as a number from 0 to %d.", key, maximum)
	}
	return T(n), nil
}

func write(w http.ResponseWriter, r *http.Request, v any, err error) {
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, v)
}
