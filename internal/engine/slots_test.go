package engine

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSlotsBoundHolders(t *testing.T) {
	dir := t.TempDir()
	a, err := acquireSlot(context.Background(), dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := acquireSlot(ctx, dir, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second acquire = %v, want the deadline: the one slot is held", err)
	}
	a.release()
	b, err := acquireSlot(context.Background(), dir, 1)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	b.release()
}

func TestSlotsAllowMaxHolders(t *testing.T) {
	dir := t.TempDir()
	var held []*slot
	for i := 0; i < 3; i++ {
		s, err := acquireSlot(context.Background(), dir, 3)
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		held = append(held, s)
	}
	for _, s := range held {
		s.release()
	}
}
