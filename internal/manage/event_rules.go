package manage

import (
	"context"
	"fmt"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// ruleFamily is the family a rule may name (event rules D2).
func ruleFamily(f string) (store.Family, error) {
	switch store.Family(f) {
	case store.FamilyProduct, store.FamilyMeasures:
		return store.Family(f), nil
	}
	return "", fmt.Errorf("family %q: rules apply to product and measures events: %w", f, ErrInvalid)
}

// ruleName refuses a name no rule may carry: empty, or reserved ($
// belongs to the server).
func ruleName(n string) error {
	if strings.TrimSpace(n) == "" {
		return fmt.Errorf("event name is required: %w", ErrInvalid)
	}
	if strings.HasPrefix(n, "$") {
		return fmt.Errorf("event %q: $ names are reserved: %w", n, ErrInvalid)
	}
	return nil
}

func ruleSubject(id int64, f store.Family, name string) string {
	return fmt.Sprintf("%d/%s/%s", id, f, name)
}

// ruleTarget validates what every rule operation names: a known project,
// a family rules apply to, and an event name.
func (o *Ops) ruleTarget(ctx context.Context, projectID int64, family, name string) (store.Family, error) {
	if o.Reg.Snapshot(ctx).Project(projectID) == nil {
		return "", fmt.Errorf("project %d: %w", projectID, ErrNotFound)
	}
	f, err := ruleFamily(family)
	if err != nil {
		return "", err
	}
	return f, ruleName(name)
}

// ListEventRules is the project's rules ordered by family, then name.
func (o *Ops) ListEventRules(ctx context.Context, projectID int64) ([]store.EventRule, error) {
	if o.Reg.Snapshot(ctx).Project(projectID) == nil {
		return nil, fmt.Errorf("project %d: %w", projectID, ErrNotFound)
	}
	all, err := o.St.LoadEventRules(ctx)
	if err != nil {
		return nil, err
	}
	out := []store.EventRule{}
	for _, r := range all {
		if r.ProjectID == projectID {
			out = append(out, r)
		}
	}
	return out, nil
}

// RenameEvent gives an event of one family a name not in use yet and keeps
// a rule, which the event files apply on read, history included. It
// returns the rule. A name that has events in the files, rules applied
// (merge it instead), or is blocked is ErrConflict.
func (o *Ops) RenameEvent(ctx context.Context, actor string, projectID int64, family, from, to string) (store.EventRule, error) {
	return o.moveEvent(ctx, actor, "event.rename", projectID, family, from, to, false)
}

// MergeEvent folds an event of one family into another: the same rule as
// RenameEvent, onto a name that may already have events. A merge cannot
// be split again. A blocked target is ErrConflict: a merge never drops
// what it moves.
func (o *Ops) MergeEvent(ctx context.Context, actor string, projectID int64, family, from, to string) (store.EventRule, error) {
	return o.moveEvent(ctx, actor, "event.merge", projectID, family, from, to, true)
}

func (o *Ops) moveEvent(ctx context.Context, actor, action string, projectID int64, family, from, to string, merge bool) (store.EventRule, error) {
	f, err := o.ruleTarget(ctx, projectID, family, from)
	if err != nil {
		return store.EventRule{}, err
	}
	if err := ruleName(to); err != nil {
		return store.EventRule{}, err
	}
	if from == to {
		return store.EventRule{}, fmt.Errorf("event %q: moving it onto itself: %w", from, ErrInvalid)
	}
	if !merge && o.Presence != nil {
		has, err := o.Presence.HasEvents(ctx, projectID, f, to)
		if err != nil {
			return store.EventRule{}, fmt.Errorf("rename %s: look for %s in the event files: %w", from, to, err)
		}
		if has {
			return store.EventRule{}, fmt.Errorf("rename %s to %s: %s already has events; merge %s into it instead: %w",
				from, to, to, from, ErrConflict)
		}
	}
	if err := o.St.RenameEvent(ctx, projectID, f, from, to, store.AuditEntry{
		Actor: actor, Action: action, Subject: ruleSubject(projectID, f, from), Detail: "to " + to}); err != nil {
		return store.EventRule{}, err
	}
	o.afterWrite(ctx, false, projectID)
	return o.writtenRule(ctx, store.EventRule{ProjectID: projectID, Family: f, Name: from,
		Action: store.EventRuleRename, To: to}), nil
}

// BlockEvent drops an event name: new events of it are not stored, and
// the event files read none of it, history included.
func (o *Ops) BlockEvent(ctx context.Context, actor string, projectID int64, family, name string) (store.EventRule, error) {
	f, err := o.ruleTarget(ctx, projectID, family, name)
	if err != nil {
		return store.EventRule{}, err
	}
	if err := o.St.BlockEvent(ctx, projectID, f, name, store.AuditEntry{
		Actor: actor, Action: "event.block", Subject: ruleSubject(projectID, f, name)}); err != nil {
		return store.EventRule{}, err
	}
	o.afterWrite(ctx, false, projectID)
	return o.writtenRule(ctx, store.EventRule{ProjectID: projectID, Family: f, Name: name,
		Action: store.EventRuleBlock}), nil
}

// RemoveEventRule lifts a rename or block. The event files read what was
// stored before the rule as it was sent again; what ingest stored under
// the new name while the rule held stays there, and what a block dropped
// stays dropped.
func (o *Ops) RemoveEventRule(ctx context.Context, actor string, projectID int64, family, name string) error {
	f, err := o.ruleTarget(ctx, projectID, family, name)
	if err != nil {
		return err
	}
	if err := o.St.RemoveEventRule(ctx, projectID, f, name, store.AuditEntry{
		Actor: actor, Action: "event.rule_remove", Subject: ruleSubject(projectID, f, name)}); err != nil {
		return err
	}
	o.afterWrite(ctx, false, projectID)
	return nil
}

// writtenRule is r with the CreatedAt the store gave it; if the read-back
// fails the rule is returned without it, the write having committed.
func (o *Ops) writtenRule(ctx context.Context, r store.EventRule) store.EventRule {
	all, err := o.St.LoadEventRules(ctx)
	if err != nil {
		return r
	}
	for _, got := range all {
		if got.ProjectID == r.ProjectID && got.Family == r.Family && got.Name == r.Name {
			r.CreatedAt = got.CreatedAt
			break
		}
	}
	return r
}
