package reporting

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// threeRows is the remote table tests' query: three rows, a text key and
// a number, in key order.
const threeRows = `SELECT 'a' AS "Key", 3 AS "N" UNION ALL SELECT 'b', 1 UNION ALL SELECT 'c', 2`

// remoteTableWidget is a "table" widget with props.mode "remote" over content.
func remoteTableWidget(name, content string) WidgetSpec {
	w := tableWidget(name, content)
	w.Props = json.RawMessage(`{"mode":"remote"}`)
	return w
}

// mustRemoteTable creates a user dashboard holding one remote table over
// content and returns the widget's id.
func mustRemoteTable(t *testing.T, svc *Service, content string) int64 {
	t.Helper()
	return mustCreate(t, svc, "D", remoteTableWidget("t", content)).Widgets[0].ID
}

// pageOf loads req and returns its rows and page block, failing the test
// on a refusal or a missing page.
func pageOf(t *testing.T, svc *Service, req DataRequest) (readsql.Result, PageInfo) {
	t.Helper()
	got, err := svc.WidgetData(context.Background(), req)
	if err != nil {
		t.Fatalf("WidgetData(%+v): %v", req, err)
	}
	res, ok := got.Data.(readsql.Result)
	if !ok {
		t.Fatalf("Data = %T, want readsql.Result", got.Data)
	}
	if got.Page == nil {
		t.Fatalf("Page = nil, want the page block of a remote table")
	}
	return res, *got.Page
}

func TestRemoteTablePagesFiltersAndSorts(t *testing.T) {
	svc := newTestService(t)
	id := mustRemoteTable(t, svc, threeRows)
	req := DataRequest{WidgetID: id, Filters: `[{"column":"N","op":">","value":"1"}]`, Sort: "N:desc", Limit: 1}

	res, page := pageOf(t, svc, req)
	if want := [][]string{{"a", "3"}}; !reflect.DeepEqual(res.Rows, want) {
		t.Errorf("rows = %v, want %v", res.Rows, want)
	}
	if !res.Truncated {
		t.Error("truncated = false, want true: a second matched row follows the page")
	}
	want := PageInfo{Offset: 0, Limit: 1, Matched: 2, Total: 3, Sort: "N:desc",
		Filters: []FilterSpec{{Column: "N", Op: ">", Value: "1"}}}
	if !reflect.DeepEqual(page, want) {
		t.Errorf("page = %+v, want %+v", page, want)
	}

	req.Offset = 1
	res, page = pageOf(t, svc, req)
	if want := [][]string{{"c", "2"}}; !reflect.DeepEqual(res.Rows, want) {
		t.Errorf("offset 1: rows = %v, want %v", res.Rows, want)
	}
	if res.Truncated {
		t.Error("offset 1: truncated = true, want false on the last page")
	}
	if page.Offset != 1 || page.Matched != 2 || page.Total != 3 {
		t.Errorf("offset 1: page = %+v", page)
	}
}

func TestRemoteTableDefaultsToTheCap(t *testing.T) {
	svc := newTestService(t)
	id := mustRemoteTable(t, svc, threeRows)

	res, page := pageOf(t, svc, DataRequest{WidgetID: id})
	if page.Limit != svc.db.MaxRows() {
		t.Errorf("page.limit = %d, want MaxRows %d", page.Limit, svc.db.MaxRows())
	}
	if len(res.Rows) != 3 || res.Truncated || page.Matched != 3 || page.Total != 3 {
		t.Errorf("rows = %v (truncated %v), page = %+v; want all three", res.Rows, res.Truncated, page)
	}
	if page.Filters == nil {
		t.Error("page.filters = nil, want [] so the JSON always carries the list")
	}
}

func TestPagingArgumentsRefusedOffRemoteTables(t *testing.T) {
	svc := newTestService(t)
	d := mustCreate(t, svc, "D",
		tableWidget("local", threeRows),
		WidgetSpec{Name: "pie", Component: "pie", Title: "pie",
			Source: Source{Type: "sql", Content: "SELECT 'a' AS label, 3 AS value"}})
	args := map[string]DataRequest{
		"filters":  {Filters: `[{"column":"N","op":"=","value":"1"}]`},
		"sort":     {Sort: "N:asc"},
		"distinct": {Distinct: "N"},
		"offset":   {Offset: 1},
		"limit":    {Limit: 5},
	}
	for _, w := range d.Widgets {
		for name, req := range args {
			req.WidgetID = w.ID
			_, err := svc.WidgetData(context.Background(), req)
			if !errors.Is(err, store.ErrInvalid) {
				t.Errorf("%s with %s: err = %v, want ErrInvalid", w.Name, name, err)
				continue
			}
			if !strings.Contains(err.Error(), "mode") {
				t.Errorf("%s with %s: %q does not name props.mode", w.Name, name, err)
			}
		}
	}
}

func TestViewRefusals(t *testing.T) {
	svc := newTestService(t)
	id := mustRemoteTable(t, svc, threeRows)
	for _, tc := range []struct {
		name string
		req  DataRequest
		msg  string // a part of the message, when it matters
	}{
		{"malformed filters", DataRequest{Filters: "not json"}, "filters"},
		{"one value to in", DataRequest{Filters: `[{"column":"N","op":"in","value":"1"}]`}, "list"},
		{"empty in", DataRequest{Filters: `[{"column":"N","op":"in","value":[]}]`}, "at least one"},
		{"a list to =", DataRequest{Filters: `[{"column":"N","op":"=","value":["1"]}]`}, "not a list"},
		{"unknown op", DataRequest{Filters: `[{"column":"N","op":"like","value":"1"}]`}, "not one of"},
		{"unknown filter column", DataRequest{Filters: `[{"column":"Zed","op":"=","value":"1"}]`}, "Key, N"},
		{"bad direction", DataRequest{Sort: "N:up"}, "<column>:asc"},
		{"no direction", DataRequest{Sort: "N"}, "<column>:asc"},
		{"unknown sort column", DataRequest{Sort: "Zed:asc"}, "Key, N"},
		{"negative offset", DataRequest{Offset: -1}, "offset"},
		{"negative limit", DataRequest{Limit: -1}, "CONSOLE_QUERY_MAX_ROWS"},
		{"limit over the cap", DataRequest{Limit: svc.db.MaxRows() + 1}, "CONSOLE_QUERY_MAX_ROWS"},
		{"distinct with sort", DataRequest{Distinct: "N", Sort: "N:asc"}, "distinct"},
		{"unknown distinct column", DataRequest{Distinct: "Zed"}, "Key, N"},
	} {
		tc.req.WidgetID = id
		_, err := svc.WidgetData(context.Background(), tc.req)
		if !errors.Is(err, store.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", tc.name, err)
			continue
		}
		if !strings.Contains(err.Error(), tc.msg) {
			t.Errorf("%s: %q does not contain %q", tc.name, err, tc.msg)
		}
		// The caller's arguments are what to change, not the widget: no
		// pointer to the release notes, even for a column only the query's
		// own run can find missing.
		if strings.Contains(err.Error(), "release notes") {
			t.Errorf("%s: %q points to the release notes, as if an update broke the widget", tc.name, err)
		}
	}
}

// TestRemoteTableBrokenQueryNamesReleaseNotes: a remote table whose query
// no longer runs is the widget's problem, not the viewer's, so it keeps
// the release notes pointer that view refusals leave out.
func TestRemoteTableBrokenQueryNamesReleaseNotes(t *testing.T) {
	svc := newTestService(t)
	rawExecOn(t, svc, "CREATE TABLE probe(n INTEGER)")
	id := mustRemoteTable(t, svc, `SELECT n AS "N" FROM probe`)
	rawExecOn(t, svc, "DROP TABLE probe")

	_, err := svc.WidgetData(context.Background(), DataRequest{WidgetID: id, Sort: "N:asc"})
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	if !strings.HasSuffix(err.Error(), "; if this started after an update, see the release notes at "+ReleasesURL) {
		t.Errorf("err = %q, want it to end with the release notes pointer", err)
	}
}

// TestRemoteTableCacheKeyPerView: each view of a remote table is its own
// cache entry, and the same view asked twice (even with its filters
// spelled differently) is one.
func TestRemoteTableCacheKeyPerView(t *testing.T) {
	now := time.Now()
	svc, _ := newTestServiceOpts(t,
		Options{CacheAge: time.Minute, RefreshAge: time.Second, Now: func() time.Time { return now }}, 1000)
	rawExecOn(t, svc, "CREATE TABLE probe(n INTEGER)")
	rawExecOn(t, svc, "INSERT INTO probe(n) VALUES (1), (2)")
	id := mustRemoteTable(t, svc, `SELECT n AS "N" FROM probe`)

	asc := DataRequest{WidgetID: id, Sort: "N:asc"}
	before, _ := pageOf(t, svc, asc)
	rawExecOn(t, svc, "INSERT INTO probe(n) VALUES (3)")

	again, _ := pageOf(t, svc, asc)
	if !reflect.DeepEqual(again.Rows, before.Rows) {
		t.Errorf("the same view twice: rows = %v, want the cached %v", again.Rows, before.Rows)
	}
	desc, page := pageOf(t, svc, DataRequest{WidgetID: id, Sort: "N:desc"})
	if want := [][]string{{"3"}, {"2"}, {"1"}}; !reflect.DeepEqual(desc.Rows, want) || page.Total != 3 {
		t.Errorf("another view: rows = %v (total %d), want a load of its own, %v", desc.Rows, page.Total, want)
	}

	// Two spellings of one filter are one view.
	spaced := DataRequest{WidgetID: id, Filters: `[ {"column": "N", "op": ">", "value": "0"} ]`}
	tight := DataRequest{WidgetID: id, Filters: `[{"column":"N","op":">","value":"0"}]`}
	first, _ := pageOf(t, svc, spaced)
	rawExecOn(t, svc, "INSERT INTO probe(n) VALUES (4)")
	second, _ := pageOf(t, svc, tight)
	if !reflect.DeepEqual(first.Rows, second.Rows) {
		t.Errorf("one filter spelled two ways: rows %v then %v, want one cache entry", first.Rows, second.Rows)
	}
}

// TestRemoteTableModeSwitchMisses: switching a cached table to remote
// leaves its source, and so the rest of its cache key, unchanged, yet the
// next read must page rather than hand back the whole cached result.
func TestRemoteTableModeSwitchMisses(t *testing.T) {
	svc, _ := newTestServiceOpts(t, Options{CacheAge: time.Minute, RefreshAge: time.Minute}, 1000)
	id := mustCreate(t, svc, "D", tableWidget("t", threeRows)).Widgets[0].ID
	ctx := context.Background()
	if got, err := svc.WidgetData(ctx, DataRequest{WidgetID: id}); err != nil || got.Page != nil {
		t.Fatalf("local: page = %+v, err = %v; want no page", got.Page, err)
	}
	if _, err := svc.UpdateWidget(ctx, "test", UpdateWidget{ID: id, Props: json.RawMessage(`{"mode":"remote"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, page := pageOf(t, svc, DataRequest{WidgetID: id}); page.Total != 3 {
		t.Errorf("remote: page = %+v, want total 3", page)
	}
}

func TestRemoteTableSortSplitsOnTheLastColon(t *testing.T) {
	svc := newTestService(t)
	id := mustRemoteTable(t, svc, `SELECT 1 AS "a:b" UNION ALL SELECT 3 UNION ALL SELECT 2`)

	res, page := pageOf(t, svc, DataRequest{WidgetID: id, Sort: "a:b:desc"})
	if want := [][]string{{"3"}, {"2"}, {"1"}}; !reflect.DeepEqual(res.Rows, want) {
		t.Errorf("rows = %v, want %v", res.Rows, want)
	}
	if page.Sort != "a:b:desc" {
		t.Errorf("page.sort = %q, want a:b:desc", page.Sort)
	}
}

func TestRemoteTableDistinct(t *testing.T) {
	svc := newTestService(t)
	id := mustRemoteTable(t, svc, threeRows+` UNION ALL SELECT 'a', 9`)

	res, page := pageOf(t, svc, DataRequest{WidgetID: id, Distinct: "Key",
		Filters: `[{"column":"Key","op":"=","value":"b"}]`})
	if want := []string{"value", "rows"}; !reflect.DeepEqual(res.Columns, want) {
		t.Errorf("columns = %v, want %v", res.Columns, want)
	}
	// The filter on Key itself is left out, so every key is offered.
	if want := [][]string{{"a", "2"}, {"b", "1"}, {"c", "1"}}; !reflect.DeepEqual(res.Rows, want) {
		t.Errorf("rows = %v, want %v", res.Rows, want)
	}
	if page.Distinct != "Key" || page.Matched != 3 || page.Total != 4 {
		t.Errorf("page = %+v, want distinct Key, 3 values of 4 rows", page)
	}
}

// TestDevWidgetDataPagesRemoteTables: reporting dev's data handler takes
// the same paging arguments from its query string as widget_data, and
// refuses a limit that is not an integer.
func TestDevWidgetDataPagesRemoteTables(t *testing.T) {
	root := devDir(t, struct{ name, component, ext, content string }{"t", "table", "sql", threeRows})
	cfg := `{"component":"table","title":"t","props":{"mode":"remote"}}`
	if err := os.WriteFile(filepath.Join(root, "ok", "t.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	h := DevHandler([]string{root}, newTestReadDB(t))
	path := "/api/widgets/" + strconv.FormatInt(firstDevDashboardID*1000+1, 10) + "/data"

	var got struct {
		Data struct {
			Rows      [][]string `json:"rows"`
			Truncated bool       `json:"truncated"`
		} `json:"data"`
		Page *PageInfo `json:"page"`
	}
	q := url.Values{"filters": {`[{"column":"N","op":">","value":"1"}]`}, "sort": {"N:desc"}, "offset": {"1"}, "limit": {"1"}}
	if res := getJSON(t, h, path+"?"+q.Encode(), &got); res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if want := [][]string{{"c", "2"}}; !reflect.DeepEqual(got.Data.Rows, want) || got.Data.Truncated {
		t.Errorf("rows = %v (truncated %v), want %v", got.Data.Rows, got.Data.Truncated, want)
	}
	if got.Page == nil || got.Page.Offset != 1 || got.Page.Limit != 1 || got.Page.Matched != 2 || got.Page.Total != 3 {
		t.Errorf("page = %+v", got.Page)
	}

	var body map[string]map[string]string
	if res := getJSON(t, h, path+"?limit=ten", &body); res.StatusCode != http.StatusBadRequest {
		t.Errorf("limit=ten: status = %d, want 400", res.StatusCode)
	}
}
