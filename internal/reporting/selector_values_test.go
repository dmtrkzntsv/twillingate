package reporting

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"sync"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// valuesNative answers a breakdown with each project's rows, [value,
// count], and records the queries and projects it was asked.
type valuesNative struct {
	mu       sync.Mutex
	rows     map[int64][][]string
	qs       []native.Query
	projects []int64
}

func (v *valuesNative) Answer(_ context.Context, q native.Query, p native.Params, _ archive.Rules) (native.Result, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.qs, v.projects = append(v.qs, q), append(v.projects, p.Project)
	return native.Result{Columns: q.Columns(), Rows: v.rows[p.Project]}, nil
}

func valuesIn(d DashboardDetail, projects ...int64) SelectorValuesIn {
	return SelectorValuesIn{DashboardID: d.ID, Name: "deployment", ProjectIDs: projects, From: "2026-10-01", To: "2026-10-31"}
}

func valuesSetup(t *testing.T, rows map[int64][][]string) (*Service, *valuesNative, DashboardDetail) {
	t.Helper()
	nat := &valuesNative{rows: rows}
	svc := selectorService(t, nat)
	return svc, nat, mustCreateWith(t, svc, "DD", []Selector{deploymentSel}, queryStatSpec("s", hostStat))
}

// TestSelectorValuesMergesProjects: counts add up per value across the
// projects, the busiest value first and ties by value; one breakdown of
// the selector's column on events per project.
func TestSelectorValuesMergesProjects(t *testing.T) {
	svc, nat, d := valuesSetup(t, map[int64][][]string{
		1: {{"cloud", "5"}, {"self-hosted", "2"}, {"", "9"}},
		2: {{"cloud", "1"}, {"eu", "6"}, {"b", "2"}},
	})
	got, err := svc.SelectorValues(context.Background(), valuesIn(d, 2, 1))
	if err != nil {
		t.Fatal(err)
	}
	want := []SelectorValue{{"cloud", 6}, {"eu", 6}, {"b", 2}, {"self-hosted", 2}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("values = %v, want %v", got, want)
	}
	if len(nat.qs) != 2 {
		t.Fatalf("%d queries, want one per project", len(nat.qs))
	}
	q := nat.qs[0]
	if q.Kind != "breakdown" || q.Table != "events" || !reflect.DeepEqual(q.Dimensions, []string{"attr_deployment"}) || q.Limit != 1000 || len(q.Metrics) != 1 || q.Metrics[0].Op != "count" {
		t.Errorf("query = %+v, want a count breakdown of attr_deployment on events, limit 1000", q)
	}
}

// TestSelectorValuesCap: at most 100 values come back, the busiest.
func TestSelectorValuesCap(t *testing.T) {
	var rows [][]string
	for i := range 150 {
		rows = append(rows, []string{fmt.Sprintf("v%03d", i), strconv.Itoa(1000 - i)})
	}
	svc, _, d := valuesSetup(t, map[int64][][]string{1: rows})
	got, err := svc.SelectorValues(context.Background(), valuesIn(d, 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 100 || got[0].Value != "v000" || got[99].Value != "v099" {
		t.Errorf("%d values from %v to %v, want 100 from v000 to v099", len(got), got[0], got[len(got)-1])
	}
}

// TestSelectorValuesProjectCap: more than 10 projects is refused, and
// so is none.
func TestSelectorValuesProjectCap(t *testing.T) {
	svc, nat, d := valuesSetup(t, nil)
	ids := make([]int64, 11)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	_, err := svc.SelectorValues(context.Background(), valuesIn(d, ids...))
	wantRefusal(t, err, store.ErrInvalid, "project_id: 11 projects; at most 10")
	_, err = svc.SelectorValues(context.Background(), valuesIn(d))
	wantRefusal(t, err, store.ErrInvalid, "project_id: name at least one project")
	if len(nat.qs) != 0 {
		t.Errorf("%d queries ran for a refused read", len(nat.qs))
	}
	if _, err := svc.SelectorValues(context.Background(), valuesIn(d, ids[:10]...)); err != nil {
		t.Errorf("10 projects: %v, want accepted", err)
	}
}

// TestSelectorValuesSearch: a case-insensitive contains.
func TestSelectorValuesSearch(t *testing.T) {
	svc, _, d := valuesSetup(t, map[int64][][]string{1: {{"Cloud", "3"}, {"self-hosted", "2"}, {"CLOUD-eu", "1"}, {"eu", "9"}}})
	in := valuesIn(d, 1)
	in.Search = "cLoUd"
	got, err := svc.SelectorValues(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if want := []SelectorValue{{"Cloud", 3}, {"CLOUD-eu", 1}}; !reflect.DeepEqual(got, want) {
		t.Errorf("values = %v, want %v", got, want)
	}
}

// TestSelectorValuesKeepsPicked: a picked value the range lacks is
// appended with count 0, even when the search excludes it; a picked value
// the answer has is not repeated.
func TestSelectorValuesKeepsPicked(t *testing.T) {
	svc, _, d := valuesSetup(t, map[int64][][]string{1: {{"cloud", "3"}, {"eu", "1"}}})
	in := valuesIn(d, 1)
	in.Picked = []string{"gone", "eu", "gone"}
	got, err := svc.SelectorValues(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if want := []SelectorValue{{"cloud", 3}, {"eu", 1}, {"gone", 0}}; !reflect.DeepEqual(got, want) {
		t.Errorf("values = %v, want %v", got, want)
	}
	in.Search = "cl"
	if got, _ = svc.SelectorValues(context.Background(), in); !reflect.DeepEqual(got, []SelectorValue{{"cloud", 3}, {"gone", 0}, {"eu", 0}}) {
		t.Errorf("searched values = %v", got)
	}
}

// TestSelectorValuesUnknownName: a selector the dashboard lacks, and a
// dashboard that is not there, are not found.
func TestSelectorValuesUnknownName(t *testing.T) {
	svc, _, d := valuesSetup(t, nil)
	in := valuesIn(d, 1)
	in.Name = "campaign"
	_, err := svc.SelectorValues(context.Background(), in)
	wantRefusal(t, err, store.ErrNotFound, fmt.Sprintf("dashboard %d has no selector campaign", d.ID))
	in.DashboardID = d.ID + 100
	if _, err := svc.SelectorValues(context.Background(), in); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown dashboard: %v, want not found", err)
	}
}
