package main

import (
	"fmt"
	"time"
)

// dashQuietLimit is how long a pass cell waits for the machine to go quiet.
const dashQuietLimit = 5 * time.Minute

func mb(kb int) string { return fmt.Sprintf("%d MB", (kb+512)/1024) }

func dashMS(v float64) string {
	if v >= 1000 {
		return fmt.Sprintf("%.2f s", v/1000)
	}
	if v >= 10 {
		return fmt.Sprintf("%.0f ms", v)
	}
	return fmt.Sprintf("%.1f ms", v)
}
