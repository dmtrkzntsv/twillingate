package sortkey

import (
	"strings"
	"testing"
)

func TestBetweenVectors(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{"", "", "a0"},
		{"", "a0", "Zz"},
		{"", "Zz", "Zy"},
		{"a0", "", "a1"},
		{"a1", "", "a2"},
		{"a0", "a1", "a0V"},
		{"a1", "a2", "a1V"},
		{"a0V", "a1", "a0l"},
		{"Zz", "a0", "ZzV"},
		{"Zz", "a1", "a0"},
		{"", "Y00", "Xzzz"},
		{"bzz", "", "c000"},
		{"a0", "a0V", "a0G"},
		{"a0", "a0G", "a08"},
		{"b125", "b129", "b127"},
		{"a0", "a1V", "a1"},
		{"Zz", "a01", "a0"},
		{"", "a0V", "a0"},
		{"", "b999", "b99"},
		{"", "A000000000000000000000000001", "A000000000000000000000000000V"},
		{"zzzzzzzzzzzzzzzzzzzzzzzzzzy", "", "zzzzzzzzzzzzzzzzzzzzzzzzzzz"},
		{"zzzzzzzzzzzzzzzzzzzzzzzzzzz", "", "zzzzzzzzzzzzzzzzzzzzzzzzzzzV"},
	}
	for _, c := range cases {
		got, err := Between(c.a, c.b)
		if err != nil || got != c.want {
			t.Errorf("Between(%q, %q) = %q, %v; want %q", c.a, c.b, got, err, c.want)
		}
	}
}

func TestBetweenRefuses(t *testing.T) {
	for _, c := range [][2]string{
		{"", "A00000000000000000000000000"}, // the smallest integer is not a key
		{"a00", ""}, {"a00", "a1"},          // trailing zero
		{"0", "1"},                 // invalid head
		{"a1", "a0"}, {"a1", "a1"}, // not before
		{"a!", ""}, // not a base-62 digit
	} {
		if got, err := Between(c[0], c[1]); err == nil {
			t.Errorf("Between(%q, %q) = %q; want an error", c[0], c[1], got)
		}
	}
}

// Inserting at one spot many times keeps keys ordered and short.
func TestRepeatedInsertStaysOrderedAndShort(t *testing.T) {
	lo, hi := "a0", "a1"
	for i := 0; i < 500; i++ {
		k, err := Between(lo, hi)
		if err != nil || !(lo < k && k < hi) {
			t.Fatalf("step %d: Between(%q, %q) = %q, %v", i, lo, hi, k, err)
		}
		if i%2 == 0 {
			lo = k
		} else {
			hi = k
		}
	}
	if len(lo) > 100 {
		t.Errorf("key grew to %d chars", len(lo))
	}
	// Appending at the end is the common case and must stay short.
	k := ""
	for i := 0; i < 1000; i++ {
		next, err := Between(k, "")
		if err != nil || next <= k {
			t.Fatalf("append %d: %q after %q: %v", i, next, k, err)
		}
		k = next
	}
	if len(k) > 4 {
		t.Errorf("1000 appends gave %q", k)
	}
}

func TestSpread(t *testing.T) {
	for _, c := range []struct{ a, b string }{{"", ""}, {"a0", ""}, {"", "a0"}, {"a0", "a1"}} {
		keys, err := Spread(c.a, c.b, 7)
		if err != nil || len(keys) != 7 {
			t.Fatalf("Spread(%q, %q, 7) = %q, %v", c.a, c.b, keys, err)
		}
		prev := c.a
		for _, k := range keys {
			if k <= prev || (c.b != "" && k >= c.b) {
				t.Errorf("Spread(%q, %q): %q out of order in %s", c.a, c.b, k, strings.Join(keys, ","))
			}
			prev = k
		}
	}
	if keys, _ := Spread("", "", 0); len(keys) != 0 {
		t.Errorf("Spread n=0 = %q", keys)
	}
}
