package sqlite

import (
	"context"
	"database/sql"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// A store error in any step of an attribute rule operation is returned,
// not swallowed: each case breaks one table or trigger the step needs.
func TestAttributeRuleOperationsReportStoreErrors(t *testing.T) {
	ctx := context.Background()
	abort := func(event, table string) string {
		return `CREATE TRIGGER broken ` + event + ` ON ` + table + ` BEGIN SELECT RAISE(ABORT, 'broken'); END`
	}
	merge := func(db *DB) error {
		_, err := db.RenameAttribute(ctx, 1, "a", "b", true, attrAudit)
		return err
	}
	inTx := func(fn func(tx *sql.Tx) error) func(db *DB) error {
		return func(db *DB) error { return db.tx(ctx, fn) }
	}
	catalogA := `INSERT INTO project_attributes (project_id, key) VALUES (1, 'a')`
	cases := []struct {
		name   string
		breaks []string
		run    func(db *DB) error
	}{
		{"load without the table", []string{`DROP TABLE rules`}, func(db *DB) error {
			_, err := db.LoadAttributeRules(ctx)
			return err
		}},
		{"rename looks for a block", []string{`DROP TABLE rules`}, merge},
		{"rename looks for the target's data", []string{`DROP TABLE usage_attributes`}, func(db *DB) error {
			_, err := db.RenameAttribute(ctx, 1, "a", "b", false, attrAudit)
			return err
		}},
		{"rename rewrites raw rows", []string{catalogA, `DROP TABLE events`}, merge},
		{"rename moves aggregates", []string{`DROP TABLE agg_product_attrs`}, merge},
		{"rename removes the old aggregates", []string{
			`INSERT INTO agg_product_attrs (project_id, day, event_name, attr_key, attr_value, count, unique_users) VALUES (1, '2026-10-01', 'e', 'a', 'v', 1, 1)`,
			abort("BEFORE DELETE", "agg_product_attrs"),
		}, merge},
		{"rename reads the catalog", []string{`DROP TABLE project_attributes`}, merge},
		{"rename clears the old key's breakdown", []string{
			`INSERT INTO project_attributes (project_id, key, breakdown) VALUES (1, 'a', 1)`,
			abort("BEFORE UPDATE", "project_attributes"),
		}, merge},
		{"rename adds the new catalog row", []string{catalogA, abort("BEFORE INSERT", "project_attributes")}, merge},
		{"rename clears the target's rule", []string{
			`INSERT INTO rules (project_id, kind, family, name, action, to_name) VALUES (1, 'attribute', 'event', 'b', 'rename', 'c')`,
			abort("BEFORE DELETE", "rules"),
		}, merge},
		{"rename repoints the rules that pointed at it", []string{
			`INSERT INTO rules (project_id, kind, family, name, action, to_name) VALUES (1, 'attribute', 'event', 'x', 'rename', 'a')`,
			abort("BEFORE UPDATE", "rules"),
		}, merge},
		{"rename keeps its rule", []string{abort("BEFORE INSERT", "rules")}, merge},
		{"block keeps its rule", []string{abort("BEFORE INSERT", "rules")}, func(db *DB) error {
			return db.BlockAttribute(ctx, 1, "a", attrAudit)
		}},
		{"block clears the breakdown", []string{catalogA, abort("BEFORE UPDATE", "project_attributes")}, func(db *DB) error {
			return db.BlockAttribute(ctx, 1, "a", attrAudit)
		}},
		{"remove deletes the rule", []string{`DROP TABLE rules`}, func(db *DB) error {
			return db.RemoveAttributeRule(ctx, 1, "a", attrAudit)
		}},
		{"insert reads the rules", []string{`DROP TABLE rules`}, inTx(func(tx *sql.Tx) error {
			_, err := attributeRules(ctx, tx, []store.Event{{Family: store.FamilyProduct, ProjectID: 1}})
			return err
		})},
		{"drop looks for attribute blocks", []string{`DROP TABLE rules`}, inTx(func(tx *sql.Tx) error {
			return dropBlockedAttributeRows(ctx, tx, 1)
		})},
		{"drop deletes blocked keys", []string{
			`INSERT INTO rules (project_id, kind, family, name, action) VALUES (1, 'attribute', 'event', 'x', 'block')`,
			`DROP TABLE agg_product_attrs`,
		}, inTx(func(tx *sql.Tx) error {
			return dropBlockedAttributeRows(ctx, tx, 1)
		})},
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
