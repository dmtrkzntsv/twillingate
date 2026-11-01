package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestKeyIssueListDisableEnable(t *testing.T) {
	withDB(t)
	var out bytes.Buffer
	var code int
	code = run([]string{"project", "create", "-name", "blog"}, &out)
	if code != 0 {
		t.Fatalf("create: %s", out.String())
	}
	out.Reset()
	code = run([]string{"key", "issue", "-project-id", "1", "-label", "web"}, &out)
	if code != 0 {
		t.Fatalf("issue: exit %d: %s", code, out.String())
	}
	if !regexp.MustCompile(`ak_[0-9a-f]{32}`).MatchString(out.String()) {
		t.Fatalf("no key in output: %s", out.String())
	}
	if !strings.Contains(out.String(), "twillingate.js") {
		t.Fatalf("no snippet in output: %s", out.String())
	}
	out.Reset()
	code = run([]string{"key", "list", "-project-id", "1"}, &out)
	if code != 0 || !strings.HasPrefix(out.String(), "1\tweb\tak_") {
		t.Fatalf("list: %s", out.String())
	}
	out.Reset()
	code = run([]string{"key", "disable", "-project-id", "1", "-label", "web"}, &out)
	if code != 0 {
		t.Fatalf("disable: %s", out.String())
	}
	out.Reset()
	code = run([]string{"key", "list", "-project-id", "1"}, &out)
	if !strings.Contains(out.String(), "disabled") {
		t.Fatalf("list after disable: %s", out.String())
	}
	out.Reset()
	code = run([]string{"key", "enable", "-project-id", "1", "-label", "web"}, &out)
	if code != 0 {
		t.Fatalf("enable: %s", out.String())
	}
}

// An unknown key subcommand is a usage error, refused before the store is
// opened: it neither creates nor migrates the database.
func TestKeyUnknownSubcommandTouchesNoDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "never.db")
	setEnv(t, dbPath)
	var out bytes.Buffer
	if code := run([]string{"key", "lst", "-project-id", "1"}, &out); code != 2 {
		t.Fatalf("exit %d, want 2: %s", code, out.String())
	}
	if !strings.Contains(out.String(), `unknown subcommand "lst"`) {
		t.Fatalf("output: %q", out.String())
	}
	if _, err := os.Stat(dbPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("database touched: stat err = %v", err)
	}
}

func TestKeygenConsole(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"keygen", "-console"}, &out); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !regexp.MustCompile(`CONSOLE_AUTH_DSN=token://ar_[0-9a-f]{64}`).MatchString(out.String()) {
		t.Fatalf("output: %s", out.String())
	}
	if strings.Contains(out.String(), "ingest_keys") {
		t.Fatal("-console must not print the JSON block")
	}
}

func TestKeygenDeprecationNotice(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"keygen"}, &out); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), "key issue") {
		t.Fatalf("no deprecation pointer: %s", out.String())
	}
}
