package api

import (
	"context"

	"github.com/dmtrkzntsv/twillingate/internal/config"
)

// The caps, by the setting that sets each.
const (
	settingViews      = "VIEWS_DIMENSIONS_TOP_N"
	settingAttrs      = "PRODUCT_ATTRIBUTES_TOP_N"
	settingIdentities = "IDENTITIES_TOP_N"
)

// ---- limits ----

type limitOut struct {
	Setting string `json:"setting"`
	Value   int    `json:"value" jsonschema:"the cap in force; 0 means no cap"`
	Default int    `json:"default"`
	Caps    string `json:"caps" jsonschema:"what the setting caps"`
}

type limitsOut struct {
	Limits []limitOut `json:"limits"`
}

// limitsFrom reads the caps in force from the running config, in the order
// the console shows them.
func limitsFrom(cfg *config.Config) []limitOut {
	return []limitOut{
		{settingViews, cfg.ViewsDimensionsTopN, config.DefaultViewsDimensionsTopN,
			"values per views breakdown and kinds, per project and day; the rest fold into (other)"},
		{settingAttrs, cfg.ProductAttributesTopN, config.DefaultProductAttributesTopN,
			"values per attribute key, per project, day and event; the rest fold into (other)"},
		{settingIdentities, cfg.IdentitiesTopN, config.DefaultIdentitiesTopN,
			"users, and groups, per project and day; the rest are dropped"},
	}
}

func (h *host) listLimits(_ context.Context, _ struct{}) (limitsOut, error) {
	return limitsOut{Limits: h.limits}, nil
}

// capOf is the cap in force for a setting; 0 (no cap) for an unknown one.
func (h *host) capOf(setting string) int {
	for _, l := range h.limits {
		if l.Setting == setting {
			return l.Value
		}
	}
	return 0
}
