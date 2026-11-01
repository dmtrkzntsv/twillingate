package main

import (
	"bytes"
	"strings"
	"testing"
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
	if got := runMonitor(t, 0, "get", "-project-id", "1"); !strings.Contains(got, "disabled") || !strings.Contains(got, "https://shop.example.com") {
		t.Errorf("get = %q, want disabled and the eligible origin listed", got)
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
	runMonitor(t, 1, "set", "-project-id", "1", "-origin", "https://evil.test") // foreign
	runMonitor(t, 1, "get", "-project-id", "9")                                 // unknown
}
