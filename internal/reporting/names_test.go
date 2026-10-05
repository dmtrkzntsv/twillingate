package reporting

import (
	"errors"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func TestCheckName(t *testing.T) {
	for _, s := range []string{"", " ", "a", "é", "  x  "} {
		if _, err := checkName("title", s); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("checkName(%q) err = %v, want ErrInvalid", s, err)
		}
	}
	for s, want := range map[string]string{"ab": "ab", "éé": "éé", "  Ops  ": "Ops"} {
		got, err := checkName("title", s)
		if err != nil {
			t.Errorf("checkName(%q): %v", s, err)
		}
		if got != want {
			t.Errorf("checkName(%q) = %q, want %q", s, got, want)
		}
	}
}
