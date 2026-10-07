package manage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// formCalls wraps a real store (for the registry) and records what the form
// operations hand it, serving forms from an in-memory map.
type formCalls struct {
	Store
	forms    map[string]store.Form
	audits   []store.AuditEntry
	approved []time.Time
	expected [][]string
	updated  []store.Form
	draftArg time.Time
	archArg  []bool
	deleted  [][]string
	found    []string
	limit    int
}

func newFormOps(t *testing.T) (*Ops, *formCalls, int64) {
	t.Helper()
	ctx := context.Background()
	fc := &formCalls{Store: testStore(t), forms: map[string]store.Form{}}
	reg := New(fc, discard())
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	ops := NewOps(reg, fc)
	ops.FormDraftDays = 7
	ops.now = func() time.Time { return time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC) }
	p, err := ops.CreateProject(ctx, "cli", ProjectSpec{Name: "Blog",
		AllowedOrigins: []string{"https://blog.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	return ops, fc, p.ID
}

func (f *formCalls) ListForms(_ context.Context, pid int64, archived bool) ([]store.Form, error) {
	var out []store.Form
	for _, fm := range f.forms {
		if fm.ProjectID == pid && (fm.ArchivedAt != nil) == archived {
			out = append(out, fm)
		}
	}
	return out, nil
}

func (f *formCalls) GetForm(_ context.Context, pid int64, name string) (store.Form, error) {
	fm, ok := f.forms[name]
	if !ok || fm.ProjectID != pid {
		return store.Form{}, store.ErrNotFound
	}
	return fm, nil
}

func (f *formCalls) ApproveForm(_ context.Context, pid int64, name string, expected []string, now time.Time, a store.AuditEntry) error {
	fm, ok := f.forms[name]
	if !ok {
		return store.ErrNotFound
	}
	if fm.Status == store.FormApproved {
		return store.ErrConflict
	}
	fm.Status, fm.ExpectedFields, fm.DraftUntil = store.FormApproved, expected, nil
	f.forms[name] = fm
	f.approved = append(f.approved, now)
	f.expected = append(f.expected, expected)
	f.audits = append(f.audits, a)
	return nil
}

func (f *formCalls) UpdateForm(_ context.Context, fm store.Form, a store.AuditEntry) error {
	f.updated = append(f.updated, fm) // as passed
	if fm.ExpectedFields == nil {     // as the store: nil keeps the stored list
		fm.ExpectedFields = f.forms[fm.Name].ExpectedFields
	}
	f.forms[fm.Name] = fm
	f.audits = append(f.audits, a)
	return nil
}

func (f *formCalls) SetFormArchived(_ context.Context, pid int64, name string, archived bool, draftUntil time.Time, a store.AuditEntry) error {
	f.draftArg = draftUntil
	f.archArg = append(f.archArg, archived)
	f.audits = append(f.audits, a)
	return nil
}

func (f *formCalls) DeleteSubmissions(_ context.Context, pid int64, ids []string, a store.AuditEntry) (int, error) {
	f.deleted = append(f.deleted, ids)
	f.audits = append(f.audits, a)
	return len(ids), nil
}

func (f *formCalls) FindSubmissions(_ context.Context, pid int64, search string, limit int, after string) ([]store.Submission, string, error) {
	f.found = append(f.found, search)
	f.limit = limit
	return nil, "", nil
}

func (f *formCalls) SubmissionIDsMatching(_ context.Context, pid int64, search string) ([]string, error) {
	f.found = append(f.found, search)
	return []string{"a"}, nil
}

func (f *formCalls) seed(pid int64, name, status string) {
	fm := store.Form{ProjectID: pid, Name: name, Status: status}
	if status == store.FormDraft {
		du := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
		fm.DraftUntil = &du
	} else {
		fm.ExpectedFields = []string{"email"}
	}
	f.forms[name] = fm
}

func TestApproveFormValidates(t *testing.T) {
	ctx := context.Background()
	ops, fc, pid := newFormOps(t)
	fc.seed(pid, "contact", store.FormDraft)
	long := strings.Repeat("a", 65)
	for name, c := range map[string]struct {
		pid      int64
		form     string
		expected []string
		want     error
	}{
		"empty expected":  {pid, "contact", nil, ErrInvalid},
		"empty name":      {pid, "contact", []string{"email", ""}, ErrInvalid},
		"too long":        {pid, "contact", []string{long}, ErrInvalid},
		"dollar":          {pid, "contact", []string{"$redirect"}, ErrInvalid},
		"unknown project": {99, "contact", []string{"email"}, ErrNotFound},
		"unknown form":    {pid, "nope", []string{"email"}, ErrNotFound},
	} {
		err := ops.ApproveForm(ctx, "cli", c.pid, c.form, c.expected)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", name, err, c.want)
		}
	}
	if len(fc.audits) != 0 {
		t.Fatalf("refusals wrote audits: %+v", fc.audits)
	}
}

func TestApproveFormRefusesArchivedAndRepeats(t *testing.T) {
	ctx := context.Background()
	ops, fc, pid := newFormOps(t)
	fc.seed(pid, "old", store.FormDraft)
	at := time.Now()
	fm := fc.forms["old"]
	fm.ArchivedAt = &at
	fc.forms["old"] = fm
	err := ops.ApproveForm(ctx, "cli", pid, "old", []string{"email"})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "restore") {
		t.Fatalf("archived: err = %v, want ErrInvalid naming restore", err)
	}

	fc.seed(pid, "contact", store.FormDraft)
	if err := ops.ApproveForm(ctx, "mcp", pid, "contact", []string{"email", "message"}); err != nil {
		t.Fatal(err)
	}
	if err := ops.ApproveForm(ctx, "mcp", pid, "contact", []string{"email"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("second approve: err = %v, want ErrConflict", err)
	}
	if len(fc.audits) != 1 {
		t.Fatalf("audits = %+v, want one", fc.audits)
	}
	a := fc.audits[0]
	if a.Actor != "mcp" || a.Action != "form.approve" || a.Subject != "form/1/contact" {
		t.Fatalf("audit = %+v", a)
	}
	if !fc.approved[0].Equal(ops.now()) {
		t.Fatalf("approved at %v, want now", fc.approved[0])
	}
}

func TestApproveKeepsClosesAt(t *testing.T) {
	ctx := context.Background()
	ops, fc, pid := newFormOps(t)
	fc.seed(pid, "contact", store.FormDraft)
	past := ops.now().Add(-48 * time.Hour)
	fm := fc.forms["contact"]
	fm.ClosesAt = &past
	fc.forms["contact"] = fm
	if err := ops.ApproveForm(ctx, "cli", pid, "contact", []string{"email"}); err != nil {
		t.Fatal(err)
	}
	got := fc.forms["contact"]
	if got.ClosesAt == nil || !got.ClosesAt.Equal(past) {
		t.Fatalf("closes_at after approve = %v, want %v", got.ClosesAt, past)
	}
	if got.Open(ops.now()) {
		t.Fatal("form closed in the past is open after approval")
	}
}

func str(s string) *string { return &s }

func TestUpdateFormMergesAndValidates(t *testing.T) {
	ctx := context.Background()
	ops, fc, pid := newFormOps(t)
	fc.seed(pid, "draft", store.FormDraft)
	fc.seed(pid, "live", store.FormApproved)
	closes := ops.now().Add(time.Hour)
	var nilTime *time.Time
	empty := []string{}
	bad := []string{"$x"}
	good := []string{"email", "name"}

	for name, c := range map[string]struct {
		form string
		spec FormSpec
		want error
	}{
		"expected on draft":   {"draft", FormSpec{ExpectedFields: &good}, ErrInvalid},
		"empty on approved":   {"live", FormSpec{ExpectedFields: &empty}, ErrInvalid},
		"bad expected":        {"live", FormSpec{ExpectedFields: &bad}, ErrInvalid},
		"return url foreign":  {"live", FormSpec{ReturnURL: str("https://evil.example.org/x")}, ErrInvalid},
		"return url relative": {"live", FormSpec{ReturnURL: str("/thanks")}, ErrInvalid},
		"unknown form":        {"nope", FormSpec{Purpose: str("x")}, ErrNotFound},
	} {
		if err := ops.UpdateForm(ctx, "cli", pid, c.form, c.spec); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", name, err, c.want)
		}
	}
	if err := ops.UpdateForm(ctx, "cli", 99, "live", FormSpec{Purpose: str("x")}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown project: err = %v", err)
	}
	if len(fc.audits) != 0 {
		t.Fatalf("refusals wrote audits: %+v", fc.audits)
	}

	// Merge: purpose set, the rest kept.
	if err := ops.UpdateForm(ctx, "api", pid, "live", FormSpec{Purpose: str("Contact us")}); err != nil {
		t.Fatal(err)
	}
	got := fc.forms["live"]
	if got.Purpose != "Contact us" || len(got.ExpectedFields) != 1 || got.ExpectedFields[0] != "email" {
		t.Fatalf("after purpose update: %+v", got)
	}
	if fc.updated[0].ExpectedFields != nil {
		t.Fatalf("a purpose-only update passed expected_fields %v to the store, want nil (keep)", fc.updated[0].ExpectedFields)
	}
	a := fc.audits[0]
	if a.Actor != "api" || a.Action != "form.update" || a.Subject != "form/1/live" {
		t.Fatalf("audit = %+v", a)
	}
	if strings.Contains(a.Detail, "Contact us") {
		t.Errorf("audit detail carries a value: %q", a.Detail)
	}

	// Return URL allowed; closes_at set, then reopened with nil-pointer-to-nil.
	if err := ops.UpdateForm(ctx, "cli", pid, "live", FormSpec{
		ReturnURL: str("https://blog.example.com/thanks"), ClosesAt: ptrTime(&closes),
		ExpectedFields: &good}); err != nil {
		t.Fatal(err)
	}
	got = fc.forms["live"]
	if got.ReturnURL != "https://blog.example.com/thanks" || got.ClosesAt == nil || !got.ClosesAt.Equal(closes) || len(got.ExpectedFields) != 2 {
		t.Fatalf("after full update: %+v", got)
	}
	if err := ops.UpdateForm(ctx, "cli", pid, "live", FormSpec{ClosesAt: ptrTime(nilTime), ReturnURL: str("")}); err != nil {
		t.Fatal(err)
	}
	got = fc.forms["live"]
	if got.ClosesAt != nil || got.ReturnURL != "" || got.Purpose != "Contact us" {
		t.Fatalf("after reopen: %+v", got)
	}

	// A draft can still take purpose, return url and closes_at.
	if err := ops.UpdateForm(ctx, "cli", pid, "draft", FormSpec{Purpose: str("p")}); err != nil {
		t.Fatal(err)
	}
}

func ptrTime(t *time.Time) **time.Time { return &t }

func TestArchiveRestoreForm(t *testing.T) {
	ctx := context.Background()
	ops, fc, pid := newFormOps(t)
	if err := ops.ArchiveForm(ctx, "cli", pid, "contact"); err != nil {
		t.Fatal(err)
	}
	if err := ops.RestoreForm(ctx, "mcp", pid, "contact"); err != nil {
		t.Fatal(err)
	}
	if want := ops.now().Add(7 * 24 * time.Hour); !fc.draftArg.Equal(want) {
		t.Fatalf("restore draftUntil = %v, want now+7d %v", fc.draftArg, want)
	}
	if len(fc.archArg) != 2 || !fc.archArg[0] || fc.archArg[1] {
		t.Fatalf("archived args = %v", fc.archArg)
	}
	if fc.audits[0].Action != "form.archive" || fc.audits[1].Action != "form.restore" ||
		fc.audits[1].Subject != "form/1/contact" || fc.audits[1].Actor != "mcp" {
		t.Fatalf("audits = %+v", fc.audits)
	}
	if err := ops.ArchiveForm(ctx, "cli", 99, "contact"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown project: %v", err)
	}
	if err := ops.RestoreForm(ctx, "cli", 99, "contact"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown project: %v", err)
	}
}

func TestListForms(t *testing.T) {
	ctx := context.Background()
	ops, fc, pid := newFormOps(t)
	fc.seed(pid, "a", store.FormDraft)
	got, err := ops.ListForms(ctx, pid, false)
	if err != nil || len(got) != 1 {
		t.Fatalf("list = %+v, %v", got, err)
	}
	if _, err := ops.ListForms(ctx, 99, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown project: %v", err)
	}
}

func TestSubmissionSearchRules(t *testing.T) {
	ctx := context.Background()
	ops, fc, pid := newFormOps(t)
	if _, _, err := ops.FindSubmissions(ctx, pid, "a", 10, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("1-char find: %v", err)
	}
	if _, err := ops.SubmissionIDsMatching(ctx, pid, " "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("short match: %v", err)
	}
	if len(fc.found) != 0 {
		t.Fatalf("short searches reached the store: %v", fc.found)
	}
	for in, want := range map[int]int{0: 100, -3: 100, 1: 1, 500: 500, 501: 500, 9999: 500} {
		if _, _, err := ops.FindSubmissions(ctx, pid, "ann", in, ""); err != nil {
			t.Fatal(err)
		}
		if fc.limit != want {
			t.Errorf("limit %d reached the store as %d, want %d", in, fc.limit, want)
		}
	}
	if ids, err := ops.SubmissionIDsMatching(ctx, pid, "ann"); err != nil || len(ids) != 1 {
		t.Fatalf("match = %v, %v", ids, err)
	}
	// The trimmed value is validated and searched.
	fc.found = nil
	if _, _, err := ops.FindSubmissions(ctx, pid, "  ab", 10, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.SubmissionIDsMatching(ctx, pid, "ab  "); err != nil {
		t.Fatal(err)
	}
	if len(fc.found) != 2 || fc.found[0] != "ab" || fc.found[1] != "ab" {
		t.Fatalf("store searched %q, want trimmed ab twice", fc.found)
	}
	if _, _, err := ops.FindSubmissions(ctx, pid, " a ", 10, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf(`" a ": %v, want ErrInvalid`, err)
	}
}

func TestDeleteSubmissionsAuditsSelectorNotContents(t *testing.T) {
	ctx := context.Background()
	ops, fc, pid := newFormOps(t)
	n, err := ops.DeleteSubmissions(ctx, "mcp", pid, []string{"s1", "s2"}, "search: ann@example.com")
	if err != nil || n != 2 {
		t.Fatalf("delete = %d, %v", n, err)
	}
	a := fc.audits[0]
	if a.Actor != "mcp" || a.Action != "submission.delete" || a.Subject != "project/1" {
		t.Fatalf("audit = %+v", a)
	}
	if !strings.Contains(a.Detail, "search: ann@example.com") || !strings.Contains(a.Detail, "(2 ids requested)") {
		t.Fatalf("detail = %q, want selector and count", a.Detail)
	}
	if _, err := ops.DeleteSubmissions(ctx, "cli", 99, []string{"x"}, "ids"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown project: %v", err)
	}
	// Nothing to delete: no store call, no audit row.
	before := len(fc.audits)
	if n, err := ops.DeleteSubmissions(ctx, "cli", pid, nil, "ids"); err != nil || n != 0 || len(fc.audits) != before {
		t.Fatalf("empty delete = %d, %v, audits %d", n, err, len(fc.audits))
	}
}

func TestActiveFormRefusesArchivedAndUnknown(t *testing.T) {
	ops, fc, pid := newFormOps(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	fc.forms["live"] = store.Form{ProjectID: pid, Name: "live", Status: store.FormDraft}
	fc.forms["old"] = store.Form{ProjectID: pid, Name: "old", Status: store.FormDraft, ArchivedAt: &at}
	if f, err := ops.ActiveForm(ctx, pid, "live"); err != nil || f.Name != "live" {
		t.Fatalf("live = %+v, %v", f, err)
	}
	for _, c := range []struct {
		pid  int64
		name string
	}{{pid, "old"}, {pid, "nope"}, {pid + 99, "live"}} {
		if _, err := ops.ActiveForm(ctx, c.pid, c.name); !errors.Is(err, ErrNotFound) {
			t.Errorf("%+v: err = %v, want ErrNotFound", c, err)
		}
	}
	if _, err := ops.GetSubmission(ctx, pid+99, "live", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown project: %v", err)
	}
}
