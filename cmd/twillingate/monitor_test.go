package main

import (
	"bytes"
	"strings"
	"testing"

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
