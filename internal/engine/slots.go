package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// slot is one of max lock files in a directory, held with flock for as
// long as its child lives: every process on the data directory counts the
// same files, so ENGINE_MAX_CHILDREN bounds children across processes (D8).
// The kernel drops the lock when the file closes, a crashed process
// included. The file is opened close-on-exec, so the child never holds it.
type slot struct{ f *os.File }

// acquireSlot takes a free slot, polling until ctx ends.
func acquireSlot(ctx context.Context, dir string, max int) (*slot, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("engine: %w", err)
	}
	for {
		for i := 0; i < max; i++ {
			f, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("slot-%d", i)), os.O_CREATE|os.O_RDWR, 0o600)
			if err != nil {
				return nil, fmt.Errorf("engine: %w", err)
			}
			if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
				return &slot{f: f}, nil
			}
			f.Close()
		}
		markWaiting(dir)
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("engine: all %d slots busy (ENGINE_MAX_CHILDREN): %w", max, ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// waitingFile is touched by a process waiting for a slot; an engine whose
// child is idle sees it and retires the child (yieldPoll).
const waitingFile = "waiting"

// waiting reports whether some process waited for a slot in the last 2 s.
func waiting(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, waitingFile))
	return err == nil && time.Since(st.ModTime()) < 2*time.Second
}

// markWaiting records that this process is waiting for a slot.
func markWaiting(dir string) {
	p := filepath.Join(dir, waitingFile)
	now := time.Now()
	if os.Chtimes(p, now, now) != nil {
		if f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			f.Close()
		}
	}
}

func (s *slot) release() { s.f.Close() }
