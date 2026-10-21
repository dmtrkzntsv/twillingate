package enginesql

import "testing"

// TestAttrColumnMatchesLikeTheFiles: a key finds its attr_<key> column as
// the files name it, without regard to case, and comes back quoted; a
// NUL in the key reads as the six characters \u0000, and a '"' doubles.
func TestAttrColumnMatchesLikeTheFiles(t *testing.T) {
	cols := []string{"day", "attr_plan", `attr_a\u0000b`, `attr_say "hi"`, "plan"}
	for _, c := range []struct {
		key, want string
		ok        bool
	}{
		{"plan", `"attr_plan"`, true},
		{"Plan", `"attr_plan"`, true},
		{"a\x00b", `"attr_a\u0000b"`, true},
		{`say "hi"`, `"attr_say ""hi"""`, true},
		{"tier", "", false},
		{"", "", false},
	} {
		got, ok := AttrColumn(cols, c.key)
		if got != c.want || ok != c.ok {
			t.Errorf("AttrColumn(%q) = %q, %v; want %q, %v", c.key, got, ok, c.want, c.ok)
		}
	}
}
