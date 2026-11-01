package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func runMonitor(t *testing.T, want int, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	if code := run(append([]string{"monitor"}, args...), &out); code != want {
		t.Fatalf("monitor %v: exit %d, want %d: %s", args, code, want, out.String())
	}
	return out.String()
}

const (
	shopOrigin = "https://shop.example.com"
	blogOrigin = "https://blog.example.com"
)

// storedMonitor is the row of origin in project 1.
func storedMonitor(t *testing.T, origin string) store.Monitor {
	t.Helper()
	ms, err := openStore(t).ListMonitors(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.Origin == origin {
			return m
		}
	}
	t.Fatalf("no row for %s in %+v", origin, ms)
	return store.Monitor{}
}

func TestMonitorRoundTrip(t *testing.T) {
	withDB(t)
	var out bytes.Buffer
	if code := run([]string{"project", "create", "-name", "Shop", "-origin", shopOrigin, "-origin", blogOrigin,
		"-origin", "https://*.example.org"}, &out); code != 0 {
		t.Fatal(out.String())
	}
	got := runMonitor(t, 0, "list", "-project-id", "1")
	for _, want := range []string{shopOrigin + " disabled\n", blogOrigin + " disabled\n", "timeout\t5 seconds\n", "each site checked every\t120 seconds\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("list = %q, missing %q", got, want)
		}
	}
	if strings.Contains(got, "*") || strings.Contains(got, "domain") {
		t.Errorf("list = %q, want no wildcard and no domain with nothing on", got)
	}
	got = runMonitor(t, 0, "set", "-project-id", "1", "-origin", shopOrigin, "-enable", "-contains", "Welcome")
	for _, want := range []string{shopOrigin + " enabled\n", blogOrigin + " disabled\n", "contains\tWelcome\n", "domain example.com\texpires unknown, checked unknown, origins " + shopOrigin + "\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("set = %q, missing %q", got, want)
		}
	}
	got = runMonitor(t, 0, "set", "-project-id", "1", "-origin", blogOrigin, "-enable")
	for _, want := range []string{"each site checked every\t240 seconds\n", "origins " + shopOrigin + ", " + blogOrigin + "\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("second set = %q, missing %q", got, want)
		}
	}
	if got := runMonitor(t, 0, "set", "-project-id", "1", "-origin", shopOrigin, "-disable", "-contains", ""); !strings.Contains(got, shopOrigin+" disabled") || strings.Contains(got, "Welcome") {
		t.Errorf("disable = %q, want disabled with the text cleared", got)
	}
	if m := storedMonitor(t, shopOrigin); m.Enabled || m.Contains != "" {
		t.Errorf("stored = %+v", m)
	}
	if m := storedMonitor(t, blogOrigin); !m.Enabled {
		t.Errorf("stored = %+v", m)
	}
}

func TestMonitorRefusals(t *testing.T) {
	withDB(t)
	var out bytes.Buffer
	if code := run([]string{"project", "create", "-name", "Shop", "-origin", shopOrigin}, &out); code != 0 {
		t.Fatal(out.String())
	}
	runMonitor(t, 2)                                                                                // no subcommand
	runMonitor(t, 2, "get", "-project-id", "1")                                                     // gone
	runMonitor(t, 2, "list")                                                                        // no project
	runMonitor(t, 2, "set", "-project-id", "1", "-enable")                                          // no origin
	runMonitor(t, 2, "check", "-project-id", "1")                                                   // no origin
	runMonitor(t, 2, "set", "-project-id", "1", "-origin", shopOrigin, "-enable", "-disable")       // both
	runMonitor(t, 2, "set", "-project-id", "1", "-origin", shopOrigin, "-enable=false", "-disable") // both, though they agree
	runMonitor(t, 1, "set", "-project-id", "1", "-origin", "https://evil.test", "-enable")          // foreign
	runMonitor(t, 1, "list", "-project-id", "9")                                                    // unknown
}

// A flag given with a value says what it says: the given flags decide.
func TestMonitorFlagValues(t *testing.T) {
	withDB(t)
	var out bytes.Buffer
	if code := run([]string{"project", "create", "-name", "Shop", "-origin", shopOrigin}, &out); code != 0 {
		t.Fatal(out.String())
	}
	for _, c := range []struct {
		flag string
		want string
	}{
		{"-disable=false", shopOrigin + " enabled"}, {"-enable=false", shopOrigin + " disabled"},
		{"-enable=true", shopOrigin + " enabled"}, {"-disable=true", shopOrigin + " disabled"},
	} {
		if got := runMonitor(t, 0, "set", "-project-id", "1", "-origin", shopOrigin, c.flag); !strings.Contains(got, c.want) {
			t.Errorf("%s = %q, want %q", c.flag, got, c.want)
		}
	}
}

func TestPrintMonitorCertificate(t *testing.T) {
	const at = "2026-11-01T00:00:00Z"
	for _, c := range []struct {
		name string
		m    store.Monitor
		want string // "" when no certificate line is printed
	}{
		{"read", store.Monitor{Origin: "https://a.example", CertExpiresAt: "2027-01-01T00:00:00Z", CertIssuer: "R3", CertValid: true, CertCheckedAt: at},
			"certificate\texpires 2027-01-01T00:00:00Z, issuer R3, valid true, checked " + at + "\n"},
		{"never read, failed", store.Monitor{Origin: "https://a.example", CertError: "refused"},
			"certificate\texpires unknown, issuer unknown, valid false, checked unknown, error refused\n"},
		{"read, last check failed", store.Monitor{Origin: "https://a.example", CertExpiresAt: "2027-01-01T00:00:00Z", CertIssuer: "R3", CertValid: true, CertCheckedAt: at, CertError: "refused"},
			"certificate\texpires 2027-01-01T00:00:00Z, issuer R3, valid true, checked " + at + ", error refused\n"},
		{"never read", store.Monitor{Origin: "https://a.example"}, ""},
		{"plain http", store.Monitor{Origin: "http://a.example", CertCheckedAt: at}, ""},
	} {
		var out bytes.Buffer
		printMonitor(&out, c.m)
		got := out.String()
		if c.want == "" {
			if strings.Contains(got, "certificate") {
				t.Errorf("%s: printed %q, want no certificate line", c.name, got)
			}
			continue
		}
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: printed %q, want %q", c.name, got, c.want)
		}
	}
}

func TestPrintMonitorRedirects(t *testing.T) {
	for _, c := range []struct {
		n    int
		want string
	}{{0, ""}, {2, "redirects\t2\n"}} {
		var out bytes.Buffer
		printMonitor(&out, store.Monitor{Origin: "https://a.example", Redirects: c.n})
		got := out.String()
		if c.want == "" && strings.Contains(got, "redirects") {
			t.Errorf("%d: printed %q, want no redirects line", c.n, got)
		}
		if c.want != "" && !strings.Contains(got, c.want) {
			t.Errorf("%d: printed %q, want %q", c.n, got, c.want)
		}
	}
}

// serveMonitorCheck plays the server's runner: it waits for the request and
// finishes it.
func serveMonitorCheck(t *testing.T, res store.MonitorCheckResult) {
	t.Helper()
	st := openStore(t)
	done := make(chan struct{})
	t.Cleanup(func() { <-done })
	go func() {
		defer close(done)
		for range 200 {
			ms, err := st.ListMonitors(context.Background(), 1)
			if err != nil {
				t.Error(err)
				return
			}
			for _, m := range ms {
				if m.CheckRequestedAt > m.CheckFinishedAt {
					res.FinishedAt = m.CheckRequestedAt
					if err := st.FinishMonitorCheck(context.Background(), 1, m.Origin, res); err != nil {
						t.Error(err)
					}
					return
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Error("no request came")
	}()
}

func TestMonitorCheckRoundTrip(t *testing.T) {
	withDB(t)
	defer func(p time.Duration) { monitorCheckPoll = p }(monitorCheckPoll)
	monitorCheckPoll = 10 * time.Millisecond
	var out bytes.Buffer
	if code := run([]string{"project", "create", "-name", "Shop", "-origin", shopOrigin, "-origin", blogOrigin}, &out); code != 0 {
		t.Fatal(out.String())
	}
	runMonitor(t, 2, "check") // no project
	check := []string{"check", "-project-id", "1", "-origin", shopOrigin}
	if got := runMonitor(t, 1, check...); !strings.Contains(got, "turn the monitor on first") {
		t.Errorf("not on = %q", got)
	}
	runMonitor(t, 0, "set", "-project-id", "1", "-origin", shopOrigin, "-enable")
	runMonitor(t, 0, "set", "-project-id", "1", "-origin", blogOrigin, "-enable")

	serveMonitorCheck(t, store.MonitorCheckResult{Up: true, Status: 200, MS: 123})
	if got := runMonitor(t, 0, check...); !strings.Contains(got, "up, 200, 123 ms") {
		t.Errorf("check = %q", got)
	}
	if m := storedMonitor(t, shopOrigin); m.CheckFinishedAt == "" {
		t.Fatalf("stored %+v", m)
	}
	if m := storedMonitor(t, blogOrigin); m.CheckRequestedAt != "" {
		t.Fatalf("the other monitor was asked too: %+v", m)
	}
	if got := runMonitor(t, 0, "list", "-project-id", "1"); !strings.Contains(got, "last manual check\tup, 200, 123 ms") {
		t.Errorf("list = %q", got)
	}
	// The cooldown is the server's refusal, printed; another monitor has its own.
	if got := runMonitor(t, 1, check...); !strings.Contains(got, "try again in") {
		t.Errorf("cooldown = %q", got)
	}
	serveMonitorCheck(t, store.MonitorCheckResult{Up: true, Status: 201, MS: 1})
	if got := runMonitor(t, 0, "check", "-project-id", "1", "-origin", blogOrigin+"/"); !strings.Contains(got, "up, 201") {
		t.Errorf("the other monitor, spelled with a slash = %q", got)
	}
}

func TestMonitorCheckDownAndNotRun(t *testing.T) {
	defer func(p time.Duration) { monitorCheckPoll = p }(monitorCheckPoll)
	monitorCheckPoll = 10 * time.Millisecond
	for _, c := range []struct {
		name string
		res  store.MonitorCheckResult
		code int
		want string
	}{
		{"down", store.MonitorCheckResult{Reason: "content", Error: "text not found", Status: 200, MS: 9}, 0, "down, content: text not found"},
		{"not run", store.MonitorCheckResult{Error: "the check did not run"}, 1, "the check did not run"},
	} {
		t.Run(c.name, func(t *testing.T) {
			withDB(t)
			var out bytes.Buffer
			if code := run([]string{"project", "create", "-name", "Shop", "-origin", shopOrigin}, &out); code != 0 {
				t.Fatal(out.String())
			}
			runMonitor(t, 0, "set", "-project-id", "1", "-origin", shopOrigin, "-enable")
			serveMonitorCheck(t, c.res)
			if got := runMonitor(t, c.code, "check", "-project-id", "1", "-origin", shopOrigin); !strings.Contains(got, c.want) {
				t.Errorf("check = %q, want %q", got, c.want)
			}
		})
	}
}

// The wait covers the longest check: availability and certificate at
// MONITOR_TIMEOUT each, the RDAP lookup's 15 s, and a tick's margin.
func TestMonitorCheckWait(t *testing.T) {
	for timeout, want := range map[time.Duration]time.Duration{
		5 * time.Second:  30 * time.Second,
		60 * time.Second: 140 * time.Second,
	} {
		if got := monitorCheckWait(timeout); got != want {
			t.Errorf("wait at a %v timeout = %v, want %v", timeout, got, want)
		}
	}
}

// Out of time, it exits 1, saying the check is still running: a slow check is
// not a server that is down.
func TestMonitorCheckDidNotFinish(t *testing.T) {
	withDB(t)
	defer func(p time.Duration) { monitorCheckPoll = p }(monitorCheckPoll)
	monitorCheckPoll = 5 * time.Millisecond
	var out bytes.Buffer
	if code := run([]string{"project", "create", "-name", "Shop", "-origin", shopOrigin}, &out); code != 0 {
		t.Fatal(out.String())
	}
	runMonitor(t, 0, "set", "-project-id", "1", "-origin", shopOrigin, "-enable")
	ops, _, closeStore, err := openOps("")
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore()
	out.Reset()
	if code := monitorCheck(context.Background(), ops, 1, shopOrigin, 30*time.Millisecond, &out); code != 1 {
		t.Errorf("exit %d, want 1: %s", code, out.String())
	}
	if got := out.String(); !strings.Contains(got, "still running") || strings.Contains(got, "server") {
		t.Errorf("out of time = %q, want it still running and no word of the server", got)
	}
}

// A request past the runner's 2-minute drop that is still pending was seen by
// no runner: that is when the server is worth asking about.
func TestMonitorCheckNotFinishedText(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	at := func(ago time.Duration) string { return now.Add(-ago).Format(time.RFC3339) }
	if got := monitorCheckNotFinished(at(2*time.Minute), now); !strings.Contains(got, "still running") {
		t.Errorf("at 2 minutes = %q", got)
	}
	if got := monitorCheckNotFinished(at(2*time.Minute+time.Second), now); !strings.Contains(got, "is the server up") {
		t.Errorf("past 2 minutes = %q", got)
	}
}
