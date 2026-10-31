package native

import (
	"context"
	"errors"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
)

// errPresent stops a presence scan at its first row.
var errPresent = errors.New("native: present")

// HasEvents says whether project has an event of family under name, over
// its whole history, the open logs among it, with rules (every project's;
// the project's own apply) applied as the reader's events table applies
// them: a name another rule renames there has events, a blocked one has
// none. It waits for a turn and runs again on a vanished file as Answer
// does, and stops at the first event found.
func (e *Engine) HasEvents(ctx context.Context, project int64, family, name string, rules archive.Rules) (bool, error) {
	return e.present(ctx, project, family, []string{"event_name"}, rules, func(b *batch) bool {
		_, c := b.col("event_name")
		for i := range b.N {
			if s, ok := textAt(c, i); ok && s == name {
				return true
			}
		}
		return false
	})
}

// HasAttribute says whether any of project's events, of any family, holds
// a value of key (attr_<key>, matched without regard to case as the
// reader's columns are, a NUL in key spelled \u0000), over its whole
// history with the open logs, with rules applied as HasEvents applies
// them: a key renamed away reads NULL, its target holds its values. An
// empty key has none.
func (e *Engine) HasAttribute(ctx context.Context, project int64, key string, rules archive.Rules) (bool, error) {
	if key == "" {
		return false, nil
	}
	col := visible("attr_" + key)
	return e.present(ctx, project, "", []string{col}, rules, func(b *batch) bool {
		_, c := b.col(col)
		if c == nil {
			return false
		}
		for i := range b.N {
			if c.Idx != nil {
				if c.Idx[i] >= 0 {
					return true
				}
			} else if c.Null == nil || !c.Null[i] {
				return true
			}
		}
		return false
	})
}

// present scans cols of project's rows of family ("" for every family),
// the rules applied, until hit says a batch holds the row looked for.
func (e *Engine) present(ctx context.Context, project int64, family string, cols []string, rules archive.Rules, hit func(*batch) bool) (bool, error) {
	table := family
	if table == "" {
		table = "events"
	}
	var found bool
	err := e.turn(ctx, func() error {
		found = false
		all, err := archive.ListFiles(e.dir)
		if err != nil {
			return err
		}
		rs, err := e.ruleSet(project, rules)
		if err != nil {
			return err
		}
		s := scan{e: e, q: Query{Table: table}, project: project, rules: rs}
		err = s.scanEvents(ctx, all[project], everything, cols, nil, e.charge(), func(_ int, b *batch) error {
			if hit(b) {
				return errPresent
			}
			return nil
		})
		if errors.Is(err, errPresent) {
			found = true
			return nil
		}
		return err
	})
	return found, err
}
