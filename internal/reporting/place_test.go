package reporting

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// TestDashboardPlacementIsSerialised runs placement writes that each read
// the order and write back what they computed from it, concurrently:
// group A moving from one end of the sidebar to the other, A's second
// tab being retitled (which writes back the sort key it read) and a tab
// joining group B. Every round must leave every group contiguous; an
// unserialised read-compute-write lets a stale key land A2 back where A
// was, splitting it.
func TestDashboardPlacementIsSerialised(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	a1 := mustCreate(t, svc, "A1")
	a2 := mustJoin(t, svc, "A2", a1.GroupID)
	b1 := mustCreate(t, svc, "B1")

	for round := range 10 {
		after := b1.ID // A after B, then back to the top
		if round%2 == 1 {
			after = 0
		}
		var wg sync.WaitGroup
		errs := make(chan error, 3)
		wg.Go(func() {
			_, err := svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: a1.ID, After: &after})
			errs <- err
		})
		wg.Go(func() {
			_, err := svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: a2.ID, Title: fmt.Sprintf("A2 %d", round)})
			errs <- err
		})
		wg.Go(func() {
			_, err := svc.CreateDashboard(ctx, "test", CreateDashboard{Title: fmt.Sprintf("B %d", round), GroupID: b1.GroupID})
			errs <- err
		})
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("round %d: %v", round, err)
			}
		}
		if o := userOrder(userRows(t, svc)); !contiguous(o) {
			t.Fatalf("round %d: groups split: %+v", round, o)
		}
	}
}
