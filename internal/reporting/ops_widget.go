package reporting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

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

type AddWidget struct {
	DashboardID int64
	After       *int64
	WidgetSpec
}

// UpdateWidget changes the fields that are set (non-nil). After moves
// the widget: after that live widget of its dashboard, 0 first; naming
// the widget itself leaves it where it is.
type UpdateWidget struct {
	ID        int64
	Name      *string
	Component *string
	Title     *string
	Props     json.RawMessage
	Source    *Source
	Width     *int
	Height    *int
	After     *int64
}

type CopyWidget struct {
	ID, DashboardID int64
	After           *int64
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
// new component, a size and a place until it has one again (D15). A
// change of only the size or the place (the page's drag) checks the size
// alone and writes only those: the content is unchanged, so its query is
// not run again.
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
	if in.Name != nil {
		name, err := givenName(*in.Name)
		if err != nil {
			return WidgetInfo{}, err
		}
		if name == "" {
			return WidgetInfo{}, refuseBlankName()
		}
		siblings, err := s.st.ListWidgets(ctx, w.DashboardID)
		if err != nil {
			return WidgetInfo{}, err
		}
		if takenNames(siblings, w.ID)[name] {
			return WidgetInfo{}, refuseNameTaken(name)
		}
		w.Name = name
	}
	if in.Component != nil {
		// Only the migrator removes a component (D14); "" here would
		// store NULL and turn a live widget into a removed one.
		if *in.Component == "" {
			return WidgetInfo{}, store.Refuse(store.ErrInvalid,
				"component must not be empty; list_components names the ones there are")
		}
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
	layoutOnly := in.Component == nil && in.Name == nil && in.Title == nil && in.Props == nil && in.Source == nil
	if layoutOnly || (in.Component == nil && w.Component == "") {
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
	a := store.AuditEntry{Actor: actor, Action: "widget.update"}
	// A layout change writes only the layout columns: w was read before
	// the checks above, and writing all of it back would undo an edit
	// that landed in between (the page's drag racing an agent's edit).
	write := func() error {
		if layoutOnly {
			return s.st.SetWidgetLayout(ctx, w.ID, w.SortKey, w.Width, w.Height, a)
		}
		return s.st.UpdateWidget(ctx, w, a)
	}
	if in.After == nil || *in.After == w.ID { // after itself: it stays where it is
		if err := write(); err != nil {
			if errors.Is(err, store.ErrConflict) { // the key is unchanged: a rename lost a race
				return WidgetInfo{}, refuseNameTaken(w.Name)
			}
			return WidgetInfo{}, err
		}
		return s.readWidget(ctx, w.ID)
	}
	err = retryConflict(func() error {
		key, err := s.widgetKeyAfter(ctx, w, in.After)
		if err != nil {
			return err
		}
		w.SortKey = key
		return write()
	}, func() error { return s.lostWidgetRace(ctx, w) })
	if err != nil {
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

// givenName trims a name an agent gave. "" stays "" (derive one from the
// title); a name of only spaces is refused rather than derived, since
// the agent meant to give one.
func givenName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" && name != "" {
		return "", refuseBlankName()
	}
	return trimmed, nil
}

func refuseBlankName() error { return store.Refuse(store.ErrInvalid, "name must not be blank") }

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
	specs = slices.Clone(specs) // names are trimmed in place; leave the caller's alone
	for i, sp := range specs {
		name, err := givenName(sp.Name)
		if err != nil {
			return nil, err
		}
		specs[i].Name = name
		if name == "" {
			continue
		}
		if taken[name] {
			return nil, refuseNameTaken(name)
		}
		taken[name] = true
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
