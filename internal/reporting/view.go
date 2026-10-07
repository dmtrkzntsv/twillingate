package reporting

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// FilterSpec is one filter as a remote table's page block echoes it.
// Value is a string, or a []string for "in" and "not in".
type FilterSpec struct {
	Column string `json:"column"`
	Op     string `json:"op"`
	Value  any    `json:"value"`
}

// PageInfo is a remote table's page block: what was applied (Limit is
// the applied size, CONSOLE_QUERY_MAX_ROWS when none was asked for), and
// how many rows matched the filters out of how many the query returns.
// Filters is always a list, empty when there are none, so a caller can
// read it without a nil check.
type PageInfo struct {
	Offset   int          `json:"offset"`
	Limit    int          `json:"limit"`
	Matched  int          `json:"matched"`
	Total    int          `json:"total"`
	Sort     string       `json:"sort,omitempty"`
	Distinct string       `json:"distinct,omitempty"`
	Filters  []FilterSpec `json:"filters"`
}

// view is a DataRequest's paging arguments, parsed: page for readsql,
// echo for the envelope (Matched and Total are filled after the run),
// and present when the caller sent any of them, which only a remote
// table accepts.
type view struct {
	page    readsql.Page
	echo    PageInfo
	present bool
}

// remoteTable reports whether w is a table whose props say "mode":
// "remote", the only widget that filters, sorts and pages on the server.
// An unmarshal error reads as local: props were validated when the
// widget was saved, so there is nothing more to say about them here.
func remoteTable(w store.Widget) bool {
	if w.Component != "table" {
		return false
	}
	var props struct {
		Mode string `json:"mode"`
	}
	_ = json.Unmarshal([]byte(w.Props), &props)
	return props.Mode == "remote"
}

// parseView parses in's paging arguments against a page size cap of
// maxRows. It refuses what it can tell from the arguments alone (the
// filters' JSON and operators, the sort's shape, the ranges); a column
// the query does not return is left to QueryPage, which knows the
// columns.
func parseView(in DataRequest, maxRows int) (view, error) {
	v := view{
		present: in.Filters != "" || in.Sort != "" || in.Distinct != "" || in.Offset != 0 || in.Limit != 0,
		echo:    PageInfo{Offset: in.Offset, Limit: in.Limit, Distinct: in.Distinct, Filters: []FilterSpec{}},
	}
	if in.Filters != "" {
		var raw []struct {
			Column string          `json:"column"`
			Op     string          `json:"op"`
			Value  json.RawMessage `json:"value"`
		}
		if err := json.Unmarshal([]byte(in.Filters), &raw); err != nil {
			return view{}, store.Refuse(store.ErrInvalid,
				"filters must be a JSON list of {column, op, value}: %s", err)
		}
		for _, r := range raw {
			f := readsql.Filter{Column: r.Column, Op: r.Op}
			// Checked by the first byte, since Unmarshal leaves a string
			// empty, not refused, for a JSON null.
			value := strings.TrimSpace(string(r.Value))
			isList := strings.HasPrefix(value, "[")
			switch r.Op {
			case "=", "!=", "<", ">":
				if isList {
					return view{}, store.Refuse(store.ErrInvalid,
						"filter on %q: %s takes one value, not a list", r.Column, r.Op)
				}
				var s string
				if !strings.HasPrefix(value, `"`) || json.Unmarshal(r.Value, &s) != nil {
					return view{}, store.Refuse(store.ErrInvalid,
						"filter on %q: the value must be a string (quote a number: \"100\")", r.Column)
				}
				f.Values = []string{s}
				v.echo.Filters = append(v.echo.Filters, FilterSpec{Column: r.Column, Op: r.Op, Value: s})
			case "in", "not in":
				var list []string
				if !isList || json.Unmarshal(r.Value, &list) != nil {
					return view{}, store.Refuse(store.ErrInvalid,
						"filter on %q: %s takes a list of strings (quote numbers: [\"1\", \"2\"])", r.Column, r.Op)
				}
				if len(list) == 0 {
					return view{}, store.Refuse(store.ErrInvalid,
						"filter on %q: %s needs at least one value", r.Column, r.Op)
				}
				f.Values = list
				v.echo.Filters = append(v.echo.Filters, FilterSpec{Column: r.Column, Op: r.Op, Value: list})
			default:
				return view{}, store.Refuse(store.ErrInvalid,
					"filter on %q: op %q is not one of =, !=, <, >, in, not in", r.Column, r.Op)
			}
			v.page.Filters = append(v.page.Filters, f)
		}
	}
	if in.Sort != "" {
		// The last ':' splits, since a column name may hold one itself.
		i := strings.LastIndex(in.Sort, ":")
		dir := in.Sort[i+1:]
		if i <= 0 || (dir != "asc" && dir != "desc") {
			return view{}, store.Refuse(store.ErrInvalid, "sort %q: use <column>:asc or <column>:desc", in.Sort)
		}
		v.page.Sort = &readsql.Sort{Column: in.Sort[:i], Desc: dir == "desc"}
		v.echo.Sort = in.Sort
	}
	// Refused here as well as in QueryPage, so the refusal reads as the
	// caller's arguments rather than as a load that failed.
	if in.Distinct != "" && in.Sort != "" {
		return view{}, store.Refuse(store.ErrInvalid, "distinct and sort cannot be combined; drop one")
	}
	if in.Offset < 0 {
		return view{}, store.Refuse(store.ErrInvalid, "offset must not be negative")
	}
	if in.Limit < 0 || in.Limit > maxRows {
		return view{}, store.Refuse(store.ErrInvalid, "limit must be between 1 and %d (CONSOLE_QUERY_MAX_ROWS)", maxRows)
	}
	if v.echo.Limit == 0 {
		v.echo.Limit = maxRows
	}
	v.page.Distinct, v.page.Offset, v.page.Limit = in.Distinct, in.Offset, v.echo.Limit
	return v, nil
}

// ParsePage parses a remote table's paging arguments exactly as
// widget_data does (parseView), for a table the server builds itself
// rather than a widget's: list_submissions takes the same arguments and
// refuses the same malformed ones. It returns the page for
// readsql.QueryPage and the block echoing it (Matched and Total left for
// the caller to fill).
func ParsePage(filters, sort, distinct string, offset, limit, maxRows int) (readsql.Page, PageInfo, error) {
	v, err := parseView(DataRequest{Filters: filters, Sort: sort, Distinct: distinct, Offset: offset, Limit: limit}, maxRows)
	return v.page, v.echo, err
}

// cacheSuffix is a remote table's part of its cache key. It hashes the
// parsed values rather than the raw arguments, so the same filters
// spelled with different whitespace share one entry, and an absent limit
// shares with an explicit one at the cap. Filters stay in the order
// given, the order the viewer added them. It is not "" even with no
// paging argument: a remote table caches a page where a local one caches
// the whole result, and switching a widget's mode leaves its source, and
// so the rest of its key, as it was.
func (v view) cacheSuffix() string {
	b, err := json.Marshal(struct {
		F    []FilterSpec
		S, D string
		O, L int
	}{v.echo.Filters, v.echo.Sort, v.echo.Distinct, v.echo.Offset, v.echo.Limit})
	if err != nil { // strings and ints only: Marshal cannot fail on them
		panic(err)
	}
	return fmt.Sprintf(":view=%x", sha256.Sum256(b))
}
