package reporting

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func TestParseManifestLoadsComponents(t *testing.T) {
	b, err := os.ReadFile("testdata/components.json")
	if err != nil {
		t.Fatal(err)
	}
	cs, err := ParseManifest(b)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if len(cs) != 5 {
		t.Fatalf("len(cs) = %d, want 5", len(cs))
	}
	want := map[string]bool{"stat": true, "line": true, "table": true, "markdown": true, "pie": true}
	for _, c := range cs {
		if !want[c.Name] {
			t.Errorf("unexpected component %s", c.Name)
		}
		delete(want, c.Name)
	}
	if len(want) != 0 {
		t.Errorf("missing components: %v", want)
	}
}

func TestParseManifestRefusesUnregisteredAcceptsType(t *testing.T) {
	entry := `{"name":"gauge","description":"d","accepts":["image"],
		"inputs":{"open":false,"columns":[]},"props":{"type":"object","additionalProperties":false},
		"default_width":3,"default_height":3}`
	_, err := ParseManifest([]byte(`{"components":[` + entry + `]}`))
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "image") {
		t.Errorf("err = %v, want it to name image", err)
	}
}

func TestParseManifestRefusesOversizedDefault(t *testing.T) {
	entry := `{"name":"gauge","description":"d","accepts":["sql"],
		"inputs":{"open":false,"columns":[]},"props":{"type":"object","additionalProperties":false},
		"default_width":13,"default_height":3}`
	_, err := ParseManifest([]byte(`{"components":[` + entry + `]}`))
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

func TestParseManifestRefusesPropsThatIsNotASchema(t *testing.T) {
	entry := `{"name":"gauge","description":"d","accepts":["sql"],
		"inputs":{"open":false,"columns":[]},"props":"not a schema",
		"default_width":3,"default_height":3}`
	_, err := ParseManifest([]byte(`{"components":[` + entry + `]}`))
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

func TestParseManifestRefusesDuplicateNames(t *testing.T) {
	entry := `{"name":"gauge","description":"d","accepts":["sql"],
		"inputs":{"open":false,"columns":[]},"props":{"type":"object","additionalProperties":false},
		"default_width":3,"default_height":3}`
	_, err := ParseManifest([]byte(`{"components":[` + entry + `,` + entry + `]}`))
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "gauge") {
		t.Errorf("err = %v, want it to name gauge", err)
	}
}

func TestParseManifestRefusesUnknownColumnType(t *testing.T) {
	entry := `{"name":"gauge","description":"d","accepts":["sql"],
		"inputs":{"open":false,"columns":[{"name":"x","types":["currency"]}]},
		"props":{"type":"object","additionalProperties":false},
		"default_width":3,"default_height":3}`
	_, err := ParseManifest([]byte(`{"components":[` + entry + `]}`))
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "currency") {
		t.Errorf("err = %v, want it to name currency", err)
	}
}

func TestCheckProps(t *testing.T) {
	stat := testComponents(t)["stat"]

	if err := stat.checkProps([]byte(`{"format":"percent"}`)); err != nil {
		t.Errorf("checkProps(format=percent) = %v, want nil", err)
	}

	err := stat.checkProps([]byte(`{"format":"bogus"}`))
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("checkProps(format=bogus) = %v, want ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "format") {
		t.Errorf("err = %v, want it to mention format", err)
	}

	err = stat.checkProps([]byte(`{"nope":"x"}`))
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("checkProps(unknown prop) = %v, want ErrInvalid", err)
	}
}

func TestCheckPropsRefusesNullAndNonObject(t *testing.T) {
	stat := testComponents(t)["stat"]
	for _, props := range []string{`null`, `"percent"`, `42`, `[1,2]`, `true`} {
		err := stat.checkProps([]byte(props))
		if !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("checkProps(%s) = %v, want ErrInvalid", props, err)
		}
		if !strings.Contains(err.Error(), "props must be a JSON object") {
			t.Errorf("checkProps(%s) = %q, want it to say props must be a JSON object", props, err.Error())
		}
	}
}

func TestCheckColumns(t *testing.T) {
	comps := testComponents(t)
	line := comps["line"]

	if err := line.checkColumns([]string{"x", "y"}); err != nil {
		t.Errorf("checkColumns(x,y) = %v, want nil", err)
	}
	if err := line.checkColumns([]string{"x", "y", "series"}); err != nil {
		t.Errorf("checkColumns(x,y,series) = %v, want nil", err)
	}

	err := line.checkColumns([]string{"x", "visitor"})
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("checkColumns(x,visitor) = %v, want ErrInvalid", err)
	}
	want := "line needs y (number); columns are x, visitor"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}

	err = line.checkColumns([]string{"x", "y", "extra"})
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("checkColumns(x,y,extra) = %v, want ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "extra is not an input of line") {
		t.Errorf("err = %q, want it to say extra is not an input of line", err.Error())
	}

	table := comps["table"]
	if err := table.checkColumns([]string{"whatever", "you", "want"}); err != nil {
		t.Errorf("table.checkColumns = %v, want nil (open)", err)
	}
}

func TestCheckRows(t *testing.T) {
	comps := testComponents(t)
	line := comps["line"]

	for _, tt := range []struct {
		value   string
		wantErr bool
	}{
		{"40", false},
		{"4.5", false},
		{"", false},
		{"abc", true},
	} {
		res := readsql.Result{Columns: []string{"x", "y"}, Rows: [][]string{{"2026-08-27", tt.value}}}
		err := line.checkRows(res)
		if (err != nil) != tt.wantErr {
			t.Errorf("checkRows(y=%q) = %v, wantErr %v", tt.value, err, tt.wantErr)
		}
		if err != nil && !errors.Is(err, store.ErrInvalid) {
			t.Errorf("checkRows(y=%q) = %v, want ErrInvalid", tt.value, err)
		}
	}

	stat := comps["stat"]
	for _, tt := range []struct {
		value   string
		wantErr bool
	}{
		{"2026-08-27", false},
		{"yesterday", true},
	} {
		res := readsql.Result{Columns: []string{"value", "x"}, Rows: [][]string{{"1", tt.value}}}
		err := stat.checkRows(res)
		if (err != nil) != tt.wantErr {
			t.Errorf("checkRows(x=%q) = %v, wantErr %v", tt.value, err, tt.wantErr)
		}
	}
}

// TestCheckRowsRefusesNonFiniteNumbers pins the fix for the finding that
// strconv.ParseFloat itself accepts "nan"/"inf"/"infinity" (any case,
// optionally signed) as valid float text — technically parseable, but
// not a value a number column's reader (a chart, a stat tile) can do
// anything with.
func TestCheckRowsRefusesNonFiniteNumbers(t *testing.T) {
	stat := testComponents(t)["stat"]
	for _, v := range []string{"nan", "NaN", "inf", "-inf", "+Inf", "infinity", "Infinity"} {
		res := readsql.Result{Columns: []string{"value"}, Rows: [][]string{{v}}}
		err := stat.checkRows(res)
		if !errors.Is(err, store.ErrInvalid) {
			t.Errorf("checkRows(value=%q) = %v, want ErrInvalid", v, err)
		}
	}
}

// TestComponentsMemoisesByRow: a second read reuses each row's resolved
// schema, and a row a release rewrites is parsed afresh, never served
// from the memo.
func TestComponentsMemoisesByRow(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	first, err := svc.Components(ctx)
	if err != nil {
		t.Fatal(err)
	}
	again, err := svc.Components(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := range first {
		if first[i].schema != again[i].schema {
			t.Errorf("%s: schema resolved again, want the memoised one", first[i].Name)
		}
	}

	rows := make([]store.Component, len(first))
	for i, c := range first {
		rows[i] = c.row()
	}
	rows[0].Description = "rewritten by a release"
	if err := svc.st.SyncReporting(ctx, store.ReportingSync{Hash: "next", Version: "next", Components: rows}); err != nil {
		t.Fatal(err)
	}
	after, err := svc.Components(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after[0].Name != rows[0].Name || after[0].Description != "rewritten by a release" {
		t.Errorf("after a rewrite: %s %q, want %s with the new description", after[0].Name, after[0].Description, rows[0].Name)
	}
}
