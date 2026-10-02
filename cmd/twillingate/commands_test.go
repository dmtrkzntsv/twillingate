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
		ingest, console                bool
		runIngest, runConsole, lenient bool
	}{
		{false, false, true, true, true}, // bare serve: both, lenient
		{true, false, true, false, false},
		{false, true, false, true, false},
		{true, true, true, true, false}, // explicit both: strict
	}
	for _, c := range cases {
		i, a, l := resolveSurfaces(c.ingest, c.console)
		if i != c.runIngest || a != c.runConsole || l != c.lenient {
			t.Errorf("resolveSurfaces(%v,%v) = %v,%v,%v; want %v,%v,%v",
				c.ingest, c.console, i, a, l, c.runIngest, c.runConsole, c.lenient)
		}
	}
}

// Explicitly requesting -console without auth config stays a hard error;
// bare serve degrades to a warning instead (exercised end to end by
// scripts/smoke.sh, which boots bare `serve` with no console config).
func TestExplicitConsoleWithoutConfigFails(t *testing.T) {
	withDB(t)
	var out bytes.Buffer
	if code := run([]string{"serve", "-console"}, &out); code != 1 {
		t.Fatalf("serve -console without CONSOLE_AUTH_DSN: exit %d, want 1: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "CONSOLE_AUTH_DSN") {
		t.Errorf("error must name the missing variable: %s", out.String())
	}
}

// Bare serve is lenient about a missing CONSOLE_AUTH_DSN, but a DSN that is set
// and fails to parse is a mistake, not an absence: it must still exit 1,
// even without -console naming the surface explicitly.
func TestBareServeFailsOnInvalidConsoleAuthDSN(t *testing.T) {
	withDB(t)
	t.Setenv("CONSOLE_AUTH_DSN", "token://x?password=p&resource=https://h.example.com/mcp")
	var out bytes.Buffer
	if code := run([]string{"serve"}, &out); code != 1 {
		t.Fatalf("bare serve with an invalid CONSOLE_AUTH_DSN: exit %d, want 1: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "CONSOLE_AUTH_DSN") {
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

// -api was renamed to -console; a leftover one in a unit or compose file
// must name its replacement rather than read as an unknown flag.
func TestRenamedAPIFlagNamesConsole(t *testing.T) {
	withDB(t)
	for _, args := range [][]string{
		{"serve", "-api"}, {"serve", "-ingest", "--api"}, {"serve", "-api=true"}, {"keygen", "-api"},
	} {
		var out bytes.Buffer
		if code := run(args, &out); code != 2 {
			t.Errorf("%v: exit %d, want 2: %s", args, code, out.String())
		}
		if !strings.Contains(out.String(), "-api was renamed to -console") {
			t.Errorf("%v: output %q, want it to name -console", args, out.String())
		}
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

func TestKeygenPrintsUsableKeys(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"keygen", "-n", "2"}, &out); code != 0 {
		t.Fatalf("exit code = %d, want 0 (%s)", code, out.String())
	}
	s := out.String()
	if !strings.Contains(s, "ak_") {
		t.Errorf("output has no ak_ prefixed key:\n%s", s)
	}
	if !strings.Contains(s, "data-key") || strings.Contains(s, "data-identity") {
		t.Errorf("output should include a ready-to-paste snippet without data-identity:\n%s", s)
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

func TestReportingDevRejectsNonLoopbackAddr(t *testing.T) {
	var out bytes.Buffer
	dir := t.TempDir()
	if code := run([]string{"reporting", "dev", dir, "-addr", "0.0.0.0:3100"}, &out); code != 2 {
		t.Fatalf("exit code = %d, want 2 (%s)", code, out.String())
	}
	if !strings.Contains(out.String(), "loopback") {
		t.Errorf("output must say why: %s", out.String())
	}
}

func TestReportingDevRequiresADirectory(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"reporting", "dev"}, &out); code != 2 {
		t.Fatalf("exit code = %d, want 2 (%s)", code, out.String())
	}
}

func TestReportingUnknownSubcommand(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"reporting", "bogus"}, &out); code != 2 {
		t.Fatalf("exit code = %d, want 2 (%s)", code, out.String())
	}
}

func TestReportingAppearsInUsage(t *testing.T) {
	var out bytes.Buffer
	run(nil, &out)
	if !strings.Contains(out.String(), "reporting") {
		t.Errorf("usage does not mention reporting: %s", out.String())
	}
}

func TestKeygenAppearsInUsage(t *testing.T) {
	var out bytes.Buffer
	run(nil, &out)
	if !strings.Contains(out.String(), "keygen") {
		t.Errorf("usage does not mention keygen: %s", out.String())
	}
}

func TestReportingDevRejectsUnknownFlagEvenAfterADir(t *testing.T) {
	var out bytes.Buffer
	dir := t.TempDir()
	if code := run([]string{"reporting", "dev", dir, "-nope"}, &out); code != 2 {
		t.Fatalf("exit code = %d, want 2 (%s)", code, out.String())
	}
}

func TestSplitFlagsAllowsFlagsAfterDirs(t *testing.T) {
	flagArgs, positional := splitFlags([]string{"d1", "d2", "-addr", "1.2.3.4:80", "-db", "x.db"}, "-db", "-addr")
	if strings.Join(positional, ",") != "d1,d2" {
		t.Errorf("positional = %v", positional)
	}
	if strings.Join(flagArgs, ",") != "-addr,1.2.3.4:80,-db,x.db" {
		t.Errorf("flagArgs = %v", flagArgs)
	}
}
