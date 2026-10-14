// Package archive keeps each project's raw events as files under the data
// directory's raw/ (raw-first spec D2): an append-only NDJSON log per
// project and UTC day, dumped to Parquet when the day closes, day files
// merged into a month file once final, month files deleted at retention.
// SQLite still holds every event too; Reader answers SQL over the files
// and open logs for the query tool's files source, and nothing else reads
// them yet.
//
// Under raw/<project_id>/:
//
//	<YYYY-MM-DD>.log          the day's open log (Log)
//	<YYYY-MM-DD>.<n>.closed   a log rotated for its dump
//	<YYYY-MM-DD>.parquet      the day file
//	<YYYY-MM-DD>.<n>.parquet  a segment of the day, from events that came late
//	<YYYY-MM>.parquet         the month file
package archive

import "time"

// Day is the UTC calendar day an event's file is named by.
func Day(t time.Time) string { return t.UTC().Format("2006-01-02") }
