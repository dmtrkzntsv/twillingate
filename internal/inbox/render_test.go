package inbox_test

import (
	"flag"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/inbox"
	"github.com/dmtrkzntsv/twillingate/internal/shared/golden"
	"github.com/dmtrkzntsv/twillingate/internal/shared/notify"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/*.golden.json from Render's answers")

const (
	projectName = "Blog <&>"
	since       = "2026-11-07T08:00:00Z"
)

const alertDetail = "Below 3% on 2026-11-23 (1,240 at the first step)."

var created = time.Date(2026, 11, 7, 12, 12, 0, 0, time.UTC) // 4h 12m after since

// sample builds one notification of each type, with the fields the
// monitor and the form hooks set.
func sample(typ string) store.Notification {
	n := store.Notification{ID: 1, Type: typ, ProjectID: 3, CreatedAt: created,
		Fields: map[string]string{"project": projectName}}
	set := func(subject string, kv ...string) {
		n.Subject = subject
		for i := 0; i < len(kv); i += 2 {
			n.Fields[kv[i]] = kv[i+1]
		}
	}
	switch typ {
	case store.NotifyUptimeDown:
		set("https://blog.example", "since", since, "reason", "HTTP 503")
	case store.NotifyUptimeStillDown, store.NotifyUptimeUp:
		set("https://blog.example", "since", since, "until", created.Format(time.RFC3339))
	case store.NotifyCertFailing:
		set("https://blog.example", "since", since, "error", "x509: certificate has expired")
	case store.NotifyCertStillFailing:
		set("https://blog.example", "since", since, "error", "x509: certificate has expired")
	case store.NotifyCertRecovered:
		set("https://blog.example", "since", since, "until", created.Format(time.RFC3339))
	case store.NotifyDomainFailing, store.NotifyDomainStillFailing:
		set("blog.example", "since", since, "error", "no such host")
	case store.NotifyDomainRecovered:
		set("blog.example", "since", since, "until", created.Format(time.RFC3339))
	case store.NotifyCertExpiring:
		set("https://blog.example", "days", "9", "expires", "2026-11-16T00:00:00Z")
	case store.NotifyDomainExpiring:
		set("blog.example", "days", "30", "expires", "2026-12-07T00:00:00Z")
	case store.NotifyFormDraftCreated, store.NotifyFormDraftDiscarded:
		set("contact us", "form", "contact us")
	case store.NotifyFormSubmission:
		set("contact us", "form", "contact us", "count", "1")
	case store.NotifyAlertBreached:
		set("Signup conversion", "alert_id", "12", "since", since, "value", "2.1%", "detail", alertDetail)
	case store.NotifyAlertStillBreached:
		set("Signup conversion", "alert_id", "12", "since", since, "value", "2.1%", "detail", alertDetail)
	case store.NotifyAlertRecovered:
		set("Signup conversion", "alert_id", "12", "since", since, "until", created.Format(time.RFC3339), "value", "3.4%", "detail", alertDetail)
	case store.NotifyAlertBroken:
		set("Signup conversion", "alert_id", "12", "reason", "the funnel's first step no longer exists")
	}
	return n
}

type rendered struct {
	Inbox    notify.Message
	Outgoing notify.Message
}

func TestRenderGolden(t *testing.T) {
	set := golden.For(t, *updateGolden)
	cases := map[string]store.Notification{}
	for _, typ := range store.NotificationTypes {
		cases[typ] = sample(typ)
	}
	many := sample(store.NotifyFormSubmission)
	many.Fields["count"] = "7"
	cases["form_submission_many"] = many
	today := sample(store.NotifyCertExpiring)
	today.Fields["days"] = "0"
	cases["cert_expiring_today"] = today
	gone := sample(store.NotifyDomainExpiring)
	gone.Fields["days"] = "-2"
	cases["domain_expired"] = gone
	short := sample(store.NotifyUptimeStillDown)
	short.CreatedAt = time.Date(2026, 11, 7, 8, 35, 0, 0, time.UTC)
	cases["uptime_still_down_35m"] = short
	long := sample(store.NotifyUptimeStillDown)
	long.CreatedAt = time.Date(2026, 11, 9, 11, 0, 0, 0, time.UTC)
	cases["uptime_still_down_2d3h"] = long

	for key, n := range cases {
		got := rendered{Inbox: inbox.Render(n), Outgoing: inbox.Outgoing(n, "https://c.example")}
		want := golden.Get(t, set, key, got)
		if d := golden.Diff(got, want); d != "" {
			t.Errorf("%s: differs from its golden at %s", key, d)
		}
	}
}

func TestRenderCoversVocabulary(t *testing.T) {
	for _, typ := range store.NotificationTypes {
		m := inbox.Render(sample(typ))
		if m.Title == "" || m.Title == typ {
			t.Errorf("%s: title %q is empty or the raw type", typ, m.Title)
		}
		if !strings.HasPrefix(m.Link, "/projects/3/") {
			t.Errorf("%s: link %q", typ, m.Link)
		}
	}
}

func TestRenderUnknownType(t *testing.T) {
	m := inbox.Render(store.Notification{Type: "mystery", ProjectID: 3})
	if m.Title != "mystery" || m.Link != "/inbox" {
		t.Errorf("got %+v", m)
	}
	if p := inbox.Path(store.Notification{Type: "mystery"}); p != "/inbox" {
		t.Errorf("path %q", p)
	}
}

func TestOutgoingLeadsWithEmojiAndProject(t *testing.T) {
	n := sample(store.NotifyUptimeDown)
	if got := inbox.Outgoing(n, "").Title; got != "🔥 "+projectName+" · https://blog.example is down" {
		t.Errorf("title %q", got)
	}
	delete(n.Fields, "project")
	if got := inbox.Outgoing(n, "").Title; got != "🔥 https://blog.example is down" {
		t.Errorf("projectless title %q", got)
	}
	if got := inbox.Outgoing(store.Notification{Type: "mystery"}, "").Title; got != "🔔 mystery" {
		t.Errorf("unknown type title %q", got)
	}
}

func TestAbsolute(t *testing.T) {
	m := notify.Message{Title: "t", Link: "/inbox"}
	for _, base := range []string{"https://c.example", "https://c.example/"} {
		if got := inbox.Absolute(m, base).Link; got != "https://c.example/app/inbox" {
			t.Errorf("%q: %q", base, got)
		}
	}
	if got := inbox.Absolute(m, ""); got != m {
		t.Errorf("empty console URL changed the message: %+v", got)
	}
}

func TestPathEscapesForm(t *testing.T) {
	if p := inbox.Path(sample(store.NotifyFormSubmission)); p != "/projects/3/forms/contact%20us" {
		t.Errorf("path %q", p)
	}
}

func TestExpiryTitlePlural(t *testing.T) {
	for _, c := range []struct{ days, want string }{
		{"1", "Certificate for https://blog.example expires in 1 day"},
		{"2", "Certificate for https://blog.example expires in 2 days"},
		{"0", "Certificate for https://blog.example expires today"},
	} {
		n := sample(store.NotifyCertExpiring)
		n.Fields["days"] = c.days
		if got := inbox.Render(n).Title; got != c.want {
			t.Errorf("days %s: title %q, want %q", c.days, got, c.want)
		}
	}
}

func TestAlertEmojiAndPath(t *testing.T) {
	var got []string
	for _, typ := range []string{store.NotifyAlertBreached, store.NotifyAlertStillBreached, store.NotifyAlertRecovered, store.NotifyAlertBroken} {
		n := sample(typ)
		got = append(got, inbox.Emoji(n))
		if p := inbox.Path(n); p != "/projects/3/alerts/12" {
			t.Errorf("%s: path %q", typ, p)
		}
		delete(n.Fields, "alert_id")
		if p := inbox.Path(n); p != "/inbox" {
			t.Errorf("%s without alert_id: path %q", typ, p)
		}
	}
	if want := "🔥 🔥 ✅ ⚠️"; strings.Join(got, " ") != want {
		t.Errorf("emoji %q, want %q", strings.Join(got, " "), want)
	}
}
