package reporting

import (
	"encoding/json"
	"slices"

	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Narrow is what a copy of query widgets is narrowed to: filters every
// row they read must also pass, and a project they read whatever the
// switcher says (0: as the source has it). duplicate_dashboard and
// copy_widget take it, so a built-in dashboard copied for one segment
// (users who were ever on the cloud host, no test traffic) needs no SQL.
type Narrow struct {
	Where     []native.Filter
	ProjectID int64
}

// empty says whether n narrows nothing.
func (n Narrow) empty() bool { return len(n.Where) == 0 && n.ProjectID == 0 }

// narrowed is w with n applied: a query widget's query gains n's filters
// before its own, and n's project; a Markdown widget is as it is. A sql
// widget is refused, its SQL being out of n's reach, as is a query the
// filters do not fit (a column its table lacks).
func narrowed(w store.Widget, n Narrow) (store.Widget, error) {
	if n.empty() || w.SourceType == "md" {
		return w, nil
	}
	if n.ProjectID < 0 {
		return store.Widget{}, store.Refuse(store.ErrInvalid, "project_id: a project's id is 1 or more")
	}
	if w.SourceType != "query" {
		return store.Widget{}, store.Refuse(store.ErrInvalid,
			"widget %s is %s: where and project_id narrow query widgets only; copy it without them, or write it as a query", w.Name, w.SourceType)
	}
	qc, _, _, err := parseQuery(w.Source)
	if err != nil {
		return store.Widget{}, err
	}
	qc.Query.Where = slices.Concat(n.Where, qc.Query.Where)
	if n.ProjectID > 0 {
		qc.Project = n.ProjectID
	}
	if err := qc.Query.Validate(); err != nil {
		return store.Widget{}, store.Refuse(store.ErrInvalid, "widget %s: %s", w.Name, queryErr(err).Error())
	}
	b, err := json.Marshal(qc)
	if err != nil {
		return store.Widget{}, err
	}
	w.Source = string(b)
	return w, nil
}
