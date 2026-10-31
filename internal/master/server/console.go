package server

import (
	"cmp"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/httpapi"
)

const (
	commandTimeout = 30 * time.Second
	// maxTail is how far back the earlier output reaches.
	maxTail = 1000
	// earlierTimeout ends the search for earlier output that never reaches the line it should
	// end at, e.g. of a server whose container was created again since.
	earlierTimeout = 10 * time.Second
	// maxEarlierBytes limits the earlier output the master holds at once, as servers can
	// write long lines, and a compromised agent many.
	maxEarlierBytes = 4 << 20
)

// text returns a line of the console, with its colours as Minecraft's codes if the client
// asks for them and the line has any.
func text(res *noryxv1.StreamLogsResponse, colors bool) string {
	if colors {
		return cmp.Or(res.GetFormatted(), res.GetLine())
	}
	return res.GetLine()
}

// logs streams the console of a server, see httpapi.StreamLog, until the server stops. With
// colors=true, as the panel asks, lines keep their colours as Minecraft's codes; scripts of
// the API get plain text otherwise.
func (h *Handler) logs(w http.ResponseWriter, r *http.Request) {
	colors := r.URL.Query().Get("colors") == "true"
	httpapi.StreamLog(w, r, func(ctx context.Context, tail uint32, after int64) (httpapi.Stream[*noryxv1.StreamLogsResponse], error) {
		c, err := h.client(ctx, r)
		if err != nil {
			return nil, err
		}
		stream, err := c.StreamLogs(ctx, &noryxv1.StreamLogsRequest{Id: r.PathValue("id"), Tail: tail, AfterUnixNano: after})
		return colored{stream, colors}, err
	})
}

// colored is a console stream whose lines have their colours if the client asks for them.
type colored struct {
	httpapi.Stream[*noryxv1.StreamLogsResponse]
	colors bool
}

func (c colored) Recv() (*noryxv1.StreamLogsResponse, error) {
	res, err := c.Stream.Recv()
	if err == nil {
		res.Line = text(res, c.colors)
	}
	return res, err
}

// earlier returns the lines of the console written before the line whose ID is before, oldest
// first and with their colours, as far back as the last maxTail lines and maxEarlierBytes
// reach. The agent sends these lines first, so reading stops at the line the panel shows
// already.
func (h *Handler) earlier(w http.ResponseWriter, r *http.Request) {
	before, err := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	if err != nil || before <= 0 {
		httpapi.WriteError(w, r, httpapi.Errorf(http.StatusBadRequest, "Name the line to load the output before."))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), earlierTimeout)
	defer cancel()
	c, err := h.client(ctx, r)
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	stream, err := c.StreamLogs(ctx, &noryxv1.StreamLogsRequest{Id: r.PathValue("id"), Tail: maxTail})
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	type line struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	lines, size := []line{}, 0
	for {
		res, err := stream.Recv()
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				httpapi.WriteError(w, r, err)
				return
			}
			break
		}
		t := res.GetTimeUnixNano()
		if t >= before {
			break
		}
		if t > 0 {
			lines = append(lines, line{strconv.FormatInt(t, 10), text(res, true)})
			size += len(lines[len(lines)-1].Text)
		}
		// The lines nearest to the one shown are kept.
		for len(lines) > maxTail || size > maxEarlierBytes {
			size -= len(lines[0].Text)
			lines = lines[1:]
		}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"lines": lines})
}

func (h *Handler) command(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Command string `json:"command"`
	}
	if err := httpapi.ReadJSON(w, r, &req); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), commandTimeout)
	defer cancel()
	c, err := h.client(ctx, r)
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	res, err := c.SendCommand(ctx, &noryxv1.SendCommandRequest{Id: r.PathValue("id"), Command: req.Command})
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"output": res.GetOutput(), "formatted": res.GetFormatted()})
}
