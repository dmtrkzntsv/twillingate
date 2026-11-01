package native

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/archive/archivetest"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// wantRows sorts got (native's rows of q's columns cols) by id and
// returns the golden's rows of q under rules.
func wantRows(t *testing.T, q string, rules archive.Rules, cols []string, got [][]string) [][]string {
	t.Helper()
	sortByFirst(got)
	_, want := goldenRows(t, q, rules, cols, got)
	return want
}

func sameRows(t *testing.T, got, want [][]string, cols []string) {
	t.Helper()
	sortByFirst(got)
	if len(got) != len(want) {
		t.Fatalf("%d rows, want %d", len(got), len(want))
	}
	for i := range want {
		if !slices.Equal(got[i], want[i]) {
			t.Fatalf("row %d:\n got %q\nwant %q\ncols %v", i, got[i], want[i], cols)
		}
	}
}

// odd is an event whose attributes collide but for case (attr_Plan
// takes the name, every attr_plan becomes attr_plan_1), hold a NUL in a
// key and whose path needs escaping.
func odd(id string) store.Event {
	return store.Event{ID: id, ProjectID: 1, Family: store.FamilyProduct, EventName: "odd",
		TS: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC), Path: "/a\"b/é\\c\u2028",
		Attributes: map[string]string{"Plan": "Big", "a\x00b": "nul", "$skip": "x"}}
}

func logOf(t *testing.T, f *archivetest.Files, project int64) string {
	t.Helper()
	_, logs, err := projectFiles(f.Dir, project, rng(t, today, today))
	if err != nil || len(logs) != 1 {
		t.Fatalf("logs %v, %v", logs, err)
	}
	return logs[0]
}

func TestLogRowsMatchGolden(t *testing.T) {
	f := archivetest.New(t)
	f.Write(t, events(t, 1, today, 30)...)
	f.Write(t, odd("odd-1"))
	f.Write(t, events(t, 2, today, 3)...)
	cols := eventsColumns(t, f.Dir, 1, archive.Rules{})
	for _, c := range []string{"attr_Plan", "attr_plan_1", `attr_a\u0000b`, "attr_color", "consent", "display_width"} {
		if !slices.Contains(cols, c) {
			t.Fatalf("events lacks %s: %v", c, cols)
		}
	}
	path := logOf(t, f, 1)
	b, err := readCached(t, path, cols, "", rng(t, today, today))
	if err != nil {
		t.Fatal(err)
	}
	for name, typ := range map[string]string{"value": "f", "ts": "t", "received_at": "t", "day": "i", "consent": "i", "attr_plan_1": "s", "path": "s"} {
		c := b.Cols[name]
		if c == nil || (typ == "f") != (c.Floats != nil) || (typ == "t") != (c.Times != nil) ||
			(typ == "i") != (c.Ints != nil) || (typ == "s") != (c.Idx != nil) {
			t.Errorf("%s: wrong kind %+v", name, c)
		}
	}
	got := formatBatch(t, b, cols, nil)
	sameRows(t, got, wantRows(t, "SELECT * FROM events WHERE project_id = 1", archive.Rules{}, cols, got), cols)

	// The attributes by pattern, one family: what SQL filters the same way.
	b, err = readCached(t, path, []string{"id", attrCols}, "product", rng(t, today, today))
	if err != nil {
		t.Fatal(err)
	}
	if got := keys(b.Cols); !slices.Equal(got, []string{"attr_Plan", `attr_a\u0000b`, "attr_color", "attr_plan_1", "id"}) {
		t.Fatalf("attr_* read %v", got)
	}
	sub := []string{"id", "attr_Plan", `attr_a\u0000b`, "attr_color", "attr_plan_1"}
	gotSub := formatBatch(t, b, sub, nil)
	sameRows(t, gotSub, wantRows(t, `SELECT id, attr_Plan, "attr_a\u0000b", attr_color, attr_plan_1 FROM events WHERE project_id = 1 AND family = 'product'`,
		archive.Rules{}, sub, gotSub), sub)

	// A range without the log's day, and a family it lacks: no rows, the
	// columns still there.
	for _, c := range []struct {
		family string
		r      dayRange
	}{{"", rng(t, "2026-09-09", "2026-09-09")}, {"zzz", rng(t, today, today)}} {
		b, err = readCached(t, path, []string{"id", "ts"}, c.family, c.r)
		if err != nil {
			t.Fatal(err)
		}
		if b.N != 0 || b.Cols["id"] == nil || b.Cols["ts"] == nil {
			t.Errorf("%v: %d rows, %v", c, b.N, keys(b.Cols))
		}
	}
	if _, err := readCached(t, filepath.Join(f.Dir, "nope.log"), cols, "", rng(t, today, today)); err == nil {
		t.Error("a missing log: no error")
	}
}

func TestLogKeepsFirstLinePerID(t *testing.T) {
	f := archivetest.New(t)
	first := events(t, 1, today, 1)
	second := slices.Clone(first)
	for i := range second {
		second[i].EventName = "later"
		second[i].Family = store.FamilyViews // a repeat need not be the same event
	}
	f.Write(t, first...)
	f.Write(t, second...)
	cols := eventsColumns(t, f.Dir, 1, archive.Rules{})
	b, err := readCached(t, logOf(t, f, 1), cols, "", rng(t, today, today))
	if err != nil {
		t.Fatal(err)
	}
	got := formatBatch(t, b, cols, nil)
	want := wantRows(t, "SELECT * FROM events WHERE project_id = 1", archive.Rules{}, cols, got)
	if len(want) != 3 {
		t.Fatalf("the golden: %d rows", len(want))
	}
	sameRows(t, got, want, cols)
	// The kept line is the first even when the family asked for is the
	// repeat's: it is not read.
	b, err = readCached(t, logOf(t, f, 1), []string{"id", "event_name"}, "views", rng(t, today, today))
	if err != nil {
		t.Fatal(err)
	}
	if b.N != 1 || b.Cols["event_name"].Dict[b.Cols["event_name"].Idx[0]] == "later" {
		t.Errorf("views: %d rows %+v", b.N, b.Cols["event_name"])
	}
}

func TestLogSkipsBadLines(t *testing.T) {
	f := archivetest.New(t)
	evs := events(t, 1, today, 4)
	f.Write(t, evs[:5]...)
	path := logOf(t, f, 1)
	appendTo := func(s string) {
		w, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.WriteString(s); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}
	appendTo("not json {\n\n[1,2]\nnull\n")
	f.Write(t, evs[5:]...)
	appendTo(`{"v":1,"id":"torn","attr_torn":"x`) // a line ingest is still writing
	cols := eventsColumns(t, f.Dir, 1, archive.Rules{})
	b, err := readCached(t, path, cols, "", rng(t, today, today))
	if err != nil {
		t.Fatal(err)
	}
	got := formatBatch(t, b, cols, nil)
	want := wantRows(t, "SELECT * FROM events WHERE project_id = 1", archive.Rules{}, cols, got)
	if len(want) != len(evs) || slices.Contains(cols, "attr_torn") {
		t.Fatalf("the golden: %d rows, %v", len(want), cols)
	}
	sameRows(t, got, want, cols)
}

// fold names b's columns as a union by name across files does (DuckDB's
// names ignore case): each name of cols is b's column batch.col resolves
// it to. The month file's attr_plan is the log's attr_Plan (the log's own
// attr_plan is attr_plan_1).
func fold(b *batch, cols []string) *batch {
	out := &batch{N: b.N, Cols: map[string]*column{}}
	for _, name := range cols {
		if _, c := b.col(name); c != nil {
			out.Cols[name] = c
		}
	}
	return out
}

func TestLogAndParquetMatchGolden(t *testing.T) {
	f := seed(t, 20)
	f.Write(t, events(t, 1, today, 20)...)
	f.Write(t, odd("odd-1"))
	cols := eventsColumns(t, f.Dir, 1, archive.Rules{})
	full := rng(t, "2026-08-01", today)
	pq, logs, err := projectFiles(f.Dir, 1, full)
	if err != nil || len(pq) != 2 || len(logs) != 1 {
		t.Fatalf("files %v, logs %v, %v", pq, logs, err)
	}
	groups, err := parquetGroups(pq, "", full)
	if err != nil {
		t.Fatal(err)
	}
	var got [][]string
	for _, b := range readAll(t, groups, 2, []string{"*"}, "", full) {
		got = formatBatch(t, fold(b, cols), cols, got)
	}
	b, err := readCached(t, logs[0], []string{"*"}, "", full)
	if err != nil {
		t.Fatal(err)
	}
	if b.Cols["v"] == nil || b.Cols["attr_plan_1"] == nil {
		t.Fatalf("log columns %v", keys(b.Cols))
	}
	got = formatBatch(t, fold(b, cols), cols, got)
	sameRows(t, got, wantRows(t, "SELECT * FROM events WHERE project_id = 1", archive.Rules{}, cols, got), cols)
	if !strings.HasSuffix(logs[0], today+".log") {
		t.Errorf("log %s", logs[0])
	}
}

func TestLogCasts(t *testing.T) {
	path := filepath.Join(t.TempDir(), today+".log")
	write := func(lines ...string) {
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"id":"a","day":"2026-09-10","family":"product","display_width":"1280.6","ts":"2026-09-10 12:00:00",`+
		`"received_at":"2026-09-10T12:00:01.5+01:00","value":"2","attr_n":5,"attr_o":{"k":1},"attr_z":null}`,
		`{"id":"b","family":"product"}`,                                          // no day: in no range
		`{"day":"2026-09-10","attr_n":"x"}`, `{"day":"2026-09-10","attr_n":"y"}`) // no id: the first kept
	b, err := readCached(t, path, []string{"id", "display_width", "ts", "received_at", "value", "attr_*"}, "", rng(t, today, today))
	if err != nil {
		t.Fatal(err)
	}
	got := formatBatch(t, b, []string{"id", "display_width", "ts", "received_at", "value", "attr_n", "attr_o", "attr_z"}, nil)
	want := [][]string{
		{"a", "1281", "2026-09-10T12:00:00.000000Z", "2026-09-10T11:00:01.500000Z", "2", "5", `{"k":1}`, nullCell},
		{nullCell, nullCell, nullCell, nullCell, nullCell, "x", nullCell, nullCell},
	}
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("got %q\nwant %q", got, want)
	}
	for _, bad := range []string{`{"id":"a","day":"2026-09-10","ts":"nope"}`, `{"id":"a","day":"10.09.2026"}`,
		`{"id":"a","day":"2026-09-10","value":"x"}`, `{"id":"a","day":"2026-09-10","consent":"yes"}`,
		`{"id":"a","day":"2026-09-10","consent":1e300}`} {
		write(bad)
		if _, err := readCached(t, path, []string{"*"}, "", rng(t, today, today)); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
	// A line longer than the read buffer is read whole.
	long := strings.Repeat("x", 3<<20)
	write(`{"id":"a","day":"2026-09-10","path":"` + long + `"}`)
	b, err = readCached(t, path, []string{"path"}, "", rng(t, today, today))
	if err != nil || b.N != 1 || b.Cols["path"].Dict[0] != long {
		t.Errorf("long line: %v", err)
	}
}
