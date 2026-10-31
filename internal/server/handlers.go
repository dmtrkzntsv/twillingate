package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/enrich"
	"github.com/dmtrkzntsv/twillingate/internal/identity"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/internal/wire"
	"github.com/google/uuid"
)

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, wire.MaxBody)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return false
	}
	return true
}

func newID() string {
	id, err := uuid.NewV7()
	if err != nil { // only on entropy exhaustion; fall back to v4
		return uuid.NewString()
	}
	return id.String()
}

// handleEvents is the events endpoint (forms have their own, forms.go). It
// demultiplexes by the event's declared or inferred family
// (resolveFamily): views, product events and measures all land in the one
// raw table under their family.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	var env envelope
	if !decode(w, r, &env) {
		return
	}

	// Header preferred; body accepted because navigator.sendBeacon cannot
	// set custom headers, so browsers have no other option.
	key := r.Header.Get("X-Analytics-Key")
	if key == "" {
		key = env.Key
	}
	snap := s.reg.Snapshot(r.Context())
	p, label, ok := s.authorize(w, r, snap, key)
	if !ok {
		return
	}
	if len(env.Events) > wire.MaxBatchEvents {
		http.Error(w, "too many events", http.StatusRequestEntityTooLarge)
		return
	}

	salt, ok := s.currentSalt(w, r)
	if !ok {
		return
	}

	ip, ua := clientIP(r), r.Header.Get("User-Agent")
	b := &batch{
		snap: snap, projectID: p.ID, hashKey: strconv.FormatInt(p.ID, 10), attributes: env.Attributes,
		salt: salt, ip: ip, ua: ua, received: time.Now().UTC(), country: s.geo.Country(r, ip),
		// Bot filtering is a web concern, keyed on kind, not name: it applies
		// to the web kind only. Applying it to app traffic would drop every
		// client whose HTTP library sends a non-browser User-Agent.
		botUA: enrich.IsBot(ua),
		// The clamp is the late window (INGEST_LATE_DAYS), at most
		// RETENTION_DAYS: a clamped event can never target a day whose file
		// the daily pass already deleted.
		maxAge: s.cfg.MaxEventAge(),
		// Attribute rules (spec 2026-10-11, D9): one lookup per batch; a
		// project without rules pays nothing more per event than an
		// emptiness check.
		attrRules: snap.AttributeRules(p.ID),
	}

	// sawUser and sawInstall record an id kind seen on a row that is
	// actually stored, never on a row that is rejected or bot-filtered, so
	// a batch that stores nothing leaves no trace in the "project receives
	// ids" log. Display names ride on the events themselves (user_name,
	// group_name), which the event files' identities table reads (raw-first
	// D17).
	var sawUser, sawInstall bool
	for i, ev := range env.Events {
		e, keep := b.buildEvent(i, ev)
		if !keep {
			continue
		}
		s.queue.Enqueue(e)
		switch e.ActorKind {
		case store.ActorUser:
			sawUser = true
		case store.ActorInstall:
			sawInstall = true
		}
		b.res.Accepted++
	}

	s.counters.record(label, b.res.Accepted, b.res.Rejected)
	if sawUser {
		s.noteIDs(p.ID, store.ActorUser)
	}
	if sawInstall {
		s.noteIDs(p.ID, store.ActorInstall)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	if err := json.NewEncoder(w).Encode(b.res); err != nil {
		s.logger.Error("write response", "error", err)
	}
}

// authorize resolves key to its project and checks the request's Origin
// against it: the two plain refusals, 401 then 403, answered on w. ok is
// false when one was.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request, snap *manage.Snapshot, key string) (p *manage.Project, label string, ok bool) {
	p, label, ok = snap.ProjectByKey(key)
	if !ok {
		// One auth outcome. Because the key resolves the project, there is
		// no unknown-project case to keep indistinguishable from a bad key.
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, "", false
	}
	// Origin only deters browser-based abuse, since a scripted client can
	// spoof or omit it — which is exactly the case worth keeping. Native
	// apps send none and are unaffected; Electron and Tauri renderers add
	// their scheme to allowed_origins.
	if r.Header.Get("Origin") != "" && !s.originAllowed(w, r, p.ID) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return nil, "", false
	}
	return p, label, true
}

// currentSalt is today's salt, the connection hash's; ok is false when it
// cannot be read, after answering 500 on w.
func (s *Server) currentSalt(w http.ResponseWriter, r *http.Request) (salt string, ok bool) {
	salt, err := s.salt.Current(r.Context())
	if err != nil {
		s.logger.Error("salt unavailable", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return "", false
	}
	return salt, true
}

// batch is what the events of one request share, and the answer they add
// up to.
type batch struct {
	snap       *manage.Snapshot
	projectID  int64
	hashKey    string         // the project id as a decimal string (resolveIdentity)
	attributes map[string]any // the batch's default attributes
	salt       string
	ip, ua     string
	country    string
	botUA      bool
	received   time.Time
	maxAge     time.Duration
	attrRules  store.AttributeRuleSet
	// warnedBlocked is the blocked attribute keys warned about so far:
	// once per key per batch.
	warnedBlocked map[string]bool
	res           ingestResult
}

// buildEvent reads event i of the batch into its row, noting its
// warnings, in their contracted order, or its rejection on b.res. keep is
// false for an event with nothing to store: one rejected, or one accepted
// only to be dropped (a blocked event, a bot's view or measure), which
// buildEvent counts as accepted. A kept row is the caller's to count.
func (b *batch) buildEvent(i int, ev rawEvent) (e store.Event, keep bool) {
	res := &b.res
	rv, unknown, overlong := resolveAttributes(mergeAttributes(b.attributes, ev.Attributes))
	for _, k := range unknown {
		res.warn(i, "unknown reserved key %s, ignored", k)
	}
	for _, k := range overlong {
		// Named by its first 16 runes: the key itself can run to the body limit.
		res.warn(i, "attribute key %q is longer than %d characters, ignored", string([]rune(k)[:16])+"…", wire.MaxAttrKey)
	}

	if ev.Name == "" {
		res.reject(i, "name is required")
		return store.Event{}, false
	}
	id, err := eventID(ev.ID)
	if err != nil {
		res.reject(i, "%v", err)
		return store.Event{}, false
	}
	ts, clamped := clampTS(parseTS(ev.TS), b.received, b.maxAge)
	if clamped {
		res.warn(i, "timestamp out of range, clamped")
	}

	actor, actorKind, user, group := resolveIdentity(rv, b.salt, b.ip, b.ua, b.hashKey)

	// The environment is declared, validated and never parsed: the
	// User-Agent is read for nothing but the bot check below. Views and
	// product events keep the same keys.
	osv, osKnown := enrich.NormalizeOS(rv.OS)
	if !osKnown {
		res.warn(i, "$os %q is not a known value, stored as other", rv.OS)
	}
	platform := res.declared(i, "$platform", rv.Platform, enrich.NormalizePlatform)
	consent, badConsent := parseConsent(rv.consentRaw)
	if badConsent {
		res.warn(i, "unknown $consent value %q, ignored", rv.consentRaw)
	}

	family, name, famWarn, famReject := resolveFamily(ev)
	if famReject != "" {
		res.reject(i, "%s", famReject)
		return store.Event{}, false
	}
	if famWarn != "" {
		res.warn(i, "%s", famWarn)
	}

	// Event rules (spec 2026-10-11, D9): a renamed name is stored under
	// its new one; a blocked one is accepted, so the client does not
	// retry, and dropped, with a warning for whoever integrates.
	sent := name
	name, blocked := b.snap.ResolveEvent(b.projectID, family, name)
	if blocked {
		if name != sent {
			res.warn(i, "event %q (renamed to %q) is blocked in this project, dropped", sent, name)
		} else {
			res.warn(i, "event %q is blocked in this project, dropped", name)
		}
		res.Accepted++
		return store.Event{}, false
	}

	var value *float64
	sampleRate := 1.0
	measure := ""
	if family == store.FamilyMeasures {
		v, ok := parseValue(ev.Value)
		if !ok {
			res.reject(i, "measures require a value: a number from 0 to 1e15")
			return store.Event{}, false
		}
		// A non-string measure (a typo like {"measure":5}) parses as ""
		// here, which fails the Contains check exactly like an absent
		// or unknown one: same rejection, no special case needed.
		measureStr, _ := jsonString(ev.Measure)
		if !slices.Contains(store.MeasureKinds, measureStr) {
			res.reject(i, "measures require measure: time, size or number")
			return store.Event{}, false
		}
		value, measure = &v, measureStr
		rate, bad := parseSampleRate(rv.sampleRateRaw)
		if bad {
			res.warn(i, "$sample_rate %q is not in [0.0001, 1], stored as 1", rv.sampleRateRaw)
		}
		sampleRate = rate
	} else {
		if len(ev.Value) > 0 {
			res.warn(i, "value is only read on the measures family, ignored")
		}
		if len(ev.Measure) > 0 {
			res.warn(i, "measure is only read on the measures family, ignored")
		}
		if rv.sampleRateRaw != "" {
			res.warn(i, "$sample_rate is only read on the measures family, ignored")
		}
	}

	// A view's kind defaults from its name; a product event has no
	// default and keeps an empty kind unless it declares one. The
	// default is taken only for an actual view: a measure or product
	// event sharing a view's name (e.g. a mistyped family) must not
	// inherit "web", which drives host-relative referrer cleaning and
	// the bot filter.
	var defaultKind string
	if family == store.FamilyViews {
		defaultKind, _ = viewName(ev.Name)
	}
	kind := defaultKind
	if rv.Kind != "" {
		if kindPattern.MatchString(rv.Kind) {
			kind = rv.Kind
		} else {
			if family == store.FamilyViews {
				res.warn(i, "invalid $kind %q, using %q", rv.Kind, defaultKind)
			} else {
				res.warn(i, "invalid $kind %q, ignored", rv.Kind)
			}
		}
	}
	path := rv.Path
	if path == "" {
		path = rv.Screen
	}
	if family == store.FamilyViews && path == "" {
		res.reject(i, "view requires $path or $screen")
		return store.Event{}, false
	}
	// An unrecognised $os becomes other; the name it would erase is
	// kept in os_name so the bucket stays investigable. An explicit
	// $os_name always wins.
	osName := rv.OSName
	if osName == "" && !osKnown {
		osName = rv.OS
	}
	// Hoisted out of the literal below so the warning order is
	// explicit: $os, $platform, then $browser and $device.
	browser := res.declared(i, "$browser", rv.Browser, enrich.NormalizeBrowser)
	device := res.declared(i, "$device", rv.Device, enrich.NormalizeDevice)
	// Rules cover product and measures rows, never views. A renamed key
	// is stored under its target, a blocked one is stripped and warned
	// about once per key per batch.
	custom := rv.Custom
	if family != store.FamilyViews && !b.attrRules.Empty() {
		var dropped []string
		custom, dropped = store.MapAttributes(custom, b.attrRules)
		for _, k := range dropped {
			if !b.warnedBlocked[k] {
				if b.warnedBlocked == nil {
					b.warnedBlocked = map[string]bool{}
				}
				b.warnedBlocked[k] = true
				res.warn(i, "attribute %q is blocked in this project, dropped", k)
			}
		}
	}
	// A name is kept only beside the id it names: $user_name without
	// $user_id names nothing.
	var userName, groupName string
	if rv.UserID != "" {
		userName = rv.UserName
	}
	if rv.GroupID != "" {
		groupName = rv.GroupName
	}
	e = store.Event{
		ID: id, ProjectID: b.projectID, Family: family, EventName: name,
		TS: ts, ReceivedAt: b.received, Kind: kind,
		ActorID: actor, ActorKind: actorKind, UserID: user, GroupID: group, SessionID: rv.SessionID,
		InstallID: rv.InstallID, UserName: userName, GroupName: groupName,
		Host: rv.Host, Path: path,
		UTMSource: rv.UTMSource, UTMMedium: rv.UTMMedium, UTMCampaign: rv.UTMCampaign,
		Platform: platform, OS: osv, OSVersion: rv.OSVersion, OSName: osName,
		Browser: browser, BrowserVersion: rv.BrowserVersion,
		Device: device, DeviceModel: rv.DeviceModel,
		AppVersion: rv.AppVersion, AppLocale: rv.AppLocale, BrowserLocale: rv.BrowserLocale,
		Country: b.country, Consent: consent, Attributes: custom,
		Value: value, Measure: measure, SampleRate: sampleRate,
	}
	// Bot filtering is the one thing still read off the User-Agent,
	// and it applies to web views and web measures only (a crawler's
	// Web Vitals are not a visitor's): any other kind declares what it
	// is and is never filtered, whatever HTTP library it uses, so a
	// backend's measures (no kind) are kept. Product events are never
	// filtered.
	if kind == "web" {
		if (family == store.FamilyViews || family == store.FamilyMeasures) && b.botUA {
			// Accepted and silently ignored: the client did nothing
			// wrong, so it must not retry.
			res.Accepted++
			return store.Event{}, false
		}
		e.ReferrerSource = enrich.CleanReferrer(rv.Referrer, rv.Host)
	} else {
		// No host to compare against, so a referrer is taken at face
		// value — a deep link can still carry one.
		e.ReferrerSource = enrich.CleanReferrer(rv.Referrer, "")
	}
	var bad bool
	if e.DisplayWidth, bad = parseDisplay(rv.displayWidthRaw); bad {
		res.warn(i, "$display_width %q is not a positive integer, ignored", rv.displayWidthRaw)
	}
	if e.DisplayHeight, bad = parseDisplay(rv.displayHeightRaw); bad {
		res.warn(i, "$display_height %q is not a positive integer, ignored", rv.displayHeightRaw)
	}
	return e, true
}

// resolveIdentity picks the actor for a row: the client's $user_id, else
// its $install_id, else a hash of the connection under the daily salt.
// actorKind records which one it was, so cohorts can be built on it later.
// Ids are stored as sent: the served SDK's identity mode decides what is
// sent (docs/twillingate.md, Identity), and a client that posts by hand
// decides by what it posts.
//
// group_id stays raw: it identifies an organization, not a natural person.
//
// hashKey is the project id as a decimal string — the id never changes, so
// a project's hash input never changes.
func resolveIdentity(rv resolved, salt, ip, ua, hashKey string) (actor, actorKind, user, group string) {
	actor, actorKind = rv.UserID, store.ActorUser
	if actor == "" {
		actor, actorKind = rv.InstallID, store.ActorInstall
	}
	if actor == "" {
		// No client identifier at all: fall back to the rotating hash
		// rather than dropping the event.
		actor, actorKind = identity.VisitorHash(salt, ip, ua, hashKey), store.ActorConnection
	}
	return actor, actorKind, rv.UserID, rv.GroupID
}

// eventID validates a client-supplied UUID or generates one. Supplying an id
// is what makes an at-least-once retry safe: the write is INSERT OR IGNORE
// on the primary key (family, project_id, day, id), and a retry repeats all
// four, so a replayed batch is a no-op. The one exception: an event whose ts
// is clamped (clampTS in ingest.go) and whose retry lands on the other side
// of midnight gets a different day and is stored twice. That gap is
// documented in the store's write.go and deploy/UPGRADES.md (migration 023).
func eventID(id string) (string, error) {
	if id == "" {
		return newID(), nil
	}
	if _, err := uuid.Parse(id); err != nil {
		return "", fmt.Errorf("id is not a valid UUID")
	}
	return id, nil
}

func parseTS(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}
