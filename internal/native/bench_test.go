package native

import (
	"context"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/archive/archivetest"
)

// benchLog is a log of today holding 20,000 events of each family for
// project 1, and the project's files.
func benchLog(b *testing.B) (string, archive.Files) {
	b.Helper()
	f := archivetest.New(b)
	f.Write(b, events(b, 1, today, 20000)...)
	all, err := archive.ListFiles(f.Dir)
	if err != nil || len(all[1].Logs) != 1 {
		b.Fatalf("logs %v, %v", all[1].Logs, err)
	}
	return all[1].Logs[0], all[1]
}

func BenchmarkReadLog(b *testing.B) {
	path, _ := benchLog(b)
	r := rng(b, today, today)
	b.ResetTimer()
	for b.Loop() {
		if _, err := readLog(path, []string{"*"}, "", r); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReadLogViews(b *testing.B) {
	path, _ := benchLog(b)
	r := rng(b, today, today)
	b.ResetTimer()
	for b.Loop() {
		if _, err := readLog(path, viewColumns, "views", r); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDeriveSessions(b *testing.B) {
	_, files := benchLog(b)
	day := epochDay(date(b, today))
	b.ResetTimer()
	for b.Loop() {
		var gr groupReader
		if _, err := deriveSessions(context.Background(), 1, day, files, &gr, readLogBatches, func(int64) error { return nil }); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDeriveIdentities(b *testing.B) {
	path, _ := benchLog(b)
	e := New(Options{Workers: 1})
	b.ResetTimer()
	for b.Loop() {
		if _, err := e.deriveIdentities(context.Background(), nil, []string{path}, func(int64) error { return nil }); err != nil {
			b.Fatal(err)
		}
	}
}
