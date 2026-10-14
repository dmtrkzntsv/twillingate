package main

// sqlq and duckq: run a query file (queries_sqlite.sql, queries_duckdb.sql)
// over one date range and print each query's time.

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type namedQuery struct{ name, sql string }

// loadQueries splits a file on "-- name: X" headers and substitutes
// {from}, {to} and every k=v in vars.
func loadQueries(path, from, to string, vars []string) []namedQuery {
	b, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	repl := []string{"{from}", from, "{to}", to}
	for _, kv := range vars {
		k, v, _ := strings.Cut(kv, "=")
		repl = append(repl, "{"+k+"}", v)
	}
	r := strings.NewReplacer(repl...)
	var qs []namedQuery
	for _, part := range strings.Split(string(b), "-- name: ")[1:] {
		name, body, _ := strings.Cut(part, "\n")
		qs = append(qs, namedQuery{strings.TrimSpace(name), strings.TrimSpace(r.Replace(body))})
	}
	return qs
}

// sqlq <db> <queries> <from> <to> <timeoutSec> [k=v ...]
// Runs each query once through modernc SQLite (twillingate's driver),
// reading every row.
func cmdSQLQ(args []string) {
	timeout, _ := strconv.Atoi(args[4])
	db := openDB(args[0])
	defer db.Close()
	only := os.Getenv("ONLY")
	for _, q := range loadQueries(args[1], args[2], args[3], args[5:]) {
		if only != "" && !strings.Contains(","+only+",", ","+q.name+",") {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
		t := time.Now()
		n, err := func() (int, error) {
			rows, err := db.QueryContext(ctx, q.sql)
			if err != nil {
				return 0, err
			}
			defer rows.Close()
			n := 0
			for rows.Next() {
				n++
			}
			return n, rows.Err()
		}()
		d := time.Since(t).Seconds()
		timedOut := ctx.Err() == context.DeadlineExceeded
		cancel()
		switch {
		case timedOut:
			fmt.Printf("%-22s  >%ds (timeout)\n", q.name, timeout)
		case err != nil:
			fmt.Printf("%-22s  error: %v\n", q.name, err)
		default:
			fmt.Printf("%-22s  %9.3fs  %6d rows\n", q.name, d, n)
		}
	}
}

var runTime = regexp.MustCompile(`Run Time \(s\): real ([0-9.]+)`)
var maxRSS = regexp.MustCompile(`(\d+)\s+maximum resident set size`)

// duckq <db|:memory:> <setup.sql|-> <queries> <from> <to> <reps> [k=v ...]
// Runs the duckdb CLI once, setup first, each query reps times (the first
// warms DuckDB's buffer pool); prints the first and the best run, and the
// process's peak RSS.
func cmdDuckQ(args []string) {
	reps, _ := strconv.Atoi(args[5])
	var script bytes.Buffer
	if args[1] != "-" {
		b, err := os.ReadFile(args[1])
		if err != nil {
			panic(err)
		}
		script.Write(b)
		script.WriteString("\n")
	}
	script.WriteString(".timer on\n.mode trash\n")
	qs := loadQueries(args[2], args[3], args[4], args[6:])
	for _, q := range qs {
		fmt.Fprintf(&script, ".print @@%s\n", q.name)
		for range reps {
			script.WriteString(q.sql + "\n")
		}
	}
	// duckdb's stderr joins its stdout so an error follows its query's marker.
	cmd := exec.Command("/usr/bin/time", "-l", "sh", "-c", `exec duckdb "$0" 2>&1`, args[0])
	cmd.Stdin = &script
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	_ = cmd.Run() // a failed query exits 1; its error is reported below
	cur := ""
	times := map[string][]float64{}
	failed := map[string]string{}
	sc := bufio.NewScanner(&out)
	for sc.Scan() {
		line := sc.Text()
		if name, ok := strings.CutPrefix(line, "@@"); ok {
			cur = name
		} else if m := runTime.FindStringSubmatch(line); m != nil && cur != "" {
			v, _ := strconv.ParseFloat(m[1], 64)
			times[cur] = append(times[cur], v)
		} else if strings.Contains(line, "Error:") && failed[cur] == "" {
			failed[cur] = line
		}
	}
	for _, q := range qs {
		ts := times[q.name]
		if f := failed[q.name]; f != "" {
			fmt.Printf("%-22s  FAILED %s\n", q.name, f)
			continue
		}
		if len(ts) == 0 {
			fmt.Printf("%-22s  no timing\n", q.name)
			continue
		}
		best := ts[0]
		for _, v := range ts {
			best = min(best, v)
		}
		fmt.Printf("%-22s  first %8.3fs  best %8.3fs\n", q.name, ts[0], best)
	}
	if m := maxRSS.FindStringSubmatch(errb.String()); m != nil {
		v, _ := strconv.ParseFloat(m[1], 64)
		fmt.Printf("peak RSS %.0f MB\n", v/1e6)
	}
}
