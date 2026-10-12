package manage

import (
	"context"
	"fmt"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// attrKey refuses a key no attribute rule may carry: empty, reserved ($
// belongs to the server), or holding a double quote (project attributes
// D5). Rules cover custom keys only.
func attrKey(k string) error {
	switch {
	case strings.TrimSpace(k) == "":
		return fmt.Errorf("attribute key is required: %w", ErrInvalid)
	case strings.HasPrefix(k, "$"):
		return fmt.Errorf("attribute %q: $ keys are reserved: %w", k, ErrInvalid)
	case strings.ContainsRune(k, '"'):
		return fmt.Errorf("attribute %q: a key may not contain a double quote: %w", k, ErrInvalid)
	}
	return nil
}

func attrSubject(id int64, key string) string { return fmt.Sprintf("%d/%s", id, key) }

// attrTarget validates what every attribute rule operation names: a known
// project and a key rules may carry.
func (o *Ops) attrTarget(ctx context.Context, projectID int64, key string) error {
	if o.Reg.Snapshot(ctx).Project(projectID) == nil {
		return fmt.Errorf("project %d: %w", projectID, ErrNotFound)
	}
	return attrKey(key)
}

// RenameAttribute gives an attribute key of the project's product and
// measures events a key not in use yet and keeps a rule so later events
// land under it. It returns the rule and the rows rewritten per table. A
// key that has data (merge it instead) or is blocked is ErrConflict.
func (o *Ops) RenameAttribute(ctx context.Context, actor string, projectID int64, from, to string) (store.AttributeRule, map[string]int64, error) {
	return o.moveAttribute(ctx, actor, "attribute.rename", projectID, from, to, false)
}

// MergeAttribute folds an attribute key into another: the same rewrite and
// rule as RenameAttribute, onto a key that may already have data. A merge
// cannot be split again. A blocked target is ErrConflict: a merge never
// drops what it moves.
func (o *Ops) MergeAttribute(ctx context.Context, actor string, projectID int64, from, to string) (store.AttributeRule, map[string]int64, error) {
	return o.moveAttribute(ctx, actor, "attribute.merge", projectID, from, to, true)
}

func (o *Ops) moveAttribute(ctx context.Context, actor, action string, projectID int64, from, to string, merge bool) (store.AttributeRule, map[string]int64, error) {
	if err := o.attrTarget(ctx, projectID, from); err != nil {
		return store.AttributeRule{}, nil, err
	}
	if err := attrKey(to); err != nil {
		return store.AttributeRule{}, nil, err
	}
	if from == to {
		return store.AttributeRule{}, nil, fmt.Errorf("attribute %q: moving it onto itself: %w", from, ErrInvalid)
	}
	counts, err := o.St.RenameAttribute(ctx, projectID, from, to, merge, store.AuditEntry{
		Actor: actor, Action: action, Subject: attrSubject(projectID, from), Detail: "to " + to})
	if err != nil {
		return store.AttributeRule{}, nil, err
	}
	o.afterWrite(ctx, true, projectID)
	return o.writtenAttributeRule(ctx, store.AttributeRule{ProjectID: projectID, Key: from,
		Action: store.EventRuleRename, To: to}), counts, nil
}

// BlockAttribute strips a key from later events: it is not stored, its
// aggregates are removed by the daily pass, and it is no longer a
// breakdown.
func (o *Ops) BlockAttribute(ctx context.Context, actor string, projectID int64, key string) (store.AttributeRule, error) {
	if err := o.attrTarget(ctx, projectID, key); err != nil {
		return store.AttributeRule{}, err
	}
	if err := o.St.BlockAttribute(ctx, projectID, key, store.AuditEntry{
		Actor: actor, Action: "attribute.block", Subject: attrSubject(projectID, key)}); err != nil {
		return store.AttributeRule{}, err
	}
	o.afterWrite(ctx, true, projectID)
	return o.writtenAttributeRule(ctx, store.AttributeRule{ProjectID: projectID, Key: key,
		Action: store.EventRuleBlock}), nil
}

// RemoveAttributeRule lifts a rename or block. Data already rewritten or
// stripped stays as it is.
func (o *Ops) RemoveAttributeRule(ctx context.Context, actor string, projectID int64, key string) error {
	if err := o.attrTarget(ctx, projectID, key); err != nil {
		return err
	}
	if err := o.St.RemoveAttributeRule(ctx, projectID, key, store.AuditEntry{
		Actor: actor, Action: "attribute.rule_remove", Subject: attrSubject(projectID, key)}); err != nil {
		return err
	}
	o.afterWrite(ctx, true, projectID)
	return nil
}

// writtenAttributeRule is r with the CreatedAt the store gave it; if the
// read-back fails the rule is returned without it, the write having
// committed.
func (o *Ops) writtenAttributeRule(ctx context.Context, r store.AttributeRule) store.AttributeRule {
	all, err := o.St.LoadAttributeRules(ctx)
	if err != nil {
		return r
	}
	for _, got := range all {
		if got.ProjectID == r.ProjectID && got.Key == r.Key {
			r.CreatedAt = got.CreatedAt
			break
		}
	}
	return r
}

// checkRuledBreakdowns refuses a breakdown list naming a key the project
// has ruled away: a blocked key is stripped from every event, and a
// renamed one is stored under its target, so neither could ever fill a
// column.
func (o *Ops) checkRuledBreakdowns(ctx context.Context, projectID int64, keys []string) error {
	set := o.Reg.Snapshot(ctx).AttributeRules(projectID)
	if set.Empty() {
		return nil
	}
	for _, k := range keys {
		if set.Blocked[k] {
			return fmt.Errorf("attribute %q is blocked; remove the block to declare it: %w", k, ErrConflict)
		}
		if to, ok := set.Renamed[k]; ok {
			return fmt.Errorf("attribute %q is renamed to %q; declare that instead: %w", k, to, ErrConflict)
		}
	}
	return nil
}
