package reporting

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func TestSQLFollows(t *testing.T) {
	s := &sqlSource{}
	for _, tt := range []struct {
		content     string
		wantProject bool
		wantRng     bool
	}{
		{"select :project as p", true, false},
		{"select :from as f", false, true},
		{"select 1", false, false},
	} {
		project, rng := s.Follows(tt.content)
		if project != tt.wantProject || rng != tt.wantRng {
			t.Errorf("Follows(%q) = (%v,%v), want (%v,%v)", tt.content, project, rng, tt.wantProject, tt.wantRng)
		}
	}
}

func TestSQLValidateRefusesUnsupportedParams(t *testing.T) {
	s := &sqlSource{} // no db: refused before it would ever be reached
	comps := testComponents(t)
	line := comps["line"]

	for _, content := range []string{
		"select :path as x, 1 as y",
		"select ? as x, 1 as y",
	} {
		err := s.Validate(context.Background(), content, line)
		if !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("Validate(%q) = %v, want ErrInvalid", content, err)
		}
		if !strings.Contains(err.Error(), "widgets get only :project, :from and :to") {
			t.Errorf("Validate(%q) = %q, want the :path/? refusal message", content, err.Error())
		}
	}
}

func TestSQLValidateRefusesReadOfMeta(t *testing.T) {
	s := &sqlSource{}
	comps := testComponents(t)
	err := s.Validate(context.Background(), "select * from meta", comps["table"])
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

func TestSQLValidateRefusesSyntaxError(t *testing.T) {
	db := newTestReadDB(t)
	s := &sqlSource{db: db}
	comps := testComponents(t)
	err := s.Validate(context.Background(), "select from", comps["table"])
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

func TestSQLValidateChecksColumnsViaLimitZero(t *testing.T) {
	db := newTestReadDB(t)
	s := &sqlSource{db: db}
	comps := testComponents(t)
	line := comps["line"]

	if err := s.Validate(context.Background(), "select 1 as x, 2 as y", line); err != nil {
		t.Errorf("Validate(x,y) = %v, want nil", err)
	}

	err := s.Validate(context.Background(), "select 1 as x, 2 as visitor", line)
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("Validate(x,visitor) = %v, want ErrInvalid", err)
	}
}

func TestSQLValidateSamplesRowsAndChecksTypes(t *testing.T) {
	db := newTestReadDB(t)
	s := &sqlSource{db: db, sampleRows: true}
	comps := testComponents(t)
	stat := comps["stat"]

	if err := s.Validate(context.Background(), "select 3.5 as value", stat); err != nil {
		t.Errorf("Validate(value=3.5) = %v, want nil", err)
	}

	err := s.Validate(context.Background(), "select 'oops' as value", stat)
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("Validate(value='oops') = %v, want ErrInvalid", err)
	}
}

func TestMDValidate(t *testing.T) {
	m := mdSource{}
	comps := testComponents(t)
	markdown := comps["markdown"]

	for _, content := range []string{"", "   \n\t"} {
		err := m.Validate(context.Background(), content, markdown)
		if !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("Validate(%q) = %v, want ErrInvalid", content, err)
		}
		if !strings.Contains(err.Error(), "markdown text is empty") {
			t.Errorf("err = %q, want it to say markdown text is empty", err.Error())
		}
	}
	if err := m.Validate(context.Background(), "# hi", markdown); err != nil {
		t.Errorf("Validate(non-empty) = %v, want nil", err)
	}
}

func TestMDLoadAndCacheable(t *testing.T) {
	m := mdSource{}
	v, err := m.Load(context.Background(), "# hi", Params{})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := v.(Markdown)
	if !ok || got.Markdown != "# hi" {
		t.Errorf("Load = %#v, want Markdown{Markdown: \"# hi\"}", v)
	}
	if m.Cacheable() {
		t.Error("md Cacheable() = true, want false")
	}
	s := &sqlSource{}
	if !s.Cacheable() {
		t.Error("sql Cacheable() = false, want true")
	}
}

func TestSQLLoadBindsOnlyUsedParams(t *testing.T) {
	db := newTestReadDB(t)
	s := &sqlSource{db: db}

	v, err := s.Load(context.Background(), "select :project as p", Params{ProjectID: 7})
	if err != nil {
		t.Fatal(err)
	}
	res, ok := v.(readsql.Result)
	if !ok {
		t.Fatalf("Load returned %T, want readsql.Result", v)
	}
	if len(res.Rows) != 1 || res.Rows[0][0] != "7" {
		t.Errorf("rows = %v, want [[7]]", res.Rows)
	}

	// content that never mentions :from or :to must still load: bindArgs
	// binds only the params content actually uses, so an unused :from/:to
	// in Params is never an error.
	v, err = s.Load(context.Background(), "select :project as p", Params{ProjectID: 3, From: "2026-01-01", To: "2026-01-02"})
	if err != nil {
		t.Fatal(err)
	}
	res, ok = v.(readsql.Result)
	if !ok || len(res.Rows) != 1 || res.Rows[0][0] != "3" {
		t.Errorf("rows = %v, want [[3]]", res.Rows)
	}
}
