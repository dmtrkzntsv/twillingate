package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestWriteViewsRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	views := []store.View{{
		ID: "h1", ProjectID: 1, TS: ts("2026-08-22T10:00:00Z"),
		Kind: "web", ActorID: "v1", ActorKind: store.ActorConnection, Path: "/x", ReferrerSource: "google",
		UTMSource: "hn", Country: "DE", Device: "desktop", Browser: "firefox", OS: "linux",
	}}
	if err := db.WriteViews(ctx, views); err != nil {
		t.Fatal(err)
	}
	var path, tsCol string
	if err := db.db.QueryRow(`SELECT path, ts FROM views WHERE id='h1'`).Scan(&path, &tsCol); err != nil {
		t.Fatal(err)
	}
	if path != "/x" || tsCol != "2026-08-22T10:00:00Z" {
		t.Fatalf("got %q %q", path, tsCol)
	}
	if err := db.WriteViews(ctx, nil); err != nil {
		t.Fatal("empty batch must be a no-op")
	}
}

func TestWriteProductEventsAttributesJSON(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	err := db.WriteProductEvents(ctx, []store.ProductEvent{
		{ID: "e1", ProjectID: 1, EventName: "sub", UserID: "u1",
			TS: ts("2026-08-22T10:00:00Z"), Attributes: map[string]string{"plan": "pro"}},
		{ID: "e2", ProjectID: 1, EventName: "sub", UserID: "u2",
			TS: ts("2026-08-22T10:01:00Z")}, // nil attributes
	})
	if err != nil {
		t.Fatal(err)
	}
	var attrs string
	if err := db.db.QueryRow(`SELECT attributes->>'plan' FROM events WHERE id='e1'`).Scan(&attrs); err != nil {
		t.Fatal(err)
	}
	if attrs != "pro" {
		t.Fatalf("attrs = %q", attrs)
	}
	if err := db.db.QueryRow(`SELECT attributes FROM events WHERE id='e2'`).Scan(&attrs); err != nil {
		t.Fatal(err)
	}
	if attrs != "{}" {
		t.Fatalf("nil attributes must store {}, got %q", attrs)
	}
}

// ProjectIDs returns every registry row including archived ones: the
// daily pass (internal/jobs) relies on this to keep maintaining a project
// after it is archived, using global retention.
func TestProjectIDsIncludesArchived(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	audit := store.AuditEntry{Actor: "test", Action: "project.create"}
	a, err := db.CreateProject(ctx, store.RegistryProject{Name: "A", Identity: "anonymous", AllowedOrigins: "[]"}, audit)
	if err != nil {
		t.Fatal(err)
	}
	b, err := db.CreateProject(ctx, store.RegistryProject{Name: "B", Identity: "anonymous", AllowedOrigins: "[]"}, audit)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetProjectArchived(ctx, b, true, store.AuditEntry{Actor: "test", Action: "project.archive"}); err != nil {
		t.Fatal(err)
	}

	ids, err := db.ProjectIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != a || ids[1] != b {
		t.Fatalf("ProjectIDs = %v, want [%d %d] (archived rows included)", ids, a, b)
	}
}

func TestMetaRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if v, err := db.GetMeta(ctx, "missing"); err != nil || v != "" {
		t.Fatalf("missing key: %q %v", v, err)
	}
	if err := db.SetMeta(ctx, "salt", "s1"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetMeta(ctx, "salt", "s2"); err != nil {
		t.Fatal(err) // overwrite
	}
	if v, _ := db.GetMeta(ctx, "salt"); v != "s2" {
		t.Fatalf("got %q", v)
	}
}

// --- app views, identities, idempotent writes ---

func TestWriteViewsAppRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tsV := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)

	in := []store.View{{
		ID: "018f-a", ProjectID: 1, TS: tsV, ReceivedAt: tsV,
		Kind: "app", ActorID: "act1", ActorKind: store.ActorInstall, UserID: "u1", GroupID: "org9", SessionID: "s1",
		Path: "/settings", Platform: "ios", OS: "ios", OSName: "iOS 17.2", AppVersion: "2.4.1",
		OSVersion: "17.2", DeviceModel: "iPhone15,2", Locale: "en-US", Country: "DE",
	}}
	if err := db.WriteViews(ctx, in); err != nil {
		t.Fatalf("write: %v", err)
	}

	var path, osCol, platform, osName, group, session, locale string
	if err := db.db.QueryRowContext(ctx,
		`SELECT path, os, platform, os_name, group_id, session_id, locale FROM views WHERE id=?`, "018f-a").
		Scan(&path, &osCol, &platform, &osName, &group, &session, &locale); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if path != "/settings" || osCol != "ios" || platform != "ios" || osName != "iOS 17.2" || group != "org9" ||
		session != "s1" || locale != "en-US" {
		t.Errorf("got %q %q %q %q %q %q %q", path, osCol, platform, osName, group, session, locale)
	}
}

func TestWriteViewsAppEmptyIsNoop(t *testing.T) {
	db := newTestDB(t)
	if err := db.WriteViews(context.Background(), nil); err != nil {
		t.Fatalf("empty write: %v", err)
	}
}

func TestWritesAreIdempotentOnID(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tsV := time.Now().UTC()

	appView := store.View{ID: "dup", ProjectID: 1, TS: tsV, ReceivedAt: tsV,
		Kind: "app", ActorID: "a", ActorKind: store.ActorInstall, Path: "/x"}
	webView := store.View{ID: "duph", ProjectID: 1, TS: tsV, ReceivedAt: tsV,
		Kind: "web", ActorID: "a", ActorKind: store.ActorConnection, Path: "/x"}
	ev := store.ProductEvent{ID: "dupe", ProjectID: 1, EventName: "n",
		TS: tsV, ReceivedAt: tsV, ActorID: "a"}

	for i := 0; i < 2; i++ {
		if err := db.WriteViews(ctx, []store.View{appView}); err != nil {
			t.Fatalf("app write %d: %v", i, err)
		}
		if err := db.WriteViews(ctx, []store.View{webView}); err != nil {
			t.Fatalf("web write %d: %v", i, err)
		}
		if err := db.WriteProductEvents(ctx, []store.ProductEvent{ev}); err != nil {
			t.Fatalf("event write %d: %v", i, err)
		}
	}

	var n int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM views`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("views has %d rows after replay, want 2 (one app, one web id)", n)
	}
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("events has %d rows after replay, want 1", n)
	}
}

func TestWriteCarriesIdentityAndAppContext(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tsV := time.Now().UTC()

	if err := db.WriteViews(ctx, []store.View{{ID: "h", ProjectID: 1, TS: tsV,
		ReceivedAt: tsV, Kind: "web", ActorID: "a", ActorKind: store.ActorConnection,
		UserID: "u1", GroupID: "org9", Path: "/x"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteProductEvents(ctx, []store.ProductEvent{{ID: "e", ProjectID: 1,
		EventName: "n", TS: tsV, ReceivedAt: tsV, ActorID: "a", UserID: "u1",
		GroupID: "org9", Platform: "electron", OS: "macos", AppVersion: "2.4.1"}}); err != nil {
		t.Fatal(err)
	}

	var hu, hg string
	if err := db.db.QueryRowContext(ctx,
		`SELECT user_id, group_id FROM views WHERE id='h'`).Scan(&hu, &hg); err != nil {
		t.Fatal(err)
	}
	if hu != "u1" || hg != "org9" {
		t.Errorf("view identity = %q %q", hu, hg)
	}

	var osCol, platform, ver string
	if err := db.db.QueryRowContext(ctx,
		`SELECT os, platform, app_version FROM events WHERE id='e'`).Scan(&osCol, &platform, &ver); err != nil {
		t.Fatal(err)
	}
	if osCol != "macos" || platform != "electron" || ver != "2.4.1" {
		t.Errorf("event context = %q %q %q", osCol, platform, ver)
	}
}

func TestUpsertIdentitiesLatestNameWins(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := db.UpsertIdentities(ctx, []store.Identity{
		{ProjectID: 1, Kind: store.KindUser, ID: "u1", Name: "Ada"},
	}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if err := db.UpsertIdentities(ctx, []store.Identity{
		{ProjectID: 1, Kind: store.KindUser, ID: "u1", Name: "Ada Lovelace"},
		{ProjectID: 1, Kind: store.KindGroup, ID: "org9", Name: "Acme"},
		{ProjectID: 1, Kind: store.KindUser, ID: "", Name: "skipped"},
		{ProjectID: 1, Kind: store.KindUser, ID: "u2", Name: ""},
	}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	var n int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM identities`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	var name string
	if err := db.db.QueryRowContext(ctx,
		`SELECT name FROM identities WHERE project_id=1 AND kind='user' AND id='u1'`).
		Scan(&name); err != nil {
		t.Fatal(err)
	}
	if n != 2 || name != "Ada Lovelace" {
		t.Errorf("rows=%d name=%q; want 2 rows and the latest name", n, name)
	}
}

func TestUpsertIdentitiesEmptyIsNoop(t *testing.T) {
	db := newTestDB(t)
	if err := db.UpsertIdentities(context.Background(), nil); err != nil {
		t.Fatalf("empty upsert: %v", err)
	}
}

func TestWriteViewsStoresHost(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	views := []store.View{{
		ID: "h1", ProjectID: 1, TS: ts("2026-08-10T10:00:00Z"),
		Kind: "web", ActorID: "v1", ActorKind: store.ActorConnection, Host: "shop.example.com", Path: "/pricing",
	}}
	if err := db.WriteViews(ctx, views); err != nil {
		t.Fatal(err)
	}
	var host string
	if err := db.db.QueryRow(
		`SELECT host FROM views WHERE id='h1'`).Scan(&host); err != nil {
		t.Fatal(err)
	}
	if host != "shop.example.com" {
		t.Errorf("host = %q, want %q", host, "shop.example.com")
	}
}

// A view written without a host must read back as the empty string, not
// NULL: every consumer scans into a string and the column is NOT NULL.
func TestWriteViewsHostDefaultsEmpty(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.WriteViews(ctx, []store.View{{
		ID: "h2", ProjectID: 1, TS: ts("2026-08-10T10:00:00Z"),
		Kind: "web", ActorID: "v1", ActorKind: store.ActorConnection, Path: "/pricing",
	}}); err != nil {
		t.Fatal(err)
	}
	var host string
	if err := db.db.QueryRow(
		`SELECT host FROM views WHERE id='h2'`).Scan(&host); err != nil {
		t.Fatal(err)
	}
	if host != "" {
		t.Errorf("host = %q, want empty", host)
	}
}
