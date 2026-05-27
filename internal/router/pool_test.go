package router

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/flowcatalyst/flowcatalyst-go/internal/common"
)

func TestGroupQueueHighPriorityDrainsFirst(t *testing.T) {
	gq := &groupQueue{}

	mk := func(id string, hi bool) common.QueuedMessage {
		return common.QueuedMessage{
			Message: common.Message{ID: id, HighPriority: hi},
		}
	}

	// Interleave: regular, regular, high, regular, high.
	in := []common.QueuedMessage{
		mk("r1", false),
		mk("r2", false),
		mk("h1", true),
		mk("r3", false),
		mk("h2", true),
	}
	for _, m := range in {
		if m.Message.HighPriority {
			gq.highPriority = append(gq.highPriority, m)
		} else {
			gq.regular = append(gq.regular, m)
		}
	}

	// Drain order: all high-priority in FIFO order first, then regulars
	// in FIFO order. Matches Rust pool.rs::MessageGroupHandler::dequeue.
	var drained []string
	for !gq.empty() {
		m, _ := gq.pop()
		drained = append(drained, m.Message.ID)
	}
	assert.Equal(t, []string{"h1", "h2", "r1", "r2", "r3"}, drained)
}

func TestGroupQueueEmptyAfterAllPopped(t *testing.T) {
	gq := &groupQueue{}
	gq.regular = append(gq.regular, common.QueuedMessage{Message: common.Message{ID: "r1"}})
	assert.False(t, gq.empty())
	_, empty := gq.pop()
	assert.True(t, empty)
	assert.True(t, gq.empty())
}

func TestPoolEnqueueRoutesByPriority(t *testing.T) {
	p := &Pool{groupQs: map[string]*groupQueue{}}
	p.enqueue("g1", common.QueuedMessage{Message: common.Message{ID: "r1", HighPriority: false}})
	p.enqueue("g1", common.QueuedMessage{Message: common.Message{ID: "h1", HighPriority: true}})
	p.enqueue("g2", common.QueuedMessage{Message: common.Message{ID: "h2", HighPriority: true}})

	g1 := p.groupQs["g1"]
	assert.Len(t, g1.regular, 1)
	assert.Equal(t, "r1", g1.regular[0].Message.ID)
	assert.Len(t, g1.highPriority, 1)
	assert.Equal(t, "h1", g1.highPriority[0].Message.ID)

	g2 := p.groupQs["g2"]
	assert.Empty(t, g2.regular)
	assert.Len(t, g2.highPriority, 1)
}
