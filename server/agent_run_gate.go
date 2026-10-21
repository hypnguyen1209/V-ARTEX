package server

import (
	"context"
	"sync"
)

const defaultAgentConcurrencyLimit = 8

type agentRunKind uint8

const (
	agentRunControl agentRunKind = iota
	agentRunWorker
)

type agentRunGateStats struct {
	Enabled bool
	Limit   int
	Active  int
	Waiting int
}

type agentRunWaiter struct {
	taskID   string
	kind     agentRunKind
	ready    chan struct{}
	granted  bool
	canceled bool
}

type agentRunGate struct {
	mu          sync.Mutex
	enabled     bool
	limit       int
	active      int
	workers     int
	waiting     int
	controlQ    []*agentRunWaiter
	workerQ     map[string][]*agentRunWaiter
	workerOrder []string
}

func newAgentRunGate(enabled bool, limit int) *agentRunGate {
	if limit < 1 {
		limit = defaultAgentConcurrencyLimit
	}
	return &agentRunGate{
		enabled: enabled,
		limit:   limit,
		workerQ: make(map[string][]*agentRunWaiter),
	}
}

func (g *agentRunGate) snapshot() agentRunGateStats {
	g.mu.Lock()
	defer g.mu.Unlock()
	return agentRunGateStats{
		Enabled: g.enabled,
		Limit:   g.limit,
		Active:  g.active,
		Waiting: g.waiting,
	}
}

func (g *agentRunGate) configure(enabled bool, limit int) {
	if limit < 1 {
		limit = defaultAgentConcurrencyLimit
	}
	g.mu.Lock()
	g.enabled = enabled
	g.limit = limit
	g.scheduleLocked()
	g.mu.Unlock()
}

func (g *agentRunGate) acquire(ctx context.Context, taskID string, kind agentRunKind) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	waiter := &agentRunWaiter{taskID: taskID, kind: kind, ready: make(chan struct{})}
	g.mu.Lock()
	g.enqueueLocked(waiter)
	g.scheduleLocked()
	g.mu.Unlock()
	select {
	case <-waiter.ready:
		if err := ctx.Err(); err != nil {
			g.release(waiter)
			return nil, err
		}
		return func() { g.release(waiter) }, nil
	case <-ctx.Done():
		g.mu.Lock()
		if waiter.granted {
			g.mu.Unlock()
			g.release(waiter)
		} else {
			waiter.canceled = true
			g.waiting--
			g.scheduleLocked()
			g.mu.Unlock()
		}
		return nil, ctx.Err()
	}
}

func (g *agentRunGate) enqueueLocked(waiter *agentRunWaiter) {
	g.waiting++
	if waiter.kind == agentRunControl {
		g.controlQ = append(g.controlQ, waiter)
		return
	}
	if len(g.workerQ[waiter.taskID]) == 0 {
		g.workerOrder = append(g.workerOrder, waiter.taskID)
	}
	g.workerQ[waiter.taskID] = append(g.workerQ[waiter.taskID], waiter)
}

func (g *agentRunGate) popControlLocked() *agentRunWaiter {
	for len(g.controlQ) > 0 {
		waiter := g.controlQ[0]
		g.controlQ = g.controlQ[1:]
		if !waiter.canceled {
			return waiter
		}
	}
	return nil
}

func (g *agentRunGate) popWorkerLocked() *agentRunWaiter {
	for len(g.workerOrder) > 0 {
		taskID := g.workerOrder[0]
		g.workerOrder = g.workerOrder[1:]
		queue := g.workerQ[taskID]
		for len(queue) > 0 && queue[0].canceled {
			queue = queue[1:]
		}
		if len(queue) == 0 {
			delete(g.workerQ, taskID)
			continue
		}
		waiter := queue[0]
		queue = queue[1:]
		if len(queue) == 0 {
			delete(g.workerQ, taskID)
		} else {
			g.workerQ[taskID] = queue
			g.workerOrder = append(g.workerOrder, taskID)
		}
		return waiter
	}
	return nil
}

func (g *agentRunGate) canRunWorkerLocked() bool {
	if !g.enabled {
		return true
	}
	if g.active >= g.limit {
		return false
	}
	if g.limit == 1 {
		return len(g.controlQ) == 0
	}
	return g.workers < g.limit-1
}

func (g *agentRunGate) grantLocked(waiter *agentRunWaiter) {
	waiter.granted = true
	g.waiting--
	g.active++
	if waiter.kind == agentRunWorker {
		g.workers++
	}
	close(waiter.ready)
}

func (g *agentRunGate) scheduleLocked() {
	for {
		granted := false
		for !g.enabled || g.active < g.limit {
			waiter := g.popControlLocked()
			if waiter == nil {
				break
			}
			g.grantLocked(waiter)
			granted = true
		}
		for g.canRunWorkerLocked() {
			waiter := g.popWorkerLocked()
			if waiter == nil {
				break
			}
			g.grantLocked(waiter)
			granted = true
		}
		if !granted {
			return
		}
	}
}

func (g *agentRunGate) release(waiter *agentRunWaiter) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !waiter.granted {
		return
	}
	waiter.granted = false
	g.active--
	if waiter.kind == agentRunWorker {
		g.workers--
	}
	g.scheduleLocked()
}
