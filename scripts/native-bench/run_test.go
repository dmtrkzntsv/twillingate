package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestQueryCommandPrintsOneJSONLine(t *testing.T) {
	files := monthFiles(t, duck(t))
	for _, src := range []string{"arrow", "pqgo"} {
		for _, q := range []string{"countries", "dau", "attrvalues", "attrvalues-approx"} {
			var out bytes.Buffer
			args := append([]string{"-src", src, "-q", q, "-from", "2026-08-01", "-to", "2026-09-30"}, files...)
			if err := queryCmd(args, &out); err != nil {
				t.Fatalf("%s %s: %v", src, q, err)
			}
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			if len(lines) != 1 {
				t.Fatalf("%s %s: want one line, got %q", src, q, out.String())
			}
			var got struct {
				WallMS    *float64 `json:"wall_ms"`
				UserMS    *float64 `json:"user_ms"`
				SysMS     *float64 `json:"sys_ms"`
				PeakRSSKB int64    `json:"peak_rss_kb"`
				Rows      int      `json:"rows"`
			}
			dec := json.NewDecoder(strings.NewReader(lines[0]))
			if err := dec.Decode(&got); err != nil {
				t.Fatalf("%s %s: %v in %q", src, q, err, lines[0])
			}
			if got.WallMS == nil || got.UserMS == nil || got.SysMS == nil {
				t.Fatalf("%s %s: missing timing fields in %q", src, q, lines[0])
			}
			if got.Rows <= 0 || got.PeakRSSKB <= 0 {
				t.Fatalf("%s %s: rows %d, peak_rss_kb %d", src, q, got.Rows, got.PeakRSSKB)
			}
		}
	}
}
