package common

import (
	"encoding/json"
	"testing"
)

// TestQueueConfigUnmarshal_CamelCaseWire verifies the canonical config-service
// wire shape ({queueName, queueUri}) decodes correctly. This is the shape the
// central config service / Rust router emit and MUST stay interoperable.
func TestQueueConfigUnmarshal_CamelCaseWire(t *testing.T) {
	const body = `{"queueName":"orders","queueUri":"sqs://orders","connections":4,"visibilityTimeout":60}`
	var q QueueConfig
	if err := json.Unmarshal([]byte(body), &q); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if q.Name != "orders" || q.URI != "sqs://orders" || q.Connections != 4 || q.VisibilityTimeout != 60 {
		t.Fatalf("got %+v", q)
	}
}

// TestQueueConfigUnmarshal_LegacyAliases verifies the legacy {name, uri} keys
// produced by older Go builds still decode.
func TestQueueConfigUnmarshal_LegacyAliases(t *testing.T) {
	const body = `{"name":"orders","uri":"sqs://orders","connections":2,"visibilityTimeout":90}`
	var q QueueConfig
	if err := json.Unmarshal([]byte(body), &q); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if q.Name != "orders" || q.URI != "sqs://orders" || q.Connections != 2 || q.VisibilityTimeout != 90 {
		t.Fatalf("got %+v", q)
	}
}

// TestQueueConfigUnmarshal_Defaults mirrors the Rust QueueConfigResponse ->
// QueueConfig conversion: name falls back to uri, connections -> 1,
// visibilityTimeout -> 120 when absent.
func TestQueueConfigUnmarshal_Defaults(t *testing.T) {
	const body = `{"queueUri":"sqs://orders"}`
	var q QueueConfig
	if err := json.Unmarshal([]byte(body), &q); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if q.Name != "sqs://orders" {
		t.Fatalf("name should default to uri, got %q", q.Name)
	}
	if q.Connections != 1 {
		t.Fatalf("connections should default to 1, got %d", q.Connections)
	}
	if q.VisibilityTimeout != 120 {
		t.Fatalf("visibilityTimeout should default to 120, got %d", q.VisibilityTimeout)
	}
}

// TestQueueConfigMarshal_EmitsCamelCase verifies a Go-served config is readable
// by an existing Rust router (which expects queueName/queueUri).
func TestQueueConfigMarshal_EmitsCamelCase(t *testing.T) {
	q := QueueConfig{Name: "orders", URI: "sqs://orders", Connections: 1, VisibilityTimeout: 120}
	b, err := json.Marshal(q)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	if _, ok := raw["queueName"]; !ok {
		t.Fatalf("expected queueName key, got %s", string(b))
	}
	if _, ok := raw["queueUri"]; !ok {
		t.Fatalf("expected queueUri key, got %s", string(b))
	}
}

// TestRouterConfigUnmarshal_FullWire exercises the full config-service payload
// shape end to end.
func TestRouterConfigUnmarshal_FullWire(t *testing.T) {
	const body = `{
		"processingPools":[{"code":"DEFAULT-POOL","concurrency":8,"rateLimitPerMinute":600}],
		"queues":[{"queueName":"q1","queueUri":"sqs://q1"},{"queueUri":"sqs://q2"}]
	}`
	var cfg RouterConfig
	if err := json.Unmarshal([]byte(body), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(cfg.ProcessingPools) != 1 || cfg.ProcessingPools[0].Code != "DEFAULT-POOL" {
		t.Fatalf("pools: %+v", cfg.ProcessingPools)
	}
	if len(cfg.Queues) != 2 {
		t.Fatalf("expected 2 queues, got %d", len(cfg.Queues))
	}
	if cfg.Queues[0].Name != "q1" || cfg.Queues[0].URI != "sqs://q1" {
		t.Fatalf("queue0: %+v", cfg.Queues[0])
	}
	// Second queue omits queueName -> defaults to its uri.
	if cfg.Queues[1].Name != "sqs://q2" {
		t.Fatalf("queue1 name should default to uri, got %q", cfg.Queues[1].Name)
	}
}
