package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// TestListAttributesReadsTheFiles: each key the range's product events
// and measures carried in the event files, with how many carried it and
// its distinct values, busiest first, rules applied as the reader applies
// them (a key merged into another counts there and is its alias). A key
// only a rule names (blocked, or a rename target) is listed received:
// false, a blocked key's rows hidden; views and another project's events
// are not read.
func TestListAttributesReadsTheFiles(t *testing.T) {
	h, cs := newTestHost(t)
	ctx := context.Background()
	// The template's files carry plan on signup (pro x5 on 08-20, team x2
	// on 08-21) and tier on one checkout_api measure (gold, 08-20).
	at := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	writeHostEvents(t, h,
		store.Event{ProjectID: 1, Family: store.FamilyProduct, EventName: "upgrade", TS: at,
			Attributes: map[string]string{"plan": "free", "ref": "x", "noisy": "1"}},
		store.Event{ProjectID: 1, Family: store.FamilyProduct, EventName: "upgrade", TS: at,
			Attributes: map[string]string{"old_ref": "y"}},
		store.Event{ProjectID: 1, Family: store.FamilyViews, EventName: "$page_view", Kind: "web", Path: "/", TS: at,
			Attributes: map[string]string{"view_key": "v"}},
		store.Event{ProjectID: 2, Family: store.FamilyProduct, EventName: "signup", TS: at,
			Attributes: map[string]string{"other_project": "z"}},
		store.Event{ProjectID: 1, Family: store.FamilyProduct, EventName: "late", TS: at.AddDate(0, 0, 2),
			Attributes: map[string]string{"after_the_range": "z"}})
	for _, step := range []func() error{
		func() error { _, err := h.ops.MergeAttribute(ctx, "test", 1, "old_ref", "ref"); return err },
		func() error { _, err := h.ops.RenameAttribute(ctx, "test", 1, "tier_old", "tier2"); return err },
		func() error { _, err := h.ops.BlockAttribute(ctx, "test", 1, "noisy"); return err },
		func() error { _, err := h.ops.BlockAttribute(ctx, "test", 1, "debug_blob"); return err },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	out, err := h.listAttributes(ctx, attributesIn{ProjectID: 1, usageRangeIn: usageRangeIn{From: "2026-08-20", To: "2026-08-21"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []attributeOut{
		{Key: "plan", Events: 8, Values: 3, Received: true},
		{Key: "ref", Events: 2, Values: 2, Received: true, Aliases: []string{"old_ref"}},
		{Key: "tier", Events: 1, Values: 1, Received: true},
		{Key: "debug_blob", Blocked: true},
		{Key: "noisy", Blocked: true},
		{Key: "tier2", Aliases: []string{"tier_old"}},
	}
	if !reflect.DeepEqual(out.Attributes, want) {
		t.Errorf("attributes = %+v, want %+v", out.Attributes, want)
	}
	if out.KeysTotal != 3 || out.ProjectID != 1 || out.From != "2026-08-20" || out.To != "2026-08-21" {
		t.Errorf("keys_total %d, range %+v; want 3 keys over 2026-08-20..21", out.KeysTotal, out)
	}
	// The old fields are gone; aliases is left out when empty.
	b, _ := json.Marshal(out)
	for _, gone := range []string{"breakdown", "max_values", "values_cap", "breakdowns_used", "breakdowns_max"} {
		if strings.Contains(string(b), `"`+gone+`"`) {
			t.Errorf("the answer still carries %s: %s", gone, b)
		}
	}
	var m map[string]any
	b, _ = json.Marshal(out.Attributes[0])
	_ = json.Unmarshal(b, &m)
	if _, has := m["aliases"]; has {
		t.Errorf("a key without aliases carries the field: %s", b)
	}

	// The MCP tool and the route answer the same keys.
	var mcpOut listAttributesOut
	if err := json.Unmarshal([]byte(textOf(callTool(t, cs, "list_attributes",
		map[string]any{"project_id": 1, "from": "2026-08-20", "to": "2026-08-21"}))), &mcpOut); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(mcpOut.Attributes, want) {
		t.Errorf("MCP attributes = %+v", mcpOut.Attributes)
	}
	r := newTestRegistrar(t, h)
	rec := serveREST(t, r, "GET", "/api/projects/1/attributes?from=2026-08-20&to=2026-08-21", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET attributes = %d %s", rec.Code, rec.Body)
	}
	var restOut listAttributesOut
	if err := json.Unmarshal(rec.Body.Bytes(), &restOut); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restOut.Attributes, want) {
		t.Errorf("REST attributes = %+v", restOut.Attributes)
	}
	if rec := serveREST(t, r, "GET", "/api/projects/99/attributes", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown project = %d %s, want 404", rec.Code, rec.Body)
	}
	for _, id := range []int64{0, 999} {
		if _, err := h.listAttributes(ctx, attributesIn{ProjectID: id}); !errors.Is(err, manage.ErrNotFound) {
			t.Errorf("project %d: %v, want not found", id, err)
		}
	}
	// A project with no attribute in its files answers none.
	empty, err := h.listAttributes(ctx, attributesIn{ProjectID: 2, usageRangeIn: usageRangeIn{From: "2026-08-01", To: "2026-08-20"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Attributes) != 0 || empty.KeysTotal != 0 {
		t.Errorf("docs before its event = %+v, want no keys", empty)
	}
}

// Past attributeKeysLimit the answer lists only the busiest keys (ties by
// key), plus every key a rule names with its own counts, and keys_total
// counts them all.
func TestListAttributesCapsTheKeys(t *testing.T) {
	h, _ := newTestHost(t)
	ctx := context.Background()
	defer func(n int) { attributeKeysLimit = n }(attributeKeysLimit)
	attributeKeysLimit = 2
	at := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	var evs []store.Event
	for _, k := range []string{"id_1", "id_1", "id_1", "id_2", "id_2", "id_3", "id_3"} {
		evs = append(evs, store.Event{ProjectID: 1, Family: store.FamilyProduct, EventName: "e", TS: at,
			Attributes: map[string]string{k: "v"}})
	}
	writeHostEvents(t, h, evs...)
	if _, err := h.ops.MergeAttribute(ctx, "test", 1, "tier_old", "tier"); err != nil {
		t.Fatal(err)
	}
	out, err := h.listAttributes(ctx, attributesIn{ProjectID: 1, usageRangeIn: usageRangeIn{From: "2026-08-21", To: "2026-08-21"}})
	if err != nil {
		t.Fatal(err)
	}
	// id_2, id_3 and the template's plan (team x2) tie at 2: id_2 comes
	// first by key, the other two are past the limit.
	want := []attributeOut{
		{Key: "id_1", Events: 3, Values: 1, Received: true},
		{Key: "id_2", Events: 2, Values: 1, Received: true},
		{Key: "tier", Aliases: []string{"tier_old"}},
	}
	if !reflect.DeepEqual(out.Attributes, want) {
		t.Errorf("attributes = %+v, want %+v", out.Attributes, want)
	}
	if out.KeysTotal != 4 {
		t.Errorf("keys_total = %d, want 4 (id_1, id_2, id_3, plan)", out.KeysTotal)
	}

	// A rule's key past the limit keeps its own counts.
	if _, err := h.ops.MergeAttribute(ctx, "test", 1, "plan_old", "id_3"); err != nil {
		t.Fatal(err)
	}
	out, err = h.listAttributes(ctx, attributesIn{ProjectID: 1, usageRangeIn: usageRangeIn{From: "2026-08-21", To: "2026-08-21"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Attributes[2]; got.Key != "id_3" || got.Events != 2 || !got.Received {
		t.Errorf("attributes = %+v, want id_3 with its 2 events after the busiest two", out.Attributes)
	}
}

// TestListAttributesCoversAYear: list_attributes reads the native
// engine, so a range runs to usage's 400 days (the 92-day cap went with
// DuckDB); its default is the last 30 days. Past the engine's groups the
// list would be partial, and is refused.
func TestListAttributesCoversAYear(t *testing.T) {
	h, cs := newTestHost(t)
	ctx := context.Background()
	out, err := h.listAttributes(ctx, attributesIn{ProjectID: 1, usageRangeIn: usageRangeIn{From: "2025-08-03", To: "2026-09-06"}})
	if err != nil || len(out.Attributes) == 0 {
		t.Errorf("400 days = %+v, %v", out, err)
	}
	if _, err := h.listAttributes(ctx, attributesIn{ProjectID: 1, usageRangeIn: usageRangeIn{From: "2025-08-02", To: "2026-09-06"}}); !errors.Is(err, manage.ErrInvalid) {
		t.Errorf("401 days: %v, want refused", err)
	}
	if out, err = h.listAttributes(ctx, attributesIn{ProjectID: 1}); err != nil {
		t.Fatal(err)
	}
	if from, _ := time.Parse(time.DateOnly, out.From); out.To != time.Now().UTC().Format(time.DateOnly) || from.AddDate(0, 0, 29).Format(time.DateOnly) != out.To {
		t.Errorf("default range %s..%s, want the last 30 days", out.From, out.To)
	}
	if res := callTool(t, cs, "list_attributes", map[string]any{"project_id": 1, "from": "2026-01-01", "to": "2026-08-01"}); res.IsError {
		t.Errorf("MCP 213 days = %s", textOf(res))
	}
	h.native = native.New(native.Options{Dir: h.eventsDir, MaxGroups: 1})
	_, err = h.listAttributes(ctx, attributesIn{ProjectID: 1, usageRangeIn: usageRangeIn{From: "2026-08-20", To: "2026-08-21"}})
	if !errors.Is(err, manage.ErrInvalid) || !strings.Contains(err.Error(), "narrow the date range") {
		t.Errorf("past the engine's groups: %v, want refused", err)
	}
}

// More rule-named keys than one query keeps are asked in turns, each key
// listed once with its own counts.
func TestListAttributesKeepsEveryRuleNamedKey(t *testing.T) {
	h, _ := newTestHost(t)
	ctx := context.Background()
	defer func(n int) { attributeKeysLimit = n }(attributeKeysLimit)
	attributeKeysLimit = 1
	at := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	writeHostEvents(t, h, store.Event{ProjectID: 1, Family: store.FamilyProduct, EventName: "e", TS: at,
		Attributes: map[string]string{"zz_last": "v"}})
	for i := range native.MaxKeep {
		if _, err := h.ops.BlockAttribute(ctx, "test", 1, fmt.Sprintf("k%04d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.ops.MergeAttribute(ctx, "test", 1, "zz_old", "zz_last"); err != nil {
		t.Fatal(err)
	}
	out, err := h.listAttributes(ctx, attributesIn{ProjectID: 1, usageRangeIn: usageRangeIn{From: "2026-08-21", To: "2026-08-21"}})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(out.Attributes); n != native.MaxKeep+2 {
		t.Errorf("%d keys, want the busiest, zz_last and the %d blocked", n, native.MaxKeep)
	}
	i := slices.IndexFunc(out.Attributes, func(a attributeOut) bool { return a.Key == "zz_last" })
	if i < 0 || !out.Attributes[i].Received || out.Attributes[i].Events != 1 {
		t.Errorf("zz_last = %+v, want received with its event", out.Attributes)
	}
}
