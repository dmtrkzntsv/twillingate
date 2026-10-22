package native

import (
	"os"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/archive/archivetest"
)

func TestMain(m *testing.M) {
	if archivetest.Child() {
		return
	}
	os.Exit(m.Run())
}
