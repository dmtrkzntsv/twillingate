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

func TestMonitorRoundTrip(t *testing.T) {
	withDB(t)
	var out bytes.Buffer
	if code := run([]string{"project", "create", "-name", "Shop", "-origin", "https://shop.example.com",
		"-origin", "https://*.example.org"}, &out); code != 0 {
		t.Fatal(out.String())
	}
	if got := runMonitor(t, 0, "get", "-project-id", "1"); !strings.Contains(got, "disabled") || !strings.Contains(got, "https://shop.example.com") || !strings.Contains(got, "timeout\t5 seconds\n") {
		t.Errorf("get = %q, want disabled, the eligible origin listed and the 5 second timeout", got)
	}
	got := runMonitor(t, 0, "set", "-project-id", "1", "-enable", "-contains", "Welcome")
	for _, want := range []string{"enabled", "https://shop.example.com", "Welcome"} {
		if !strings.Contains(got, want) {
			t.Errorf("set = %q, missing %q", got, want)
		}
	}
	if got := runMonitor(t, 0, "set", "-project-id", "1", "-disable", "-contains", ""); !strings.Contains(got, "disabled") || strings.Contains(got, "Welcome") {
		t.Errorf("disable = %q, want disabled with the text cleared", got)
	}
	m, err := openStore(t).GetMonitor(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if m.Enabled || m.Origin != "https://shop.example.com" || m.Contains != "" {
		t.Errorf("stored = %+v", m)
	}
}

func TestMonitorRefusals(t *testing.T) {
	withDB(t)
	var out bytes.Buffer
	if code := run([]string{"project", "create", "-name", "Shop", "-origin", "https://shop.example.com"}, &out); code != 0 {
		t.Fatal(out.String())
	}
	runMonitor(t, 2)                                                            // no subcommand
	runMonitor(t, 2, "get")                                                     // no project
	runMonitor(t, 2, "set", "-project-id", "1", "-enable", "-disable")          // both
	runMonitor(t, 2, "set", "-project-id", "1", "-enable=false", "-disable")    // both, though they agree
	runMonitor(t, 1, "set", "-project-id", "1", "-origin", "https://evil.test") // foreign
	runMonitor(t, 1, "get", "-project-id", "9")                                 // unknown
}

// A flag given with a value says what it says: the given flags decide.
func TestMonitorFlagValues(t *testing.T) {
	withDB(t)
	var out bytes.Buffer
	if code := run([]string{"project", "create", "-name", "Shop", "-origin", "https://shop.example.com"}, &out); code != 0 {
		t.Fatal(out.String())
	}
	for _, c := range []struct {
		flag string
		want string
	}{
		{"-disable=false", "monitor enabled"}, {"-enable=false", "monitor disabled"},
		{"-enable=true", "monitor enabled"}, {"-disable=true", "monitor disabled"},
	} {
		if got := runMonitor(t, 0, "set", "-project-id", "1", c.flag); !strings.Contains(got, c.want) {
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
		printMonitor(&out, c.m, nil, 0)
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
		printMonitor(&out, store.Monitor{Origin: "https://a.example", Redirects: c.n}, nil, 0)
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
			m, err := st.GetMonitor(context.Background(), 1)
			if err != nil {
				t.Error(err)
				return
			}
			if m.CheckRequestedAt > m.CheckFinishedAt {
				res.FinishedAt = m.CheckRequestedAt
				if err := st.FinishMonitorCheck(context.Background(), 1, m.Origin, res); err != nil {
					t.Error(err)
				}
				return
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
	if code := run([]string{"project", "create", "-name", "Shop", "-origin", "https://shop.example.com"}, &out); code != 0 {
		t.Fatal(out.String())
	}
	runMonitor(t, 2, "check") // no project
	if got := runMonitor(t, 1, "check", "-project-id", "1"); !strings.Contains(got, "turn the monitor on first") {
		t.Errorf("not on = %q", got)
	}
	runMonitor(t, 0, "set", "-project-id", "1", "-enable")

	serveMonitorCheck(t, store.MonitorCheckResult{Up: true, Status: 200, MS: 123})
	if got := runMonitor(t, 0, "check", "-project-id", "1"); !strings.Contains(got, "up, 200, 123 ms") {
		t.Errorf("check = %q", got)
	}
	st := openStore(t)
	m, err := st.GetMonitor(context.Background(), 1)
	if err != nil || m.CheckFinishedAt == "" {
		t.Fatalf("stored %+v, %v", m, err)
	}
	if got := runMonitor(t, 0, "get", "-project-id", "1"); !strings.Contains(got, "last manual check\tup, 200, 123 ms") {
		t.Errorf("get = %q", got)
	}
	// The cooldown is the server's refusal, printed.
	if got := runMonitor(t, 1, "check", "-project-id", "1"); !strings.Contains(got, "try again in") {
		t.Errorf("cooldown = %q", got)
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
			if code := run([]string{"project", "create", "-name", "Shop", "-origin", "https://shop.example.com"}, &out); code != 0 {
				t.Fatal(out.String())
			}
			runMonitor(t, 0, "set", "-project-id", "1", "-enable")
			serveMonitorCheck(t, c.res)
			if got := runMonitor(t, c.code, "check", "-project-id", "1"); !strings.Contains(got, c.want) {
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
	if code := run([]string{"project", "create", "-name", "Shop", "-origin", "https://shop.example.com"}, &out); code != 0 {
		t.Fatal(out.String())
	}
	runMonitor(t, 0, "set", "-project-id", "1", "-enable")
	ops, _, closeStore, err := openOps("")
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore()
	out.Reset()
	if code := monitorCheck(context.Background(), ops, 1, 30*time.Millisecond, &out); code != 1 {
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
