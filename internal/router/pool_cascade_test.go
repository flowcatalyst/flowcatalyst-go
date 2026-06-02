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

// cascadeMediator records every ID it was asked to mediate and fails the
// message whose ID == failID. failTimes bounds how many times failID fails
// before it starts succeeding (0 = fail forever); this lets a test exercise
// the in-pipeline retry loop and confirm a message eventually succeeds. A
// transient failure returns DelaySeconds:0 so the retry backoff stays small.
type cascadeMediator struct {
	failID    string
	failTimes int
	mu        sync.Mutex
	seen      []string
	failed    int
}

func (m *cascadeMediator) Mediate(_ context.Context, msg *common.Message) common.MediationOutcome {
	m.mu.Lock()
	m.seen = append(m.seen, msg.ID)
	fail := false
	if msg.ID == m.failID && (m.failTimes == 0 || m.failed < m.failTimes) {
		m.failed++
		fail = true
	}
	m.mu.Unlock()
	if fail {
		return common.MediationOutcome{Result: common.MediationErrorProcess, DelaySeconds: 0}
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

// TestPoolOrderedRetryPreservesFIFO verifies the ordered-delivery contract
// under the in-pipeline retry model: when the head of an ordered group fails
// transiently, it is re-inserted at the FRONT and retried (blocking the group,
// head-of-line) until it succeeds — and only THEN do the later messages run.
// Nothing is released to the broker on failure (no NACKs); the message stays in
// the pipeline. m1 fails twice then succeeds, so the attempt order is
// m1,m1,m1,m2,m3 and all three are ACKed.
func TestPoolOrderedRetryPreservesFIFO(t *testing.T) {
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
	med := &cascadeMediator{failID: "m1", failTimes: 2}
	pool := newCascadePool(med, func(string) queue.Consumer { return cons })

	submitBatch(context.Background(), pool, []common.QueuedMessage{mk("m1"), mk("m2"), mk("m3")})

	select {
	case <-cons.done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for 3 ACKs")
	}

	med.mu.Lock()
	seen := append([]string(nil), med.seen...)
	med.mu.Unlock()
	cons.mu.Lock()
	nacked := append([]string(nil), cons.nacked...)
	acked := append([]string(nil), cons.acked...)
	cons.mu.Unlock()

	assert.Equal(t, []string{"m1", "m1", "m1", "m2", "m3"}, seen,
		"m1 must be retried at the front until it succeeds before m2/m3 are attempted")
	assert.Empty(t, nacked, "in-pipeline retries must not NACK to the broker")
	assert.ElementsMatch(t, []string{"m1", "m2", "m3"}, acked, "all three should ACK on success")
}

// TestPoolImmediateRetriesIndependently verifies that IMMEDIATE-mode messages
// retry in-pipeline too, but independently (no group ordering): a transient
// failure of one is retried until it succeeds without NACKing to the broker and
// without blocking the others. m1 fails twice then succeeds; m2/m3 succeed
// immediately; all three are eventually ACKed and m1 is attempted three times.
func TestPoolImmediateRetriesIndependently(t *testing.T) {
	mk := func(id string) common.QueuedMessage {
		return common.QueuedMessage{
			Message: common.Message{
				ID:              id,
				MediationType:   common.MediationTypeHTTP,
				MediationTarget: "http://example.invalid",
				DispatchMode:    common.DispatchImmediate,
			},
			ReceiptHandle: id,
		}
	}
	cons := &cascadeConsumer{wantTotal: 3, done: make(chan struct{})}
	med := &cascadeMediator{failID: "m1", failTimes: 2}
	pool := newCascadePool(med, func(string) queue.Consumer { return cons })

	submitBatch(context.Background(), pool, []common.QueuedMessage{mk("m1"), mk("m2"), mk("m3")})

	select {
	case <-cons.done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for 3 ACKs")
	}

	med.mu.Lock()
	m1Attempts := 0
	for _, id := range med.seen {
		if id == "m1" {
			m1Attempts++
		}
	}
	med.mu.Unlock()
	cons.mu.Lock()
	nacked := append([]string(nil), cons.nacked...)
	acked := append([]string(nil), cons.acked...)
	cons.mu.Unlock()

	assert.Equal(t, 3, m1Attempts, "m1 is retried in-pipeline until it succeeds (2 fails + 1 success)")
	assert.Empty(t, nacked, "in-pipeline retries must not NACK to the broker")
	assert.ElementsMatch(t, []string{"m1", "m2", "m3"}, acked, "all three should ACK on success")
}
