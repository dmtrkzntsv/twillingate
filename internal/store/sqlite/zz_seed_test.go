package sqlite

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// TestSeedEvidenceFixture builds a populated database for the Evidence build
// check. Skipped unless SEED_DB is set.
func TestSeedEvidenceFixture(t *testing.T) {
	path := os.Getenv("SEED_DB")
	if path == "" {
		t.Skip("SEED_DB not set")
	}
	db, err := openAt(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	audit := store.AuditEntry{Actor: "seed", Action: "project.create"}
	var appID int64
	for i, name := range []string{"app", "blog"} {
		id, err := db.CreateProject(ctx, store.RegistryProject{
			Name: name, AllowedOrigins: "[]"}, audit)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			appID = id
		}
	}
	var views []store.View
	var evs []store.ProductEvent
	base := time.Now().UTC().AddDate(0, 0, -10)
	for d := 0; d < 10; d++ {
		day := base.AddDate(0, 0, d)
		for i := 0; i < 5; i++ {
			tsV := day.Add(time.Duration(i) * time.Hour)
			views = append(views, store.View{
				ID: fmt.Sprintf("h%d-%d", d, i), ProjectID: appID, Kind: "web", TS: tsV,
				ActorID: fmt.Sprintf("v%d", i%3), ActorKind: store.ActorConnection,
				Path:           []string{"/", "/pricing", "/docs"}[i%3],
				ReferrerSource: []string{"google", "", "hn"}[i%3],
				Country:        []string{"US", "DE", "FR"}[i%3], Device: []string{"desktop", "mobile"}[i%2],
				Browser: "chrome", OS: "linux",
				UTMSource: []string{"hn", "", ""}[i%3], UTMMedium: []string{"social", "", ""}[i%3],
				UTMCampaign: []string{"launch", "", ""}[i%3],
				Consent:     []store.Consent{store.ConsentGiven, store.ConsentNone, store.ConsentUnknown}[i%3],
			})
			evs = append(evs, store.ProductEvent{
				ID: fmt.Sprintf("e%d-%d", d, i), ProjectID: appID,
				EventName: []string{"signup", "subscribed"}[i%2],
				ActorID:   fmt.Sprintf("u%d", i%3), TS: tsV,
				Attributes: map[string]string{"plan": []string{"pro", "free"}[i%2]},
			})
		}
	}
	if err := db.WriteViews(ctx, views); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteProductEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	// Aggregate the oldest days so both sides of the stitch views have rows.
	for d := 0; d < 4; d++ {
		day := civilOf(base.AddDate(0, 0, d))
		if err := db.AggregateViewDay(ctx, appID, day); err != nil {
			t.Fatal(err)
		}
		if err := db.AggregateProductDay(ctx, appID, day, []string{"plan"}, 10); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.RebuildFlatView(ctx, []string{"plan"}); err != nil {
		t.Fatal(err)
	}
	t.Logf("seeded %s: %d views, %d events", path, len(views), len(evs))
}

func civilOf(t time.Time) civil.Date { return civil.DateOf(t) }
