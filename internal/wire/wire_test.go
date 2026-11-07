package wire

import (
	"strings"
	"testing"
)

func TestValidFormName(t *testing.T) {
	for _, ok := range []string{"contact", "a", "sign-up_2", strings.Repeat("z", 64)} {
		if !ValidFormName(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "Contact", "a b", "a/b", "é", "a.b", strings.Repeat("z", 65)} {
		if ValidFormName(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}
