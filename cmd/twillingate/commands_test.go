package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setEnv points the process environment at a scratch database; commands
// read config from the environment only. The project list lives in the
// registry (that database), not in an env-named file.
func setEnv(t *testing.T, dbPath string) {
	t.Helper()
	t.Setenv("DATABASE_DSN", "sqlite://"+dbPath)
}

func TestMigrateSubcommand(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "m.db")
	setEnv(t, dbPath)
	var out bytes.Buffer
	if code := run([]string{"migrate"}, &out); code != 0 {
		t.Fatalf("exit code = %d, want 0 (%s)", code, out.String())
	}
	if !strings.Contains(out.String(), "migrations applied") {
		t.Errorf("output = %q", out.String())
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("database not created: %v", err)
	}
	// Re-running must stay clean.
	out.Reset()
	if code := run([]string{"migrate"}, &out); code != 0 {
		t.Fatalf("second migrate exit = %d (%s)", code, out.String())
	}
}

// A missing or unusable configuration must fail loudly rather than starting
// with defaults. serve needs a surface flag to get past the usage check and
// reach config loading at all.
func TestSubcommandsRejectBadConfig(t *testing.T) {
	argsFor := map[string][]string{"serve": {"serve", "-ingest"}, "migrate": {"migrate"}}
	for _, cmd := range []string{"serve", "migrate"} {
		t.Run(cmd+" missing DATABASE_DSN", func(t *testing.T) {
			t.Setenv("DATABASE_DSN", "")
			var out bytes.Buffer
			if code := run(argsFor[cmd], &out); code != 1 {
				t.Fatalf("exit code = %d, want 1", code)
			}
			if out.Len() == 0 {
				t.Error("want an error message on stdout")
			}
		})
		t.Run(cmd+" bad flag", func(t *testing.T) {
			var out bytes.Buffer
			if code := run([]string{cmd, "-nope"}, &out); code != 2 {
				t.Fatalf("exit code = %d, want 2", code)
			}
		})
	}
}

func TestResolveSurfaces(t *testing.T) {
	cases := []struct {
		ingest, api                bool
		runIngest, runAPI, lenient bool
	}{
		{false, false, true, true, true}, // bare serve: both, lenient
		{true, false, true, false, false},
		{false, true, false, true, false},
		{true, true, true, true, false}, // explicit both: strict
	}
	for _, c := range cases {
		i, a, l := resolveSurfaces(c.ingest, c.api)
		if i != c.runIngest || a != c.runAPI || l != c.lenient {
			t.Errorf("resolveSurfaces(%v,%v) = %v,%v,%v; want %v,%v,%v",
				c.ingest, c.api, i, a, l, c.runIngest, c.runAPI, c.lenient)
		}
	}
}

// Explicitly requesting -api without auth config stays a hard error;
// bare serve degrades to a warning instead (exercised end to end by
// scripts/smoke.sh, which boots bare `serve` with no API config).
func TestExplicitAPIWithoutConfigFails(t *testing.T) {
	withDB(t)
	var out bytes.Buffer
	if code := run([]string{"serve", "-api"}, &out); code != 1 {
		t.Fatalf("serve -api without API_AUTH_DSN: exit %d, want 1: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "API_AUTH_DSN") {
		t.Errorf("error must name the missing variable: %s", out.String())
	}
}

// Bare serve is lenient about a missing API_AUTH_DSN, but a DSN that is set
// and fails to parse is a mistake, not an absence: it must still exit 1,
// even without -api naming the surface explicitly.
func TestBareServeFailsOnInvalidAPIAuthDSN(t *testing.T) {
	withDB(t)
	t.Setenv("API_AUTH_DSN", "token://x?password=p&resource=https://h.example.com/mcp")
	var out bytes.Buffer
	if code := run([]string{"serve"}, &out); code != 1 {
		t.Fatalf("bare serve with an invalid API_AUTH_DSN: exit %d, want 1: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "API_AUTH_DSN") {
		t.Errorf("error must name the bad variable: %s", out.String())
	}
}

// The old -mcp flag is gone; serve must reject it as unknown rather than
// silently accepting it as a no-op.
func TestServeRejectsRemovedMCPFlag(t *testing.T) {
	withDB(t)
	var out bytes.Buffer
	if code := run([]string{"serve", "-mcp"}, &out); code != 2 {
		t.Fatalf("serve -mcp: exit %d, want 2 (unknown flag): %s", code, out.String())
	}
}

func TestMigrateRejectsBadDSN(t *testing.T) {
	setEnv(t, "unused.db")
	t.Setenv("DATABASE_DSN", "bogus://nope")
	var out bytes.Buffer
	if code := run([]string{"migrate"}, &out); code != 1 {
		t.Fatalf("exit code = %d, want 1 (%s)", code, out.String())
	}
}

// dashboards renders whatever database it is pointed at; unlike serve and
// migrate it must start without a project list.
func TestDashboardsRejectsMissingDatabase(t *testing.T) {
	t.Setenv("DATABASE_DSN", "")
	t.Setenv("DASHBOARDS_DB_PATH", "")
	var out bytes.Buffer
	if code := run([]string{"dashboards"}, &out); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(out.String(), "DASHBOARDS_DB_PATH") {
		t.Errorf("error = %q, want it to name the missing setting", out.String())
	}
}

func TestKeygenPrintsUsableKeys(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"keygen", "-n", "2"}, &out); code != 0 {
		t.Fatalf("exit code = %d, want 0 (%s)", code, out.String())
	}
	s := out.String()
	if !strings.Contains(s, "ak_") {
		t.Errorf("output has no ak_ prefixed key:\n%s", s)
	}
	if !strings.Contains(s, "data-key") || !strings.Contains(s, "data-identity") {
		t.Errorf("output should include a ready-to-paste snippet:\n%s", s)
	}
	if !strings.Contains(s, "ingest_keys") {
		t.Errorf("output should include the projects.json fragment:\n%s", s)
	}
	if got := strings.Count(s, `"key":`); got != 2 {
		t.Errorf("asked for 2 keys, got %d:\n%s", got, s)
	}
}

func TestKeygenKeysAreUniqueAndWellFormed(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"keygen", "-n", "8"}, &out); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	seen := map[string]bool{}
	for _, f := range strings.Fields(out.String()) {
		f = strings.Trim(f, `",`)
		if !strings.HasPrefix(f, "ak_") {
			continue
		}
		if seen[f] {
			t.Fatalf("duplicate key %q", f)
		}
		// 16 random bytes hex-encoded, plus the "ak_" prefix.
		if len(f) != 3+32 {
			t.Errorf("key %q has length %d, want %d", f, len(f), 3+32)
		}
		seen[f] = true
	}
	if len(seen) != 8 {
		t.Errorf("got %d distinct keys, want 8", len(seen))
	}
}

func TestKeygenRejectsBadCount(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"keygen", "-n", "0"}, &out); code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
}

func TestKeygenAppearsInUsage(t *testing.T) {
	var out bytes.Buffer
	run(nil, &out)
	if !strings.Contains(out.String(), "keygen") {
		t.Errorf("usage does not mention keygen: %s", out.String())
	}
}
