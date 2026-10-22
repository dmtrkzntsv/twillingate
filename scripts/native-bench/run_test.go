package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
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

func TestWaitQuietGivesUp(t *testing.T) {
	busy := func() (float64, error) { return 3.0, nil }
	start := time.Now()
	got, ok := waitQuiet(1.0, 10*time.Millisecond, 50*time.Millisecond, busy)
	if el := time.Since(start); el > 200*time.Millisecond {
		t.Fatalf("gave up after %s, want within 200ms", el)
	}
	if got != 3.0 || ok {
		t.Fatalf("always busy: got (%v, %v), want (3, false)", got, ok)
	}

	calls := 0
	settles := func() (float64, error) {
		calls++
		if calls == 1 {
			return 3.0, nil
		}
		return 0.5, nil
	}
	got, ok = waitQuiet(1.0, 10*time.Millisecond, 50*time.Millisecond, settles)
	if got != 0.5 || !ok {
		t.Fatalf("settles: got (%v, %v), want (0.5, true)", got, ok)
	}
}

func TestCompareApprox(t *testing.T) {
	exact := []Row{{"a", "x", "10", "40", "0"}, {"a", "y", "9", "2000", "0"}, {"b", "z", "1", "100", "0"}}
	approx := []Row{{"a", "x", "10", "41", "0"}, {"a", "y", "9", "2100", "0"}, {"b", "z", "1", "101", "0"}}
	st := compareApprox(approx, exact)
	if st.Err != "" || st.Values != 6 || st.Outside != 1 || st.UsersOutside != 1 || st.Over1024 != 1 || st.Over1024Outside != 1 {
		t.Fatalf("got %+v", st)
	}
	if st.MaxRelErr != 0.05 || !st.approxFails() {
		t.Fatalf("got max %v, fails %v", st.MaxRelErr, st.approxFails())
	}
	approx[0][2] = "11"
	if st := compareApprox(approx, exact); st.Err == "" {
		t.Fatal("a different count must be an error")
	}
}
