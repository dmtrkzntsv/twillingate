package archive_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
)

// TestWriterFilesReadByNative: native's reader reads a Go-written day file
// of every type back as written: strings, a BIGINT and a DOUBLE, the DATE
// day, and the TIMESTAMP ts.
func TestWriterFilesReadByNative(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "1"), 0o750); err != nil {
		t.Fatal(err)
	}
	cols := []archive.PQColumn{
		{Name: "id", Type: archive.ColVarchar}, {Name: "project_id", Type: archive.ColBigint},
		{Name: "family", Type: archive.ColVarchar}, {Name: "day", Type: archive.ColDate},
		{Name: "ts", Type: archive.ColTimestamp}, {Name: "event_name", Type: archive.ColVarchar},
		{Name: "value", Type: archive.ColDouble}, {Name: "display_width", Type: archive.ColBigint},
	}
	day := civil.Date{Year: 2026, Month: 10, Day: 1}
	days := int32(day.Time().Unix() / 86400)
	at := day.Time().UnixMicro()
	w, err := archive.NewRowWriter(filepath.Join(dir, "1", "2026-10-01.parquet"), cols, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"$lcp", "$lcp", "$cls"} {
		r := archive.Row{archive.StrValue(fmt.Sprint("e", i)), archive.IntValue(1), archive.StrValue("measures"),
			archive.DateValue(days), archive.TimestampValue(at + int64(i)*1_500_000), archive.StrValue(name),
			archive.FloatValue(0.25 * float64(i+1)), archive.IntValue(int64(1000 + i))}
		if err := w.Write(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	e := native.New(native.Options{Dir: dir, Now: func() time.Time { return day.Time().AddDate(0, 0, 1) }})
	p := native.Params{Project: 1, From: day, To: day}
	stat := native.Query{Kind: "stat", Table: "measures", Metrics: []native.Metric{
		{Op: "count"}, {Op: "sum", Column: "value"}, {Op: "sum", Column: "display_width"},
		{Op: "min", Column: "day"}, {Op: "min", Column: "ts"}, {Op: "max", Column: "ts"},
	}}
	res, err := e.Answer(context.Background(), stat, p, archive.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprint(res.Rows), "[[3 1.5 3003 2026-10-01 2026-10-01T00:00:00Z 2026-10-01T00:00:03Z]]"; got != want {
		t.Errorf("stat = %s, want %s", got, want)
	}
	bd := native.Query{Kind: "breakdown", Table: "measures", Dimensions: []string{"event_name"}, Order: "event_name",
		Metrics: []native.Metric{{Op: "count"}}}
	res, err = e.Answer(context.Background(), bd, p, archive.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprint(res.Rows), "[[$cls 1] [$lcp 2]]"; got != want {
		t.Errorf("breakdown = %s, want %s", got, want)
	}
}

// TestNativeReadsPlainFallback: native reads a chunk whose dictionary
// pages give way to PLAIN ones as written.
func TestNativeReadsPlainFallback(t *testing.T) {
	defer archive.SetDictMaxBytes(1 << 10)()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "1"), 0o750); err != nil {
		t.Fatal(err)
	}
	cols := []archive.PQColumn{{Name: "family", Type: archive.ColVarchar}, {Name: "day", Type: archive.ColDate},
		{Name: "ts", Type: archive.ColTimestamp}, {Name: "event_name", Type: archive.ColVarchar}}
	day := civil.Date{Year: 2026, Month: 10, Day: 1}
	w, err := archive.NewRowWriter(filepath.Join(dir, "1", "2026-10-01.parquet"), cols, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 20000 {
		name := archive.StrValue(fmt.Sprintf("event-%05d", i))
		if i%2 == 1 {
			name = archive.Value{} // NULLs in both kinds of page
		}
		r := archive.Row{archive.StrValue("product"), archive.DateValue(int32(day.Time().Unix() / 86400)),
			archive.TimestampValue(day.Time().UnixMicro() + int64(i)), name}
		if err := w.Write(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	e := native.New(native.Options{Dir: dir, Now: func() time.Time { return day.Time().AddDate(0, 0, 1) }})
	q := native.Query{Kind: "stat", Table: "product", Metrics: []native.Metric{
		{Op: "count"},
		{Op: "count", As: "first", Where: []native.Filter{{Column: "event_name", Op: "eq", Values: []string{"event-00000"}}}},
		{Op: "count", As: "last", Where: []native.Filter{{Column: "event_name", Op: "eq", Values: []string{"event-19998"}}}},
		{Op: "count", As: "named", Where: []native.Filter{{Column: "event_name", Op: "prefix", Values: []string{"event-"}}}},
	}}
	res, err := e.Answer(context.Background(), q, native.Params{Project: 1, From: day, To: day}, archive.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprint(res.Rows), "[[20000 1 1 10000]]"; got != want {
		t.Errorf("stat = %s, want %s", got, want)
	}
}
