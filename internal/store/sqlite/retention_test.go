package sqlite

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func onDay(y int, m time.Month, d int) civil.Date { return civil.Date{Year: y, Month: m, Day: d} }

func viewAt(id, actor string, t time.Time) store.Event {
	return store.Event{Family: store.FamilyViews, ID: id, ProjectID: 1, TS: t, ReceivedAt: t,
		Kind: "app", ActorID: actor, ActorKind: store.ActorInstall, Path: "/x", OS: "ios"}
}

func TestUpsertActorsTracksFirstAndLastSeen(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	d1 := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)

	if err := db.WriteEvents(ctx, []store.Event{viewAt("1", "a", d1)}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertActors(ctx, 1, onDay(2026, 8, 1)); err != nil {
		t.Fatalf("upsert day 1: %v", err)
	}
	if err := db.WriteEvents(ctx, []store.Event{viewAt("2", "a", d2)}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertActors(ctx, 1, onDay(2026, 8, 8)); err != nil {
		t.Fatalf("upsert day 8: %v", err)
	}

	var first, last, actorKind string
	if err := db.db.QueryRowContext(ctx,
		`SELECT first_seen_day, last_seen_day, actor_kind FROM actors WHERE project_id=1 AND actor_id='a'`).
		Scan(&first, &last, &actorKind); err != nil {
		t.Fatalf("read actor: %v", err)
	}
	if first != "2026-08-01" || last != "2026-08-08" || actorKind != store.ActorInstall {
		t.Errorf("actor = %q %q %q; want 2026-08-01 2026-08-08 install", first, last, actorKind)
	}
}

func TestUpsertActorsRecordsUserActorKind(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	ts := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	if err := db.WriteEvents(ctx, []store.Event{
		{Family: store.FamilyViews, ID: "1", ProjectID: 1, TS: ts, ReceivedAt: ts, Kind: "web", ActorID: "w",
			ActorKind: store.ActorUser, UserID: "u1", Path: "/"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertActors(ctx, 1, onDay(2026, 8, 1)); err != nil {
		t.Fatal(err)
	}

	var actorKind string
	if err := db.db.QueryRowContext(ctx,
		`SELECT actor_kind FROM actors WHERE project_id=1 AND actor_id='w'`).Scan(&actorKind); err != nil {
		t.Fatal(err)
	}
	if actorKind != store.ActorUser {
		t.Errorf("actor_kind = %q, want user", actorKind)
	}
}

func TestUpsertActorsSkipsConnectionActors(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedViews(t, db,
		store.Event{Family: store.FamilyViews, ID: "1", TS: at(10, 0), ActorID: "hash-1", ActorKind: store.ActorConnection, Path: "/"},
		store.Event{Family: store.FamilyViews, ID: "2", TS: at(10, 0), ActorID: "u1", ActorKind: store.ActorUser, UserID: "u1", Path: "/"},
		store.Event{Family: store.FamilyViews, ID: "3", TS: at(10, 0), ActorID: "i1", ActorKind: store.ActorInstall, Path: "/", Kind: "app"},
	)
	if err := db.WriteEvents(ctx, []store.Event{
		{Family: store.FamilyProduct, ID: "4", ProjectID: 1, EventName: "x", TS: at(10, 0), ReceivedAt: at(10, 0), ActorID: "legacy", ActorKind: ""},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertActors(ctx, 1, day("2026-08-10")); err != nil {
		t.Fatal(err)
	}
	rows, err := db.db.Query(`SELECT actor_id, actor_kind FROM actors WHERE project_id=1 ORDER BY actor_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var id, kind string
		if err := rows.Scan(&id, &kind); err != nil {
			t.Fatal(err)
		}
		got[id] = kind
	}
	want := map[string]string{"u1": "user", "i1": "install"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("actors = %v, want %v (connection and legacy '' actors are never cohorted)", got, want)
	}
}

func TestUpsertActorsIgnoresEmptyActor(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	ts := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	if err := db.WriteEvents(ctx, []store.Event{
		{Family: store.FamilyViews, ID: "1", ProjectID: 1, TS: ts, ReceivedAt: ts, Kind: "app", ActorID: "", ActorKind: store.ActorInstall, Path: "/x"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertActors(ctx, 1, onDay(2026, 8, 1)); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM actors`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("actors rows = %d, want 0 for an empty actor id", n)
	}
}

func TestAggregateRetentionDayComputesOffsets(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	cohort := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	later := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)

	// Two actors on day 0; one returns on day 7.
	if err := db.WriteEvents(ctx, []store.Event{
		viewAt("1", "a", cohort), viewAt("2", "b", cohort),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertActors(ctx, 1, onDay(2026, 8, 1)); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateRetentionDay(ctx, 1, onDay(2026, 8, 1)); err != nil {
		t.Fatal(err)
	}

	if err := db.WriteEvents(ctx, []store.Event{viewAt("3", "a", later)}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertActors(ctx, 1, onDay(2026, 8, 8)); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateRetentionDay(ctx, 1, onDay(2026, 8, 8)); err != nil {
		t.Fatal(err)
	}

	var d0, d7 int
	if err := db.db.QueryRowContext(ctx,
		`SELECT actors FROM agg_retention WHERE project_id=1 AND actor_kind='install'
		   AND cohort_day='2026-08-01' AND day_offset=0`).Scan(&d0); err != nil {
		t.Fatalf("offset 0: %v", err)
	}
	if err := db.db.QueryRowContext(ctx,
		`SELECT actors FROM agg_retention WHERE project_id=1 AND actor_kind='install'
		   AND cohort_day='2026-08-01' AND day_offset=7`).Scan(&d7); err != nil {
		t.Fatalf("offset 7: %v", err)
	}
	if d0 != 2 || d7 != 1 {
		t.Errorf("cohort = d0 %d d7 %d; want 2 1", d0, d7)
	}
}

func TestRetentionViewExposesCohortSize(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	cohort := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	later := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)

	if err := db.WriteEvents(ctx, []store.Event{
		viewAt("1", "a", cohort), viewAt("2", "b", cohort),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertActors(ctx, 1, onDay(2026, 8, 1)); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateRetentionDay(ctx, 1, onDay(2026, 8, 1)); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteEvents(ctx, []store.Event{viewAt("3", "a", later)}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertActors(ctx, 1, onDay(2026, 8, 2)); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateRetentionDay(ctx, 1, onDay(2026, 8, 2)); err != nil {
		t.Fatal(err)
	}

	var actors, size int
	var actorKind string
	if err := db.db.QueryRowContext(ctx,
		`SELECT actors, cohort_size, actor_kind FROM v_retention
		 WHERE project_id=1 AND cohort_day='2026-08-01' AND day_offset=1`).
		Scan(&actors, &size, &actorKind); err != nil {
		t.Fatalf("v_retention: %v", err)
	}
	if actors != 1 || size != 2 || actorKind != store.ActorInstall {
		t.Errorf("v_retention d1 = actors %d of %d kind %q; want 1 of 2 install", actors, size, actorKind)
	}
}

func TestAggregateRetentionDayIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	cohort := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	if err := db.WriteEvents(ctx, []store.Event{viewAt("1", "a", cohort)}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertActors(ctx, 1, onDay(2026, 8, 1)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := db.AggregateRetentionDay(ctx, 1, onDay(2026, 8, 1)); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}

	var n, actors int
	if err := db.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(MAX(actors),0) FROM agg_retention WHERE project_id=1`).
		Scan(&n, &actors); err != nil {
		t.Fatal(err)
	}
	if n != 1 || actors != 1 {
		t.Errorf("rows=%d actors=%d after replay; want 1 and 1", n, actors)
	}
}

func TestUpsertActorsIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	ts := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	if err := db.WriteEvents(ctx, []store.Event{viewAt("1", "a", ts)}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := db.UpsertActors(ctx, 1, onDay(2026, 8, 1)); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	var n int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM actors`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("actors rows = %d, want 1", n)
	}
}

func TestPruneActorsEvictsStale(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	old := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	if err := db.WriteEvents(ctx, []store.Event{
		viewAt("1", "stale", old), viewAt("2", "fresh", recent),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertActors(ctx, 1, onDay(2025, 1, 1)); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertActors(ctx, 1, onDay(2026, 8, 20)); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateRetentionDay(ctx, 1, onDay(2025, 1, 1)); err != nil {
		t.Fatal(err)
	}
	if err := db.PruneActors(ctx, 1, onDay(2026, 1, 1)); err != nil {
		t.Fatalf("prune: %v", err)
	}

	var actors, cohorts int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM actors`).Scan(&actors); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agg_retention`).Scan(&cohorts); err != nil {
		t.Fatal(err)
	}
	if actors != 1 || cohorts != 0 {
		t.Errorf("after prune: actors=%d cohorts=%d; want 1 and 0", actors, cohorts)
	}
}

func TestUpsertActorsPromotesInstallToUser(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	d10 := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	d11 := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)

	// x is seen as both install and user on the same day, within the one
	// UpsertActors call: two rows for the same actor_id resolve through
	// ON CONFLICT within a single statement.
	// y is install-only on day 1, then logs in as a user on day 2 (a
	// separate UpsertActors call, and hence a separate conflict).
	// z is install-only throughout: the control that must never be promoted.
	if err := db.WriteEvents(ctx, []store.Event{
		{Family: store.FamilyViews, ID: "1", ProjectID: 1, TS: d10, ReceivedAt: d10, Kind: "app",
			ActorID: "x", ActorKind: store.ActorInstall, Path: "/x"},
		{Family: store.FamilyViews, ID: "2", ProjectID: 1, TS: d10, ReceivedAt: d10, Kind: "app",
			ActorID: "x", UserID: "x", ActorKind: store.ActorUser, Path: "/x"},
		{Family: store.FamilyViews, ID: "3", ProjectID: 1, TS: d10, ReceivedAt: d10, Kind: "app",
			ActorID: "y", ActorKind: store.ActorInstall, Path: "/x"},
		{Family: store.FamilyViews, ID: "4", ProjectID: 1, TS: d10, ReceivedAt: d10, Kind: "app",
			ActorID: "z", ActorKind: store.ActorInstall, Path: "/x"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertActors(ctx, 1, onDay(2026, 8, 10)); err != nil {
		t.Fatal(err)
	}

	if err := db.WriteEvents(ctx, []store.Event{
		{Family: store.FamilyViews, ID: "5", ProjectID: 1, TS: d11, ReceivedAt: d11, Kind: "app",
			ActorID: "y", UserID: "y", ActorKind: store.ActorUser, Path: "/x"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertActors(ctx, 1, onDay(2026, 8, 11)); err != nil {
		t.Fatal(err)
	}

	kindOf := func(actorID string) (kind, first string) {
		t.Helper()
		if err := db.db.QueryRowContext(ctx,
			`SELECT actor_kind, first_seen_day FROM actors WHERE project_id=1 AND actor_id=?`, actorID).
			Scan(&kind, &first); err != nil {
			t.Fatalf("read actor %s: %v", actorID, err)
		}
		return kind, first
	}

	if kind, first := kindOf("x"); kind != store.ActorUser || first != "2026-08-10" {
		t.Errorf("x = %q %q; want user 2026-08-10", kind, first)
	}
	if kind, first := kindOf("y"); kind != store.ActorUser || first != "2026-08-10" {
		t.Errorf("y = %q %q; want user 2026-08-10", kind, first)
	}
	if kind, _ := kindOf("z"); kind != store.ActorInstall {
		t.Errorf("z = %q, want install (never seen as a user)", kind)
	}

	if err := db.AggregateRetentionDay(ctx, 1, onDay(2026, 8, 11)); err != nil {
		t.Fatal(err)
	}
	var userActors int
	if err := db.db.QueryRowContext(ctx,
		`SELECT actors FROM agg_retention WHERE project_id=1 AND actor_kind='user'
		   AND cohort_day='2026-08-10' AND day_offset=1`).Scan(&userActors); err != nil {
		t.Fatalf("user cohort: %v", err)
	}
	if userActors != 1 {
		t.Errorf("user cohort d1 actors = %d, want 1 (y)", userActors)
	}
	var installActors int
	err := db.db.QueryRowContext(ctx,
		`SELECT actors FROM agg_retention WHERE project_id=1 AND actor_kind='install'
		   AND cohort_day='2026-08-10' AND day_offset=1`).Scan(&installActors)
	if err != sql.ErrNoRows {
		t.Errorf("install cohort d1 = actors %d err %v; want no row: y was promoted to user before this aggregation ran",
			installActors, err)
	}
}
