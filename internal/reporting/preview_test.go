package reporting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/reporting/sqlbind"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// previewService pins today to 2026-09-14 so ranges are deterministic.
func previewService(t *testing.T, maxRows int) (*Service, store.Store) {
	t.Helper()
	now := func() time.Time { return time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC) }
	return newTestServiceOpts(t, Options{Now: now}, maxRows)
}

func sqlPreview(component, content string) PreviewIn {
	return PreviewIn{Component: component, Source: Source{Type: "sql", Content: content}}
}

func TestPreviewBindsTheGivenValues(t *testing.T) {
	svc, _ := previewService(t, 1000)
	seedViews(t, svc, 1, "2026-09-01", "2026-09-02", "2026-09-10")
	seedViews(t, svc, 2, "2026-09-02")
	in := sqlPreview("line", "SELECT day AS x, count(*) AS y FROM views WHERE project_id = :project AND day BETWEEN :from AND :to GROUP BY day ORDER BY day")
	in.ProjectID, in.From, in.To = 1, "2026-09-01", "2026-09-02"
	got, err := svc.Preview(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Problem != "" || len(got.Rows) != 2 || got.Rows[0][0] != "2026-09-01" || got.Rows[1][1] != "2" || got.Columns[1] != "y" {
		t.Errorf("preview = %+v", got)
	}
}

func TestPreviewReturnsRowsWithAProblem(t *testing.T) {
	svc, _ := previewService(t, 1000)
	ctx := context.Background()
	for name, tc := range map[string]struct {
		in   PreviewIn
		want string
	}{
		"missing column": {sqlPreview("line", "SELECT 'a' AS x"), "line needs y (number); columns are x"},
		"extra column":   {sqlPreview("pie", "SELECT 'a' AS label, 1 AS value, 2 AS z"), "z is not an input of pie"},
		"row type":       {sqlPreview("stat", "SELECT 'many' AS value"), `stat.value: "many" is not number`},
		"props": {PreviewIn{Component: "stat", Props: json.RawMessage(`{"format":"weird"}`),
			Source: Source{Type: "sql", Content: "SELECT 1 AS value"}}, ""}, // message from jsonschema; checked by prefix below
	} {
		t.Run(name, func(t *testing.T) {
			got, err := svc.Preview(ctx, tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Rows) != 1 {
				t.Errorf("rows = %v, want the one row back", got.Rows)
			}
			if tc.want != "" && got.Problem != tc.want {
				t.Errorf("problem = %q, want %q", got.Problem, tc.want)
			}
			if tc.want == "" && (len(got.Problem) < 6 || got.Problem[:6] != "stat: ") {
				t.Errorf("problem = %q, want a stat props refusal", got.Problem)
			}
		})
	}
}

func TestPreviewNoRowsIsNoProblem(t *testing.T) {
	svc, _ := previewService(t, 1000)
	got, err := svc.Preview(context.Background(), sqlPreview("line", "SELECT day AS x, count(*) AS y FROM views WHERE false GROUP BY day"))
	if err != nil || got.Problem != "" || len(got.Rows) != 0 || len(got.Columns) != 2 {
		t.Errorf("preview = %+v, %v", got, err)
	}
}

func TestPreviewTruncates(t *testing.T) {
	svc, _ := previewService(t, 2)
	got, err := svc.Preview(context.Background(), sqlPreview("table",
		"SELECT i FROM range(1, 6) t(i)"))
	if err != nil || !got.Truncated || len(got.Rows) != 2 {
		t.Errorf("preview = %+v, %v", got, err)
	}
}

func TestPreviewRefusals(t *testing.T) {
	svc, _ := previewService(t, 1000)
	ctx := context.Background()
	withRange := func(in PreviewIn, from, to string) PreviewIn { in.From, in.To = from, to; return in }
	for name, tc := range map[string]struct {
		in   PreviewIn
		want string
	}{
		"unknown component": {sqlPreview("nope", "SELECT 1"), "component nope does not exist; list_components names the ones there are"},
		"md source":         {PreviewIn{Component: "markdown", Source: Source{Type: "md", Content: "# hi"}}, "preview runs sql and query only; markdown is drawn as it is typed"},
		"md-only component": {sqlPreview("markdown", "SELECT 1"), "markdown does not accept sql; it accepts md"},
		"param":             {sqlPreview("table", "SELECT :other AS x"), "sql uses :other; widgets get only :project, :from and :to"},
		"no project":        {sqlPreview("table", "SELECT :project AS p"), "the sql uses :project; pass project_id"},
		"no range":          {sqlPreview("table", "SELECT :from AS f"), "the sql uses :from or :to; pass from and to"},
		"bad range":         {withRange(sqlPreview("table", "SELECT :from AS f"), "2026-09-10", "2026-09-01"), ""},
		"duckdb error":      {sqlPreview("table", "SELECT nope FROM nowhere"), ""},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.Preview(ctx, tc.in)
			if !errors.Is(err, store.ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
			if tc.want != "" && err.Error() != tc.want {
				t.Errorf("err = %q, want %q", err, tc.want)
			}
		})
	}
}

func TestPreviewClampsAFutureTo(t *testing.T) {
	svc, _ := previewService(t, 1000)
	in := sqlPreview("table", "SELECT :to AS t")
	in.From, in.To = "2026-09-10", "2026-09-20"
	got, err := svc.Preview(context.Background(), in)
	if err != nil || got.Rows[0][0] != "2026-09-14" {
		t.Errorf("preview = %+v, %v; want to clamped to today", got, err)
	}
}

// TestPreviewWithDashboardSelectors: with dashboard_id, a draft may read
// that dashboard's selectors, bound to the values given (the empty list
// for All); without it, only :project, :from and :to, as before.
func TestPreviewWithDashboardSelectors(t *testing.T) {
	svc := selectorService(t, &captureNative{})
	fake := filesOf(t, svc).sqle
	d := mustCreateWith(t, svc, "DD", []Selector{deploymentSel})
	in := sqlPreview("stat", readsDeployment("x").Source.Content)
	_, err := svc.Preview(context.Background(), in)
	wantRefusal(t, err, store.ErrInvalid, "sql uses :deployment; widgets get only :project, :from and :to")
	in.Selectors = map[string][]string{"deployment": {"cloud"}}
	_, err = svc.Preview(context.Background(), in)
	wantRefusal(t, err, store.ErrInvalid, "selectors: pass the dashboard_id whose selectors they are")

	in.DashboardID = d.ID
	for _, c := range []struct {
		sel  map[string][]string
		want string
	}{{map[string][]string{"deployment": {"cloud"}}, sqlbind.ListText([]string{"cloud"})}, {nil, sqlbind.ListText(nil)}} {
		in.Selectors = c.sel
		got, err := svc.Preview(context.Background(), in)
		if err != nil {
			t.Fatalf("%v: %v", c.sel, err)
		}
		if got.Problem != "" {
			t.Errorf("%v: problem %q", c.sel, got.Problem)
		}
		reqs := fake.Requests()
		if last := reqs[len(reqs)-1]; last.Args["tw_deployment"] != c.want {
			t.Errorf("%v: bound %v, want %q", c.sel, last.Args, c.want)
		}
	}
	in.Selectors = map[string][]string{"campaign": {"a"}}
	_, err = svc.Preview(context.Background(), in)
	wantRefusal(t, err, store.ErrInvalid, fmt.Sprintf("dashboard %d has no selector campaign", d.ID))
}
