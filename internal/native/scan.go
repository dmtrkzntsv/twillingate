package native

import (
	"context"
	"slices"
	"strings"
	"sync"
)

// scanParallel hands groups (row groups, or any other work items), in order, to `workers` goroutines and calls
// read(worker, g) for each from that worker's goroutine only, so read may
// keep per-worker state (a groupReader) indexed by worker without locks.
// Groups go out in file order, each to whichever worker is free first, so
// a file's groups spread over the workers: each worker opens a file once
// per run of its groups from it, and a worker that takes several groups of
// one file in a row reuses it. The first error stops every worker and is
// returned; so is ctx's error once it is done.
func scanParallel[T any](ctx context.Context, groups []T, workers int, read func(w int, g T) error) error {
	if len(groups) == 0 {
		return nil
	}
	workers = max(1, min(workers, len(groups)))
	work := make(chan T)
	done := make(chan struct{})
	var (
		once     sync.Once
		firstErr error
		wg       sync.WaitGroup
	)
	fail := func(err error) {
		once.Do(func() {
			firstErr = err
			close(done)
		})
	}
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for g := range work {
				select {
				case <-done:
					return
				case <-ctx.Done():
					fail(ctx.Err())
					return
				default:
				}
				if err := read(w, g); err != nil {
					fail(err)
					return
				}
			}
		}()
	}
feed:
	for _, g := range groups {
		if err := ctx.Err(); err != nil {
			fail(err)
			break
		}
		select {
		case work <- g:
		case <-done:
			break feed
		case <-ctx.Done():
			fail(ctx.Err())
			break feed
		}
	}
	close(work)
	wg.Wait()
	return firstErr
}

// expandCols resolves the requested names against one file's columns: a
// "prefix*" pattern becomes every file column with that prefix; a plain
// name becomes the file's column as Batch.col would find it (the file's
// spelling, which may differ in case), and stays as it is when the file
// has none (it then reads all null). Each name comes once, in the order
// first asked for.
func expandCols(fileCols []string, want []string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(n string) {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for _, w := range want {
		if prefix, ok := strings.CutSuffix(w, "*"); ok {
			for _, c := range fileCols {
				if strings.HasPrefix(c, prefix) {
					add(c)
				}
			}
			continue
		}
		if n, ok := resolveName(w, slices.Values(fileCols)); ok {
			w = n
		}
		add(w)
	}
	return out
}

// groupStats is what a row group's statistics say about family and day
// (days since 1970-01-01): their bounds, ok false when a column has none,
// and how many of the group's values are null.
type groupStats struct {
	famOK          bool
	famMin, famMax string
	famNulls       int64
	dayOK          bool
	dayMin, dayMax int64
	dayNulls       int64
}

// decide says whether a row group may hold rows of family (any, when "")
// with day in [from, to] (keep), and whether every row in it does (all),
// so the row filter can be skipped. The bounds leave nulls out, and a null
// family or day matches no filter, so all needs none (of family only when
// filtering by it).
func (s groupStats) decide(family string, from, to int64) (keep, all bool) {
	if family != "" && s.famOK && (family < s.famMin || family > s.famMax) {
		return false, false
	}
	if s.dayOK && (s.dayMax < from || s.dayMin > to) {
		return false, false
	}
	famAll := family == "" || (s.famOK && s.famNulls == 0 && s.famMin == family && s.famMax == family)
	return true, famAll && s.dayOK && s.dayNulls == 0 && s.dayMin >= from && s.dayMax <= to
}

// mayHold says whether a row group may hold rows of any of families: its
// family bounds, when it has them, take one of them in.
func (s groupStats) mayHold(families []string) bool {
	if !s.famOK {
		return true
	}
	for _, f := range families {
		if f >= s.famMin && f <= s.famMax {
			return true
		}
	}
	return false
}

// filterBatch keeps the rows whose family is family (any, when "") and
// whose day is in [from, to]; b itself when every row stays.
func filterBatch(b *Batch, family string, from, to int64) *Batch {
	fam, day := b.Cols["family"], b.Cols["day"]
	var match []bool
	if family != "" {
		match = make([]bool, len(fam.Dict))
		for i, s := range fam.Dict {
			match[i] = s == family
		}
	}
	sel := make([]int32, 0, b.N)
	for i := range b.N {
		if match != nil {
			if f := fam.Idx[i]; f < 0 || !match[f] {
				continue
			}
		}
		if day.Null != nil && day.Null[i] {
			continue
		}
		if d := day.Ints[i]; d < from || d > to {
			continue
		}
		sel = append(sel, int32(i))
	}
	if len(sel) == b.N {
		return b
	}
	out := &Batch{N: len(sel), Cols: make(map[string]*Column, len(b.Cols))}
	for name, c := range b.Cols {
		out.Cols[name] = c.take(sel)
	}
	return out
}

// finish runs the batch through the filter unless all says every row
// matches, and drops the columns read only for it.
func finish(b *Batch, requested []string, all bool, family string, from, to int64) *Batch {
	if !all {
		b = filterBatch(b, family, from, to)
	}
	if len(b.Cols) != len(requested) {
		keep := make(map[string]bool, len(requested))
		for _, n := range requested {
			keep[n] = true
		}
		for n := range b.Cols {
			if !keep[n] {
				delete(b.Cols, n)
			}
		}
	}
	return b
}
