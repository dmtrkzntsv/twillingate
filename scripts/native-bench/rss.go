package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// peakRSSKB is this process's peak resident set (VmHWM), in KB.
func peakRSSKB() (int64, error) {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "VmHWM:"); ok {
			return strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
		}
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("no VmHWM in /proc/self/status")
}

// cpuTimes is this process's user and system CPU so far.
func cpuTimes() (user, sys time.Duration) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0, 0
	}
	return time.Duration(ru.Utime.Nano()), time.Duration(ru.Stime.Nano())
}

// loadAvg1 is the host's 1-minute load average; -1 when unreadable.
func loadAvg1() float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return -1
	}
	fs := strings.Fields(string(b))
	if len(fs) == 0 {
		return -1
	}
	v, err := strconv.ParseFloat(fs[0], 64)
	if err != nil {
		return -1
	}
	return v
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
