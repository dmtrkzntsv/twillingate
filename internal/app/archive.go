package app

import (
	"context"
	"log/slog"

	"github.com/dmtrkzntsv/twillingate/internal/pipeline"
	"github.com/dmtrkzntsv/twillingate/internal/server"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// archivedForms writes an approved form's $form_submit event to the
// archive log too: submissions are written straight to the store, never
// through the pipeline (server.FormStore), so the log would miss them.
// A failed log write is logged; the submission stands.
type archivedForms struct {
	server.FormStore
	log    pipeline.Sink
	logger *slog.Logger
}

func (f archivedForms) WriteSubmission(ctx context.Context, n store.NewSubmission) (store.Form, bool, error) {
	form, inserted, err := f.FormStore.WriteSubmission(ctx, n)
	if err == nil && inserted && form.Status == store.FormApproved {
		if lerr := f.log.WriteEvents(ctx, []store.Event{n.StoredEvent()}); lerr != nil {
			f.logger.Error("archive: form submission not logged", "project", n.Submission.ProjectID, "form", n.Submission.Form, "error", lerr)
		}
	}
	return form, inserted, err
}
