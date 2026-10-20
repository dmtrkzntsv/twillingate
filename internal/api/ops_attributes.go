package api

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/enginesql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// ---- list_attributes ----

// attributeKeysLimit is how many received keys an answer lists, the
// busiest over the range, below CONSOLE_QUERY_ROWS_MAX' default (1000):
// a project sending ids as keys can carry more than any row cap. The keys
// a rule names are listed beyond it. A variable so a test can lower it.
var attributeKeysLimit = 500

type attributesIn struct {
	ProjectID int64 `json:"project_id" jsonschema:"project id; call list_projects first"`
	usageRangeIn
}

type attributeOut struct {
	Key      string   `json:"key"`
	Events   int64    `json:"events" jsonschema:"product events and measures that carried the key over the range, rules applied"`
	Values   int64    `json:"values" jsonschema:"distinct values the key had over the range"`
	Received bool     `json:"received" jsonschema:"whether any event in the range carried the key; false for a blocked key or a rename target none carried"`
	Blocked  bool     `json:"blocked" jsonschema:"whether block_attribute hides the key from every read"`
	Aliases  []string `json:"aliases,omitempty" jsonschema:"keys renamed or merged into this one; they read as this key"`
}

type listAttributesOut struct {
	ProjectID  int64          `json:"project_id"`
	From       string         `json:"from"`
	To         string         `json:"to"`
	Attributes []attributeOut `json:"attributes" jsonschema:"the 500 busiest keys received in the range and every key a rule names, busiest first; a key renamed away is only in its target's aliases"`
	KeysTotal  int64          `json:"keys_total" jsonschema:"distinct keys received over the range, rules applied; more than the received keys listed when the list is capped"`
}

// listAttributes answers a project's attribute keys over a range, from
// the event files: each key its product events and measures carried
// (the busiest only, and those its attribute rules name), with how many
// carried it and its distinct values, and the keys only a rule names. The
// reader applies the rules, so a key renamed away reads as its target and
// a blocked key reads as none.
func (h *host) listAttributes(ctx context.Context, in attributesIn) (listAttributesOut, error) {
	snap := h.reg.Snapshot(ctx)
	fromD, toD, err := usageRange(in.usageRangeIn, time.Now())
	if err != nil {
		return listAttributesOut{}, err
	}
	if days := int(toD.Time().Sub(fromD.Time()).Hours()/24) + 1; days > productAttributesMaxDays {
		return listAttributesOut{}, invalidf("list_attributes covers at most %d days a call, got %d: it reads every value of the range. "+
			"Narrow from and to", productAttributesMaxDays, days)
	}
	p := snap.Project(in.ProjectID)
	if p == nil {
		return listAttributesOut{}, h.unknownProjectErr(ctx, in.ProjectID)
	}
	out := listAttributesOut{ProjectID: p.ID, From: fromD.String(), To: toD.String(), Attributes: []attributeOut{}}

	blocked := map[string]bool{}
	aliases := map[string][]string{}
	var named []string // listed even past the busiest
	for _, r := range snap.AttributeRuleList(p.ID) { // ordered by key, so are the aliases
		switch r.Action {
		case store.EventRuleBlock:
			blocked[r.Key] = true
			named = append(named, r.Key)
		case store.EventRuleRename:
			if len(aliases[r.To]) == 0 {
				named = append(named, r.To)
			}
			aliases[r.To] = append(aliases[r.To], r.Key)
		}
	}

	// Every attr_ column of the range's product and measures rows,
	// unpivoted as product_attributes does; the NULL "attr_$" (no key
	// ingest stores) keeps the unpivot from an empty column list, and the
	// unpivot drops NULLs, a key an event did not carry. keys_total is
	// counted over every key before the busiest are kept.
	limit := min(attributeKeysLimit, h.queryRows)
	vals := map[string]enginesql.Value{":limit": {Type: "BIGINT", Text: strconv.Itoa(limit)}}
	keep := `row_number() OVER (ORDER BY events DESC, attr_key) <= :limit`
	if len(named) > 0 {
		params := make([]string, len(named))
		for i, k := range named {
			params[i] = ":k" + strconv.Itoa(i)
			vals[params[i]] = enginesql.Value{Type: "VARCHAR", Text: k}
		}
		keep += ` OR attr_key IN (` + strings.Join(params, ", ") + `)`
	}
	tbl, err := h.filesTable(ctx, rangeIn{ProjectID: p.ID, From: out.From, To: out.To}, `WITH a AS (
  UNPIVOT (
    SELECT COLUMNS('^(day|attr_.*)$'), CAST(NULL AS VARCHAR) AS "attr_$"
    FROM events
    WHERE family IN ('product', 'measures') AND day BETWEEN :from AND :to
  )
  ON COLUMNS('^attr_') INTO NAME col VALUE attr_value
),
k AS (
  SELECT substr(col, 6) AS attr_key, count(*) AS events, count(DISTINCT attr_value) AS "values"
  FROM a
  GROUP BY col
)
SELECT attr_key, events, "values", count(*) OVER () AS keys_total
FROM k
QUALIFY `+keep+`
ORDER BY events DESC, attr_key`, vals)
	if err != nil {
		return listAttributesOut{}, err
	}
	if tbl.Truncated {
		return listAttributesOut{}, invalidf("too many attribute keys for CONSOLE_QUERY_ROWS_MAX (%d); narrow the date range", h.queryRows)
	}
	seen := map[string]bool{}
	for _, r := range tbl.Rows {
		k := attributeOut{Key: r[0], Received: true, Blocked: blocked[r[0]], Aliases: aliases[r[0]]}
		k.Events, _ = strconv.ParseInt(r[1], 10, 64)
		k.Values, _ = strconv.ParseInt(r[2], 10, 64)
		out.KeysTotal, _ = strconv.ParseInt(r[3], 10, 64)
		out.Attributes = append(out.Attributes, k)
		seen[r[0]] = true
	}
	// The keys only a rule names, with nothing in the range.
	for _, k := range named {
		if !seen[k] {
			out.Attributes = append(out.Attributes, attributeOut{Key: k, Blocked: blocked[k], Aliases: aliases[k]})
			seen[k] = true
		}
	}
	sort.SliceStable(out.Attributes, func(i, j int) bool {
		a, b := out.Attributes[i], out.Attributes[j]
		if a.Events != b.Events {
			return a.Events > b.Events
		}
		if a.Received != b.Received {
			return a.Received
		}
		return a.Key < b.Key
	})
	return out, nil
}
