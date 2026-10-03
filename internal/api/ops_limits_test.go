package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/config"
)

// limits reports the caps in force beside their defaults, 0 included
// (no cap), in a fixed order: views, attributes, identities.
func TestLimitsReportsTheCapsInForce(t *testing.T) {
	h, cs := newTestHost(t)
	h.limits = limitsFrom(&config.Config{ProductAttributesTopN: 7, ViewsDimensionsTopN: 0, IdentitiesTopN: 2000})
	out, err := h.listLimits(context.Background(), struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		setting   string
		value, df int
	}{
		{"VIEWS_DIMENSIONS_TOP_N", 0, config.DefaultViewsDimensionsTopN},
		{"PRODUCT_ATTRIBUTES_TOP_N", 7, config.DefaultProductAttributesTopN},
		{"IDENTITIES_TOP_N", 2000, config.DefaultIdentitiesTopN},
	}
	if len(out.Limits) != len(want) {
		t.Fatalf("limits = %+v", out.Limits)
	}
	for i, w := range want {
		l := out.Limits[i]
		if l.Setting != w.setting || l.Value != w.value || l.Default != w.df || l.Caps == "" {
			t.Errorf("limits[%d] = %+v, want %s %d (default %d) with a description", i, l, w.setting, w.value, w.df)
		}
	}
	if h.capOf(settingViews) != 0 || h.capOf(settingAttrs) != 7 || h.capOf(settingIdentities) != 2000 {
		t.Errorf("capOf disagrees with limits: %+v", out.Limits)
	}
	// The MCP tool answers the same.
	var mcpOut limitsOut
	if err := json.Unmarshal([]byte(textOf(callTool(t, cs, "limits", map[string]any{}))), &mcpOut); err != nil {
		t.Fatal(err)
	}
	if len(mcpOut.Limits) != 3 {
		t.Errorf("MCP limits = %+v", mcpOut.Limits)
	}
}
