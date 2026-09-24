package server

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Wire limits.
const (
	maxBatchEvents = 500
	maxAttrs       = 50
	maxAttrKey     = 64
	maxAttrValue   = 512
	maxNotices     = 10
	futureSkew     = 5 * time.Minute
)

// Reserved event names. Both are views; the name only supplies the default
// kind. $pageview is the pre-views spelling every deployed tag still sends,
// accepted silently and never documented. The namespace is open but
// reserved: `$` belongs to the server, but an unrecognized `$` name is
// stored as an ordinary custom event rather than rejected. Rejecting would
// mean a client shipping a future reserved name against a not-yet-upgraded
// server receives a 4xx, which clients treat as a poison batch to drop —
// permanent data loss in exactly the window forward compatibility matters.
const (
	namePageView   = "$page_view"
	nameScreenView = "$screen_view"
	aliasPageview  = "$pageview"
)

// kindPattern bounds a client-declared $kind so a typo cannot mint a
// dimension value; an invalid kind falls back to the name's default.
var kindPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,15}$`)

// viewName reports whether name is a view and its default kind.
func viewName(name string) (kind string, ok bool) {
	switch name {
	case namePageView, aliasPageview:
		return "web", true
	case nameScreenView:
		return "app", true
	}
	return "", false
}

type envelope struct {
	Key        string         `json:"key"`
	Attributes map[string]any `json:"attributes"`
	Events     []rawEvent     `json:"events"`
}

type rawEvent struct {
	ID         string         `json:"id"`
	TS         string         `json:"ts"`
	Name       string         `json:"name"`
	Attributes map[string]any `json:"attributes"`
}

type notice struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
}

type ingestResult struct {
	Accepted int      `json:"accepted"`
	Rejected int      `json:"rejected"`
	Errors   []notice `json:"errors"`
	Warnings []notice `json:"warnings"`
}

// reject records a per-event failure. Rejection is per event, never per
// batch: one malformed event must not poison a 500-event offline replay.
func (res *ingestResult) reject(i int, format string, a ...any) {
	res.Rejected++
	if len(res.Errors) < maxNotices {
		res.Errors = append(res.Errors, notice{Index: i, Reason: fmt.Sprintf(format, a...)})
	}
}

// warn records something ignored rather than refused. This is what keeps
// "ignore unknown $" from meaning "silently discard": during integration the
// mistake shows up in the response body.
func (res *ingestResult) warn(i int, format string, a ...any) {
	if len(res.Warnings) < maxNotices {
		res.Warnings = append(res.Warnings, notice{Index: i, Reason: fmt.Sprintf(format, a...)})
	}
}

// resolved is the reserved half of an event's attributes split into typed
// fields, plus whatever ordinary attributes remain. Environment fields
// hold what the client sent; the handler validates them.
type resolved struct {
	InstallID, UserID, UserName   string
	GroupID, GroupName, SessionID string
	Kind, Platform, OS, OSVersion string
	OSName, AppVersion            string
	Browser, BrowserVersion       string
	Device, DeviceModel, Locale   string
	Host, Path, Referrer, Screen  string
	UTMSource, UTMMedium          string
	UTMCampaign                   string
	displayWidthRaw               string
	displayHeightRaw              string
	consentRaw                    string
	Custom                        map[string]string
}

// reservedKeys maps every system-defined attribute key to its destination.
// Location attributes are stored verbatim: the client owns normalization
// (masking, routing mode), so the server does no URL parsing at all. That
// is what lets a site report /account/[id]/edit without the raw path ever
// leaving the browser. $screen is a fallback for path, resolved in
// handleEvents when $path is absent. The environment keys are declared by
// the client and only validated here; the User-Agent is never a source
// for any of them.
var reservedKeys = map[string]func(*resolved, string){
	"$install_id":      func(r *resolved, v string) { r.InstallID = v },
	"$user_id":         func(r *resolved, v string) { r.UserID = v },
	"$user_name":       func(r *resolved, v string) { r.UserName = v },
	"$group_id":        func(r *resolved, v string) { r.GroupID = v },
	"$group_name":      func(r *resolved, v string) { r.GroupName = v },
	"$session_id":      func(r *resolved, v string) { r.SessionID = v },
	"$consent":         func(r *resolved, v string) { r.consentRaw = v },
	"$kind":            func(r *resolved, v string) { r.Kind = v },
	"$platform":        func(r *resolved, v string) { r.Platform = v },
	"$os":              func(r *resolved, v string) { r.OS = v },
	"$os_version":      func(r *resolved, v string) { r.OSVersion = v },
	"$os_name":         func(r *resolved, v string) { r.OSName = v },
	"$browser":         func(r *resolved, v string) { r.Browser = v },
	"$browser_version": func(r *resolved, v string) { r.BrowserVersion = v },
	"$device":          func(r *resolved, v string) { r.Device = v },
	"$app_version":     func(r *resolved, v string) { r.AppVersion = v },
	"$device_model":    func(r *resolved, v string) { r.DeviceModel = v },
	"$locale":          func(r *resolved, v string) { r.Locale = v },
	"$host":            func(r *resolved, v string) { r.Host = v },
	"$path":            func(r *resolved, v string) { r.Path = v },
	"$utm_source":      func(r *resolved, v string) { r.UTMSource = v },
	"$utm_medium":      func(r *resolved, v string) { r.UTMMedium = v },
	"$utm_campaign":    func(r *resolved, v string) { r.UTMCampaign = v },
	"$referrer":        func(r *resolved, v string) { r.Referrer = v },
	"$screen":          func(r *resolved, v string) { r.Screen = v },
	"$display_width":   func(r *resolved, v string) { r.displayWidthRaw = v },
	"$display_height":  func(r *resolved, v string) { r.displayHeightRaw = v },
}

// mergeAttributes layers per-event attributes over batch defaults, key by
// key. This is the only merge rule, and it applies to system and ordinary
// keys alike — which is what lets an offline queue spanning an app
// self-update stamp $app_version on just the events that differ, instead of
// grouping the queue by context before flushing.
//
// Neither input is mutated: batch defaults are reused across every event.
func mergeAttributes(batch, event map[string]any) map[string]any {
	out := make(map[string]any, len(batch)+len(event))
	for k, v := range batch {
		out[k] = v
	}
	for k, v := range event {
		out[k] = v
	}
	return out
}

// resolveAttributes splits merged attributes into typed reserved fields and
// ordinary attributes, returning the unknown `$` keys it dropped. Unknown
// reserved keys are dropped rather than stored: the `$` namespace is
// reserved for system fields, so a `$` key this server does not recognize
// is a client bug, and silently storing it as data would hide that.
func resolveAttributes(m map[string]any) (resolved, []string) {
	r := resolved{Custom: map[string]string{}}
	var unknown []string
	for k, v := range m {
		if strings.HasPrefix(k, "$") {
			set, ok := reservedKeys[k]
			if !ok {
				unknown = append(unknown, k)
				continue
			}
			set(&r, truncate(stringify(v), maxAttrValue))
			continue
		}
		if len(k) > maxAttrKey || len(r.Custom) >= maxAttrs {
			continue
		}
		r.Custom[k] = truncate(stringify(v), maxAttrValue)
	}
	return r, unknown
}

// stringify renders a JSON scalar the way the attributes blob stores it.
// Numbers decode as float64, so integral values must print without a
// decimal point or "3" would become "3.000000".
func stringify(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprint(t)
	}
}

// parseDisplay turns a raw pixel string into an int, reporting whether the
// value was present but unusable (non-integer or <= 0) so the caller can
// warn. Absent → 0, no warning.
func parseDisplay(raw string) (int, bool) {
	if raw == "" {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 {
		return 0, true
	}
	return n, false
}

// parseConsent reads a declared $consent, trimmed and case-folded. Absent
// (or null, or "") is unknown and not a mistake; a value outside the
// accepted spellings is unknown too, and bad reports it so the handler
// warns. Never a rejection: a client sending a value this server does not
// know must not be handed a 4xx, which the retry rules treat as poison.
func parseConsent(raw string) (c store.Consent, bad bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return store.ConsentUnknown, false
	case "1", "true":
		return store.ConsentGiven, false
	case "0", "false":
		return store.ConsentNone, false
	}
	return store.ConsentUnknown, true
}

// declared runs one environment validator and warns when the value was
// present but unrecognised, so the mistake surfaces in the response body
// during integration instead of becoming a quiet other months later. A
// deliberate other or unknown is recognised by every validator, so a
// client that means it is never warned.
func (res *ingestResult) declared(i int, key, raw string, normalize func(string) (string, bool)) string {
	v, ok := normalize(raw)
	if !ok {
		res.warn(i, "%s %q is not a known value, stored as %s", key, raw, v)
	}
	return v
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// clampTS bounds a client timestamp to [received-maxAge, received+skew].
// Out-of-range values are clamped and counted, never dropped: a device with
// a broken clock still contributes. The lower bound is tied to the project's
// views raw window, which is what guarantees a clamped event can never
// target a day that has already been aggregated and had its raw rows
// deleted.
func clampTS(client, received time.Time, maxAge time.Duration) (time.Time, bool) {
	if client.IsZero() {
		return received, false
	}
	if oldest := received.Add(-maxAge); client.Before(oldest) {
		return oldest, true
	}
	if client.After(received.Add(futureSkew)) {
		return received, true
	}
	return client, false
}
