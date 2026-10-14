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
// (*archive.Reader).
type Attributor interface {
	Attribute(ctx context.Context, ls []archive.Lookup, days int, rules archive.Rules) ([]archive.Found, error)
}

// EventLog appends events to the archive log (*archive.Log): a
// submission's event goes to SQLite and the files alike.
type EventLog interface {
	WriteEvents(ctx context.Context, evs []store.Event) error
}

// attributionDelay is how old a submission is before its lookup, so the
// views sent just before it are on file: the SDK flushes every 10
// seconds, the pipeline every 5.
const attributionDelay = time.Minute

// attributionGiveUp is how long a submission waits for a lookup that
// works: past it, it is attributed with what its client sent alone, so a
// broken engine costs attribution, never the conversion.
const attributionGiveUp = time.Hour

// attributionTimeout bounds one lookup: the minute tick also runs the
// hour-0 salt rotation and the hour-3 daily pass, so a hung engine, or
// engine slots all held by console queries, must not hold it. A timeout is
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

// WithAttribution makes the runner attribute form submissions each
// minute, looking them up through a and writing their events to log too.
// Only a process that ingests has both.
func (r *Runner) WithAttribution(a Attributor, log EventLog) *Runner {
	r.attributor, r.eventLog = a, log
	return r
}

// attributeSubmissions attributes the submissions at least
// attributionDelay old, a batch at a time (attributeBatch) while full
// batches keep coming and attributionTickBudget lasts. A failed lookup
// ends the tick and is retried the next minute; the submissions past
// attributionGiveUp are attributed without it, counted in one error line.
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
}

// attributeBatch attributes up to attributionBatch pending submissions:
// one lookup for all of them, one transaction each, then the written
// events to the log. n is how many were pending; more is false when the
// next batch would not move on: the read or the lookup failed, or a
// submission stayed pending (its write failed), so the next read would
// return it again. givenUp counts the submissions attributed without a
// lookup.
func (r *Runner) attributeBatch(ctx context.Context, now time.Time, givenUp *int) (n int, more bool) {
	pending, err := r.store.PendingSubmissions(ctx, now.Add(-attributionDelay), attributionBatch)
	if err != nil {
		// Unreadable rows (named in err) are skipped; the rest go on.
		r.logger.Error("form attribution: pending submissions", "error", err)
		if len(pending) == 0 {
			return 0, false
		}
	}
	if len(pending) == 0 {
		return 0, false
	}
	ls := make([]archive.Lookup, len(pending))
	for i, p := range pending {
		ls[i] = archive.Lookup{ProjectID: p.ProjectID, ID: p.ID, ActorKind: p.Event.ActorKind,
			ActorID: p.Event.ActorID, InstallID: p.Event.InstallID, ConnectionID: p.ConnectionID, At: p.ReceivedAt}
	}
	events, attrs := r.reg.Snapshot(ctx).RuleLists()
	lookupCtx, cancel := context.WithTimeout(ctx, r.attributionTimeout)
	found, err := r.attributor.Attribute(lookupCtx, ls, r.cfg.Forms.AttributionDays, archive.Rules{Events: events, Attributes: attrs})
	cancel()
	more = err == nil
	if err != nil {
		r.logger.Warn("form attribution: lookup failed, retrying next minute", "submissions", len(pending), "error", err)
	}
	key := func(project int64, id string) string { return strconv.FormatInt(project, 10) + "/" + id }
	byKey := make(map[string]archive.Found, len(found))
	for _, f := range found {
		byKey[key(f.ProjectID, f.ID)] = f
	}
	var written []store.Event
	for _, p := range pending {
		f, ok := byKey[key(p.ProjectID, p.ID)]
		if !ok && now.Sub(p.ReceivedAt) < attributionGiveUp {
			more = false
			continue
		}
		ev := attributed(p.Event, f)
		wrote, err := r.store.AttributeSubmission(ctx, p, ev)
		if err != nil {
			r.logger.Error("form attribution: write", "project", p.ProjectID, "submission", p.ID, "error", err)
			more = false
			continue
		}
		if !ok {
			*givenUp++
		}
		if wrote {
			written = append(written, ev)
		}
	}
	if len(written) > 0 && r.eventLog != nil {
		if err := r.eventLog.WriteEvents(ctx, written); err != nil {
			r.logger.Error("form attribution: events not logged", "events", len(written), "error", err)
		}
	}
	return len(pending), more
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
