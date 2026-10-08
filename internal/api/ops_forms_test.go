package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// formSeeder writes submissions straight through the store, as ingest does.
type formSeeder struct {
	t    *testing.T
	st   store.Store
	base time.Time
	n    int
}

func newFormSeeder(t *testing.T, h *host) *formSeeder {
	t.Helper()
	st, ok := h.ops.St.(store.Store)
	if !ok {
		t.Fatal("host store is not a store.Store")
	}
	return &formSeeder{t: t, st: st, base: time.Now().UTC().Truncate(time.Second).Add(-time.Hour)}
}

// add writes one submission to form on project 1, each a minute after the
// last, from example.com/contact, with attr as its attribution (nil for
// none).
func (s *formSeeder) add(form, id string, fields map[string]string, attr *store.Attribution) {
	s.t.Helper()
	s.addTo(1, form, id, fields, attr)
}

func (s *formSeeder) addTo(project int64, form, id string, fields map[string]string, attr *store.Attribution) {
	s.t.Helper()
	s.n++
	at := s.base.Add(time.Duration(s.n) * time.Minute)
	sub := store.Submission{ProjectID: project, ID: id, Form: form, ReceivedAt: at, Fields: fields}
	if attr != nil {
		sub.Attribution = *attr
	}
	_, _, err := s.st.WriteSubmission(context.Background(), store.NewSubmission{
		Submission: sub,
		DraftUntil: at.Add(7 * 24 * time.Hour),
		Event: store.Event{ID: id, ProjectID: project, Family: store.FamilyProduct, EventName: store.FormSubmitEvent,
			TS: at, ReceivedAt: at, Kind: "web", ActorKind: "user", ActorID: "a1", Host: "example.com", Path: "/contact"},
	})
	if err != nil {
		s.t.Fatal(err)
	}
}

// callAs runs a tool that must succeed and decodes its answer into T.
func callAs[T any](t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) T {
	t.Helper()
	res := callTool(t, cs, name, args)
	if res.IsError {
		t.Fatalf("%s %v: %s", name, args, textOf(res))
	}
	var out T
	if err := json.Unmarshal([]byte(textOf(res)), &out); err != nil {
		t.Fatalf("%s: decode: %v\n%s", name, err, textOf(res))
	}
	return out
}

// refused runs a tool that must fail and returns its message.
func refused(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res := callTool(t, cs, name, args)
	if !res.IsError {
		t.Fatalf("%s %v succeeded: %s", name, args, textOf(res))
	}
	return textOf(res)
}

type listedForm struct {
	Name           string   `json:"name"`
	Status         string   `json:"status"`
	Purpose        string   `json:"purpose"`
	ReturnURL      string   `json:"return_url"`
	Fields         []string `json:"fields"`
	ExpectedFields []string `json:"expected_fields"`
	DraftUntil     string   `json:"draft_until"`
	ApprovedAt     string   `json:"approved_at"`
	ClosesAt       string   `json:"closes_at"`
	Submissions    int      `json:"submissions"`
	LastSubmitted  string   `json:"last_submitted_at"`
	Archived       bool     `json:"archived"`
}

type submissionsTable struct {
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
	IDs     []string   `json:"ids"`
	Matched int        `json:"matched"`
	Total   int        `json:"total"`
	Offset  int        `json:"offset"`
	Limit   int        `json:"limit"`
}

func listForms(t *testing.T, cs *mcp.ClientSession, archived bool) map[string]listedForm {
	t.Helper()
	out := callAs[struct {
		Forms []listedForm `json:"forms"`
	}](t, cs, "list_forms", map[string]any{"project_id": 1, "archived": archived})
	m := map[string]listedForm{}
	for _, f := range out.Forms {
		m[f.Name] = f
	}
	return m
}

func TestFormToolsRoundTrip(t *testing.T) {
	h, cs := newTestHost(t)
	seed := newFormSeeder(t, h)
	seed.add("contact", "s1", map[string]string{"email": "a@x.io", "note": "hi"}, nil)
	seed.add("contact", "s2", map[string]string{"email": "b@x.io"}, nil)

	f := listForms(t, cs, false)["contact"]
	if f.Status != "draft" || f.Submissions != 2 || !reflect.DeepEqual(f.Fields, []string{"email", "note"}) ||
		f.DraftUntil == "" || f.ExpectedFields != nil || f.LastSubmitted == "" || f.Archived {
		t.Fatalf("draft = %+v", f)
	}

	callAs[okOut](t, cs, "approve_form", map[string]any{"project_id": 1, "name": "contact", "expected_fields": []string{"email"}})
	f = listForms(t, cs, false)["contact"]
	if f.Status != "approved" || !reflect.DeepEqual(f.ExpectedFields, []string{"email"}) || f.ApprovedAt == "" || f.DraftUntil != "" {
		t.Fatalf("approved = %+v", f)
	}
	if msg := refused(t, cs, "approve_form", map[string]any{"project_id": 1, "name": "contact", "expected_fields": []string{"email"}}); !strings.Contains(msg, "already approved") {
		t.Errorf("second approve: %s", msg)
	}
	refused(t, cs, "approve_form", map[string]any{"project_id": 1, "name": "contact", "expected_fields": []string{}})

	callAs[okOut](t, cs, "update_form", map[string]any{"project_id": 1, "name": "contact",
		"purpose": "Leads", "closes_at": "2030-01-02T03:04:05+01:00", "expected_fields": []string{"email", "note"}})
	f = listForms(t, cs, false)["contact"]
	if f.Purpose != "Leads" || f.ClosesAt != "2030-01-02T02:04:05Z" || !reflect.DeepEqual(f.ExpectedFields, []string{"email", "note"}) {
		t.Fatalf("updated = %+v", f)
	}
	// Omitted fields are kept; closes_at null reopens.
	callAs[okOut](t, cs, "update_form", map[string]any{"project_id": 1, "name": "contact", "closes_at": nil})
	f = listForms(t, cs, false)["contact"]
	if f.Purpose != "Leads" || f.ClosesAt != "" {
		t.Fatalf("reopened = %+v", f)
	}
	refused(t, cs, "update_form", map[string]any{"project_id": 1, "name": "contact", "closes_at": "tomorrow"})
	refused(t, cs, "update_form", map[string]any{"project_id": 1, "name": "contact", "expected_fields": []string{}})
	// A return URL must be an allowed target: project 1 lists no origins.
	refused(t, cs, "update_form", map[string]any{"project_id": 1, "name": "contact", "return_url": "https://evil.example/"})

	callAs[okOut](t, cs, "archive_form", map[string]any{"project_id": 1, "name": "contact"})
	if _, ok := listForms(t, cs, false)["contact"]; ok {
		t.Fatal("archived form still listed as active")
	}
	if f := listForms(t, cs, true)["contact"]; !f.Archived {
		t.Fatalf("archived list = %+v", f)
	}
	if msg := refused(t, cs, "list_submissions", map[string]any{"project_id": 1, "name": "contact"}); !strings.Contains(msg, "archived") {
		t.Errorf("archived list_submissions: %s", msg)
	}
	refused(t, cs, "get_submission", map[string]any{"project_id": 1, "name": "contact", "id": "s1"})
	callAs[okOut](t, cs, "restore_form", map[string]any{"project_id": 1, "name": "contact"})
	if _, ok := listForms(t, cs, false)["contact"]; !ok {
		t.Fatal("restored form not listed")
	}
	refused(t, cs, "list_submissions", map[string]any{"project_id": 1, "name": "nope"})
	refused(t, cs, "list_forms", map[string]any{"project_id": 99})
	refused(t, cs, "archive_form", map[string]any{"project_id": 1, "name": "nope"})
	refused(t, cs, "restore_form", map[string]any{"project_id": 99, "name": "contact"})
}

func TestListFormsActionBase(t *testing.T) {
	h, cs := newTestHost(t)
	type answer struct {
		ActionBase string `json:"action_base"`
	}
	if got := callAs[answer](t, cs, "list_forms", map[string]any{"project_id": 1}).ActionBase; got != "https://collector.test/ingest/forms" {
		t.Errorf("action_base with PUBLIC_URL = %q", got)
	}
	h.publicURL = ""
	res := callTool(t, cs, "list_forms", map[string]any{"project_id": 1})
	if res.IsError {
		t.Fatal(textOf(res))
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(textOf(res)), &raw); err != nil {
		t.Fatal(err)
	}
	if got, ok := raw["action_base"]; !ok || string(got) != `""` {
		t.Errorf("action_base without PUBLIC_URL = %s (present %v), want \"\"", got, ok)
	}
}

func TestListSubmissionsColumnsDraftAndApproved(t *testing.T) {
	h, cs := newTestHost(t)
	seed := newFormSeeder(t, h)
	seed.add("contact", "s1", map[string]string{"email": "a@x.io", "note": "hi"},
		&store.Attribution{Referrer: "news.example", UTMSource: "nl", UTMMedium: "email", UTMCampaign: "oct"})
	seed.add("contact", "s2", map[string]string{"email": "b@x.io"}, nil)

	tab := callAs[submissionsTable](t, cs, "list_submissions", map[string]any{"project_id": 1, "name": "contact"})
	if want := []string{"Received", "email", "note"}; !reflect.DeepEqual(tab.Columns, want) {
		t.Fatalf("draft columns = %q", tab.Columns)
	}
	// Newest first; ids align with rows.
	if !reflect.DeepEqual(tab.IDs, []string{"s2", "s1"}) || len(tab.Rows) != 2 || tab.Matched != 2 || tab.Total != 2 ||
		tab.Offset != 0 || tab.Limit != 1000 {
		t.Fatalf("table = %+v", tab)
	}
	if got := tab.Rows[1]; len(got) != 3 || got[1] != "a@x.io" || got[2] != "hi" {
		t.Fatalf("s1 row = %q", got)
	}
	if got := tab.Rows[0]; got[1] != "b@x.io" || got[2] != "" {
		t.Fatalf("s2 row = %q", got)
	}

	callAs[okOut](t, cs, "approve_form", map[string]any{"project_id": 1, "name": "contact", "expected_fields": []string{"email"}})
	tab = callAs[submissionsTable](t, cs, "list_submissions", map[string]any{"project_id": 1, "name": "contact"})
	if want := []string{"Received", "email"}; !reflect.DeepEqual(tab.Columns, want) {
		t.Fatalf("approved columns = %q", tab.Columns)
	}

	// get_submission still shows the field the form no longer expects, and
	// the attribution.
	sub := callAs[map[string]any](t, cs, "get_submission", map[string]any{"project_id": 1, "name": "contact", "id": "s1"})
	want := map[string]any{"id": "s1", "form": "contact", "received_at": sub["received_at"],
		"fields":   map[string]any{"email": "a@x.io", "note": "hi"},
		"referrer": "news.example", "utm_source": "nl", "utm_medium": "email", "utm_campaign": "oct"}
	if !reflect.DeepEqual(sub, want) || sub["received_at"] == "" {
		t.Fatalf("submission = %v", sub)
	}
	if msg := refused(t, cs, "get_submission", map[string]any{"project_id": 1, "name": "contact", "id": "nope"}); !strings.Contains(msg, "no submission") {
		t.Errorf("unknown id: %s", msg)
	}
}

// TestListSubmissionsFieldNamedLikeAColumn: a field named received shows as
// "received (field)" and filters and sorts as itself (Review Focus).
func TestListSubmissionsFieldNamedLikeAColumn(t *testing.T) {
	h, cs := newTestHost(t)
	seed := newFormSeeder(t, h)
	seed.add("contact", "s1", map[string]string{"received": "x", "id": "7"}, nil)
	seed.add("contact", "s2", map[string]string{"received": "y", "id": "8"}, nil)

	tab := callAs[submissionsTable](t, cs, "list_submissions", map[string]any{"project_id": 1, "name": "contact",
		"filters": `[{"column":"received (field)","op":"=","value":"x"}]`})
	if want := []string{"Received", "id (field)", "received (field)"}; !reflect.DeepEqual(tab.Columns, want) {
		t.Fatalf("columns = %q", tab.Columns)
	}
	if !reflect.DeepEqual(tab.IDs, []string{"s1"}) || tab.Rows[0][1] != "7" || tab.Rows[0][2] != "x" ||
		tab.Matched != 1 || tab.Total != 2 {
		t.Fatalf("filtered = %+v", tab)
	}
	tab = callAs[submissionsTable](t, cs, "list_submissions", map[string]any{"project_id": 1, "name": "contact",
		"sort": "received (field):desc"})
	if !reflect.DeepEqual(tab.IDs, []string{"s2", "s1"}) {
		t.Fatalf("sorted desc = %v", tab.IDs)
	}
	tab = callAs[submissionsTable](t, cs, "list_submissions", map[string]any{"project_id": 1, "name": "contact",
		"sort": "received (field):asc"})
	if !reflect.DeepEqual(tab.IDs, []string{"s1", "s2"}) {
		t.Fatalf("sorted asc = %v", tab.IDs)
	}
}

func TestListSubmissionsPagingAndRefusals(t *testing.T) {
	h, cs := newTestHost(t)
	seed := newFormSeeder(t, h)
	for _, c := range []struct{ id, email, plan string }{
		{"s1", "c@x.io", "pro"}, {"s2", "a@x.io", "free"}, {"s3", "b@x.io", "pro"},
	} {
		seed.add("contact", c.id, map[string]string{"email": c.email, "plan": c.plan}, nil)
	}
	args := func(kv ...any) map[string]any {
		m := map[string]any{"project_id": 1, "name": "contact"}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}
	tab := callAs[submissionsTable](t, cs, "list_submissions", args("sort", "email:asc", "offset", 1, "limit", 1))
	if !reflect.DeepEqual(tab.IDs, []string{"s3"}) || tab.Rows[0][1] != "b@x.io" || tab.Offset != 1 || tab.Limit != 1 ||
		tab.Matched != 3 || tab.Total != 3 {
		t.Fatalf("page = %+v", tab)
	}
	tab = callAs[submissionsTable](t, cs, "list_submissions", args("filters", `[{"column":"plan","op":"in","value":["pro"]}]`))
	if !reflect.DeepEqual(tab.IDs, []string{"s3", "s1"}) || tab.Matched != 2 || tab.Total != 3 {
		t.Fatalf("filtered = %+v", tab)
	}
	tab = callAs[submissionsTable](t, cs, "list_submissions", args("distinct", "plan"))
	if !reflect.DeepEqual(tab.Columns, []string{"value", "rows"}) || tab.IDs != nil ||
		!reflect.DeepEqual(tab.Rows, [][]string{{"pro", "2"}, {"free", "1"}}) {
		t.Fatalf("distinct = %+v", tab)
	}
	// Past the end: an empty page, still counted.
	tab = callAs[submissionsTable](t, cs, "list_submissions", args("offset", 10))
	if len(tab.Rows) != 0 || tab.Rows == nil || tab.IDs == nil || len(tab.IDs) != 0 || tab.Matched != 3 {
		t.Fatalf("past the end = %+v", tab)
	}
	// An empty page still says "ids": [], so a caller indexes it as it does rows.
	res := callTool(t, cs, "list_submissions", args("offset", 10))
	if !strings.Contains(textOf(res), `"ids":[]`) {
		t.Errorf("empty page lacks ids: []: %s", textOf(res))
	}
	if res := callTool(t, cs, "list_submissions", args("distinct", "plan")); strings.Contains(textOf(res), `"ids"`) {
		t.Errorf("distinct carries ids: %s", textOf(res))
	}
	for _, bad := range []map[string]any{
		args("filters", "not json"),
		args("filters", `[{"column":"plan","op":"~","value":"x"}]`),
		args("filters", `[{"column":"nope","op":"=","value":"x"}]`),
		args("sort", "email"),
		args("sort", "nope:asc"),
		args("distinct", "plan", "sort", "email:asc"),
		args("limit", 5000),
		args("offset", -1),
	} {
		refused(t, cs, "list_submissions", bad)
	}
}

// TestListSubmissionsOddFieldNames: a field name is only ever a quoted
// literal, whatever it holds.
func TestListSubmissionsOddFieldNames(t *testing.T) {
	h, cs := newTestHost(t)
	seed := newFormSeeder(t, h)
	fields := map[string]string{`a"b`: "1", `a\b`: "2", "x.y": "3", "it's": "4", "[0]": "5", "ü ö": "6", "Email": "7", "email": "8"}
	seed.add("contact", "s1", fields, nil)
	tab := callAs[submissionsTable](t, cs, "list_submissions", map[string]any{"project_id": 1, "name": "contact"})
	got := map[string]string{}
	for i, c := range tab.Columns {
		got[c] = tab.Rows[0][i]
	}
	want := map[string]string{`a"b`: "1", `a\b`: "2", "x.y": "3", "it's": "4", "[0]": "5", "ü ö": "6", "Email": "7", "email (field)": "8"}
	for c, v := range want {
		if got[c] != v {
			t.Errorf("column %q = %q, want %q (columns %q)", c, got[c], v, tab.Columns)
		}
	}
}

func TestFindSubmissionsAcrossForms(t *testing.T) {
	h, cs := newTestHost(t)
	seed := newFormSeeder(t, h)
	seed.add("contact", "s1", map[string]string{"email": "Alice@Example.com"}, nil)
	seed.add("newsletter", "s2", map[string]string{"email": "alice@example.com"}, nil)
	seed.add("contact", "s3", map[string]string{"email": "bob@example.com"}, nil)
	seed.addTo(2, "contact", "s4", map[string]string{"email": "alice@example.com"}, nil)
	seed.add("old", "s5", map[string]string{"email": "alice@example.com"}, nil)
	callAs[okOut](t, cs, "archive_form", map[string]any{"project_id": 1, "name": "old"})

	type found struct {
		Submissions []struct {
			ID       string            `json:"id"`
			Form     string            `json:"form"`
			Fields   map[string]string `json:"fields"`
			Archived *bool             `json:"archived"`
		} `json:"submissions"`
		NextCursor string `json:"next_cursor"`
	}
	// An archived form's submissions are found too, marked archived: an
	// erasure request reaches them.
	out := callAs[found](t, cs, "find_submissions", map[string]any{"project_id": 1, "search": "ALICE"})
	if len(out.Submissions) != 3 || out.Submissions[0].ID != "s5" || out.Submissions[0].Archived == nil || !*out.Submissions[0].Archived ||
		out.Submissions[1].ID != "s2" || out.Submissions[1].Form != "newsletter" || out.Submissions[1].Archived == nil || *out.Submissions[1].Archived ||
		out.Submissions[2].ID != "s1" || out.Submissions[2].Fields["email"] != "Alice@Example.com" || out.NextCursor != "" {
		t.Fatalf("found = %+v", out)
	}
	callAs[okOut](t, cs, "restore_form", map[string]any{"project_id": 1, "name": "old"})
	callAs[struct{}](t, cs, "delete_submissions", map[string]any{"project_id": 1, "ids": []string{"s5"}})
	out = callAs[found](t, cs, "find_submissions", map[string]any{"project_id": 1, "search": "alice", "limit": 1})
	if len(out.Submissions) != 1 || out.NextCursor != "s2" {
		t.Fatalf("page 1 = %+v", out)
	}
	out = callAs[found](t, cs, "find_submissions", map[string]any{"project_id": 1, "search": "alice", "limit": 1, "cursor": out.NextCursor})
	if len(out.Submissions) != 1 || out.Submissions[0].ID != "s1" || out.NextCursor != "" {
		t.Fatalf("page 2 = %+v", out)
	}
	refused(t, cs, "find_submissions", map[string]any{"project_id": 1, "search": "a"})
	refused(t, cs, "find_submissions", map[string]any{"project_id": 99, "search": "alice"})
}

func TestDeleteSubmissionsSelectors(t *testing.T) {
	h, cs := newTestHost(t)
	seed := newFormSeeder(t, h)
	seed.add("contact", "s1", map[string]string{"email": "a@x.io", "plan": "pro"}, nil)
	seed.add("contact", "s2", map[string]string{"email": "b@x.io", "plan": "free"}, nil)
	seed.add("contact", "s3", map[string]string{"email": "c@x.io", "plan": "pro"}, nil)
	seed.add("contact", "s4", map[string]string{"email": "d@x.io", "plan": "pro"}, nil)
	seed.add("newsletter", "s5", map[string]string{"email": "alice@x.io"}, nil)
	seed.add("contact", "s6", map[string]string{"email": "ALICE@x.io", "plan": "free"}, nil)
	// A small page, so delete by filters pages through the matches.
	subs, err := readsql.Open(testDBPaths[h], 5*time.Second, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { subs.Close() })
	h.subs = subs

	ids := func() []string {
		t.Helper()
		return callAs[submissionsTable](t, cs, "list_submissions", map[string]any{"project_id": 1, "name": "contact", "limit": 2,
			"filters": `[{"column":"plan","op":"!=","value":"none"}]`}).IDs
	}
	type deleted struct {
		Deleted int `json:"deleted"`
	}
	for _, bad := range []map[string]any{
		{"project_id": 1},
		{"project_id": 1, "ids": []string{}},
		{"project_id": 1, "ids": []string{"s1"}, "search": "alice"},
		{"project_id": 1, "form": "contact", "filters": `[{"column":"plan","op":"=","value":"pro"}]`, "search": "alice"},
		{"project_id": 1, "form": "contact"},
		{"project_id": 1, "filters": `[{"column":"plan","op":"=","value":"pro"}]`},
		{"project_id": 1, "form": "contact", "filters": `[]`},
		{"project_id": 1, "form": "contact", "filters": `[{"column":"nope","op":"=","value":"x"}]`},
		{"project_id": 1, "form": "gone", "filters": `[{"column":"plan","op":"=","value":"x"}]`},
		{"project_id": 1, "search": "a"},
	} {
		refused(t, cs, "delete_submissions", bad)
	}

	if d := callAs[deleted](t, cs, "delete_submissions", map[string]any{"project_id": 1, "ids": []string{"s2", "nope"}}); d.Deleted != 1 {
		t.Fatalf("by ids deleted %d", d.Deleted)
	}
	// By filters: exactly the rows the table shows, past one page.
	if d := callAs[deleted](t, cs, "delete_submissions", map[string]any{"project_id": 1, "form": "contact",
		"filters": `[{"column":"plan","op":"=","value":"pro"}]`}); d.Deleted != 3 {
		t.Fatalf("by filters deleted %d", d.Deleted)
	}
	if got := ids(); !reflect.DeepEqual(got, []string{"s6"}) {
		t.Fatalf("left after filters = %v", got)
	}
	// By search: across forms, case-insensitive.
	if d := callAs[deleted](t, cs, "delete_submissions", map[string]any{"project_id": 1, "search": "alice"}); d.Deleted != 2 {
		t.Fatalf("by search deleted %d", d.Deleted)
	}
	if got := ids(); len(got) != 0 {
		t.Fatalf("left after search = %v", got)
	}
	res, err := h.db.Run(context.Background(), `SELECT detail FROM audit_log WHERE action='submission.delete' ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	var details []string
	for _, r := range res.Rows {
		details = append(details, r[0])
	}
	// The selector's kind and the form, never the search text or the
	// filter values: they are usually the erased person's email.
	// The count is what was deleted ("nope" matched nothing), not what was asked for.
	want := []string{"ids (1 deleted)", "filters: contact (3 deleted)", "search (2 deleted)"}
	if !reflect.DeepEqual(details, want) {
		t.Fatalf("audit details = %q", details)
	}
	for _, d := range details {
		if strings.Contains(d, "alice") || strings.Contains(d, "pro") || strings.Contains(d, "plan") {
			t.Errorf("audit detail %q holds a search or filter value", d)
		}
	}
	// The custom-SQL handle still cannot read the table.
	if msg := refused(t, cs, "query", map[string]any{"sql": "select * from submissions"}); !strings.Contains(msg, "submissions") {
		t.Errorf("query refusal: %s", msg)
	}
}

func TestExportSubmissionsCSV(t *testing.T) {
	h, _ := newTestHost(t)
	seed := newFormSeeder(t, h)
	seed.add("contact", "s1", map[string]string{"email": "a@x.io", "note": "line one\nline, two"}, nil)
	seed.add("contact", "s2", map[string]string{"email": "b@x.io"}, nil)
	seed.add("contact", "s3", map[string]string{"email": "c@x.io"}, nil)
	subs, err := readsql.Open(testDBPaths[h], 5*time.Second, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { subs.Close() })
	h.subs = subs
	mux := newTestRegistrar(t, h).rest

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	rec := get("/api/projects/1/forms/contact/submissions.csv?sort=" + url.QueryEscape("email:asc"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/csv; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="contact-submissions.csv"` {
		t.Errorf("Content-Disposition = %q", cd)
	}
	records, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Received", "email", "note"}; !reflect.DeepEqual(records[0], want) {
		t.Fatalf("header = %q", records[0])
	}
	if len(records) != 4 || records[1][1] != "a@x.io" || records[1][2] != "line one\nline, two" || records[3][1] != "c@x.io" {
		t.Fatalf("records = %q", records)
	}

	rec = get("/api/projects/1/forms/contact/submissions.csv?filters=" + url.QueryEscape(`[{"column":"email","op":"=","value":"b@x.io"}]`))
	records, err = csv.NewReader(rec.Body).ReadAll()
	if err != nil || len(records) != 2 || records[1][1] != "b@x.io" {
		t.Fatalf("filtered = %q, %v", records, err)
	}
	if rec := get("/api/projects/1/forms/nope/submissions.csv"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown form: %d", rec.Code)
	}
	if rec := get("/api/projects/1/forms/contact/submissions.csv?filters=nope"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad filters: %d", rec.Code)
	}
	// A cell starting like a formula is made inert, header cells too.
	for i, v := range []string{"=1+1", "+x", "-y", "@z", "\tt", "\rr", "plain", ""} {
		seed.add("inj", "i"+string(rune('0'+i)), map[string]string{"=sum": v}, nil)
	}
	rec = get("/api/projects/1/forms/inj/submissions.csv?sort=" + url.QueryEscape("Received:asc"))
	records, err = csv.NewReader(rec.Body).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if records[0][1] != "'=sum" {
		t.Errorf("header cell = %q", records[0][1])
	}
	var cells []string
	for _, r := range records[1:] {
		cells = append(cells, r[1])
	}
	if want := []string{"'=1+1", "'+x", "'-y", "'@z", "'\tt", "'\rr", "plain", ""}; !reflect.DeepEqual(cells, want) {
		t.Errorf("cells = %q, want %q", cells, want)
	}

	// The JSON routes answer too.
	if rec := get("/api/projects/1/forms/contact/submissions?limit=1"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ids":["s3"]`) {
		t.Errorf("list route: %d %s", rec.Code, rec.Body)
	}
	if rec := get("/api/projects/1/forms/contact/submissions/s1"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"a@x.io"`) {
		t.Errorf("get route: %d %s", rec.Code, rec.Body)
	}
}

func TestListSubmissionsTimeout(t *testing.T) {
	h, cs := newTestHost(t)
	newFormSeeder(t, h).add("contact", "s1", map[string]string{"email": "a@x.io"}, nil)
	subs, err := readsql.Open(testDBPaths[h], time.Nanosecond, 1000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { subs.Close() })
	h.subs = subs
	if msg := refused(t, cs, "list_submissions", map[string]any{"project_id": 1, "name": "contact"}); !strings.Contains(msg, "exceeded") {
		t.Errorf("timeout: %s", msg)
	}
}

// TestSubmissionsFieldsNamedLikeRefusedTables: a field named like a table
// readsql refuses (meta, dbstat, sqlite_*, pragma_*, any case) never
// reaches the SQL as a name, so its form lists, filters, sorts, counts,
// exports and deletes by filters like any other.
func TestSubmissionsFieldsNamedLikeRefusedTables(t *testing.T) {
	h, cs := newTestHost(t)
	seed := newFormSeeder(t, h)
	row := func(v string) map[string]string {
		return map[string]string{"meta": v, "Meta": "M" + v, "dbstat": "d" + v, "sqlite_x": "s" + v, "pragma_y": "p" + v}
	}
	seed.add("odd", "s1", row("1"), nil)
	seed.add("odd", "s2", row("2"), nil)
	seed.add("odd", "s3", row("3"), nil)
	args := func(kv ...any) map[string]any {
		m := map[string]any{"project_id": 1, "name": "odd"}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}

	tab := callAs[submissionsTable](t, cs, "list_submissions", args())
	want := []string{"Received", "Meta", "dbstat", "meta (field)", "pragma_y", "sqlite_x"}
	if !reflect.DeepEqual(tab.Columns, want) {
		t.Fatalf("columns = %q, want %q", tab.Columns, want)
	}
	if !reflect.DeepEqual(tab.IDs, []string{"s3", "s2", "s1"}) || tab.Rows[0][1] != "M3" || tab.Rows[0][3] != "3" ||
		tab.Rows[0][5] != "s3" {
		t.Fatalf("table = %+v", tab)
	}
	tab = callAs[submissionsTable](t, cs, "list_submissions", args("filters", `[{"column":"meta (field)","op":"in","value":["1","2"]}]`,
		"sort", "dbstat:asc"))
	if !reflect.DeepEqual(tab.IDs, []string{"s1", "s2"}) || tab.Matched != 2 || tab.Total != 3 {
		t.Fatalf("filtered and sorted = %+v", tab)
	}
	tab = callAs[submissionsTable](t, cs, "list_submissions", args("distinct", "pragma_y"))
	if !reflect.DeepEqual(tab.Columns, []string{"value", "rows"}) || len(tab.Rows) != 3 {
		t.Fatalf("distinct = %+v", tab)
	}
	if msg := refused(t, cs, "list_submissions", args("sort", "nope:asc")); !strings.Contains(msg, "sqlite_x") {
		t.Errorf("unknown column refusal does not name the columns: %s", msg)
	}

	rec := httptest.NewRecorder()
	newTestRegistrar(t, h).rest.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/projects/1/forms/odd/submissions.csv?sort="+url.QueryEscape("sqlite_x:asc"), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d %s", rec.Code, rec.Body)
	}
	records, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil || !reflect.DeepEqual(records[0], want) || len(records) != 4 || records[1][5] != "s1" {
		t.Fatalf("export = %q, %v", records, err)
	}

	d := callAs[struct {
		Deleted int `json:"deleted"`
	}](t, cs, "delete_submissions", map[string]any{"project_id": 1, "form": "odd",
		"filters": `[{"column":"Meta","op":"=","value":"M2"}]`})
	if d.Deleted != 1 {
		t.Fatalf("deleted %d", d.Deleted)
	}
	if tab := callAs[submissionsTable](t, cs, "list_submissions", args()); !reflect.DeepEqual(tab.IDs, []string{"s3", "s1"}) {
		t.Fatalf("left = %v", tab.IDs)
	}
}
