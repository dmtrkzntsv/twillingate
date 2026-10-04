package api

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func ptr[T any](v T) *T { return &v }

// received_attributes lists the range's received keys merged with the
// project's declared ones (events 0, max_values null when not received),
// sorted by events then key, with the values cap and the breakdown
// budget; without project_id it answers only the budget.
func TestReceivedAttributes(t *testing.T) {
	h, cs := newTestHost(t)
	ctx := context.Background()
	// Project 1 declares plan (the fixture's) and never_sent; plan was
	// received on two days, order_id on one day not yet counted (today's).
	for _, q := range []string{
		`UPDATE projects SET attributes = '["plan","never_sent"]' WHERE id = 1`,
		`INSERT INTO received_attributes (project_id, day, attr_key, events, max_values) VALUES
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
	out, err := h.receivedAttributes(ctx, receivedIn{ProjectID: 1, usageRangeIn: usageRangeIn{From: "2026-10-01", To: "2026-10-02"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []receivedKeyOut{
		{Key: "order_id", Events: 9, MaxValues: nil, Received: true, Declared: false},
		{Key: "plan", Events: 7, MaxValues: ptr(int64(4)), Received: true, Declared: true},
		// First received today: ingest records it with events 0 until the
		// daily pass counts the day; listed before a key none carried.
		{Key: "zz_new_today", Events: 0, MaxValues: nil, Received: true},
		{Key: "never_sent", Events: 0, MaxValues: nil, Declared: true},
	}
	if !reflect.DeepEqual(out.Keys, want) {
		t.Errorf("keys = %+v, want %+v", out.Keys, want)
	}
	if out.KeysTotal != 3 {
		t.Errorf("keys_total = %d, want 3", out.KeysTotal)
	}
	if out.From != "2026-10-01" || out.To != "2026-10-02" || out.ProjectID != 1 {
		t.Errorf("range = %+v", out)
	}
	if out.ValuesCap != h.capOf(settingAttrs) || out.BreakdownsMax != h.capOf(settingBreakdowns) || out.BreakdownsUsed != 2 {
		t.Errorf("budget = %+v", out)
	}
	// Without a project: no keys, the budget only.
	none, err := h.receivedAttributes(ctx, receivedIn{})
	if err != nil || len(none.Keys) != 0 || none.Keys == nil || none.BreakdownsUsed != 2 {
		t.Errorf("no project = %+v, %v", none, err)
	}
	// An unknown project is refused like every project tool.
	if _, err := h.receivedAttributes(ctx, receivedIn{ProjectID: 999}); err == nil {
		t.Error("unknown project accepted")
	}
	// The MCP tool answers the same keys.
	var mcpOut receivedOut
	if err := json.Unmarshal([]byte(textOf(callTool(t, cs, "received_attributes",
		map[string]any{"project_id": 1, "from": "2026-10-01", "to": "2026-10-02"}))), &mcpOut); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(mcpOut.Keys, want) {
		t.Errorf("MCP keys = %+v", mcpOut.Keys)
	}
}

// Past receivedKeysLimit the answer lists only the busiest keys (ties by
// key), plus every declared key with its own counts, and keys_total
// counts them all, rather than failing on the row cap.
func TestReceivedAttributesCapsTheKeys(t *testing.T) {
	h, _ := newTestHost(t)
	ctx := context.Background()
	defer func(n int) { receivedKeysLimit = n }(receivedKeysLimit)
	receivedKeysLimit = 2
	for _, q := range []string{
		`UPDATE projects SET attributes = '["plan","never_sent"]' WHERE id = 1`,
		`INSERT INTO received_attributes (project_id, day, attr_key, events, max_values) VALUES
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
	out, err := h.receivedAttributes(ctx, receivedIn{ProjectID: 1, usageRangeIn: usageRangeIn{From: "2026-10-01", To: "2026-10-02"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []receivedKeyOut{
		{Key: "id_1", Events: 10, MaxValues: ptr(int64(1)), Received: true},
		{Key: "id_2", Events: 8, MaxValues: ptr(int64(1)), Received: true},
		{Key: "plan", Events: 3, MaxValues: ptr(int64(2)), Received: true, Declared: true},
		{Key: "never_sent", Declared: true},
	}
	if !reflect.DeepEqual(out.Keys, want) {
		t.Errorf("keys = %+v, want %+v", out.Keys, want)
	}
	if out.KeysTotal != 5 {
		t.Errorf("keys_total = %d, want 5", out.KeysTotal)
	}
}
