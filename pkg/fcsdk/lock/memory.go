package lock

import (
	"context"
	"sync"
	"time"
)

// ─── NoOp ────────────────────────────────────────────────────────────

// NoOp is a lock provider whose Acquire always succeeds. Use when the
// underlying work can run concurrently or when you de-dupe by some
// other means (idempotency keys, partition assignment, etc.).
type NoOp struct{}

// NewNoOp builds a NoOp provider.
func NewNoOp() *NoOp { return &NoOp{} }

// Acquire always returns a no-op handle.
func (*NoOp) Acquire(_ context.Context, _ string, _ time.Duration) (Handle, error) {
	return noopHandle{}, nil
}

type noopHandle struct{}

func (noopHandle) Release(_ context.Context) error { return nil }

// ─── Memory ──────────────────────────────────────────────────────────

// Memory is a process-local lock provider. Serialises holders for a
// given key inside this process. Does NOT survive multiple replicas —
// use postgreslock or redislock for that.
type Memory struct {
	mu   sync.Mutex
	held map[string]time.Time
	now  func() time.Time
}

// NewMemory builds an empty Memory provider.
func NewMemory() *Memory {
	return &Memory{held: map[string]time.Time{}, now: time.Now}
}

// Acquire — see Provider.Acquire.
func (m *Memory) Acquire(_ context.Context, key string, ttl time.Duration) (Handle, error) {
	if err := EnsurePositiveTTL(ttl); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if existing, ok := m.held[key]; ok && existing.After(now) {
		return nil, nil // contended
	}
	expires := now.Add(ttl)
	m.held[key] = expires
	return &memoryHandle{owner: m, key: key, expires: expires}, nil
}

type memoryHandle struct {
	owner    *Memory
	key      string
	expires  time.Time
	released bool
}

// Release frees the lock, but only if we still own the same lease —
// protects against double-release stomping a lock that has since been
// reclaimed by another holder after TTL expiry.
func (h *memoryHandle) Release(_ context.Context) error {
	if h.released {
		return nil
	}
	h.released = true
	h.owner.mu.Lock()
	defer h.owner.mu.Unlock()
	if current, ok := h.owner.held[h.key]; ok && current.Equal(h.expires) {
		delete(h.owner.held, h.key)
	}
	return nil
}
