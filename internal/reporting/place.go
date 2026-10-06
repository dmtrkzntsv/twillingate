package reporting

import (
	"context"
	"errors"
	"slices"

	"github.com/dmtrkzntsv/twillingate/internal/shared/sortkey"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// placed is one item of an order: its id and sort key.
type placed struct {
	id  int64
	key string
}

// keyAfter returns a key for an item placed after `after` in order
// (ascending, archived items included so a new key never takes an
// archived item's): nil last, 0 first, an id right after that item.
// notIn refuses an id order does not hold.
func keyAfter(order []placed, after *int64, notIn func(id int64) error) (string, error) {
	i := len(order) // the new item goes before order[i]
	if after != nil {
		if *after == 0 {
			i = 0
		} else {
			i = slices.IndexFunc(order, func(p placed) bool { return p.id == *after })
			if i < 0 {
				return "", notIn(*after)
			}
			i++
		}
	}
	var prev, next string
	if i > 0 {
		prev = order[i-1].key
	}
	if i < len(order) {
		next = order[i].key
	}
	return sortkey.Between(prev, next)
}

// retryConflict runs place, and runs it once more if another writer took
// the key it chose (ErrConflict): place reads the order afresh each time.
// A second conflict becomes lost(), a refusal in the caller's words
// rather than the store's, which names sort keys an agent never sees.
func retryConflict(place func() error, lost func() error) error {
	err := place()
	if errors.Is(err, store.ErrConflict) {
		err = place()
	}
	if errors.Is(err, store.ErrConflict) {
		return lost()
	}
	return err
}

// lostDashboardRace is retryConflict's lost() for placing a dashboard.
func lostDashboardRace() error {
	return store.Refuse(store.ErrConflict, "the dashboard order changed while placing this dashboard; try again")
}

// placeDashboards runs place, a dashboard placement's read of the order
// and the writes computed from it, under placeMu and through
// retryConflict.
func (s *Service) placeDashboards(place func() error) error {
	s.placeMu.Lock()
	defer s.placeMu.Unlock()
	return retryConflict(place, lostDashboardRace)
}

// readOrder reads the user dashboards in order, archived ones included:
// what every dashboard placement is computed over, read afresh on each
// retryConflict attempt.
func (s *Service) readOrder(ctx context.Context) (order, error) {
	ds, err := s.st.ListDashboards(ctx)
	if err != nil {
		return nil, err
	}
	return userOrder(ds), nil
}

// refuseGroup refuses group g unless a live user dashboard is in it. A
// dashboard joins a group by its live tabs, so a group that is unknown,
// wholly archived or another owner's (a system group: o holds only user
// rows) is refused alike (spec decisions 5 and 8).
func refuseGroup(o order, g int64) error {
	for _, d := range o {
		if d.GroupID == g && d.ArchivedAt == "" {
			return nil
		}
	}
	return store.Refuse(store.ErrInvalid, "group %d has no live user dashboard", g)
}

// insertWidget places w on its dashboard after `after` and writes it,
// returning what was written.
func (s *Service) insertWidget(ctx context.Context, w store.Widget, after *int64, a store.AuditEntry) (WidgetInfo, error) {
	var id int64
	err := retryConflict(func() error {
		ws, err := s.st.ListWidgets(ctx, w.DashboardID)
		if err != nil {
			return err
		}
		order := make([]placed, len(ws))
		for i, x := range ws {
			// An archived widget still holds its key (order keeps it so a
			// new key never takes it), but after may not name one: the
			// page shows nothing to place the new widget by.
			if after != nil && x.ID == *after && x.ArchivedAt != "" {
				return store.Refuse(store.ErrInvalid, "after %d is archived; name a live widget", x.ID)
			}
			order[i] = placed{x.ID, x.SortKey}
		}
		w.SortKey, err = keyAfter(order, after, func(id int64) error {
			return store.Refuse(store.ErrInvalid, "after %d is not a widget on dashboard %d", id, w.DashboardID)
		})
		if err != nil {
			return err
		}
		id, err = s.st.InsertWidget(ctx, w, a)
		return err
	}, func() error { return s.lostWidgetRace(ctx, w) })
	if err != nil {
		return WidgetInfo{}, err
	}
	return s.readWidget(ctx, id)
}

// lostWidgetRace explains a widget write that kept conflicting: the name
// was taken meanwhile, or the dashboard's order kept changing.
func (s *Service) lostWidgetRace(ctx context.Context, w store.Widget) error {
	ws, err := s.st.ListWidgets(ctx, w.DashboardID)
	if err != nil {
		return err
	}
	if takenNames(ws, w.ID)[w.Name] {
		return refuseNameTaken(w.Name)
	}
	return store.Refuse(store.ErrConflict,
		"dashboard %d changed while placing this widget; try again", w.DashboardID)
}
