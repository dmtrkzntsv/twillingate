package main

// Column is one column of a batch. Strings arrive dictionary-encoded:
// Dict holds this batch's distinct values, Idx one entry per row (-1 = null).
// Ints hold DATE (days since 1970-01-01) and integer columns.
type Column struct {
	Dict []string
	Idx  []int32
	Ints []int64
	Null []bool // for Ints; nil when no nulls
}

// Batch is a run of rows, column by column.
type Batch struct {
	N    int
	Cols map[string]*Column
}

// AttrCols is the column pattern a query passes to ask for every column
// whose name starts with "attr_": it cannot name them up front.
const AttrCols = "attr_*"

// Group is one row group of one file that survived pruning by statistics.
type Group struct {
	File  string
	Index int
	Rows  int64
	All   bool // statistics prove every row matches family and range
}

// Source reads row groups of Parquet files. Groups lists, in file order,
// the row groups whose statistics allow rows with family == family and day
// in [from, to] (days since epoch, inclusive); it reads no pages. ReadGroup
// reads one of them into a batch holding only matching rows: family and day
// filtering are the Source's job, and queries trust what it yields. A name
// in cols ending in "*" is a prefix pattern: the Source expands it to every
// column of the group's file with that prefix (so batches may differ in
// which attr_ columns they hold, and a column absent from a file is simply
// absent from its batches).
//
// ReadGroup takes a GroupReader from NewGroupReader: one per goroutine, it
// keeps the last file it opened open, so consecutive groups of one file
// reuse it.
type Source interface {
	Name() string
	Groups(files []string, family string, from, to int64) ([]Group, error)
	NewGroupReader() *GroupReader
	ReadGroup(r *GroupReader, g Group, cols []string, family string, from, to int64) (*Batch, error)
}

// GroupReader is a per-worker handle; state is the Source's own (nil until
// it opens a file). Close releases it.
type GroupReader struct{ state interface{ Close() error } }

func (r *GroupReader) Close() error {
	if r.state == nil {
		return nil
	}
	err := r.state.Close()
	r.state = nil
	return err
}

// Row is one query result row, values formatted as DuckDB prints them.
type Row = []string
