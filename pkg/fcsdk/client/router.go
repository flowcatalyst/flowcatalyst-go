package client

import "context"

// Router monitoring endpoints. These call the message-router (a
// separate process from the platform) at routerBaseURL configured on
// the client. When unset, calls fall back to baseURL — correct only
// when router and platform are co-located (e.g. fc-dev).
//
// Designed for external recovery / replay processes that maintain their
// own list of "messages that look stuck" and want to confirm whether
// the router is still actively processing each one before re-enqueueing.

// InPipelineCheckResponse — GET /monitoring/in-flight-messages/check.
// InPipeline=true means the router currently holds the message; the
// caller should not re-enqueue. InPipeline=false → safe to resend.
type InPipelineCheckResponse struct {
	MessageID  string            `json:"messageId"`
	InPipeline bool              `json:"inPipeline"`
	Detail     *InPipelineDetail `json:"detail,omitempty"`
}

// InPipelineDetail — populated when InPipeline=true.
type InPipelineDetail struct {
	MessageID           string `json:"messageId"`
	BrokerMessageID     string `json:"brokerMessageId,omitempty"`
	QueueID             string `json:"queueId"`
	PoolCode            string `json:"poolCode"`
	ElapsedTimeMs       uint64 `json:"elapsedTimeMs"`
	AddedToInPipelineAt string `json:"addedToInPipelineAt"`
}

// InPipelineBatchRequest — body for /check-batch. Capped at 5000 ids.
type InPipelineBatchRequest struct {
	MessageIDs []string `json:"messageIds"`
}

// RouterResource — message-router monitoring endpoints. Uses
// routerBaseURL (or baseURL as fallback).
type RouterResource struct {
	c *FlowCatalystClient
}

// InPipeline — GET /monitoring/in-flight-messages/check?messageId=...
// Returns whether the router currently holds the message.
func (r *RouterResource) InPipeline(ctx context.Context, messageID string) (*InPipelineCheckResponse, error) {
	q := NewQuery().String("messageId", messageID).Encode()
	var out InPipelineCheckResponse
	if err := r.c.GetRouter(ctx, "/monitoring/in-flight-messages/check"+q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// InPipelineBatch — POST /monitoring/in-flight-messages/check-batch.
// Returns messageId → bool for each input. Server caps the batch at 5000.
func (r *RouterResource) InPipelineBatch(ctx context.Context, messageIDs []string) (map[string]bool, error) {
	body := &InPipelineBatchRequest{MessageIDs: messageIDs}
	var out map[string]bool
	if err := r.c.postRouter(ctx, "/monitoring/in-flight-messages/check-batch", body, &out); err != nil {
		return nil, err
	}
	return out, nil
}
