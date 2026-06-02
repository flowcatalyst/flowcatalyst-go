package router

// Guardrail suite for the message router. Run with: go test -race ./internal/router/ -run Guardrail
//
// These encode the operational contract the Go router must preserve (the rules
// the Rust implementation defines) and the concurrency invariants Go cannot
// enforce at compile time. The breaker now lives in the mediator, so the
// breaker-accounting guardrails target that layer; the pool guardrails assert
// resolution always fires (incl. panic) and that concurrent submit is race-free
// (`-race` turns a dropped lock into a hard failure here).

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/internal/common"
	"github.com/flowcatalyst/flowcatalyst-go/internal/queue"
)

// grConsumer is a thread-safe fake queue.Consumer that records terminal actions.
type grConsumer struct {
	id            string
	acks          atomic.Int64
	nacks         atomic.Int64
	defers        atomic.Int64
	lastNackDelay atomic.Pointer[uint32]
}

func (c *grConsumer) Identifier() string { return c.id }
func (c *grConsumer) Poll(ctx context.Context, max uint32) ([]common.QueuedMessage, error) {
	return nil, nil
}
func (c *grConsumer) Ack(ctx context.Context, receipt string) error { c.acks.Add(1); return nil }
func (c *grConsumer) Nack(ctx context.Context, receipt string, delay *uint32) error {
	c.nacks.Add(1)
	c.lastNackDelay.Store(delay)
	return nil
}

func (c *grConsumer) Defer(ctx context.Context, receipt string, delay *uint32) error {
	c.defers.Add(1)
	return nil
}

func (c *grConsumer) ExtendVisibility(ctx context.Context, receipt string, sec uint32) error {
	return nil
}
func (c *grConsumer) Healthy() bool                                       { return true }
func (c *grConsumer) Stop()                                               {}
func (c *grConsumer) Metrics(ctx context.Context) (*queue.Metrics, error) { return nil, nil }
func (c *grConsumer) Counters() *queue.Metrics                            { return nil }
func (c *grConsumer) total() int64                                        { return c.acks.Load() + c.nacks.Load() + c.defers.Load() }

// grMediator is a Mediator that returns a fixed outcome (or panics). It does NOT
// consult a breaker — breaker behaviour is tested against the real HTTPMediator.
type grMediator struct {
	outcome  common.MediationOutcome
	panicMsg string
	called   atomic.Bool
}

func (m *grMediator) Mediate(ctx context.Context, msg *common.Message) common.MediationOutcome {
	m.called.Store(true)
	if m.panicMsg != "" {
		panic(m.panicMsg)
	}
	return m.outcome
}

func grPool(med Mediator, c queue.Consumer) *Pool {
	cfg := common.PoolConfig{Code: "TEST", Concurrency: 8}
	return NewPool(cfg, med, NewInFlightTracker(), func(string) queue.Consumer { return c })
}

func grMsg(id, endpoint string) common.QueuedMessage {
	return common.QueuedMessage{
		Message:         common.Message{ID: id, MediationTarget: endpoint, DispatchMode: common.DispatchImmediate},
		ReceiptHandle:   "rh-" + id,
		BrokerMessageID: "bk-" + id,
		QueueIdentifier: "q1",
	}
}

func grWaitFor(t *testing.T, cond func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("condition not met within %s", timeout)
	}
}

// --- Pool: resolution follows the in-pipeline-retry contract ---
//
// Terminal outcomes (2xx success, 4xx) ACK once and clear the in-flight entry.
// Retryable outcomes (5xx/timeout, connection, 429, circuit-open, panic) DO
// NOT touch the broker — the message is retried in-pipeline — so processOne
// returns processRetry with a backoff and the consumer sees zero terminal
// actions. (SQS Nack is a no-op anyway; releasing to the broker is no longer
// part of the failure path.)

func TestGuardrail_ResolutionOnSuccess(t *testing.T) {
	c := &grConsumer{id: "q1"}
	p := grPool(&grMediator{outcome: common.Success()}, c)
	res, _ := p.processOne(context.Background(), grMsg("evt_ok", "http://t/ok"))
	if res != processDone || c.acks.Load() != 1 || c.nacks.Load() != 0 || c.defers.Load() != 0 {
		t.Fatalf("success must ACK exactly once and report processDone; got res=%d acks=%d nacks=%d defers=%d",
			res, c.acks.Load(), c.nacks.Load(), c.defers.Load())
	}
}

func TestGuardrail_RetryOnProcessError(t *testing.T) {
	c := &grConsumer{id: "q1"}
	p := grPool(&grMediator{outcome: common.ErrorProcess(30, "5xx")}, c)
	res, delay := p.processOne(context.Background(), grMsg("evt_5xx", "http://t/5xx"))
	if res != processRetry {
		t.Fatalf("process error must retry in-pipeline; got res=%d", res)
	}
	if c.total() != 0 {
		t.Fatalf("process error must NOT touch the broker (in-pipeline retry); got %d terminal actions", c.total())
	}
	if delay < 30*time.Second {
		t.Fatalf("process-error backoff must honour the 30s retry-after floor; got %v", delay)
	}
}

func TestGuardrail_RetryOnCircuitOpen(t *testing.T) {
	// The mediator owns the breaker and returns MediationCircuitOpen when open;
	// the pool must retry in-pipeline (no broker action) after the reset delay.
	c := &grConsumer{id: "q1"}
	p := grPool(&grMediator{outcome: common.CircuitOpen(5)}, c)
	res, delay := p.processOne(context.Background(), grMsg("evt_cb", "http://t/cb"))
	if res != processRetry || c.total() != 0 {
		t.Fatalf("circuit-open must retry in-pipeline with no broker action; got res=%d terminal=%d",
			res, c.total())
	}
	if delay < 5*time.Second {
		t.Fatalf("circuit-open backoff must honour the 5s reset floor; got %v", delay)
	}
}

func TestGuardrail_RetryOnPanic(t *testing.T) {
	// A panic mid-mediation must be recovered, NOT crash the process, and be
	// retried in-pipeline (the in-flight entry is kept) — processOne recovers
	// internally and returns processRetry with no broker action.
	c := &grConsumer{id: "q1"}
	p := grPool(&grMediator{panicMsg: "boom"}, c)
	res, _ := p.processOne(context.Background(), grMsg("evt_panic", "http://t/panic"))
	if res != processRetry {
		t.Fatalf("a panic mid-mediation must be recovered and retried in-pipeline; got res=%d", res)
	}
	if c.total() != 0 {
		t.Fatalf("a recovered panic must not produce a terminal broker action; got %d", c.total())
	}
}

// --- Pool (marquee): the data-race surface under contention ---

// Hammer submit() from many goroutines across both dispatch paths (IMMEDIATE
// goroutine-per-message + ordered per-group drainers) and overlapping groups.
// Exercises groupQs (p.mu), the swappable semaphore and the atomic counters
// concurrently. Under -race, any future edit that drops a lock fails here (or
// panics on concurrent map write). All messages succeed here, so the invariant
// is: every submitted message is ACKed exactly once (no loss, no double-ack).
func TestGuardrail_ConcurrentSubmitNoRaceAndResolvesEach(t *testing.T) {
	const n = 600
	c := &grConsumer{id: "q1"}
	p := grPool(&grMediator{outcome: common.Success()}, c)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			m := grMsg(fmt.Sprintf("evt_%d", i), "http://t/x")
			m.BatchID = fmt.Sprintf("b_%d", i%8)
			if i%2 == 0 {
				g := fmt.Sprintf("grp_%d", i%5)
				m.Message.MessageGroupID = &g
				m.Message.DispatchMode = common.DispatchMode("BLOCK_ON_ERROR") // ordered path
			}
			p.submit(ctx, m)
		}(i)
	}
	wg.Wait()

	grWaitFor(t, func() bool { return c.total() == n }, 10*time.Second)
	if c.total() != int64(n) {
		t.Fatalf("every message must resolve exactly once; got %d of %d", c.total(), n)
	}
}

// --- Mediator: breaker accounting now lives here ---

// A 4xx means the endpoint is reachable, so it must record a circuit-breaker
// SUCCESS (it must not trip the breaker, and must let an open breaker recover).
// Centralising the recording in the mediator removes the bug class where a pool
// switch arm forgot to record.
func TestGuardrail_BreakerRecordsSuccessOn4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	br := NewBreakerRegistry(DefaultBreakerConfig())
	med := NewHTTPMediator(DevMediatorConfig(), br)
	defer med.Close()

	out := med.Mediate(context.Background(),
		&common.Message{ID: "evt_4xx", MediationType: common.MediationTypeHTTP, MediationTarget: srv.URL})

	if out.Result != common.MediationErrorConfig {
		t.Fatalf("404 must classify as ErrorConfig; got %v", out.Result)
	}
	if s := br.Get(srv.URL).Stats().Successes; s != 1 {
		t.Fatalf("ErrorConfig (4xx) must record a circuit-breaker success (reachable); got successes=%d", s)
	}
}

// An open breaker short-circuits in the mediator: no HTTP is attempted and a
// MediationCircuitOpen outcome is returned for the pool to DEFER.
func TestGuardrail_MediatorShortCircuitsWhenOpen(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	br := NewBreakerRegistry(DefaultBreakerConfig())
	for i := 0; i < 10; i++ { // trip: 10 failures @ 100% >= 0.5 threshold, MinCalls 10
		br.Get(srv.URL).RecordFailure()
	}
	med := NewHTTPMediator(DevMediatorConfig(), br)
	defer med.Close()

	out := med.Mediate(context.Background(),
		&common.Message{ID: "evt_open", MediationType: common.MediationTypeHTTP, MediationTarget: srv.URL})

	if out.Result != common.MediationCircuitOpen {
		t.Fatalf("open breaker must yield MediationCircuitOpen; got %v", out.Result)
	}
	if hits.Load() != 0 {
		t.Fatalf("open breaker must NOT hit the endpoint; got %d hits", hits.Load())
	}
}

// Config-class responses (400/401/403/404 → Error, 501 → Critical) must surface
// as Configuration warnings on the WarningService when one is wired, so they
// appear on /warnings and a 501 degrades health.
func TestGuardrail_ConfigErrorsSurfaceAsWarnings(t *testing.T) {
	cases := []struct {
		status int
		sev    WarningSeverity
	}{
		{http.StatusNotFound, WarningError},
		{http.StatusNotImplemented, WarningCritical},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
		}))
		ws := NewWarningService(DefaultWarningServiceConfig())
		med := NewHTTPMediator(DevMediatorConfig(), NewBreakerRegistry(DefaultBreakerConfig()))
		med.SetWarnings(ws)

		med.Mediate(context.Background(),
			&common.Message{ID: "m", MediationType: common.MediationTypeHTTP, MediationTarget: srv.URL})

		found := false
		for _, w := range ws.BySeverity(tc.sev) {
			if w.Category == WarningCategoryConfiguration {
				found = true
			}
		}
		if !found {
			t.Fatalf("status %d must record a %s Configuration warning", tc.status, tc.sev)
		}
		med.Close()
		srv.Close()
	}
}
