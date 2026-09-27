package reporting

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/civil"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// ReleasesURL is linked from a widget data load failure, since a query
// that no longer runs or rows that no longer fit their component most
// often mean a release changed the view or column shape the widget
// depended on.
const ReleasesURL = "https://github.com/dmtrkzntsv/twillingate/releases"

// DataRequest is get_widget_data's input: the widget and the viewer's
// current selection. ProjectID and From/To matter only when the widget
// follows that switcher (WidgetInfo.FollowsProject/FollowsRange); Fresh
// asks for a value no older than Options.RefreshAge rather than
// Options.CacheAge — a manual refresh.
type DataRequest struct {
	WidgetID  int64
	ProjectID int64
	From, To  string
	Fresh     bool
}

// WidgetData is get_widget_data's answer: the fixed envelope every
// widget's value rides in, whatever its component or source type.
// ProjectID/From/To are set only when the widget follows that switcher,
// and echo the applied (possibly clamped) values. CachedAt and
// RefreshAfter are set only for a cacheable source (sql; not md). Data is
// nil when Removed.
type WidgetData struct {
	WidgetID     int64      `json:"widget_id"`
	SourceType   string     `json:"source_type"`
	ProjectID    *int64     `json:"project_id,omitempty"`
	From         string     `json:"from,omitempty"`
	To           string     `json:"to,omitempty"`
	CachedAt     *time.Time `json:"cached_at,omitempty"`
	RefreshAfter *time.Time `json:"refresh_after,omitempty"`
	Removed      bool       `json:"removed"`
	Data         any        `json:"data"` // readsql.Result (sql) | Markdown (md) | nil (removed)
}

// WidgetData loads one widget's value: checked against what it follows,
// clamped where a date range runs into the future, and cached per
// Options.CacheAge/RefreshAge when its source type is cacheable (sql).
// Rows are type-checked against the widget's component on every load —
// not only when the widget was saved — since a release may have changed
// the view or query underneath it.
func (s *Service) WidgetData(ctx context.Context, in DataRequest) (WidgetData, error) {
	w, err := s.st.GetWidget(ctx, in.WidgetID)
	if err != nil {
		return WidgetData{}, err
	}
	if w.ArchivedAt != "" {
		return WidgetData{}, store.Refuse(store.ErrNotFound, "widget %d is archived", w.ID)
	}
	out := WidgetData{WidgetID: w.ID, SourceType: w.SourceType}
	if w.Component == "" { // removed (D14): nothing left to check rows against
		out.Removed = true
		return out, nil
	}

	followsProject, followsRange := s.follows(w)
	params, echo, err := s.widgetParams(followsProject, followsRange, w.ID, in)
	if err != nil {
		return WidgetData{}, err
	}
	projectID, from, to := params.ProjectID, params.From, params.To
	out.ProjectID, out.From, out.To = echo.ProjectID, echo.From, echo.To

	comps, err := s.components(ctx)
	if err != nil {
		return WidgetData{}, err
	}
	comp, ok := comps[w.Component]
	if !ok {
		return WidgetData{}, fmt.Errorf("reporting: widget %d: component %s not found", w.ID, w.Component)
	}
	src, ok := s.sources[w.SourceType]
	if !ok {
		return WidgetData{}, fmt.Errorf("reporting: widget %d: source type %s not registered", w.ID, w.SourceType)
	}

	// context.WithoutCancel: this call may run on behalf of every request
	// currently sharing it through the cache's singleflight (below), not
	// only the one goroutine that happens to run it, so it must not carry
	// any one caller's cancellation — a client disconnecting partway
	// through must not turn into a false API_QUERY_TIMEOUT for every
	// other request sharing the same load. readsql applies its own
	// deadline (API_QUERY_TIMEOUT) regardless.
	loadCtx := context.WithoutCancel(ctx)
	load := func() (any, error) {
		return src.Load(loadCtx, w.Source, Params{ProjectID: projectID, From: from, To: to})
	}

	var v any
	if src.Cacheable() {
		key := cacheKey(w.ID, w.SourceType, w.Source, followsProject, followsRange, projectID, from, to)
		var cachedAt time.Time
		if v, cachedAt, err = s.cache.get(key, in.Fresh, load); err != nil {
			return WidgetData{}, wrapLoadErr(err)
		}
		ca, ra := cachedAt, cachedAt.Add(s.cache.refreshAge)
		out.CachedAt, out.RefreshAfter = &ca, &ra
	} else {
		if v, err = load(); err != nil {
			return WidgetData{}, wrapLoadErr(err)
		}
	}

	// Checked here, after the cache (hit or miss), against the requesting
	// widget's own current component — not baked into the cached load —
	// so a value shared from the cache is still checked against whichever
	// widget is asking for it now, and a component swapped on the widget
	// since the value was cached is still caught.
	if res, ok := v.(readsql.Result); ok {
		if err := comp.checkRows(res); err != nil {
			return WidgetData{}, wrapLoadErr(err)
		}
	}
	out.Data = v
	return out, nil
}

// widgetParams resolves a load's bound Params and the fields WidgetData
// echoes, from what a widget's content follows and what the caller
// asked for: refused when a followed switcher's input is missing,
// clamped when a custom "to" runs into the future. Shared by WidgetData
// (against a widget already read from the store) and reporting dev's
// data handler (against one read fresh from a file, never saved) — the
// project/range handling is otherwise identical either way.
func (s *Service) widgetParams(followsProject, followsRange bool, widgetID int64, in DataRequest) (Params, WidgetData, error) {
	var p Params
	var echo WidgetData
	if followsProject {
		if in.ProjectID == 0 {
			return Params{}, WidgetData{}, store.Refuse(store.ErrInvalid,
				"widget %d follows the project switcher; pass project_id", widgetID)
		}
		projectID := in.ProjectID
		p.ProjectID = projectID
		echo.ProjectID = &projectID
	}
	if followsRange {
		if in.From == "" && in.To == "" {
			return Params{}, WidgetData{}, store.Refuse(store.ErrInvalid,
				"widget %d follows the date range; pass from and to", widgetID)
		}
		from, to, err := checkRange(in.From, in.To, civil.Today(s.now()))
		if err != nil {
			return Params{}, WidgetData{}, err
		}
		p.From, p.To = from, to
		echo.From, echo.To = from, to
	}
	return p, echo, nil
}

// checkRange validates a widget's requested date range and clamps a
// future "to" back to today: from and to must parse as YYYY-MM-DD, from
// must not be after to, and the span must not exceed 365 days (checked
// with the plain, unclamped values, same as SetView's). from itself must
// not be after today either — checked before the "to" clamp below, since
// a wholly future range (both ends past today) would otherwise clamp to
// below the still-future from, coming back from≤to but from>to. It
// returns the applied values, which the caller echoes in the envelope.
func checkRange(from, to string, today civil.Date) (string, string, error) {
	if err := checkDates(from, to); err != nil {
		return "", "", err
	}
	f, err := civil.Parse(from) // checkDates already refused an unparseable from
	if err != nil {
		return "", "", err
	}
	if today.Before(f) {
		return "", "", store.Refuse(store.ErrInvalid, "from %s is after today", from)
	}
	t, err := civil.Parse(to) // checkDates already refused an unparseable to
	if err != nil {
		return "", "", err
	}
	if today.Before(t) {
		to = today.String()
	}
	return from, to, nil
}

// wrapLoadErr appends a pointer to the release notes to a load failure —
// a query that no longer runs, or rows that no longer fit the component —
// while keeping it an ErrInvalid a caller can still match with errors.Is.
func wrapLoadErr(err error) error {
	if err == nil {
		return nil
	}
	return store.Refuse(store.ErrInvalid,
		"%s; if this started after an update, see the release notes at %s", err, ReleasesURL)
}

// cacheKey identifies one widget's cached value: its id, its source
// type, a SHA-256 of its source content (Deviation 2), and only the
// parts of Params it actually follows. The id keeps two widgets from
// ever sharing an entry even when their content is byte-identical — a
// value cached for one component (say table, which reads any shape)
// must never be handed to a different widget whose component (say stat)
// has its own column requirements WidgetData checks separately on every
// read; sharing by content alone let a hit skip that check entirely,
// since only a genuine load ever ran it. The id also gives a copied
// widget (same content, new id) an empty cache to start from, per D33.
// The content hash still matters on top of the id: a widget whose source
// changes gets a fresh key rather than needing an explicit invalidation
// on update.
func cacheKey(widgetID int64, sourceType, content string, followsProject, followsRange bool, projectID int64, from, to string) string {
	sum := sha256.Sum256([]byte(content))
	key := fmt.Sprintf("%d:%s:%x", widgetID, sourceType, sum)
	if followsProject {
		key += fmt.Sprintf(":p=%d", projectID)
	}
	if followsRange {
		key += fmt.Sprintf(":r=%s,%s", from, to)
	}
	return key
}
