package router

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/flowcatalyst/flowcatalyst-go/internal/common"
)

// A message group is a strict FIFO: messages drain in arrival order regardless
// of HighPriority (which is a queue-level concern and must NOT reorder within a
// group — doing so would defeat in-order delivery).
func TestGroupQueueIsStrictFIFO(t *testing.T) {
	gq := &groupQueue{}

	mk := func(id string, hi bool) common.QueuedMessage {
		return common.QueuedMessage{Message: common.Message{ID: id, HighPriority: hi}}
	}

	// Interleave regular and "high priority" — HighPriority must be ignored.
	gq.msgs = append(gq.msgs, []common.QueuedMessage{
		mk("a", false), mk("b", true), mk("c", false), mk("d", true),
	}...)

	var drained []string
	for !gq.empty() {
		m, _ := gq.pop()
		drained = append(drained, m.Message.ID)
	}
	assert.Equal(t, []string{"a", "b", "c", "d"}, drained, "group drains in strict arrival order")
}

func TestGroupQueueEmptyAfterAllPopped(t *testing.T) {
	gq := &groupQueue{}
	gq.msgs = append(gq.msgs, common.QueuedMessage{Message: common.Message{ID: "r1"}})
	assert.False(t, gq.empty())
	_, empty := gq.pop()
	assert.True(t, empty)
	assert.True(t, gq.empty())
}

func TestPoolEnqueueAppendsToBackEnqueueFrontPrepends(t *testing.T) {
	p := &Pool{groupQs: map[string]*groupQueue{}}
	p.enqueue("g1", common.QueuedMessage{Message: common.Message{ID: "m1"}})
	p.enqueue("g1", common.QueuedMessage{Message: common.Message{ID: "m2"}})
	// A retry re-inserts at the front so it is attempted before m1/m2.
	p.enqueueFront("g1", common.QueuedMessage{Message: common.Message{ID: "retry"}})

	g1 := p.groupQs["g1"]
	got := make([]string, 0, len(g1.msgs))
	for _, m := range g1.msgs {
		got = append(got, m.Message.ID)
	}
	assert.Equal(t, []string{"retry", "m1", "m2"}, got, "enqueue → back, enqueueFront → head")
}
