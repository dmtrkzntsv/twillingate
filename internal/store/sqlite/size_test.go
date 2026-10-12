package sqlite

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// TestEncodedRowsAreSmall pins the encoding's point: a prod-shaped batch
// stores in under half today's 491 bytes per row (dbstat counts the
// table's pages).
func TestEncodedRowsAreSmall(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	execAll(t, db, `INSERT INTO projects (id, name, allowed_origins) VALUES (1, 'p', '[]')`)
	// 2,000 product events shaped like econumo's: 14 attributes of ~10-char
	// keys and ~5-char values drawn from small sets, UUID ids, 32-hex actors.
	evs := prodShapedEvents(2000)
	if err := db.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != len(evs) {
		t.Fatalf("events holds %d rows, want %d", rows, len(evs))
	}
	var bytes int64
	if err := db.db.QueryRow(`SELECT SUM(pgsize) FROM dbstat WHERE name = 'events'`).Scan(&bytes); err != nil {
		t.Skipf("dbstat unavailable: %v", err)
	}
	per := bytes / int64(len(evs))
	t.Logf("events: %d bytes over %d rows = %d bytes per row", bytes, len(evs), per)
	if r, err := db.db.Query(`SELECT d.name, SUM(d.pgsize) FROM dbstat d JOIN sqlite_schema s ON s.name = d.name
		WHERE s.tbl_name IN ('events', 'strings', 'attribute_values', 'attribute_keys') GROUP BY d.name ORDER BY 2 DESC`); err == nil {
		for r.Next() {
			var name string
			var n int64
			if r.Scan(&name, &n) == nil {
				t.Logf("  %s: %d bytes", name, n)
			}
		}
		r.Close()
	}
	if per > 245 {
		t.Errorf("events takes %d bytes per row, want under 245", per)
	}
}

// prodShapedEvents returns n deterministic product events: 14 attributes
// of 10-character keys whose values (about five characters) come from 3 to
// 40 choices per key, 50 paths, UUID ids from a counter, and 32-hex actors,
// users and sessions drawn from pools, over three days.
func prodShapedEvents(n int) []store.Event {
	keys := make([]string, 14)
	cards := make([]int, 14)
	for k := range keys {
		keys[k] = fmt.Sprintf("attr_key_%02d", k)[:10]
		cards[k] = 3 + (k*37)%38 // 3..40, spread over the keys
	}
	names := []string{"txn_created", "acct_opened", "budget_set", "report_run", "login_ok", "export_csv"}
	hex32 := func(seed int) string {
		return fmt.Sprintf("%016x%016x", uint64(seed)*0x9e3779b97f4a7c15, uint64(seed)*0xc2b2ae3d27d4eb4f)
	}
	base := time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC)
	out := make([]store.Event, n)
	for i := range out {
		attrs := make(map[string]string, len(keys))
		for k, key := range keys {
			attrs[key] = fmt.Sprintf("v%d_%02d", k%10, (i*(k+3)+i/7)%cards[k])
		}
		ts := base.Add(time.Duration(i) * 97 * time.Second) // ~2.2 days
		out[i] = store.Event{
			Family:     store.FamilyProduct,
			ID:         fmt.Sprintf("0192f1a2-7c3b-7d4e-%04x-%012x", i%0xffff, uint64(i)*2654435761),
			ProjectID:  1,
			EventName:  names[i%len(names)],
			TS:         ts,
			ReceivedAt: ts.Add(time.Second),
			Kind:       "web",
			ActorID:    hex32(i % 400),
			ActorKind:  store.ActorConnection,
			UserID:     hex32(1000 + i%150),
			SessionID:  hex32(5000 + i/6),
			Path:       fmt.Sprintf("/section/%02d/page", i%50),
			Host:       "app.example.com",
			Platform:   "web",
			OS:         []string{"macOS", "Windows", "Linux", "iOS", "Android"}[i%5],
			Browser:    []string{"Chrome", "Safari", "Firefox"}[i%3],
			AppVersion: "3.14.2",
			Country:    []string{"US", "DE", "GB", "FR", "NL", "PL"}[i%6],
			Attributes: attrs,
		}
	}
	return out
}
