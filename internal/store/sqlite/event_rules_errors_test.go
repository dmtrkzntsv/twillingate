package sqlite

import (
	"context"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// A store error in any step of a rule operation is returned, not swallowed:
// each case breaks one table or trigger the step needs.
func TestEventRuleOperationsReportStoreErrors(t *testing.T) {
	ctx := context.Background()
	abort := func(event, table string) string {
		return `CREATE TRIGGER broken ` + event + ` ON ` + table + ` BEGIN SELECT RAISE(ABORT, 'broken'); END`
	}
	cases := []struct {
		name   string
		breaks []string
		run    func(db *DB) error
	}{
		{"load without the table", []string{`DROP TABLE rules`}, func(db *DB) error {
			_, err := db.LoadEventRules(ctx)
			return err
		}},
		{"rename to the same name", nil, func(db *DB) error {
			return db.RenameEvent(ctx, 1, store.FamilyProduct, "a", "a", ruleAudit)
		}},
		{"rename clears the target's rule", []string{`DROP TABLE rules`}, func(db *DB) error {
			return db.RenameEvent(ctx, 1, store.FamilyProduct, "a", "b", ruleAudit)
		}},
		{"rename repoints the rules that pointed at it", []string{
			`INSERT INTO rules (project_id, kind, family, name, action, to_name) VALUES (1, 'event', 'product', 'x', 'rename', 'a')`,
			abort("BEFORE UPDATE", "rules"),
		}, func(db *DB) error {
			return db.RenameEvent(ctx, 1, store.FamilyProduct, "a", "b", ruleAudit)
		}},
		{"rename keeps its rule", []string{abort("BEFORE INSERT", "rules")}, func(db *DB) error {
			return db.RenameEvent(ctx, 1, store.FamilyProduct, "a", "b", ruleAudit)
		}},
		{"block keeps its rule", []string{abort("BEFORE INSERT", "rules")}, func(db *DB) error {
			return db.BlockEvent(ctx, 1, store.FamilyProduct, "a", ruleAudit)
		}},
		{"remove deletes the rule", []string{`DROP TABLE rules`}, func(db *DB) error {
			return db.RemoveEventRule(ctx, 1, store.FamilyProduct, "a", ruleAudit)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := ruleDB(t)
			execAll(t, db, c.breaks...)
			if err := c.run(db); err == nil {
				t.Fatal("want an error, got nil")
			}
		})
	}
}
