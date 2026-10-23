package jobs

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Attributor finds where submissions came from in the event files
// (*native.Engine).
type Attributor interface {
	Attribute(ctx context.Context, ls []archive.Lookup, days int, rules archive.Rules) ([]archive.Found, error)
}

// EventLog appends events to the archive log (*archive.Log): a
// submission's event goes to the files alone. A line written
// twice is harmless: the log drops a repeated id for ten minutes; after
// that the dump drops it within a log, and the month merge across a
// day's segments.
type EventLog interface {
	WriteEvents(ctx context.Context, evs []store.Event) error
}

// attributionDelay is how old a submission is before its lookup, so the
// views sent just before it are on file: the SDK flushes every 10
// seconds, the pipeline every 5.
const attributionDelay = time.Minute

// attributionGiveUp is how long a submission waits for a lookup that
// works: past it, it is attributed with what its client sent alone, so a
// broken lookup costs attribution, never the conversion.
const attributionGiveUp = time.Hour

// attributionTimeout bounds one lookup: the minute tick also runs the
// hour-0 salt rotation and the hour-3 daily pass, so a lookup waiting for
// a native scan turn (MaxScans, shared with the console's structured
// queries) or slowed by a large log must not hold it. A timeout is
// a failed lookup: retried next minute, given up after attributionGiveUp.
const attributionTimeout = 30 * time.Second

// attributionBatch bounds the submissions one lookup asks about.
const attributionBatch = 100

// attributionTickBudget bounds one minute's attribution: batch after batch
// while full ones keep coming, so a flood of submissions drains in a few
// minutes without holding the tick (which also runs the salt rotation
// and the daily pass). The batch it runs out in is finished; the rest
// wait for the next minute.
const attributionTickBudget = 30 * time.Second

// outboxPage bounds the events one flush of the outbox reads and logs in
// one write.
const outboxPage = 1000

// WithAttribution makes the runner attribute form submissions each
// minute, looking them up through a and writing their events to log too
// (nil: they stay in the outbox). Only a process that ingests has both.
func (r *Runner) WithAttribution(a Attributor, log EventLog) *Runner {
	r.attributor, r.eventLog = a, log
	return r
}

// attributeSubmissions attributes the submissions at least
// attributionDelay old, a batch at a time (attributeBatch) while full
// batches keep coming and attributionTickBudget lasts. A failed lookup
// ends the tick and is retried the next minute; the submissions past
// attributionGiveUp are attributed without it, counted in one error line.
// Then it flushes the outbox to the event log (flushOutbox).
func (r *Runner) attributeSubmissions(ctx context.Context) {
	if r.attributor == nil {
		return
	}
	now := r.now().UTC()
	started := time.Now()
	givenUp := 0
	for {
		n, more := r.attributeBatch(ctx, now, &givenUp)
		if !more || n < attributionBatch || time.Since(started) >= r.attributionTickBudget || ctx.Err() != nil {
			break
		}
	}
	if givenUp > 0 {
		r.logger.Error(fmt.Sprintf("form attribution: %d submissions given up after an hour without a lookup; attributed with what their clients sent", givenUp),
			"submissions", givenUp)
	}
	r.flushOutbox(ctx, started)
}

// flushOutbox appends the events AttributeSubmission left in the outbox to
// the event log, a page at a time while full pages keep coming and the
// tick's budget (from started) lasts, and marks each page logged once it
// is written: delivery at least once, so a crash or a failed write
// between the outbox and the files is retried the next minute, never lost.
// A page is written whole, so a write that fails for any one project or
// day holds back every project's form events until it clears, retried
// each minute; its likely causes (a full disk, permissions) are global
// anyway. The error line reports the backlog. An event that does not
// read is logged as an error and marked with its page. Without a log,
// the events wait.
func (r *Runner) flushOutbox(ctx context.Context, started time.Time) {
	if r.eventLog == nil {
		return
	}
	for {
		us, err := r.store.UnloggedEvents(ctx, outboxPage)
		if err != nil {
			r.logger.Error("form attribution: unlogged events", "error", err)
			return
		}
		if len(us) == 0 {
			return
		}
		evs := make([]store.Event, 0, len(us))
		for _, u := range us {
			if u.Err != nil {
				r.logger.Error("form attribution: unreadable unlogged event; kept in the outbox only",
					"project", u.ProjectID, "submission", u.ID, "error", u.Err)
				continue
			}
			evs = append(evs, u.Event)
		}
		if len(evs) > 0 {
			if err := r.eventLog.WriteEvents(ctx, evs); err != nil {
				args := []any{"events", len(evs), "error", err}
				if backlog, cerr := r.store.UnloggedCount(ctx); cerr == nil {
					args = append(args, "backlog", backlog)
				}
				r.logger.Error("form attribution: events not logged, retrying next minute", args...)
				return
			}
		}
		if err := r.store.MarkLogged(ctx, us); err != nil {
			// Logged but still unlogged: the next minute writes them again,
			// which the log and the dump drop by id.
			r.logger.Error("form attribution: mark logged", "events", len(us), "error", err)
			return
		}
		if len(us) < outboxPage || time.Since(started) >= r.attributionTickBudget || ctx.Err() != nil {
			return
		}
	}
}

// attributeBatch attributes up to attributionBatch pending submissions:
// one lookup for all of them, one transaction each, which leaves the
// written events in the outbox. n is how many were pending; more is false when the
// next batch would not move on: the read or the lookup failed, or a
// submission stayed pending (its write failed), so the next read would
// return it again. givenUp counts the submissions attributed without a
// lookup. A submission whose stored event does not read is logged and
// cleared (clearUnreadable), never looked up.
func (r *Runner) attributeBatch(ctx context.Context, now time.Time, givenUp *int) (n int, more bool) {
	pending, err := r.store.PendingSubmissions(ctx, now.Add(-attributionDelay), attributionBatch)
	if err != nil {
		r.logger.Error("form attribution: pending submissions", "error", err)
		return 0, false
	}
	if len(pending) == 0 {
		return 0, false
	}
	more = true
	ls := make([]archive.Lookup, 0, len(pending))
	for _, p := range pending {
		if p.Err == nil {
			ls = append(ls, archive.Lookup{ProjectID: p.ProjectID, ID: p.ID, ActorKind: p.Event.ActorKind,
				ActorID: p.Event.ActorID, InstallID: p.Event.InstallID, ConnectionID: p.ConnectionID, At: p.ReceivedAt})
		}
	}
	var found []archive.Found
	if len(ls) > 0 {
		events, attrs := r.reg.Snapshot(ctx).RuleLists()
		lookupCtx, cancel := context.WithTimeout(ctx, r.attributionTimeout)
		found, err = r.attributor.Attribute(lookupCtx, ls, r.cfg.Forms.AttributionDays, archive.Rules{Events: events, Attributes: attrs})
		cancel()
		if err != nil {
			more = false
			r.logger.Warn("form attribution: lookup failed, retrying next minute", "submissions", len(ls), "error", err)
		}
	}
	key := func(project int64, id string) string { return strconv.FormatInt(project, 10) + "/" + id }
	byKey := make(map[string]archive.Found, len(found))
	for _, f := range found {
		byKey[key(f.ProjectID, f.ID)] = f
	}
	for _, p := range pending {
		if p.Err != nil {
			if !r.clearUnreadable(ctx, p) {
				more = false
			}
			continue
		}
		f, ok := byKey[key(p.ProjectID, p.ID)]
		if !ok && now.Sub(p.ReceivedAt) < attributionGiveUp {
			more = false
			continue
		}
		ev := attributed(p.Event, f)
		if _, err := r.store.AttributeSubmission(ctx, p, ev); err != nil {
			r.logger.Error("form attribution: write", "project", p.ProjectID, "submission", p.ID, "error", err)
			more = false
			continue
		}
		if !ok {
			*givenUp++
		}
	}
	return len(pending), more
}

// clearUnreadable takes a submission whose stored event does not read
// out of the queue: it is logged once, as an error, and attributed with
// nothing and no event, keeping its fields. Left pending, it would come
// back every minute, take a batch slot each time and log again. ok is
// false when the clearing write failed (logged; retried next minute).
func (r *Runner) clearUnreadable(ctx context.Context, p store.PendingSubmission) (ok bool) {
	r.logger.Error("form attribution: unreadable submission event; kept without attribution or event",
		"project", p.ProjectID, "submission", p.ID, "error", p.Err)
	p.Event, p.WriteEvent = store.Event{}, false
	if _, err := r.store.AttributeSubmission(ctx, p, store.Event{}); err != nil {
		r.logger.Error("form attribution: write", "project", p.ProjectID, "submission", p.ID, "error", err)
		return false
	}
	return true
}

// attributed is a submission's event with what the files found: the
// newest view's kind, group, session id, environment and consent, and,
// unless the client sent a referrer or a UTM itself, the source.
func attributed(ev store.Event, f archive.Found) store.Event {
	n := f.Newest
	ev.Kind, ev.GroupID, ev.SessionID = n.Kind, n.GroupID, n.SessionID
	ev.Platform, ev.OS, ev.OSVersion, ev.OSName = n.Platform, n.OS, n.OSVersion, n.OSName
	ev.Browser, ev.BrowserVersion, ev.BrowserLocale = n.Browser, n.BrowserVersion, n.BrowserLocale
	ev.AppVersion, ev.AppLocale, ev.Device, ev.DeviceModel = n.AppVersion, n.AppLocale, n.Device, n.DeviceModel
	ev.DisplayWidth, ev.DisplayHeight, ev.Consent = n.DisplayWidth, n.DisplayHeight, n.Consent
	if ev.ReferrerSource == "" && ev.UTMSource == "" && ev.UTMMedium == "" && ev.UTMCampaign == "" {
		s := f.Source
		ev.ReferrerSource, ev.UTMSource, ev.UTMMedium, ev.UTMCampaign = s.ReferrerSource, s.UTMSource, s.UTMMedium, s.UTMCampaign
	}
	return ev
}
