package enginesql

import "testing"

func TestWraps(t *testing.T) {
	q := "SELECT 1 AS a -- one\n;; \t\n"
	if got, want := Trim(q), "SELECT 1 AS a -- one"; got != want {
		t.Errorf("Trim = %q, want %q", got, want)
	}
	// The newline ends a trailing line comment before the wrap's own paren.
	if got, want := Text(q), "SELECT tw_cell(COLUMNS(*)) FROM (SELECT 1 AS a -- one\n)"; got != want {
		t.Errorf("Text = %q, want %q", got, want)
	}
	if got, want := Shape(q), "SELECT * FROM (SELECT 1 AS a -- one\n) LIMIT 0"; got != want {
		t.Errorf("Shape = %q, want %q", got, want)
	}
}
