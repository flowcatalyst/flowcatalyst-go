// Package router is the Go port of fc-router. It consumes messages
// from per-pool queue consumers, applies rate limits and circuit
// breakers, and delivers via HTTP webhook (with HMAC-SHA256 signing).
package router

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/net/http2"

	"github.com/flowcatalyst/flowcatalyst-go/internal/common"
)

// SignatureHeader matches the Rust SIGNATURE_HEADER constant.
const SignatureHeader = "X-FLOWCATALYST-SIGNATURE"

// TimestampHeader matches the Rust TIMESTAMP_HEADER constant.
const TimestampHeader = "X-FLOWCATALYST-TIMESTAMP"

// Mediator delivers a message to its target. The HTTP implementation
// signs the payload with HMAC-SHA256 when a signing secret is supplied.
type Mediator interface {
	Mediate(ctx context.Context, m *common.Message) common.MediationOutcome
}

// HTTPVersion controls the negotiated HTTP version.
type HTTPVersion int

const (
	HTTPVersion1 HTTPVersion = iota // HTTP/1.1
	HTTPVersion2                    // HTTP/2 via ALPN
)

// MediatorConfig configures the HTTP mediator.
type MediatorConfig struct {
	Timeout             time.Duration
	ConnectTimeout      time.Duration
	TLSHandshakeTimeout time.Duration
	HTTPVersion         HTTPVersion
	MaxRetries          int
	RetryDelays         []time.Duration
	// HostPoolSizing tunes the per-host HTTP/2 connection pool (slot
	// grow/shrink). Mirrors crates/fc-router/src/http_pool.rs sizing.
	// Zero-value Sizing means "use the default for the negotiated HTTP
	// version" — DefaultHostPoolSizing for HTTP/2, HTTP1HostPoolSizing
	// for HTTP/1.1.
	HostPoolSizing HostPoolSizing
}

// DefaultMediatorConfig matches the Rust production defaults (15min timeout, HTTP/2).
func DefaultMediatorConfig() MediatorConfig {
	return MediatorConfig{
		Timeout:             15 * time.Minute,
		ConnectTimeout:      30 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		HTTPVersion:         HTTPVersion2,
		MaxRetries:          3,
		RetryDelays:         []time.Duration{1 * time.Second, 2 * time.Second, 3 * time.Second},
		HostPoolSizing:      DefaultHostPoolSizing(),
	}
}

// DevMediatorConfig is the developer-friendly variant (30s timeout, HTTP/1.1).
func DevMediatorConfig() MediatorConfig {
	c := DefaultMediatorConfig()
	c.Timeout = 30 * time.Second
	c.ConnectTimeout = 10 * time.Second
	c.HTTPVersion = HTTPVersion1
	c.HostPoolSizing = HTTP1HostPoolSizing()
	return c
}

// HTTPMediator delivers via net/http with a per-host HTTP/2 connection
// pool (HostPoolRegistry). Each origin gets one or more *http.Client
// slots, each backed by its own *http.Transport so the slots' h2
// connection pools are independent. Mirrors crates/fc-router/src/http_pool.rs.
type HTTPMediator struct {
	pools    *HostPoolRegistry
	cfg      MediatorConfig
	breakers *BreakerRegistry
	warnings *WarningService // optional; set via SetWarnings. nil → no-op.
}

// NewHTTPMediator wires an HTTP mediator with the supplied config.
// Each per-slot *http.Client has its own Transport tuned to match
// crates/fc-router/src/mediator.rs (reqwest::Client builder):
//
//   - MaxIdleConnsPerHost = 10           ↔ pool_max_idle_per_host(10)
//   - IdleConnTimeout = 90s              ↔ reqwest default
//   - DialContext.Timeout = ConnectTimeout ↔ connect_timeout(...)
//   - Client.Timeout = Timeout           ↔ timeout(...)
//
// HTTP/2 specifics:
//   - http2.Transport.StrictMaxConcurrentStreams=true: honour ALB's
//     advertised H2 stream limit instead of oversubscribing inside a
//     single connection.
//   - Per-host pool grows additional slots (each a separate h2
//     connection) when in-flight on every slot exceeds the high
//     watermark, raising the effective concurrent-stream cap.
//
// `ResponseHeaderTimeout` is intentionally NOT set: it would shadow
// Client.Timeout for the response-header phase only and obscure which
// timeout is actually enforced. Single source of truth: Client.Timeout.
func NewHTTPMediator(cfg MediatorConfig, breakers *BreakerRegistry) *HTTPMediator {
	sizing := cfg.HostPoolSizing
	if sizing.MaxSlotsPerHost == 0 {
		if cfg.HTTPVersion == HTTPVersion1 {
			sizing = HTTP1HostPoolSizing()
		} else {
			sizing = DefaultHostPoolSizing()
		}
		cfg.HostPoolSizing = sizing
	}
	builder := newClientBuilder(cfg)
	pools := NewHostPoolRegistry(sizing, builder)
	pools.StartSweep()
	return &HTTPMediator{pools: pools, cfg: cfg, breakers: breakers}
}

// Close stops the host-pool sweep goroutine. Safe to call multiple
// times. Calls to Mediate after Close are still permitted but the pool
// will no longer shrink in the background.
func (m *HTTPMediator) Close() {
	if m.pools != nil {
		m.pools.Close()
	}
}

// HostPools is exposed for tests/metrics. Production code should not
// poke at the registry directly.
func (m *HTTPMediator) HostPools() *HostPoolRegistry { return m.pools }

// SetWarnings wires a WarningService so configuration-class responses surface on
// /warnings and degrade health. Opt-in: when unset, warnConfig only logs. Set
// once at startup, before serving.
func (m *HTTPMediator) SetWarnings(ws *WarningService) { m.warnings = ws }

// warnConfig logs a configuration-class warning and, when a WarningService is
// wired, records it so it shows on /warnings and (for Critical, e.g. 501)
// degrades health. Mirrors the Rust mediator's config-error warnings.
func (m *HTTPMediator) warnConfig(severity WarningSeverity, message string, msg *common.Message) {
	slog.Warn("mediation config error", "message_id", msg.ID, "target", msg.MediationTarget,
		"detail", message, "severity", severity)
	if m.warnings != nil {
		m.warnings.Add(WarningCategoryConfiguration, severity, message, "HttpMediator")
	}
}

// newClientBuilder returns a ClientBuilder that mints a fresh
// *http.Client with its own *http.Transport per call. Each Transport
// owns its own connection pool, so two slots backed by separate
// Transports give us two independent h2 connections to the origin.
func newClientBuilder(cfg MediatorConfig) ClientBuilder {
	return func() *http.Client {
		dialer := &net.Dialer{
			Timeout:   cfg.ConnectTimeout,
			KeepAlive: 30 * time.Second,
		}
		transport := &http.Transport{
			DialContext:         dialer.DialContext,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: cfg.TLSHandshakeTimeout,
		}
		if cfg.HTTPVersion == HTTPVersion1 {
			transport.ForceAttemptHTTP2 = false
			transport.TLSNextProto = map[string]func(authority string, c *tls.Conn) http.RoundTripper{}
		} else {
			transport.ForceAttemptHTTP2 = true
			if h2, err := http2.ConfigureTransports(transport); err == nil && h2 != nil {
				h2.StrictMaxConcurrentStreams = true
			}
		}
		return &http.Client{Transport: transport, Timeout: cfg.Timeout}
	}
}

// mediationPayload is the JSON body sent to the target. Byte-identical
// to the Rust `MediationPayload { message_id: &str }` struct.
type mediationPayload struct {
	MessageID string `json:"messageId"`
}

// mediationResponse is what we expect back from the target.
type mediationResponse struct {
	Ack          *bool   `json:"ack,omitempty"`
	DelaySeconds *uint32 `json:"delaySeconds,omitempty"`
}

// signWebhook computes the HMAC-SHA256 over `timestamp + payload` and
// returns (signatureHex, timestampStr). MUST byte-match the Rust
// fc-router sign_webhook for the test vector in
// tests/golden/webhook/mediation-payload.json.
func signWebhook(payload []byte, signingSecret string) (sigHex, ts string) {
	// Millisecond-precision ISO8601 UTC, exactly 3 fractional digits.
	// Matches Rust format "%Y-%m-%dT%H:%M:%S%.3fZ".
	ts = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	mac := hmac.New(sha256.New, []byte(signingSecret))
	mac.Write([]byte(ts))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil)), ts
}

// Mediate consults the per-endpoint circuit breaker, delivers with retry, and
// records the breaker outcome in ONE place. Centralising the success/failure
// recording here (rather than per-outcome in the pool) removes the class of bug
// where a single switch arm forgets to record. An open breaker short-circuits:
// no HTTP is attempted and a circuit-open outcome is returned for the pool to DEFER.
func (m *HTTPMediator) Mediate(ctx context.Context, msg *common.Message) common.MediationOutcome {
	cb := m.breakers.Get(msg.MediationTarget)
	if err := cb.Allow(); err != nil {
		return common.CircuitOpen(int(cb.ResetTimeout().Seconds()))
	}
	outcome := m.deliverWithRetry(ctx, msg)
	switch outcome.Result {
	case common.MediationSuccess, common.MediationErrorConfig:
		// 4xx is reachable → record a SUCCESS: a config error must not trip the
		// breaker, and must let an open/half-open breaker recover.
		cb.RecordSuccess()
	case common.MediationErrorProcess, common.MediationErrorConnection:
		cb.RecordFailure()
	case common.MediationRateLimited, common.MediationCircuitOpen:
		// 429: destination healthy, just throttling — neither success nor failure.
		// CircuitOpen is returned before delivery, so it never reaches here; listed
		// for switch exhaustiveness.
	}
	return outcome
}

// deliverWithRetry delivers the message with retry. Returns the outcome.
func (m *HTTPMediator) deliverWithRetry(ctx context.Context, msg *common.Message) common.MediationOutcome {
	var last common.MediationOutcome
	// Mirrors crates/fc-router/src/mediator/retry.rs exactly: MaxRetries is
	// the max TOTAL attempts (default 3), and a delay is taken only between
	// attempts (after attempt 1 and 2 for the default), never after the last.
	attempts := 0
	for {
		last = m.mediateOnce(ctx, msg)

		// Don't retry on success, config errors, or rate-limit responses.
		// For 429 the queue applies Retry-After delay rather than busy-waiting here.
		switch last.Result {
		case common.MediationSuccess, common.MediationErrorConfig, common.MediationRateLimited:
			return last
		default:
			// ErrorProcess / ErrorConnection are retryable; fall through to backoff.
			// (CircuitOpen is returned before the retry loop, so never reaches here.)
		}
		attempts++
		if attempts >= m.cfg.MaxRetries {
			return last
		}

		// Backoff according to configured retry_delays (index = attempts-1).
		delay := 3 * time.Second
		if attempts-1 < len(m.cfg.RetryDelays) {
			delay = m.cfg.RetryDelays[attempts-1]
		}
		select {
		case <-ctx.Done():
			return last
		case <-time.After(delay):
		}
	}
}

func (m *HTTPMediator) mediateOnce(ctx context.Context, msg *common.Message) common.MediationOutcome {
	if msg.MediationType != common.MediationTypeHTTP {
		return common.ErrorConfig(0, fmt.Sprintf("Unsupported mediation type: %s", msg.MediationType))
	}

	payload, err := json.Marshal(mediationPayload{MessageID: msg.ID})
	if err != nil {
		return common.ErrorConfig(0, fmt.Sprintf("payload marshal: %v", err))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, msg.MediationTarget, bytes.NewReader(payload))
	if err != nil {
		return common.ErrorConnection(fmt.Sprintf("build request: %v", err))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	if msg.SigningSecret != nil {
		sig, ts := signWebhook(payload, *msg.SigningSecret)
		req.Header.Set(SignatureHeader, sig)
		req.Header.Set(TimestampHeader, ts)
	}
	if msg.AuthToken != nil {
		req.Header.Set("Authorization", "Bearer "+*msg.AuthToken)
	}

	host, err := HostKeyFromURL(msg.MediationTarget)
	if err != nil {
		return common.ErrorConfig(0, fmt.Sprintf("invalid mediation target URL: %v", err))
	}
	guard := m.pools.Acquire(host)
	defer guard.Release()

	resp, err := guard.Client().Do(req)
	if err != nil {
		// Map common error types.
		var netErr interface{ Timeout() bool }
		if errors.As(err, &netErr) && netErr.Timeout() {
			return common.ErrorConnection("Request timeout")
		}
		return common.ErrorConnection(fmt.Sprintf("Request failed: %v", err))
	}
	defer resp.Body.Close()

	status := resp.StatusCode
	switch {
	case status >= 200 && status < 300:
		// Parse {"ack": false, "delaySeconds": N}; if ack=false treat as transient.
		body, err := io.ReadAll(resp.Body)
		if err == nil && len(body) > 0 {
			var r mediationResponse
			if err := json.Unmarshal(body, &r); err == nil && r.Ack != nil && !*r.Ack {
				delay := uint32(30)
				if r.DelaySeconds != nil {
					delay = *r.DelaySeconds
				}
				out := common.ErrorProcess(int(delay), "Target returned ack=false")
				out.StatusCode = status
				return out
			}
		}
		return common.Success()

	case status == 400:
		m.warnConfig(WarningError, "HTTP 400: Bad request", msg)
		return common.ErrorConfig(status, "HTTP 400: Bad request")

	case status == 401 || status == 403:
		m.warnConfig(WarningError, fmt.Sprintf("HTTP %d: Auth error", status), msg)
		return common.ErrorConfig(status, fmt.Sprintf("HTTP %d: Auth error", status))

	case status == 404:
		m.warnConfig(WarningError, "HTTP 404: Not found", msg)
		return common.ErrorConfig(status, "HTTP 404: Not found")

	case status == 429:
		retryAfter := 30
		if v := resp.Header.Get("Retry-After"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				retryAfter = n
			}
		}
		slog.Warn("rate limited by target", "message_id", msg.ID, "retry_after", retryAfter)
		return common.RateLimited(retryAfter)

	case status == 501:
		m.warnConfig(WarningCritical, "HTTP 501: Not implemented", msg)
		return common.ErrorConfig(status, "HTTP 501: Not implemented")

	case status >= 400 && status < 500:
		slog.Warn("client error from target", "message_id", msg.ID, "status", status)
		return common.ErrorConfig(status, fmt.Sprintf("HTTP %d: Client error", status))

	case status >= 500:
		slog.Warn("server error from target", "message_id", msg.ID, "status", status)
		out := common.ErrorProcess(30, fmt.Sprintf("HTTP %d: Server error", status))
		out.StatusCode = status
		return out

	default:
		slog.Warn("unexpected status from target", "message_id", msg.ID, "status", status)
		return common.ErrorProcess(30, fmt.Sprintf("HTTP %d: Unexpected status", status))
	}
}
