package api

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
)

func ptr[T any](v T) *T { return &v }

// list_attributes lists the range's received keys merged with the
// project's breakdown keys (events 0, max_values null when not received),
// sorted by events then key, with the values cap and the breakdown budget.
func TestListAttributes(t *testing.T) {
	h, cs := newTestHost(t)
	ctx := context.Background()
	// Project 1 breaks down plan (the fixture's) and never_sent; plan was
	// received on two days, order_id on one day not yet counted (today's).
	for _, q := range []string{
		`INSERT OR REPLACE INTO project_attributes (project_id, key, breakdown) VALUES (1, 'plan', 1), (1, 'never_sent', 1)`,
		`INSERT INTO usage_attributes (project_id, day, attr_key, events, max_values) VALUES
		 (1,'2026-10-01','plan',5,3), (1,'2026-10-02','plan',2,4), (1,'2026-10-02','order_id',9,NULL), (1,'2026-10-02','zz_new_today',0,NULL),
		 (1,'2026-09-30','old_key',1,1), (2,'2026-10-02','other_project',4,1)`,
	} {
		if _, err := rawExec(h.ops.St, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	out, err := h.listAttributes(ctx, attributesIn{ProjectID: 1, usageRangeIn: usageRangeIn{From: "2026-10-01", To: "2026-10-02"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []attributeOut{
		{Key: "order_id", Events: 9, MaxValues: nil, Received: true},
		{Key: "plan", Events: 7, MaxValues: ptr(int64(4)), Received: true, Breakdown: true},
		// First received today: ingest records it with events 0 until the
		// daily pass counts the day; listed before a key none carried.
		{Key: "zz_new_today", Events: 0, MaxValues: nil, Received: true},
		{Key: "never_sent", Events: 0, MaxValues: nil, Breakdown: true},
	}
	if !reflect.DeepEqual(out.Attributes, want) {
		t.Errorf("attributes = %+v, want %+v", out.Attributes, want)
	}
	if out.KeysTotal != 3 {
		t.Errorf("keys_total = %d, want 3", out.KeysTotal)
	}
	if out.From != "2026-10-01" || out.To != "2026-10-02" || out.ProjectID != 1 {
		t.Errorf("range = %+v", out)
	}
	if out.ValuesCap != h.valuesCap || out.BreakdownsMax != h.breakdownsMax || out.BreakdownsUsed != 2 {
		t.Errorf("budget = %+v", out)
	}
	// An unknown project, or none, is refused like every project tool.
	for _, id := range []int64{0, 999} {
		if _, err := h.listAttributes(ctx, attributesIn{ProjectID: id}); err == nil {
			t.Errorf("project %d accepted", id)
		}
	}
	// The MCP tool answers the same keys.
	var mcpOut listAttributesOut
	if err := json.Unmarshal([]byte(textOf(callTool(t, cs, "list_attributes",
		map[string]any{"project_id": 1, "from": "2026-10-01", "to": "2026-10-02"}))), &mcpOut); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(mcpOut.Attributes, want) {
		t.Errorf("MCP attributes = %+v", mcpOut.Attributes)
	}
	// So does the route, with project_id in the path.
	r := newTestRegistrar(t, h)
	rec := serveREST(t, r, "GET", "/api/projects/1/attributes?from=2026-10-01&to=2026-10-02", "")
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
	// The old tool and route are gone.
	if rec := serveREST(t, r, "GET", "/api/received-attributes?project_id=1", ""); rec.Code != http.StatusNotFound {
		t.Errorf("GET /api/received-attributes = %d, want 404", rec.Code)
	}
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "received_attributes" {
			t.Error("received_attributes is still an MCP tool")
		}
	}
}

// Rules show on the keys they touch: a blocked key is listed even with
// nothing received, the keys renamed or merged into a key are its
// aliases (the target listed even with nothing in the range, so every
// rule can be found), and a key renamed away is not listed, even when a
// usage row still names it, nor counted in keys_total.
func TestListAttributesShowsRules(t *testing.T) {
	h, _ := newTestHost(t)
	ctx := context.Background()
	if _, err := rawExec(h.ops.St, `INSERT INTO usage_attributes (project_id, day, attr_key, events, max_values) VALUES
		(1,'2026-10-01','plan',5,3), (1,'2026-10-01','noisy',4,2)`); err != nil {
		t.Fatal(err)
	}
	for _, step := range []func() error{
		func() error { _, _, err := h.ops.MergeAttribute(ctx, "test", 1, "planType", "plan"); return err },
		func() error { _, _, err := h.ops.MergeAttribute(ctx, "test", 1, "Plan", "plan"); return err },
		func() error { _, _, err := h.ops.RenameAttribute(ctx, "test", 1, "tier_old", "tier"); return err },
		func() error { _, err := h.ops.BlockAttribute(ctx, "test", 1, "noisy"); return err },
		func() error { _, err := h.ops.BlockAttribute(ctx, "test", 1, "debug_blob"); return err },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	// A row naming a key renamed away, as one counted before the rule.
	if _, err := rawExec(h.ops.St, `INSERT INTO usage_attributes (project_id, day, attr_key, events, max_values) VALUES
		(1,'2026-10-02','planType',50,1)`); err != nil {
		t.Fatal(err)
	}
	out, err := h.listAttributes(ctx, attributesIn{ProjectID: 1, usageRangeIn: usageRangeIn{From: "2026-10-01", To: "2026-10-02"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []attributeOut{
		{Key: "plan", Events: 5, MaxValues: ptr(int64(3)), Received: true, Breakdown: true, Aliases: []string{"Plan", "planType"}},
		{Key: "noisy", Events: 4, MaxValues: ptr(int64(2)), Received: true, Blocked: true},
		{Key: "debug_blob", Blocked: true},
		{Key: "tier", Aliases: []string{"tier_old"}},
	}
	if !reflect.DeepEqual(out.Attributes, want) {
		t.Errorf("attributes = %+v, want %+v", out.Attributes, want)
	}
	if out.KeysTotal != 2 {
		t.Errorf("keys_total = %d, want 2 (planType renamed away)", out.KeysTotal)
	}
	// aliases is left out when empty.
	b, _ := json.Marshal(out.Attributes[1])
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if _, has := m["aliases"]; has {
		t.Errorf("a key without aliases carries the field: %s", b)
	}
}

// Past attributeKeysLimit the answer lists only the busiest keys (ties by
// key), plus every breakdown key with its own counts, and keys_total
// counts them all, rather than failing on the row cap.
func TestListAttributesCapsTheKeys(t *testing.T) {
	h, _ := newTestHost(t)
	ctx := context.Background()
	defer func(n int) { attributeKeysLimit = n }(attributeKeysLimit)
	attributeKeysLimit = 2
	for _, q := range []string{
		`INSERT OR REPLACE INTO project_attributes (project_id, key, breakdown) VALUES (1, 'plan', 1), (1, 'never_sent', 1)`,
		`INSERT INTO usage_attributes (project_id, day, attr_key, events, max_values) VALUES
		 (1,'2026-10-01','id_1',9,1), (1,'2026-10-02','id_1',1,1), (1,'2026-10-01','id_2',8,1),
		 (1,'2026-10-01','id_3',8,1), (1,'2026-10-01','plan',3,2), (1,'2026-10-01','id_4',5,1)`,
	} {
		if _, err := rawExec(h.ops.St, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	out, err := h.listAttributes(ctx, attributesIn{ProjectID: 1, usageRangeIn: usageRangeIn{From: "2026-10-01", To: "2026-10-02"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []attributeOut{
		{Key: "id_1", Events: 10, MaxValues: ptr(int64(1)), Received: true},
		{Key: "id_2", Events: 8, MaxValues: ptr(int64(1)), Received: true},
		{Key: "plan", Events: 3, MaxValues: ptr(int64(2)), Received: true, Breakdown: true},
		{Key: "never_sent", Breakdown: true},
	}
	if !reflect.DeepEqual(out.Attributes, want) {
		t.Errorf("attributes = %+v, want %+v", out.Attributes, want)
	}
	if out.KeysTotal != 5 {
		t.Errorf("keys_total = %d, want 5", out.KeysTotal)
	}
}
