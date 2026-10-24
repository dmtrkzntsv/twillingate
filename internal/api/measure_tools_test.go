//go:build measure

// The analytics tools on the native engine against the SQL they ran before
// (native engine step 3f), each once over one range of a generated
// install's files, in one process:
//
//	MEASURE_DIR=~/.cache/twbench-native/3f/1m GOMAXPROCS=2 \
//	  go test -tags measure -run TestMeasureTools -v ./internal/api
//
// MEASURE_DIR holds raw/ as internal/app's TestMeasureSystemDashboard
// generates it; MEASURE_FROM and MEASURE_TO (default 2026-09-01 and
// 2026-09-30) bound the range. Each tool runs twice on each side, the
// first and the second time; the old SQL goes through filesTable (the
// parity harness's), so through archive.Reader and an engine child at the
// server's defaults (threads = GOMAXPROCS, 512 MB), which a throwaway
// query starts before the first timing. Not part of make check.
package api

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/archive/archivetest"
	"github.com/dmtrkzntsv/twillingate/internal/engine"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/shared/enginesql"
	"github.com/dmtrkzntsv/twillingate/internal/store/storetest"
)

func TestMeasureTools(t *testing.T) {
	dir := os.Getenv("MEASURE_DIR")
	if dir == "" {
		t.Skip("MEASURE_DIR is not set")
	}
	raw := filepath.Join(dir, "raw")
	work := t.TempDir()
	ctx := context.Background()
	st := storetest.Open(t, filepath.Join(work, "twillingate.db"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := manage.New(st, logger)
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	ops := manage.NewOps(reg, st)
	if p, err := ops.CreateProject(ctx, "bench", manage.ProjectSpec{Name: "bench"}); err != nil || p.ID != 1 {
		t.Fatalf("project: %v, %v", p, err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.New(engine.Options{Path: self, Env: []string{archivetest.ChildEnv + "=1"}, AllowedDirs: []string{raw},
		SpillRoot: filepath.Join(work, "spill"), SlotDir: filepath.Join(work, "slots"), MaxChildren: 2, MemoryLimit: "512MB"})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	h := &host{reg: reg, ops: ops, sql: archivetest.ServeSQL(t, raw, eng).Client(t), native: native.New(native.Options{Dir: raw}), eventsDir: raw,
		queryTimeout: 10 * time.Minute, queryRows: 1000, logger: logger}
	r := rangeIn{ProjectID: 1, From: envOrDefault("MEASURE_FROM", "2026-09-01"), To: envOrDefault("MEASURE_TO", "2026-09-30")}
	text := func(s string) enginesql.Value { return enginesql.Value{Type: "VARCHAR", Text: s} }
	old := func(q string, vals map[string]enginesql.Value) func() error {
		return func() error { _, err := h.filesTable(ctx, r, q, vals); return err }
	}
	if err := old("SELECT count(*) FROM events WHERE project_id = :project AND day BETWEEN :from AND :to", nil)(); err != nil {
		t.Fatal(err)
	}
	type tool struct {
		name   string
		native func() error
		sql    []func() error // the old SQL, one query or several run in turn
	}
	tools := []tool{
		{"views_overview", func() error { _, err := h.viewsOverview(ctx, overviewIn{rangeIn: r}); return err },
			[]func() error{old(oldViewsOverviewSQL(""), nil)}},
		{"product_events", func() error { _, err := h.productEvents(ctx, productEventsIn{rangeIn: r}); return err },
			[]func() error{old(oldProductEventsSQL("", `day, "count" DESC, event_name`), nil), old(oldProductTotalsSQL, nil)}},
		{"event_names", func() error { _, err := h.eventNames(ctx, r); return err },
			[]func() error{old(oldEventNamesSQL, nil)}},
		{"product_attributes", func() error { _, err := h.productAttributes(ctx, productEventsIn{rangeIn: r}); return err },
			[]func() error{old(oldProductAttributesSQL(""), nil)}},
		{"measures", func() error { _, err := h.measures(ctx, measuresIn{rangeIn: r}); return err },
			[]func() error{old(oldMeasuresSQL("", ""), nil)}},
		{"retention (user)", func() error { _, err := h.retention(ctx, retentionIn{rangeIn: r, Actor: "user"}); return err },
			[]func() error{old(oldRetentionSQL, map[string]enginesql.Value{":actor": text("user")})}},
		{"identities (user)", func() error { _, err := h.identities(ctx, identitiesIn{rangeIn: r, Kind: "user"}); return err },
			[]func() error{old(oldIdentitiesSQL("user"), map[string]enginesql.Value{":kind": text("user"), ":limit": {Type: "BIGINT", Text: "50"}})}},
		{"identities (group)", func() error { _, err := h.identities(ctx, identitiesIn{rangeIn: r, Kind: "group"}); return err },
			[]func() error{old(oldIdentitiesSQL("group"), map[string]enginesql.Value{":kind": text("group"), ":limit": {Type: "BIGINT", Text: "50"}})}},
	}
	dims := make([]string, 0, len(oldViewsDimensions))
	for name := range oldViewsDimensions {
		dims = append(dims, name)
	}
	slices.Sort(dims)
	for _, name := range dims {
		dim := oldViewsDimensions[name]
		tools = append(tools, tool{"views_breakdown " + name,
			func() error {
				_, err := h.viewsBreakdown(ctx, breakdownIn{rangeIn: r, Dimension: name, Limit: 20})
				return err
			},
			[]func() error{old(oldViewsBreakdownSQL(dim), map[string]enginesql.Value{":limit": {Type: "BIGINT", Text: "20"}})}})
	}
	timed := func(fs ...func() error) (time.Duration, error) {
		began := time.Now()
		for _, f := range fs {
			if err := f(); err != nil {
				return 0, err
			}
		}
		return time.Since(began), nil
	}
	ms := func(d time.Duration) string { return strconv.FormatFloat(float64(d.Microseconds())/1000, 'f', 1, 64) }
	fmt.Printf("| Tool | Native first | Native second | Old SQL first | Old SQL second |\n| --- | --- | --- | --- | --- |\n")
	for _, tl := range tools {
		var cells []string
		for _, run := range [][]func() error{{tl.native}, {tl.native}, tl.sql, tl.sql} {
			d, err := timed(run...)
			if err != nil {
				t.Fatalf("%s: %v", tl.name, err)
			}
			cells = append(cells, ms(d)+" ms")
		}
		fmt.Printf("| %s | %s | %s | %s | %s |\n", tl.name, cells[0], cells[1], cells[2], cells[3])
	}
}

func envOrDefault(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
