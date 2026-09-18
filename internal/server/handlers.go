package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/enrich"
	"github.com/dmtrkzntsv/twillingate/internal/identity"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
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
// views to the views table, everything else to product_events.
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
	p, label, ok := s.reg.Snapshot(r.Context()).ProjectByKey(key)
	if !ok {
		// One auth outcome. Because the key resolves the project, there is
		// no unknown-project case to keep indistinguishable from a bad key.
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Origin only deters browser-based abuse, since a scripted client can
	// spoof or omit it — which is exactly the case worth keeping. Native
	// apps send none and are unaffected; Electron and Tauri renderers add
	// their scheme to allowed_origins.
	if origin := r.Header.Get("Origin"); origin != "" && !s.originAllowed(w, r, p.Alias) {
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
	maxAge := s.cfg.MaxEventAge()

	var res ingestResult
	var names []store.Identity
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

		actor, actorKind, user, group := resolveIdentity(p, rv, salt, ip, ua)
		names = append(names, identityNames(p, rv)...)

		defaultKind, isView := viewName(ev.Name)
		if !isView {
			if strings.HasPrefix(ev.Name, "$") {
				res.warn(i, "unknown reserved name %s, stored as a custom event", ev.Name)
			}
			s.queue.EnqueueEvent(store.ProductEvent{
				ID: id, Project: p.Alias, EventName: ev.Name,
				TS: ts, ReceivedAt: received,
				ActorID: actor, ActorKind: actorKind, UserID: user, GroupID: group,
				OS: enrich.NormalizeOS(rv.OS), AppVersion: rv.AppVersion,
				Attributes: rv.Custom,
			})
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
		v := store.View{
			ID: id, Project: p.Alias, TS: ts, ReceivedAt: received, Kind: kind,
			ActorID: actor, ActorKind: actorKind, UserID: user, GroupID: group, SessionID: rv.SessionID,
			Host: rv.Host, Path: path,
			UTMSource: rv.UTMSource, UTMMedium: rv.UTMMedium, UTMCampaign: rv.UTMCampaign,
			OSVersion: rv.OSVersion, AppVersion: rv.AppVersion,
			DeviceModel: rv.DeviceModel, Locale: rv.Locale, Country: country,
		}
		// Only web rows are enriched from the connection: the User-Agent
		// names the browser, OS and device class, and a crawler is dropped.
		// Every other kind declares its own environment and is never
		// filtered, whatever HTTP library it uses.
		if kind == "web" {
			if botUA {
				// Accepted and silently ignored: the client did nothing
				// wrong, so it must not retry.
				res.Accepted++
				continue
			}
			v.Device, v.Browser, v.BrowserVersion, v.OS = enrich.ParseUserAgent(ua)
			v.ReferrerSource = enrich.CleanReferrer(rv.Referrer, rv.Host)
		} else {
			// No host to compare against, so a referrer is taken at face
			// value — a deep link can still carry one.
			v.ReferrerSource = enrich.CleanReferrer(rv.Referrer, "")
		}
		// Declared environment overrides whatever was parsed.
		if rv.OS != "" {
			v.OS = enrich.NormalizeOS(rv.OS)
		}
		var bad bool
		if v.DisplayWidth, bad = parseDisplay(rv.displayWidthRaw); bad {
			res.warn(i, "$display_width %q is not a positive integer, ignored", rv.displayWidthRaw)
		}
		if v.DisplayHeight, bad = parseDisplay(rv.displayHeightRaw); bad {
			res.warn(i, "$display_height %q is not a positive integer, ignored", rv.displayHeightRaw)
		}
		s.queue.EnqueueView(v)
		res.Accepted++
	}

	if names = dedupeIdentities(names, p.Alias); len(names) > 0 {
		if err := s.names.UpsertIdentities(r.Context(), names); err != nil {
			s.logger.Error("identity upsert failed", "error", err)
		}
	}
	s.counters.record(label, res.Accepted, res.Rejected)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	if err := json.NewEncoder(w).Encode(res); err != nil {
		s.logger.Error("write response", "error", err)
	}
}

// resolveIdentity applies the project's identity mode. anonymous salts and
// rotates whatever identifier the client supplied; identified stores it as
// given. actorKind records how the actor id was derived (user, install or
// connection) regardless of identity mode, so cohorts can be built on it
// later.
//
// group_id stays raw in both modes: it identifies an organization, not a
// natural person, and hashing it would make dashboards unreadable for no
// real privacy gain.
func resolveIdentity(p *manage.Project, rv resolved, salt, ip, ua string) (actor, actorKind, user, group string) {
	raw := rv.UserID
	actorKind = store.ActorUser
	if raw == "" {
		raw = rv.InstallID
		actorKind = store.ActorInstall
	}
	if raw == "" {
		actorKind = store.ActorConnection
	}
	if p.Identity == config.IdentityIdentified {
		actor = raw
		if actor == "" {
			// No client identifier at all: fall back to the rotating hash
			// rather than dropping the event.
			actor = identity.VisitorHash(salt, ip, ua, p.Alias)
		}
		return actor, actorKind, rv.UserID, rv.GroupID
	}
	if raw == "" {
		actor = identity.VisitorHash(salt, ip, ua, p.Alias)
	} else {
		actor = identity.ActorHash(salt, raw, p.Alias)
	}
	if rv.UserID != "" {
		user = identity.ActorHash(salt, rv.UserID, p.Alias)
	}
	return actor, actorKind, user, rv.GroupID
}

// identityNames collects display names to upsert.
//
// $user_name is ignored in anonymous mode: storing a person's name against a
// hash that rotates daily would both defeat the anonymisation and accumulate
// a fresh row per user per day. $group_name is kept in both modes, on the
// same reasoning that keeps group_id raw.
func identityNames(p *manage.Project, rv resolved) []store.Identity {
	var out []store.Identity
	if rv.GroupID != "" && rv.GroupName != "" {
		out = append(out, store.Identity{Kind: store.KindGroup, ID: rv.GroupID, Name: rv.GroupName})
	}
	if p.Identity == config.IdentityIdentified && rv.UserID != "" && rv.UserName != "" {
		out = append(out, store.Identity{Kind: store.KindUser, ID: rv.UserID, Name: rv.UserName})
	}
	return out
}

// dedupeIdentities collapses the repeats a batch-level name produces across
// every event, and stamps the project.
func dedupeIdentities(in []store.Identity, project string) []store.Identity {
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
		i.Project = project
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
