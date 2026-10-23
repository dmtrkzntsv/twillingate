package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/reporting"
)

// maxStreamWidgets caps one POST /api/widgets/stream: more widgets are
// refused before the stream starts.
const maxStreamWidgets = 64

// streamKeepalive is how often the widget stream sends a keepalive
// comment, so a proxy or a browser does not take a long scan for a dead
// connection.
const streamKeepalive = 15 * time.Second

type widgetStreamIn struct {
	Widgets []widgetStreamItem `json:"widgets" jsonschema:"the widgets to load, at most 64, each as GET /api/widgets/{widget_id}/data takes it without a remote table's paging arguments"`
}

type widgetStreamItem struct {
	WidgetID  int64  `json:"widget_id" jsonschema:"widget id"`
	ProjectID int64  `json:"project_id,omitempty" jsonschema:"required when the widget follows the project switcher; ignored otherwise"`
	From      string `json:"from,omitempty" jsonschema:"YYYY-MM-DD; with to, required when the widget follows the range switcher; ignored otherwise"`
	To        string `json:"to,omitempty" jsonschema:"YYYY-MM-DD, at most 365 days after from; a day after today is clamped to today"`
	Fresh     bool   `json:"fresh,omitempty" jsonschema:"true reuses a cached result only up to REPORTING_REFRESH_MAX_AGE old"`
}

// widgetEvent is one widget's event in the stream: its GET body as data,
// or the error GET would answer, code and message.
type widgetEvent struct {
	WidgetID int64                 `json:"widget_id"`
	Data     *reporting.WidgetData `json:"data,omitempty"`
	Error    *apiError             `json:"error,omitempty"`
}

// registerWidgetStream registers POST /api/widgets/stream, REST only: the
// console's way to load a dashboard's widgets in one request.
func (h *host) registerWidgetStream(r *registrar) {
	s := spec{Name: "widget_stream", Method: "POST", Path: "/api/widgets/stream", RESTOnly: true, Stream: true,
		Description: "The console's dashboard load: up to 64 widgets in one request, answered as Server-Sent Events, one `widget` event per widget as it is ready ({widget_id, data} with data the body GET /api/widgets/{widget_id}/data answers, or {widget_id, error: {code, message}} with its refusal), cached widgets first, then the rest as they finish, and a final `done` event. A `: keepalive` comment goes out every 15 seconds. Closing the request stops its widgets' queries. Remote tables' paging stays on the GET route."}
	s.in, s.out = schemaFor[widgetStreamIn](), schemaFor[widgetEvent]()
	r.specs = append(r.specs, s)
	if r.rest != nil {
		r.rest.HandleFunc(s.Method+" "+s.Path, h.widgetStream(r))
	}
}

// widgetStream answers POST /api/widgets/stream. A bad body or more than
// maxStreamWidgets widgets is a plain JSON refusal; after that the answer
// is a stream, and a widget's refusal is its own event.
//
// One goroutine, this handler's, writes: WidgetDataStream hands each
// widget's answer to a channel with room for every widget, so a slow
// client never holds up the loads (or the engine's scans) and the loads
// never write concurrently. The loads run in the request's context: when
// the client goes away, or a write fails and the handler returns, every
// query still running stops and what it had not finished is not cached.
func (h *host) widgetStream(r *registrar) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in widgetStreamIn
		if err := decodeRequest(req, &in); err != nil {
			writeError(w, r.logger, req, err)
			return
		}
		if len(in.Widgets) > maxStreamWidgets {
			writeError(w, r.logger, req, invalidf("widgets: %d widgets; a stream takes at most %d", len(in.Widgets), maxStreamWidgets))
			return
		}
		reqs := make([]reporting.WidgetRequest, len(in.Widgets))
		for i, x := range in.Widgets {
			reqs[i] = reporting.WidgetRequest{WidgetID: x.WidgetID, ProjectID: x.ProjectID, From: x.From, To: x.To, Fresh: x.Fresh}
		}

		ctx := withActor(req.Context(), "api")
		rc := http.NewResponseController(w)
		// A server write timeout would cut a long stream; the stream ends
		// when its widgets are done or its client leaves instead.
		_ = rc.SetWriteDeadline(time.Time{})
		hd := w.Header()
		hd.Set("Content-Type", "text/event-stream")
		hd.Set("Cache-Control", "no-cache")
		hd.Set("X-Accel-Buffering", "no") // nginx: do not buffer this response
		w.WriteHeader(http.StatusOK)
		if rc.Flush() != nil {
			return
		}

		results := make(chan reporting.WidgetResult, len(reqs))
		go func() {
			defer close(results)
			// WidgetDataStream answers its own panics; this is the last
			// guard, so a bug never takes the process down. The stream
			// then ends with done, a widget left unanswered showing as
			// pending, which the page loads alone.
			defer func() {
				if p := recover(); p != nil {
					r.logger.Error("widget stream panicked", "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
				}
			}()
			h.rep.WidgetDataStream(ctx, reqs, func(res reporting.WidgetResult) { results <- res })
		}()
		every := h.keepalive
		if every <= 0 {
			every = streamKeepalive
		}
		tick := time.NewTicker(every)
		defer tick.Stop()
		write := func(b []byte) bool {
			if _, err := w.Write(b); err != nil {
				return false
			}
			return rc.Flush() == nil
		}
		for {
			select {
			case <-ctx.Done():
				// The client went away: the answers still to come (each
				// the context's error, or a load that finished just now)
				// go into results, which nobody reads, and are dropped
				// with it; WidgetDataStream never blocks on them.
				return
			case <-tick.C:
				if !write([]byte(": keepalive\n\n")) {
					return
				}
			case res, ok := <-results:
				if !ok {
					write([]byte("event: done\ndata: {}\n\n"))
					return
				}
				ev := widgetEvent{WidgetID: res.WidgetID}
				if res.Err != nil {
					_, e := refusalOf(r.logger, req, res.Err)
					ev.Error = &e
				} else {
					ev.Data = &res.Data
				}
				b, err := json.Marshal(ev)
				if err != nil {
					_, e := refusalOf(r.logger, req, err)
					b, _ = json.Marshal(widgetEvent{WidgetID: res.WidgetID, Error: &e})
				}
				if !write(append(append([]byte("event: widget\ndata: "), b...), '\n', '\n')) {
					return
				}
			}
		}
	}
}
