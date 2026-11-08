// Package inbox turns a stored notification into the one message the
// inbox shows and every channel sends.
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

// Render is n as its message: the same words in the inbox and in a
// channel. Link is a console path; Absolute makes it a URL. It never
// reads form field values: a form is only ever named.
func Render(n store.Notification) notify.Message {
	m := notify.Message{Link: Path(n)}
	p := n.Fields["project"]
	since := stamp(n.Fields["since"])
	origin, domain, form := subject(n, "origin"), subject(n, "domain"), subject(n, "form")
	errText := n.Fields["error"]

	switch n.Type {
	case store.NotifyUptimeDown:
		m.Title = origin + " is down"
		m.Body = lead(p, "down since "+since+" ("+n.Fields["reason"]+")")
	case store.NotifyUptimeStillDown:
		m.Title = origin + " is still down"
		m.Body = lead(p, "down since "+since+", "+elapsed(n.Fields["since"], n.CreatedAt))
	case store.NotifyUptimeUp:
		m.Title = origin + " is up"
		m.Body = lead(p, "was down since "+since+" ("+span(n.Fields["since"], n.Fields["until"])+")")
	case store.NotifyCertFailing:
		m.Title = "Certificate problem on " + origin
		m.Body = lead(p, errText+" (since "+since+")")
	case store.NotifyCertStillFailing:
		m.Title = "Certificate still failing on " + origin
		m.Body = lead(p, errText+", since "+since+", "+elapsed(n.Fields["since"], n.CreatedAt))
	case store.NotifyCertRecovered:
		m.Title = "Certificate fixed on " + origin
		m.Body = lead(p, "failing since "+since+" ("+span(n.Fields["since"], n.Fields["until"])+")")
	case store.NotifyDomainFailing:
		m.Title = "Domain lookup failing for " + domain
		m.Body = lead(p, errText+" (since "+since+")")
	case store.NotifyDomainStillFailing:
		m.Title = "Domain lookup still failing for " + domain
		m.Body = lead(p, "since "+since+", "+elapsed(n.Fields["since"], n.CreatedAt))
	case store.NotifyDomainRecovered:
		m.Title = "Domain lookup works again for " + domain
		m.Body = lead(p, "failing since "+since+" ("+span(n.Fields["since"], n.Fields["until"])+")")
	case store.NotifyCertExpiring:
		m.Title = "Certificate for " + origin + expiresIn(n)
		m.Body = lead(p, expiry(n))
	case store.NotifyDomainExpiring:
		m.Title = "Domain " + domain + expiresIn(n)
		m.Body = lead(p, expiry(n))
	case store.NotifyFormDraftCreated:
		m.Title = "New form " + form + " is waiting for approval"
		m.Body = p
	case store.NotifyFormDraftDiscarded:
		m.Title = "Form " + form + " was discarded unapproved"
		m.Body = p
	case store.NotifyFormSubmission:
		if c, err := strconv.Atoi(n.Fields["count"]); err == nil && c > 1 {
			m.Title = fmt.Sprintf("%d new form submissions in %s / %s", c, p, form)
		} else {
			m.Title = "A new form submission in " + p + " / " + form
		}
	default:
		m.Title = n.Type
	}
	return m
}

// Absolute is m with its Link made a URL under consoleURL; unchanged when
// consoleURL is empty.
func Absolute(m notify.Message, consoleURL string) notify.Message {
	if consoleURL == "" {
		return m
	}
	m.Link = strings.TrimRight(consoleURL, "/") + "/" + strings.TrimLeft(m.Link, "/")
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

// lead puts the project name before the rest of a body.
func lead(project, rest string) string {
	if project == "" {
		return rest
	}
	return project + ": " + rest
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

// expiry is an expiry notification's body after the project.
func expiry(n store.Notification) string {
	days, err := strconv.Atoi(n.Fields["days"])
	switch {
	case err == nil && days < 0:
		return "expired"
	case err == nil && days == 0:
		return "expires today"
	}
	return "expires " + stamp(n.Fields["expires"])
}
