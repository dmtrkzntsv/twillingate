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

// placeDashboard writes row (already retitled if asked) at the place in
// asks for, computed over o, the user order read for this attempt.
func (s *Service) placeDashboard(ctx context.Context, o order, row store.Dashboard, in UpdateDashboard, a store.AuditEntry) error {
	switch {
	case in.GroupID != nil && *in.GroupID == 0:
		return s.leaveGroup(ctx, o, row, in.After, a)

	case in.GroupID != nil:
		g := *in.GroupID
		if err := refuseGroup(o, g); err != nil {
			return err
		}
		if in.After != nil && *in.After == row.ID && row.GroupID == g {
			break // after itself in its own group: stays where it is
		}
		key, err := o.keyInGroup(row.ID, g, in.After)
		if err != nil {
			return err
		}
		if g != row.GroupID {
			heirs := o.handOver(row)
			row.GroupID, row.SortKey = g, key
			return s.writePlaced(ctx, row, heirs, a)
		}
		row.SortKey = key

	case in.After != nil && *in.After != row.ID: // after itself: stays where it is
		if x, ok := o.find(*in.After); ok && x.GroupID == row.GroupID {
			key, err := o.keyInGroup(row.ID, row.GroupID, in.After) // among its tabs
			if err != nil {
				return err
			}
			row.SortKey = key
			break
		}
		keys, err := o.moveGroup(row.GroupID, *in.After) // with its whole group
		if err != nil {
			return err
		}
		if keys == nil {
			break // already there: only the title, if any, below
		}
		// MoveDashboards audits under its first key's dashboard: name
		// the one the caller moved. The keys are parked before any is
		// written, so their order is free.
		i := slices.IndexFunc(keys, func(k store.DashboardKey) bool { return k.ID == row.ID })
		keys[0], keys[i] = keys[i], keys[0]
		if err := s.st.MoveDashboards(ctx, keys, store.GroupRekey{}, a); err != nil {
			return err
		}
		if in.Title == "" {
			return nil
		}
		// A new title is a second write (and a second audit row): the
		// move rewrites keys only. row takes its new key so the title
		// write keeps it.
		row.SortKey = keys[0].SortKey
	}
	return s.st.UpdateDashboard(ctx, row, a)
}

// leaveGroup takes row out of its group as a group of one (group_id: 0),
// placed by after in the sidebar, or right after the group it left.
//
// A dashboard already alone is a group of one: it keeps its group id and
// moves only if after says so. One with others takes its own id as its
// group id: that id is free, because a group whose id it was is handed
// to another member as row leaves (order.handOver, spec decision 2).
func (s *Service) leaveGroup(ctx context.Context, o order, row store.Dashboard, after *int64, a store.AuditEntry) error {
	if after != nil && *after == row.ID {
		after = nil // after itself: no place of its own to name
	}
	others := o.without(row.ID).group(row.GroupID)
	var heirs []store.DashboardKey
	if len(others) > 0 {
		heirs = o.handOver(row)
		row.GroupID = row.ID
		if after == nil {
			after = &others[len(others)-1].ID
		}
	}
	if after != nil {
		key, err := o.keyAfterGroup(row.ID, after)
		if err != nil {
			return err
		}
		row.SortKey = key
	}
	return s.writePlaced(ctx, row, heirs, a)
}

// writePlaced writes row at its new group and key. When row leaves a
// group that used its id, heirs repoints the members left behind in the
// same transaction (MoveDashboards), so no moment exists where two
// groups share a number; a title change, if any, is a second write. The
// group's name, if it has one, moves with the heirs (D3): heirs is
// non-empty only when row founded the group, so its old number is row.ID.
func (s *Service) writePlaced(ctx context.Context, row store.Dashboard, heirs []store.DashboardKey, a store.AuditEntry) error {
	if len(heirs) == 0 {
		return s.st.UpdateDashboard(ctx, row, a)
	}
	// MoveDashboards audits under its first key's dashboard: row, the one
	// the caller moved.
	keys := append([]store.DashboardKey{{ID: row.ID, GroupID: row.GroupID, SortKey: row.SortKey}}, heirs...)
	rekey := store.GroupRekey{From: row.ID, To: heirs[0].GroupID}
	if err := s.st.MoveDashboards(ctx, keys, rekey, a); err != nil {
		return err
	}
	return s.st.UpdateDashboard(ctx, row, a)
}

// insertWidget places w on its dashboard after `after` and writes it,
// returning what was written.
func (s *Service) insertWidget(ctx context.Context, w store.Widget, after *int64, a store.AuditEntry) (WidgetInfo, error) {
	var id int64
	err := retryConflict(func() error {
		key, err := s.widgetKeyAfter(ctx, w, after)
		if err != nil {
			return err
		}
		w.SortKey = key
		id, err = s.st.InsertWidget(ctx, w, a)
		return err
	}, func() error { return s.lostWidgetRace(ctx, w) })
	if err != nil {
		return WidgetInfo{}, err
	}
	return s.readWidget(ctx, id)
}

// widgetKeyAfter reads w's dashboard's order afresh and returns a key
// that places w after `after` (keyAfter's nil, 0 or id). w itself is left
// out of the order, so a widget being moved is placed among the others.
func (s *Service) widgetKeyAfter(ctx context.Context, w store.Widget, after *int64) (string, error) {
	ws, err := s.st.ListWidgets(ctx, w.DashboardID)
	if err != nil {
		return "", err
	}
	order := make([]placed, 0, len(ws))
	for _, x := range ws {
		if x.ID == w.ID {
			continue
		}
		// An archived widget still holds its key (order keeps it so a
		// new key never takes it), but after may not name one: the
		// page shows nothing to place the widget by.
		if after != nil && x.ID == *after && x.ArchivedAt != "" {
			return "", store.Refuse(store.ErrInvalid, "after %d is archived; name a live widget", x.ID)
		}
		order = append(order, placed{x.ID, x.SortKey})
	}
	return keyAfter(order, after, func(id int64) error {
		return store.Refuse(store.ErrInvalid, "after %d is not a widget on dashboard %d", id, w.DashboardID)
	})
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
