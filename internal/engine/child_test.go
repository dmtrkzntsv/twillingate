package engine

import (
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testChild runs ServeChild in-process on pipes: the protocol without a
// process, so these tests need no binary.
type testChild struct {
	in   *io.PipeWriter
	out  *io.PipeReader
	enc  *json.Encoder
	dec  *json.Decoder
	done chan error
}

// childArgs allows dir/raw only and spills into dir/spill.
func childArgs(dir string) []string {
	return []string{"-threads", "1", "-memory", "256MB", "-spill", filepath.Join(dir, "spill"),
		"-parallel", "1", "-allow", filepath.Join(dir, "raw")}
}

func startChild(t *testing.T, args []string) *testChild {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &testChild{in: inW, out: outR, enc: json.NewEncoder(inW), dec: json.NewDecoder(outR), done: make(chan error, 1)}
	c.dec.UseNumber()
	go func() {
		c.done <- ServeChild(args, inR, outW)
		outW.Close()
	}()
	var first reply
	if err := c.dec.Decode(&first); err != nil || !first.Ready {
		t.Fatalf("first line = %+v, %v; want ready", first, err)
	}
	t.Cleanup(func() { inW.Close(); <-c.done })
	return c
}

func (c *testChild) ask(t *testing.T, r request) reply {
	t.Helper()
	if err := c.enc.Encode(r); err != nil {
		t.Fatal(err)
	}
	var rep reply
	if err := c.dec.Decode(&rep); err != nil {
		t.Fatal(err)
	}
	if rep.ID != r.ID {
		t.Fatalf("reply id = %d, want %d", rep.ID, r.ID)
	}
	return rep
}

func TestChildAnswers(t *testing.T) {
	c := startChild(t, childArgs(t.TempDir()))
	rep := c.ask(t, request{ID: 1, SQL: "SELECT 42 AS answer"})
	if rep.Err != "" || len(rep.Columns) != 1 || rep.Columns[0] != "answer" ||
		len(rep.Rows) != 1 || rep.Rows[0][0] != json.Number("42") {
		t.Fatalf("reply = %+v", rep)
	}
}

// TestChildKeepsTypes: what DuckDB scans into Go values reaches the parent
// as JSON a reader can trust: integers whole, decimals exact, dates and
// times as text, UUIDs as text, NULL as null.
func TestChildKeepsTypes(t *testing.T) {
	c := startChild(t, childArgs(t.TempDir()))
	rep := c.ask(t, request{ID: 1, SQL: `SELECT 9007199254740993::BIGINT, 12.34::DECIMAL(10,2),
		170141183460469231731687303715884105727::HUGEINT, DATE '2026-10-12',
		TIMESTAMP '2026-10-12 01:02:03', TIME '01:02:03', '0af59e70-fb48-4e2b-8bc0-03e93be19ea0'::UUID,
		NULL::VARCHAR, 'x', true, 1.5::DOUBLE`})
	if rep.Err != "" {
		t.Fatal(rep.Err)
	}
	want := []any{json.Number("9007199254740993"), json.Number("12.34"),
		json.Number("170141183460469231731687303715884105727"), "2026-10-12",
		"2026-10-12T01:02:03Z", "01:02:03", "0af59e70-fb48-4e2b-8bc0-03e93be19ea0",
		nil, "x", true, json.Number("1.5")}
	for i, w := range want {
		if rep.Rows[0][i] != w {
			t.Errorf("column %d = %#v, want %#v", i, rep.Rows[0][i], w)
		}
	}
}

// TestChildSendsNonFiniteFloats: encoding/json refuses NaN and the
// infinities, so they travel as text rather than costing the request its reply.
func TestChildSendsNonFiniteFloats(t *testing.T) {
	c := startChild(t, childArgs(t.TempDir()))
	rep := c.ask(t, request{ID: 1, SQL: "SELECT 'NaN'::DOUBLE, 'inf'::DOUBLE, '-inf'::DOUBLE, 1.5::DOUBLE"})
	if rep.Err != "" {
		t.Fatal(rep.Err)
	}
	want := []any{"NaN", "Infinity", "-Infinity", json.Number("1.5")}
	for i, w := range want {
		if rep.Rows[0][i] != w {
			t.Errorf("column %d = %#v, want %#v", i, rep.Rows[0][i], w)
		}
	}
}

// TestReplySendAnswersWhatItCannotEncode: a reply JSON refuses still becomes
// a reply, an error one, for its request.
func TestReplySendAnswersWhatItCannotEncode(t *testing.T) {
	var out strings.Builder
	w := &replyWriter{out: &out}
	w.send(reply{ID: 9, Rows: [][]any{{math.NaN()}}})
	var rep reply
	if err := json.Unmarshal([]byte(out.String()), &rep); err != nil || rep.ID != 9 || !strings.Contains(rep.Err, "cannot be sent") {
		t.Fatalf("output = %q, want an error reply for id 9", out.String())
	}
}

// TestChildCapsReplySize: one reply past maxReplyBytes becomes an error, so a
// huge value cannot flood the parent's decoder; the child carries on.
func TestChildCapsReplySize(t *testing.T) {
	c := startChild(t, childArgs(t.TempDir()))
	rep := c.ask(t, request{ID: 1, SQL: "SELECT repeat('x', 40000000) AS s"})
	if !strings.Contains(rep.Err, "larger than") || len(rep.Rows) != 0 {
		t.Fatalf("reply: err = %q, rows = %d; want a size error and no rows", rep.Err, len(rep.Rows))
	}
	if rep := c.ask(t, request{ID: 2, SQL: "SELECT 1"}); rep.Err != "" {
		t.Fatalf("after the oversized reply: %q", rep.Err)
	}
}

func TestChildReportsSQLErrorsAndCarriesOn(t *testing.T) {
	c := startChild(t, childArgs(t.TempDir()))
	if rep := c.ask(t, request{ID: 1, SQL: "SELECT nope"}); !strings.Contains(rep.Err, "nope") {
		t.Fatalf("err = %q, want one naming nope", rep.Err)
	}
	if rep := c.ask(t, request{ID: 2, SQL: "SELECT 1"}); rep.Err != "" {
		t.Fatalf("after an error: %q", rep.Err)
	}
}

func TestChildCapsRows(t *testing.T) {
	c := startChild(t, childArgs(t.TempDir()))
	rep := c.ask(t, request{ID: 1, SQL: "SELECT * FROM range(10)", MaxRows: 3})
	if len(rep.Rows) != 3 || !rep.Truncated {
		t.Fatalf("rows = %d, truncated = %v; want 3 and true", len(rep.Rows), rep.Truncated)
	}
	rep = c.ask(t, request{ID: 2, SQL: "SELECT * FROM range(10)"})
	if len(rep.Rows) != 10 || rep.Truncated {
		t.Fatalf("rows = %d, truncated = %v; want 10 and false", len(rep.Rows), rep.Truncated)
	}
}

// TestChildExecKeepsState: an in-memory table made by Exec stays for later
// queries (PR 3 loads the open log this way, once per child).
func TestChildExecKeepsState(t *testing.T) {
	c := startChild(t, childArgs(t.TempDir()))
	if rep := c.ask(t, request{ID: 1, SQL: "CREATE TABLE hot AS SELECT 7 AS n", Exec: true}); rep.Err != "" {
		t.Fatal(rep.Err)
	}
	if rep := c.ask(t, request{ID: 2, SQL: "SELECT n FROM hot"}); rep.Err != "" || rep.Rows[0][0] != json.Number("7") {
		t.Fatalf("reply = %+v", rep)
	}
}

// TestChildConfinedToAllowedDirs is Review Focus 1: SQL reads and writes
// inside the allowed directory and nowhere else, a sibling whose name
// starts the same included, and cannot lift the lock.
func TestChildConfinedToAllowedDirs(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"raw/in.csv", "rawx/in.csv", "twillingate.db"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte("a\n1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "spill"), 0o750); err != nil { // the parent makes it per child
		t.Fatal(err)
	}
	c := startChild(t, childArgs(dir))
	id := uint64(0)
	// The sandbox under test is the dump's: its writes go as Exec, since a
	// query is refused before it reaches the sandbox or the lock.
	run := func(sql string, exec bool) string {
		id++
		return c.ask(t, request{ID: id, SQL: sql, Exec: exec}).Err
	}
	if err := run("SELECT * FROM read_csv('"+filepath.Join(dir, "raw/in.csv")+"')", false); err != "" {
		t.Errorf("reading inside raw: %s", err)
	}
	if err := run("COPY (SELECT 1 AS a) TO '"+filepath.Join(dir, "raw/out.parquet")+"' (FORMAT parquet)", true); err != "" {
		t.Errorf("writing inside raw: %s", err)
	}
	// The spill directory is writable too: per child, removed at exit.
	if err := run("COPY (SELECT 1 AS a) TO '"+filepath.Join(dir, "spill/x.csv")+"' (FORMAT csv)", true); err != "" {
		t.Errorf("writing into the spill directory: %s", err)
	}
	for _, c := range []struct {
		sql  string
		exec bool
	}{
		{"SELECT * FROM read_text('" + filepath.Join(dir, "raw/../twillingate.db") + "')", false},
		{"SELECT * FROM read_csv('" + filepath.Join(dir, "rawx/in.csv") + "')", false},
		{"SELECT * FROM read_text('" + filepath.Join(dir, "twillingate.db") + "')", false},
		{"COPY (SELECT 1 AS a) TO '" + filepath.Join(dir, "twillingate.db") + "' (FORMAT csv)", true},
		{"ATTACH '" + filepath.Join(dir, "twillingate.db") + "' AS meta", true},
		{"SELECT * FROM read_text('/etc/hostname')", false},
		{"INSTALL httpfs", true},
	} {
		if err := run(c.sql, c.exec); !strings.Contains(err, "Permission Error") {
			t.Errorf("%s: err = %q, want a permission error", c.sql, err)
		}
	}
	for _, sql := range []string{"SET enable_external_access = true", "SET memory_limit = '4GB'"} {
		if err := run(sql, true); !strings.Contains(err, "locked") {
			t.Errorf("%s: err = %q, want the configuration locked", sql, err)
		}
	}
}

// TestChildQueriesAreReadOnly is Review Focus 5: a query is one SELECT and
// nothing else, refused before anything runs; Exec (the archive's dump)
// still writes.
func TestChildQueriesAreReadOnly(t *testing.T) {
	dir := t.TempDir()
	raw := filepath.Join(dir, "raw")
	if err := os.MkdirAll(raw, 0o750); err != nil {
		t.Fatal(err)
	}
	c := startChild(t, childArgs(dir))
	out := filepath.Join(raw, "out.csv")
	id := uint64(0)
	ask := func(sql string, exec bool) string {
		id++
		return c.ask(t, request{ID: id, SQL: sql, Exec: exec}).Err
	}
	for _, sql := range []string{
		"COPY (SELECT 1 AS a) TO '" + out + "' (FORMAT csv)",
		"COPY (SELECT 1 AS a) TO '" + out + "' (FORMAT csv); SELECT 1",
		"SELECT 1; SELECT 2",
		"CREATE TABLE t AS SELECT 1",
		"ATTACH ':memory:' AS m",
		"PRAGMA version",
	} {
		if err := ask(sql, false); !strings.Contains(err, "one SELECT") {
			t.Errorf("%s: err = %q, want a refusal naming one SELECT", sql, err)
		}
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("%s exists after refused queries: a refused statement ran", out)
	}
	if err := ask("SELECT nope FROM", false); !strings.Contains(err, "syntax error") {
		t.Errorf("syntax error: err = %q, want DuckDB's parser message", err)
	}
	for _, sql := range []string{"SELECT 1", "WITH a AS (SELECT 1 AS x) SELECT x FROM a", "FROM range(3)", "DESCRIBE SELECT 1", "VALUES (1)"} {
		if err := ask(sql, false); err != "" {
			t.Errorf("%s: %s", sql, err)
		}
	}
	if err := ask("COPY (SELECT 1 AS a) TO '"+out+"' (FORMAT csv)", true); err != "" {
		t.Fatalf("Exec of a COPY into raw: %s", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("Exec wrote nothing: %v", err)
	}
}

func TestChildCapsItsSpill(t *testing.T) {
	c := startChild(t, append(childArgs(t.TempDir()), "-temp-max", "4GB"))
	rep := c.ask(t, request{ID: 1, SQL: "SELECT current_setting('max_temp_directory_size') AS s"})
	if rep.Err != "" || rep.Rows[0][0] != "3.7 GiB" {
		t.Fatalf("max_temp_directory_size = %+v, want 3.7 GiB", rep)
	}
}

// slowSQL runs for minutes unless interrupted.
const slowSQL = "SELECT count(*) FROM range(1000000000000) t(i) WHERE i % 7 = 3"

// TestChildCancelInterrupts is Review Focus 3 at the child: a cancelled
// query stops and the child answers the next one at once.
func TestChildCancelInterrupts(t *testing.T) {
	c := startChild(t, childArgs(t.TempDir()))
	if err := c.enc.Encode(request{ID: 1, SQL: slowSQL}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	if err := c.enc.Encode(request{ID: 1, Cancel: true}); err != nil {
		t.Fatal(err)
	}
	var rep reply
	if err := c.dec.Decode(&rep); err != nil {
		t.Fatal(err)
	}
	if rep.ID != 1 || rep.Err == "" || time.Since(start) > 5*time.Second {
		t.Fatalf("reply = %+v after %v; want an error for id 1 within 5s", rep, time.Since(start))
	}
	if rep := c.ask(t, request{ID: 2, SQL: "SELECT 1"}); rep.Err != "" {
		t.Fatalf("after a cancel: %q", rep.Err)
	}
}

// TestChildExitsOnEOF is Review Focus 4: the parent's end of stdin closing
// (the server exited) ends the child even with a query in flight.
func TestChildExitsOnEOF(t *testing.T) {
	c := startChild(t, childArgs(t.TempDir()))
	if err := c.enc.Encode(request{ID: 1, SQL: slowSQL}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	go io.Copy(io.Discard, c.out) // a pipe write blocks until read: take the cancelled reply
	c.in.Close()
	select {
	case err := <-c.done:
		c.done <- err // for the cleanup's receive
		if err != nil {
			t.Fatalf("ServeChild = %v, want nil on EOF", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ServeChild still running 5s after stdin closed")
	}
}

func TestChildRefusesABadSetup(t *testing.T) {
	inR, _ := io.Pipe()
	var out strings.Builder
	args := append(childArgs(t.TempDir()), "-memory", "lots")
	if err := ServeChild(args, inR, &out); err == nil {
		t.Fatal("ServeChild = nil, want an error for memory_limit 'lots'")
	}
	var rep reply
	if err := json.Unmarshal([]byte(out.String()), &rep); err != nil || rep.Ready || rep.Err == "" {
		t.Fatalf("output = %q, want one error line", out.String())
	}
}
