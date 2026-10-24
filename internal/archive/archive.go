// Package archive keeps each project's raw events as files under the data
// directory's raw/ (raw-first spec D2): an append-only NDJSON log per
// project and UTC day, dumped to Parquet when the day closes, day files
// merged into a month file once final, month files deleted at retention.
// SQLite holds no events; the daily pass also derives the
// sessions, identities and aliases tables from the files. TableSpec lists
// the files for the external SQL engine; internal/native reads them in Go.
//
// Under raw/<project_id>/:
//
//	<YYYY-MM-DD>.log          the day's open log (Log)
//	<YYYY-MM-DD>.<n>.closed   a log rotated for its dump
//	<YYYY-MM-DD>.parquet      the day file
//	<YYYY-MM-DD>.<n>.parquet  a segment of the day, from events that came late
//	<YYYY-MM>.parquet         the month file
//	sessions/<YYYY-MM-DD>.parquet, sessions/<YYYY-MM>.parquet
//	                          the sessions of a day or month file
//	identities.parquet        one row per user and group
//	aliases.parquet           one row per user_id and install_id pair
//
// raw/.lock is held (flock) by the one Log open on the directory.
package archive

import "time"

// Day is the UTC calendar day an event's file is named by.
func Day(t time.Time) string { return t.UTC().Format("2006-01-02") }
