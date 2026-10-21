package enginesql

import "strings"

// AttrColumn is the column of cols (an event file's columns) that holds
// the attribute key, quoted for the engine: attr_<key> as the files name
// it, a NUL in the key written as the six characters \u0000, matched
// without regard to case as DuckDB's column names are (attr_Plan is
// attr_plan). ok is false when no column holds the key.
func AttrColumn(cols []string, key string) (col string, ok bool) {
	if key == "" {
		return "", false
	}
	name := "attr_" + strings.ReplaceAll(key, "\x00", `\u0000`)
	for _, c := range cols {
		if strings.EqualFold(c, name) {
			return `"` + strings.ReplaceAll(c, `"`, `""`) + `"`, true
		}
	}
	return "", false
}
