package api

import (
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"

	"github.com/dmtrkzntsv/twillingate/internal/reporting"
)

// maxStreamWidgets caps one POST /api/widgets/stream: more widgets are
// refused before the stream starts.
const maxStreamWidgets = 64

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
		stream, err := newSSE(w, req)
		if err != nil {
			r.logger.Warn("widget stream: the response cannot be flushed", "err", err)
			return
		}
		// Closed before the handler returns, so the keepalive loop never
		// writes after it.
		defer stream.Close()

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
		stream.Keepalive(h.keepalive)
		for {
			select {
			case <-stream.Done():
				// The client went away, or a write failed: the answers
				// still to come (each the context's error, or a load that
				// finished just now) go into results, which nobody reads,
				// and are dropped with it; WidgetDataStream never blocks
				// on them. Returning cancels the request's context, which
				// stops every load still running.
				return
			case res, ok := <-results:
				if !ok {
					_ = stream.Event("done", struct{}{})
					return
				}
				ev := widgetEvent{WidgetID: res.WidgetID}
				if res.Err != nil {
					_, e := refusalOf(r.logger, req, res.Err)
					ev.Error = &e
				} else {
					ev.Data = &res.Data
				}
				err := stream.Event("widget", ev)
				if err != nil && !errors.Is(err, errSSEClosed) {
					// The answer did not marshal: the widget gets the error instead.
					_, e := refusalOf(r.logger, req, err)
					err = stream.Event("widget", widgetEvent{WidgetID: res.WidgetID, Error: &e})
				}
				if err != nil {
					return
				}
			}
		}
	}
}
