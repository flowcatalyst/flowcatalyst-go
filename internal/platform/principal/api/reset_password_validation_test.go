package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/httpcompat"
)

// TestResetPasswordAcceptsSDKBody pins the reset-password body contract against
// the Laravel SDK, which posts {newPassword, enforcePasswordComplexity}.
//
// huma generates request schemas with additionalProperties:false, and this
// service's error model (httpcompat.newError) renders EVERY huma validation
// failure as 400 "validation failed" (it deliberately ignores huma's intended
// 422). So before enforcePasswordComplexity existed on ResetPasswordRequest, the
// SDK's extra field was rejected as 400 "validation failed" — exactly the error
// seen in production. This test would fail if that field is ever dropped again.
func TestResetPasswordAcceptsSDKBody(t *testing.T) {
	httpcompat.Init() // install the prod {error,message}+forced-400 error model

	_, api := humatest.New(t)
	huma.Register(api, huma.Operation{
		OperationID: "test-reset-password-validation",
		Method:      http.MethodPost,
		Path:        "/api/principals/{id}/reset-password",
	}, func(_ context.Context, _ *resetPasswordInput) (*statusMessageOutput, error) {
		// Reaching the handler means body validation passed.
		return &statusMessageOutput{}, nil
	})

	// The exact SDK payload must pass validation (no 400).
	ok := api.Post("/api/principals/p_123/reset-password",
		map[string]any{"newPassword": "hunter22!", "enforcePasswordComplexity": false})
	if ok.Code == http.StatusBadRequest {
		t.Fatalf("SDK reset-password body was rejected: %d %s", ok.Code, ok.Body.String())
	}

	// A genuinely unknown field still fails — proves additionalProperties is the
	// mechanism and the wire shape is the same 400 "validation failed" the SDK saw.
	bad := api.Post("/api/principals/p_123/reset-password",
		map[string]any{"newPassword": "hunter22!", "bogusField": 1})
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unknown field, got %d: %s", bad.Code, bad.Body.String())
	}
	if !strings.Contains(bad.Body.String(), "validation failed") {
		t.Fatalf("expected 'validation failed' envelope, got: %s", bad.Body.String())
	}
}

// TestSendPasswordResetAllowsEmptyBody pins that the admin "Resend Reset
// Password" action — which POSTs with no request body — reaches the handler.
// sendPasswordResetInput.Body is a pointer (optional); a non-pointer body made
// huma reject the body-less call with "request body is required", which is the
// production error. A body carrying reset2fa must keep working too.
func TestSendPasswordResetAllowsEmptyBody(t *testing.T) {
	httpcompat.Init()

	_, api := humatest.New(t)
	huma.Register(api, huma.Operation{
		OperationID: "test-send-password-reset-body",
		Method:      http.MethodPost,
		Path:        "/api/principals/{id}/send-password-reset",
	}, func(_ context.Context, _ *sendPasswordResetInput) (*statusMessageOutput, error) {
		return &statusMessageOutput{}, nil
	})

	// Body-less POST (what the SPA sends) must not be a 400.
	empty := api.Post("/api/principals/p_123/send-password-reset")
	if empty.Code == http.StatusBadRequest {
		t.Fatalf("body-less send-password-reset was rejected: %d %s", empty.Code, empty.Body.String())
	}

	// An explicit reset2fa body must also pass validation.
	withBody := api.Post("/api/principals/p_123/send-password-reset",
		map[string]any{"reset2fa": true})
	if withBody.Code == http.StatusBadRequest {
		t.Fatalf("reset2fa body was rejected: %d %s", withBody.Code, withBody.Body.String())
	}
}
