package app

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/civil"
	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/config/configtest"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	_ "github.com/dmtrkzntsv/twillingate/internal/store/sqlite"
)

const chromeUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

func mustDay(s string) civil.Date { d, _ := civil.Parse(s); return d }

// seedProject writes a project and ingest key straight into the registry
// (bypassing HTTP/CLI) before Serve boots, since the running server only
// ever reads projects from the database now. A no-op if any project already
// exists (names are not keys), so a restart test can call it again against
// the same file.
func seedProject(t *testing.T, dbPath string, spec manage.ProjectSpec, key, keyLabel string) {
	t.Helper()
	st, err := store.Open("sqlite://" + dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	reg := manage.New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if len(reg.Snapshot(ctx).Projects()) > 0 {
		return
	}
	ops := manage.NewOps(reg, st)
	p, err := ops.CreateProject(ctx, "test", spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.InsertIngestKey(ctx, store.RegistryKey{Key: key, ProjectID: p.ID, Label: keyLabel},
		store.AuditEntry{Actor: "test", Action: "key.issue", Subject: keyLabel}); err != nil {
		t.Fatal(err)
	}
}

func testConfig(t *testing.T, addr, dbPath string) *config.Config {
	t.Helper()
	seedProject(t, dbPath,
		manage.ProjectSpec{Name: "App", AllowedOrigins: []string{"https://app.com"}},
		"ak_test", "web")
	return configtest.Load(t, map[string]string{
		"INGEST_ADDR":             addr,
		"DATABASE_DSN":            "sqlite://" + dbPath,
		"BUFFER_FLUSH_MAX_EVENTS": "2",
		"BUFFER_FLUSH_INTERVAL":   "50ms",
		"BUFFER_CAPACITY":         "100",
	})
}

func waitHealthy(t *testing.T, base string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(base + "/healthz"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("server never became healthy")
}

func TestServeEndToEnd(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "e2e.db")
	addr := freePort(t)
	cfg := testConfig(t, addr, dbPath)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, cfg, slog.Default(), true, false) }()

	base := "http://" + addr
	waitHealthy(t, base)

	post := func(path, origin, body string) *http.Response {
		t.Helper()
		req, err := http.NewRequest("POST", base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		req.Header.Set("User-Agent", chromeUA)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	// One envelope covering all three destinations.
	if r := post("/ingest/events", "https://app.com",
		`{"key":"ak_test","attributes":{"$os":"ios","$app_version":"1.0"},
		  "events":[
		    {"name":"$page_view","attributes":{"$host":"app.com","$path":"/pricing"}},
		    {"name":"$screen_view","attributes":{"$screen":"/settings"}},
		    {"name":"signup","attributes":{"plan":"pro"}}]}`); r.StatusCode != 202 {
		t.Fatalf("events: %d", r.StatusCode)
	}
	if r := post("/ingest/events", "",
		`{"key":"ak_test","events":[{"name":"signup","attributes":{"plan":"pro"}}]}`); r.StatusCode != 202 {
		t.Fatalf("keyed event without Origin: %d", r.StatusCode)
	}
	if r := post("/ingest/events", "https://evil.com",
		`{"key":"ak_test","events":[{"name":"x"}]}`); r.StatusCode != 403 {
		t.Fatalf("evil origin: %d", r.StatusCode)
	}
	if r := post("/ingest/events", "",
		`{"key":"nope","events":[{"name":"x"}]}`); r.StatusCode != 401 {
		t.Fatalf("bad key: %d", r.StatusCode)
	}

	// The SDK must be served by the same process.
	resp, err := http.Get(base + "/js/twillingate.js")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("twillingate.js: %d", resp.StatusCode)
	}

	// Graceful shutdown must flush the buffer.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not shut down")
	}

	st, err := store.Open("sqlite://" + dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	bg := context.Background()
	days, err := st.ViewDaysBefore(bg, 1, mustDay("2100-01-01"))
	if err != nil || len(days) != 1 {
		t.Fatalf("view not persisted: %v %v", days, err)
	}
	pdays, err := st.ProductDaysBefore(bg, 1, mustDay("2100-01-01"))
	if err != nil || len(pdays) != 1 {
		t.Fatalf("event not persisted: %v %v", pdays, err)
	}
	// The seeded project must still be there after a full boot/shutdown
	// cycle: Serve reads it, never rewrites the registry.
	ids, err := st.ProjectIDs(bg)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("ids = %v, want [1]", ids)
	}
}

// Restarting against an existing database must re-run migrations harmlessly
// and keep prior data.
func TestServeRestartsOnExistingDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "restart.db")
	bg := context.Background()

	run := func(body string) {
		t.Helper()
		addr := freePort(t)
		ctx, cancel := context.WithCancel(bg)
		done := make(chan error, 1)
		go func() { done <- Serve(ctx, testConfig(t, addr, dbPath), slog.Default(), true, false) }()
		base := "http://" + addr
		waitHealthy(t, base)
		req, err := http.NewRequest("POST", base+"/ingest/events", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", "https://app.com")
		req.Header.Set("User-Agent", chromeUA)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 202 {
			t.Fatalf("hit: %d", resp.StatusCode)
		}
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("serve: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("serve did not shut down")
		}
	}
	run(`{"key":"ak_test","events":[{"name":"$pageview","attributes":{"$host":"app.com","$path":"/one"}}]}`)
	run(`{"key":"ak_test","events":[{"name":"$pageview","attributes":{"$host":"app.com","$path":"/two"}}]}`)

	st, err := store.Open("sqlite://" + dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	days, err := st.ViewDaysBefore(bg, 1, mustDay("2100-01-01"))
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 {
		t.Fatalf("days = %v, want one day holding both hits", days)
	}
}

// apiTestConfig is testConfig plus the API surface's env: token auth mode
// so a request with no bearer token is a deterministic 401, and (when
// apiAddr is non-empty) a second listener address for the split-listener
// sub-run.
func apiTestConfig(t *testing.T, addr, dbPath, apiAddr string) *config.Config {
	t.Helper()
	seedProject(t, dbPath,
		manage.ProjectSpec{Name: "App", AllowedOrigins: []string{"https://app.com"}},
		"ak_test", "web")
	vars := map[string]string{
		"INGEST_ADDR":             addr,
		"DATABASE_DSN":            "sqlite://" + dbPath,
		"BUFFER_FLUSH_MAX_EVENTS": "2",
		"BUFFER_FLUSH_INTERVAL":   "50ms",
		"BUFFER_CAPACITY":         "100",
		"API_AUTH_DSN":            "token://ar_apptest",
	}
	if apiAddr != "" {
		vars["API_ADDR"] = apiAddr
	}
	return configtest.Load(t, vars)
}

// Spec §3.2/Task 21: -ingest and -api together share one listener when
// API_ADDR equals INGEST_ADDR, and use two listeners otherwise. Both
// arrangements must serve both surfaces correctly and shut down cleanly.
func TestServeSharedListenerServesBothSurfaces(t *testing.T) {
	run := func(t *testing.T, cfg *config.Config) {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- Serve(ctx, cfg, slog.Default(), true, true) }()
		waitHealthy(t, "http://"+cfg.IngestAddr)

		t.Cleanup(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("serve: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("serve did not shut down within 5s of ctx cancel")
			}
		})

		if resp, err := http.Get("http://" + cfg.IngestAddr + "/healthz"); err != nil {
			t.Fatalf("healthz: %v", err)
		} else {
			resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Errorf("healthz on %s = %d, want 200", cfg.IngestAddr, resp.StatusCode)
			}
		}

		if resp, err := http.Post("http://"+cfg.IngestAddr+"/ingest/events", "application/json",
			strings.NewReader(`{"key":"ak_test","events":[{"name":"x"}]}`)); err != nil {
			t.Fatalf("events: %v", err)
		} else {
			resp.Body.Close()
			if resp.StatusCode == 404 {
				t.Errorf("events on %s = 404, want the ingest surface to answer", cfg.IngestAddr)
			}
		}

		getAPI := func(addr string) *http.Response {
			t.Helper()
			resp, err := http.Get("http://" + addr + "/api/projects")
			if err != nil {
				t.Fatalf("GET /api/projects on %s: %v", addr, err)
			}
			return resp
		}

		postMCP := func(addr string) *http.Response {
			t.Helper()
			resp, err := http.Post("http://"+addr+"/mcp", "application/json", strings.NewReader(`{}`))
			if err != nil {
				t.Fatalf("POST /mcp on %s: %v", addr, err)
			}
			return resp
		}

		// The legacy ingest path sits under /api/ but belongs to ingest on
		// any topology: a shared listener must route it past the API's auth.
		// A preflight tells the two apart — ingest answers 204, auth 401.
		legacy, err := http.NewRequest("OPTIONS", "http://"+cfg.IngestAddr+"/api/events", nil)
		if err != nil {
			t.Fatal(err)
		}
		legacy.Header.Set("Origin", "https://app.com")
		if resp, err := http.DefaultClient.Do(legacy); err != nil {
			t.Fatalf("OPTIONS /api/events: %v", err)
		} else {
			resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent {
				t.Errorf("OPTIONS /api/events on %s = %d, want 204 from the ingest surface", cfg.IngestAddr, resp.StatusCode)
			}
		}

		if cfg.API.Addr == cfg.IngestAddr {
			resp := getAPI(cfg.IngestAddr)
			resp.Body.Close()
			if resp.StatusCode != 401 {
				t.Errorf("GET /api/projects (shared, no token) = %d, want 401", resp.StatusCode)
			}

			resp = postMCP(cfg.IngestAddr)
			resp.Body.Close()
			if resp.StatusCode != 401 {
				t.Errorf("POST /mcp (shared, no token) = %d, want 401", resp.StatusCode)
			}
		} else {
			resp := getAPI(cfg.IngestAddr)
			resp.Body.Close()
			if resp.StatusCode != 404 {
				t.Errorf("GET /api/projects on ingest port %s = %d, want 404", cfg.IngestAddr, resp.StatusCode)
			}
			resp = getAPI(cfg.API.Addr)
			resp.Body.Close()
			if resp.StatusCode != 401 {
				t.Errorf("GET /api/projects on API port %s (no token) = %d, want 401", cfg.API.Addr, resp.StatusCode)
			}

			resp = postMCP(cfg.IngestAddr)
			resp.Body.Close()
			if resp.StatusCode != 404 {
				t.Errorf("POST /mcp on ingest port %s = %d, want 404", cfg.IngestAddr, resp.StatusCode)
			}
			resp = postMCP(cfg.API.Addr)
			resp.Body.Close()
			if resp.StatusCode != 401 {
				t.Errorf("POST /mcp on API port %s (no token) = %d, want 401", cfg.API.Addr, resp.StatusCode)
			}
		}
	}

	t.Run("shared listener", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "shared.db")
		addr := freePort(t)
		cfg := apiTestConfig(t, addr, dbPath, "")
		run(t, cfg)
	})

	t.Run("separate listeners", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "split.db")
		addr := freePort(t)
		apiAddr := freePort(t)
		cfg := apiTestConfig(t, addr, dbPath, apiAddr)
		run(t, cfg)
	})
}

// A listen address already in use must surface as an error, not a hang.
func TestServeReportsListenFailure(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	cfg := testConfig(t, l.Addr().String(), filepath.Join(t.TempDir(), "busy.db"))
	errCh := make(chan error, 1)
	go func() { errCh <- Serve(context.Background(), cfg, slog.Default(), true, false) }()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("want an error binding an occupied port, got nil")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve hung instead of reporting the bind failure")
	}
}

func TestServeRejectsBadDatabaseDSN(t *testing.T) {
	cfg := testConfig(t, freePort(t), filepath.Join(t.TempDir(), "x.db"))
	cfg.Database = "bogus://nope"
	if err := Serve(context.Background(), cfg, slog.Default(), true, false); err == nil {
		t.Fatal("want an error for an unknown DSN scheme")
	}
}

func TestNewLogger(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error", ""} {
		if NewLogger(config.LogConfig{Level: level}) == nil {
			t.Errorf("level %q returned nil", level)
		}
	}
	if NewLogger(config.LogConfig{Format: "text"}) == nil {
		t.Error("text format returned nil")
	}
	// A configured file must actually receive the output.
	path := filepath.Join(t.TempDir(), "log.json")
	NewLogger(config.LogConfig{File: path}).Info("hello")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "hello") {
		t.Errorf("log file = %q, want it to contain the message", body)
	}
}

// Spec §5.4a: an IP address or a full User-Agent must never reach the
// database or the logs. They may only feed the one-way visitor hash and the
// device/browser classification.
func TestServeNeverPersistsIPOrUserAgent(t *testing.T) {
	const (
		secretIP = "203.0.113.77"
		secretUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
			"(KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 UNIQUEMARKERSTRING"
	)
	dbPath := filepath.Join(t.TempDir(), "privacy.db")
	addr := freePort(t)
	cfg := testConfig(t, addr, dbPath)

	var logs strings.Builder
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, cfg, logger, true, false) }()
	base := "http://" + addr
	waitHealthy(t, base)

	req, err := http.NewRequest("POST", base+"/ingest/events",
		strings.NewReader(`{"key":"ak_test","events":[{"name":"$pageview","attributes":{"$host":"app.com","$path":"/pricing"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "https://app.com")
	req.Header.Set("User-Agent", secretUA)
	req.Header.Set("X-Forwarded-For", secretIP)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 202 {
		t.Fatalf("hit: %d", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not shut down")
	}

	// Scan the raw database bytes, not just the columns: this catches an IP
	// or UA smuggled into any field, index or free page.
	raw, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{secretIP, "UNIQUEMARKERSTRING", secretUA} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Errorf("database contains %q; it must never be persisted", secret)
		}
		if strings.Contains(logs.String(), secret) {
			t.Errorf("logs contain %q; it must never be logged", secret)
		}
	}
	// Sanity: the hit really was recorded, so the assertions above are not
	// passing merely because nothing was written.
	st, err := store.Open("sqlite://" + dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	days, err := st.ViewDaysBefore(context.Background(), 1, mustDay("2100-01-01"))
	if err != nil || len(days) != 1 {
		t.Fatalf("hit not persisted: %v %v", days, err)
	}
}

// runServeAndCollectLogs boots Serve, waits for /healthz, then shuts it
// down and returns everything logged in between.
func runServeAndCollectLogs(t *testing.T, cfg *config.Config) string {
	t.Helper()
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, cfg, logger, true, false) }()
	waitHealthy(t, "http://"+cfg.IngestAddr)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not shut down")
	}
	return logs.String()
}
