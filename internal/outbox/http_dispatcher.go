package outbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/internal/common"
)

// batchResponse / itemResult mirror the platform batch-ingest response
// {results:[{id,status,error?}]} (status is SCREAMING_SNAKE_CASE per item).
type batchResponse struct {
	Results []itemResult `json:"results"`
}

type itemResult struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// parseItemStatus maps the per-item wire status to an OutboxStatus. The wire
// strings are exactly OutboxStatus.String() (SCREAMING_SNAKE_CASE).
func parseItemStatus(s string) (common.OutboxStatus, bool) {
	switch s {
	case "SUCCESS":
		return common.OutboxSuccess, true
	case "BAD_REQUEST":
		return common.OutboxBadRequest, true
	case "INTERNAL_ERROR":
		return common.OutboxInternalError, true
	case "UNAUTHORIZED":
		return common.OutboxUnauthorized, true
	case "FORBIDDEN":
		return common.OutboxForbidden, true
	case "GATEWAY_ERROR":
		return common.OutboxGatewayError, true
	}
	return 0, false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// HTTPDispatcher sends outbox items to the FlowCatalyst platform API.
// Mirrors fc-outbox/src/http_dispatcher.rs.
type HTTPDispatcher struct {
	platformURL string
	authToken   string
	client      *http.Client
}

// NewHTTPDispatcher wires a dispatcher.
func NewHTTPDispatcher(platformURL, authToken string, timeout time.Duration) *HTTPDispatcher {
	return &HTTPDispatcher{
		platformURL: platformURL,
		authToken:   authToken,
		client:      &http.Client{Timeout: timeout},
	}
}

// DispatchOutcome is the result of sending one item.
type DispatchOutcome struct {
	Status  common.OutboxStatus
	Message string
}

// Send POSTs the item's payload to the appropriate batch endpoint and
// classifies the response into an OutboxStatus.
func (d *HTTPDispatcher) Send(ctx context.Context, item Item) DispatchOutcome {
	endpoint := d.platformURL + item.ItemType.APIPath()
	body, err := json.Marshal(map[string]any{"items": []json.RawMessage{item.Payload}})
	if err != nil {
		return DispatchOutcome{Status: common.OutboxBadRequest, Message: "marshal: " + err.Error()}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return DispatchOutcome{Status: common.OutboxInternalError, Message: "build: " + err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	if d.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+d.authToken)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return DispatchOutcome{Status: common.OutboxInternalError, Message: "request: " + err.Error()}
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		// OB5: a 2xx is NOT blanket success — the platform reports a PER-ITEM
		// outcome in {results:[{id,status,error}]}. A batch can return 2xx
		// while individual items are BAD_REQUEST/etc. Honour the per-item
		// status (single-item batch → results[0]); a parse failure or empty
		// results falls back to INTERNAL_ERROR (retryable), matching Rust.
		var br batchResponse
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err := json.Unmarshal(body, &br); err != nil || len(br.Results) == 0 {
			return DispatchOutcome{Status: common.OutboxInternalError, Message: "parse results: " + truncate(string(body), 200)}
		}
		r := br.Results[0]
		st, ok := parseItemStatus(r.Status)
		if !ok {
			return DispatchOutcome{Status: common.OutboxInternalError, Message: "unknown item status: " + r.Status}
		}
		return DispatchOutcome{Status: st, Message: r.Error}
	case resp.StatusCode == http.StatusUnauthorized:
		return DispatchOutcome{Status: common.OutboxUnauthorized, Message: "401"}
	case resp.StatusCode == http.StatusForbidden:
		return DispatchOutcome{Status: common.OutboxForbidden, Message: "403"}
	case resp.StatusCode == http.StatusBadGateway,
		resp.StatusCode == http.StatusServiceUnavailable,
		resp.StatusCode == http.StatusGatewayTimeout:
		return DispatchOutcome{Status: common.OutboxGatewayError, Message: fmt.Sprintf("%d", resp.StatusCode)}
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return DispatchOutcome{Status: common.OutboxBadRequest, Message: fmt.Sprintf("%d", resp.StatusCode)}
	default:
		return DispatchOutcome{Status: common.OutboxInternalError, Message: fmt.Sprintf("%d", resp.StatusCode)}
	}
}
