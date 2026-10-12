package store

import "sort"

// MapAttributes applies a project's attribute rules to an event's custom
// keys (spec D9): a key renamed away moves to its target unless the event
// carries the target (the target wins; several keys renamed to one target
// with none sent: the first in key order wins), then a blocked key is
// removed. It returns attrs itself when nothing applies — no allocation —
// and otherwise a new map; attrs is never written. dropped lists the
// blocked keys removed, sorted.
func MapAttributes(attrs map[string]string, r AttributeRuleSet) (out map[string]string, dropped []string) {
	if r.Empty() || len(attrs) == 0 {
		return attrs, nil
	}
	hit := false
	for k := range attrs {
		if _, ok := r.Renamed[k]; ok || r.Blocked[k] {
			hit = true
			break
		}
	}
	if !hit {
		return attrs, nil
	}
	out = make(map[string]string, len(attrs))
	var moved []string
	for k, v := range attrs {
		if _, ok := r.Renamed[k]; ok {
			moved = append(moved, k)
			continue
		}
		out[k] = v
	}
	// Sorted, so that of several keys renamed to one absent target the
	// first in key order wins; a target the event carries is already in
	// out and wins over all of them.
	sort.Strings(moved)
	for _, k := range moved {
		if to := r.Renamed[k]; to != "" {
			if _, taken := out[to]; !taken {
				out[to] = attrs[k]
			}
		}
	}
	for k := range out {
		if r.Blocked[k] {
			dropped = append(dropped, k)
		}
	}
	for _, k := range dropped {
		delete(out, k)
	}
	sort.Strings(dropped)
	return out, dropped
}
