package api

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"time"
)

// ---- received_attributes ----

// receivedKeysLimit is how many received keys an answer lists, the
// busiest over the range, below CONSOLE_QUERY_MAX_ROWS' default (1000):
// a project sending ids as keys can carry more than any row cap. The
// declared keys are listed beyond it. A variable so a test can lower it.
var receivedKeysLimit = 500

type receivedIn struct {
	ProjectID int64 `json:"project_id,omitempty" jsonschema:"one project; absent answers only the breakdown budget"`
	usageRangeIn
}

type receivedKeyOut struct {
	Key       string `json:"key"`
	Events    int64  `json:"events" jsonschema:"events and measures that carried the key over the range, as the daily pass counted them; today's are counted the night after, so a key first received today has 0"`
	MaxValues *int64 `json:"max_values" jsonschema:"the most distinct values one event (or measure) had on one day; null until the daily pass counts the day (today's), and for a key not received"`
	Received  bool   `json:"received" jsonschema:"whether any event in the range carried the key; false for a declared key none carried"`
	Declared  bool   `json:"declared" jsonschema:"whether the project declares the key as a breakdown"`
}

type receivedOut struct {
	ProjectID      int64            `json:"project_id,omitempty"`
	From           string           `json:"from"`
	To             string           `json:"to"`
	Keys           []receivedKeyOut `json:"keys" jsonschema:"the 500 busiest received keys and every declared key, busiest first"`
	KeysTotal      int64            `json:"keys_total" jsonschema:"distinct keys received over the range; more than the received keys listed when the list is capped"`
	ValuesCap      int              `json:"values_cap" jsonschema:"ATTRIBUTE_VALUES_TOP_N; 0 means no cap"`
	BreakdownsUsed int              `json:"breakdowns_used" jsonschema:"attributes declared across every active project"`
	BreakdownsMax  int              `json:"breakdowns_max" jsonschema:"ATTRIBUTE_BREAKDOWNS_MAX; 0 means no limit"`
}

func (h *host) receivedAttributes(ctx context.Context, in receivedIn) (receivedOut, error) {
	snap := h.reg.Snapshot(ctx)
	out := receivedOut{ProjectID: in.ProjectID, Keys: []receivedKeyOut{},
		ValuesCap: h.capOf(settingAttrs), BreakdownsUsed: snap.BreakdownsInUse(), BreakdownsMax: h.capOf(settingBreakdowns)}
	fromD, toD, err := usageRange(in.usageRangeIn, time.Now())
	if err != nil {
		return receivedOut{}, err
	}
	out.From, out.To = fromD.String(), toD.String()
	if in.ProjectID == 0 {
		return out, nil
	}
	p := snap.Project(in.ProjectID)
	if p == nil {
		return receivedOut{}, h.unknownProjectErr(ctx, in.ProjectID)
	}
	// The busiest keys only, so a project with more keys than the row cap
	// still answers (h.run refuses a truncated result); keys_total says
	// how many there are.
	where := `FROM received_attributes WHERE project_id = ? AND day BETWEEN ? AND ?`
	total, err := h.run(ctx, `SELECT COUNT(DISTINCT attr_key) `+where, in.ProjectID, out.From, out.To)
	if err != nil {
		return receivedOut{}, err
	}
	out.KeysTotal, _ = strconv.ParseInt(total.Rows[0][0], 10, 64)
	res, err := h.run(ctx, `SELECT attr_key, SUM(events), MAX(max_values) `+where+`
		GROUP BY attr_key ORDER BY SUM(events) DESC, attr_key LIMIT ?`,
		in.ProjectID, out.From, out.To, min(receivedKeysLimit, h.db.MaxRows()))
	if err != nil {
		return receivedOut{}, err
	}
	declared := map[string]bool{}
	for _, k := range p.Attributes {
		declared[k] = true
	}
	seen := map[string]bool{}
	add := func(rows [][]string) {
		for _, r := range rows {
			n, _ := strconv.ParseInt(r[1], 10, 64)
			k := receivedKeyOut{Key: r[0], Events: n, Received: true, Declared: declared[r[0]]}
			// A day not yet counted has a NULL max_values, which reads as no number.
			if mv, err := strconv.ParseInt(r[2], 10, 64); err == nil {
				k.MaxValues = &mv
			}
			out.Keys = append(out.Keys, k)
			seen[r[0]] = true
		}
	}
	add(res.Rows)
	// Declared keys past the busiest: their own counts, then the ones none
	// carried, with events 0.
	var rest []string
	for _, k := range p.Attributes {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	if len(rest) > 0 {
		keys, _ := json.Marshal(rest)
		res, err := h.run(ctx, `SELECT attr_key, SUM(events), MAX(max_values) `+where+`
			AND attr_key IN (SELECT value FROM json_each(?)) GROUP BY attr_key`,
			in.ProjectID, out.From, out.To, string(keys))
		if err != nil {
			return receivedOut{}, err
		}
		add(res.Rows)
	}
	for _, k := range rest {
		if !seen[k] {
			out.Keys = append(out.Keys, receivedKeyOut{Key: k, Declared: true})
			seen[k] = true
		}
	}
	sort.SliceStable(out.Keys, func(i, j int) bool {
		a, b := out.Keys[i], out.Keys[j]
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
