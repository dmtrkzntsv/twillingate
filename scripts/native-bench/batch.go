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

// Source streams batches holding the named columns of rows with
// family == family and day in [from, to] (days since epoch, inclusive).
// Family and day filtering are the Source's job; queries trust what Scan
// yields. A name in cols ending in "*" is a prefix pattern: the Source
// expands it to every column of each file with that prefix (so batches may
// differ in which attr_ columns they hold, and a column absent from a file
// is simply absent from its batches).
type Source interface {
	Name() string
	Scan(files []string, cols []string, family string, from, to int64, fn func(*Batch) error) error
}

// Row is one query result row, values formatted as DuckDB prints them.
type Row = []string
