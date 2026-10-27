package manage

import (
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// The outcomes an edge (MCP tool, CLI command) distinguishes when an
// operation is refused. All five are the store's own values re-exported,
// the way os.ErrNotExist is fs.ErrNotExist, so a caller checks one package:
// ErrNotFound when the id (or key label) names nothing, ErrConflict when a
// key label is already taken for that project, ErrInvalid when the spec
// failed validation before anything reached the store, ErrBusy when a
// service it needs is at capacity and the same call may succeed later,
// ErrTimeout when it ran past its time limit.
var (
	ErrNotFound = store.ErrNotFound
	ErrConflict = store.ErrConflict
	ErrInvalid  = store.ErrInvalid
	ErrBusy     = store.ErrBusy
	ErrTimeout  = store.ErrTimeout
)
