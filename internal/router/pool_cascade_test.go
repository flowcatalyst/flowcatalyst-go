package router_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/flowcatalyst/flowcatalyst-go/internal/common"
	"github.com/flowcatalyst/flowcatalyst-go/internal/queue"
	"github.com/flowcatalyst/flowcatalyst-go/internal/router"
)

// cascadeConsumer delivers a single poll batch, then empty polls. It records
// the terminal action per receipt handle and closes `done` once the expected
// number of terminal actions has been observed.
type cascadeConsumer struct {
	batch     []common.QueuedMessage
	wantTotal int
	done      chan struct{}

	mu        sync.Mutex
	delivered bool
	nacked    []string
	acked     []string
	deferred  []string
	doneOnce  sync.Once
}

func (c *cascadeConsumer) Poll(_ context.Context, _ uint32) ([]common.QueuedMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.delivered {
		c.delivered = true
		return c.batch, nil
	}
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
func (c *cascadeConsumer) ExtendVisibility(_ context.Context, _ string, _ uint32) error { return nil }
func (c *cascadeConsumer) Identifier() string                                           { return "cascade-test" }
func (c *cascadeConsumer) Healthy() bool                                                { return true }
func (c *cascadeConsumer) Stop()                                                        {}
func (c *cascadeConsumer) Metrics(_ context.Context) (*queue.Metrics, error) {
	return &queue.Metrics{}, nil
}
func (c *cascadeConsumer) Counters() *queue.Metrics { return nil }

// cascadeMediator fails the message whose ID == failID and records every ID it
// was actually asked to mediate.
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

// TestPoolBatchGroupCascadeNack verifies Rust-parity FIFO behaviour: when the
// first message in a (batch, group) fails transiently, the remaining messages
// in that same batch+group are NACKed without being attempted, preserving
// ordering. All three messages arrive in one poll batch (one batch id) and
// share a message group.
func TestPoolBatchGroupCascadeNack(t *testing.T) {
	group := "g"
	mk := func(id string) common.QueuedMessage {
		return common.QueuedMessage{
			Message: common.Message{
				ID:              id,
				MediationType:   common.MediationTypeHTTP,
				MediationTarget: "http://example.invalid",
				MessageGroupID:  &group,
			},
			ReceiptHandle: id,
		}
	}
	cons := &cascadeConsumer{
		batch:     []common.QueuedMessage{mk("m1"), mk("m2"), mk("m3")},
		wantTotal: 3,
		done:      make(chan struct{}),
	}
	med := &cascadeMediator{failID: "m1"}

	cfg := common.PoolConfig{Code: "test", Concurrency: 1}
	pool := router.NewPool(cfg, cons, med, router.NewBreakerRegistry(router.DefaultBreakerConfig()), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pool.Run(ctx)

	select {
	case <-cons.done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for 3 terminal actions")
	}
	cancel()

	med.mu.Lock()
	seen := append([]string(nil), med.seen...)
	med.mu.Unlock()
	cons.mu.Lock()
	nacked := append([]string(nil), cons.nacked...)
	cons.mu.Unlock()

	assert.Equal(t, []string{"m1"}, seen, "only the first message in the batch+group should be attempted")
	assert.ElementsMatch(t, []string{"m1", "m2", "m3"}, nacked, "all three should be NACKed (m1 transient, m2/m3 cascade)")
}
