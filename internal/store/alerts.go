package store

import "encoding/json"

// An alert's state. Paused and snoozed are derived from the alert's own
// fields; the rest are what the last evaluation left.
const (
	AlertPending  = "pending"
	AlertOK       = "ok"
	AlertBreached = "breached"
	AlertNoData   = "no_data"
	AlertBroken   = "broken"
	AlertPaused   = "paused"
	AlertSnoozed  = "snoozed"
	AlertError    = "error" // an evaluation outcome only: a runtime failure that is retried
)

// AlertStates is every state an alert shows.
var AlertStates = []string{AlertPending, AlertOK, AlertBreached, AlertNoData, AlertBroken, AlertPaused, AlertSnoozed}

// AlertOutcomes is every outcome an evaluation records.
var AlertOutcomes = []string{AlertOK, AlertBreached, AlertNoData, AlertBroken, AlertError}

// AlertEvaluationDays is how many days of evaluations an alert keeps.
const AlertEvaluationDays = 400

// AlertRead says which number of the query an alert reads.
type AlertRead struct {
	Metric string `json:"metric,omitempty"`
	Volume string `json:"volume,omitempty"`
	Step   string `json:"step,omitempty"`
	From   string `json:"from,omitempty"` // start | previous
}

// AlertCondition is the line the number is held against.
type AlertCondition struct {
	Type      string   `json:"type"` // threshold | change | silence
	Op        string   `json:"op,omitempty"`
	Value     *float64 `json:"value,omitempty"`
	Direction string   `json:"direction,omitempty"`
	Percent   float64  `json:"percent,omitempty"`
	Against   string   `json:"against,omitempty"`
	Hours     int      `json:"hours,omitempty"`
}

// AlertDef is an alert's definition, stored as one JSON object (alerts.definition).
type AlertDef struct {
	Query      json.RawMessage   `json:"query"` // a native.Query, normalized by internal/alerts
	Read       AlertRead         `json:"read"`
	Condition  AlertCondition    `json:"condition"`
	WindowDays int               `json:"window_days"`
	MinVolume  float64           `json:"min_volume,omitempty"`
	Template   string            `json:"template,omitempty"`
	Fields     map[string]string `json:"fields,omitempty"`
}

// Alert is one alert row.
type Alert struct {
	ID, ProjectID                                               int64
	Name                                                        string
	Def                                                         AlertDef
	Paused                                                      bool
	SnoozedUntil                                                string // RFC 3339 UTC, "" none
	Revision                                                    int64
	State                                                       string // one of AlertStates but paused and snoozed (those are derived)
	Reason                                                      string
	LastValue                                                   *float64
	LastEvaluatedAt, LastDay                                    string
	Fails                                                       int
	RetryAt, CheckRequestedAt, CreatedAt, UpdatedAt, ArchivedAt string
}

// CheckPending reports whether a check was requested and not evaluated at or
// after the request. An evaluation in the request's own second counts as its
// answer.
func (a Alert) CheckPending() bool {
	return a.CheckRequestedAt != "" && (a.LastEvaluatedAt == "" || a.LastEvaluatedAt < a.CheckRequestedAt)
}

// AlertEdit is a partial edit of an alert: a nil field is left alone.
type AlertEdit struct {
	Name         *string
	Def          *AlertDef
	Paused       *bool
	SnoozedUntil *string // "" clears
}
