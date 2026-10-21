package sqlite

import (
	"context"
	"testing"
)

// A store error in any step of an attribute rule operation is returned,
// not swallowed: each case breaks one table or trigger the step needs.
func TestAttributeRuleOperationsReportStoreErrors(t *testing.T) {
	ctx := context.Background()
	abort := func(event, table string) string {
		return `CREATE TRIGGER broken ` + event + ` ON ` + table + ` BEGIN SELECT RAISE(ABORT, 'broken'); END`
	}
	rename := func(db *DB) error {
		return db.RenameAttribute(ctx, 1, "a", "b", attrAudit)
	}
	cases := []struct {
		name   string
		breaks []string
		run    func(db *DB) error
	}{
		{"load without the table", []string{`DROP TABLE rules`}, func(db *DB) error {
			_, err := db.LoadAttributeRules(ctx)
			return err
		}},
		{"rename looks for a block", []string{`DROP TABLE rules`}, rename},
		{"rename clears the target's rule", []string{
			`INSERT INTO rules (project_id, kind, family, name, action, to_name) VALUES (1, 'attribute', 'event', 'b', 'rename', 'c')`,
			abort("BEFORE DELETE", "rules"),
		}, rename},
		{"rename repoints the rules that pointed at it", []string{
			`INSERT INTO rules (project_id, kind, family, name, action, to_name) VALUES (1, 'attribute', 'event', 'x', 'rename', 'a')`,
			abort("BEFORE UPDATE", "rules"),
		}, rename},
		{"rename keeps its rule", []string{abort("BEFORE INSERT", "rules")}, rename},
		{"block keeps its rule", []string{abort("BEFORE INSERT", "rules")}, func(db *DB) error {
			return db.BlockAttribute(ctx, 1, "a", attrAudit)
		}},
		{"remove deletes the rule", []string{`DROP TABLE rules`}, func(db *DB) error {
			return db.RemoveAttributeRule(ctx, 1, "a", attrAudit)
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
