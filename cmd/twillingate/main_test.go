package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// asBinary makes this test binary act as twillingate when a test spawns it
// as its own child: `engine -check` runs `<self> engine …`.
const asBinary = "TWILLINGATE_TEST_AS_BINARY"

func TestMain(m *testing.M) {
	if os.Getenv(asBinary) == "1" {
		os.Exit(run(os.Args[1:], os.Stdout))
	}
	os.Exit(m.Run())
}

func TestVersionSubcommand(t *testing.T) {
	var out bytes.Buffer
	code := run([]string{"version"}, &out)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.HasPrefix(out.String(), "twillingate ") {
		t.Fatalf("output = %q, want prefix 'twillingate '", out.String())
	}
}

func TestUnknownSubcommand(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"bogus"}, &out); code == 0 {
		t.Fatal("unknown subcommand must return non-zero")
	}
}

func TestNoSubcommandPrintsUsage(t *testing.T) {
	var out bytes.Buffer
	if code := run(nil, &out); code == 0 {
		t.Fatal("missing subcommand must return non-zero")
	}
	if !strings.Contains(out.String(), "usage") {
		t.Fatalf("output %q must contain usage", out.String())
	}
	// The mode list is the CLI's contract; `sync` was removed with the
	// application's replication support, `dashboards` with Evidence.
	if got := out.String(); !strings.Contains(got, "reporting") || strings.Contains(got, "sync") || strings.Contains(got, "dashboards") {
		t.Fatalf("usage = %q, want reporting listed and sync and dashboards gone", got)
	}
}
