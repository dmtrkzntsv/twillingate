//go:build race

package reporting

// Under -race (make test) TestSystemWidgetsMatchSQLite compares 7d alone:
// SQLite's views run about 27 times slower there, and every preset took
// minutes. make check compares every preset without -race on each pull
// request, and the release workflow runs it right before make test.
func init() { parityPresets = []string{"7d"} }
