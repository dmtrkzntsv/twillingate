package reporting

import (
	"strings"
	"unicode/utf8"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// minNameRunes is the fewest characters a dashboard title or a group
// name may have, after trimming (spec 2026-10-04 D5, D11).
const minNameRunes = 2

// checkName trims s and refuses it with ErrInvalid when it has fewer
// than minNameRunes characters; what names the field in the message.
func checkName(what, s string) (string, error) {
	t := strings.TrimSpace(s)
	if utf8.RuneCountInString(t) < minNameRunes {
		return "", store.Refuse(store.ErrInvalid, "%s must have at least %d characters", what, minNameRunes)
	}
	return t, nil
}
