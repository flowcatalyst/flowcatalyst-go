// Per-host HTTP/2 connection pool with dynamic grow/shrink.
//
// Port of crates/fc-router/src/http_pool.rs. Go's net/http reuses one
// HTTP/2 connection per origin per *http.Transport, so high-concurrency
// mediation against a single host saturates a single connection and
// excess requests queue invisibly inside h2 (visible only as latency).
//
// This module sits between HTTPMediator and the net/http Transport.
// Each origin gets a HostConnectionPool holding one or more ClientSlot,
// each slot owns its own *http.Client (and therefore its own h2
// connection). The pool grows a new slot when every existing slot is
// above the high watermark, up to a configurable cap. A sweep loop
// removes slots that have fallen below the low watermark and stayed
// quiet through a grace window.
//
// Saturation is inferred from our own in-flight counters because h2
// does not surface backpressure when it queues on
// SETTINGS_MAX_CONCURRENT_STREAMS. A target advertising a tighter cap
// than our high watermark can still saturate before we grow — same
// correctness compromise the Rust impl documents.

package router

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

// HostKey identifies an origin. Two URLs sharing (scheme, host, port)
// share a single HTTP/2 connection inside one http.Transport, so we
// pool at this granularity.
type HostKey struct {
	Scheme string
	Host   string
	Port   uint16
}

// HostKeyFromURL parses a target URL into a HostKey. Returns an error
// when the URL is malformed, has no host, or has no port and no known
// default for its scheme.
func HostKeyFromURL(target string) (HostKey, error) {
	u, err := url.Parse(target)
	if err != nil {
		return HostKey{}, fmt.Errorf("parse: %w", err)
	}
	if u.Host == "" || u.Hostname() == "" {
		return HostKey{}, fmt.Errorf("missing host")
	}
	scheme := u.Scheme
	host := u.Hostname()
	portStr := u.Port()
	var port uint16
	if portStr != "" {
		var p uint32
		if _, err := fmt.Sscanf(portStr, "%d", &p); err != nil || p == 0 || p > 65535 {
			return HostKey{}, fmt.Errorf("invalid port %q", portStr)
		}
		port = uint16(p)
	} else {
		switch scheme {
		case "https", "wss":
			port = 443
		case "http", "ws":
			port = 80
		default:
			return HostKey{}, fmt.Errorf("no default port for scheme %q", scheme)
		}
	}
	return HostKey{Scheme: scheme, Host: host, Port: port}, nil
}

// String renders the origin (scheme://host:port).
func (k HostKey) String() string {
	return fmt.Sprintf("%s://%s:%d", k.Scheme, k.Host, k.Port)
}

// HostPoolSizing tunes per-host slot growth/shrink.
//
// Defaults sit below AWS ALB's SETTINGS_MAX_CONCURRENT_STREAMS = 128
// with enough headroom to absorb a short burst while we grow.
type HostPoolSizing struct {
	// StreamsHighWatermark grows a new slot when every existing slot has
	// in-flight >= this value.
	StreamsHighWatermark int
	// StreamsLowWatermark makes a slot shrink-eligible once its in-flight
	// drops to or below this value AND it has been quiet through
	// SlotIdleGrace.
	StreamsLowWatermark int
	// MaxSlotsPerHost is the hard cap on slots per origin. Once reached
	// we warn (throttled) and fall back to the least-loaded existing slot.
	MaxSlotsPerHost int
	// SlotIdleGrace is how long a slot must remain quiet before the
	// sweep can remove it.
	SlotIdleGrace time.Duration
	// SweepInterval is the cadence of the sweep goroutine.
	SweepInterval time.Duration
	// MaxSlotsWarningInterval throttles the "at MaxSlotsPerHost" warning
	// to at most one per host per interval.
	MaxSlotsWarningInterval time.Duration
}

// DefaultHostPoolSizing matches the Rust HostPoolSizing::default().
func DefaultHostPoolSizing() HostPoolSizing {
	return HostPoolSizing{
		StreamsHighWatermark:    100,
		StreamsLowWatermark:     20,
		MaxSlotsPerHost:         8,
		SlotIdleGrace:           60 * time.Second,
		SweepInterval:           15 * time.Second,
		MaxSlotsWarningInterval: 60 * time.Second,
	}
}

// HTTP1HostPoolSizing is the HTTP/1.1 preset: HTTP/1.1 doesn't
// multiplex, so growing past one slot per host duplicates the
// Transport's own connection-pool behaviour. Equivalent to Rust
// HostPoolSizing::http1.
func HTTP1HostPoolSizing() HostPoolSizing {
	s := DefaultHostPoolSizing()
	s.MaxSlotsPerHost = 1
	return s
}

// ClientBuilder produces a fresh *http.Client. Each invocation MUST
// return a client with its own *http.Transport so slots are
// connection-isolated.
type ClientBuilder func() *http.Client

// ClientSlot is one slot in a HostConnectionPool. Owns a *http.Client
// (with its own Transport) plus the counters the pool uses to decide
// grow/shrink.
type ClientSlot struct {
	client     *http.Client
	inFlight   atomic.Int64
	lastUsedNs atomic.Int64
}

func newClientSlot(client *http.Client) *ClientSlot {
	s := &ClientSlot{client: client}
	s.lastUsedNs.Store(time.Now().UnixNano())
	return s
}

// Client returns the underlying *http.Client. Callers MUST go through
// HostConnectionPool.Acquire so the in-flight counters stay correct.
func (s *ClientSlot) Client() *http.Client { return s.client }

// InFlight is the current in-flight request count (for tests/metrics).
func (s *ClientSlot) InFlight() int64 { return s.inFlight.Load() }

// SlotGuard is the handle returned by HostConnectionPool.Acquire. Use
// Client() for the request; Release() once the response body is drained
// and closed. Release() is safe to call exactly once.
//
// Go doesn't have RAII; defer release immediately after Acquire:
//
//	g := pool.Acquire(host)
//	defer g.Release()
//	resp, err := g.Client().Do(req)
type SlotGuard struct {
	slot     *ClientSlot
	released atomic.Bool
}

// Client returns the slot's *http.Client. Must not be used after Release.
func (g *SlotGuard) Client() *http.Client { return g.slot.client }

// Release decrements the slot's in-flight counter and stamps last-used.
// Safe to call exactly once; subsequent calls are no-ops.
func (g *SlotGuard) Release() {
	if g.released.Swap(true) {
		return
	}
	g.slot.inFlight.Add(-1)
	g.slot.lastUsedNs.Store(time.Now().UnixNano())
}

// HostConnectionPool is the per-origin pool of slots.
type HostConnectionPool struct {
	host    HostKey
	sizing  HostPoolSizing
	builder ClientBuilder

	mu    sync.RWMutex
	slots []*ClientSlot

	// growLock serialises slot creation; held only across the slice
	// push, never across blocking work.
	growLock sync.Mutex

	lastMaxWarnNs atomic.Int64
}

// NewHostConnectionPool builds a pool with one initial slot.
func NewHostConnectionPool(host HostKey, sizing HostPoolSizing, builder ClientBuilder) *HostConnectionPool {
	return &HostConnectionPool{
		host:    host,
		sizing:  sizing,
		builder: builder,
		slots:   []*ClientSlot{newClientSlot(builder())},
	}
}

// Acquire selects a slot for one request. Returns a guard whose Release
// must be called after the response body is drained.
//
// Selection: pick the least-loaded slot. If every slot is at or above
// the high watermark, try to grow; if growth is capped, fall back to
// the least-loaded slot (and let h2 queue inside the transport).
func (p *HostConnectionPool) Acquire() *SlotGuard {
	p.mu.RLock()
	least, minInFlight := selectLeastLoaded(p.slots)
	p.mu.RUnlock()

	if minInFlight >= int64(p.sizing.StreamsHighWatermark) {
		if grown := p.tryGrow(); grown != nil {
			grown.inFlight.Add(1)
			return &SlotGuard{slot: grown}
		}
	}

	least.inFlight.Add(1)
	return &SlotGuard{slot: least}
}

func (p *HostConnectionPool) tryGrow() *ClientSlot {
	p.growLock.Lock()
	defer p.growLock.Unlock()
	// Re-check under growLock: another goroutine may have grown the
	// pool, or an in-flight request may have completed and pushed the
	// minimum back below the high watermark.
	p.mu.RLock()
	for _, s := range p.slots {
		if s.InFlight() < int64(p.sizing.StreamsHighWatermark) {
			p.mu.RUnlock()
			return nil
		}
	}
	atCap := len(p.slots) >= p.sizing.MaxSlotsPerHost
	p.mu.RUnlock()
	if atCap {
		p.warnMaxSlots()
		return nil
	}

	slot := newClientSlot(p.builder())
	p.mu.Lock()
	p.slots = append(p.slots, slot)
	newCount := len(p.slots)
	p.mu.Unlock()
	slog.Info("grew per-host HTTP/2 connection pool",
		"host", p.host.String(),
		"slots", newCount,
		"high_watermark", p.sizing.StreamsHighWatermark)
	return slot
}

func (p *HostConnectionPool) warnMaxSlots() {
	now := time.Now().UnixNano()
	throttle := p.sizing.MaxSlotsWarningInterval.Nanoseconds()
	last := p.lastMaxWarnNs.Load()
	if now-last < throttle {
		return
	}
	// Best-effort throttle: a concurrent caller might also pass this
	// check before we store. Acceptable — at worst two warnings for
	// the same saturation event.
	p.lastMaxWarnNs.Store(now)
	slog.Warn("per-host HTTP/2 connection pool at MaxSlotsPerHost; requests will queue inside h2",
		"host", p.host.String(),
		"slots", p.sizing.MaxSlotsPerHost)
}

// Sweep removes slots below the low watermark that have been quiet
// through SlotIdleGrace. Always keeps at least one slot per host.
// Closes evicted slots' idle TCP connections so the OS reclaims them.
func (p *HostConnectionPool) Sweep() {
	graceNs := p.sizing.SlotIdleGrace.Nanoseconds()
	now := time.Now().UnixNano()

	p.mu.Lock()
	if len(p.slots) <= 1 {
		p.mu.Unlock()
		return
	}
	before := len(p.slots)
	kept := make([]*ClientSlot, 0, before)
	evicted := make([]*ClientSlot, 0, before-1)
	for _, s := range p.slots {
		idle := now-s.lastUsedNs.Load() > graceNs
		evict := s.InFlight() <= int64(p.sizing.StreamsLowWatermark) && idle
		if evict {
			evicted = append(evicted, s)
		} else {
			kept = append(kept, s)
		}
	}
	if len(kept) == 0 {
		// Every slot was evictable. Keep the freshest (largest
		// lastUsedNs) so we never strip below 1 slot. Move the
		// freshest from evicted back to kept.
		var freshest *ClientSlot
		for _, s := range evicted {
			if freshest == nil || s.lastUsedNs.Load() > freshest.lastUsedNs.Load() {
				freshest = s
			}
		}
		kept = append(kept, freshest)
		filtered := evicted[:0]
		for _, s := range evicted {
			if s != freshest {
				filtered = append(filtered, s)
			}
		}
		evicted = filtered
	}
	p.slots = kept
	remaining := len(p.slots)
	p.mu.Unlock()

	for _, s := range evicted {
		if t, ok := s.client.Transport.(*http.Transport); ok {
			t.CloseIdleConnections()
		}
	}
	if removed := before - remaining; removed > 0 {
		slog.Info("shrank per-host HTTP/2 connection pool",
			"host", p.host.String(),
			"removed", removed,
			"remaining", remaining)
	}
}

// SlotCount is the current slot count (tests/metrics).
func (p *HostConnectionPool) SlotCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.slots)
}

// Host returns the pool's HostKey.
func (p *HostConnectionPool) Host() HostKey { return p.host }

// selectLeastLoaded returns (slot, in-flight). slots must be non-empty.
func selectLeastLoaded(slots []*ClientSlot) (*ClientSlot, int64) {
	best := slots[0]
	bestLoad := best.InFlight()
	for _, s := range slots[1:] {
		l := s.InFlight()
		if l < bestLoad {
			best = s
			bestLoad = l
			if l == 0 {
				break
			}
		}
	}
	return best, bestLoad
}

// HostPoolRegistry owns the HostConnectionPool for each origin. Holds
// the sweep goroutine; Close stops the sweep.
type HostPoolRegistry struct {
	sizing  HostPoolSizing
	builder ClientBuilder

	mu    sync.RWMutex
	pools map[HostKey]*HostConnectionPool

	sweepStop chan struct{}
	sweepDone chan struct{}
}

// NewHostPoolRegistry builds a registry. builder is called to mint a
// fresh *http.Client per slot — each call MUST return a client with
// its own *http.Transport so slots' connection pools are independent.
func NewHostPoolRegistry(sizing HostPoolSizing, builder ClientBuilder) *HostPoolRegistry {
	return &HostPoolRegistry{
		sizing:  sizing,
		builder: builder,
		pools:   make(map[HostKey]*HostConnectionPool),
	}
}

// Acquire returns a slot for the origin, creating the per-host pool on
// first use.
func (r *HostPoolRegistry) Acquire(host HostKey) *SlotGuard {
	r.mu.RLock()
	pool, ok := r.pools[host]
	r.mu.RUnlock()
	if ok {
		return pool.Acquire()
	}
	r.mu.Lock()
	pool, ok = r.pools[host]
	if !ok {
		pool = NewHostConnectionPool(host, r.sizing, r.builder)
		r.pools[host] = pool
	}
	r.mu.Unlock()
	return pool.Acquire()
}

// SweepAll runs Sweep on every host pool.
func (r *HostPoolRegistry) SweepAll() {
	r.mu.RLock()
	pools := make([]*HostConnectionPool, 0, len(r.pools))
	for _, p := range r.pools {
		pools = append(pools, p)
	}
	r.mu.RUnlock()
	for _, p := range pools {
		p.Sweep()
	}
}

// TotalSlots is the sum of slot counts across every host pool
// (tests/metrics).
func (r *HostPoolRegistry) TotalSlots() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	total := 0
	for _, p := range r.pools {
		total += p.SlotCount()
	}
	return total
}

// HostCount is the number of distinct origins in the registry.
func (r *HostPoolRegistry) HostCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.pools)
}

// StartSweep launches the sweep goroutine on the registry's
// SweepInterval. Idempotent; subsequent calls before Close are no-ops.
func (r *HostPoolRegistry) StartSweep() {
	r.mu.Lock()
	if r.sweepStop != nil {
		r.mu.Unlock()
		return
	}
	r.sweepStop = make(chan struct{})
	r.sweepDone = make(chan struct{})
	stop := r.sweepStop
	done := r.sweepDone
	interval := r.sizing.SweepInterval
	r.mu.Unlock()

	if interval <= 0 {
		interval = 15 * time.Second
	}
	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				r.SweepAll()
			}
		}
	}()
}

// Close stops the sweep goroutine and closes idle connections on every
// slot. Safe to call multiple times.
func (r *HostPoolRegistry) Close() {
	r.mu.Lock()
	stop := r.sweepStop
	done := r.sweepDone
	r.sweepStop = nil
	r.sweepDone = nil
	pools := make([]*HostConnectionPool, 0, len(r.pools))
	for _, p := range r.pools {
		pools = append(pools, p)
	}
	r.mu.Unlock()

	if stop != nil {
		close(stop)
		<-done
	}
	for _, p := range pools {
		p.mu.RLock()
		slots := append([]*ClientSlot(nil), p.slots...)
		p.mu.RUnlock()
		for _, s := range slots {
			if t, ok := s.client.Transport.(*http.Transport); ok {
				t.CloseIdleConnections()
			}
		}
	}
}
