package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/enrich"
	"github.com/dmtrkzntsv/twillingate/internal/identity"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/google/uuid"
)

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
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

// handleEvents is the only ingest endpoint. It demultiplexes by event name:
// views to the views table, everything else to events.
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
	p, label, ok := snap.ProjectByKey(key)
	if !ok {
		// One auth outcome. Because the key resolves the project, there is
		// no unknown-project case to keep indistinguishable from a bad key.
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	hashKey := strconv.FormatInt(p.ID, 10)
	// Origin only deters browser-based abuse, since a scripted client can
	// spoof or omit it — which is exactly the case worth keeping. Native
	// apps send none and are unaffected; Electron and Tauri renderers add
	// their scheme to allowed_origins.
	if origin := r.Header.Get("Origin"); origin != "" && !s.originAllowed(w, r, p.ID) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	if len(env.Events) > maxBatchEvents {
		http.Error(w, "too many events", http.StatusRequestEntityTooLarge)
		return
	}

	salt, err := s.salt.Current(r.Context())
	if err != nil {
		s.logger.Error("salt unavailable", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	received := time.Now().UTC()
	ip, ua := clientIP(r), r.Header.Get("User-Agent")
	country := s.geo.Country(r, ip)
	// Bot filtering is a web concern, keyed on kind, not name: it applies to
	// the web kind only. Applying it to app traffic would drop every client
	// whose HTTP library sends a non-browser User-Agent.
	botUA := enrich.IsBot(ua)
	// Retention is global, so the clamp is the configured raw window: a
	// clamped event can never target a day the daily pass already
	// aggregated and deleted.
	maxAge := s.cfg.MaxEventAge()

	var res ingestResult
	var names []store.Identity
	var sawUser, sawInstall bool
	// noteRow records an id kind seen and any display name carried by a row
	// that is actually stored — called once beside each Enqueue call below,
	// never for a row that is rejected or bot-filtered, so a batch that
	// stores nothing leaves no trace in the identities table or the
	// "project receives ids" log.
	noteRow := func(actorKind string, rv resolved) {
		switch actorKind {
		case store.ActorUser:
			sawUser = true
		case store.ActorInstall:
			sawInstall = true
		}
		names = append(names, identityNames(rv)...)
	}
	for i, ev := range env.Events {
		rv, unknown := resolveAttributes(mergeAttributes(env.Attributes, ev.Attributes))
		for _, k := range unknown {
			res.warn(i, "unknown reserved key %s, ignored", k)
		}

		if ev.Name == "" {
			res.reject(i, "name is required")
			continue
		}
		id, err := eventID(ev.ID)
		if err != nil {
			res.reject(i, "%v", err)
			continue
		}
		ts, clamped := clampTS(parseTS(ev.TS), received, maxAge)
		if clamped {
			res.warn(i, "timestamp out of range, clamped")
		}

		actor, actorKind, user, group := resolveIdentity(rv, salt, ip, ua, hashKey)

		// The environment is declared, validated and never parsed: the
		// User-Agent is read for nothing but the bot check below. $os and
		// $platform land on views and product events alike; the rest are
		// views-only and are resolved and dropped on a product event.
		osv, osKnown := enrich.NormalizeOS(rv.OS)
		if !osKnown {
			res.warn(i, "$os %q is not a known value, stored as other", rv.OS)
		}
		platform := res.declared(i, "$platform", rv.Platform, enrich.NormalizePlatform)
		consent, badConsent := parseConsent(rv.consentRaw)
		if badConsent {
			res.warn(i, "unknown $consent value %q, ignored", rv.consentRaw)
		}

		defaultKind, isView := viewName(ev.Name)
		if !isView {
			if strings.HasPrefix(ev.Name, "$") {
				res.warn(i, "unknown reserved name %s, stored as a custom event", ev.Name)
			}
			s.queue.EnqueueEvent(store.ProductEvent{
				ID: id, ProjectID: p.ID, EventName: ev.Name,
				TS: ts, ReceivedAt: received,
				ActorID: actor, ActorKind: actorKind, UserID: user, GroupID: group,
				Platform: platform, OS: osv, AppVersion: rv.AppVersion,
				Consent:    consent,
				Attributes: rv.Custom,
			})
			noteRow(actorKind, rv)
			res.Accepted++
			continue
		}

		kind := defaultKind
		if rv.Kind != "" {
			if kindPattern.MatchString(rv.Kind) {
				kind = rv.Kind
			} else {
				res.warn(i, "invalid $kind %q, using %q", rv.Kind, defaultKind)
			}
		}
		path := rv.Path
		if path == "" {
			path = rv.Screen
		}
		if path == "" {
			res.reject(i, "view requires $path or $screen")
			continue
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
		v := store.View{
			ID: id, ProjectID: p.ID, TS: ts, ReceivedAt: received, Kind: kind,
			ActorID: actor, ActorKind: actorKind, UserID: user, GroupID: group, SessionID: rv.SessionID,
			Host: rv.Host, Path: path,
			UTMSource: rv.UTMSource, UTMMedium: rv.UTMMedium, UTMCampaign: rv.UTMCampaign,
			Platform: platform, OS: osv, OSVersion: rv.OSVersion, OSName: osName,
			Browser:        browser,
			BrowserVersion: rv.BrowserVersion,
			Device:         device,
			AppVersion:     rv.AppVersion, DeviceModel: rv.DeviceModel, Locale: rv.Locale, Country: country,
			Consent: consent,
		}
		// Bot filtering is the one thing still read off the User-Agent,
		// and it applies to web rows only: any other kind declares what
		// it is and is never filtered, whatever HTTP library it uses.
		if kind == "web" {
			if botUA {
				// Accepted and silently ignored: the client did nothing
				// wrong, so it must not retry.
				res.Accepted++
				continue
			}
			v.ReferrerSource = enrich.CleanReferrer(rv.Referrer, rv.Host)
		} else {
			// No host to compare against, so a referrer is taken at face
			// value — a deep link can still carry one.
			v.ReferrerSource = enrich.CleanReferrer(rv.Referrer, "")
		}
		var bad bool
		if v.DisplayWidth, bad = parseDisplay(rv.displayWidthRaw); bad {
			res.warn(i, "$display_width %q is not a positive integer, ignored", rv.displayWidthRaw)
		}
		if v.DisplayHeight, bad = parseDisplay(rv.displayHeightRaw); bad {
			res.warn(i, "$display_height %q is not a positive integer, ignored", rv.displayHeightRaw)
		}
		s.queue.EnqueueView(v)
		noteRow(actorKind, rv)
		res.Accepted++
	}

	if names = dedupeIdentities(names, p.ID); len(names) > 0 {
		if err := s.names.UpsertIdentities(r.Context(), names); err != nil {
			s.logger.Error("identity upsert failed", "error", err)
		}
	}
	s.counters.record(label, res.Accepted, res.Rejected)
	if sawUser {
		s.noteIDs(p.ID, store.ActorUser)
	}
	if sawInstall {
		s.noteIDs(p.ID, store.ActorInstall)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	if err := json.NewEncoder(w).Encode(res); err != nil {
		s.logger.Error("write response", "error", err)
	}
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

// identityNames collects display names to upsert. A name is kept only
// beside the id it names: $user_name without $user_id names nothing.
func identityNames(rv resolved) []store.Identity {
	var out []store.Identity
	if rv.GroupID != "" && rv.GroupName != "" {
		out = append(out, store.Identity{Kind: store.KindGroup, ID: rv.GroupID, Name: rv.GroupName})
	}
	if rv.UserID != "" && rv.UserName != "" {
		out = append(out, store.Identity{Kind: store.KindUser, ID: rv.UserID, Name: rv.UserName})
	}
	return out
}

// dedupeIdentities collapses the repeats a batch-level name produces across
// every event, and stamps the project.
func dedupeIdentities(in []store.Identity, projectID int64) []store.Identity {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, i := range in {
		k := i.Kind + "\x00" + i.ID
		if seen[k] {
			continue
		}
		seen[k] = true
		i.ProjectID = projectID
		out = append(out, i)
	}
	return out
}

// eventID validates a client-supplied UUID or generates one. Supplying an id
// is what makes an at-least-once retry safe: the write is INSERT OR IGNORE
// on this primary key, so a replayed batch is a no-op.
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
