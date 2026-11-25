package app

import (
	"context"

	"github.com/dmtrkzntsv/twillingate/internal/alerts"
	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// alertProjects is alerts.Projects over the registry, read from its
// snapshot as it stands when asked.
type alertProjects struct{ reg *manage.Registry }

var (
	_ alerts.Projects = alertProjects{}
	_ alerts.Presence = (*filePresence)(nil)
)

func (p alertProjects) Project(ctx context.Context, id int64) (name string, archived, ok bool) {
	pr := p.reg.Snapshot(ctx).Project(id)
	if pr == nil {
		return "", false, false
	}
	return pr.Name, pr.Archived, true
}

func (p alertProjects) ResolveEvent(ctx context.Context, projectID int64, family store.Family, name string) (string, bool) {
	return p.reg.Snapshot(ctx).ResolveEvent(projectID, family, name)
}

func (p alertProjects) Rules(ctx context.Context) archive.Rules {
	return p.reg.Snapshot(ctx).Rules()
}
