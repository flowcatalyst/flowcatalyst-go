package client

import (
	"context"
	"encoding/json"
)

// SyncOpenAPISpecRequest — body for POST /api/applications/{appCode}/openapi/sync.
type SyncOpenAPISpecRequest struct {
	Spec json.RawMessage `json:"spec"`
}

// SyncOpenAPISpecResponse — result of an openapi sync.
type SyncOpenAPISpecResponse struct {
	ApplicationCode      string `json:"applicationCode"`
	SpecID               string `json:"specId"`
	Version              string `json:"version"`
	Status               string `json:"status"`
	ArchivedPriorVersion string `json:"archivedPriorVersion,omitempty"`
	HasBreaking          bool   `json:"hasBreaking"`
	// Unchanged is true when the submitted spec is byte-identical to the
	// currently published version — the platform short-circuits and
	// returns the existing SpecID/Version.
	Unchanged bool `json:"unchanged"`
}

// OpenAPIResource — /api/applications/{appCode}/openapi/*.
type OpenAPIResource struct {
	c *FlowCatalystClient
}

// Sync publishes or replaces this application's OpenAPI document. Each
// call replaces the currently-published spec; resubmitting the same
// content is detected on the server side via Unchanged=true.
func (r *OpenAPIResource) Sync(ctx context.Context, appCode string, spec json.RawMessage) (*SyncOpenAPISpecResponse, error) {
	var out SyncOpenAPISpecResponse
	if err := r.c.Post(ctx, "/api/applications/"+appCode+"/openapi/sync",
		&SyncOpenAPISpecRequest{Spec: spec}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
