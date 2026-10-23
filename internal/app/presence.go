package app

import (
	"context"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// filePresence is manage.Presence over the event files, answered by the
// native engine: whether a rename's target already has events, or values,
// in the project's whole history, with the registry's rules as they stand
// when asked. A name another rule maps there therefore has events, and a
// rename onto it is a merge.
type filePresence struct {
	nat *native.Engine
	reg *manage.Registry
	// timeout bounds each check (CONSOLE_QUERY_TIMEOUT); 0 is none.
	timeout time.Duration
}

var _ manage.Presence = (*filePresence)(nil)

func (p *filePresence) HasEvents(ctx context.Context, projectID int64, family store.Family, name string) (bool, error) {
	ctx, rules, cancel := p.prepare(ctx)
	defer cancel()
	return p.nat.HasEvents(ctx, projectID, string(family), name, rules)
}

// HasAttribute looks for a value of key in the project's events: the
// column attr_<key>, matched without regard to case, non-NULL in any row.
func (p *filePresence) HasAttribute(ctx context.Context, projectID int64, key string) (bool, error) {
	ctx, rules, cancel := p.prepare(ctx)
	defer cancel()
	return p.nat.HasAttribute(ctx, projectID, key, rules)
}

// prepare is ctx bounded by the timeout, and the registry's rules.
func (p *filePresence) prepare(ctx context.Context) (context.Context, archive.Rules, context.CancelFunc) {
	cancel := context.CancelFunc(func() {})
	if p.timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, p.timeout)
	}
	events, attrs := p.reg.Snapshot(ctx).RuleLists()
	return ctx, archive.Rules{Events: events, Attributes: attrs}, cancel
}
