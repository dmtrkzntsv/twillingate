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
	var projectID int64
	if followsProject {
		if in.ProjectID == 0 {
			return WidgetData{}, store.Refuse(store.ErrInvalid,
				"widget %d follows the project switcher; pass project_id", w.ID)
		}
		projectID = in.ProjectID
		out.ProjectID = &projectID
	}
	var from, to string
	if followsRange {
		if in.From == "" && in.To == "" {
			return WidgetData{}, store.Refuse(store.ErrInvalid,
				"widget %d follows the date range; pass from and to", w.ID)
		}
		if from, to, err = checkRange(in.From, in.To, civil.Today(s.now())); err != nil {
			return WidgetData{}, err
		}
		out.From, out.To = from, to
	}

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

	load := func() (any, error) {
		v, err := src.Load(ctx, w.Source, Params{ProjectID: projectID, From: from, To: to})
		if err != nil {
			return nil, err
		}
		if res, ok := v.(readsql.Result); ok {
			if err := comp.checkRows(res); err != nil {
				return nil, err
			}
		}
		return v, nil
	}

	if !src.Cacheable() {
		v, err := load()
		if err != nil {
			return WidgetData{}, wrapLoadErr(err)
		}
		out.Data = v
		return out, nil
	}

	key := cacheKey(w.Source, followsProject, followsRange, projectID, from, to)
	v, cachedAt, err := s.cache.get(key, in.Fresh, load)
	if err != nil {
		return WidgetData{}, wrapLoadErr(err)
	}
	out.Data = v
	ca, ra := cachedAt, cachedAt.Add(s.cache.refreshAge)
	out.CachedAt, out.RefreshAfter = &ca, &ra
	return out, nil
}

// checkRange validates a widget's requested date range and clamps a
// future "to" back to today: from and to must parse as YYYY-MM-DD, from
// must not be after to, and the span must not exceed 365 days (checked
// with the plain, unclamped values, same as SetView's). It returns the
// applied values, which the caller echoes in the envelope.
func checkRange(from, to string, today civil.Date) (string, string, error) {
	if err := checkDates(from, to); err != nil {
		return "", "", err
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

// cacheKey identifies a widget's cached value by its source content and
// only the parts of Params it actually follows (Deviation 2): two
// widgets whose source is byte-identical and read the same project/range
// share a cache entry, and a widget whose source changes gets a fresh key
// rather than needing an explicit invalidation on update.
func cacheKey(content string, followsProject, followsRange bool, projectID int64, from, to string) string {
	sum := sha256.Sum256([]byte(content))
	key := fmt.Sprintf("%x", sum)
	if followsProject {
		key += fmt.Sprintf(":p=%d", projectID)
	}
	if followsRange {
		key += fmt.Sprintf(":r=%s,%s", from, to)
	}
	return key
}
