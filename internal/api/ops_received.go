package api

import (
	"context"
	"sort"
	"strconv"
	"time"
)

// ---- received_attributes ----

type receivedIn struct {
	ProjectID int64 `json:"project_id,omitempty" jsonschema:"one project; absent answers only the breakdown budget"`
	usageRangeIn
}

type receivedKeyOut struct {
	Key       string `json:"key"`
	Events    int64  `json:"events" jsonschema:"events and measures that carried the key over the range; 0 for a declared key none carried"`
	MaxValues *int64 `json:"max_values" jsonschema:"the most distinct values one event (or measure) had on one day; null until the daily pass counts the day (today's), and for a key not received"`
	Declared  bool   `json:"declared" jsonschema:"whether the project declares the key as a breakdown"`
}

type receivedOut struct {
	ProjectID      int64            `json:"project_id,omitempty"`
	From           string           `json:"from"`
	To             string           `json:"to"`
	Keys           []receivedKeyOut `json:"keys" jsonschema:"received and declared keys, busiest first"`
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
	// One row per key, so the answer fits the row cap for any range.
	res, err := h.run(ctx, `SELECT attr_key, SUM(events), MAX(max_values) FROM received_attributes
		WHERE project_id = ? AND day BETWEEN ? AND ? GROUP BY attr_key`, in.ProjectID, out.From, out.To)
	if err != nil {
		return receivedOut{}, err
	}
	declared := map[string]bool{}
	for _, k := range p.Attributes {
		declared[k] = true
	}
	seen := map[string]bool{}
	for _, r := range res.Rows {
		n, _ := strconv.ParseInt(r[1], 10, 64)
		k := receivedKeyOut{Key: r[0], Events: n, Declared: declared[r[0]]}
		// A day not yet counted has a NULL max_values, which reads as no number.
		if mv, err := strconv.ParseInt(r[2], 10, 64); err == nil {
			k.MaxValues = &mv
		}
		out.Keys = append(out.Keys, k)
		seen[r[0]] = true
	}
	for _, k := range p.Attributes {
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
		return a.Key < b.Key
	})
	return out, nil
}
