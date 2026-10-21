package app

import (
	"context"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/internal/store/storetest"
)

// TestDeleteSubmissionsLeavesTheFiles: deleting a submission removes the
// row and leaves its $form_submit event in the log, which holds no field
// (raw-first PR 6, Decision 11).
func TestDeleteSubmissionsLeavesTheFiles(t *testing.T) {
	ctx := context.Background()
	data := t.TempDir()
	st := storetest.Open(t, filepath.Join(data, "twillingate.db"))
	reg := manage.New(st, slog.Default())
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := manage.NewOps(reg, st).CreateProject(ctx, "test", manage.ProjectSpec{Name: "App"}); err != nil {
		t.Fatal(err)
	}
	log, err := archive.OpenLog(filepath.Join(data, "raw"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	at := time.Now().UTC().Truncate(time.Second)
	ev := store.Event{ID: "00000000-0000-4000-a000-000000000001", ProjectID: 1, Family: store.FamilyProduct,
		EventName: store.FormSubmitEvent, TS: at, ReceivedAt: at, Attributes: map[string]string{"form": "contact"}}
	if err := log.WriteEvents(ctx, []store.Event{ev}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.WriteSubmission(ctx, store.NewSubmission{
		Submission: store.Submission{ProjectID: 1, ID: ev.ID, Form: "contact", ReceivedAt: at, Fields: map[string]string{"email": "a@x.io"}},
		DraftUntil: at.Add(7 * 24 * time.Hour),
		Event:      ev,
	}); err != nil {
		t.Fatal(err)
	}

	n, err := st.DeleteSubmissions(ctx, 1, []string{ev.ID}, store.AuditEntry{Actor: "test", Action: "submission.delete", Subject: "project/1", Detail: "ids"})
	if err != nil || n != 1 {
		t.Fatalf("deleted %d, %v; want 1", n, err)
	}
	if got, err := st.DeleteSubmissions(ctx, 1, []string{ev.ID}, store.AuditEntry{Actor: "test", Action: "submission.delete", Subject: "project/1", Detail: "ids"}); err != nil || got != 0 {
		t.Fatalf("second delete = %d, %v; want 0 (the submission is gone)", got, err)
	}
	if names := logEventNames(t, filepath.Join(data, "raw")); !slices.Equal(names, []string{store.FormSubmitEvent}) {
		t.Fatalf("logged events after the delete = %v, want the $form_submit line", names)
	}
}
