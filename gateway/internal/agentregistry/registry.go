// Package agentregistry tracks worker-host agents and their liveness.
package agentregistry

import (
	"context"
	"errors"
	"sync"
	"time"
)

type Capacity struct {
	CPUMillis   uint32 `json:"cpu_millis"`
	MemoryBytes uint64 `json:"memory_bytes"`
}

type Agent struct {
	ID            string
	AdvertiseAddr string
	Capacity      Capacity
	Status        string // ready | draining | lost
	LastHeartbeat time.Time
}

// ErrNoAgent is returned by Pick when no ready agent is available.
var ErrNoAgent = errors.New("agentregistry: no ready agent")

type Registry struct {
	mu     sync.Mutex
	agents map[string]*Agent
	now    func() time.Time
}

func NewInMemory() *Registry {
	return NewInMemoryWithClock(time.Now)
}

// NewInMemoryWithClock is NewInMemory with an injectable clock, for tests that
// need to drive liveness transitions deterministically.
func NewInMemoryWithClock(now func() time.Time) *Registry {
	if now == nil {
		now = time.Now
	}
	return &Registry{agents: map[string]*Agent{}, now: now}
}

func (r *Registry) Register(_ context.Context, id, addr string, capacity Capacity) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents[id] = &Agent{
		ID: id, AdvertiseAddr: addr, Capacity: capacity,
		Status: "ready", LastHeartbeat: r.now(),
	}
	return nil
}

// Restore inserts an agent with the given status and heartbeat timestamp,
// without the "ready"/now() override that Register applies. The boot rebuild
// uses it so a host persisted as "lost" stays lost (and un-Pick-able) until it
// actually heartbeats again.
func (r *Registry) Restore(id, addr, status string, capacity Capacity, lastHeartbeat time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents[id] = &Agent{
		ID: id, AdvertiseAddr: addr, Capacity: capacity,
		Status: status, LastHeartbeat: lastHeartbeat,
	}
}

func (r *Registry) Heartbeat(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.agents[id]
	if !ok {
		return errors.New("agentregistry: unknown agent " + id)
	}
	a.LastHeartbeat = r.now()
	a.Status = "ready"
	return nil
}

// Sweep marks agents whose last heartbeat is older than ttl as "lost" and
// returns the ids that transitioned on this call.
func (r *Registry) Sweep(_ context.Context, at time.Time, ttl time.Duration) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var lost []string
	for id, a := range r.agents {
		if a.Status != "lost" && at.Sub(a.LastHeartbeat) > ttl {
			a.Status = "lost"
			lost = append(lost, id)
		}
	}
	return lost
}

func (r *Registry) Pick(_ context.Context) (Agent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range r.agents {
		if a.Status == "ready" {
			return *a, nil
		}
	}
	return Agent{}, ErrNoAgent
}

// Addr returns the advertise address of the agent with the given id, and
// whether it is known to the registry.
func (r *Registry) Addr(id string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.agents[id]
	if !ok {
		return "", false
	}
	return a.AdvertiseAddr, true
}

func (r *Registry) List() []Agent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Agent, 0, len(r.agents))
	for _, a := range r.agents {
		out = append(out, *a)
	}
	return out
}
