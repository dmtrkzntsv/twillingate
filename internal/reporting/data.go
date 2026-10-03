package reporting

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
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
// Options.CacheAge — a manual refresh. Filters, Sort, Distinct, Offset
// and Limit are a remote table's paging arguments (view.go), refused on
// any other widget; zero means absent, and Limit 0 the
// CONSOLE_QUERY_MAX_ROWS cap.
type DataRequest struct {
	WidgetID  int64
	ProjectID int64
	From, To  string
	Fresh     bool

	Filters  string // JSON: [{"column": "...", "op": "...", "value": "..." | ["..."]}]
	Sort     string // "<column>:asc" | "<column>:desc", split on the last ':'
	Distinct string // a column name
	Offset   int
	Limit    int
}

// WidgetData is get_widget_data's answer: the fixed envelope every
// widget's value rides in, whatever its component or source type.
// ProjectID/From/To are set only when the widget follows that switcher,
// and echo the applied (possibly clamped) values. CachedAt and
// RefreshAfter are set only for a cacheable source (sql; not md). Data is
// nil when Removed. Page is set only for a remote table, whose Data is
// then one page of its result.
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
	Page         *PageInfo  `json:"page,omitempty"`
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
	v, load, err := s.loader(w, src, params, in)
	if err != nil {
		return WidgetData{}, err
	}

	// context.WithoutCancel: this call may run on behalf of every request
	// currently sharing it through the cache's singleflight (below), not
	// only the one goroutine that happens to run it, so it must not carry
	// any one caller's cancellation — a client disconnecting partway
	// through must not turn into a false CONSOLE_QUERY_TIMEOUT for every
	// other request sharing the same load. readsql applies its own
	// deadline (CONSOLE_QUERY_TIMEOUT) regardless.
	loadCtx := context.WithoutCancel(ctx)

	var value any
	if src.Cacheable() {
		var suffix string
		if remoteTable(w) {
			suffix = v.cacheSuffix()
		}
		key := cacheKey(w.ID, w.SourceType, w.Source, followsProject, followsRange, projectID, from, to, suffix)
		var cachedAt time.Time
		if value, cachedAt, err = s.cache.get(key, in.Fresh, func() (any, error) { return load(loadCtx) }); err != nil {
			return WidgetData{}, wrapLoadErr(err)
		}
		ca, ra := cachedAt, cachedAt.Add(s.cache.refreshAge)
		out.CachedAt, out.RefreshAfter = &ca, &ra
	} else {
		if value, err = load(loadCtx); err != nil {
			return WidgetData{}, wrapLoadErr(err)
		}
	}

	// Checked here, after the cache (hit or miss), against the requesting
	// widget's own current component — not baked into the cached load —
	// so a value shared from the cache is still checked against whichever
	// widget is asking for it now, and a component swapped on the widget
	// since the value was cached is still caught.
	if err := setData(&out, comp, v, value); err != nil {
		return WidgetData{}, err
	}
	return out, nil
}

// loader parses in's paging arguments for w and returns them with the
// load that answers them: one page of the result for a remote table, the
// whole result for any other widget, which is refused any paging
// argument. Shared by WidgetData and reporting dev's data handler, so
// the two cannot disagree on which widgets page or how.
func (s *Service) loader(w store.Widget, src SourceType, p Params, in DataRequest) (view, func(context.Context) (any, error), error) {
	v, err := parseView(in, s.db.MaxRows())
	if err != nil {
		return view{}, nil, err
	}
	if !remoteTable(w) {
		if v.present {
			return view{}, nil, store.Refuse(store.ErrInvalid,
				"widget %d is not a remote table; filters, sort, distinct, offset and limit need a table with props.mode \"remote\"", w.ID)
		}
		return v, func(ctx context.Context) (any, error) { return src.Load(ctx, w.Source, p) }, nil
	}
	pl, ok := src.(pageLoader)
	if !ok { // a table accepts only sql, which pages
		return view{}, nil, fmt.Errorf("reporting: widget %d: source type %s cannot page", w.ID, w.SourceType)
	}
	return v, func(ctx context.Context) (any, error) { return pl.LoadPage(ctx, w.Source, p, v.page) }, nil
}

// setData checks a loaded value's rows against comp and puts it in out:
// a remote table's page as Data, with its page block. A distinct answer
// is not checked, its columns being value and rows rather than the
// widget's own.
func setData(out *WidgetData, comp Component, v view, value any) error {
	switch res := value.(type) {
	case readsql.PageResult:
		if v.page.Distinct == "" {
			if err := comp.checkRows(res.Result); err != nil {
				return wrapLoadErr(err)
			}
		}
		page := v.echo
		page.Matched, page.Total = res.Matched, res.Total
		out.Data, out.Page = res.Result, &page
	case readsql.Result:
		if err := comp.checkRows(res); err != nil {
			return wrapLoadErr(err)
		}
		out.Data = res
	default:
		out.Data = value
	}
	return nil
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
// on update. suffix is a remote table's view (view.cacheSuffix), "" for
// every other widget, so a page is cached per filters, sort and range of
// rows.
func cacheKey(widgetID int64, sourceType, content string, followsProject, followsRange bool, projectID int64, from, to, suffix string) string {
	sum := sha256.Sum256([]byte(content))
	key := fmt.Sprintf("%d:%s:%x", widgetID, sourceType, sum)
	if followsProject {
		key += fmt.Sprintf(":p=%d", projectID)
	}
	if followsRange {
		key += fmt.Sprintf(":r=%s,%s", from, to)
	}
	return key + suffix
}
