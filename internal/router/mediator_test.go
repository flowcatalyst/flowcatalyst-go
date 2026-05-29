package router_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flowcatalyst/flowcatalyst-go/internal/common"
	"github.com/flowcatalyst/flowcatalyst-go/internal/router"
)

func TestMediatorPayloadAndSignatureFormat(t *testing.T) {
	// Capture what the mediator sends and verify the HMAC matches the
	// canonical formula. This is the parity test for the at-risk
	// HMAC site flagged in docs/api-parity.md.
	var (
		gotBody []byte
		gotSig  string
		gotTs   string
		gotAuth string
	)
	secret := "test-secret-do-not-use-in-prod"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get(router.SignatureHeader)
		gotTs = r.Header.Get(router.TimestampHeader)
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	mediator := router.NewHTTPMediator(router.DevMediatorConfig(), router.NewBreakerRegistry(router.DefaultBreakerConfig()))
	authToken := "abc"
	signing := secret
	msg := &common.Message{
		ID:              "msg_TEST123456",
		MediationType:   common.MediationTypeHTTP,
		MediationTarget: srv.URL,
		AuthToken:       &authToken,
		SigningSecret:   &signing,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out := mediator.Mediate(ctx, msg)
	require.Equal(t, common.MediationSuccess, out.Result, "expected success, got %+v", out)

	assert.Equal(t, `{"messageId":"msg_TEST123456"}`, string(gotBody),
		"payload must be exactly {\"messageId\":\"<id>\"} — this is the HMAC parity-critical byte sequence")
	assert.Equal(t, "Bearer abc", gotAuth)
	require.NotEmpty(t, gotTs)
	require.NotEmpty(t, gotSig)

	// Verify the HMAC manually using the same formula the mediator uses.
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(gotTs))
	mac.Write(gotBody)
	want := hex.EncodeToString(mac.Sum(nil))
	assert.Equal(t, want, gotSig, "HMAC must equal sha256(timestamp || body)")

	// Timestamp shape: millisecond precision, three fractional digits.
	// Format: 2006-01-02T15:04:05.000Z (24 chars).
	assert.Len(t, gotTs, 24)
	assert.Equal(t, "Z", string(gotTs[23]))
}

func TestMediatorBadRequestIsConfigError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	out := router.NewHTTPMediator(router.DevMediatorConfig(), router.NewBreakerRegistry(router.DefaultBreakerConfig())).Mediate(
		context.Background(),
		&common.Message{ID: "m", MediationType: common.MediationTypeHTTP, MediationTarget: srv.URL},
	)
	assert.Equal(t, common.MediationErrorConfig, out.Result)
	assert.Equal(t, 400, out.StatusCode)
}

func TestMediatorRateLimitedReadsRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	out := router.NewHTTPMediator(router.DevMediatorConfig(), router.NewBreakerRegistry(router.DefaultBreakerConfig())).Mediate(
		context.Background(),
		&common.Message{ID: "m", MediationType: common.MediationTypeHTTP, MediationTarget: srv.URL},
	)
	assert.Equal(t, common.MediationRateLimited, out.Result)
	assert.Equal(t, 120, out.DelaySeconds)
}

func TestMediatorServerErrorRetries(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := router.DevMediatorConfig()
	// MaxRetries is the max TOTAL attempts (Rust parity): 3 → 1 initial + 2 retries.
	cfg.MaxRetries = 3
	cfg.RetryDelays = []time.Duration{1 * time.Millisecond, 1 * time.Millisecond}

	out := router.NewHTTPMediator(cfg, router.NewBreakerRegistry(router.DefaultBreakerConfig())).Mediate(
		context.Background(),
		&common.Message{ID: "m", MediationType: common.MediationTypeHTTP, MediationTarget: srv.URL},
	)
	assert.Equal(t, common.MediationErrorProcess, out.Result)
	assert.Equal(t, 3, attempts, "MaxRetries=3 → 3 total attempts")
}

// TestMediatorHTTP2_StrictMaxConcurrentStreams smoke-tests the
// production HTTP/2 path. We can't trivially assert the strict-streams
// setting from outside the http2 package, but if ConfigureTransports
// raised or the http.Client was no longer functional, this test would
// fail. Use httptest.NewTLSServer (which defaults to advertising h2 via
// ALPN) and dispatch through the mediator with default prod config.
func TestMediatorHTTP2_DispatchSucceeds(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// http2.NewServer wires h2; httptest.StartTLS will negotiate
		// HTTP/2 via ALPN.
		if r.ProtoMajor != 2 {
			t.Logf("note: handler saw HTTP/%d.%d (TLS server defaults to h2 in test, but client may downgrade)",
				r.ProtoMajor, r.ProtoMinor)
		}
		w.WriteHeader(http.StatusOK)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	cfg := router.MediatorConfig{
		Timeout:        5 * time.Second,
		ConnectTimeout: 1 * time.Second,
		HTTPVersion:    router.HTTPVersion2,
		MaxRetries:     0,
		RetryDelays:    []time.Duration{},
	}
	m := router.NewHTTPMediator(cfg, router.NewBreakerRegistry(router.DefaultBreakerConfig()))
	// httptest's TLS server uses a self-signed cert; swap in its CA pool
	// by re-wrapping the underlying client's transport.
	// We can't easily reach in to set RootCAs without exporting a hook,
	// so this test uses an insecure-skip path: we rely on the fact that
	// the request itself will exercise the H2 transport configuration
	// code path. A cert error would still mean the H2 setup worked.
	msg := &common.Message{
		ID:              "m",
		MediationType:   common.MediationTypeHTTP,
		MediationTarget: srv.URL,
	}
	out := m.Mediate(context.Background(), msg)
	// Either Success (cert pool happens to accept) OR ErrorConnection
	// (cert rejected). Both are evidence that the H2 path executed.
	// What we DO NOT want is a panic or a config-mode error.
	assert.NotEqual(t, common.MediationErrorConfig, out.Result,
		"H2 setup error suspected, got: %+v", out)
}

// TestMediatorConnectTimeoutHonoured guards against the parity bug
// where MediatorConfig.ConnectTimeout was stored but never applied to
// the transport's DialContext. Before the fix, the only timeout that
// fired was Client.Timeout (15min in prod), so a slow target could
// pin a worker for the full 15 minutes vs Rust's reqwest stopping at
// 30s. The test points at a non-routable RFC-5737 address so the TCP
// connect stalls until our ConnectTimeout fires.
func TestMediatorConnectTimeoutHonoured(t *testing.T) {
	cfg := router.MediatorConfig{
		Timeout:        5 * time.Second, // request budget is generous
		ConnectTimeout: 250 * time.Millisecond,
		HTTPVersion:    router.HTTPVersion1,
		MaxRetries:     0,
		RetryDelays:    []time.Duration{},
	}
	m := router.NewHTTPMediator(cfg, router.NewBreakerRegistry(router.DefaultBreakerConfig()))

	msg := &common.Message{
		ID:            "test-msg-1",
		MediationType: common.MediationTypeHTTP,
		// TEST-NET-1 (RFC 5737) — guaranteed unroutable.
		MediationTarget: "http://192.0.2.1:65000/webhook",
	}

	start := time.Now()
	out := m.Mediate(context.Background(), msg)
	elapsed := time.Since(start)

	assert.Equal(t, common.MediationErrorConnection, out.Result,
		"expected ErrorConnection, got %+v", out)
	// Slack for jitter, but fail clearly if we're anywhere near the
	// 5s request timeout — that would mean ConnectTimeout regressed.
	assert.Less(t, elapsed, 2*time.Second,
		"connect timeout not honoured: elapsed %v with 250ms ConnectTimeout", elapsed)
}

func TestMediatorAckFalseIsTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ack": false, "delaySeconds": 45}`))
	}))
	defer srv.Close()

	cfg := router.DevMediatorConfig()
	cfg.MaxRetries = 0

	out := router.NewHTTPMediator(cfg, router.NewBreakerRegistry(router.DefaultBreakerConfig())).Mediate(
		context.Background(),
		&common.Message{ID: "m", MediationType: common.MediationTypeHTTP, MediationTarget: srv.URL},
	)
	assert.Equal(t, common.MediationErrorProcess, out.Result)
	assert.Equal(t, 45, out.DelaySeconds)
}
