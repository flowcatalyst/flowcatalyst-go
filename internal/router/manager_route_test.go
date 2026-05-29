package router

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/flowcatalyst/flowcatalyst-go/internal/common"
	"github.com/flowcatalyst/flowcatalyst-go/internal/queue"
)

// TestManagerPoolForMessage verifies R2 routing resolution: a message routes
// to the pool named by its pool_code; an empty or unknown pool_code falls
// back to DEFAULT-POOL.
func TestManagerPoolForMessage(t *testing.T) {
	med := &cascadeMediator{}
	m := NewManager(med, nil)
	resolve := func(string) queue.Consumer { return nil }
	poolA := NewPool(common.PoolConfig{Code: "A"}, med, nil, resolve)
	poolDefault := NewPool(common.PoolConfig{Code: defaultPoolCode}, med, nil, resolve)
	m.pools["A"] = poolA
	m.pools[defaultPoolCode] = poolDefault

	mk := func(poolCode string) common.QueuedMessage {
		return common.QueuedMessage{Message: common.Message{ID: "x", PoolCode: poolCode}}
	}

	assert.Same(t, poolA, m.poolForMessage(mk("A")), "pool_code A → pool A")
	assert.Same(t, poolDefault, m.poolForMessage(mk("")), "empty pool_code → DEFAULT-POOL")
	assert.Same(t, poolDefault, m.poolForMessage(mk("NOPE")), "unknown pool_code → DEFAULT-POOL")
}

// TestInFlightTrackerExternalRequeue covers the R4 dedup predicate: the same
// app message id in flight under a DIFFERENT broker id is an external requeue;
// the same broker id, an unknown id, or a blank broker id is not.
func TestInFlightTrackerExternalRequeue(t *testing.T) {
	tr := NewInFlightTracker()
	tr.Insert(common.NewInFlightMessage(&common.Message{ID: "app1"}, "broker1", "q", "b", "rh"))

	assert.True(t, tr.IsExternalRequeue("app1", "broker2"), "different broker id → external requeue")
	assert.False(t, tr.IsExternalRequeue("app1", "broker1"), "same broker id → not a requeue")
	assert.False(t, tr.IsExternalRequeue("app2", "broker2"), "unknown app id → not a requeue")
	assert.False(t, tr.IsExternalRequeue("app1", ""), "blank broker id → not a requeue")
}

// TestManagerRouteExternalRequeueAcks verifies R4 end-to-end: route() ACK-drops
// a message whose app id is already in flight under a different broker id,
// rather than submitting it to a pool.
func TestManagerRouteExternalRequeueAcks(t *testing.T) {
	med := &cascadeMediator{}
	tr := NewInFlightTracker()
	m := NewManager(med, tr)
	m.pools[defaultPoolCode] = NewPool(common.PoolConfig{Code: defaultPoolCode}, med, tr, func(string) queue.Consumer { return nil })

	// The original is in flight under broker1.
	tr.Insert(common.NewInFlightMessage(&common.Message{ID: "app1"}, "broker1", "q", "b", "rh-orig"))

	cons := &cascadeConsumer{wantTotal: 1, done: make(chan struct{})}
	requeued := common.QueuedMessage{
		Message:         common.Message{ID: "app1"},
		BrokerMessageID: "broker2",
		ReceiptHandle:   "rh-requeue",
		QueueIdentifier: "q",
	}
	m.route(context.Background(), []common.QueuedMessage{requeued}, cons)

	select {
	case <-cons.done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the requeued duplicate to be ACKed")
	}

	cons.mu.Lock()
	acked := append([]string(nil), cons.acked...)
	cons.mu.Unlock()
	assert.Equal(t, []string{"rh-requeue"}, acked, "the external-requeue duplicate should be ACKed and not submitted")

	med.mu.Lock()
	seen := append([]string(nil), med.seen...)
	med.mu.Unlock()
	assert.Empty(t, seen, "the requeued duplicate must not be mediated")
}
