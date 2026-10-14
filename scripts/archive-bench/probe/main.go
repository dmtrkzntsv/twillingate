// Command probe: <sqlite|duck> <db file or parquet glob> <queries.sql> <from> <to> <memlimit> <threads> k=v...
// In one Go process: RSS after open, then the query set twice (warm-up,
// measured); per query wall and CPU (user+sys); peak RSS; RSS 20 s later.
package main

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
	_ "modernc.org/sqlite"
)

func rss() float64 {
	out, _ := exec.Command("ps", "-o", "rss=", "-p", fmt.Sprint(os.Getpid())).Output()
	var kb float64
	fmt.Sscan(strings.TrimSpace(string(out)), &kb)
	return kb / 1024
}

func cpu() time.Duration {
	var ru syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

func maxrss() float64 {
	var ru syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	if runtime.GOOS == "linux" {
		return float64(ru.Maxrss) / 1024 // KB on linux
	}
	return float64(ru.Maxrss) / 1024 / 1024 // bytes on darwin
}

func main() {
	eng, target, qfile, from, to, lim, threads := os.Args[1], os.Args[2], os.Args[3], os.Args[4], os.Args[5], os.Args[6], os.Args[7]
	repl := []string{"{from}", from, "{to}", to}
	for _, kv := range os.Args[8:] {
		k, v, _ := strings.Cut(kv, "=")
		repl = append(repl, "{"+k+"}", v)
	}
	r := strings.NewReplacer(repl...)
	b, _ := os.ReadFile(qfile)
	type q struct{ name, sql string }
	var qs []q
	for _, p := range strings.Split(string(b), "-- name: ")[1:] {
		n, body, _ := strings.Cut(p, "\n")
		qs = append(qs, q{strings.TrimSpace(n), r.Replace(body)})
	}
	base := rss()
	var db *sql.DB
	var err error
	if eng == "sqlite" {
		db, err = sql.Open("sqlite", "file:"+target+"?mode=ro&_pragma=cache_size(-16000)&_pragma=temp_store(2)")
	} else {
		db, err = sql.Open("duckdb", "")
	}
	if err != nil {
		panic(err)
	}
	db.SetMaxOpenConns(1)
	if eng == "duck" {
		setup := []string{"SET threads=" + threads, "SET memory_limit='" + lim + "'", "SET temp_directory='/tmp/duckspill'"}
		if strings.HasSuffix(target, ".duckdb") {
			setup = append(setup, "ATTACH '"+target+"' AS d (READ_ONLY)", "USE d")
		} else {
			setup = append(setup, "CREATE VIEW events AS SELECT * FROM read_parquet('"+target+"', hive_partitioning=true)")
		}
		for _, s := range setup {
			if _, err := db.Exec(s); err != nil {
				panic(err)
			}
		}
	} else {
		db.Exec("SELECT 1")
	}
	fmt.Printf("RSS: process %.0f MB, after open %.0f MB\n", base, rss())
	var totW, totC time.Duration
	for pass := 0; pass < 2; pass++ {
		for _, x := range qs {
			t, c := time.Now(), cpu()
			rows, err := db.Query(x.sql)
			if err == nil {
				for rows.Next() {
				}
				err = rows.Err()
				rows.Close()
			}
			w, cc := time.Since(t), cpu()-c
			if pass == 1 {
				totW += w
				totC += cc
				if err != nil {
					fmt.Printf("%-22s FAILED %.70s\n", x.name, err)
				} else {
					fmt.Printf("%-22s wall %7.3fs  cpu %7.3fs\n", x.name, w.Seconds(), cc.Seconds())
				}
			}
		}
	}
	fmt.Printf("TOTAL wall %.2fs cpu %.2fs | peak RSS %.0f MB, RSS now %.0f MB", totW.Seconds(), totC.Seconds(), maxrss(), rss())
	time.Sleep(20 * time.Second)
	fmt.Printf(", 20 s idle %.0f MB\n", rss())
	if os.Getenv("RELEASE") != "" {
		_, err := db.Exec("SET memory_limit='16MB'")
		time.Sleep(5 * time.Second)
		fmt.Printf("after SET memory_limit='16MB': %.0f MB (%v)\n", rss(), err)
		db.Close()
		time.Sleep(5 * time.Second)
		fmt.Printf("after closing the database: %.0f MB\n", rss())
	}
}
