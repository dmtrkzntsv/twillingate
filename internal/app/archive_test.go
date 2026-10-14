package app

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type fakeForms struct {
	form     store.Form
	inserted bool
}

func (f fakeForms) WriteSubmission(ctx context.Context, n store.NewSubmission) (store.Form, bool, error) {
	return f.form, f.inserted, nil
}

func (f fakeForms) SessionAt(ctx context.Context, projectID int64, actorKind, actorID string, at time.Time) (*store.Event, error) {
	return nil, nil
}

type logSink struct{ got []store.Event }

func (s *logSink) WriteEvents(ctx context.Context, evs []store.Event) error {
	s.got = append(s.got, evs...)
	return nil
}

func TestArchivedFormsLogsOnlyAnApprovedNewSubmission(t *testing.T) {
	at := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	n := store.NewSubmission{Submission: store.Submission{ProjectID: 1, ID: "s1", Form: "f", ReceivedAt: at}}
	for _, c := range []struct {
		name     string
		status   string
		inserted bool
		want     int
	}{
		{"approved, new", store.FormApproved, true, 1},
		{"approved, a repeat", store.FormApproved, false, 0},
		{"draft", store.FormDraft, true, 0},
	} {
		log := &logSink{}
		f := archivedForms{FormStore: fakeForms{store.Form{Status: c.status}, c.inserted}, log: log, logger: discardLogger()}
		if _, _, err := f.WriteSubmission(context.Background(), n); err != nil {
			t.Fatal(err)
		}
		if len(log.got) != c.want {
			t.Errorf("%s: logged %d events, want %d", c.name, len(log.got), c.want)
		}
	}
}
