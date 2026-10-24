package native

import (
	"slices"
	"testing"
)

// prefix "" passes every text value but NULL, the empty one included:
// "not NULL", which is_set is not (it leaves "" out too). internal/api's
// measures keeps a custom key's empty value with it, as its old SQL's
// IS NOT NULL did.
func TestPrefixEmptyIsNotNull(t *testing.T) {
	b := &Batch{N: 4, Cols: map[string]*Column{"attr_k": {Dict: []string{"", "x"}, Idx: []int32{0, 1, -1, 0}}}}
	for _, c := range []struct {
		f    Filter
		want []bool
	}{
		{Filter{Column: "attr_k", Op: "prefix", Values: []string{""}}, []bool{true, true, false, true}},
		{Filter{Column: "attr_k", Op: "is_set"}, []bool{false, true, false, false}},
	} {
		if err := c.f.validate("measures", "where[0]"); err != nil {
			t.Fatalf("%s: %v", c.f.Op, err)
		}
		fs, err := compileFilters([]Filter{c.f}, "where")
		if err != nil {
			t.Fatal(err)
		}
		keep, err := passRows(b, fs, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(keep, c.want) {
			t.Errorf("%s = %v, want %v", c.f.Op, keep, c.want)
		}
	}
}
