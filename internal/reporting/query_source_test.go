package reporting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// queryBarList, queryStat and queryTable are bar_list, stat and table as
// they accept a query source, which testdata's components do not.
const (
	queryBarList = `{"name":"bar_list","description":"Bars.","accepts":["sql","query"],"inputs":{"open":false,"columns":[{"name":"label","types":["text"]},{"name":"value","types":["number"]}]},"props":{"type":"object"},"default_width":6,"default_height":4}`
	queryStat    = `{"name":"stat","description":"A number.","accepts":["sql","query"],"inputs":{"open":false,"columns":[{"name":"value","types":["number"]},{"name":"previous","types":["number"],"optional":true},{"name":"x","types":["day"],"optional":true}]},"props":{"type":"object"},"default_width":3,"default_height":3}`
	queryTable   = `{"name":"table","description":"Rows.","accepts":["sql","query"],"inputs":{"open":true,"columns":[]},"props":{"type":"object","properties":{"mode":{"enum":["local","remote"]}}},"default_width":6,"default_height":10}`
)

// countryBars is a breakdown of actors by country, renamed for bar_list.
const countryBars = `{"query":{"kind":"breakdown","table":"events","metrics":[{"op":"actors"}],"dimensions":["country"]},"rename":{"country":"label","actors":"value"}}`

// querySources is the registered source types over files and an engine
// on its directory, with rules recording the projects it was asked for.
func querySources(t *testing.T, files *testFiles, asked *[]int64) map[string]SourceType {
	t.Helper()
	rules := func(_ context.Context, project int64) archive.Rules {
		if asked != nil {
			*asked = append(*asked, project)
		}
		return archive.Rules{}
	}
	return newSources(files, false, time.Now, native.New(native.Options{Dir: files.Dir}), rules)
}

// countryEvent is one view of project 1 on day by actor in country.
func countryEvent(t *testing.T, id, day, actor, country string) store.Event {
	t.Helper()
	e := event(t, id, 1, store.FamilyViews, "$page_view", day)
	e.ActorID, e.Country = actor, country
	return e
}

func writeCountries(t *testing.T, files *testFiles) {
	t.Helper()
	files.Write(t,
		countryEvent(t, "e1", "2026-10-20", "a", "DE"),
		countryEvent(t, "e2", "2026-10-21", "b", "DE"),
		countryEvent(t, "e3", "2026-10-21", "a", "DE"),
		countryEvent(t, "e4", "2026-10-21", "c", "FR"))
}

var countryParams = Params{ProjectID: 1, From: "2026-10-20", To: "2026-10-21"}

func TestQuerySourceLoads(t *testing.T) {
	_, files := newTestStoreAndFiles(t, 1000)
	writeCountries(t, files)
	var asked []int64
	src := querySources(t, files, &asked)["query"]
	if err := src.Validate(context.Background(), countryBars, oneComponent(t, queryBarList)); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if p, r := src.Follows(countryBars); !p || !r {
		t.Errorf("Follows = %v, %v; want true, true", p, r)
	}
	if !src.Cacheable() {
		t.Error("Cacheable = false, want true")
	}
	got, err := src.Load(context.Background(), countryBars, countryParams)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := readsql.Result{Columns: []string{"label", "value"}, Rows: [][]string{{"DE", "2"}, {"FR", "1"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Load = %+v, want %+v", got, want)
	}
	if !reflect.DeepEqual(asked, []int64{1}) {
		t.Errorf("rules asked for %v, want [1]", asked)
	}
	if err := oneComponent(t, queryBarList).checkRows(got.(readsql.Result)); err != nil {
		t.Errorf("checkRows: %v", err)
	}

	// Capped at Files.MaxRows, the rest cut and said so.
	files.maxRows = 1
	got, err = src.Load(context.Background(), countryBars, countryParams)
	if err != nil {
		t.Fatalf("Load capped: %v", err)
	}
	want = readsql.Result{Columns: []string{"label", "value"}, Rows: [][]string{{"DE", "2"}}, Truncated: true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Load capped = %+v, want %+v", got, want)
	}
}

func TestQuerySourceStatPrevious(t *testing.T) {
	_, files := newTestStoreAndFiles(t, 1000)
	writeCountries(t, files)
	content := `{"query":{"kind":"stat","table":"views","metrics":[{"op":"actors","as":"value"}],"compare":"previous"},"rename":{"previous_value":"previous"}}`
	src := querySources(t, files, nil)["query"]
	stat := oneComponent(t, queryStat)
	if err := src.Validate(context.Background(), content, stat); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	got, err := src.Load(context.Background(), content, Params{ProjectID: 1, From: "2026-10-21", To: "2026-10-21"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := readsql.Result{Columns: []string{"value", "previous"}, Rows: [][]string{{"3", "1"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Load = %+v, want %+v", got, want)
	}

	// Without the rename, previous_value is no input of stat.
	bare := `{"query":{"kind":"stat","table":"views","metrics":[{"op":"actors","as":"value"}],"compare":"previous"}}`
	wantRefusal(t, src.Validate(context.Background(), bare, stat), store.ErrInvalid, "previous_value is not an input of stat")
}

func TestQuerySourceValidate(t *testing.T) {
	_, files := newTestStoreAndFiles(t, 1000)
	svc := &Service{files: files, sources: querySources(t, files, nil), now: time.Now}
	comps := map[string]Component{}
	for _, c := range []string{queryBarList, queryStat, queryTable} {
		comp := oneComponent(t, c)
		comps[comp.Name] = comp
	}
	widget := func(component, props, content string) store.Widget {
		return store.Widget{Component: component, SourceType: "query", Source: content, Width: 6, Height: 4, Props: props}
	}
	stat := `{"kind":"stat","table":"events","metrics":[{"op":"count","as":"value"}]}`
	cases := []struct {
		name string
		w    store.Widget
		msg  string
	}{
		{"not JSON", widget("stat", "{}", `{"query":`), "query source: unexpected EOF"},
		{"empty", widget("stat", "{}", ` `), "query source is empty"},
		{"unknown field", widget("stat", "{}", `{"query":`+stat+`,"limit":3}`), `query source: json: unknown field "limit"`},
		{"unknown query field", widget("stat", "{}", `{"query":{"kind":"stat","table":"events","metrics":[{"op":"count"}],"group":"x"}}`), `query source: json: unknown field "group"`},
		{"trailing", widget("stat", "{}", `{"query":`+stat+`} {}`), "query source: more than one JSON value"},
		{"invalid query", widget("stat", "{}", `{"query":{"kind":"pie","table":"events","metrics":[{"op":"count"}]}}`),
			`query.kind: "pie" is not one of stat, trend, breakdown, retention, attributes`},
		{"rename unknown", widget("stat", "{}", `{"query":`+stat+`,"rename":{"count":"value"}}`),
			"rename: count is not a column of the query; its columns are value"},
		{"rename onto one", widget("bar_list", "{}", `{"query":{"kind":"breakdown","table":"events","metrics":[{"op":"actors"}],"dimensions":["country"]},"rename":{"country":"value","actors":"value"}}`),
			"rename: actors and country are both renamed value"},
		{"rename onto kept", widget("bar_list", "{}", `{"query":{"kind":"breakdown","table":"events","metrics":[{"op":"actors"}],"dimensions":["country"]},"rename":{"country":"actors"}}`),
			"rename: country is renamed actors, which the query already returns"},
		{"rename empty", widget("stat", "{}", `{"query":`+stat+`,"rename":{"value":" "}}`),
			"rename: value is renamed to an empty name"},
		{"columns", widget("bar_list", "{}", `{"query":{"kind":"breakdown","table":"events","metrics":[{"op":"actors"}],"dimensions":["country"]}}`),
			"bar_list needs label (text); columns are country, actors"},
		{"remote table", widget("table", `{"mode":"remote"}`, `{"query":`+stat+`}`),
			`a remote table takes sql only; a query source needs props.mode "local"`},
		{"text into number", widget("stat", "{}", `{"query":{"kind":"breakdown","table":"events","metrics":[{"op":"count"}],"dimensions":["path"]},"rename":{"path":"value","count":"previous"}}`),
			"stat.value: the query's column is text, not number"},
		{"attribute into number", widget("stat", "{}", `{"query":{"kind":"breakdown","table":"events","metrics":[{"op":"count","as":"value"}],"dimensions":["attr_n"]},"rename":{"attr_n":"previous"}}`),
			"stat.previous: the query's column is text, not number"},
		{"retention offset into day", widget("stat", "{}", `{"query":{"kind":"retention","output":"curve","identified_by":"user"},"rename":{"retention":"value","offset":"x"}}`),
			"stat.x: the query's column is number, not day"},
		{"retention grid identified_by", widget("stat", "{}", `{"query":{"kind":"retention","output":"grid","identified_by":"group"}}`),
			`query.identified_by: "group" is not install or user; a grid without it has both`},
		{"other day into day", widget("stat", "{}", `{"query":{"kind":"breakdown","table":"events","metrics":[{"op":"count","as":"value"}],"dimensions":["day"],"other":true},"rename":{"day":"x"}}`),
			"stat.x: the query's column is text, not day"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantRefusal(t, svc.validateWidget(context.Background(), comps, c.w), store.ErrInvalid, c.msg)
		})
	}

	// Columns whose types fit: day into a day, a number dimension and a
	// metric into numbers, a stat's previous_ column into a number.
	for _, content := range []string{
		`{"query":{"kind":"breakdown","table":"events","metrics":[{"op":"count","as":"value"}],"dimensions":["day"]},"rename":{"day":"x"}}`,
		`{"query":{"kind":"breakdown","table":"events","metrics":[{"op":"count","as":"previous"}],"dimensions":["display_width"]},"rename":{"display_width":"value"}}`,
		`{"query":{"kind":"stat","table":"views","metrics":[{"op":"actors","as":"value"}],"compare":"previous"},"rename":{"previous_value":"previous"}}`,
		`{"query":{"kind":"trend","table":"views","metrics":[{"op":"count","as":"value"}]},"rename":{"day":"x"}}`,
		`{"query":{"kind":"retention","output":"curve","identified_by":"user"},"rename":{"retention":"value","offset":"previous"}}`,
		`{"query":{"kind":"retention","output":"milestones","identified_by":"install","milestones":[7,30]},"rename":{"d7":"value","d30":"previous"}}`,
	} {
		if err := svc.validateWidget(context.Background(), comps, widget("stat", "{}", content)); err != nil {
			t.Errorf("%s: %v", content, err)
		}
	}
	// bar_list's label is text, which takes any column.
	if err := svc.validateWidget(context.Background(), comps, widget("bar_list", "{}",
		`{"query":{"kind":"breakdown","table":"events","metrics":[{"op":"count","as":"value"}],"dimensions":["day"]},"rename":{"day":"label"}}`)); err != nil {
		t.Errorf("day into text: %v", err)
	}

	// A local table takes whatever the query returns.
	if err := svc.validateWidget(context.Background(), comps, widget("table", `{"mode":"local"}`, `{"query":`+stat+`}`)); err != nil {
		t.Errorf("local table: %v", err)
	}
}

func TestQuerySourceEmptyProject(t *testing.T) {
	_, files := newTestStoreAndFiles(t, 1000)
	src := querySources(t, files, nil)["query"]
	got, err := src.Load(context.Background(), countryBars, Params{ProjectID: 99, From: "2026-10-01", To: "2026-10-21"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	res := got.(readsql.Result)
	if res.Rows == nil || len(res.Rows) != 0 || !reflect.DeepEqual(res.Columns, []string{"label", "value"}) {
		t.Errorf("Load = %+v, want columns label, value and no rows", res)
	}
}

func TestQuerySourceWithoutNative(t *testing.T) {
	_, files := newTestStoreAndFiles(t, 1000)
	src := newSources(files, false, time.Now, nil, nil)["query"]
	// Validating needs no engine: the columns come from the query.
	if err := src.Validate(context.Background(), countryBars, oneComponent(t, queryBarList)); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	_, err := src.Load(context.Background(), countryBars, countryParams)
	wantRefusal(t, err, store.ErrInvalid, "structured queries are not available in this process")
}

// TestQuerySourceRefusesTooLarge: native's refusals of a query it was
// given read as the widget's own refusal.
func TestQuerySourceRefusesTooLarge(t *testing.T) {
	_, files := newTestStoreAndFiles(t, 1000)
	writeCountries(t, files)
	nat := native.New(native.Options{Dir: files.Dir, MaxBytes: 1})
	src := newSources(files, false, time.Now, nat, nil)["query"]
	content := `{"query":{"kind":"stat","table":"views","metrics":[{"op":"actors","as":"value"}]}}`
	_, err := src.Load(context.Background(), content, countryParams)
	if !errors.Is(err, store.ErrInvalid) || !strings.Contains(err.Error(), "more memory than allowed") {
		t.Errorf("err = %v, want an invalid refusal naming the memory", err)
	}
}

func TestQuerySourcePreview(t *testing.T) {
	svc, _ := newTestServiceOpts(t, Options{}, 1000)
	files := filesOf(t, svc)
	writeCountries(t, files)
	svc.sources = querySources(t, files, nil)
	if err := svc.st.SyncReporting(context.Background(), store.ReportingSync{
		Hash: "query", Version: "test", Components: []store.Component{oneComponent(t, queryBarList).row(), oneComponent(t, queryTable).row()},
	}); err != nil {
		t.Fatal(err)
	}
	// A remote table is refused as add_widget refuses it.
	_, err := svc.Preview(context.Background(), PreviewIn{Component: "table", Props: json.RawMessage(`{"mode":"remote"}`),
		Source: Source{Type: "query", Content: countryBars}, ProjectID: 1, From: "2026-10-20", To: "2026-10-21"})
	wantRefusal(t, err, store.ErrInvalid, `a remote table takes sql only; a query source needs props.mode "local"`)

	in := PreviewIn{Component: "bar_list", Source: Source{Type: "query", Content: countryBars}, From: "2026-10-20", To: "2026-10-21"}
	_, err = svc.Preview(context.Background(), in)
	wantRefusal(t, err, store.ErrInvalid, "a query follows the project; pass project_id")

	in.ProjectID = 1
	got, err := svc.Preview(context.Background(), in)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if got.Problem != "" || !reflect.DeepEqual(got.Rows, [][]string{{"DE", "2"}, {"FR", "1"}}) {
		t.Errorf("Preview = %+v", got)
	}
	in.Props = json.RawMessage(`{}`)
	in.From, in.To = "", ""
	_, err = svc.Preview(context.Background(), in)
	wantRefusal(t, err, store.ErrInvalid, "a query follows the date range; pass from and to")
}

// TestQuerySourceApproximate: a stat over more distinct actors than a
// sketch counts exactly reaches widget_data with its renamed column named
// approximate, from the load and again from the cache.
func TestQuerySourceApproximate(t *testing.T) {
	svc, _ := newTestServiceOpts(t, Options{}, 1000)
	files := filesOf(t, svc)
	var evs []store.Event
	for d, day := range []string{"2026-10-20", "2026-10-21"} {
		for i := range 1500 {
			e := event(t, fmt.Sprintf("a-%s-%d", day, i), 1, store.FamilyViews, "$page_view", day)
			e.ActorID = fmt.Sprintf("actor-%d", d*1000+i)
			evs = append(evs, e)
		}
	}
	files.Write(t, evs...)
	svc.sources = querySources(t, files, nil)
	if err := svc.st.SyncReporting(context.Background(), store.ReportingSync{
		Hash: "query", Version: "test", Components: []store.Component{oneComponent(t, queryStat).row()},
	}); err != nil {
		t.Fatal(err)
	}
	content := `{"query":{"kind":"stat","table":"views","metrics":[{"op":"actors"}]},"rename":{"actors":"value"}}`
	d := mustCreate(t, svc, "Approx", WidgetSpec{Name: "s", Component: "stat", Title: "s", Source: Source{Type: "query", Content: content}})
	in := DataRequest{WidgetID: d.Widgets[0].ID, ProjectID: 1, From: "2026-10-20", To: "2026-10-21"}
	for _, pass := range []string{"load", "cache"} {
		got, err := svc.WidgetData(context.Background(), in)
		if err != nil {
			t.Fatalf("%s: WidgetData: %v", pass, err)
		}
		res, ok := got.Data.(readsql.Result)
		if !ok {
			t.Fatalf("%s: Data is %T", pass, got.Data)
		}
		if !reflect.DeepEqual(res.Approximate, []string{"value"}) {
			t.Errorf("%s: approximate = %q, want [value]", pass, res.Approximate)
		}
		b, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `"approximate":["value"]`) {
			t.Errorf("%s: envelope %s has no approximate", pass, b)
		}
	}

	// A day with no events counts exactly: no approximate key at all.
	in.From, in.To = "2026-10-19", "2026-10-19"
	got, err := svc.WidgetData(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(got); strings.Contains(string(b), "approximate") {
		t.Errorf("empty day: envelope %s names approximate", b)
	}
}

// TestQuerySourceLabels: labels relabel a column's values after rename,
// keyed on the renamed name; values not listed pass through, and a
// column that is no column, or a day or number, is refused.
func TestQuerySourceLabels(t *testing.T) {
	_, files := newTestStoreAndFiles(t, 1000)
	writeCountries(t, files)
	src := querySources(t, files, nil)["query"]
	bars := oneComponent(t, queryBarList)
	content := `{"query":{"kind":"breakdown","table":"events","metrics":[{"op":"actors"}],"dimensions":["country"]},` +
		`"rename":{"country":"label","actors":"value"},"labels":{"label":{"DE":"Germany","US":"United States"}}}`
	if err := src.Validate(context.Background(), content, bars); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	got, err := src.Load(context.Background(), content, countryParams)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := readsql.Result{Columns: []string{"label", "value"}, Rows: [][]string{{"Germany", "2"}, {"FR", "1"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Load = %+v, want %+v", got, want)
	}

	table := oneComponent(t, queryTable)
	for _, c := range []struct{ name, content, msg string }{
		{"unknown column", `{"query":{"kind":"breakdown","table":"events","metrics":[{"op":"actors"}],"dimensions":["country"]},"rename":{"country":"label"},"labels":{"country":{"DE":"Germany"}}}`,
			"labels.country: not a column of the query; its columns are label, actors"},
		{"day column", `{"query":{"kind":"trend","table":"events","metrics":[{"op":"count"}]},"labels":{"day":{"2026-10-20":"Monday"}}}`,
			"labels.day: a day column takes no labels"},
		{"number column", `{"query":{"kind":"breakdown","table":"events","metrics":[{"op":"actors"}],"dimensions":["country"]},"labels":{"actors":{"1":"one"}}}`,
			"labels.actors: a number column takes no labels"},
	} {
		t.Run(c.name, func(t *testing.T) {
			wantRefusal(t, src.Validate(context.Background(), c.content, table), store.ErrInvalid, c.msg)
		})
	}
}

// TestQuerySourceLabelsKeepRows: labels relabel values only: two values
// under one label stay two rows, and the "" key labels the empty value.
func TestQuerySourceLabelsKeepRows(t *testing.T) {
	_, files := newTestStoreAndFiles(t, 1000)
	writeCountries(t, files)
	files.Write(t, countryEvent(t, "e5", "2026-10-21", "d", ""))
	src := querySources(t, files, nil)["query"]
	content := `{"query":{"kind":"breakdown","table":"events","metrics":[{"op":"actors"}],"dimensions":["country"]},` +
		`"rename":{"country":"label","actors":"value"},"labels":{"label":{"DE":"Europe","FR":"Europe","":"Unknown"}}}`
	if err := src.Validate(context.Background(), content, oneComponent(t, queryBarList)); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	got, err := src.Load(context.Background(), content, countryParams)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := readsql.Result{Columns: []string{"label", "value"}, Rows: [][]string{{"Europe", "2"}, {"Europe", "1"}, {"Unknown", "1"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Load = %+v, want %+v", got, want)
	}
}

// TestQuerySourceColumns: columns keeps the named columns (after rename)
// in its order, the rest dropped, before the component checks them; a
// list that is empty, names no column or one twice is refused.
func TestQuerySourceColumns(t *testing.T) {
	svc, _ := newTestServiceOpts(t, Options{}, 1000)
	files := filesOf(t, svc)
	writeCountries(t, files)
	svc.sources = querySources(t, files, nil)
	src := svc.sources["query"]
	bars, stat := oneComponent(t, queryBarList), oneComponent(t, queryStat)
	breakdown := `{"kind":"breakdown","table":"events","metrics":[{"op":"count"},{"op":"actors"}],"dimensions":["country"]}`
	content := `{"query":` + breakdown + `,"rename":{"country":"label"},"columns":["actors","label"]}`
	if err := src.Validate(context.Background(), content, oneComponent(t, queryTable)); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	got, err := src.Load(context.Background(), content, countryParams)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := readsql.Result{Columns: []string{"actors", "label"}, Rows: [][]string{{"2", "DE"}, {"1", "FR"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Load = %+v, want %+v", got, want)
	}

	// The component reads the picked columns: dropping count lets bar_list
	// take the rest; picking count for a number checks its type.
	if err := src.Validate(context.Background(), `{"query":`+breakdown+`,"rename":{"country":"label","actors":"value"},"columns":["label","value"]}`, bars); err != nil {
		t.Errorf("bar_list: %v", err)
	}
	wantRefusal(t, src.Validate(context.Background(), `{"query":`+breakdown+`,"rename":{"country":"value"},"columns":["value"]}`, stat),
		store.ErrInvalid, "stat.value: the query's column is text, not number")
	wantRefusal(t, src.Validate(context.Background(), `{"query":`+breakdown+`,"rename":{"country":"label","actors":"value"},"columns":["label"]}`, bars),
		store.ErrInvalid, "bar_list needs value (number); columns are label")
	for _, c := range []struct{ name, columns, msg string }{
		{"empty", `[]`, "columns: name at least one; omit it to keep every column"},
		{"unknown", `["country"]`, "columns: country is not a column of the query; its columns are label, count, actors"},
		{"twice", `["label","count","label"]`, "columns: label is named twice"},
	} {
		t.Run(c.name, func(t *testing.T) {
			wantRefusal(t, src.Validate(context.Background(), `{"query":`+breakdown+`,"rename":{"country":"label"},"columns":`+c.columns+`}`,
				oneComponent(t, queryTable)), store.ErrInvalid, c.msg)
		})
	}

	// A table widget and its preview return the picked columns.
	if err := svc.st.SyncReporting(context.Background(), store.ReportingSync{
		Hash: "query", Version: "test", Components: []store.Component{oneComponent(t, queryTable).row()},
	}); err != nil {
		t.Fatal(err)
	}
	d := mustCreate(t, svc, "Columns", WidgetSpec{Name: "t", Component: "table", Title: "t", Props: json.RawMessage(`{"mode":"local"}`),
		Source: Source{Type: "query", Content: content}})
	data, err := svc.WidgetData(context.Background(), DataRequest{WidgetID: d.Widgets[0].ID, ProjectID: 1, From: "2026-10-20", To: "2026-10-21"})
	if err != nil {
		t.Fatalf("WidgetData: %v", err)
	}
	if res, ok := data.Data.(readsql.Result); !ok || !reflect.DeepEqual(res, want) {
		t.Errorf("WidgetData = %+v, want %+v", data.Data, want)
	}
	pre, err := svc.Preview(context.Background(), PreviewIn{Component: "table", Props: json.RawMessage(`{"mode":"local"}`),
		Source: Source{Type: "query", Content: content}, ProjectID: 1, From: "2026-10-20", To: "2026-10-21"})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if pre.Problem != "" || !reflect.DeepEqual(pre.Result, want) {
		t.Errorf("Preview = %+v, want %+v", pre, want)
	}
}

// pickColumns keeps the approximate marks of the columns it keeps.
func TestPickColumnsApproximate(t *testing.T) {
	res := readsql.Result{Columns: []string{"a", "b", "c"}, Rows: [][]string{{"1", "2", "3"}}, Approximate: []string{"a", "c"}}
	got, err := pickColumns(res, []string{"c", "b"})
	if err != nil {
		t.Fatal(err)
	}
	want := readsql.Result{Columns: []string{"c", "b"}, Rows: [][]string{{"3", "2"}}, Approximate: []string{"c"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pickColumns = %+v, want %+v", got, want)
	}
	if _, err := pickColumns(res, []string{"d"}); err == nil {
		t.Error("pickColumns of a missing column: no error")
	}
}
