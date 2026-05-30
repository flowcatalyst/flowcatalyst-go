package router

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/internal/common"
)

// InFlightTracker records messages currently being processed across
// the entire router process. Used by:
//   - stall detection (force-NACK messages stuck longer than threshold)
//   - duplicate filter (SQS at-least-once redelivery during processing)
//   - graceful shutdown (drain to zero before exit)
type InFlightTracker struct {
	mu sync.RWMutex
	// keyed by broker message ID; the message itself doubles as the lookup
	// key when the broker ID is unavailable (Postgres-backed queues etc.).
	byBroker  map[string]*common.InFlightMessage
	byMessage map[string]*common.InFlightMessage
}

// NewInFlightTracker constructs an empty tracker.
func NewInFlightTracker() *InFlightTracker {
	return &InFlightTracker{
		byBroker:  make(map[string]*common.InFlightMessage),
		byMessage: make(map[string]*common.InFlightMessage),
	}
}

// Insert records a message as in-flight. Returns the existing entry if
// the broker has redelivered an already-tracked message; callers should
// swap the receipt handle and continue processing the original instance.
func (t *InFlightTracker) Insert(im *common.InFlightMessage) (existing *common.InFlightMessage, isDuplicate bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if im.BrokerMessageID != "" {
		if prev, ok := t.byBroker[im.BrokerMessageID]; ok {
			prev.UpdateReceiptHandle(im.ReceiptHandle)
			return prev, true
		}
		t.byBroker[im.BrokerMessageID] = im
	}
	if prev, ok := t.byMessage[im.MessageID]; ok {
		prev.UpdateReceiptHandle(im.ReceiptHandle)
		return prev, true
	}
	t.byMessage[im.MessageID] = im
	return im, false
}

// IsExternalRequeue reports whether the given application message ID is
// already in flight under a DIFFERENT broker message ID — i.e. an external
// process requeued a message (new broker/SQS id) while the original is still
// being processed. The Manager ACK-drops such duplicates at route time.
// Mirrors Rust filter_duplicates' app-message-id check. A blank brokerID
// (Postgres-style queues without a distinct broker id) is never a requeue.
func (t *InFlightTracker) IsExternalRequeue(appMsgID, brokerID string) bool {
	if brokerID == "" {
		return false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	e, ok := t.byMessage[appMsgID]
	return ok && e.BrokerMessageID != "" && e.BrokerMessageID != brokerID
}

// Remove clears the message from the tracker. Idempotent.
func (t *InFlightTracker) Remove(messageID, brokerID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.byMessage, messageID)
	if brokerID != "" {
		delete(t.byBroker, brokerID)
	}
}

// Count returns the number of in-flight messages.
func (t *InFlightTracker) Count() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.byMessage)
}

// Snapshot returns the current in-flight messages (by copy).
func (t *InFlightTracker) Snapshot() []common.InFlightMessage {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]common.InFlightMessage, 0, len(t.byMessage))
	for _, im := range t.byMessage {
		out = append(out, *im)
	}
	return out
}

// Reaper periodically prunes entries older than maxAge. Wires together
// with the lifecycle reaper goroutine in cmd/fc-router.
func (t *InFlightTracker) Reap(maxAge time.Duration) (reaped int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, im := range t.byMessage {
		if time.Since(im.StartedAt) > maxAge {
			delete(t.byMessage, id)
			if im.BrokerMessageID != "" {
				delete(t.byBroker, im.BrokerMessageID)
			}
			reaped++
		}
	}
	return
}

// StallConfig configures the stall detector.
type StallConfig struct {
	Enabled               bool
	StallThresholdSeconds uint64
	ForceNackStalled      bool
	ForceNackAfterSeconds uint64
	NackDelaySeconds      uint32
	CheckInterval         time.Duration
}

// DefaultStallConfig matches the Rust defaults (5 min threshold, force-nack off).
func DefaultStallConfig() StallConfig {
	return StallConfig{
		Enabled:               true,
		StallThresholdSeconds: 300,
		ForceNackStalled:      false,
		ForceNackAfterSeconds: 600,
		NackDelaySeconds:      30,
		CheckInterval:         60 * time.Second,
	}
}

// StallDetector watches the in-flight tracker for messages stuck longer
// than the threshold. Emits warnings and optionally force-NACKs.
type StallDetector struct {
	cfg      StallConfig
	tracker  *InFlightTracker
	notifier *Notifier
}

// NewStallDetector wires a detector. notifier may be nil.
func NewStallDetector(cfg StallConfig, tracker *InFlightTracker, notifier *Notifier) *StallDetector {
	return &StallDetector{cfg: cfg, tracker: tracker, notifier: notifier}
}

// Watch runs the periodic check until ctx is cancelled.
func (d *StallDetector) Watch(ctx context.Context) {
	if !d.cfg.Enabled {
		return
	}
	tick := time.NewTicker(d.cfg.CheckInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			d.tick()
		}
	}
}

func (d *StallDetector) tick() {
	stalled := []common.InFlightMessage{}
	for _, im := range d.tracker.Snapshot() {
		if im.ElapsedSeconds() >= int64(d.cfg.StallThresholdSeconds) {
			stalled = append(stalled, im)
		}
	}
	if len(stalled) == 0 {
		return
	}
	slog.Warn("stalled messages detected", "count", len(stalled))
	if d.notifier == nil {
		return
	}
	for _, im := range stalled {
		d.notifier.Add(Warning{
			Category: WarningCategoryStall,
			Severity: WarningWarning,
			Message: "Message " + im.MessageID + " stalled for " +
				utoa(uint64(im.ElapsedSeconds())) + "s in pool " + im.PoolCode,
			Source: "StallDetector",
		})
	}
}
