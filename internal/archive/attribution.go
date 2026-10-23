package archive

import (
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Lookup asks where one form submission came from (native's Attribute,
// the jobs' attributor).
type Lookup struct {
	ProjectID          int64
	ID                 string    // the submission's id
	ActorKind, ActorID string    // its actor
	InstallID          string    // the $install_id it was sent with, "" if none
	ConnectionID       string    // its request's connection hash, "" if none
	At                 time.Time // its arrival
}

// Found is what the files say of one Lookup: Source holds the referrer
// and UTMs of the newest session with any (zero: direct), Newest the
// newest view's kind, group, session id, environment and consent (zero:
// no view).
type Found struct {
	ProjectID int64
	ID        string
	Source    store.Event
	Newest    store.Event
}
