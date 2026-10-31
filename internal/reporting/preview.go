package reporting

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// PreviewIn is an unsaved widget the console's editor runs (spec D1):
// its component, props and sql, and the project and range to bind.
// ProjectID and From/To matter only when the sql uses them.
type PreviewIn struct {
	Component string
	Props     json.RawMessage // nil or empty means {}
	Source    Source
	ProjectID int64
	From, To  string
}

// Preview is the run's answer: the rows as widget_data returns them, and
// the first way they do not fit the component (props, columns, then
// value types), which does not withhold the rows (D4): the editor shows
// the table and why the chart will not draw.
type Preview struct {
	readsql.Result
	Problem   string `json:"problem,omitempty"`
	ElapsedMS int64  `json:"elapsed_ms"`
}

// Preview runs an unsaved widget's sql or query with the values given rather than
// sample ones, uncached and unaudited. What keeps it from running at all
// is refused as add_widget refuses it (D2); markdown has nothing to run.
func (s *Service) Preview(ctx context.Context, in PreviewIn) (Preview, error) {
	comps, err := s.components(ctx)
	if err != nil {
		return Preview{}, err
	}
	comp, err := componentNamed(comps, in.Component)
	if err != nil {
		return Preview{}, err
	}
	if in.Source.Type != "sql" && in.Source.Type != "query" {
		return Preview{}, store.Refuse(store.ErrInvalid, "preview runs sql and query only; markdown is drawn as it is typed")
	}
	if in.Source.Type == "sql" && s.sql == nil {
		return Preview{}, ErrSQLEngine
	}
	if err := checkAccepts(comp, in.Source.Type); err != nil {
		return Preview{}, err
	}
	if err := checkRemoteTable(store.Widget{Component: in.Component, Props: string(in.Props), SourceType: in.Source.Type}); err != nil {
		return Preview{}, err
	}
	start := time.Now()
	res, err := s.previewRows(ctx, in)
	if err != nil {
		return Preview{}, err
	}
	out := Preview{Result: res, ElapsedMS: time.Since(start).Milliseconds()}
	props := in.Props
	if len(props) == 0 {
		props = json.RawMessage(`{}`)
	}
	if err := comp.checkProps(props); err != nil {
		out.Problem = err.Error()
	} else if err := comp.checkColumns(res.Columns); err != nil {
		out.Problem = err.Error()
	} else if err := comp.checkRows(res); err != nil {
		out.Problem = err.Error()
	}
	return out, nil
}

// previewRows runs in's source with the values in gives: sql binding the
// parameters it uses, a query over the range and the project (its own,
// when its content names one).
func (s *Service) previewRows(ctx context.Context, in PreviewIn) (readsql.Result, error) {
	if in.Source.Type == "query" {
		qs, ok := s.sources["query"].(*querySource)
		if !ok {
			return readsql.Result{}, errors.New("reporting: the query source type is not registered")
		}
		qc, _, _, err := parseQuery(in.Source.Content)
		if err != nil {
			return readsql.Result{}, err
		}
		p, err := s.previewQueryParams(in, qc.Project > 0)
		if err != nil {
			return readsql.Result{}, err
		}
		return qs.load(ctx, in.Source.Content, p)
	}
	params, err := checkSQLParams(in.Source.Content)
	if err != nil {
		return readsql.Result{}, err
	}
	p, err := s.previewParams(params, in)
	if err != nil {
		return readsql.Result{}, err
	}
	return loadRows(ctx, s.sql, in.Source.Content, params, p)
}

// previewQueryParams is previewParams for a query, which follows the
// range, and the project unless its content fixes one (fixed).
func (s *Service) previewQueryParams(in PreviewIn, fixed bool) (Params, error) {
	if in.ProjectID == 0 && !fixed {
		return Params{}, store.Refuse(store.ErrInvalid, "a query follows the project; pass project_id")
	}
	if in.From == "" && in.To == "" {
		return Params{}, store.Refuse(store.ErrInvalid, "a query follows the date range; pass from and to")
	}
	from, to, err := checkRange(in.From, in.To, civil.Today(s.now()))
	if err != nil {
		return Params{}, err
	}
	return Params{ProjectID: in.ProjectID, From: from, To: to}, nil
}

// previewParams resolves the values to bind for exactly the parameters
// the sql uses: a missing one is refused, the range is checked and a
// future "to" clamped as widget_data does.
func (s *Service) previewParams(params []string, in PreviewIn) (Params, error) {
	var p Params
	for _, name := range params {
		switch name {
		case ":project":
			if in.ProjectID == 0 {
				return Params{}, store.Refuse(store.ErrInvalid, "the sql uses :project; pass project_id")
			}
			p.ProjectID = in.ProjectID
		case ":from", ":to":
			if p.From != "" {
				continue
			}
			if in.From == "" && in.To == "" {
				return Params{}, store.Refuse(store.ErrInvalid, "the sql uses :from or :to; pass from and to")
			}
			from, to, err := checkRange(in.From, in.To, civil.Today(s.now()))
			if err != nil {
				return Params{}, err
			}
			p.From, p.To = from, to
		}
	}
	return p, nil
}
