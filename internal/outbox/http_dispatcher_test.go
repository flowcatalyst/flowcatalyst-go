package outbox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/internal/common"
)

func newItem() Item {
	return Item{ID: "ob1", ItemType: common.OutboxItemEvent, Payload: json.RawMessage(`{"k":"v"}`)}
}

// OB5 regression: a 2xx response whose per-item result is a failure must NOT
// be classified as success — the prior code ignored the body and marked the
// whole batch success on any 2xx.
func TestSend_PerItemFailureWithin2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK) // 2xx envelope...
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{"id": "ob1", "status": "BAD_REQUEST", "error": "schema invalid"}},
		})
	}))
	defer srv.Close()

	d := NewHTTPDispatcher(srv.URL, "", 5*time.Second)
	out := d.Send(context.Background(), newItem())
	if out.Status != common.OutboxBadRequest {
		t.Fatalf("Send status = %v, want BAD_REQUEST (per-item failure inside a 2xx)", out.Status)
	}
	if out.Message != "schema invalid" {
		t.Errorf("Send message = %q, want the per-item error", out.Message)
	}
}

func TestSend_PerItemSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{"id": "ob1", "status": "SUCCESS"}},
		})
	}))
	defer srv.Close()

	d := NewHTTPDispatcher(srv.URL, "", 5*time.Second)
	if out := d.Send(context.Background(), newItem()); out.Status != common.OutboxSuccess {
		t.Fatalf("Send status = %v, want SUCCESS", out.Status)
	}
}

func TestSend_Non2xxFallsBackToHTTPStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	d := NewHTTPDispatcher(srv.URL, "", 5*time.Second)
	if out := d.Send(context.Background(), newItem()); out.Status != common.OutboxGatewayError {
		t.Fatalf("Send status = %v, want GATEWAY_ERROR for 503", out.Status)
	}
}

func batchItem(id string) Item {
	return Item{ID: id, ItemType: common.OutboxItemEvent, Payload: json.RawMessage(`{"id":"` + id + `"}`)}
}

// OB4: SendBatch posts all items in one call and maps each per-item result;
// an item missing from results is INTERNAL_ERROR (retryable).
func TestSendBatch_PerItemResults(t *testing.T) {
	var gotItems int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Items []json.RawMessage `json:"items"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotItems = len(body.Items)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{
			{"id": "a", "status": "SUCCESS"},
			{"id": "b", "status": "BAD_REQUEST", "error": "bad"},
			// "c" intentionally omitted from results.
		}})
	}))
	defer srv.Close()

	d := NewHTTPDispatcher(srv.URL, "", 5*time.Second)
	out := d.SendBatch(context.Background(), []Item{batchItem("a"), batchItem("b"), batchItem("c")})
	if gotItems != 3 {
		t.Fatalf("server received %d items, want 3 (one batched call)", gotItems)
	}
	if out["a"].Status != common.OutboxSuccess {
		t.Errorf("a = %v, want SUCCESS", out["a"].Status)
	}
	if out["b"].Status != common.OutboxBadRequest || out["b"].Message != "bad" {
		t.Errorf("b = %v/%q, want BAD_REQUEST/bad", out["b"].Status, out["b"].Message)
	}
	if out["c"].Status != common.OutboxInternalError {
		t.Errorf("c (missing from results) = %v, want INTERNAL_ERROR", out["c"].Status)
	}
}

// A non-2xx fails the whole batch with the mapped status.
func TestSendBatch_Non2xxFailsAll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	d := NewHTTPDispatcher(srv.URL, "", 5*time.Second)
	out := d.SendBatch(context.Background(), []Item{batchItem("a"), batchItem("b")})
	for _, id := range []string{"a", "b"} {
		if out[id].Status != common.OutboxGatewayError {
			t.Fatalf("%s = %v, want GATEWAY_ERROR for 502", id, out[id].Status)
		}
	}
}

func TestParseItemStatus(t *testing.T) {
	cases := map[string]common.OutboxStatus{
		"SUCCESS":        common.OutboxSuccess,
		"BAD_REQUEST":    common.OutboxBadRequest,
		"INTERNAL_ERROR": common.OutboxInternalError,
		"UNAUTHORIZED":   common.OutboxUnauthorized,
		"FORBIDDEN":      common.OutboxForbidden,
		"GATEWAY_ERROR":  common.OutboxGatewayError,
	}
	for s, want := range cases {
		got, ok := parseItemStatus(s)
		if !ok || got != want {
			t.Errorf("parseItemStatus(%q) = (%v,%v), want (%v,true)", s, got, ok, want)
		}
	}
	if _, ok := parseItemStatus("WAT"); ok {
		t.Error("parseItemStatus(WAT) should be ok=false")
	}
}
