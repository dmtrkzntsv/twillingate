//go:build measure

package archive

import (
	"os"
	"strconv"
)

// A measurement binary takes dayCutRows from ARCHIVE_DAY_CUT_ROWS, to
// compare row-group layouts: 1 cuts at every day, 65536 at none.
func init() {
	if v := os.Getenv("ARCHIVE_DAY_CUT_ROWS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			panic("ARCHIVE_DAY_CUT_ROWS: " + v)
		}
		dayCutRows = n
	}
}
