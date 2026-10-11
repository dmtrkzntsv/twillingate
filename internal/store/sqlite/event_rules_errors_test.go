package sqlite

import (
	"context"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// A store error in any step of a rule operation is returned, not swallowed:
// each case breaks one table, view or trigger the step needs.
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
		{"load without the table", []string{`DROP TABLE event_rules`}, func(db *DB) error {
			_, err := db.LoadEventRules(ctx)
			return err
		}},
		{"rename to the same name", nil, func(db *DB) error {
			_, err := db.RenameEvent(ctx, 1, store.FamilyProduct, "a", "a", false, ruleAudit)
			return err
		}},
		{"rename looks for product rows", []string{`DROP VIEW raw_product`}, func(db *DB) error {
			_, err := db.RenameEvent(ctx, 1, store.FamilyProduct, "a", "b", false, ruleAudit)
			return err
		}},
		{"rename looks for measures rows", []string{`DROP VIEW raw_measures`}, func(db *DB) error {
			_, err := db.RenameEvent(ctx, 1, store.FamilyMeasures, "a", "b", false, ruleAudit)
			return err
		}},
		{"rename rewrites raw rows", []string{`DROP TABLE events`}, func(db *DB) error {
			_, err := db.RenameEvent(ctx, 1, store.FamilyProduct, "a", "b", true, ruleAudit)
			return err
		}},
		{"rename rewrites aggregates", []string{`DROP TABLE agg_product_daily`}, func(db *DB) error {
			_, err := db.RenameEvent(ctx, 1, store.FamilyProduct, "a", "b", true, ruleAudit)
			return err
		}},
		{"rename removes the old aggregates", []string{
			`INSERT INTO agg_product_daily (project_id, day, event_name, count, unique_users) VALUES (1, '2026-10-01', 'a', 1, 1)`,
			abort("BEFORE DELETE", "agg_product_daily"),
		}, func(db *DB) error {
			_, err := db.RenameEvent(ctx, 1, store.FamilyProduct, "a", "b", true, ruleAudit)
			return err
		}},
		{"rename clears the target's rule", []string{`DROP TABLE event_rules`}, func(db *DB) error {
			_, err := db.RenameEvent(ctx, 1, store.FamilyProduct, "a", "b", true, ruleAudit)
			return err
		}},
		{"rename repoints the rules that pointed at it", []string{
			`INSERT INTO event_rules (project_id, family, name, action, to_name) VALUES (1, 'product', 'x', 'rename', 'a')`,
			abort("BEFORE UPDATE", "event_rules"),
		}, func(db *DB) error {
			_, err := db.RenameEvent(ctx, 1, store.FamilyProduct, "a", "b", true, ruleAudit)
			return err
		}},
		{"rename keeps its rule", []string{abort("BEFORE INSERT", "event_rules")}, func(db *DB) error {
			_, err := db.RenameEvent(ctx, 1, store.FamilyProduct, "a", "b", true, ruleAudit)
			return err
		}},
		{"block keeps its rule", []string{abort("BEFORE INSERT", "event_rules")}, func(db *DB) error {
			return db.BlockEvent(ctx, 1, store.FamilyProduct, "a", ruleAudit)
		}},
		{"remove deletes the rule", []string{`DROP TABLE event_rules`}, func(db *DB) error {
			return db.RemoveEventRule(ctx, 1, store.FamilyProduct, "a", ruleAudit)
		}},
		{"drop blocked aggregates", []string{`DROP TABLE agg_product_daily`}, func(db *DB) error {
			return db.DropBlockedAggregates(ctx, 1)
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
