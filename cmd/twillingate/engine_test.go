package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEngineCheckSpawnsAChild(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATABASE_DSN", "sqlite://"+filepath.Join(dir, "twillingate.db"))
	var out strings.Builder
	if code := run([]string{"engine", "-check"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if !strings.HasPrefix(out.String(), "engine ok: 42 in ") || !strings.Contains(out.String(), "DuckDB v") {
		t.Errorf("output = %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "raw")); err != nil {
		t.Errorf("raw directory: %v", err)
	}
}

func TestEngineChildRefusesUnknownFlags(t *testing.T) {
	var out strings.Builder
	if code := run([]string{"engine", "-nope"}, &out); code != 1 || !strings.Contains(out.String(), `"err"`) {
		t.Fatalf("exit %d, output %q; want 1 and an error line", code, out.String())
	}
}
