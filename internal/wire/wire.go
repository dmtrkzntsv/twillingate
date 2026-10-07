// Package wire holds the ingest wire format's fixed limits: internal/server
// enforces them and internal/api lists them beside the limits that are
// settings, so the two cannot drift.
package wire

import "time"

const (
	// MaxBody accommodates a full MaxBatchEvents batch; single events are
	// a batch of one.
	MaxBody        = 256 << 10
	MaxBatchEvents = 500
	// MaxAttrKey bounds a custom attribute's key; a longer one is dropped
	// with a warning. An event's attribute count has no limit of its own.
	MaxAttrKey = 64
	// MaxAttrValue truncates a value, never rejects it.
	MaxAttrValue = 512
	// FutureSkew is how far ahead of the server's clock a client timestamp
	// may be before it is clamped to the time received.
	FutureSkew = 5 * time.Minute
)

// The form endpoint's limits (POST /ingest/forms/{name}). A larger body is
// refused, multipart file parts included, which are read only to be
// discarded; past MaxFormFields names the rest are dropped (in sorted
// order), a name longer than MaxFormFieldName is dropped, and a value is
// truncated to MaxFormValue bytes on a rune boundary, never refused.
const (
	MaxFormBody      = 64 << 10
	MaxFormFields    = 100
	MaxFormFieldName = 64
	MaxFormValue     = 8 << 10
)

// MaxMeasureValue bounds a measure's value: 1e15 is about 31,000 years in
// milliseconds and 1 PB in bytes, beyond anything a time, size or count can
// honestly be. A larger value is a bug on the client; accepted, it would
// overflow the weighted sums and push the histogram to an infinite bucket.
const MaxMeasureValue = 1e15

// MinSampleRate bounds $sample_rate from below: one sample in 10,000. A
// smaller rate would weight a single sample as more than 10,000 samples and
// let one client outvote every unsampled one in the percentiles.
const MinSampleRate = 1e-4
