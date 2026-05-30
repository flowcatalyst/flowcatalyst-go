package router

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/flowcatalyst/flowcatalyst-go/internal/common"
	"github.com/flowcatalyst/flowcatalyst-go/internal/queue"
)

// cascadeConsumer records the terminal action per receipt handle and closes
// `done` once wantTotal terminal actions have been observed. Poll is unused
// (these tests drive the passive pool via submit) but kept for the
// queue.Consumer interface.
type cascadeConsumer struct {
	wantTotal int
	done      chan struct{}

	mu       sync.Mutex
	nacked   []string
	acked    []string
	deferred []string
	doneOnce sync.Once
}

func (c *cascadeConsumer) Poll(context.Context, uint32) ([]common.QueuedMessage, error) {
	return nil, nil
}
func (c *cascadeConsumer) record(list *[]string, rh string) {
	c.mu.Lock()
	*list = append(*list, rh)
	total := len(c.nacked) + len(c.acked) + len(c.deferred)
	c.mu.Unlock()
	if total >= c.wantTotal {
		c.doneOnce.Do(func() { close(c.done) })
	}
}
func (c *cascadeConsumer) Ack(_ context.Context, rh string) error { c.record(&c.acked, rh); return nil }
func (c *cascadeConsumer) Nack(_ context.Context, rh string, _ *uint32) error {
	c.record(&c.nacked, rh)
	return nil
}
func (c *cascadeConsumer) Defer(_ context.Context, rh string, _ *uint32) error {
	c.record(&c.deferred, rh)
	return nil
}
func (c *cascadeConsumer) ExtendVisibility(context.Context, string, uint32) error { return nil }
func (c *cascadeConsumer) Identifier() string                                     { return "cascade-test" }
func (c *cascadeConsumer) Healthy() bool                                          { return true }
func (c *cascadeConsumer) Stop()                                                  {}
func (c *cascadeConsumer) Metrics(context.Context) (*queue.Metrics, error) {
	return &queue.Metrics{}, nil
}
func (c *cascadeConsumer) Counters() *queue.Metrics { return nil }

// cascadeMediator fails the message whose ID == failID and records every ID
// it was asked to mediate.
type cascadeMediator struct {
	failID string
	mu     sync.Mutex
	seen   []string
}

func (m *cascadeMediator) Mediate(_ context.Context, msg *common.Message) common.MediationOutcome {
	m.mu.Lock()
	m.seen = append(m.seen, msg.ID)
	m.mu.Unlock()
	if msg.ID == m.failID {
		return common.MediationOutcome{Result: common.MediationErrorProcess, DelaySeconds: 1}
	}
	return common.MediationOutcome{Result: common.MediationSuccess}
}

// submitBatch tags each message with a shared batch id and submits it to the
// passive pool — the role the Manager's route() plays in production.
func submitBatch(ctx context.Context, p *Pool, msgs []common.QueuedMessage) {
	for _, m := range msgs {
		m.BatchID = "b1"
		p.submit(ctx, m)
	}
}

func newCascadePool(med Mediator, resolve func(string) queue.Consumer) *Pool {
	cfg := common.PoolConfig{Code: "test", Concurrency: 1}
	return NewPool(cfg, med, nil, resolve)
}

// TestPoolBatchGroupCascadeNack verifies Rust-parity FIFO behaviour for
// ORDERED-mode messages: when the first message in a (batch, group) fails
// transiently, the remaining ordered messages in that batch+group are NACKed
// without being attempted, preserving ordering. The cascade is an ordering
// feature, so the messages carry BLOCK_ON_ERROR — IMMEDIATE-mode messages
// dispatch concurrently and do NOT cascade (see TestPoolImmediateModeNoCascade).
func TestPoolBatchGroupCascadeNack(t *testing.T) {
	group := "g"
	mk := func(id string) common.QueuedMessage {
		return common.QueuedMessage{
			Message: common.Message{
				ID:              id,
				MediationType:   common.MediationTypeHTTP,
				MediationTarget: "http://example.invalid",
				MessageGroupID:  &group,
				DispatchMode:    common.DispatchBlockOnError,
			},
			ReceiptHandle: id,
		}
	}
	cons := &cascadeConsumer{wantTotal: 3, done: make(chan struct{})}
	med := &cascadeMediator{failID: "m1"}
	pool := newCascadePool(med, func(string) queue.Consumer { return cons })

	submitBatch(context.Background(), pool, []common.QueuedMessage{mk("m1"), mk("m2"), mk("m3")})

	select {
	case <-cons.done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for 3 terminal actions")
	}

	med.mu.Lock()
	seen := append([]string(nil), med.seen...)
	med.mu.Unlock()
	cons.mu.Lock()
	nacked := append([]string(nil), cons.nacked...)
	cons.mu.Unlock()

	assert.Equal(t, []string{"m1"}, seen, "only the first message in the batch+group should be attempted")
	assert.ElementsMatch(t, []string{"m1", "m2", "m3"}, nacked, "all three should be NACKed (m1 transient, m2/m3 cascade)")
}

// TestPoolImmediateModeNoCascade verifies R1: IMMEDIATE-mode messages (the
// default) dispatch independently — a transient failure of one does NOT
// cascade-NACK the others in the same batch+group. With concurrency 1 the
// three run one at a time, but each is attempted regardless of m1's failure.
func TestPoolImmediateModeNoCascade(t *testing.T) {
	group := "g"
	mk := func(id string) common.QueuedMessage {
		return common.QueuedMessage{
			Message: common.Message{
				ID:              id,
				MediationType:   common.MediationTypeHTTP,
				MediationTarget: "http://example.invalid",
				MessageGroupID:  &group,
				DispatchMode:    common.DispatchImmediate,
			},
			ReceiptHandle: id,
		}
	}
	cons := &cascadeConsumer{wantTotal: 3, done: make(chan struct{})}
	med := &cascadeMediator{failID: "m1"}
	pool := newCascadePool(med, func(string) queue.Consumer { return cons })

	submitBatch(context.Background(), pool, []common.QueuedMessage{mk("m1"), mk("m2"), mk("m3")})

	select {
	case <-cons.done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for 3 terminal actions")
	}

	med.mu.Lock()
	seen := append([]string(nil), med.seen...)
	med.mu.Unlock()
	cons.mu.Lock()
	nacked := append([]string(nil), cons.nacked...)
	acked := append([]string(nil), cons.acked...)
	cons.mu.Unlock()

	assert.ElementsMatch(t, []string{"m1", "m2", "m3"}, seen, "all three IMMEDIATE messages should be attempted (no cascade)")
	assert.ElementsMatch(t, []string{"m1"}, nacked, "only the failing message should be NACKed")
	assert.ElementsMatch(t, []string{"m2", "m3"}, acked, "the two successful messages should be ACKed")
}
