package archive

// The writer, for pqwrite_native_test.go: an archive_test file may import
// native, which imports archive.
type (
	PQColumn = pqColumn
	Row      = row
	Value    = value
)

const (
	ColVarchar   = colVarchar
	ColBigint    = colBigint
	ColDouble    = colDouble
	ColDate      = colDate
	ColTimestamp = colTimestamp
)

var (
	NewRowWriter   = newRowWriter
	StrValue       = strValue
	IntValue       = intValue
	FloatValue     = floatValue
	DateValue      = dateValue
	TimestampValue = timestampValue
)

// SetDictMaxBytes lowers the dictionary bound, so a small file holds
// pages of both encodings; the returned func restores it.
func SetDictMaxBytes(n int64) func() {
	old := dictMaxBytes
	dictMaxBytes = n
	return func() { dictMaxBytes = old }
}
