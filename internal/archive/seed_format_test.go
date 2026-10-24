package archive

import (
	"context"
	"database/sql"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store/storetest"

	_ "modernc.org/sqlite"
)

// TestSeedDemoLinesDumpLikeIngestLines: the closed logs scripts/seed-demo.py
// writes are lines the pass dumps, every family and the names of users and
// groups included: read back from the Parquet files the pass writes. Under
// race it seeds 35 days rather than 180.
func TestSeedDemoLinesDumpLikeIngestLines(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	dir := filepath.Join(t.TempDir(), "raw")
	l, err := OpenLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
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
	args := []string{script, db, "--events-dir", dir}
	if raceEnabled {
		// 35 days still reach back into a closed month and carry every
		// family and the named identities, at a fifth of the work.
		args = append(args, "--days", "35")
	}
	if out, err := exec.Command(py, args...).CombinedOutput(); err != nil {
		t.Fatalf("seed: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "1")); err != nil {
		t.Fatal(err)
	}

	today := civil.Today(time.Now().UTC())
	if err := New(l, slog.New(slog.DiscardHandler)).Maintain(context.Background(), today, 1, 400); err != nil {
		t.Fatal(err)
	}
	families, named := map[string]bool{}, 0
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".parquet") {
			return err
		}
		switch {
		case d.Name() == identitiesFile:
			for _, r := range readTable(t, path, "name").rows {
				if s, _ := r[0].(string); s != "" {
					named++
				}
			}
		case d.Name() == aliasesFile, filepath.Base(filepath.Dir(path)) == sessionsDir:
		default:
			for _, r := range readTable(t, path, "family").rows {
				families[r[0].(string)] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, fam := range []string{"views", "product", "measures"} {
		if !families[fam] {
			t.Errorf("no %s rows after the dump: %v", fam, families)
		}
	}
	if named == 0 {
		t.Error("no named identities")
	}
}
