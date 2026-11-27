package alerts

import (
	"encoding/json"
	"errors"
	"maps"
	"math"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Input is a definition as create_alert and preview_alert take it:
// exactly one source, Query with Read, Where (silence; WhereSet tells an
// empty where from none) or Template with Fields. Build does not read
// Name; CheckName checks it.
type Input struct {
	Name       string
	Query      *native.Query
	Read       *store.AlertRead
	Where      []native.Filter
	WhereSet   bool
	Template   string
	Fields     map[string]string
	Condition  store.AlertCondition
	WindowDays int
	MinVolume  float64
}

// The condition types.
const (
	condThreshold = "threshold"
	condChange    = "change"
	condSilence   = "silence"
)

// readKind is what kind of number an alert reads, which its value's
// wording follows.
type readKind int

const (
	readCount    readKind = iota + 1 // a count of rows or ids
	readFraction                     // 0–1: a funnel's conversion, a bounce rate
	readNumber                       // any other number: a sum, an average, a ratio
	readSilence                      // the newest matching event's time
)

// Parsed is a stored definition read back for evaluation.
type Parsed struct {
	Def    store.AlertDef
	Query  native.Query
	Kind   string // stat | funnel | silence
	Read   readKind
	Metric int // stat and silence: the read metric's index in Query.Metrics
	Volume int // stat: the volume metric's index, -1 when none (and for a funnel or silence)
	Step   int // funnel: the read step's index in Query.Steps
	Base   int // funnel: the denominator step's index
}

// The stat ops whose value is a rate or an average, which need a volume
// metric; the ops a volume metric takes; the ops that count.
var (
	rateOps   = map[string]bool{"ratio": true, "bounce_rate": true, "avg": true, "mean": true, "percentile": true}
	volumeOps = []string{"count", "actors", "users", "groups", "weight"}
	countOps  = map[string]bool{"count": true, "actors": true, "users": true, "groups": true, "weight": true, "bounces": true, "days": true, "daily_sum": true, "daily_max": true}
)

// The bounds of a definition.
const (
	maxWindowDays        = 30
	conversionWindowDays = 7
	maxPercent           = 1000
	maxSilenceHours      = 720
	maxNameRunes         = 100
)

func invalid(format string, args ...any) error {
	return store.Refuse(store.ErrInvalid, format, args...)
}

// Build validates in and returns the definition to store: its source
// resolved to a query and read, the defaults written out (the read
// metric of a one-metric stat, a funnel's from, the window), and a stat's
// compare and shape cleared. Every refusal wraps store.ErrInvalid.
func Build(in Input) (store.AlertDef, error) {
	in.Fields = cleanFields(in.Fields) // an empty field is an absent one
	if err := checkCondition(in.Condition); err != nil {
		return store.AlertDef{}, err
	}
	q, read, err := resolveSource(in)
	if err != nil {
		return store.AlertDef{}, err
	}
	q.Compare, q.Shape = "", "" // the alert decides the days it compares
	raw, err := json.Marshal(q)
	if err != nil {
		return store.AlertDef{}, invalid("query: %v", err)
	}
	def := store.AlertDef{
		Query:      raw,
		Read:       read,
		Condition:  in.Condition,
		WindowDays: in.WindowDays,
		MinVolume:  in.MinVolume,
	}
	if in.Template != "" {
		def.Template, def.Fields = in.Template, in.Fields
	}
	if def.WindowDays == 0 {
		def.WindowDays = 1
		if in.Template == "conversion" {
			def.WindowDays = conversionWindowDays
		}
	}
	p, err := parse(def, q)
	if err != nil {
		return store.AlertDef{}, err
	}
	return p.Def, nil
}

// resolveSource turns in's one source into a query and a read.
func resolveSource(in Input) (native.Query, store.AlertRead, error) {
	hasWhere := in.WhereSet || len(in.Where) > 0
	n := 0
	for _, has := range []bool{in.Query != nil, hasWhere, in.Template != ""} {
		if has {
			n++
		}
	}
	silence := in.Condition.Type == condSilence
	switch {
	case n != 1:
		return native.Query{}, store.AlertRead{}, invalid("an alert takes exactly one of query (with read), where (a silence alert's events) or template (with fields)")
	case in.Read != nil && in.Query == nil:
		return native.Query{}, store.AlertRead{}, invalid("read: read names a number of query; give it with query")
	case len(in.Fields) > 0 && in.Template == "":
		return native.Query{}, store.AlertRead{}, invalid("fields: fields fill a template; give them with template")
	}
	switch {
	case in.Query != nil:
		if silence {
			return native.Query{}, store.AlertRead{}, invalid("query: a silence alert takes where (the events it watches) or the no_events template, not a query; the alert builds its stat")
		}
		var read store.AlertRead
		if in.Read != nil {
			read = *in.Read
		}
		return *in.Query, read, nil
	case hasWhere:
		if !silence {
			return native.Query{}, store.AlertRead{}, invalid("where: where is a silence alert's; a threshold or change alert takes query and read, or a template")
		}
		return silenceQuery(in.Where), store.AlertRead{}, nil
	}
	q, read, _, _, offered, err := fromTemplate(in.Template, in.Fields)
	if err != nil {
		return native.Query{}, store.AlertRead{}, err
	}
	if !slices.Contains(offered, in.Condition.Type) {
		return native.Query{}, store.AlertRead{}, invalid("condition.type: template %s takes a %s condition, not %s", in.Template, strings.Join(offered, " or "), in.Condition.Type)
	}
	return q, read, nil
}

// silenceQuery is a silence alert's stat: the newest received_at of the
// events passing where.
func silenceQuery(where []native.Filter) native.Query {
	if len(where) == 0 {
		where = nil
	}
	return native.Query{Kind: "stat", Table: "events", Metrics: []native.Metric{{Op: "max", Column: "received_at"}}, Where: where}
}

// cleanFields is f without its empty values, nil when none is left.
func cleanFields(f map[string]string) map[string]string {
	out := maps.Clone(f)
	maps.DeleteFunc(out, func(_, v string) bool { return v == "" })
	if len(out) == 0 {
		return nil
	}
	return out
}

// Parse reads a stored definition back for evaluation, refusing one that
// no longer holds (store.ErrInvalid).
func Parse(def store.AlertDef) (Parsed, error) {
	var q native.Query
	if err := json.Unmarshal(def.Query, &q); err != nil {
		return Parsed{}, invalid("query: the stored definition is not a query: %v", err)
	}
	return parse(def, q)
}

// InputOf is the Input Build was given for def, as far as def keeps it:
// its template and fields, its where (silence), or its query and read.
// An edit merges over it.
func InputOf(def store.AlertDef) (Input, error) {
	var q native.Query
	if err := json.Unmarshal(def.Query, &q); err != nil {
		return Input{}, invalid("query: the stored definition is not a query: %v", err)
	}
	in := Input{Condition: def.Condition, WindowDays: def.WindowDays, MinVolume: def.MinVolume}
	switch {
	case def.Template != "":
		in.Template, in.Fields = def.Template, maps.Clone(def.Fields)
	case def.Condition.Type == condSilence:
		in.Where, in.WhereSet = q.Where, true
	default:
		read := def.Read
		in.Query, in.Read = &q, &read
	}
	return in, nil
}

// parse checks def, whose query is q, and reads its number's place; the
// returned Parsed.Def has the read's defaults written out.
func parse(def store.AlertDef, q native.Query) (Parsed, error) {
	c := def.Condition
	if err := checkCondition(c); err != nil {
		return Parsed{}, err
	}
	silence := c.Type == condSilence
	switch {
	case silence && def.WindowDays != 1:
		return Parsed{}, invalid("window_days: a silence alert reads its last hours, not days; it takes none")
	case !silence && (def.WindowDays < 1 || def.WindowDays > maxWindowDays):
		return Parsed{}, invalid("window_days: %d is not between 1 and %d", def.WindowDays, maxWindowDays)
	case def.MinVolume != 0 && !(def.MinVolume >= 1 && !math.IsInf(def.MinVolume, 1)):
		return Parsed{}, invalid("min_volume: %v is not 1 or more (0 for none)", def.MinVolume)
	case silence && def.MinVolume != 0:
		return Parsed{}, invalid("min_volume: a silence alert watches whether events arrive at all; it takes none")
	}
	p := Parsed{Def: def, Query: q, Volume: -1}
	if silence {
		if def.Read != (store.AlertRead{}) || !sameJSON(q, silenceQuery(q.Where)) {
			return Parsed{}, invalid("query: a silence alert's query is the stat it builds from where; give where")
		}
		if err := validate(q); err != nil {
			return Parsed{}, err
		}
		p.Kind, p.Read = condSilence, readSilence
		return p, nil
	}
	if q.Kind != "stat" && q.Kind != "funnel" {
		return Parsed{}, invalid("query.kind: an alert reads a `stat` (one metric) or a `funnel` (one step over the first), not a %s", q.Kind)
	}
	if err := validate(q); err != nil {
		return Parsed{}, err
	}
	read := def.Read
	var err error
	if q.Kind == "stat" {
		p.Kind = "stat"
		p.Metric, p.Volume, p.Read, err = readStat(q, &read)
		if err == nil && c.Type == condThreshold && read.Volume == "" && def.MinVolume != 0 {
			err = invalid("min_volume: a threshold on a count or a number watches the value itself; name a volume metric in read.volume for min_volume to measure")
		}
	} else {
		p.Kind, p.Read = "funnel", readFraction
		p.Step, p.Base, err = readFunnel(q, &read)
	}
	if err != nil {
		return Parsed{}, err
	}
	p.Def.Read = read
	return p, nil
}

// validate is q.Check (Validate and the engine's compile of filter
// values, so save refuses what evaluation would), its refusal worded as
// a store refusal.
func validate(q native.Query) error {
	err := q.Check()
	if errors.Is(err, native.ErrInvalid) {
		return invalid("query: %s", strings.TrimPrefix(err.Error(), native.ErrInvalid.Error()+": "))
	}
	return err
}

// sameJSON says whether a and b encode the same, so an empty list equals
// none.
func sameJSON(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}

// readStat finds the metric r reads in the stat q, and its volume
// metric, defaulting r's metric when q has one.
func readStat(q native.Query, r *store.AlertRead) (metric, volume int, kind readKind, err error) {
	switch {
	case r.Step != "":
		return 0, 0, 0, invalid("read.step: a stat's read names a metric, not a step")
	case r.From != "":
		return 0, 0, 0, invalid("read.from: a stat's read names a metric; from is a funnel's")
	}
	names := make([]string, len(q.Metrics))
	for i, m := range q.Metrics {
		names[i] = m.Name()
	}
	list := strings.Join(names, ", ")
	if r.Metric == "" {
		if len(names) != 1 {
			return 0, 0, 0, invalid("read.metric: the stat has %d metrics; name the one the alert reads (%s)", len(names), list)
		}
		r.Metric = names[0]
	}
	metric = slices.Index(names, r.Metric)
	if metric < 0 {
		return 0, 0, 0, invalid("read.metric: %q is not one of the stat's metrics (%s)", r.Metric, list)
	}
	m := q.Metrics[metric]
	switch {
	case m.Op == "latest":
		return 0, 0, 0, invalid("read.metric: %s reads the latest row's value, not a number an alert can watch", r.Metric)
	case q.ColumnTypes()[metric] != "number":
		return 0, 0, 0, invalid("read.metric: %s of %s is not a number an alert can watch", m.Op, m.Column)
	}
	switch {
	case m.Op == "bounce_rate":
		kind = readFraction
	case countOps[m.Op]:
		kind = readCount
	default:
		kind = readNumber
	}
	if r.Volume == "" {
		if rateOps[m.Op] {
			return 0, 0, 0, invalid("read.volume: %s is a rate or an average; name a second metric counting the same rows (%s) in read.volume, so a day of few rows is not judged", m.Op, strings.Join(volumeOps, ", "))
		}
		return metric, -1, kind, nil
	}
	volume = slices.Index(names, r.Volume)
	switch {
	case volume < 0:
		return 0, 0, 0, invalid("read.volume: %q is not one of the stat's metrics (%s)", r.Volume, list)
	case volume == metric:
		return 0, 0, 0, invalid("read.volume: %s is the metric read; name a second metric, a count of the same rows", r.Volume)
	}
	v := q.Metrics[volume]
	if !slices.Contains(volumeOps, v.Op) {
		return 0, 0, 0, invalid("read.volume: %s is a %s, not a count (%s)", r.Volume, v.Op, strings.Join(volumeOps, ", "))
	}
	base, of := m.Where, r.Metric+"'s"
	if m.Op == "ratio" && m.Den != nil {
		base, of = m.Den.Where, r.Metric+"'s den's"
	}
	if !sameWhere(v.Where, base) {
		return 0, 0, 0, invalid("read.volume: %s counts other rows; its where must be the same as %s", r.Volume, of)
	}
	return metric, volume, kind, nil
}

// sameWhere says whether two metrics' filters are the same.
func sameWhere(a, b []native.Filter) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return sameJSON(a, b)
}

// readFunnel finds the step r reads in the funnel q and the step it is
// divided by, defaulting r's from to start.
func readFunnel(q native.Query, r *store.AlertRead) (step, base int, err error) {
	switch {
	case r.Metric != "":
		return 0, 0, invalid("read.metric: a funnel's read names a step, not a metric")
	case r.Volume != "":
		return 0, 0, invalid("read.volume: a funnel's volume is the step it is divided by; it takes none")
	}
	labels := make([]string, len(q.Steps))
	for i, s := range q.Steps {
		labels[i] = s.Label
	}
	list := strings.Join(labels, ", ")
	step = slices.Index(labels, r.Step)
	switch {
	case r.Step == "":
		return 0, 0, invalid("read.step: name the step the alert reads (one of %s but the first)", list)
	case step < 0:
		return 0, 0, invalid("read.step: %q is not one of the funnel's steps (%s)", r.Step, list)
	case step == 0:
		return 0, 0, invalid("read.step: %q is the first step, which every step is divided by; name a later one", r.Step)
	}
	switch r.From {
	case "", "start":
		r.From = "start"
		return step, 0, nil
	case "previous":
		return step, step - 1, nil
	}
	return 0, 0, invalid("read.from: %q is not start or previous", r.From)
}

// checkCondition refuses a condition out of its bounds, or carrying
// another type's fields.
func checkCondition(c store.AlertCondition) error {
	type field struct {
		name string
		set  bool
	}
	var stray []field
	switch c.Type {
	case condThreshold:
		switch {
		case c.Op != "below" && c.Op != "above":
			return invalid("condition.op: %q is not below or above", c.Op)
		case c.Value == nil:
			return invalid("condition.value: a threshold needs a value")
		case math.IsNaN(*c.Value) || math.IsInf(*c.Value, 0):
			return invalid("condition.value: %v is not a finite number", *c.Value)
		}
		stray = []field{{"direction", c.Direction != ""}, {"percent", c.Percent != 0}, {"against", c.Against != ""}, {"hours", c.Hours != 0}}
	case condChange:
		switch {
		case c.Direction != "drop" && c.Direction != "rise" && c.Direction != "either":
			return invalid("condition.direction: %q is not drop, rise or either", c.Direction)
		case !(c.Percent > 0 && c.Percent <= maxPercent):
			return invalid("condition.percent: %v is not above 0 and at most %d", c.Percent, maxPercent)
		case c.Against != "previous" && c.Against != "week_ago":
			return invalid("condition.against: %q is not previous or week_ago", c.Against)
		}
		stray = []field{{"op", c.Op != ""}, {"value", c.Value != nil}, {"hours", c.Hours != 0}}
	case condSilence:
		if c.Hours < 1 || c.Hours > maxSilenceHours {
			return invalid("condition.hours: %d is not between 1 and %d", c.Hours, maxSilenceHours)
		}
		stray = []field{{"op", c.Op != ""}, {"value", c.Value != nil}, {"direction", c.Direction != ""}, {"percent", c.Percent != 0}, {"against", c.Against != ""}}
	default:
		return invalid("condition.type: %q is not threshold, change or silence", c.Type)
	}
	for _, f := range stray {
		if f.set {
			return invalid("condition.%s: a %s condition takes none", f.name, c.Type)
		}
	}
	return nil
}

// Reference is an event or an attribute a query names: one of Event and
// Attr is set. Family is the family the query reads it from, "" for
// every one (the events table). Never marks one named under a never
// filter, which the project need not have sent: an actor who never did
// it is what the filter asks for.
type Reference struct {
	Family store.Family
	Event  string
	Attr   string
	Never  bool
}

// References are the events (event_name's eq and in values, $ names
// included) and attributes (attr_<key> columns) p's query names, in the
// order it names them, each once (once more, marked Never, when a never
// filter names it too).
func (p Parsed) References() []Reference {
	var r refs
	q := p.Query
	fam := tableFamily(q.Table)
	r.filters(fam, q.Where, false)
	for _, d := range q.Define {
		for _, c := range d.Cases {
			r.filters(fam, c.Where, false)
		}
	}
	for _, m := range q.Metrics {
		r.metric(fam, m)
	}
	for _, s := range q.Steps {
		r.filters(fam, s.Where, false)
	}
	return r.out
}

// tableFamily is the family a table's rows are, "" for events (every
// family) and the tables of no one family.
func tableFamily(table string) store.Family {
	switch table {
	case "product":
		return store.FamilyProduct
	case "measures":
		return store.FamilyMeasures
	case "views":
		return store.FamilyViews
	case "checks":
		return store.FamilyChecks
	}
	return ""
}

// refs collects References, each once.
type refs struct {
	out  []Reference
	seen map[Reference]bool
}

func (r *refs) add(ref Reference) {
	if r.seen[ref] {
		return
	}
	if r.seen == nil {
		r.seen = map[Reference]bool{}
	}
	r.seen[ref] = true
	r.out = append(r.out, ref)
}

func (r *refs) column(fam store.Family, column string, never bool) {
	if key, ok := strings.CutPrefix(column, "attr_"); ok && key != "" {
		r.add(Reference{Family: fam, Attr: key, Never: never})
	}
}

func (r *refs) metric(fam store.Family, m native.Metric) {
	r.column(fam, m.Column, false)
	r.filters(fam, m.Where, false)
	for _, side := range []*native.Metric{m.Num, m.Den} {
		if side != nil {
			r.metric(fam, *side)
		}
	}
}

// filters collects what fs names, marked Never under a never filter.
func (r *refs) filters(fam store.Family, fs []native.Filter, never bool) {
	for _, f := range fs {
		switch f.Op {
		case "ever", "never": // over the project's events, whatever the table
			r.filters("", f.Where, never || f.Op == "never")
			continue
		case "any_of":
			r.filters(fam, f.Any, never)
			continue
		}
		r.column(fam, f.Column, never)
		for _, c := range f.Columns {
			r.column(fam, c, never)
		}
		if f.Column == "event_name" && (f.Op == "eq" || f.Op == "in") {
			for _, v := range f.Values {
				r.add(Reference{Family: fam, Event: v, Never: never})
			}
		}
	}
}

// CheckName refuses an alert name that is blank, longer than 100
// characters, not UTF-8, or holds a control character.
func CheckName(name string) error {
	switch {
	case strings.TrimSpace(name) == "":
		return invalid("name: an alert needs a name")
	case !utf8.ValidString(name):
		return invalid("name: not valid UTF-8")
	case utf8.RuneCountInString(name) > maxNameRunes:
		return invalid("name: %d characters is more than %d", utf8.RuneCountInString(name), maxNameRunes)
	case strings.ContainsFunc(name, unicode.IsControl):
		return invalid("name: holds a control character (a tab, a line break)")
	}
	return nil
}
