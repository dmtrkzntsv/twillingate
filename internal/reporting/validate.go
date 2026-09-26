package reporting

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// deriveName turns a widget's title into a stable, URL-safe name: ASCII
// letters and digits are kept (lower-cased), any run of anything else —
// spaces, punctuation, an emoji, a whole non-Latin word — becomes one
// '-', and the result is trimmed of leading/trailing '-' and cut to 60
// characters (also trimmed, so the cut never leaves a trailing '-'). A
// title that yields nothing usable (empty, or entirely outside a-z0-9)
// falls back to "widget". taken is every name already used on the
// dashboard; a collision gets "-2", then "-3", and so on.
func deriveName(title string, taken map[string]bool) string {
	var b strings.Builder
	sep := true // true at the start suppresses a leading '-'
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			sep = false
		case !sep:
			b.WriteByte('-')
			sep = true
		}
	}
	name := strings.TrimRight(b.String(), "-")
	if len(name) > 60 {
		name = strings.TrimRight(name[:60], "-")
	}
	if name == "" {
		name = "widget"
	}
	if !taken[name] {
		return name
	}
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s-%d", name, n)
		if !taken[candidate] {
			return candidate
		}
	}
}

// checkSize refuses a width or height outside the 12x12 grid a dashboard
// lays widgets on (each row 40px tall, so height 12 is 480px).
func checkSize(width, height int) error {
	if width < 1 || width > 12 {
		return store.Refuse(store.ErrInvalid, "width is columns out of 12, from 1 to 12")
	}
	if height < 1 || height > 12 {
		return store.Refuse(store.ErrInvalid, "height is rows of 40px, from 1 to 12")
	}
	return nil
}

// joinAnd renders items as a person reads a list: one item alone, two
// joined by "and", three or more comma-separated with "and" before the
// last.
func joinAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	default:
		return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
	}
}

// validateWidget refuses a widget that names a component that does not
// exist, a source type that does not exist, a component/source-type pair
// the component does not accept, a size outside the grid, props that do
// not satisfy the component's schema, or — the most expensive check,
// last — content its source type rejects.
func (s *Service) validateWidget(ctx context.Context, comps map[string]Component, w store.Widget) error {
	comp, ok := comps[w.Component]
	if !ok {
		return store.Refuse(store.ErrInvalid,
			"component %s does not exist; list_components names the ones there are", w.Component)
	}
	st, ok := s.sources[w.SourceType]
	if !ok {
		names := make([]string, 0, len(s.sources))
		for n := range s.sources {
			names = append(names, n)
		}
		sort.Strings(names)
		return store.Refuse(store.ErrInvalid,
			"source type %s does not exist; there are %s", w.SourceType, joinAnd(names))
	}
	accepted := false
	for _, a := range comp.Accepts {
		if a == w.SourceType {
			accepted = true
			break
		}
	}
	if !accepted {
		return store.Refuse(store.ErrInvalid,
			"%s does not accept %s; it accepts %s", comp.Name, w.SourceType, joinAnd(comp.Accepts))
	}
	if err := checkSize(w.Width, w.Height); err != nil {
		return err
	}
	if err := comp.checkProps(json.RawMessage(w.Props)); err != nil {
		return err
	}
	return st.Validate(ctx, w.Source, comp)
}
