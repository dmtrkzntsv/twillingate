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
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("engine: all %d slots busy (ENGINE_MAX_CHILDREN): %w", max, ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (s *slot) release() { s.f.Close() }
