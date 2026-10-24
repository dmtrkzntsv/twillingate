package archive

import (
	"cmp"
	"slices"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Rules are the event and attribute rules the tables apply on read
// (raw-first D6): the files keep events as they were written, and a rule
// made later renames, merges or blocks them in every query.
type Rules struct {
	Events     []store.EventRule
	Attributes []store.AttributeRule
}

// sortRules orders copies of the rules by project, then family and name
// (events) or key (attributes): a table spec must not depend on the order
// the caller listed them in.
func sortRules(r Rules) Rules {
	ev := slices.Clone(r.Events)
	slices.SortFunc(ev, func(a, b store.EventRule) int {
		return cmp.Or(cmp.Compare(a.ProjectID, b.ProjectID), cmp.Compare(a.Family, b.Family), cmp.Compare(a.Name, b.Name))
	})
	at := slices.Clone(r.Attributes)
	slices.SortFunc(at, func(a, b store.AttributeRule) int {
		return cmp.Or(cmp.Compare(a.ProjectID, b.ProjectID), cmp.Compare(a.Key, b.Key))
	})
	return Rules{Events: ev, Attributes: at}
}

func sortedIDs(files map[int64]Files) []int64 {
	ids := make([]int64, 0, len(files))
	for id := range files {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}
