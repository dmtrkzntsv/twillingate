package config

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

// The shipped env examples must stay loadable; a typo here breaks every
// fresh install, since install.sh copies .env.example to /etc/twillingate/
// and `ship install` deploy/ship/twillingate.env.example to /etc/<service>/.
// Each is parsed the way systemd's EnvironmentFile= reads it: KEY=VALUE lines, with
// commented lines documenting defaults — those are uncommented here so
// every documented key round-trips through FromEnv.
func TestExamplesLoad(t *testing.T) {
	for _, path := range []string{"../../.env.example", "../../deploy/ship/twillingate.env.example"} {
		t.Run(path, func(t *testing.T) { exampleLoads(t, path) })
	}
}

func exampleLoads(t *testing.T, path string) {
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	vars := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		line = strings.TrimPrefix(line, "#")
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.ContainsAny(k, " \t") {
			continue // prose comment, not a documented default
		}
		vars[k] = v
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"DATABASE_DSN", "INGEST_ADDR", "BUFFER_FLUSH_INTERVAL", "REPORTING_CACHE_MAX_AGE"} {
		if _, ok := vars[key]; !ok {
			t.Fatalf("example env must document %s", key)
		}
	}
	cfg, err := FromEnv(func(k string) (string, bool) { v, ok := vars[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Reporting.CacheAge.Minutes() != 15 {
		t.Errorf("reporting cache age = %v", cfg.Reporting.CacheAge)
	}
	if cfg.Retention.Days != 400 {
		t.Errorf("retention = %+v", cfg.Retention)
	}
}
