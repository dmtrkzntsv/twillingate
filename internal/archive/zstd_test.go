package archive

import (
	"bytes"
	"math/rand/v2"
	"runtime"
	"testing"

	"github.com/parquet-go/parquet-go"
)

// TestPageCodecKeepsItsEncoders: a page compressed by the writer's codec
// decodes with parquet-go's, a dictionary page's size included, and
// compressing pages allocates about their output, not a new encoder's
// history (16 MB with zstd's default window) after a GC.
func TestPageCodecKeepsItsEncoders(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	page := make([]byte, dictMaxBytes)
	for i := range page {
		page[i] = "abcdefgh"[r.IntN(8)]
	}
	enc, err := zstdPages.Encode(nil, page)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := parquet.Zstd.Decode(nil, enc)
	if err != nil || !bytes.Equal(dec, page) {
		t.Fatalf("decoded %d bytes, %v; want the page back", len(dec), err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	var dst []byte
	for range 50 {
		runtime.GC()
		if dst, err = zstdPages.Encode(dst, page[:pageBytes]); err != nil {
			t.Fatal(err)
		}
	}
	runtime.ReadMemStats(&after)
	if got := after.TotalAlloc - before.TotalAlloc; got > 8<<20 {
		t.Fatalf("50 pages across GCs allocated %d MB, want under 8", got>>20)
	}
}
