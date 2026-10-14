package archivetest

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func TestMain(m *testing.M) {
	if Child() {
		return
	}
	os.Exit(m.Run())
}

func event(id string, project int64, at string) store.Event {
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		panic(err)
	}
	return store.Event{ID: id, ProjectID: project, Family: store.FamilyProduct, EventName: "x", TS: t, ReceivedAt: t}
}

// TestWritesTwoDaysAndReadsThemBack: yesterday is a day file after the
// pass, today an open log, and a scoped query sees one project of them.
func TestWritesTwoDaysAndReadsThemBack(t *testing.T) {
	f := New(t)
	f.Write(t, event("a", 1, "2026-10-11T10:00:00Z"), event("b", 1, "2026-10-12T10:00:00Z"), event("c", 2, "2026-10-12T11:00:00Z"))
	f.Maintain(t, "2026-10-12")
	days, err := archive.LatestDays(f.Dir)
	if err != nil || fmt.Sprint(days) != "map[1:2026-10-12 2:2026-10-12]" {
		t.Fatalf("latest days = %v, %v", days, err)
	}
	res, err := f.Reader.QueryScoped(context.Background(), archive.Scoped{SQL: "SELECT CAST(day AS VARCHAR), count(*) FROM events GROUP BY day ORDER BY day", Project: 1}, archive.Rules{})
	if err != nil || fmt.Sprint(res.Rows) != "[[2026-10-11 1] [2026-10-12 1]]" {
		t.Fatalf("project 1 by day = %v, %v", res.Rows, err)
	}
	res, err = f.Reader.Query(context.Background(), "SELECT count(*) FROM events", 0, archive.Rules{})
	if err != nil || fmt.Sprint(res.Rows) != "[[3]]" {
		t.Fatalf("all projects = %v, %v", res.Rows, err)
	}
}
