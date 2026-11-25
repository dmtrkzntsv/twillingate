package alerts

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func ptr[T any](v T) *T { return &v }

func evt(name string) native.Filter {
	return native.Filter{Column: "event_name", Op: "eq", Values: []string{name}}
}

var (
	below5  = store.AlertCondition{Type: "threshold", Op: "below", Value: ptr(5.0)}
	drop30  = store.AlertCondition{Type: "change", Direction: "drop", Percent: 30, Against: "previous"}
	silent6 = store.AlertCondition{Type: "silence", Hours: 6}
)

// statIn is a stat alert over metrics, read as read, under cond.
func statIn(read store.AlertRead, cond store.AlertCondition, metrics ...native.Metric) Input {
	return Input{
		Query:     &native.Query{Kind: "stat", Table: "events", Metrics: metrics, Where: []native.Filter{evt("signup")}},
		Read:      &read,
		Condition: cond,
	}
}

// funnelIn is a three-step funnel alert read as read.
func funnelIn(read store.AlertRead) Input {
	return Input{
		Query: &native.Query{Kind: "funnel", Table: "events", Steps: []native.Step{
			{Label: "visit", Where: []native.Filter{evt("$page_view")}},
			{Label: "signup", Where: []native.Filter{evt("signup")}},
			{Label: "paid", Where: []native.Filter{evt("paid")}},
		}},
		Read:      &read,
		Condition: below5,
	}
}

// mustInvalid fails unless err is a refusal of kind ErrInvalid whose
// message contains each of want.
func mustInvalid(t *testing.T, err error, want ...string) {
	t.Helper()
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	for _, w := range want {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("err = %q, want it to contain %q", err, w)
		}
	}
}

func mustBuild(t *testing.T, in Input) store.AlertDef {
	t.Helper()
	def, err := Build(in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return def
}

func mustParse(t *testing.T, in Input) Parsed {
	t.Helper()
	p, err := Parse(mustBuild(t, in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return p
}

func TestBuildRefusesOtherKinds(t *testing.T) {
	for _, q := range []native.Query{
		{Kind: "trend", Table: "events", Metrics: []native.Metric{{Op: "count"}}},
		{Kind: "breakdown", Table: "events", Metrics: []native.Metric{{Op: "count"}}, Dimensions: []string{"path"}},
	} {
		_, err := Build(Input{Query: &q, Condition: below5})
		mustInvalid(t, err, "an alert reads a `stat` (one metric) or a `funnel`")
	}
}

func TestBuildRefusesInvalidQuery(t *testing.T) {
	in := statIn(store.AlertRead{}, below5, native.Metric{Op: "nope"})
	_, err := Build(in)
	mustInvalid(t, err, "query: metrics[0].op")
	if strings.Contains(err.Error(), "native:") {
		t.Errorf("err = %q, want native's prefix trimmed", err)
	}
}

func TestBuildStatRead(t *testing.T) {
	def := mustBuild(t, statIn(store.AlertRead{}, below5, native.Metric{Op: "count"}))
	if def.Read != (store.AlertRead{Metric: "count"}) {
		t.Errorf("one metric: read = %+v, want metric count", def.Read)
	}
	p := mustParse(t, statIn(store.AlertRead{}, below5, native.Metric{Op: "count"}))
	if p.Kind != "stat" || p.Read != readCount || p.Metric != 0 || p.Volume != -1 {
		t.Errorf("parsed = %+v", p)
	}

	two := []native.Metric{{Op: "count"}, {Op: "actors"}}
	_, err := Build(statIn(store.AlertRead{}, below5, two...))
	mustInvalid(t, err, "read.metric")
	_, err = Build(statIn(store.AlertRead{Metric: "users"}, below5, two...))
	mustInvalid(t, err, "users")
	p = mustParse(t, statIn(store.AlertRead{Metric: "actors"}, below5, two...))
	if p.Metric != 1 {
		t.Errorf("metric actors: index %d, want 1", p.Metric)
	}
	_, err = Build(statIn(store.AlertRead{Metric: "users"}, below5, native.Metric{Op: "count"}))
	mustInvalid(t, err, "users")

	for _, m := range []native.Metric{
		{Op: "latest", Column: "value"},
		{Op: "max", Column: "day"},
		{Op: "max", Column: "received_at"},
		{Op: "min", Column: "ts"},
	} {
		_, err := Build(statIn(store.AlertRead{}, below5, m))
		mustInvalid(t, err, "not a number")
	}
	// A session's start is a time too.
	in := statIn(store.AlertRead{}, below5, native.Metric{Op: "min", Column: "started_at"})
	in.Query.Table, in.Query.Where = "sessions", nil
	_, err = Build(in)
	mustInvalid(t, err, "not a number")

	// A funnel's read fields are not a stat's.
	_, err = Build(statIn(store.AlertRead{Step: "x"}, below5, native.Metric{Op: "count"}))
	mustInvalid(t, err, "read.step")
	_, err = Build(statIn(store.AlertRead{From: "start"}, below5, native.Metric{Op: "count"}))
	mustInvalid(t, err, "read.from")
}

func TestBuildReadKinds(t *testing.T) {
	cnt := native.Metric{Op: "count"}
	for _, tc := range []struct {
		m    native.Metric
		want readKind
	}{
		{native.Metric{Op: "users"}, readCount},
		{native.Metric{Op: "daily_avg", Of: "actors"}, readNumber},
		{native.Metric{Op: "sum", Column: "value"}, readNumber},
		{native.Metric{Op: "avg", Column: "value"}, readNumber},
	} {
		in := statIn(store.AlertRead{Metric: tc.m.Name(), Volume: "count"}, drop30, tc.m, cnt)
		if p := mustParse(t, in); p.Read != tc.want {
			t.Errorf("%s: read kind %v, want %v", tc.m.Name(), p.Read, tc.want)
		}
	}
	in := statIn(store.AlertRead{Metric: "bounce_rate", Volume: "count"}, below5, native.Metric{Op: "bounce_rate"}, cnt)
	in.Query.Table, in.Query.Where = "sessions", nil
	if p := mustParse(t, in); p.Read != readFraction || p.Volume != 1 {
		t.Errorf("bounce_rate: %+v", p)
	}
	if p := mustParse(t, funnelIn(store.AlertRead{Step: "paid"})); p.Read != readFraction {
		t.Errorf("funnel: read kind %v", p.Read)
	}
	if p := mustParse(t, Input{WhereSet: true, Condition: silent6}); p.Read != readSilence || p.Volume != -1 {
		t.Errorf("silence: %+v", p)
	}
}

func TestBuildRateNeedsVolume(t *testing.T) {
	avg := native.Metric{Op: "avg", Column: "value"}
	_, err := Build(statIn(store.AlertRead{Metric: "avg_value"}, below5, avg, native.Metric{Op: "count"}))
	mustInvalid(t, err, "read.volume")

	_, err = Build(statIn(store.AlertRead{Metric: "avg_value", Volume: "sum_value"}, below5, avg, native.Metric{Op: "sum", Column: "value"}))
	mustInvalid(t, err, "read.volume", "sum")

	other := native.Metric{Op: "count", Where: []native.Filter{{Column: "country", Op: "eq", Values: []string{"DE"}}}}
	_, err = Build(statIn(store.AlertRead{Metric: "avg_value", Volume: "count"}, below5, avg, other))
	mustInvalid(t, err, "read.volume", "where")

	_, err = Build(statIn(store.AlertRead{Metric: "avg_value", Volume: "avg_value"}, below5, avg))
	mustInvalid(t, err, "read.volume")
	_, err = Build(statIn(store.AlertRead{Metric: "avg_value", Volume: "nope"}, below5, avg))
	mustInvalid(t, err, "read.volume", "nope")

	p := mustParse(t, statIn(store.AlertRead{Metric: "avg_value", Volume: "count"}, below5, avg, native.Metric{Op: "count"}))
	if p.Metric != 0 || p.Volume != 1 {
		t.Errorf("avg with count: %+v", p)
	}

	// A ratio's volume counts over its den's where.
	paid := []native.Filter{evt("paid")}
	signups := []native.Filter{evt("signup")}
	ratio := native.Metric{Op: "ratio", As: "conv",
		Num: &native.Metric{Op: "actors", Where: paid},
		Den: &native.Metric{Op: "actors", Where: signups}}
	in := statIn(store.AlertRead{Metric: "conv", Volume: "people"}, below5, ratio, native.Metric{Op: "actors", As: "people", Where: signups})
	in.Query.Where = nil
	p = mustParse(t, in)
	if p.Metric != 0 || p.Volume != 1 {
		t.Errorf("ratio: %+v", p)
	}
	in.Query.Metrics[1].Where = paid
	_, err = Build(in)
	mustInvalid(t, err, "read.volume", "where")

	// An empty where equals none.
	in = statIn(store.AlertRead{Metric: "avg_value", Volume: "count"}, below5, avg, native.Metric{Op: "count", Where: []native.Filter{}})
	if _, err := Build(in); err != nil {
		t.Errorf("an empty where: %v", err)
	}

	// Any other read may carry a volume.
	p = mustParse(t, statIn(store.AlertRead{Metric: "users", Volume: "count"}, below5, native.Metric{Op: "users"}, native.Metric{Op: "count"}))
	if p.Volume != 1 {
		t.Errorf("users with count: %+v", p)
	}
}

func TestBuildFunnelRead(t *testing.T) {
	_, err := Build(funnelIn(store.AlertRead{}))
	mustInvalid(t, err, "read.step")
	_, err = Build(funnelIn(store.AlertRead{Step: "visit"}))
	mustInvalid(t, err, "read.step", "first")
	_, err = Build(funnelIn(store.AlertRead{Step: "nope"}))
	mustInvalid(t, err, "read.step", "nope")
	_, err = Build(funnelIn(store.AlertRead{Step: "paid", From: "sideways"}))
	mustInvalid(t, err, "read.from")
	_, err = Build(funnelIn(store.AlertRead{Step: "paid", Volume: "x"}))
	mustInvalid(t, err, "read.volume")
	_, err = Build(funnelIn(store.AlertRead{Step: "paid", Metric: "x"}))
	mustInvalid(t, err, "read.metric")

	def := mustBuild(t, funnelIn(store.AlertRead{Step: "paid"}))
	if def.Read != (store.AlertRead{Step: "paid", From: "start"}) {
		t.Errorf("read = %+v, want from start", def.Read)
	}
	p := mustParse(t, funnelIn(store.AlertRead{Step: "paid"}))
	if p.Kind != "funnel" || p.Step != 2 || p.Base != 0 || p.Volume != -1 {
		t.Errorf("from start: %+v", p)
	}
	p = mustParse(t, funnelIn(store.AlertRead{Step: "paid", From: "previous"}))
	if p.Step != 2 || p.Base != p.Step-1 {
		t.Errorf("from previous: %+v", p)
	}
}

func TestBuildConditions(t *testing.T) {
	cnt := native.Metric{Op: "count"}
	for name, c := range map[string]store.AlertCondition{
		"no type":              {},
		"unknown type":         {Type: "spike"},
		"threshold op":         {Type: "threshold", Op: "equal", Value: ptr(1.0)},
		"threshold no value":   {Type: "threshold", Op: "below"},
		"threshold NaN":        {Type: "threshold", Op: "below", Value: ptr(math.NaN())},
		"threshold Inf":        {Type: "threshold", Op: "above", Value: ptr(math.Inf(1))},
		"threshold with hours": {Type: "threshold", Op: "above", Value: ptr(1.0), Hours: 3},
		"change direction":     {Type: "change", Direction: "down", Percent: 30, Against: "previous"},
		"change percent 0":     {Type: "change", Direction: "drop", Against: "previous"},
		"change percent 1001":  {Type: "change", Direction: "drop", Percent: 1001, Against: "previous"},
		"change percent NaN":   {Type: "change", Direction: "drop", Percent: math.NaN(), Against: "previous"},
		"change against":       {Type: "change", Direction: "rise", Percent: 30, Against: "yesterday"},
		"change with value":    {Type: "change", Direction: "rise", Percent: 30, Against: "previous", Value: ptr(1.0)},
		"silence hours 0":      {Type: "silence"},
		"silence hours 721":    {Type: "silence", Hours: 721},
		"silence with op":      {Type: "silence", Hours: 6, Op: "below"},
	} {
		var in Input
		if c.Type == "silence" {
			in = Input{WhereSet: true, Condition: c}
		} else {
			in = statIn(store.AlertRead{}, c, cnt)
		}
		if _, err := Build(in); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	for _, c := range []store.AlertCondition{
		{Type: "threshold", Op: "above", Value: ptr(0.0)},
		{Type: "change", Direction: "either", Percent: 1000, Against: "week_ago"},
	} {
		if _, err := Build(statIn(store.AlertRead{}, c, cnt)); err != nil {
			t.Errorf("%+v: %v", c, err)
		}
	}
	for _, h := range []int{1, 720} {
		if _, err := Build(Input{WhereSet: true, Condition: store.AlertCondition{Type: "silence", Hours: h}}); err != nil {
			t.Errorf("silence %dh: %v", h, err)
		}
	}
}

func TestBuildSources(t *testing.T) {
	cnt := native.Metric{Op: "count"}
	for name, in := range map[string]Input{
		"none":               {Condition: below5},
		"query and where":    {Query: &native.Query{Kind: "stat", Table: "events", Metrics: []native.Metric{cnt}}, WhereSet: true, Condition: silent6},
		"template and query": {Query: &native.Query{Kind: "stat", Table: "events", Metrics: []native.Metric{cnt}}, Template: "event_count", Fields: map[string]string{"event": "x"}, Condition: below5},
		"read without query": {Read: &store.AlertRead{Metric: "count"}, WhereSet: true, Condition: silent6},
		"fields with query":  {Query: &native.Query{Kind: "stat", Table: "events", Metrics: []native.Metric{cnt}}, Fields: map[string]string{"event": "x"}, Condition: below5},
		"unknown template":   {Template: "spikes", Condition: below5},
		"fields alone":       {Fields: map[string]string{"event": "x"}, Condition: below5},
		"query on silence":   statIn(store.AlertRead{}, silent6, cnt),
		"where on threshold": {WhereSet: true, Condition: below5},
	} {
		if _, err := Build(in); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func TestBuildMinVolume(t *testing.T) {
	cnt := native.Metric{Op: "count"}
	in := statIn(store.AlertRead{}, below5, cnt)
	in.MinVolume = 10
	_, err := Build(in)
	mustInvalid(t, err, "min_volume")

	in = statIn(store.AlertRead{}, drop30, cnt)
	in.MinVolume = 10
	if def := mustBuild(t, in); def.MinVolume != 10 {
		t.Errorf("change on a count: min_volume %v", def.MinVolume)
	}

	// A threshold on a count with a volume metric takes it.
	in = statIn(store.AlertRead{Metric: "users", Volume: "count"}, below5, native.Metric{Op: "users"}, cnt)
	in.MinVolume = 10
	mustBuild(t, in)

	// A funnel measures its base.
	f := funnelIn(store.AlertRead{Step: "paid"})
	f.MinVolume = 50
	mustBuild(t, f)

	_, err = Build(Input{WhereSet: true, Condition: silent6, MinVolume: 10})
	mustInvalid(t, err, "min_volume")

	for _, v := range []float64{0.5, -1, math.NaN(), math.Inf(1)} {
		in = statIn(store.AlertRead{}, drop30, cnt)
		in.MinVolume = v
		if _, err := Build(in); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("min_volume %v: err = %v", v, err)
		}
	}
}

func TestBuildWindowDays(t *testing.T) {
	cnt := native.Metric{Op: "count"}
	if def := mustBuild(t, statIn(store.AlertRead{}, below5, cnt)); def.WindowDays != 1 {
		t.Errorf("default window %d, want 1", def.WindowDays)
	}
	conv := Input{Template: "conversion", Fields: map[string]string{"from_event": "signup", "to_event": "paid"}, Condition: below5}
	if def := mustBuild(t, conv); def.WindowDays != 7 {
		t.Errorf("conversion default window %d, want 7", def.WindowDays)
	}
	conv.WindowDays = 1
	if def := mustBuild(t, conv); def.WindowDays != 1 {
		t.Errorf("conversion explicit window %d, want 1", def.WindowDays)
	}
	in := statIn(store.AlertRead{}, below5, cnt)
	in.WindowDays = 30
	mustBuild(t, in)
	for _, w := range []int{31, -1} {
		in.WindowDays = w
		_, err := Build(in)
		mustInvalid(t, err, "window_days")
	}
	_, err := Build(Input{WhereSet: true, Condition: silent6, WindowDays: 7})
	mustInvalid(t, err, "window_days")
	if def := mustBuild(t, Input{WhereSet: true, Condition: silent6, WindowDays: 1}); def.WindowDays != 1 {
		t.Errorf("silence window %d", def.WindowDays)
	}
}

func TestBuildStripsCompareAndShape(t *testing.T) {
	in := statIn(store.AlertRead{}, below5, native.Metric{Op: "count"})
	in.Query.Compare, in.Query.Shape = "previous", "long"
	def := mustBuild(t, in)
	var raw map[string]any
	if err := json.Unmarshal(def.Query, &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"compare", "shape"} {
		if _, ok := raw[k]; ok {
			t.Errorf("stored query has %s: %s", k, def.Query)
		}
	}
	if in.Query.Compare != "previous" {
		t.Error("Build changed the caller's query")
	}
	// A funnel's compare is ignored rather than refused by native.
	f := funnelIn(store.AlertRead{Step: "paid"})
	f.Query.Compare = "previous"
	mustBuild(t, f)
}

func TestBuildSilenceFromWhere(t *testing.T) {
	where := []native.Filter{evt("heartbeat")}
	def := mustBuild(t, Input{Where: where, WhereSet: true, Condition: silent6})
	var got native.Query
	if err := json.Unmarshal(def.Query, &got); err != nil {
		t.Fatal(err)
	}
	want := native.Query{Kind: "stat", Table: "events", Metrics: []native.Metric{{Op: "max", Column: "received_at"}}, Where: where}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("silence query = %+v, want %+v", got, want)
	}
	if def.Read != (store.AlertRead{}) || def.WindowDays != 1 {
		t.Errorf("silence def = %+v", def)
	}
	p, err := Parse(def)
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != "silence" || p.Read != readSilence || p.Metric != 0 {
		t.Errorf("parsed = %+v", p)
	}

	if _, err := Build(Input{Where: []native.Filter{}, WhereSet: true, Condition: silent6}); err != nil {
		t.Errorf("an empty where: %v", err)
	}
	_, err = Build(Input{Where: []native.Filter{{Column: "nope", Op: "eq", Values: []string{"x"}}}, WhereSet: true, Condition: silent6})
	mustInvalid(t, err, "query: where[0]")
}

func TestParseRefusesBrokenDefinitions(t *testing.T) {
	good := mustBuild(t, statIn(store.AlertRead{}, below5, native.Metric{Op: "count"}))
	for name, mutate := range map[string]func(*store.AlertDef){
		"bad json":       func(d *store.AlertDef) { d.Query = json.RawMessage(`{`) },
		"unknown metric": func(d *store.AlertDef) { d.Read.Metric = "nope" },
		"zero window":    func(d *store.AlertDef) { d.WindowDays = 0 },
		"bad condition":  func(d *store.AlertDef) { d.Condition = store.AlertCondition{Type: "spike"} },
		"silence over a count": func(d *store.AlertDef) {
			d.Condition = silent6
		},
	} {
		d := good
		mutate(&d)
		if _, err := Parse(d); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func TestParseRoundTrip(t *testing.T) {
	avg := native.Metric{Op: "avg", Column: "value"}
	for name, in := range map[string]Input{
		"stat":       statIn(store.AlertRead{Metric: "avg_value", Volume: "count"}, drop30, avg, native.Metric{Op: "count"}),
		"funnel":     funnelIn(store.AlertRead{Step: "signup", From: "previous"}),
		"silence":    {Where: []native.Filter{evt("heartbeat")}, WhereSet: true, Condition: silent6},
		"template":   {Template: "event_count", Fields: map[string]string{"event": "signup"}, Condition: drop30, MinVolume: 20},
		"conversion": {Template: "conversion", Fields: map[string]string{"from_event": "a", "to_event": "b"}, Condition: below5, WindowDays: 14},
	} {
		def := mustBuild(t, in)
		p, err := Parse(def)
		if err != nil {
			t.Fatalf("%s: Parse: %v", name, err)
		}
		if !reflect.DeepEqual(p.Def, def) {
			t.Errorf("%s: Parsed.Def = %+v, want %+v", name, p.Def, def)
		}
		// Build already resolved the source; parsing what it stored
		// gives the same answer.
		back, err := InputOf(def)
		if err != nil {
			t.Fatalf("%s: InputOf: %v", name, err)
		}
		def2 := mustBuild(t, back)
		if !reflect.DeepEqual(def2, def) {
			t.Errorf("%s: Build(InputOf(def)) = %+v, want %+v", name, def2, def)
		}
		p2, err := Parse(def2)
		if err != nil {
			t.Fatal(err)
		}
		if p2.Kind != p.Kind || p2.Read != p.Read || p2.Metric != p.Metric || p2.Volume != p.Volume || p2.Step != p.Step || p2.Base != p.Base {
			t.Errorf("%s: parsed again %+v, want %+v", name, p2, p)
		}
		switch name {
		case "template", "conversion":
			if back.Template != in.Template || !reflect.DeepEqual(back.Fields, in.Fields) || back.Query != nil || back.WhereSet {
				t.Errorf("%s: InputOf = %+v", name, back)
			}
		case "silence":
			if !back.WhereSet || !reflect.DeepEqual(back.Where, in.Where) || back.Query != nil || back.Template != "" {
				t.Errorf("%s: InputOf = %+v", name, back)
			}
		default:
			if back.Query == nil || back.Read == nil || back.WhereSet || back.Template != "" {
				t.Errorf("%s: InputOf = %+v", name, back)
			}
		}
		if back.Condition != def.Condition || back.WindowDays != def.WindowDays || back.MinVolume != def.MinVolume {
			t.Errorf("%s: InputOf lost the condition, window or min_volume: %+v", name, back)
		}
	}
	if _, err := InputOf(store.AlertDef{Query: json.RawMessage(`[`)}); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("InputOf of bad json: %v", err)
	}
}

func TestReferences(t *testing.T) {
	q := native.Query{Kind: "stat", Table: "product",
		Where: []native.Filter{
			evt("signup"),
			{Column: "event_name", Op: "in", Values: []string{"a", "b", "signup"}},
			{Column: "event_name", Op: "ne", Values: []string{"ignored"}},
			{Column: "attr_plan", Op: "eq", Values: []string{"pro"}},
			{Op: "any_of", Any: []native.Filter{evt("c"), {Column: "attr_tier", Op: "is_set"}}},
			{Op: "ever", Where: []native.Filter{evt("$page_view"), {Column: "attr_plan", Op: "eq", Values: []string{"x"}}}},
			{Op: "any_set", Columns: []string{"attr_seat", "user_id"}},
		},
		Metrics: []native.Metric{
			{Op: "count", Where: []native.Filter{evt("d")}},
			{Op: "ratio", As: "r",
				Num: &native.Metric{Op: "count", Where: []native.Filter{evt("num")}},
				Den: &native.Metric{Op: "count", Where: []native.Filter{evt("den")}}},
			{Op: "latest", Column: "attr_version"},
		},
	}
	P, F := store.FamilyProduct, store.Family("")
	want := []Reference{
		{Family: P, Event: "signup"},
		{Family: P, Event: "a"},
		{Family: P, Event: "b"},
		{Family: P, Attr: "plan"},
		{Family: P, Event: "c"},
		{Family: P, Attr: "tier"},
		{Family: F, Event: "$page_view"},
		{Family: F, Attr: "plan"},
		{Family: P, Attr: "seat"},
		{Family: P, Event: "d"},
		{Family: P, Event: "num"},
		{Family: P, Event: "den"},
		{Family: P, Attr: "version"},
	}
	if got := (Parsed{Query: q}).References(); !reflect.DeepEqual(got, want) {
		t.Errorf("References =\n%+v\nwant\n%+v", got, want)
	}

	defined := native.Query{Kind: "stat", Table: "measures", Define: []native.Define{{Name: "def_kind", Cases: []native.Case{
		{Label: "a", Where: []native.Filter{evt("$lcp")}},
		{Label: "b", Where: []native.Filter{{Column: "attr_route", Op: "is_set"}}},
	}}}}
	wantD := []Reference{{Family: store.FamilyMeasures, Event: "$lcp"}, {Family: store.FamilyMeasures, Attr: "route"}}
	if got := (Parsed{Query: defined}).References(); !reflect.DeepEqual(got, wantD) {
		t.Errorf("define References = %+v, want %+v", got, wantD)
	}

	f := mustParse(t, funnelIn(store.AlertRead{Step: "paid"}))
	wantF := []Reference{{Family: F, Event: "$page_view"}, {Family: F, Event: "signup"}, {Family: F, Event: "paid"}}
	if got := f.References(); !reflect.DeepEqual(got, wantF) {
		t.Errorf("funnel References = %+v, want %+v", got, wantF)
	}

	for table, fam := range map[string]store.Family{
		"events": "", "views": store.FamilyViews, "measures": store.FamilyMeasures, "checks": store.FamilyChecks, "sessions": "",
	} {
		got := (Parsed{Query: native.Query{Kind: "stat", Table: table, Where: []native.Filter{evt("x")}}}).References()
		if len(got) != 1 || got[0] != (Reference{Family: fam, Event: "x"}) {
			t.Errorf("table %s: %+v, want family %q", table, got, fam)
		}
	}
	vf := native.Query{Kind: "funnel", Table: "views", Steps: []native.Step{{Label: "a", Where: []native.Filter{evt("x")}}}}
	if got := (Parsed{Query: vf}).References(); len(got) != 1 || got[0].Family != store.FamilyViews {
		t.Errorf("views funnel: %+v", got)
	}

	// What a never filter names is marked Never, and only there: an event
	// named outside it as well is a reference of each kind.
	nq := native.Query{Kind: "stat", Table: "product", Where: []native.Filter{
		evt("signup"),
		{Op: "never", Where: []native.Filter{evt("refund"), {Column: "attr_coupon", Op: "is_set"},
			{Op: "any_of", Any: []native.Filter{evt("signup")}}}},
		{Op: "ever", Where: []native.Filter{evt("paid")}},
	}}
	wantN := []Reference{
		{Family: P, Event: "signup"},
		{Family: F, Event: "refund", Never: true},
		{Family: F, Attr: "coupon", Never: true},
		{Family: F, Event: "signup", Never: true},
		{Family: F, Event: "paid"},
	}
	if got := (Parsed{Query: nq}).References(); !reflect.DeepEqual(got, wantN) {
		t.Errorf("never References =\n%+v\nwant\n%+v", got, wantN)
	}
}

func TestCheckName(t *testing.T) {
	for _, ok := range []string{"a", strings.Repeat("é", 100), "Signups dropped (EU)"} {
		if err := CheckName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "   ", "\xff", strings.Repeat("a", 101), "tab\there", "line\nbreak", "del\x7f"} {
		if err := CheckName(bad); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("%q: err = %v, want ErrInvalid", bad, err)
		}
	}
}

// TestBuildRefusesValuesTheEngineWould: a filter value its column cannot
// hold, which Validate lets through and the engine refuses, is refused
// at save rather than breaking the alert at its first evaluation.
func TestBuildRefusesValuesTheEngineWould(t *testing.T) {
	wide := native.Filter{Column: "display_width", Op: "eq", Values: []string{"wide"}}
	in := statIn(store.AlertRead{}, below5, native.Metric{Op: "count"})
	in.Query.Where = append(in.Query.Where, wide)
	_, err := Build(in)
	mustInvalid(t, err, `query: where[1].values: "wide" is not an integer`)

	_, err = Build(Input{Where: []native.Filter{wide}, WhereSet: true, Condition: silent6})
	mustInvalid(t, err, `query: where[0].values: "wide" is not an integer`)

	f := funnelIn(store.AlertRead{Step: "paid"})
	f.Query.Steps[1].Where = append(f.Query.Steps[1].Where, wide)
	_, err = Build(f)
	mustInvalid(t, err, `query: steps[1].where[1].values: "wide" is not an integer`)
}
