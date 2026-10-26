// Package queue lets at most n holders run at once and the rest wait their
// turn, first come first served. n is read on every turn, so it can change
// while the queue is in use.
package queue

import (
	"context"
	"sync"
)

// Queue is a first-come, first-served limit on how many holders run at
// once. The zero value is not usable: build one with New.
type Queue struct {
	size    func() int
	mu      sync.Mutex
	running int
	waiting []chan struct{} // closed when its holder's turn comes
}

// New is a Queue letting size() holders run at once; a size below 1
// counts as 1.
func New(size func() int) *Queue { return &Queue{size: size} }

func (q *Queue) limit() int { return max(q.size(), 1) }

// Acquire waits for a turn, behind every holder that asked before, and
// returns its release, which the caller calls once when done. It returns
// ctx's error, and holds nothing, when ctx ends first.
func (q *Queue) Acquire(ctx context.Context) (release func(), err error) {
	q.mu.Lock()
	if len(q.waiting) == 0 && q.running < q.limit() {
		q.running++
		q.mu.Unlock()
		return q.releaser(), nil
	}
	turn := make(chan struct{})
	q.waiting = append(q.waiting, turn)
	q.mu.Unlock()

	select {
	case <-turn:
		return q.releaser(), nil
	case <-ctx.Done():
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	select {
	case <-turn:
		// The turn came as ctx ended: hand it on.
		q.running--
		q.grant()
	default:
		for i, w := range q.waiting {
			if w == turn {
				q.waiting = append(q.waiting[:i], q.waiting[i+1:]...)
				break
			}
		}
	}
	return nil, ctx.Err()
}

// Wake gives turns to waiters the size now has room for: call it after
// the size grows, since a turn is otherwise only handed on at a release.
func (q *Queue) Wake() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.grant()
}

// releaser is a turn's release, which acts once however often it is called.
func (q *Queue) releaser() func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			q.mu.Lock()
			defer q.mu.Unlock()
			q.running--
			q.grant()
		})
	}
}

// grant gives turns to the first waiters while the limit has room. The
// caller holds mu.
func (q *Queue) grant() {
	for len(q.waiting) > 0 && q.running < q.limit() {
		close(q.waiting[0])
		q.waiting = q.waiting[1:]
		q.running++
	}
}
