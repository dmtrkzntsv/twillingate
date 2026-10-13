// Command engine measures D8's child process: spawn to ready, spawn to
// first result, then an 8-widget dashboard through the pipe, first one
// widget after another, then all 8 at once.
//
//	engine parent <parquet glob> <log|-> <queries.sql> <from> <to> <threads> <memlimit> [k=v ...]
//	engine child  <parquet glob> <log|-> <threads> <memlimit>      (spawned by parent)
//
// MATERIALIZE=1 makes the child load the log into an in-memory table once
// instead of parsing it in every query.
//
// Protocol: one JSON object per line each way. Request {"id":1,"sql":"…"},
// reply {"id":1,"rows":n,"err":"…"}; the child says {"ready":true} once its
// view exists. Rows are counted, not shipped: a widget's result is small and
// the question is DuckDB's and the pipe's latency, not JSON encoding.
package main

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
)

type req struct {
	ID  int    `json:"id"`
	SQL string `json:"sql"`
}

type reply struct {
	ID    int    `json:"id"`
	Rows  int    `json:"rows"`
	Err   string `json:"err,omitempty"`
	Ready bool   `json:"ready,omitempty"`
}

func main() {
	switch os.Args[1] {
	case "child":
		child(os.Args[2:])
	case "parent":
		parent(os.Args[2:])
	default:
		fmt.Println("usage: engine parent|child ...")
		os.Exit(2)
	}
}

func child(args []string) {
	glob, log, threads, lim := args[0], args[1], args[2], args[3]
	db, err := sql.Open("duckdb", "")
	if err != nil {
		panic(err)
	}
	// The open log is read once per query through the view, or once per
	// child into an in-memory table (MATERIALIZE=1).
	setup := []string{"SET threads=" + threads, "SET memory_limit='" + lim + "'", "SET temp_directory='/tmp/duckspill'"}
	view := "CREATE VIEW events AS SELECT * EXCLUDE (month) FROM read_parquet('" + glob + "', hive_partitioning=true, union_by_name=true)"
	switch {
	case log == "-":
	case os.Getenv("MATERIALIZE") != "":
		setup = append(setup, "CREATE TABLE hot AS SELECT * FROM read_ndjson('"+log+"', sample_size=-1)")
		view += " UNION ALL BY NAME SELECT * FROM hot"
	default:
		view += " UNION ALL BY NAME SELECT * FROM read_ndjson('" + log + "', sample_size=-1)"
	}
	for _, s := range append(setup, view) {
		if _, err := db.Exec(s); err != nil {
			panic(err)
		}
	}
	out := json.NewEncoder(os.Stdout)
	out.Encode(reply{Ready: true})
	var mu sync.Mutex
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var r req
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue
		}
		go func(r req) {
			rep := reply{ID: r.ID}
			rows, err := db.Query(r.SQL)
			if err == nil {
				for rows.Next() {
					rep.Rows++
				}
				err = rows.Err()
				rows.Close()
			}
			if err != nil {
				rep.Err = err.Error()
			}
			mu.Lock()
			out.Encode(rep)
			mu.Unlock()
		}(r)
	}
}

type engine struct {
	cmd     *exec.Cmd
	in      *json.Encoder
	mu      sync.Mutex
	pending map[int]chan reply
	next    int
}

func spawn(self, glob, log, threads, lim string) (*engine, time.Duration) {
	t := time.Now()
	cmd := exec.Command(self, "child", glob, log, threads, lim)
	cmd.Stderr = os.Stderr
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		panic(err)
	}
	e := &engine{cmd: cmd, in: json.NewEncoder(stdin), pending: map[int]chan reply{}}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	sc.Scan()
	var first reply
	json.Unmarshal(sc.Bytes(), &first)
	if !first.Ready {
		panic("child not ready: " + sc.Text())
	}
	ready := time.Since(t)
	go func() {
		for sc.Scan() {
			var r reply
			json.Unmarshal(sc.Bytes(), &r)
			e.mu.Lock()
			ch := e.pending[r.ID]
			delete(e.pending, r.ID)
			e.mu.Unlock()
			if ch != nil {
				ch <- r
			}
		}
	}()
	return e, ready
}

func (e *engine) query(sqlText string) reply {
	ch := make(chan reply, 1)
	e.mu.Lock()
	e.next++
	id := e.next
	e.pending[id] = ch
	e.in.Encode(req{ID: id, SQL: sqlText})
	e.mu.Unlock()
	return <-ch
}

func (e *engine) close() {
	e.cmd.Process.Kill()
	e.cmd.Wait()
}

// The dashboard: 8 widgets, the Views page's and the product page's heaviest.
var dashboard = []string{"views_visitors_daily", "sessions_bounce", "top_pages", "product_events_daily",
	"dau", "distinct_users_range", "two_attrs", "retention_weekly"}

func parent(args []string) {
	glob, log, qfile, from, to, threads, lim := args[0], args[1], args[2], args[3], args[4], args[5], args[6]
	repl := []string{"{from}", from, "{to}", to}
	for _, kv := range args[7:] {
		k, v, _ := strings.Cut(kv, "=")
		repl = append(repl, "{"+k+"}", v)
	}
	r := strings.NewReplacer(repl...)
	b, _ := os.ReadFile(qfile)
	qs := map[string]string{}
	for _, p := range strings.Split(string(b), "-- name: ")[1:] {
		n, body, _ := strings.Cut(p, "\n")
		qs[strings.TrimSpace(n)] = r.Replace(body)
	}
	self, _ := os.Executable()

	// Spawn three times: the first pays for page cache and the binary, the
	// others are what a dashboard load after idle costs.
	for i := 0; i < 3; i++ {
		t := time.Now()
		e, ready := spawn(self, glob, log, threads, lim)
		rep := e.query(qs["views_visitors_daily"])
		first := time.Since(t)
		fmt.Printf("spawn %d: ready %6.0f ms, first result %6.0f ms (%d rows%s)\n", i+1, ready.Seconds()*1e3, first.Seconds()*1e3, rep.Rows, errSuffix(rep))
		e.close()
	}

	e, _ := spawn(self, glob, log, threads, lim)
	defer e.close()
	for pass := 0; pass < 2; pass++ { // warm, then measured
		t := time.Now()
		for _, n := range dashboard {
			rep := e.query(qs[n])
			if pass == 1 && rep.Err != "" {
				fmt.Printf("  %s: %s\n", n, rep.Err)
			}
		}
		if pass == 1 {
			fmt.Printf("dashboard, 8 widgets one after another: %.2f s\n", time.Since(t).Seconds())
		}
	}
	t := time.Now()
	var wg sync.WaitGroup
	for _, n := range dashboard {
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			if rep := e.query(qs[n]); rep.Err != "" {
				fmt.Printf("  %s: %s\n", n, rep.Err)
			}
		}(n)
	}
	wg.Wait()
	fmt.Printf("dashboard, 8 widgets at once:          %.2f s\n", time.Since(t).Seconds())
}

func errSuffix(r reply) string {
	if r.Err == "" {
		return ""
	}
	return ", error: " + r.Err
}
