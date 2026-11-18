// Package inbox turns a stored notification into the message the inbox
// shows and, led by an emoji and the project, the one a channel sends.
package inbox

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/notify"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

const timeLayout = "2006-01-02 15:04 UTC"

// Render is n as the inbox shows it: the title, and the details under it
// as a sentence. The project is not in it (the inbox shows it beside the
// item; Outgoing puts it in the title). Link is a console path; Absolute
// makes it a URL. It never reads form field values: a form is only ever
// named.
func Render(n store.Notification) notify.Message {
	m := notify.Message{Link: Path(n)}
	since := stamp(n.Fields["since"])
	origin, domain, form := subject(n, "origin"), subject(n, "domain"), subject(n, "form")
	errText := n.Fields["error"]
	lasted := span(n.Fields["since"], n.Fields["until"])
	sofar := elapsed(n.Fields["since"], n.CreatedAt)

	switch n.Type {
	case store.NotifyUptimeDown:
		m.Title = origin + " is down"
		m.Body = "Down since " + since + " (" + n.Fields["reason"] + ")."
	case store.NotifyUptimeStillDown:
		m.Title = origin + " is still down"
		m.Body = "Down for " + sofar + ", since " + since + "."
	case store.NotifyUptimeUp:
		m.Title = origin + " is up"
		m.Body = "Was down for " + lasted + ", since " + since + "."
	case store.NotifyCertFailing:
		m.Title = "Certificate problem on " + origin
		m.Body = "Failing since " + since + ": " + errText + "."
	case store.NotifyCertStillFailing:
		m.Title = "Certificate still failing on " + origin
		m.Body = "Failing for " + sofar + ", since " + since + ": " + errText + "."
	case store.NotifyCertRecovered:
		m.Title = "Certificate fixed on " + origin
		m.Body = "Was failing for " + lasted + ", since " + since + "."
	case store.NotifyDomainFailing:
		m.Title = "Domain lookup failing for " + domain
		m.Body = "Failing since " + since + ": " + errText + "."
	case store.NotifyDomainStillFailing:
		m.Title = "Domain lookup still failing for " + domain
		m.Body = "Failing for " + sofar + ", since " + since + "."
	case store.NotifyDomainRecovered:
		m.Title = "Domain lookup works again for " + domain
		m.Body = "Was failing for " + lasted + ", since " + since + "."
	case store.NotifyCertExpiring:
		m.Title = "Certificate for " + origin + expiresIn(n)
		m.Body = expiry(n)
	case store.NotifyDomainExpiring:
		m.Title = "Domain " + domain + expiresIn(n)
		m.Body = expiry(n)
	case store.NotifyFormDraftCreated:
		m.Title = "New form " + form + " is waiting for approval"
	case store.NotifyFormDraftDiscarded:
		m.Title = "Form " + form + " was discarded unapproved"
	case store.NotifyFormSubmission:
		if c, err := strconv.Atoi(n.Fields["count"]); err == nil && c > 1 {
			m.Title = fmt.Sprintf("%d new submissions to %s", c, form)
		} else {
			m.Title = "New submission to " + form
		}
	default:
		m.Title = n.Type
	}
	return m
}

// Outgoing is n as a channel gets it: Render's title led by the type's
// emoji and the project ("🔥 Blog · https://blog.example is down"), the
// same body, and the link a URL under consoleURL.
func Outgoing(n store.Notification, consoleURL string) notify.Message {
	m := Absolute(Render(n), consoleURL)
	lead := Emoji(n)
	if p := n.Fields["project"]; p != "" {
		lead += " " + p + " ·"
	}
	m.Title = lead + " " + m.Title
	return m
}

// Emoji is the sign a channel message about n starts with: 🔥 for
// something failing or expired, ✅ for its recovery, ⏳ for an expiry
// ahead, 🆕, 🗑️ and 📝 for forms, 🔔 for anything else.
func Emoji(n store.Notification) string {
	switch n.Type {
	case store.NotifyUptimeDown, store.NotifyUptimeStillDown,
		store.NotifyCertFailing, store.NotifyCertStillFailing,
		store.NotifyDomainFailing, store.NotifyDomainStillFailing:
		return "🔥"
	case store.NotifyUptimeUp, store.NotifyCertRecovered, store.NotifyDomainRecovered:
		return "✅"
	case store.NotifyCertExpiring, store.NotifyDomainExpiring:
		if days, err := strconv.Atoi(n.Fields["days"]); err == nil && days < 0 {
			return "🔥"
		}
		return "⏳"
	case store.NotifyFormDraftCreated:
		return "🆕"
	case store.NotifyFormDraftDiscarded:
		return "🗑️"
	case store.NotifyFormSubmission:
		return "📝"
	}
	return "🔔"
}

// Absolute is m with its Link made a URL of the console page: the web app
// under consoleURL's /app (Link, a path the web router reads, has no
// /app of its own); unchanged when consoleURL is empty.
func Absolute(m notify.Message, consoleURL string) notify.Message {
	if consoleURL == "" {
		return m
	}
	m.Link = strings.TrimRight(consoleURL, "/") + "/app/" + strings.TrimLeft(m.Link, "/")
	return m
}

// Path is the console page n leads to: the project's domains for a monitor
// notification, its form for a form one, the inbox otherwise.
func Path(n store.Notification) string {
	switch n.Type {
	case store.NotifyUptimeDown, store.NotifyUptimeStillDown, store.NotifyUptimeUp,
		store.NotifyCertFailing, store.NotifyCertStillFailing, store.NotifyCertRecovered,
		store.NotifyDomainFailing, store.NotifyDomainStillFailing, store.NotifyDomainRecovered,
		store.NotifyCertExpiring, store.NotifyDomainExpiring:
		if n.ProjectID != 0 {
			return fmt.Sprintf("/projects/%d/settings/domains", n.ProjectID)
		}
	case store.NotifyFormDraftCreated, store.NotifyFormDraftDiscarded, store.NotifyFormSubmission:
		if n.ProjectID != 0 {
			return fmt.Sprintf("/projects/%d/forms/%s", n.ProjectID, url.PathEscape(subject(n, "form")))
		}
	}
	return "/inbox"
}

// subject is what n is about: its Subject, else the named field.
func subject(n store.Notification, field string) string {
	if n.Subject != "" {
		return n.Subject
	}
	return n.Fields[field]
}

// stamp renders an RFC 3339 field as "2006-01-02 15:04 UTC"; a value that
// does not parse is shown as it is.
func stamp(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.UTC().Format(timeLayout)
}

// elapsed is the time from the RFC 3339 field since to now.
func elapsed(since string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, since)
	if err != nil {
		return ""
	}
	return duration(now.Sub(t))
}

// span is the time from one RFC 3339 field to another.
func span(from, to string) string {
	a, err1 := time.Parse(time.RFC3339, from)
	b, err2 := time.Parse(time.RFC3339, to)
	if err1 != nil || err2 != nil {
		return ""
	}
	return duration(b.Sub(a))
}

// duration is d as "35m", "4h 12m" or "2d 3h": the two largest units
// that apply, none for a negative one.
func duration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	mins := int(d / time.Minute)
	switch {
	case mins < 60:
		return fmt.Sprintf("%dm", mins)
	case mins < 24*60:
		return fmt.Sprintf("%dh %dm", mins/60, mins%60)
	default:
		return fmt.Sprintf("%dd %dh", mins/(24*60), mins/60%24)
	}
}

// expiresIn is the end of an expiry title: " expires in 9 days", or
// " expires today" or " has expired" from the day it is due.
func expiresIn(n store.Notification) string {
	days, err := strconv.Atoi(n.Fields["days"])
	switch {
	case err != nil:
		return " expires in " + n.Fields["days"] + " days"
	case days < 0:
		return " has expired"
	case days == 0:
		return " expires today"
	}
	if days == 1 {
		return " expires in 1 day"
	}
	return fmt.Sprintf(" expires in %d days", days)
}

// expiry is an expiry notification's body.
func expiry(n store.Notification) string {
	days, err := strconv.Atoi(n.Fields["days"])
	switch {
	case err == nil && days < 0:
		return "Expired " + stamp(n.Fields["expires"]) + "."
	case err == nil && days == 0:
		return "Expires today, " + stamp(n.Fields["expires"]) + "."
	}
	return "Expires " + stamp(n.Fields["expires"]) + "."
}
