package reporting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/sortkey"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Presets is the closed vocabulary of a dashboard's stored range (D18).
// The UI resolves each to dates; Go knows them only to validate.
var Presets = []string{"today", "yesterday", "7d", "30d", "90d", "custom"}

// WidgetSpec is a widget as create_dashboard and add_widget take it.
type WidgetSpec struct {
	Name      string          `json:"name,omitempty" jsonschema:"unique on the dashboard; derived from the title when omitted"`
	Component string          `json:"component" jsonschema:"a component name from list_components"`
	Title     string          `json:"title,omitempty"`
	Props     json.RawMessage `json:"props,omitempty"`
	Source    Source          `json:"source"`
	Width     int             `json:"width,omitempty" jsonschema:"columns out of 12 (1–12); default from the component"`
	Height    int             `json:"height,omitempty" jsonschema:"rows of 40px (1–12); default from the component"`
}

// After, in every input below, places an item: nil last, 0 first, an id
// right after that item (Deviation 1).

type CreateDashboard struct {
	Title, Range string
	After        *int64
	Widgets      []WidgetSpec
}

// UpdateDashboard changes the title when Title is not "", and moves the
// dashboard when After is not nil.
type UpdateDashboard struct {
	ID    int64
	Title string
	After *int64
}

type AddWidget struct {
	DashboardID int64
	After       *int64
	WidgetSpec
}

// UpdateWidget changes the fields that are set (non-nil).
type UpdateWidget struct {
	ID        int64
	Name      *string
	Component *string
	Title     *string
	Props     json.RawMessage
	Source    *Source
	Width     *int
	Height    *int
}

type CopyWidget struct {
	ID, DashboardID int64
	After           *int64
}

// View is a viewer's selection on a dashboard: each part only when the
// dashboard has that switcher, From and To only with Range "custom".
type View struct {
	DashboardID, ProjectID int64
	Range, From, To        string
}

// CreateDashboard creates a user dashboard with its widgets, all or
// nothing: every widget is checked before anything is written.
func (s *Service) CreateDashboard(ctx context.Context, actor string, in CreateDashboard) (DashboardDetail, error) {
	if strings.TrimSpace(in.Title) == "" {
		return DashboardDetail{}, store.Refuse(store.ErrInvalid, "title must not be empty")
	}
	rng := in.Range
	if rng == "" {
		rng = "7d"
	}
	if err := checkPreset(rng); err != nil {
		return DashboardDetail{}, err
	}
	if rng == "custom" {
		return DashboardDetail{}, store.Refuse(store.ErrInvalid, "create with a preset; the viewer picks custom dates")
	}
	comps, err := s.components(ctx)
	if err != nil {
		return DashboardDetail{}, err
	}
	ws, err := buildWidgets(comps, in.Widgets, map[string]bool{})
	if err != nil {
		return DashboardDetail{}, err
	}
	keys, err := sortkey.Spread("", "", len(ws))
	if err != nil {
		return DashboardDetail{}, err
	}
	for i := range ws {
		if err := s.validateWidget(ctx, comps, ws[i]); err != nil {
			return DashboardDetail{}, inWidget(ws[i].Name, err)
		}
		ws[i].SortKey = keys[i]
	}
	var id int64
	err = retryConflict(func() error {
		key, err := s.dashboardKey(ctx, 0, in.After)
		if err != nil {
			return err
		}
		id, err = s.st.InsertDashboard(ctx,
			store.Dashboard{Owner: store.OwnerUser, Title: in.Title, SortKey: key, LastRange: rng},
			ws, store.AuditEntry{Actor: actor, Action: "dashboard.create"})
		return err
	})
	if err != nil {
		return DashboardDetail{}, err
	}
	return s.Dashboard(ctx, id)
}

// UpdateDashboard retitles and/or moves a user dashboard.
func (s *Service) UpdateDashboard(ctx context.Context, actor string, in UpdateDashboard) (DashboardInfo, error) {
	d, err := s.editableDashboard(ctx, in.ID)
	if err != nil {
		return DashboardInfo{}, err
	}
	if in.Title != "" {
		if strings.TrimSpace(in.Title) == "" {
			return DashboardInfo{}, store.Refuse(store.ErrInvalid, "title must not be empty")
		}
		d.Title = in.Title
	}
	err = retryConflict(func() error {
		if in.After != nil && *in.After != d.ID { // after itself: stays where it is
			key, err := s.dashboardKey(ctx, d.ID, in.After)
			if err != nil {
				return err
			}
			d.SortKey = key
		}
		return s.st.UpdateDashboard(ctx, d, store.AuditEntry{Actor: actor, Action: "dashboard.update"})
	})
	if err != nil {
		return DashboardInfo{}, err
	}
	d, err = s.st.GetDashboard(ctx, d.ID)
	return dashboardInfo(d), err
}

// DuplicateDashboard makes a user copy of any dashboard, system ones
// included, placed last: its live widgets in order with fresh keys and
// the same content, and the same stored selection. The widgets are
// copied as they are, not re-checked, so a copy is exact even when one
// no longer validates (a removed component).
func (s *Service) DuplicateDashboard(ctx context.Context, actor string, id int64) (DashboardDetail, error) {
	src, err := s.st.GetDashboard(ctx, id)
	if err != nil {
		return DashboardDetail{}, err
	}
	all, err := s.st.ListWidgets(ctx, id)
	if err != nil {
		return DashboardDetail{}, err
	}
	var ws []store.Widget
	for _, w := range all {
		if w.ArchivedAt == "" {
			ws = append(ws, freshCopy(w))
		}
	}
	keys, err := sortkey.Spread("", "", len(ws))
	if err != nil {
		return DashboardDetail{}, err
	}
	for i := range ws {
		ws[i].SortKey = keys[i]
	}
	copyOf := store.Dashboard{
		Owner: store.OwnerUser, Title: src.Title + " (copy)",
		LastProjectID: src.LastProjectID, LastRange: src.LastRange, LastFrom: src.LastFrom, LastTo: src.LastTo,
	}
	var newID int64
	err = retryConflict(func() error {
		key, err := s.dashboardKey(ctx, 0, nil)
		if err != nil {
			return err
		}
		copyOf.SortKey = key
		newID, err = s.st.InsertDashboard(ctx, copyOf, ws, store.AuditEntry{
			Actor: actor, Action: "dashboard.duplicate", Detail: fmt.Sprintf("from dashboard/%d", id)})
		return err
	})
	if err != nil {
		return DashboardDetail{}, err
	}
	return s.Dashboard(ctx, newID)
}

func (s *Service) ArchiveDashboard(ctx context.Context, actor string, id int64) error {
	return s.setDashboardArchived(ctx, actor, id, true)
}

func (s *Service) RestoreDashboard(ctx context.Context, actor string, id int64) error {
	return s.setDashboardArchived(ctx, actor, id, false)
}

func (s *Service) setDashboardArchived(ctx context.Context, actor string, id int64, archived bool) error {
	d, err := s.st.GetDashboard(ctx, id)
	if err != nil {
		return err
	}
	if err := refuseSystem(d); err != nil {
		return err
	}
	return s.st.SetDashboardArchived(ctx, id, archived,
		store.AuditEntry{Actor: actor, Action: archiveAction("dashboard", archived)})
}

// AddWidget adds one widget to a user dashboard.
func (s *Service) AddWidget(ctx context.Context, actor string, in AddWidget) (WidgetInfo, error) {
	d, err := s.editableDashboard(ctx, in.DashboardID)
	if err != nil {
		return WidgetInfo{}, err
	}
	comps, err := s.components(ctx)
	if err != nil {
		return WidgetInfo{}, err
	}
	existing, err := s.st.ListWidgets(ctx, d.ID)
	if err != nil {
		return WidgetInfo{}, err
	}
	ws, err := buildWidgets(comps, []WidgetSpec{in.WidgetSpec}, takenNames(existing, 0))
	if err != nil {
		return WidgetInfo{}, err
	}
	w := ws[0]
	w.DashboardID = d.ID
	if err := s.validateWidget(ctx, comps, w); err != nil {
		return WidgetInfo{}, err
	}
	return s.insertWidget(ctx, w, in.After, store.AuditEntry{Actor: actor, Action: "widget.add"})
}

// UpdateWidget changes the fields in that are set and checks the whole
// resulting widget. A widget whose component was removed takes only a
// new component and a size until it has one again (D15).
func (s *Service) UpdateWidget(ctx context.Context, actor string, in UpdateWidget) (WidgetInfo, error) {
	w, err := s.liveWidget(ctx, in.ID)
	if err != nil {
		return WidgetInfo{}, err
	}
	if _, err := s.editableDashboard(ctx, w.DashboardID); err != nil {
		return WidgetInfo{}, err
	}
	if w.Component == "" && (in.Name != nil || in.Title != nil || in.Props != nil || in.Source != nil) {
		return WidgetInfo{}, refuseRemoved(w.ID)
	}
	if in.Name != nil && *in.Name != w.Name {
		if *in.Name == "" {
			return WidgetInfo{}, store.Refuse(store.ErrInvalid, "name must not be empty; omit it to keep the current one")
		}
		siblings, err := s.st.ListWidgets(ctx, w.DashboardID)
		if err != nil {
			return WidgetInfo{}, err
		}
		if takenNames(siblings, w.ID)[*in.Name] {
			return WidgetInfo{}, refuseNameTaken(*in.Name)
		}
		w.Name = *in.Name
	}
	if in.Component != nil {
		w.Component = *in.Component
	}
	if in.Title != nil {
		w.Title = *in.Title
	}
	if in.Props != nil {
		w.Props = string(in.Props)
	}
	if in.Source != nil {
		w.SourceType, w.Source = in.Source.Type, in.Source.Content
	}
	if in.Width != nil {
		w.Width = *in.Width
	}
	if in.Height != nil {
		w.Height = *in.Height
	}
	if w.Component == "" { // still removed: only resized
		err = checkSize(w.Width, w.Height)
	} else {
		var comps map[string]Component
		if comps, err = s.components(ctx); err == nil {
			err = s.validateWidget(ctx, comps, w)
		}
	}
	if err != nil {
		return WidgetInfo{}, err
	}
	if err := s.st.UpdateWidget(ctx, w, store.AuditEntry{Actor: actor, Action: "widget.update"}); err != nil {
		return WidgetInfo{}, err
	}
	return s.readWidget(ctx, w.ID)
}

// CopyWidget copies a widget, a system one included, onto a user
// dashboard, keeping its size; its name gets a suffix if taken there.
func (s *Service) CopyWidget(ctx context.Context, actor string, in CopyWidget) (WidgetInfo, error) {
	src, err := s.liveWidget(ctx, in.ID)
	if err != nil {
		return WidgetInfo{}, err
	}
	if src.Component == "" {
		return WidgetInfo{}, refuseRemoved(src.ID)
	}
	d, err := s.editableDashboard(ctx, in.DashboardID)
	if err != nil {
		return WidgetInfo{}, err
	}
	existing, err := s.st.ListWidgets(ctx, d.ID)
	if err != nil {
		return WidgetInfo{}, err
	}
	w := freshCopy(src)
	w.DashboardID = d.ID
	if taken := takenNames(existing, 0); taken[w.Name] {
		w.Name = deriveName(w.Name, taken)
	}
	comps, err := s.components(ctx)
	if err != nil {
		return WidgetInfo{}, err
	}
	if err := s.validateWidget(ctx, comps, w); err != nil {
		return WidgetInfo{}, err
	}
	return s.insertWidget(ctx, w, in.After, store.AuditEntry{
		Actor: actor, Action: "widget.copy", Detail: fmt.Sprintf("from widget/%d", src.ID)})
}

func (s *Service) ArchiveWidget(ctx context.Context, actor string, id int64) error {
	return s.setWidgetArchived(ctx, actor, id, true)
}

// RestoreWidget unhides a widget where it was: it kept its sort key.
func (s *Service) RestoreWidget(ctx context.Context, actor string, id int64) error {
	return s.setWidgetArchived(ctx, actor, id, false)
}

func (s *Service) setWidgetArchived(ctx context.Context, actor string, id int64, archived bool) error {
	w, err := s.st.GetWidget(ctx, id)
	if err != nil {
		return err
	}
	d, err := s.st.GetDashboard(ctx, w.DashboardID)
	if err != nil {
		return err
	}
	if err := refuseSystem(d); err != nil {
		return err
	}
	return s.st.SetWidgetArchived(ctx, id, archived,
		store.AuditEntry{Actor: actor, Action: archiveAction("widget", archived)})
}

// SetView stores a viewer's selection. Which parts it takes follows from
// the dashboard's switchers, i.e. what its live widgets read; the parts
// it has no switcher for keep their stored values. Viewer state, not
// definition: allowed on system dashboards, and not audited.
func (s *Service) SetView(ctx context.Context, in View) error {
	d, err := s.st.GetDashboard(ctx, in.DashboardID)
	if err != nil {
		return err
	}
	ws, err := s.st.ListWidgets(ctx, d.ID)
	if err != nil {
		return err
	}
	var hasProject, hasRange bool
	for _, w := range ws {
		if w.ArchivedAt == "" {
			p, r := s.follows(w)
			hasProject, hasRange = hasProject || p, hasRange || r
		}
	}
	switch {
	case hasProject && in.ProjectID == 0:
		return store.Refuse(store.ErrInvalid, "project_id is required: dashboard %d has a project switcher", d.ID)
	case !hasProject && in.ProjectID != 0:
		return store.Refuse(store.ErrInvalid, "dashboard %d has no project switcher; omit project_id", d.ID)
	case hasProject:
		d.LastProjectID = in.ProjectID
	}
	switch {
	case hasRange && in.Range == "":
		return store.Refuse(store.ErrInvalid, "range is required: dashboard %d has a range switcher", d.ID)
	case !hasRange && (in.Range != "" || in.From != "" || in.To != ""):
		return store.Refuse(store.ErrInvalid, "dashboard %d has no range switcher; omit range, from and to", d.ID)
	case hasRange:
		if err := checkPreset(in.Range); err != nil {
			return err
		}
		if in.Range == "custom" {
			if err := checkDates(in.From, in.To); err != nil {
				return err
			}
		} else if in.From != "" || in.To != "" {
			return store.Refuse(store.ErrInvalid, "from and to go only with range custom")
		}
		d.LastRange, d.LastFrom, d.LastTo = in.Range, in.From, in.To
	}
	return s.st.SetDashboardView(ctx, d)
}

// checkPreset refuses a range outside Presets.
func checkPreset(r string) error {
	if !slices.Contains(Presets, r) {
		return store.Refuse(store.ErrInvalid, "range must be one of %s", strings.Join(Presets, ", "))
	}
	return nil
}

// checkDates refuses a custom range that is not two days, from ≤ to, at
// most 365 days apart.
func checkDates(from, to string) error {
	f, errFrom := time.Parse(time.DateOnly, from)
	t, errTo := time.Parse(time.DateOnly, to)
	if errFrom != nil || errTo != nil {
		return store.Refuse(store.ErrInvalid, "from and to are days, YYYY-MM-DD")
	}
	if f.After(t) {
		return store.Refuse(store.ErrInvalid, "from %s is after to %s", from, to)
	}
	if t.Sub(f) > 365*24*time.Hour {
		return store.Refuse(store.ErrInvalid, "from %s to %s spans more than 365 days", from, to)
	}
	return nil
}

// --- placement ---

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
func retryConflict(place func() error) error {
	err := place()
	if errors.Is(err, store.ErrConflict) {
		err = place()
	}
	return err
}

// dashboardKey places a dashboard among the user dashboards, leaving out
// self (the one being moved; 0 for a new one).
func (s *Service) dashboardKey(ctx context.Context, self int64, after *int64) (string, error) {
	ds, err := s.st.ListDashboards(ctx)
	if err != nil {
		return "", err
	}
	var order []placed
	for _, d := range ds {
		if d.Owner == store.OwnerUser && d.ID != self {
			order = append(order, placed{d.ID, d.SortKey})
		}
	}
	return keyAfter(order, after, func(id int64) error {
		return store.Refuse(store.ErrInvalid, "after %d is not a user dashboard", id)
	})
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
	})
	if err != nil {
		return WidgetInfo{}, err
	}
	return s.readWidget(ctx, id)
}

// --- loading and refusing ---

// refuseSystem refuses a write to a system dashboard: those change only
// through the release's migrator (D11).
func refuseSystem(d store.Dashboard) error {
	if d.Owner == store.OwnerSystem {
		return store.Refuse(store.ErrInvalid,
			"dashboard %d is a system dashboard and changes only with a release; duplicate_dashboard makes an editable copy", d.ID)
	}
	return nil
}

// editableDashboard loads dashboard id for a write that changes it or
// what it holds: refused on a system or an archived dashboard.
func (s *Service) editableDashboard(ctx context.Context, id int64) (store.Dashboard, error) {
	d, err := s.st.GetDashboard(ctx, id)
	if err != nil {
		return store.Dashboard{}, err
	}
	if err := refuseSystem(d); err != nil {
		return store.Dashboard{}, err
	}
	if d.ArchivedAt != "" {
		return store.Dashboard{}, store.Refuse(store.ErrInvalid, "dashboard %d is archived; restore_dashboard first", id)
	}
	return d, nil
}

// liveWidget loads widget id for an update or a copy: refused when
// archived.
func (s *Service) liveWidget(ctx context.Context, id int64) (store.Widget, error) {
	w, err := s.st.GetWidget(ctx, id)
	if err != nil {
		return store.Widget{}, err
	}
	if w.ArchivedAt != "" {
		return store.Widget{}, store.Refuse(store.ErrInvalid, "widget %d is archived; restore_widget first", id)
	}
	return w, nil
}

func (s *Service) readWidget(ctx context.Context, id int64) (WidgetInfo, error) {
	w, err := s.st.GetWidget(ctx, id)
	if err != nil {
		return WidgetInfo{}, err
	}
	return s.widgetInfo(w), nil
}

func refuseRemoved(id int64) error {
	return store.Refuse(store.ErrInvalid, "widget %d's component was removed; set component first", id)
}

func refuseNameTaken(name string) error {
	return store.Refuse(store.ErrConflict, "widget name %s is already used on this dashboard", name)
}

// inWidget prefixes a refusal of one widget in a batch with its name.
func inWidget(name string, err error) error {
	var r *store.Refusal
	if errors.As(err, &r) {
		return store.Refuse(r.Kind, "widget %s: %s", name, r.Msg)
	}
	return err
}

// takenNames is every widget name in ws but except's: names stay unique
// on a dashboard across live and archived widgets alike.
func takenNames(ws []store.Widget, except int64) map[string]bool {
	taken := make(map[string]bool, len(ws))
	for _, w := range ws {
		if w.ID != except {
			taken[w.Name] = true
		}
	}
	return taken
}

// buildWidgets turns specs into rows (no dashboard, no key yet): given
// names are claimed first and must be free in taken; the rest are
// derived from their titles; sizes left at 0 take the component's
// defaults. taken gains every name used.
func buildWidgets(comps map[string]Component, specs []WidgetSpec, taken map[string]bool) ([]store.Widget, error) {
	for _, sp := range specs {
		if sp.Name == "" {
			continue
		}
		if taken[sp.Name] {
			return nil, refuseNameTaken(sp.Name)
		}
		taken[sp.Name] = true
	}
	out := make([]store.Widget, 0, len(specs))
	for _, sp := range specs {
		w := store.Widget{
			Component: sp.Component, Name: sp.Name, Title: sp.Title, Props: string(sp.Props),
			SourceType: sp.Source.Type, Source: sp.Source.Content, Width: sp.Width, Height: sp.Height,
		}
		if w.Name == "" {
			w.Name = deriveName(sp.Title, taken)
			taken[w.Name] = true
		}
		if w.Props == "" {
			w.Props = "{}"
		}
		if c, ok := comps[w.Component]; ok { // an unknown one is refused by validateWidget
			if w.Width == 0 {
				w.Width = c.DefaultWidth
			}
			if w.Height == 0 {
				w.Height = c.DefaultHeight
			}
		}
		out = append(out, w)
	}
	return out, nil
}

// freshCopy is w's content as a new row: no id, dashboard, key, times or
// archived state.
func freshCopy(w store.Widget) store.Widget {
	return store.Widget{
		Component: w.Component, Width: w.Width, Height: w.Height,
		Name: w.Name, Title: w.Title, Props: w.Props, SourceType: w.SourceType, Source: w.Source,
	}
}

func archiveAction(kind string, archived bool) string {
	if archived {
		return kind + ".archive"
	}
	return kind + ".restore"
}
