package reporting

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func TestDeriveName(t *testing.T) {
	if got := deriveName("Visitors & views", nil); got != "visitors-views" {
		t.Errorf("deriveName = %q, want visitors-views", got)
	}

	taken := map[string]bool{"visitors-views": true}
	if got := deriveName("Visitors & views", taken); got != "visitors-views-2" {
		t.Errorf("deriveName (taken) = %q, want visitors-views-2", got)
	}
	taken["visitors-views-2"] = true
	if got := deriveName("Visitors & views", taken); got != "visitors-views-3" {
		t.Errorf("deriveName (taken x2) = %q, want visitors-views-3", got)
	}

	for _, title := range []string{"Посетители", "📈", ""} {
		if got := deriveName(title, nil); got != "widget" {
			t.Errorf("deriveName(%q) = %q, want widget", title, got)
		}
	}

	long := strings.Repeat("a", 200)
	got := deriveName(long, nil)
	if len(got) > 60 {
		t.Errorf("deriveName(200 chars) len = %d, want <= 60", len(got))
	}
	if strings.HasSuffix(got, "-") {
		t.Errorf("deriveName(200 chars) = %q, want no trailing -", got)
	}

	// "Abcde " repeats every 6 folded characters ("abcde-"), so position
	// 60 of the 200-char title — where the naive [:60] cut lands — falls
	// exactly on the separator between two words: cutting there leaves a
	// trailing '-' unless the cut itself also trims it.
	separator := strings.Repeat("Abcde ", 34)[:200]
	got = deriveName(separator, nil)
	if len(got) > 60 {
		t.Errorf("deriveName(separator-aligned) len = %d, want <= 60", len(got))
	}
	if strings.HasSuffix(got, "-") {
		t.Errorf("deriveName(separator-aligned) = %q, want no trailing -", got)
	}
	want := "abcde-abcde-abcde-abcde-abcde-abcde-abcde-abcde-abcde-abcde"
	if got != want {
		t.Errorf("deriveName(separator-aligned) = %q, want %q", got, want)
	}
}

func TestCheckSize(t *testing.T) {
	for _, w := range []int{0, 13} {
		err := checkSize(w, 3)
		if !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("checkSize(width=%d) = %v, want ErrInvalid", w, err)
		}
		want := "width is columns out of 12, from 1 to 12"
		if err.Error() != want {
			t.Errorf("err = %q, want %q", err.Error(), want)
		}
	}
	for _, h := range []int{0, 13} {
		err := checkSize(3, h)
		if !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("checkSize(height=%d) = %v, want ErrInvalid", h, err)
		}
		want := "height is rows of 40px, from 1 to 12"
		if err.Error() != want {
			t.Errorf("err = %q, want %q", err.Error(), want)
		}
	}
	if err := checkSize(3, 3); err != nil {
		t.Errorf("checkSize(3,3) = %v, want nil", err)
	}
}

func TestValidateWidget(t *testing.T) {
	db := newTestReadDB(t)
	svc := New(nil, db, Options{})
	comps := testComponents(t)

	w := store.Widget{Component: "gauge", SourceType: "sql", Source: "select 1", Width: 3, Height: 3, Props: "{}"}
	err := svc.validateWidget(context.Background(), comps, w)
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("unknown component: err = %v, want ErrInvalid", err)
	}
	want := "component gauge does not exist; list_components names the ones there are"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}

	w = store.Widget{Component: "markdown", SourceType: "sql", Source: "select 1", Width: 3, Height: 3, Props: "{}"}
	err = svc.validateWidget(context.Background(), comps, w)
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("wrong source type for component: err = %v, want ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "markdown") || !strings.Contains(err.Error(), "sql") {
		t.Errorf("err = %q, want it to name both markdown and sql", err.Error())
	}

	w = store.Widget{Component: "line", SourceType: "image", Source: "x", Width: 3, Height: 3, Props: "{}"}
	err = svc.validateWidget(context.Background(), comps, w)
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("unregistered source type: err = %v, want ErrInvalid", err)
	}
	want = "source type image does not exist; there are md and sql"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}
