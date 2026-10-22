package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/apache/arrow-go/v18/arrow/memory"
	arrowpq "github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/compress"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"
	"github.com/parquet-go/parquet-go"
	pqcompress "github.com/parquet-go/parquet-go/compress"
	"github.com/parquet-go/parquet-go/encoding"
)

// WriteStats is one rewrite: wall time of the whole rewrite (open, read,
// encode, write, rename), the final file's size and its row groups.
type WriteStats struct {
	Wall      time.Duration
	Bytes     int64
	RowGroups int
}

// Rewrite copies the Parquet file in to out with the library lib ("arrow"
// or "pqgo"), streaming: rows are read in order and written in row groups
// of exactly rowsPerGroup rows (the last one shorter), zstd, strings
// dictionary-encoded, the input's schema unchanged. The file is written as
// out.tmp and renamed; on error nothing is left at either path.
func Rewrite(lib, in, out string, rowsPerGroup int) (WriteStats, error) {
	var write func(in string, w io.Writer, rowsPerGroup int) error
	switch lib {
	case "arrow":
		write = rewriteArrow
	case "pqgo":
		write = rewritePQGo
	default:
		return WriteStats{}, fmt.Errorf("unknown library %q (arrow, pqgo)", lib)
	}
	if rowsPerGroup <= 0 {
		return WriteStats{}, fmt.Errorf("rows per group %d", rowsPerGroup)
	}
	start := time.Now()
	tmp := out + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return WriteStats{}, err
	}
	// The library sees a plain writer, so it cannot close the file itself.
	err = write(in, struct{ io.Writer }{f}, rowsPerGroup)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, out)
	}
	if err != nil {
		os.Remove(tmp)
		return WriteStats{}, fmt.Errorf("%s: rewrite %s: %w", lib, in, err)
	}
	st := WriteStats{Wall: time.Since(start)}
	st.Bytes, st.RowGroups, err = fileGroups(out)
	return st, err
}

// fileGroups reads only the footer, outside the measured wall time.
func fileGroups(path string) (int64, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return 0, 0, err
	}
	pf, err := parquet.OpenFile(f, fi.Size(), parquet.SkipPageIndex(true), parquet.SkipBloomFilters(true))
	if err != nil {
		return 0, 0, err
	}
	return fi.Size(), len(pf.Metadata().RowGroups), nil
}

// rewriteArrow reads with pqarrow's record reader, whose batches of
// rowsPerGroup rows run across the input's row groups, and hands each batch
// to pqarrow.FileWriter.Write, which makes it one row group. Column chunks
// are read through a buffered stream, not whole, and the serial row group
// writer flushes each column's pages as it goes, so the process holds about
// one batch of decoded rows.
func rewriteArrow(in string, w io.Writer, rowsPerGroup int) error {
	mem := memory.DefaultAllocator
	rprops := arrowpq.NewReaderProperties(mem)
	rprops.BufferedStreamEnabled = true
	rprops.BufferSize = 256 << 10
	rdr, err := file.OpenParquetFile(in, false, file.WithReadProps(rprops))
	if err != nil {
		return err
	}
	defer rdr.Close()
	fr, err := pqarrow.NewFileReader(rdr, pqarrow.ArrowReadProperties{BatchSize: int64(rowsPerGroup)}, mem)
	if err != nil {
		return err
	}
	sc, err := fr.Schema()
	if err != nil {
		return err
	}
	rr, err := fr.GetRecordReader(context.Background(), nil, nil)
	if err != nil {
		return err
	}
	defer rr.Release()
	wprops := arrowpq.NewWriterProperties(
		arrowpq.WithAllocator(mem),
		arrowpq.WithCompression(compress.Codecs.Zstd),
		arrowpq.WithDictionaryDefault(true),
		arrowpq.WithMaxRowGroupLength(int64(rowsPerGroup)),
	)
	fw, err := pqarrow.NewFileWriter(sc, w, wprops, pqarrow.NewArrowWriterProperties(pqarrow.WithAllocator(mem)))
	if err != nil {
		return err
	}
	for rr.Next() {
		if err := fw.Write(rr.RecordBatch()); err != nil {
			fw.Close()
			return err
		}
	}
	if err := rr.Err(); err != nil && !errors.Is(err, io.EOF) {
		fw.Close()
		return err
	}
	return fw.Close()
}

// rewritePQGo copies rows with parquet.CopyRows from each input row group
// into one parquet.Writer, which cuts a row group every rowsPerGroup rows.
// The writer buffers that row group's values and pages; the reader decodes
// one page per column at a time. The schema is the file's own (names,
// order, types, repetition), with each leaf's encoding and codec, which
// parquet-go carries over from the input's first chunk, replaced.
func rewritePQGo(in string, w io.Writer, rowsPerGroup int) error {
	f, err := os.Open(in)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	pf, err := parquet.OpenFile(f, fi.Size(), parquet.SkipPageIndex(true), parquet.SkipBloomFilters(true),
		parquet.ReadBufferSize(256<<10))
	if err != nil {
		return err
	}
	schema := parquet.NewSchema(pf.Schema().Name(), reencode(pf.Root()))
	pw := parquet.NewWriter(w, schema,
		parquet.Compression(&parquet.Zstd),
		parquet.MaxRowsPerRowGroup(int64(rowsPerGroup)),
	)
	for _, rg := range pf.RowGroups() {
		rows := rg.Rows()
		_, err := parquet.CopyRows(pw, rows)
		rows.Close()
		if err != nil {
			return err
		}
	}
	return pw.Close()
}

// pqNode overrides a file node's encoding, codec and fields, keeping the
// rest (name, type, repetition, field id) and, unlike parquet.Group, the
// order of the fields.
type pqNode struct {
	parquet.Field
	enc    encoding.Encoding
	codec  pqcompress.Codec
	fields []parquet.Field
}

func (n *pqNode) Encoding() encoding.Encoding   { return n.enc }
func (n *pqNode) Compression() pqcompress.Codec { return n.codec }
func (n *pqNode) Fields() []parquet.Field       { return n.fields }

// reencode gives string leaves RLE_DICTIONARY and the others parquet-go's
// default encoding (nil), every leaf zstd.
func reencode(f parquet.Field) parquet.Field {
	n := &pqNode{Field: f}
	if f.Leaf() {
		n.codec = &parquet.Zstd
		if f.Type().Kind() == parquet.ByteArray {
			n.enc = &parquet.RLEDictionary
		}
		return n
	}
	for _, c := range f.Fields() {
		n.fields = append(n.fields, reencode(c))
	}
	return n
}

func rewriteCmd(args []string) error {
	fs := flag.NewFlagSet("rewrite", flag.ContinueOnError)
	lib := fs.String("lib", "", "arrow or pqgo")
	per := fs.Int("rows", 0, "rows per row group")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 || *per <= 0 {
		return errors.New("usage: native-bench rewrite -lib arrow|pqgo -rows N <in> <out>")
	}
	st, err := Rewrite(*lib, fs.Arg(0), fs.Arg(1), *per)
	if err != nil {
		return err
	}
	fmt.Printf("lib=%s wall=%s bytes=%d row_groups=%d\n", *lib, st.Wall.Round(time.Millisecond), st.Bytes, st.RowGroups)
	return nil
}
