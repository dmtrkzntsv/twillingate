package alerts

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// The pages of an alert's history, and how many of its newest
// evaluations a view holds.
const (
	defaultHistoryLimit = 50
	maxHistoryLimit     = 400
	recentEvaluations   = 14
)

// Ops are the audited operations on alerts, which the MCP tools and the
// REST routes call. Every write is audited by the store, in its
// transaction, from the entry built here.
type Ops struct {
	// Timeout bounds a preview's queries (CONSOLE_QUERY_TIMEOUT).
	Timeout time.Duration

	st       Store
	projects Projects
	eng      Engine   // nil: this process cannot query the event files
	presence Presence // nil: never-sent events are not warned about
	now      func() time.Time
}

// NewOps returns the operations over st. eng and presence may be nil.
func NewOps(st Store, projects Projects, eng Engine, presence Presence) *Ops {
	return &Ops{st: st, projects: projects, eng: eng, presence: presence, now: time.Now}
}

// View is an alert as the operations answer it.
type View struct {
	store.Alert
	// Parsed is the definition read back; zero when it no longer parses
	// (the next evaluation reports the alert broken).
	Parsed Parsed
	// State is the state shown: paused, snoozed (until a time still
	// ahead), else the stored state.
	State string
	// ValueText is the last value as the read words it.
	ValueText string
	// Recent are the newest evaluations, newest first, at most 14.
	Recent []store.AlertEvaluation
	// Warnings, on a create or an edit of the definition only, name the
	// events and attributes the project has never sent.
	Warnings []string
}

// Patch is an edit of an alert: a nil field is left alone. A new source
// replaces the old one whole: a query (with its read, or none), a where,
// or a template (with its fields, or none). A read alone keeps the
// query, and fields alone keep the template.
type Patch struct {
	Name         *string
	Condition    *store.AlertCondition
	WindowDays   *int
	MinVolume    *float64
	Paused       *bool
	SnoozedUntil **time.Time // a non-nil pointer to nil clears the snooze
	Query        *native.Query
	Read         *store.AlertRead
	Where        *[]native.Filter
	Template     *string
	Fields       map[string]string
}

// definition reports whether p edits the definition.
func (p Patch) definition() bool {
	return p.Condition != nil || p.WindowDays != nil || p.MinVolume != nil ||
		p.Query != nil || p.Read != nil || p.Where != nil || p.Template != nil || p.Fields != nil
}

func auditOf(actor, op string, id int64) store.AuditEntry {
	e := store.AuditEntry{Actor: actor, Action: "alert." + op}
	if id > 0 {
		e.Subject = fmt.Sprintf("alert/%d", id)
	}
	return e
}

// project refuses an unknown project (store.ErrNotFound) and reports
// whether it is archived.
func (o *Ops) project(ctx context.Context, id int64) (archived bool, err error) {
	_, archived, ok := o.projects.Project(ctx, id)
	if !ok {
		return false, store.Refuse(store.ErrNotFound, "no project %d", id)
	}
	return archived, nil
}

// List lists the project's live alerts, or its archived ones, by name.
func (o *Ops) List(ctx context.Context, projectID int64, archived bool) ([]View, error) {
	if _, err := o.project(ctx, projectID); err != nil {
		return nil, err
	}
	as, err := o.st.ListAlerts(ctx, projectID, archived)
	if err != nil {
		return nil, err
	}
	return o.views(ctx, as)
}

// Create validates in and saves it as a new alert of a live project,
// which asks for a check at once. An event an event rule renames or
// blocks is refused; one never sent is a warning.
func (o *Ops) Create(ctx context.Context, actor string, projectID int64, in Input) (View, error) {
	archived, err := o.project(ctx, projectID)
	if err != nil {
		return View{}, err
	}
	if archived {
		return View{}, invalid("project %d is archived; restore the project first", projectID)
	}
	name, err := checkedName(in.Name)
	if err != nil {
		return View{}, err
	}
	def, err := Build(in)
	if err != nil {
		return View{}, err
	}
	warnings, err := o.checkEvents(ctx, projectID, def)
	if err != nil {
		return View{}, err
	}
	a, err := o.st.CreateAlert(ctx, store.Alert{ProjectID: projectID, Name: name, Def: def}, MaxLive, o.now(), auditOf(actor, "create", 0))
	if err != nil {
		return View{}, err
	}
	return o.view(ctx, a, warnings)
}

// Update merges p over the alert. Its events are checked again, as at
// create, only when the definition changes; a pause, a snooze or a
// rename alone is saved whatever its events have become.
func (o *Ops) Update(ctx context.Context, actor string, projectID, id int64, p Patch) (View, error) {
	if _, err := o.project(ctx, projectID); err != nil {
		return View{}, err
	}
	cur, err := o.st.GetAlert(ctx, projectID, id)
	if err != nil {
		return View{}, err
	}
	if cur.ArchivedAt != "" {
		return View{}, invalid("alert %d is archived; restore it to edit it", id)
	}
	e := store.AlertEdit{Paused: p.Paused}
	if p.Name != nil {
		name, err := checkedName(*p.Name)
		if err != nil {
			return View{}, err
		}
		e.Name = &name
	}
	if p.SnoozedUntil != nil {
		until := ""
		if t := *p.SnoozedUntil; t != nil {
			u := t.UTC().Truncate(time.Second)
			if !u.After(o.now()) {
				return View{}, invalid("snoozed_until: %s is not in the future", u.Format(time.RFC3339))
			}
			until = u.Format(time.RFC3339) // the store compares it as a string
		}
		e.SnoozedUntil = &until
	}
	var warnings []string
	if p.definition() {
		def, err := merged(cur.Def, p)
		if err != nil {
			return View{}, err
		}
		if !sameJSON(def, cur.Def) {
			if warnings, err = o.checkEvents(ctx, projectID, def); err != nil {
				return View{}, err
			}
			e.Def = &def
		}
	}
	a, err := o.st.UpdateAlert(ctx, projectID, id, e, o.now(), auditOf(actor, "update", id))
	if err != nil {
		return View{}, err
	}
	return o.view(ctx, a, warnings)
}

// merged is the definition cur becomes with p's definition fields over
// it, built and validated.
func merged(cur store.AlertDef, p Patch) (store.AlertDef, error) {
	in, err := InputOf(cur)
	if err != nil {
		return store.AlertDef{}, err
	}
	if p.Condition != nil {
		in.Condition = *p.Condition
	}
	if p.WindowDays != nil {
		in.WindowDays = *p.WindowDays
	}
	if p.MinVolume != nil {
		in.MinVolume = *p.MinVolume
	}
	switch {
	case p.Query != nil || p.Where != nil || p.Template != nil:
		in.Query, in.Read, in.Where, in.WhereSet, in.Template, in.Fields = p.Query, p.Read, nil, false, "", p.Fields
		if p.Where != nil {
			in.Where, in.WhereSet = *p.Where, true
		}
		if p.Template != nil {
			in.Template = *p.Template
		}
	default:
		if p.Read != nil {
			in.Read = p.Read // Build refuses it beside a where or a template
		}
		if p.Fields != nil {
			in.Fields = p.Fields // Build refuses them beside a query or a where
		}
	}
	return Build(in)
}

// Archive archives the alert: it stops, and its name is free again.
func (o *Ops) Archive(ctx context.Context, actor string, projectID, id int64) (View, error) {
	return o.setArchived(ctx, actor, projectID, id, true)
}

// Restore brings an archived alert back, pending, unless a live alert
// took its name or the project holds MaxLive (store.ErrConflict).
func (o *Ops) Restore(ctx context.Context, actor string, projectID, id int64) (View, error) {
	return o.setArchived(ctx, actor, projectID, id, false)
}

func (o *Ops) setArchived(ctx context.Context, actor string, projectID, id int64, archived bool) (View, error) {
	if _, err := o.project(ctx, projectID); err != nil {
		return View{}, err
	}
	op := "restore"
	if archived {
		op = "archive"
	}
	a, err := o.st.SetAlertArchived(ctx, projectID, id, archived, MaxLive, o.now(), auditOf(actor, op, id))
	if err != nil {
		return View{}, err
	}
	return o.view(ctx, a, nil)
}

// Check asks the runner to evaluate the alert within a minute. An alert
// the runner would not evaluate (archived, paused, snoozed, of an
// archived project) is store.ErrInvalid. While a check is pending it
// answers the alert unchanged.
func (o *Ops) Check(ctx context.Context, actor string, projectID, id int64) (View, error) {
	if _, err := o.project(ctx, projectID); err != nil {
		return View{}, err
	}
	a, _, err := o.st.RequestAlertCheck(ctx, projectID, id, o.now(), auditOf(actor, "check", id))
	if err != nil {
		return View{}, err
	}
	return o.view(ctx, a, nil)
}

// History pages the alert's evaluations, newest first: limit of them
// (50 when unset, at most 400) older than cursor, an evaluation id ("" for
// the newest). The cursor it returns reads the next page; "" on the
// last.
func (o *Ops) History(ctx context.Context, projectID, id int64, limit int, cursor string) ([]store.AlertEvaluation, string, error) {
	if _, err := o.project(ctx, projectID); err != nil {
		return nil, "", err
	}
	if _, err := o.st.GetAlert(ctx, projectID, id); err != nil {
		return nil, "", err
	}
	var before int64
	if cursor != "" {
		n, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil || n <= 0 {
			return nil, "", invalid("cursor: %q is not a next_cursor alert_history gave", cursor)
		}
		before = n
	}
	switch {
	case limit <= 0:
		limit = defaultHistoryLimit
	case limit > maxHistoryLimit:
		limit = maxHistoryLimit
	}
	es, err := o.st.ListAlertEvaluations(ctx, id, limit+1, before) // one more tells a last page from a full one
	if err != nil || len(es) <= limit {
		return es, "", err
	}
	es = es[:limit]
	return es, strconv.FormatInt(es[limit-1].ID, 10), nil
}

// checkEvents refuses def when an event rule renames or blocks an event
// it names, and, with presence, warns of each event or attribute the
// project has never sent. A presence failure is a warning too.
func (o *Ops) checkEvents(ctx context.Context, projectID int64, def store.AlertDef) ([]string, error) {
	p, err := Parse(def)
	if err != nil {
		return nil, err
	}
	refs := p.References()
	for _, r := range refs {
		if r.Event == "" || strings.HasPrefix(r.Event, "$") {
			continue
		}
		for _, f := range ruleFamilies(r.Family) {
			to, blocked := o.projects.ResolveEvent(ctx, projectID, f, r.Event)
			switch {
			case to != r.Event:
				return nil, invalid("`%s` is now `%s` (an event rule); alert on `%s`", r.Event, to, to)
			case blocked:
				return nil, invalid("`%s` is blocked by an event rule, so none is stored; remove the rule or alert on another event", r.Event)
			}
		}
	}
	if o.presence == nil {
		return nil, nil
	}
	var warnings []string
	for _, r := range refs {
		name := r.Event
		var sent bool
		if r.Event != "" {
			sent, err = o.presence.HasEvents(ctx, projectID, r.Family, r.Event)
		} else {
			name = "attr_" + r.Attr
			sent, err = o.presence.HasAttribute(ctx, projectID, r.Attr)
		}
		switch {
		case err != nil:
			warnings = append(warnings, fmt.Sprintf("could not check whether `%s` was sent: %v", name, err))
		case !sent:
			warnings = append(warnings, fmt.Sprintf("`%s` has never been sent by this project; the alert reads no_data until it is", name))
		}
	}
	return warnings, nil
}

// ruleFamilies are the families whose event rules apply to an event a
// query reads from family f: its own for product and measures, both for
// the events table (""), none for views and checks, which have no rules.
func ruleFamilies(f store.Family) []store.Family {
	switch f {
	case store.FamilyProduct, store.FamilyMeasures:
		return []store.Family{f}
	case "":
		return []store.Family{store.FamilyProduct, store.FamilyMeasures}
	}
	return nil
}

// checkedName is name trimmed, refused by CheckName.
func checkedName(name string) (string, error) {
	name = strings.TrimSpace(name)
	return name, CheckName(name)
}

// view is a's View, with warnings.
func (o *Ops) view(ctx context.Context, a store.Alert, warnings []string) (View, error) {
	vs, err := o.views(ctx, []store.Alert{a})
	if err != nil {
		return View{}, err
	}
	vs[0].Warnings = warnings
	return vs[0], nil
}

// views are the alerts' Views, their recent evaluations read at once.
func (o *Ops) views(ctx context.Context, as []store.Alert) ([]View, error) {
	ids := make([]int64, len(as))
	for i, a := range as {
		ids[i] = a.ID
	}
	recent, err := o.st.RecentAlertEvaluations(ctx, ids, recentEvaluations)
	if err != nil {
		return nil, err
	}
	now := o.now().UTC().Format(time.RFC3339)
	out := make([]View, len(as))
	for i, a := range as {
		p, _ := Parse(a.Def) // zero when it no longer parses
		state := a.State
		switch {
		case a.Paused:
			state = store.AlertPaused
		case a.SnoozedUntil > now:
			state = store.AlertSnoozed
		}
		out[i] = View{Alert: a, Parsed: p, State: state, ValueText: formatValue(p, a.LastValue), Recent: recent[a.ID]}
	}
	return out, nil
}
