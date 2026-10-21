package agent

import (
	"fmt"
	"sync"
)

const plannerIntentLimit = 4

type intentBudget struct {
	mu    sync.Mutex
	limit int
	used  int // successful creations plus in-flight reservations
}

func newIntentBudget(limit int) *intentBudget {
	return &intentBudget{limit: limit}
}

func (b *intentBudget) remaining() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.limit - b.used
}

func (b *intentBudget) reserve(n int) (func(successful int), error) {
	if b == nil {
		return func(int) {}, nil
	}
	if n < 1 {
		return nil, fmt.Errorf("at least one intent is required")
	}
	b.mu.Lock()
	remaining := b.limit - b.used
	if n > remaining {
		b.mu.Unlock()
		return nil, fmt.Errorf("planner intent limit exceeded: requested %d, remaining %d of %d this round", n, remaining, b.limit)
	}
	b.used += n
	b.mu.Unlock()

	var once sync.Once
	return func(successful int) {
		once.Do(func() {
			if successful < 0 {
				successful = 0
			}
			if successful > n {
				successful = n
			}
			b.mu.Lock()
			b.used -= n - successful
			b.mu.Unlock()
		})
	}, nil
}
