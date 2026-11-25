// Package alerts holds metric alerts: their definitions and templates,
// the audited operations on them, their evaluation over complete days,
// the backtest, and the loop that runs them. It reaches the project
// registry, the event files and the store through the interfaces here,
// which internal/app implements.
package alerts

import (
	"context"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// MaxLive is how many live (not archived) alerts a project holds.
const MaxLive = 100

// Projects is what alerts read of the project registry.
type Projects interface {
	// Project names a project, and says whether it is archived; ok is
	// false when there is none.
	Project(ctx context.Context, id int64) (name string, archived, ok bool)
	// ResolveEvent is manage.Snapshot.ResolveEvent: the name the event is
	// stored under, and whether it is blocked.
	ResolveEvent(ctx context.Context, projectID int64, family store.Family, name string) (to string, blocked bool)
	// Rules are the event rules a query applies.
	Rules(ctx context.Context) archive.Rules
}

// Presence says whether a project ever sent an event or an attribute
// (manage.Presence's method set). Family "" is every family.
type Presence interface {
	HasEvents(ctx context.Context, projectID int64, family store.Family, name string) (bool, error)
	HasAttribute(ctx context.Context, projectID int64, key string) (bool, error)
}

// Engine answers structured queries over the event files
// (native.Engine).
type Engine interface {
	AnswerAll(ctx context.Context, reqs []native.Request) []native.Answered
}
