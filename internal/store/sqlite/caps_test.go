package sqlite

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Each cap (ATTRIBUTE_VALUES_TOP_N, which also caps the views breakdowns,
// and IDENTITIES_TOP_N) reaches the live halves through meta and the daily
// pass as an argument; the two must agree for every setting, 0 (no cap)
// included, or a day's numbers jump when it rolls up. The fixture has 110
// plan values, 600 paths and kinds, and 600 users and groups on one day.
func TestCapsAgreeAcrossRollup(t *testing.T) {
	for _, n := range []int{0, 3, 50} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			db := newTestDB(t)
			ctx := context.Background()
			for _, key := range []string{"attributes_top_n", "identities_top_n"} {
				if err := db.SetMeta(ctx, key, strconv.Itoa(n)); err != nil {
					t.Fatal(err)
				}
			}
			id := seedDeclaredProject(t, db, []string{"plan"})
			seedAttrDay(t, db, id)
			var views []store.Event
			base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
			for i := 0; i < 1200; i++ {
				ts := base.Add(time.Duration(i) * time.Minute)
				views = append(views, store.Event{Family: store.FamilyViews, ID: fmt.Sprintf("v%04d", i),
					ProjectID: id, TS: ts, ReceivedAt: ts, Kind: fmt.Sprintf("k%d", i%600),
					ActorID: fmt.Sprintf("a%d", i%70), ActorKind: store.ActorConnection,
					Path: fmt.Sprintf("/p/%d", i%600), UserID: fmt.Sprintf("u%d", i%600),
					GroupID: fmt.Sprintf("g%d", i%600)})
			}
			if err := db.WriteEvents(ctx, views); err != nil {
				t.Fatal(err)
			}

			before := typedViews(t, db)
			count := func(q string) int {
				t.Helper()
				var c int
				if err := db.db.QueryRow(q, id).Scan(&c); err != nil {
					t.Fatal(err)
				}
				return c
			}
			plans := count(`SELECT COUNT(*) FROM v_product_attrs WHERE project_id = ? AND attr_key = 'plan'`)
			paths := count(`SELECT COUNT(*) FROM v_views_paths WHERE project_id = ?`)
			kinds := count(`SELECT COUNT(*) FROM v_views_daily WHERE project_id = ?`)
			users := count(`SELECT COUNT(*) FROM v_identity_daily WHERE project_id = ? AND kind = 'user'`)
			// Capped: n kept plus one (other) row (identities: n, no
			// (other)). Uncapped: every value, no (other). seedAttrDay's
			// one ping event adds a plan row of its own.
			wantPlans, wantPaths, wantUsers := n+1+1, n+1, n
			if n == 0 {
				wantPlans, wantPaths, wantUsers = defaultAttrsTopN+10+1, 600, 600
			}
			if plans != wantPlans || paths != wantPaths || kinds != wantPaths || users != wantUsers {
				t.Errorf("rows: plans %d paths %d kinds %d users %d, want %d %d %d %d",
					plans, paths, kinds, users, wantPlans, wantPaths, wantPaths, wantUsers)
			}

			d := civil.DateOf(base)
			if err := db.AggregateIdentityDay(ctx, id, d, n); err != nil {
				t.Fatal(err)
			}
			if err := db.AggregateViewDay(ctx, id, d, n); err != nil {
				t.Fatal(err)
			}
			if err := db.AggregateProductDay(ctx, id, d, []string{"plan"}, n); err != nil {
				t.Fatal(err)
			}
			after := typedViews(t, db)
			for v, rows := range before {
				if !reflect.DeepEqual(rows, after[v]) {
					t.Errorf("%s changed when the day rolled up with cap %d:\nbefore %v\nafter  %v", v, n, rows, after[v])
				}
			}
		})
	}
}
