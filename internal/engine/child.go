// Package engine runs DuckDB in a child process of this binary (raw-first
// spec D8): the ingest process never initializes DuckDB, a crash or an
// out-of-memory error in widget SQL takes down only the child, a child
// that exits gives its memory back, and the child can reach one directory
// and nothing else but its own spill directory (DuckDB's temp_directory,
// writable, per child, removed at exit). ServeChild is the child's end of the pipe; Engine
// (engine.go) is the parent's.
package engine

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"sync"

	_ "github.com/duckdb/duckdb-go/v2" // registers the "duckdb" driver
)

// DefaultMemoryLimit is ENGINE_MEMORY_LIMIT's default (D8): at 256 MB a
// COUNT(DISTINCT) over 3 years fails.
const DefaultMemoryLimit = "512MB"

// maxReplyBytes caps one reply line, so a huge value cannot flood the parent's decoder.
const maxReplyBytes = 32 << 20

// ServeChild is `twillingate engine`: it opens an in-memory DuckDB locked
// to the -allow directories, writes {"ready":true}, then answers one
// request per line until stdin closes, when it cancels what is in flight
// and returns nil. A setup failure is written as one {"err":…} line and
// returned.
func ServeChild(args []string, in io.Reader, out io.Writer) error {
	w := &replyWriter{out: out}
	fs := flag.NewFlagSet("engine", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	threads := fs.Int("threads", 1, "DuckDB threads")
	memory := fs.String("memory", DefaultMemoryLimit, "DuckDB memory_limit")
	spill := fs.String("spill", "", "DuckDB temp_directory")
	tempMax := fs.String("temp-max", "", "DuckDB max_temp_directory_size")
	parallel := fs.Int("parallel", 1, "requests run at once")
	var allow dirList
	fs.Var(&allow, "allow", "a directory SQL may read and write (repeatable)")
	if err := fs.Parse(args); err != nil {
		return w.fail(fmt.Errorf("engine: %w", err))
	}
	if *parallel < 1 {
		*parallel = 1
	}
	db, err := open(*threads, *memory, *spill, *tempMax, allow, *parallel)
	if err != nil {
		return w.fail(err)
	}
	defer db.Close()
	w.send(reply{Ready: true})
	return serve(db, in, w, *parallel)
}

// open starts an in-memory DuckDB and locks it down. Every setting is
// GLOBAL, so it holds on each connection the pool opens later, and
// lock_configuration stops SQL from changing any of them back. Directories
// get a trailing slash, so allowing raw does not allow rawx.
func open(threads int, memory, spill, tempMax string, allow []string, parallel int) (*sql.DB, error) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, fmt.Errorf("engine: %w", err)
	}
	db.SetMaxOpenConns(parallel)
	dirs := make([]string, len(allow))
	for i, d := range allow {
		dirs[i] = sqlString(strings.TrimSuffix(d, "/") + "/")
	}
	setup := []string{
		fmt.Sprintf("SET GLOBAL threads = %d", threads),
		"SET GLOBAL memory_limit = " + sqlString(memory),
	}
	if spill != "" {
		setup = append(setup, "SET GLOBAL temp_directory = "+sqlString(spill))
	}
	if tempMax != "" {
		setup = append(setup, "SET GLOBAL max_temp_directory_size = "+sqlString(tempMax))
	}
	setup = append(setup,
		"SET GLOBAL parquet_metadata_cache = true",
		"SET GLOBAL allowed_directories = ["+strings.Join(dirs, ", ")+"]",
		"SET GLOBAL autoinstall_known_extensions = false",
		"SET GLOBAL autoload_known_extensions = false",
		"SET GLOBAL enable_external_access = false",
		"SET GLOBAL lock_configuration = true",
	)
	for _, s := range setup {
		if _, err := db.Exec(s); err != nil {
			db.Close()
			return nil, fmt.Errorf("engine: %s: %w", s, err)
		}
	}
	return db, nil
}

// serve answers requests until in ends. Each request runs in its own
// goroutine, at most parallel at once (D8: one or two widgets at a time
// per child on small servers); a cancel line interrupts the request with
// its id.
func serve(db *sql.DB, in io.Reader, w *replyWriter, parallel int) error {
	dec := json.NewDecoder(in)
	var mu sync.Mutex
	cancels := map[uint64]context.CancelFunc{}
	running := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	defer func() {
		mu.Lock()
		for _, cancel := range cancels {
			cancel()
		}
		mu.Unlock()
		wg.Wait()
	}()
	for {
		var r request
		if err := dec.Decode(&r); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("engine: reading a request: %w", err)
		}
		if r.Cancel {
			mu.Lock()
			if cancel := cancels[r.ID]; cancel != nil {
				cancel()
			}
			mu.Unlock()
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		mu.Lock()
		cancels[r.ID] = cancel
		mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				mu.Lock()
				delete(cancels, r.ID)
				mu.Unlock()
				cancel()
			}()
			select {
			case running <- struct{}{}:
			case <-ctx.Done():
				w.send(reply{ID: r.ID, Err: ctx.Err().Error()})
				return
			}
			defer func() { <-running }()
			w.send(run(ctx, db, r))
		}()
	}
}

// runScript runs a script on one connection and returns its error text. A script
// that fails after its BEGIN leaves the transaction open on that pooled
// connection, aborted, for whatever request gets it next: it is rolled back
// here, on the same connection (a ROLLBACK with none open is refused, and
// that is fine).
func runScript(ctx context.Context, db *sql.DB, script string) string {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err.Error()
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, script); err != nil {
		conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		return err.Error()
	}
	return ""
}

// run executes one request.
func run(ctx context.Context, db *sql.DB, r request) reply {
	rep := reply{ID: r.ID}
	if r.Exec {
		rep.Err = runScript(ctx, db, r.SQL)
		return rep
	}
	if err := readOnly(ctx, db, r.SQL); err != nil {
		rep.Err = err.Error()
		return rep
	}
	rows, err := db.QueryContext(ctx, r.SQL)
	if err != nil {
		rep.Err = err.Error()
		return rep
	}
	defer rows.Close()
	types, err := rows.ColumnTypes()
	if err != nil {
		rep.Err = err.Error()
		return rep
	}
	vals := make([]any, len(types))
	ptrs := make([]any, len(types))
	for i := range vals {
		ptrs[i] = &vals[i]
		rep.Columns = append(rep.Columns, types[i].Name())
	}
	for rows.Next() {
		if r.MaxRows > 0 && len(rep.Rows) == r.MaxRows {
			rep.Truncated = true
			break
		}
		if err := rows.Scan(ptrs...); err != nil {
			return reply{ID: r.ID, Err: err.Error()}
		}
		row := make([]any, len(vals))
		for i, v := range vals {
			row[i] = jsonValue(v, types[i].DatabaseTypeName())
		}
		rep.Rows = append(rep.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return reply{ID: r.ID, Err: err.Error()}
	}
	return rep
}

// readOnly lets one SELECT through (DESCRIBE, SHOW, SUMMARIZE, FROM and
// VALUES parse as one) and nothing else: DuckDB's allowed directory is
// read-write, so a query must not COPY, CREATE or ATTACH. It parses with
// json_serialize_sql, which runs nothing; preparing would run every
// statement of a multi-statement string but the last.
func readOnly(ctx context.Context, db *sql.DB, q string) error {
	var out string
	if err := db.QueryRowContext(ctx, "SELECT json_serialize_sql(CAST(? AS VARCHAR))::VARCHAR", q).Scan(&out); err != nil {
		return fmt.Errorf("engine: %w", err)
	}
	var parsed struct {
		Error      bool              `json:"error"`
		Type       string            `json:"error_type"`
		Message    string            `json:"error_message"`
		Statements []json.RawMessage `json:"statements"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return fmt.Errorf("engine: %w", err)
	}
	if parsed.Error && parsed.Type == "parser" {
		return errors.New("engine: " + parsed.Message)
	}
	if parsed.Error || len(parsed.Statements) != 1 {
		return errors.New("engine: a query must be one SELECT statement")
	}
	return nil
}

// replyWriter serializes lines from concurrent requests. A write error
// means the parent is gone; stdin's EOF ends the child right after.
type replyWriter struct {
	mu  sync.Mutex
	out io.Writer
}

func (w *replyWriter) send(r reply) {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Marshal first: encoding/json writes nothing when it refuses a value,
	// and a reply past the cap is replaced before any byte is sent. Either
	// way the request is answered with an error, never left without a reply.
	line, err := encodeLine(r)
	if err == nil && len(line) > maxReplyBytes {
		err = fmt.Errorf("the result is larger than %d MiB; select fewer rows or columns", maxReplyBytes>>20)
		line = nil
	} else if err != nil {
		err = fmt.Errorf("the result cannot be sent: %w", err)
	}
	if err != nil {
		if r.ID == 0 {
			return
		}
		line, _ = encodeLine(reply{ID: r.ID, Err: "engine: " + err.Error()})
	}
	_, _ = w.out.Write(line)
}

// encodeLine is one reply as a JSON line.
func encodeLine(r reply) ([]byte, error) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(r); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (w *replyWriter) fail(err error) error {
	w.send(reply{Err: err.Error()})
	return err
}

// dirList is a repeatable string flag.
type dirList []string

func (d *dirList) String() string     { return strings.Join(*d, ",") }
func (d *dirList) Set(v string) error { *d = append(*d, v); return nil }

// sqlString quotes s as a SQL string literal.
func sqlString(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
