package strata

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

type QueueItem struct {
	ID        string `json:"id"`
	Source    string `json:"source"` // "pi" | "arena" | "chat"
	Model     string `json:"model,omitempty"`
	Status    string `json:"status"` // "queued" | "running"
	QueuedAt  string `json:"queuedAt"`
	StartedAt string `json:"startedAt,omitempty"`
}

type Queue struct {
	sem      chan struct{}
	mu       sync.RWMutex
	items    map[string]*QueueItem
	order    []string
	seq      atomic.Uint64
	onChange func([]QueueItem)
}

func NewQueue(concurrency int) *Queue {
	if concurrency < 1 {
		concurrency = 1
	}
	return &Queue{
		sem:   make(chan struct{}, concurrency),
		items: make(map[string]*QueueItem),
	}
}

func (q *Queue) SetOnChange(fn func([]QueueItem)) {
	q.mu.Lock()
	q.onChange = fn
	q.mu.Unlock()
}

func (q *Queue) Snapshot() []QueueItem {
	q.mu.RLock()
	defer q.mu.RUnlock()
	out := make([]QueueItem, 0, len(q.order))
	for _, id := range q.order {
		if it, ok := q.items[id]; ok {
			out = append(out, *it)
		}
	}
	return out
}

func (q *Queue) emit() {
	q.mu.RLock()
	cb := q.onChange
	q.mu.RUnlock()
	if cb != nil {
		cb(q.Snapshot())
	}
}

func (q *Queue) Acquire(ctx context.Context, source, model string) (func(), error) {
	id := fmt.Sprintf("sq-%d", q.seq.Add(1))
	it := &QueueItem{
		ID:       id,
		Source:   source,
		Model:    model,
		Status:   "queued",
		QueuedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	q.mu.Lock()
	q.items[id] = it
	q.order = append(q.order, id)
	q.mu.Unlock()
	q.emit()

	remove := func() {
		q.mu.Lock()
		delete(q.items, id)
		for i, oid := range q.order {
			if oid == id {
				q.order = append(q.order[:i], q.order[i+1:]...)
				break
			}
		}
		q.mu.Unlock()
		q.emit()
	}

	select {
	case q.sem <- struct{}{}:
		q.mu.Lock()
		if cur, ok := q.items[id]; ok {
			cur.Status = "running"
			cur.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
		}
		q.mu.Unlock()
		q.emit()
		var once sync.Once
		return func() {
			once.Do(func() {
				<-q.sem
				remove()
			})
		}, nil
	case <-ctx.Done():
		remove()
		return nil, ctx.Err()
	}
}
