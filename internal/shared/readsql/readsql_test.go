package readsql

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
	_ "github.com/dmtrkzntsv/twillingate/internal/store/sqlite"
)

// newTestDB builds a database at the current schema with the writer
// store, inserts one project, and closes the writer — which checkpoints
// the WAL into the main file (verified by internal/store/sqlite's own
// tests) — so the returned path is a single, quiescent file a readsql.DB
// can open independently. It returns both the open DB and its path so a
// test can hash the file or reopen it with different settings.
func newTestDB(t *testing.T, timeout time.Duration, maxRows int) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "readsql.db")
	st, err := store.Open("sqlite://" + path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateProject(ctx, store.RegistryProject{
		Name: "blog", AllowedOrigins: "[]", Attributes: "[]"},
		store.AuditEntry{Actor: "test", Action: "project.create"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path, timeout, maxRows)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, path
}

// hashFile hashes path and, if present, its -wal sidecar, so a test can
// notice a write that only touched the WAL and never checkpointed.
func hashFile(t *testing.T, path string) string {
	t.Helper()
	h := sha256.New()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	h.Write(data)
	if wal, err := os.ReadFile(path + "-wal"); err == nil {
		h.Write(wal)
	}
	return string(h.Sum(nil))
}

func TestQueryReturnsRowsAndTruncates(t *testing.T) {
	db, _ := newTestDB(t, 2*time.Second, 3)
	res, err := db.Query(context.Background(), `select value from json_each('[1,2,3,4]')`)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 3 {
		t.Fatalf("rows = %v, want 3", res.Rows)
	}
	if !res.Truncated {
		t.Error("Truncated = false, want true")
	}
}

func TestQueryTrailingCommentAndSemicolon(t *testing.T) {
	db, _ := newTestDB(t, 2*time.Second, 3)
	for _, q := range []string{
		"select 1 as x -- trailing",
		"select 1 as x;",
		"select 1 as x; ;",
		"select 1 as x;\n",
	} {
		res, err := db.Query(context.Background(), q)
		if err != nil {
			t.Fatalf("Query(%q): %v", q, err)
		}
		if len(res.Rows) != 1 || len(res.Rows[0]) != 1 || res.Rows[0][0] != "1" {
			t.Errorf("Query(%q) rows = %v, want [[1]]", q, res.Rows)
		}
	}
}

// TestQueryRefusesTrailingCommentAfterSemicolon pins the fix for the
// finding that "select 1; -- c" passed Check but broke Query: Check
// allowed a comment after the trailing ';', while Query's trim (a
// trailing run of ';' and whitespace only) left the ';' and the comment
// inside the subquery wrap, a syntax error rather than a clean refusal.
// The two now agree: this is refused by Check itself, with the message a
// caller can act on, never reaching the wrap at all.
func TestQueryRefusesTrailingCommentAfterSemicolon(t *testing.T) {
	db, _ := newTestDB(t, 2*time.Second, 3)
	_, err := db.Query(context.Background(), "select 1; -- c")
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "single SELECT or WITH statement") {
		t.Fatalf("Query(%q) = %v, want the single-statement refusal", "select 1; -- c", err)
	}
}

func TestQueryNamedParams(t *testing.T) {
	db, _ := newTestDB(t, 2*time.Second, 3)
	res, err := db.Query(context.Background(), `select :project as p`, sql.Named("project", 7))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || res.Rows[0][0] != "7" {
		t.Errorf("rows = %v, want [[7]]", res.Rows)
	}
}

func TestQueryRefusesWrites(t *testing.T) {
	db, path := newTestDB(t, 2*time.Second, 1000)
	// A target for the VACUUM INTO escape below: if Check ever regresses
	// and lets it run, a file appears here.
	vacuumTarget := filepath.Join(t.TempDir(), "escaped-copy.db")
	before := hashFile(t, path)
	for _, q := range []string{
		`INSERT INTO projects(name) VALUES('x')`,
		`UPDATE projects SET name='x'`,
		`DELETE FROM projects`,
		`REPLACE INTO projects(id,name) VALUES(1,'x')`,
		`DROP TABLE projects`,
		`CREATE TABLE t(x)`,
		`CREATE TEMP TABLE t(x)`,
		`WITH x AS (SELECT 1) INSERT INTO projects(name) SELECT 'x'`,
		`PRAGMA user_version = 5`,
		`PRAGMA query_only=0`,
		`ATTACH 'y.db' AS y`,
		`VACUUM INTO 'z.db'`,
		`SELECT 1; SELECT 2`,
		`SELECT load_extension('x')`,
		// The three below escape the subquery wrap itself: a stray ')'
		// with no matching '(' earlier in the text closes the wrapper's
		// own paren early, and the driver runs whatever textually
		// follows as a separate statement on the same call.
		`select 1); select 2; select * from (select 3`,
		fmt.Sprintf(`select 1 where 0); PRAGMA query_only=0; VACUUM INTO '%s'; select * from (select 1`, vacuumTarget),
		`select 1); CREATE TEMP VIEW v_retention AS SELECT 'poisoned' AS cohort_day; select * from (select 1`,
	} {
		if _, err := db.Query(context.Background(), q); err == nil {
			t.Errorf("Query(%q) succeeded, want an error", q)
		}
	}
	after := hashFile(t, path)
	if before != after {
		t.Error("database file changed after write attempts")
	}
	if _, err := os.Stat(vacuumTarget); !os.IsNotExist(err) {
		t.Errorf("VACUUM INTO escape wrote %s", vacuumTarget)
	}
	// The CREATE TEMP VIEW escape, if it had run, would shadow the real
	// v_retention for whichever pooled connection executed it — poisoning
	// every later trusted Run/Query that lands on that connection. Check
	// refuses it before it ever reaches the driver, so no temp view exists
	// on any connection to poison.
	temp, err := db.Run(context.Background(),
		`SELECT count(*) FROM sqlite_temp_master WHERE type='view' AND name='v_retention'`)
	if err != nil {
		t.Fatal(err)
	}
	if len(temp.Rows) != 1 || temp.Rows[0][0] != "0" {
		t.Errorf("a poisoned temp view v_retention exists: %v", temp.Rows)
	}
}

func TestRunTimeout(t *testing.T) {
	db, _ := newTestDB(t, 50*time.Millisecond, 1000)
	_, err := db.Run(context.Background(),
		`WITH RECURSIVE r(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM r WHERE i < 1000000000) SELECT COUNT(*) FROM r`)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
}
