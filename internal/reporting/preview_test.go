package reporting

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

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
	svc, st := previewService(t, 1000)
	seedViewsDaily(t, st, 1, "2026-09-01", "2026-09-02", "2026-09-10")
	seedViewsDaily(t, st, 2, "2026-09-02")
	in := sqlPreview("line", "SELECT day AS x, views AS y FROM agg_views_daily WHERE project_id=:project AND day BETWEEN :from AND :to ORDER BY day")
	in.ProjectID, in.From, in.To = 1, "2026-09-01", "2026-09-02"
	got, err := svc.Preview(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Problem != "" || len(got.Rows) != 2 || got.Rows[0][0] != "2026-09-01" || got.Columns[1] != "y" {
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
	got, err := svc.Preview(context.Background(), sqlPreview("line", "SELECT day AS x, views AS y FROM agg_views_daily WHERE 0"))
	if err != nil || got.Problem != "" || len(got.Rows) != 0 || len(got.Columns) != 2 {
		t.Errorf("preview = %+v, %v", got, err)
	}
}

func TestPreviewTruncates(t *testing.T) {
	svc, _ := previewService(t, 2)
	got, err := svc.Preview(context.Background(), sqlPreview("table",
		"WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<5) SELECT i FROM n"))
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
		"md source":         {PreviewIn{Component: "markdown", Source: Source{Type: "md", Content: "# hi"}}, "preview runs sql only; markdown is drawn as it is typed"},
		"md-only component": {sqlPreview("markdown", "SELECT 1"), "markdown does not accept sql; it accepts md"},
		"param":             {sqlPreview("table", "SELECT :other AS x"), "sql uses :other; widgets get only :project, :from and :to"},
		"no project":        {sqlPreview("table", "SELECT :project AS p"), "the sql uses :project; pass project_id"},
		"no range":          {sqlPreview("table", "SELECT :from AS f"), "the sql uses :from or :to; pass from and to"},
		"bad range":         {withRange(sqlPreview("table", "SELECT :from AS f"), "2026-09-10", "2026-09-01"), ""},
		"sqlite error":      {sqlPreview("table", "SELECT nope FROM nowhere"), ""},
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
