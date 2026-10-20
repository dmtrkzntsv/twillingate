package manage

import (
	"context"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Presence answers from the event files, rules applied.
type Presence interface {
	HasEvents(ctx context.Context, projectID int64, family store.Family, name string) (bool, error)
	HasAttribute(ctx context.Context, projectID int64, key string) (bool, error)
}
