package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive/archivetest"
	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/sqlengine"
)

// socketDir is a directory for a test's socket, short enough for
// sun_path, removed with t.
func socketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(filepath.Join(os.Getenv("HOME"), ".cache"), "app-sqle-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// fakeHealth serves GET /v1/health on a socket with protocol, until t ends.
func fakeHealth(t *testing.T, protocol int) string {
	t.Helper()
	sock := filepath.Join(socketDir(t), "e.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(sqlengine.Health{Protocol: protocol, Engine: "fake 9"})
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return sock
}

// TestOpenSQL: no socket is no engine; an engine speaking another
// protocol fails the start; one that does not answer is logged and kept,
// each query trying it again.
func TestOpenSQL(t *testing.T) {
	ctx := context.Background()
	var out bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&out, nil))
	if c, err := openSQL(ctx, "", logger); c != nil || err != nil {
		t.Errorf("no socket = %v, %v; want no client", c, err)
	}

	c, err := openSQL(ctx, fakeHealth(t, sqlengine.Protocol+1), logger)
	if c != nil || !errors.Is(err, errSQLEngineProtocol) || !strings.Contains(err.Error(), "fake 9") ||
		!strings.Contains(err.Error(), "SQL_ENGINE_SOCKET") {
		t.Errorf("another protocol = %v, %v; want the start refused, naming the engine and the variable", c, err)
	}

	out.Reset()
	c, err = openSQL(ctx, filepath.Join(socketDir(t), "missing.sock"), logger)
	if c == nil || err != nil {
		t.Fatalf("unreachable = %v, %v; want a client and no error", c, err)
	}
	c.Close()
	if !strings.Contains(out.String(), "level=WARN") || !strings.Contains(out.String(), "does not answer") {
		t.Errorf("logged %q, want a warning", out.String())
	}

	out.Reset()
	c, err = openSQL(ctx, fakeHealth(t, sqlengine.Protocol), logger)
	if c == nil || err != nil || !strings.Contains(out.String(), "engine=\"fake 9\"") {
		t.Errorf("a matching engine = %v, %v, logged %q; want it named", c, err, out.String())
	}
	if c != nil {
		c.Close()
	}
}

// TestServeRefusesAnotherProtocol: a start whose engine speaks another
// protocol stops before anything listens.
func TestServeRefusesAnotherProtocol(t *testing.T) {
	addr := freePort(t)
	cfg := testConfig(t, addr, filepath.Join(t.TempDir(), "proto.db"))
	cfg.SQLEngineSocket = fakeHealth(t, sqlengine.Protocol+1)
	err := Serve(context.Background(), cfg, slog.New(slog.DiscardHandler), true, false)
	if !errors.Is(err, errSQLEngineProtocol) {
		t.Fatalf("serve = %v, want the protocol mismatch", err)
	}
	if err := Migrate(context.Background(), cfg, nil); !errors.Is(err, errSQLEngineProtocol) {
		t.Errorf("migrate = %v, want the protocol mismatch", err)
	}
}

// TestServeAnswersSQLThroughTheEngine: the console's query tool answers
// sql through the engine at SQL_ENGINE_SOCKET, refuses it with none, and
// starts with one that does not answer, refusing sql until it does.
func TestServeAnswersSQLThroughTheEngine(t *testing.T) {
	serve := func(t *testing.T, socket func(cfg *config.Config) string) string {
		t.Helper()
		addr := freePort(t)
		cfg := apiTestConfig(t, addr, filepath.Join(t.TempDir(), "sql.db"), "")
		writeEvent(t, cfg.EventsDir)
		cfg.SQLEngineSocket = socket(cfg)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- Serve(ctx, cfg, slog.New(slog.DiscardHandler), true, true) }()
		t.Cleanup(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("serve: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Error("serve did not shut down")
			}
		})
		waitHealthy(t, "http://"+addr)
		return addr
	}
	query := func(t *testing.T, addr string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest("POST", "http://"+addr+"/api/query", strings.NewReader(`{"sql":"SELECT count(*) AS n FROM events"}`))
		req.Header.Set("Authorization", "Bearer ar_apptest")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	t.Run("engine", func(t *testing.T) {
		var eng *archivetest.SQLEngine
		addr := serve(t, func(cfg *config.Config) string {
			e, _, err := openFiles(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { e.Close() })
			eng = archivetest.ServeSQL(t, cfg.EventsDir, e)
			return eng.Socket
		})
		if code, body := query(t, addr); code != 200 || !strings.Contains(body, `"rows":[["1"]]`) {
			t.Errorf("query = %d %s, want the one event counted", code, body)
		}
		reqs := eng.Requests()
		if len(reqs) == 0 || !reqs[len(reqs)-1].Confine || reqs[len(reqs)-1].Scope != nil {
			t.Errorf("the engine got %+v, want the query confined over every project", reqs)
		}
	})
	t.Run("none", func(t *testing.T) {
		addr := serve(t, func(*config.Config) string { return "" })
		if code, body := query(t, addr); code != 400 || !strings.Contains(body, "SQL needs an external SQL engine (SQL_ENGINE_SOCKET)") {
			t.Errorf("query = %d %s, want 400 refused", code, body)
		}
	})
	t.Run("down", func(t *testing.T) {
		addr := serve(t, func(*config.Config) string { return filepath.Join(socketDir(t), "missing.sock") })
		if code, body := query(t, addr); code != 400 || !strings.Contains(body, "the SQL engine failed") {
			t.Errorf("query = %d %s, want 400 naming the failed engine", code, body)
		}
	})
}
