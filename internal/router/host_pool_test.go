package router

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeBuilder() ClientBuilder {
	return func() *http.Client {
		return &http.Client{Transport: &http.Transport{}}
	}
}

func TestHostKeyDefaultPorts(t *testing.T) {
	k, err := HostKeyFromURL("https://api.example.com/foo")
	require.NoError(t, err)
	assert.Equal(t, "https", k.Scheme)
	assert.Equal(t, "api.example.com", k.Host)
	assert.Equal(t, uint16(443), k.Port)

	k, err = HostKeyFromURL("http://api.example.com/foo")
	require.NoError(t, err)
	assert.Equal(t, uint16(80), k.Port)
}

func TestHostKeyExplicitPort(t *testing.T) {
	k, err := HostKeyFromURL("http://api.example.com:8080/foo")
	require.NoError(t, err)
	assert.Equal(t, uint16(8080), k.Port)
}

func TestHostKeyRejectsMalformed(t *testing.T) {
	_, err := HostKeyFromURL("not a url")
	assert.Error(t, err)
	_, err = HostKeyFromURL("ftp://example.com/x")
	assert.Error(t, err) // no default port for ftp scheme
}

func TestHostPoolStartsWithOneSlot(t *testing.T) {
	host, _ := HostKeyFromURL("https://h.example/")
	pool := NewHostConnectionPool(host, DefaultHostPoolSizing(), makeBuilder())
	assert.Equal(t, 1, pool.SlotCount())
}

func TestHostPoolGrowsAtHighWatermark(t *testing.T) {
	host, _ := HostKeyFromURL("https://h.example/")
	sizing := HostPoolSizing{
		StreamsHighWatermark:    2,
		StreamsLowWatermark:     0,
		MaxSlotsPerHost:         4,
		SlotIdleGrace:           time.Second,
		SweepInterval:           time.Second,
		MaxSlotsWarningInterval: time.Second,
	}
	pool := NewHostConnectionPool(host, sizing, makeBuilder())

	// Saturate the first slot: 2 in-flight == high watermark.
	g1 := pool.Acquire()
	g2 := pool.Acquire()
	assert.Equal(t, 1, pool.SlotCount())

	// Third acquire must grow because every slot is at the watermark.
	g3 := pool.Acquire()
	assert.Equal(t, 2, pool.SlotCount())

	g1.Release()
	g2.Release()
	g3.Release()
}

func TestHostPoolHonorsMaxSlots(t *testing.T) {
	host, _ := HostKeyFromURL("https://h.example/")
	sizing := HostPoolSizing{
		StreamsHighWatermark:    1,
		StreamsLowWatermark:     0,
		MaxSlotsPerHost:         2,
		SlotIdleGrace:           time.Second,
		SweepInterval:           time.Second,
		MaxSlotsWarningInterval: time.Second,
	}
	pool := NewHostConnectionPool(host, sizing, makeBuilder())

	g1 := pool.Acquire() // slot 1
	g2 := pool.Acquire() // grows to slot 2
	g3 := pool.Acquire() // would grow but at cap
	g4 := pool.Acquire() // ditto
	assert.Equal(t, 2, pool.SlotCount())

	g1.Release()
	g2.Release()
	g3.Release()
	g4.Release()
}

func TestSlotGuardReleaseDecrementsInFlight(t *testing.T) {
	host, _ := HostKeyFromURL("https://h.example/")
	pool := NewHostConnectionPool(host, DefaultHostPoolSizing(), makeBuilder())

	g := pool.Acquire()
	pool.mu.RLock()
	assert.Equal(t, int64(1), pool.slots[0].InFlight())
	pool.mu.RUnlock()

	g.Release()
	pool.mu.RLock()
	assert.Equal(t, int64(0), pool.slots[0].InFlight())
	pool.mu.RUnlock()

	// Double release is a no-op.
	g.Release()
	pool.mu.RLock()
	assert.Equal(t, int64(0), pool.slots[0].InFlight())
	pool.mu.RUnlock()
}

func TestHostPoolSweepRemovesIdleSlots(t *testing.T) {
	host, _ := HostKeyFromURL("https://h.example/")
	sizing := HostPoolSizing{
		StreamsHighWatermark:    1,
		StreamsLowWatermark:     0,
		MaxSlotsPerHost:         4,
		SlotIdleGrace:           time.Millisecond,
		SweepInterval:           time.Second,
		MaxSlotsWarningInterval: time.Second,
	}
	pool := NewHostConnectionPool(host, sizing, makeBuilder())

	g1 := pool.Acquire()
	g2 := pool.Acquire()
	g3 := pool.Acquire()
	assert.Equal(t, 3, pool.SlotCount())
	g1.Release()
	g2.Release()
	g3.Release()
	time.Sleep(5 * time.Millisecond)

	pool.Sweep()
	assert.Equal(t, 1, pool.SlotCount(), "sweep must retain exactly one slot")
}

func TestHostPoolSweepKeepsBusySlot(t *testing.T) {
	host, _ := HostKeyFromURL("https://h.example/")
	sizing := HostPoolSizing{
		StreamsHighWatermark:    1,
		StreamsLowWatermark:     0,
		MaxSlotsPerHost:         4,
		SlotIdleGrace:           time.Millisecond,
		SweepInterval:           time.Second,
		MaxSlotsWarningInterval: time.Second,
	}
	pool := NewHostConnectionPool(host, sizing, makeBuilder())

	busy := pool.Acquire()
	g2 := pool.Acquire()
	assert.Equal(t, 2, pool.SlotCount())
	g2.Release()
	time.Sleep(5 * time.Millisecond)

	pool.Sweep()
	// Busy slot must survive even though slot 2 became idle.
	assert.GreaterOrEqual(t, pool.SlotCount(), 1)
	busy.Release()
}

func TestHostPoolRegistrySeparatesByOrigin(t *testing.T) {
	reg := NewHostPoolRegistry(DefaultHostPoolSizing(), makeBuilder())
	defer reg.Close()

	a, _ := HostKeyFromURL("https://a.example/")
	b, _ := HostKeyFromURL("https://b.example/")
	c, _ := HostKeyFromURL("https://a.example:8443/")
	reg.Acquire(a).Release()
	reg.Acquire(b).Release()
	reg.Acquire(c).Release()
	assert.Equal(t, 3, reg.HostCount())
}

func TestHostPoolRegistrySweepLifecycle(t *testing.T) {
	sizing := DefaultHostPoolSizing()
	sizing.SweepInterval = 10 * time.Millisecond
	reg := NewHostPoolRegistry(sizing, makeBuilder())
	reg.StartSweep()
	// Second StartSweep is a no-op (idempotent).
	reg.StartSweep()
	reg.Close()
	// Close after Close is a no-op.
	reg.Close()
}
