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
