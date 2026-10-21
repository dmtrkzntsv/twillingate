package archive_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/archive/archivetest"
	"github.com/dmtrkzntsv/twillingate/internal/store/storetest"

	_ "modernc.org/sqlite"
)

// TestSeedDemoLinesDumpLikeIngestLines: the closed logs scripts/seed-demo.py
// writes are lines the pass dumps and the reader reads, every family and the
// names of users and groups included.
func TestSeedDemoLinesDumpLikeIngestLines(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	f := archivetest.New(t)
	db := filepath.Join(t.TempDir(), "seed.db")
	storetest.Copy(t, db)
	conn, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO projects (id, name) VALUES (1, 'dev'), (2, 'app')`); err != nil {
		t.Fatal(err)
	}
	conn.Close()

	script, err := filepath.Abs("../../scripts/seed-demo.py")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(py, script, db, "--events-dir", f.Dir).CombinedOutput(); err != nil {
		t.Fatalf("seed: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(f.Dir, "1")); err != nil {
		t.Fatal(err)
	}

	f.Maintain(t, time.Now().UTC().Format("2006-01-02"))
	ctx := context.Background()
	res, err := f.Reader.Query(ctx, `SELECT family, count(*) FROM events GROUP BY ALL`, 100, archive.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range res.Rows {
		got[r[0].(string)] = true
	}
	for _, fam := range []string{"views", "product", "measures"} {
		if !got[fam] {
			t.Errorf("no %s rows after the dump: %v", fam, res.Rows)
		}
	}
	res, err = f.Reader.Query(ctx, `SELECT count(*) FROM identities WHERE name <> ''`, 10, archive.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	if n := fmt.Sprint(res.Rows[0][0]); n == "0" {
		t.Errorf("no named identities: %v", res.Rows)
	}
}
