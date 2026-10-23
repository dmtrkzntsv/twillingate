package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/reporting"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// heldNative is the engine with every AnswerMany held until open is
// closed or its context ends, and the requests' contexts recorded.
type heldNative struct {
	*native.Engine
	open    chan struct{}
	started chan struct{}
	once    sync.Once
	mu      sync.Mutex
	ctxs    []context.Context
}

func (n *heldNative) AnswerMany(ctx context.Context, reqs []native.Request, done func(int, native.Answered)) {
	n.mu.Lock()
	for _, r := range reqs {
		n.ctxs = append(n.ctxs, r.Ctx)
	}
	n.mu.Unlock()
	n.once.Do(func() { close(n.started) })
	select {
	case <-n.open:
	case <-ctx.Done():
	}
	n.Engine.AnswerMany(ctx, reqs, done)
}

// streamServer is the test host's REST routes over a real HTTP server,
// with query widgets answered by a held engine and the keepalive at
// every.
func streamServer(t *testing.T, every time.Duration) (*host, *httptest.Server, *heldNative) {
	t.Helper()
	var nat *heldNative
	h, srv := streamServerWith(t, every, func(e *native.Engine) reporting.Native {
		nat = &heldNative{Engine: e, open: make(chan struct{}), started: make(chan struct{})}
		return nat
	})
	return h, srv, nat
}

// streamServerWith is streamServer with query widgets answered by what
// mk makes of the host's engine.
func streamServerWith(t *testing.T, every time.Duration, mk func(*native.Engine) reporting.Native) (*host, *httptest.Server) {
	t.Helper()
	h, _ := newTestHost(t)
	st, err := store.Open("sqlite://" + testDBPaths[h])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h.rep = reporting.New(st, nil, reporting.Options{CacheAge: time.Minute, RefreshAge: time.Second, Native: mk(h.native.(*native.Engine))})
	h.keepalive = every
	srv := httptest.NewServer(newTestRegistrar(t, h).rest)
	t.Cleanup(srv.Close)
	return h, srv
}

// sseEvent is one event of the stream, or a comment (name "").
type sseEvent struct {
	name, data, comment string
}

// readEvent reads the next event or comment off sc.
func readEvent(t *testing.T, sc *bufio.Scanner) (sseEvent, bool) {
	t.Helper()
	var ev sseEvent
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			return ev, true
		case strings.HasPrefix(line, ": "):
			ev.comment = strings.TrimPrefix(line, ": ")
		case strings.HasPrefix(line, "event: "):
			ev.name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			ev.data = strings.TrimPrefix(line, "data: ")
		default:
			t.Fatalf("unexpected line %q", line)
		}
	}
	return ev, false
}

// viewsWidgets is the Views dashboard's widgets for blog over the
// host's two days, and the body asking for them.
func viewsWidgets(t *testing.T, h *host) ([]widgetStreamItem, string) {
	t.Helper()
	d, err := h.rep.Dashboard(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	var items []widgetStreamItem
	for _, w := range d.Widgets {
		items = append(items, widgetStreamItem{WidgetID: w.ID, ProjectID: 1, From: "2026-08-20", To: "2026-08-21"})
	}
	b, _ := json.Marshal(map[string]any{"widgets": items})
	return items, string(b)
}

func postStream(t *testing.T, ctx context.Context, url, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, "POST", url+"/api/widgets/stream", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// TestWidgetStreamRoute drives POST /api/widgets/stream over HTTP: the
// headers, an event read while the engine is still held (so events
// arrive before the stream ends), each widget's data the same bytes as
// its GET body, a missing widget's error with GET's code, and done last.
func TestWidgetStreamRoute(t *testing.T) {
	h, srv, nat := streamServer(t, time.Hour)
	items, _ := viewsWidgets(t, h)
	items = append([]widgetStreamItem{{WidgetID: 999999}}, items...)
	body, _ := json.Marshal(map[string]any{"widgets": items})

	resp := postStream(t, context.Background(), srv.URL, string(body))
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	for k, v := range map[string]string{"Content-Type": "text/event-stream", "Cache-Control": "no-cache", "X-Accel-Buffering": "no"} {
		if got := resp.Header.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)

	// The missing widget is refused before any load: its event comes
	// while the engine is held.
	first, ok := readEvent(t, sc)
	if !ok || first.name != "widget" || !strings.Contains(first.data, `"code":"not_found"`) || !strings.Contains(first.data, `"widget_id":999999`) {
		t.Fatalf("first event %+v", first)
	}
	<-nat.started
	close(nat.open)

	got := map[int64]string{}
	var last sseEvent
	for {
		ev, ok := readEvent(t, sc)
		if !ok {
			break
		}
		last = ev
		if ev.name != "widget" {
			continue
		}
		var e struct {
			WidgetID int64           `json:"widget_id"`
			Data     json.RawMessage `json:"data"`
			Error    *apiError       `json:"error"`
		}
		if err := json.Unmarshal([]byte(ev.data), &e); err != nil {
			t.Fatalf("event %q: %v", ev.data, err)
		}
		if e.Error != nil {
			t.Errorf("widget %d: %+v", e.WidgetID, e.Error)
			continue
		}
		got[e.WidgetID] = string(e.Data)
	}
	if last.name != "done" || last.data != "{}" {
		t.Errorf("last event %+v, want done", last)
	}
	if len(got) != len(items)-1 {
		t.Fatalf("%d widgets answered, want %d", len(got), len(items)-1)
	}
	// The GETs hit the entries the stream stored: the same bytes.
	for _, it := range items[1:] {
		r, err := http.Get(fmt.Sprintf("%s/api/widgets/%d/data?project_id=1&from=%s&to=%s", srv.URL, it.WidgetID, it.From, it.To))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if want := strings.TrimSuffix(string(b), "\n"); got[it.WidgetID] != want {
			t.Errorf("widget %d:\nstream %s\nGET    %s", it.WidgetID, got[it.WidgetID], want)
		}
	}
}

// TestWidgetStreamCancelsOnClose: closing the request cancels every
// engine request of the stream.
func TestWidgetStreamCancelsOnClose(t *testing.T) {
	h, srv, nat := streamServer(t, time.Hour)
	_, body := viewsWidgets(t, h)
	ctx, cancel := context.WithCancel(context.Background())
	resp := postStream(t, ctx, srv.URL, body)
	defer resp.Body.Close()
	select {
	case <-nat.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the engine never started")
	}
	cancel()
	deadline := time.Now().Add(10 * time.Second)
	for {
		nat.mu.Lock()
		running := 0
		for _, c := range nat.ctxs {
			if c.Err() == nil {
				running++
			}
		}
		n := len(nat.ctxs)
		nat.mu.Unlock()
		if n > 0 && running == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d engine requests still running after the client closed", running, n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestWidgetStreamKeepalive: while the engine is held, a keepalive comment
// goes out every interval.
func TestWidgetStreamKeepalive(t *testing.T) {
	h, srv, nat := streamServer(t, 20*time.Millisecond)
	_, body := viewsWidgets(t, h)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp := postStream(t, ctx, srv.URL, body)
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	keepalives := 0
	for keepalives < 2 {
		ev, ok := readEvent(t, sc)
		if !ok {
			t.Fatalf("the stream ended after %d keepalives", keepalives)
		}
		if ev.comment == "keepalive" && ev.name == "" {
			keepalives++
		}
	}
	close(nat.open)
	var last sseEvent
	for {
		ev, ok := readEvent(t, sc)
		if !ok {
			break
		}
		if ev.comment == "" {
			last = ev
		}
	}
	if last.name != "done" {
		t.Errorf("last event %+v, want done", last)
	}
}

// TestWidgetStreamRefusals: a bad body and more than 64 widgets are plain
// JSON errors before any stream; no widgets streams done alone.
func TestWidgetStreamRefusals(t *testing.T) {
	_, srv, _ := streamServer(t, time.Hour)
	many := make([]widgetStreamItem, 65)
	for i := range many {
		many[i] = widgetStreamItem{WidgetID: int64(i + 1)}
	}
	b, _ := json.Marshal(map[string]any{"widgets": many})
	for name, body := range map[string]string{
		"65 widgets":    string(b),
		"unknown field": `{"widgets":[{"widget_id":1,"limit":5}]}`,
		"not JSON":      `{"widgets":`,
	} {
		resp := postStream(t, context.Background(), srv.URL, body)
		out, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 400 || resp.Header.Get("Content-Type") != "application/json" || !strings.Contains(string(out), `"code":"invalid"`) {
			t.Errorf("%s: %d %s %s", name, resp.StatusCode, resp.Header.Get("Content-Type"), out)
		}
	}
	resp := postStream(t, context.Background(), srv.URL, `{"widgets":[]}`)
	out, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(out) != "event: done\ndata: {}\n\n" {
		t.Errorf("no widgets: %q", out)
	}
}

// TestWidgetStreamNeedsAuth: the route is behind the console's bearer
// token, as every /api/ route.
func TestWidgetStreamNeedsAuth(t *testing.T) {
	srv := httptest.NewServer(e2eHandler(t, "token://ar_testtoken", false))
	defer srv.Close()
	for _, c := range []struct {
		token string
		want  int
	}{{"", 401}, {"ar_wrong", 401}, {"ar_testtoken", 200}} {
		req, _ := http.NewRequest("POST", srv.URL+"/api/widgets/stream", strings.NewReader(`{"widgets":[{"widget_id":1}]}`))
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("token %q: %d %s", c.token, resp.StatusCode, out)
		}
		if c.want == 200 && !strings.HasSuffix(string(out), "event: done\ndata: {}\n\n") {
			t.Errorf("authorized stream %q", out)
		}
	}
}

// panicManyNative is the engine with AnswerMany panicking (a bug).
type panicManyNative struct{ *native.Engine }

func (panicManyNative) AnswerMany(context.Context, []native.Request, func(int, native.Answered)) {
	panic("boom in AnswerMany")
}

// TestWidgetStreamPanics: a panic in the engine's batch ends the stream
// with each query widget's internal error and done, the server goes on,
// and a GET of one of those widgets then answers.
func TestWidgetStreamPanics(t *testing.T) {
	h, srv := streamServerWith(t, time.Hour, func(e *native.Engine) reporting.Native { return panicManyNative{e} })
	items, body := viewsWidgets(t, h)
	resp := postStream(t, context.Background(), srv.URL, body)
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	internal, widgets := 0, 0
	var last sseEvent
	for {
		ev, ok := readEvent(t, sc)
		if !ok {
			break
		}
		last = ev
		if ev.name == "widget" {
			widgets++
			if strings.Contains(ev.data, `"code":"internal"`) {
				internal++
			}
		}
	}
	resp.Body.Close()
	if last.name != "done" || widgets != len(items) || internal == 0 {
		t.Fatalf("last %+v, %d widget events of %d, %d internal", last, widgets, len(items), internal)
	}
	for _, it := range items {
		r, err := http.Get(fmt.Sprintf("%s/api/widgets/%d/data?project_id=1&from=%s&to=%s", srv.URL, it.WidgetID, it.From, it.To))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != 200 {
			t.Errorf("GET widget %d after the panic: %d %s", it.WidgetID, r.StatusCode, b)
		}
	}
}
