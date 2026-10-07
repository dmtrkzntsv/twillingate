package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// formDB is a database with project 1 (allowing https://shop.example.com)
// and a draft form "contact" holding two submissions.
func formDB(t *testing.T) {
	t.Helper()
	withDB(t)
	var out bytes.Buffer
	if code := run([]string{"project", "create", "-name", "Shop",
		"-origin", "https://shop.example.com"}, &out); code != 0 {
		t.Fatal(out.String())
	}
	seedSubmission(t, "s1", map[string]string{"email": "ann@example.com", "message": "=HYPERLINK(\"x\")"})
	seedSubmission(t, "s2", map[string]string{"email": "bob@example.com", "message": "hi"})
}

func openStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.Open(os.Getenv("DATABASE_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// seedSubmission writes a submission to project 1's form "contact"; the
// later the id, the later it arrived.
func seedSubmission(t *testing.T, id string, fields map[string]string) {
	t.Helper()
	at := time.Now().UTC().Add(-time.Hour)
	if id == "s2" {
		at = at.Add(time.Minute)
	}
	_, _, err := openStore(t).WriteSubmission(context.Background(), store.NewSubmission{
		Submission: store.Submission{
			ProjectID: 1, ID: id, Form: "contact", ReceivedAt: at, Fields: fields,
			ActorKind: "user", ActorID: "a1", Host: "shop.example.com", Path: "/contact", Via: "form",
		},
		DraftUntil: at.Add(7 * 24 * time.Hour),
		Event: store.Event{
			ID: id, ProjectID: 1, Family: store.FamilyProduct, EventName: "$form_submit",
			TS: at, ReceivedAt: at, Kind: "web", ActorKind: "user", ActorID: "a1",
			Host: "shop.example.com", Path: "/contact", Attributes: map[string]string{"form": "contact"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func getForm(t *testing.T) store.Form {
	t.Helper()
	f, err := openStore(t).GetForm(context.Background(), 1, "contact")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func runForm(t *testing.T, want int, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	if code := run(append([]string{"form"}, args...), &out); code != want {
		t.Fatalf("form %v: exit %d, want %d: %s", args, code, want, out.String())
	}
	return out.String()
}

func TestFormList(t *testing.T) {
	formDB(t)
	out := runForm(t, 0, "list", "-project", "1")
	if !strings.HasPrefix(out, "contact\tdraft\t2\t") || !strings.Contains(out, "email,message") {
		t.Fatalf("list output: %q", out)
	}
	if out := runForm(t, 0, "list", "-project", "1", "-archived"); out != "" {
		t.Fatalf("archived list of an unarchived project: %q", out)
	}
	runForm(t, 2, "list")
	if out := runForm(t, 1, "list", "-project", "9"); !strings.Contains(out, "unknown project 9") {
		t.Fatalf("unknown project: %q", out)
	}
}

func TestFormApproveKeepsExpectedFields(t *testing.T) {
	formDB(t)
	runForm(t, 2, "approve", "-project", "1", "-name", "contact") // -fields required
	out := runForm(t, 0, "approve", "-project", "1", "-name", "contact", "-fields", "email, message")
	if !strings.Contains(out, `form "contact" approved`) {
		t.Fatalf("approve output: %q", out)
	}
	f := getForm(t)
	if f.Status != store.FormApproved || strings.Join(f.ExpectedFields, ",") != "email,message" {
		t.Fatalf("form after approve: %+v", f)
	}
	// An approved form cannot be approved again.
	if out := runForm(t, 1, "approve", "-project", "1", "-name", "contact", "-fields", "email"); out == "" {
		t.Fatal("refusal printed nothing")
	}
	if out := runForm(t, 1, "approve", "-project", "1", "-name", "nope", "-fields", "email"); !strings.Contains(out, "not found") {
		t.Fatalf("unknown form: %q", out)
	}
}

func TestFormUpdate(t *testing.T) {
	formDB(t)
	runForm(t, 0, "update", "-project", "1", "-name", "contact",
		"-purpose", "Sales enquiries", "-return-url", "https://shop.example.com/thanks",
		"-closes-at", "2031-01-02T03:04:05Z")
	f := getForm(t)
	if f.Purpose != "Sales enquiries" || f.ReturnURL != "https://shop.example.com/thanks" ||
		f.ClosesAt == nil || !f.ClosesAt.Equal(time.Date(2031, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("after update: %+v", f)
	}

	// A flag left out keeps the value; an empty one clears it.
	runForm(t, 0, "update", "-project", "1", "-name", "contact", "-return-url", "")
	f = getForm(t)
	if f.ReturnURL != "" || f.Purpose != "Sales enquiries" || f.ClosesAt == nil {
		t.Fatalf("after clearing return_url: %+v", f)
	}

	runForm(t, 0, "update", "-project", "1", "-name", "contact", "-closes-at", "never")
	if f = getForm(t); f.ClosesAt != nil {
		t.Fatalf("never did not reopen: %v", f.ClosesAt)
	}
	before := time.Now().UTC().Add(-time.Second)
	runForm(t, 0, "update", "-project", "1", "-name", "contact", "-closes-at", "now")
	if f = getForm(t); f.ClosesAt == nil || f.ClosesAt.Before(before) || f.Open(time.Now().Add(time.Second)) {
		t.Fatalf("now did not close the form: %v", f.ClosesAt)
	}

	// -fields on an approved form.
	runForm(t, 0, "approve", "-project", "1", "-name", "contact", "-fields", "email")
	runForm(t, 0, "update", "-project", "1", "-name", "contact", "-fields", "email,message")
	if f = getForm(t); strings.Join(f.ExpectedFields, ",") != "email,message" {
		t.Fatalf("expected fields: %v", f.ExpectedFields)
	}
}

func TestFormUpdateRefusals(t *testing.T) {
	formDB(t)
	if out := runForm(t, 1, "update", "-project", "1", "-name", "contact",
		"-return-url", "https://evil.example.org/"); !strings.Contains(out, "return_url") {
		t.Fatalf("return_url refusal: %q", out)
	}
	if out := runForm(t, 1, "update", "-project", "1", "-name", "contact", "-fields", "email"); !strings.Contains(out, "draft") {
		t.Fatalf("-fields on a draft: %q", out)
	}
	if out := runForm(t, 2, "update", "-project", "1", "-name", "contact", "-closes-at", "tomorrow"); !strings.Contains(out, "-closes-at") {
		t.Fatalf("bad -closes-at: %q", out)
	}
	if out := runForm(t, 2, "update", "-project", "1", "-name", "contact"); !strings.Contains(out, "nothing to update") {
		t.Fatalf("no changes: %q", out)
	}
	runForm(t, 1, "update", "-project", "1", "-name", "nope", "-purpose", "x")
}

func TestFormArchiveRestore(t *testing.T) {
	formDB(t)
	runForm(t, 0, "archive", "-project", "1", "-name", "contact")
	if f := getForm(t); f.ArchivedAt == nil {
		t.Fatal("not archived")
	}
	if out := runForm(t, 0, "list", "-project", "1", "-archived"); !strings.HasPrefix(out, "contact\t") {
		t.Fatalf("archived list: %q", out)
	}
	runForm(t, 0, "restore", "-project", "1", "-name", "contact")
	if f := getForm(t); f.ArchivedAt != nil {
		t.Fatal("not restored")
	}
	runForm(t, 2, "archive", "-project", "1")
	runForm(t, 1, "restore", "-project", "1", "-name", "nope")
}

func TestFormExportWritesInertCSV(t *testing.T) {
	formDB(t)
	out := runForm(t, 0, "export", "-project", "1", "-name", "contact")
	recs, err := csv.NewReader(strings.NewReader(out)).ReadAll()
	if err != nil {
		t.Fatalf("not CSV: %v\n%s", err, out)
	}
	if len(recs) != 3 {
		t.Fatalf("records = %d, want header and two rows: %q", len(recs), out)
	}
	if strings.Join(recs[0], ",") != "Received,email,message,Page,Referrer,UTM source,UTM medium,UTM campaign" {
		t.Fatalf("header: %q", recs[0])
	}
	// Newest first; a cell that would run as a formula is made text.
	if recs[1][1] != "bob@example.com" || recs[2][1] != "ann@example.com" {
		t.Fatalf("order: %q", recs)
	}
	if recs[2][2] != `'=HYPERLINK("x")` || recs[2][3] != "shop.example.com/contact" {
		t.Fatalf("row: %q", recs[2])
	}

	runForm(t, 0, "archive", "-project", "1", "-name", "contact")
	if out := runForm(t, 1, "export", "-project", "1", "-name", "contact"); !strings.Contains(out, "archived") {
		t.Fatalf("archived export: %q", out)
	}
	runForm(t, 2, "export", "-project", "1")
}

func TestFormEraseByIDAndSearch(t *testing.T) {
	formDB(t)
	runForm(t, 2, "erase", "-project", "1")
	runForm(t, 2, "erase", "-project", "1", "-id", "s1", "-search", "ann@")
	if out := runForm(t, 1, "erase", "-project", "1", "-search", "a"); !strings.Contains(out, "at least 2") {
		t.Fatalf("short search: %q", out)
	}

	out := runForm(t, 0, "erase", "-project", "1", "-search", "ANN@example")
	if !strings.Contains(out, "1 submission erased") || strings.Contains(strings.ToLower(out), "ann@") {
		t.Fatalf("search erase output: %q", out)
	}
	list := runForm(t, 0, "list", "-project", "1")
	if !strings.HasPrefix(list, "contact\tdraft\t1\t") {
		t.Fatalf("after search erase: %q", list)
	}

	out = runForm(t, 0, "erase", "-project", "1", "-id", "s2", "-id", "unknown")
	if !strings.Contains(out, "1 submission erased") {
		t.Fatalf("id erase output: %q", out)
	}
	if list := runForm(t, 0, "list", "-project", "1"); !strings.HasPrefix(list, "contact\tdraft\t0\t") {
		t.Fatalf("after id erase: %q", list)
	}
	if out := runForm(t, 0, "erase", "-project", "1", "-search", "nobody@"); !strings.Contains(out, "0 submissions erased") {
		t.Fatalf("nothing matched: %q", out)
	}
}

func TestFormUsage(t *testing.T) {
	withDB(t)
	runForm(t, 2)
	if out := runForm(t, 2, "frobnicate"); !strings.Contains(out, "unknown subcommand") {
		t.Fatalf("unknown subcommand: %q", out)
	}
}
