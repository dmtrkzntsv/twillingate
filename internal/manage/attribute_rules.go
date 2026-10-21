package manage

import (
	"context"
	"fmt"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/internal/wire"
)

// attrKey refuses a key no attribute rule may carry: empty, reserved ($
// belongs to the server), holding a double quote (project attributes D5),
// or longer than ingest keeps (wire.MaxAttrKey bytes, the limit ingest
// counts in), which no event can carry. Rules cover custom keys only.
func attrKey(k string) error {
	switch {
	case strings.TrimSpace(k) == "":
		return fmt.Errorf("attribute key is required: %w", ErrInvalid)
	case strings.HasPrefix(k, "$"):
		return fmt.Errorf("attribute %q: $ keys are reserved: %w", k, ErrInvalid)
	case strings.ContainsRune(k, '"'):
		return fmt.Errorf("attribute %q: a key may not contain a double quote: %w", k, ErrInvalid)
	case len(k) > wire.MaxAttrKey:
		return fmt.Errorf("attribute key is longer than %d bytes: %w", wire.MaxAttrKey, ErrInvalid)
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
// measures events a key not in use yet and keeps a rule, which the event
// files apply on read, history included. It returns the rule. A key that
// has a value in the files, rules applied (merge it instead), or is
// blocked is ErrConflict.
func (o *Ops) RenameAttribute(ctx context.Context, actor string, projectID int64, from, to string) (store.AttributeRule, error) {
	return o.moveAttribute(ctx, actor, "attribute.rename", projectID, from, to, false)
}

// MergeAttribute folds an attribute key into another: the same rule as
// RenameAttribute, onto a key that may already have data. A merge cannot
// be split again. A blocked target is ErrConflict: a merge never drops
// what it moves.
func (o *Ops) MergeAttribute(ctx context.Context, actor string, projectID int64, from, to string) (store.AttributeRule, error) {
	return o.moveAttribute(ctx, actor, "attribute.merge", projectID, from, to, true)
}

func (o *Ops) moveAttribute(ctx context.Context, actor, action string, projectID int64, from, to string, merge bool) (store.AttributeRule, error) {
	if err := o.attrTarget(ctx, projectID, from); err != nil {
		return store.AttributeRule{}, err
	}
	if err := attrKey(to); err != nil {
		return store.AttributeRule{}, err
	}
	if from == to {
		return store.AttributeRule{}, fmt.Errorf("attribute %q: moving it onto itself: %w", from, ErrInvalid)
	}
	if !merge && o.Presence != nil {
		has, err := o.Presence.HasAttribute(ctx, projectID, to)
		if err != nil {
			return store.AttributeRule{}, fmt.Errorf("rename attribute %s: look for %s in the event files: %w", from, to, err)
		}
		if has {
			return store.AttributeRule{}, fmt.Errorf("rename attribute %s to %s: %s already has data; merge %s into it instead: %w",
				from, to, to, from, ErrConflict)
		}
	}
	if err := o.St.RenameAttribute(ctx, projectID, from, to, store.AuditEntry{
		Actor: actor, Action: action, Subject: attrSubject(projectID, from), Detail: "to " + to}); err != nil {
		return store.AttributeRule{}, err
	}
	o.afterWrite(ctx, projectID)
	return o.writtenAttributeRule(ctx, store.AttributeRule{ProjectID: projectID, Key: from,
		Action: store.EventRuleRename, To: to}), nil
}

// BlockAttribute strips a key: later events do not store it, the event
// files read none of its values, history included.
func (o *Ops) BlockAttribute(ctx context.Context, actor string, projectID int64, key string) (store.AttributeRule, error) {
	if err := o.attrTarget(ctx, projectID, key); err != nil {
		return store.AttributeRule{}, err
	}
	if err := o.St.BlockAttribute(ctx, projectID, key, store.AuditEntry{
		Actor: actor, Action: "attribute.block", Subject: attrSubject(projectID, key)}); err != nil {
		return store.AttributeRule{}, err
	}
	o.afterWrite(ctx, projectID)
	return o.writtenAttributeRule(ctx, store.AttributeRule{ProjectID: projectID, Key: key,
		Action: store.EventRuleBlock}), nil
}

// RemoveAttributeRule lifts a rename or block. The event files read what
// was stored before the rule as it was sent again; what ingest stored
// under the new key while the rule held stays there, and what a block
// stripped stays stripped.
func (o *Ops) RemoveAttributeRule(ctx context.Context, actor string, projectID int64, key string) error {
	if err := o.attrTarget(ctx, projectID, key); err != nil {
		return err
	}
	if err := o.St.RemoveAttributeRule(ctx, projectID, key, store.AuditEntry{
		Actor: actor, Action: "attribute.rule_remove", Subject: attrSubject(projectID, key)}); err != nil {
		return err
	}
	o.afterWrite(ctx, projectID)
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
