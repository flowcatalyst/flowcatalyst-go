package router

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flowcatalyst/flowcatalyst-go/internal/common"
)

// TestMergeConfigsUnionFirstWins covers R5's multi-URL merge: pools are
// keyed by code and queues by URI; the first source to define a key wins and
// later conflicting duplicates are dropped.
func TestMergeConfigsUnionFirstWins(t *testing.T) {
	c5 := uint32(5)
	a := common.RouterConfig{
		ProcessingPools: []common.PoolConfig{{Code: "P1", Concurrency: 5, RateLimitPerMinute: &c5}},
		Queues:          []common.QueueConfig{{Name: "q1", URI: "uri1", Connections: 1}},
	}
	b := common.RouterConfig{
		ProcessingPools: []common.PoolConfig{
			{Code: "P1", Concurrency: 9}, // same code, different value → dropped
			{Code: "P2", Concurrency: 3},
		},
		Queues: []common.QueueConfig{
			{Name: "q1-dup", URI: "uri1", Connections: 2}, // same URI → dropped
			{Name: "q2", URI: "uri2", Connections: 1},
		},
	}

	merged := mergeConfigs([]sourceConfig{{url: "A", cfg: a}, {url: "B", cfg: b}})

	require.Len(t, merged.ProcessingPools, 2)
	assert.Equal(t, "P1", merged.ProcessingPools[0].Code)
	assert.Equal(t, uint32(5), merged.ProcessingPools[0].Concurrency, "first-wins on pool conflict")
	assert.Equal(t, "P2", merged.ProcessingPools[1].Code)

	require.Len(t, merged.Queues, 2)
	assert.Equal(t, "uri1", merged.Queues[0].URI)
	assert.Equal(t, "q1", merged.Queues[0].Name, "first-wins on queue URI conflict")
	assert.Equal(t, "uri2", merged.Queues[1].URI)
}

// TestMergeConfigsSinglePassthrough verifies a single source is returned
// unchanged (no dedup pass).
func TestMergeConfigsSinglePassthrough(t *testing.T) {
	a := common.RouterConfig{Queues: []common.QueueConfig{{Name: "q1", URI: "uri1"}}}
	merged := mergeConfigs([]sourceConfig{{url: "A", cfg: a}})
	assert.Equal(t, a.Queues, merged.Queues)
}

// TestNewConfigSourceParsesCommaSeparated verifies multi-URL parsing +
// retry defaults.
func TestNewConfigSourceParsesCommaSeparated(t *testing.T) {
	cs := NewConfigSource(" http://a/cfg , http://b/cfg ,, http://c/cfg ")
	assert.Equal(t, []string{"http://a/cfg", "http://b/cfg", "http://c/cfg"}, cs.URLs)
	assert.Equal(t, 12, cs.MaxAttempts)
}
