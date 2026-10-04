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
	// MaxAttrs bounds an event's custom attributes; the ones past it are
	// dropped, as is a custom key longer than MaxAttrKey.
	MaxAttrs   = 50
	MaxAttrKey = 64
	// MaxAttrValue truncates a value, never rejects it.
	MaxAttrValue = 512
	// FutureSkew is how far ahead of the server's clock a client timestamp
	// may be before it is clamped to the time received.
	FutureSkew = 5 * time.Minute
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
