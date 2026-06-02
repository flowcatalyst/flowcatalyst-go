package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2/humatest"
	"github.com/go-chi/chi/v5"

	"github.com/flowcatalyst/flowcatalyst-go/internal/common"
	"github.com/flowcatalyst/flowcatalyst-go/internal/queue"
	"github.com/flowcatalyst/flowcatalyst-go/internal/router"
	routerapi "github.com/flowcatalyst/flowcatalyst-go/internal/router/api"
)

// ── Test doubles for optional providers ──────────────────────────────────

type stubPoolStatsProvider struct{ stats []router.PoolStats }

func (s stubPoolStatsProvider) PoolStats() []router.PoolStats { return s.stats }

type stubBreakerSnapshotProvider struct {
	snap     map[string]router.BreakerStats
	resetN   int
	resetAll int
	resetOK  bool
}

func (s *stubBreakerSnapshotProvider) Snapshot() map[string]router.BreakerStats { return s.snap }
func (s *stubBreakerSnapshotProvider) Reset(_ string) bool {
	s.resetN++
	return s.resetOK
}

func (s *stubBreakerSnapshotProvider) ResetAll() int {
	s.resetAll = len(s.snap)
	return s.resetAll
}

type stubInFlightProvider struct{ entries []common.InFlightMessage }

func (s stubInFlightProvider) Snapshot() []common.InFlightMessage { return s.entries }

type stubBrokerStatsProvider struct {
	metrics []queue.Metrics
	refresh int
}

func (s *stubBrokerStatsProvider) GetWindowed(_ time.Duration) []queue.Metrics { return s.metrics }
func (s *stubBrokerStatsProvider) Refresh()                                    { s.refresh++ }
func (s *stubBrokerStatsProvider) AgeSeconds() int64                           { return 7 }

type stubPoolUpdater struct {
	lastCode    string
	lastConc    uint32
	lastRate    *uint32
	lastSetRate bool
	ok          bool
}

func (s *stubPoolUpdater) UpdatePool(code string, concurrency uint32, rate *uint32, setRate bool) bool {
	s.lastCode = code
	s.lastConc = concurrency
	s.lastRate = rate
	s.lastSetRate = setRate
	return s.ok
}

type stubPublisher struct {
	identifier string
	lastMsg    common.Message
	brokerID   string
	publishErr error
}

func (s *stubPublisher) Identifier() string { return s.identifier }
func (s *stubPublisher) Publish(_ context.Context, m common.Message) (string, error) {
	s.lastMsg = m
	if s.publishErr != nil {
		return "", s.publishErr
	}
	return s.brokerID, nil
}

func (s *stubPublisher) PublishBatch(_ context.Context, msgs []common.Message) ([]string, error) {
	if s.publishErr != nil {
		return nil, s.publishErr
	}
	ids := make([]string, len(msgs))
	for i := range msgs {
		ids[i] = s.brokerID
	}
	return ids, nil
}

type stubPublisherProvider struct{ pub *stubPublisher }

func (s stubPublisherProvider) Publisher(_ context.Context, _ string) (queue.Publisher, error) {
	return s.pub, nil
}

type stubLeader struct {
	leader     bool
	standby    bool
	instanceID string
}

func (l stubLeader) IsLeader() bool       { return l.leader }
func (l stubLeader) StandbyEnabled() bool { return l.standby }
func (l stubLeader) InstanceID() string   { return l.instanceID }

// ── Setup ────────────────────────────────────────────────────────────────

func setupAPI(t *testing.T) (humatest.TestAPI, *chi.Mux, *stubBreakerSnapshotProvider, *stubBrokerStatsProvider, *stubPoolUpdater, *stubPublisher) {
	t.Helper()
	ws := router.NewWarningService(router.WarningServiceConfig{})
	hs := router.NewHealthService(router.DefaultHealthServiceConfig(), ws)

	rl := uint32(60)
	pools := stubPoolStatsProvider{stats: []router.PoolStats{{
		PoolCode:           "demo",
		Concurrency:        10,
		ActiveWorkers:      3,
		QueueSize:          5,
		QueueCapacity:      200,
		MessageGroupCount:  2,
		RateLimitPerMinute: &rl,
		Metrics: &common.EnhancedPoolMetrics{
			TotalSuccess: 1000, TotalFailure: 5, TotalRateLimited: 2,
			SuccessRate: 0.995,
			ProcessingTime: common.ProcessingTimeMetrics{
				AvgMs: 25.0, MinMs: 10, MaxMs: 100,
				P50Ms: 20, P95Ms: 80, P99Ms: 95, SampleCount: 1005,
			},
			Last5Min:  common.WindowedMetrics{SuccessCount: 50, FailureCount: 1, SuccessRate: 50.0 / 51.0, ProcessingTime: common.ProcessingTimeMetrics{AvgMs: 22.0}},
			Last30Min: common.WindowedMetrics{SuccessCount: 300, FailureCount: 2, SuccessRate: 300.0 / 302.0, ProcessingTime: common.ProcessingTimeMetrics{AvgMs: 24.0}},
		},
	}}}
	breakers := &stubBreakerSnapshotProvider{
		snap: map[string]router.BreakerStats{
			"target-a": {State: router.CircuitClosed, Successes: 100, Failures: 1, RecentFailures: 0},
		},
		resetOK: true,
	}
	inflight := stubInFlightProvider{entries: []common.InFlightMessage{{
		MessageID: "msg-1", BrokerMessageID: "br-1", PoolCode: "demo",
		QueueIdentifier: "q-demo", StartedAt: time.Now().Add(-1500 * time.Millisecond),
	}}}
	bstats := &stubBrokerStatsProvider{metrics: []queue.Metrics{{
		QueueIdentifier: "q-demo", PendingMessages: 10, InFlightMessages: 2,
		TotalPolled: 500, TotalAcked: 490, TotalNacked: 5, TotalDeferred: 5,
	}}}
	updater := &stubPoolUpdater{ok: true}
	pub := &stubPublisher{identifier: "q-demo://test", brokerID: "br-pub-1"}

	state := &routerapi.State{
		Warnings:    ws,
		Health:      hs,
		PoolStats:   pools,
		OpenCount:   nil,
		Breakers:    breakers,
		InFlight:    inflight,
		BrokerStats: bstats,
		PoolUpdater: updater,
		Publisher:   stubPublisherProvider{pub: pub},
		Leader:      stubLeader{leader: true, standby: false, instanceID: "test"},
		Mocks:       routerapi.NewMockState(),
	}

	// humatest.New attaches an httptest server and returns a TestAPI
	// wrapping a humachi-backed huma.API. Pull the underlying chi mux
	// out so we can also mount the dashboard HTML on it.
	_, api := humatest.New(t)
	routerapi.Register(api, state)

	// Mount HTML dashboard on a separate chi router for tests that need it
	// (TestAPI alone can't serve the embedded HTML — huma operations are JSON).
	htmlRouter := chi.NewRouter()
	routerapi.MountDashboard(htmlRouter)

	return api, htmlRouter, breakers, bstats, updater, pub
}

func decodeBody(t *testing.T, body []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("decode: %v body=%q", err, string(body))
	}
}

// ── Health probes ────────────────────────────────────────────────────────

func TestHealthLive(t *testing.T) {
	api, _, _, _, _, _ := setupAPI(t)
	resp := api.Get("/health/live")
	if resp.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.Code)
	}
	var p routerapi.ProbeResponse
	decodeBody(t, resp.Body.Bytes(), &p)
	if p.Status != "LIVE" {
		t.Errorf("status=%q want LIVE", p.Status)
	}
}

func TestHealthReady_HealthyReturns200(t *testing.T) {
	api, _, _, _, _, _ := setupAPI(t)
	resp := api.Get("/health/ready")
	if resp.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.Code)
	}
}

func TestHealth_SimpleResponse(t *testing.T) {
	api, _, _, _, _, _ := setupAPI(t)
	resp := api.Get("/health")
	if resp.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.Code)
	}
	var h routerapi.SimpleHealthResponse
	decodeBody(t, resp.Body.Bytes(), &h)
	if h.Status == "" {
		t.Errorf("missing status")
	}
}

// ── Monitoring overview ──────────────────────────────────────────────────

func TestMonitoring(t *testing.T) {
	api, _, _, _, _, _ := setupAPI(t)
	resp := api.Get("/monitoring")
	if resp.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", resp.Code, resp.Body.String())
	}
	var m routerapi.MonitoringResponse
	decodeBody(t, resp.Body.Bytes(), &m)
	if len(m.PoolStats) != 1 || m.PoolStats[0].PoolCode != "demo" {
		t.Errorf("PoolStats=%+v", m.PoolStats)
	}
}

func TestMonitoringHealth(t *testing.T) {
	api, _, _, _, _, _ := setupAPI(t)
	resp := api.Get("/monitoring/health")
	if resp.Code != http.StatusOK {
		t.Fatalf("status %d", resp.Code)
	}
	var d routerapi.DashboardHealthResponse
	decodeBody(t, resp.Body.Bytes(), &d)
	if d.Details == nil {
		t.Fatalf("missing details")
	}
}

// ── Dashboard JSON endpoints ─────────────────────────────────────────────

func TestDashboardPoolStats_AllTime(t *testing.T) {
	api, _, _, _, _, _ := setupAPI(t)
	resp := api.Get("/monitoring/pool-stats")
	if resp.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", resp.Code, resp.Body.String())
	}
	var body map[string]routerapi.DashboardPoolStats
	decodeBody(t, resp.Body.Bytes(), &body)
	demo, ok := body["demo"]
	if !ok {
		t.Fatalf("missing demo key: %v", body)
	}
	if demo.TotalSucceeded != 1000 {
		t.Errorf("TotalSucceeded=%d want 1000", demo.TotalSucceeded)
	}
	if demo.AvailablePermits != 7 {
		t.Errorf("AvailablePermits=%d want 7", demo.AvailablePermits)
	}
}

func TestDashboardPoolStats_5Min(t *testing.T) {
	api, _, _, _, _, _ := setupAPI(t)
	resp := api.Get("/monitoring/pool-stats?time_window=5min")
	var body map[string]routerapi.DashboardPoolStats
	decodeBody(t, resp.Body.Bytes(), &body)
	if body["demo"].TotalSucceeded != 50 {
		t.Errorf("5min TotalSucceeded=%d want 50", body["demo"].TotalSucceeded)
	}
}

func TestDashboardQueueStats(t *testing.T) {
	api, _, _, bstats, _, _ := setupAPI(t)
	resp := api.Get("/monitoring/queue-stats?refresh=true")
	if resp.Code != http.StatusOK {
		t.Fatalf("status %d", resp.Code)
	}
	if bstats.refresh != 1 {
		t.Errorf("refresh count=%d want 1", bstats.refresh)
	}
	var body map[string]routerapi.DashboardQueueStats
	decodeBody(t, resp.Body.Bytes(), &body)
	q, ok := body["q-demo"]
	if !ok {
		t.Fatalf("missing q-demo key: %v", body)
	}
	if q.TotalConsumed != 490 {
		t.Errorf("TotalConsumed=%d want 490", q.TotalConsumed)
	}
}

func TestDashboardCircuitBreakers(t *testing.T) {
	api, _, _, _, _, _ := setupAPI(t)
	resp := api.Get("/monitoring/circuit-breakers")
	if resp.Code != http.StatusOK {
		t.Fatalf("status %d", resp.Code)
	}
	var body map[string]routerapi.DashboardCircuitBreaker
	decodeBody(t, resp.Body.Bytes(), &body)
	cb, ok := body["target-a"]
	if !ok {
		t.Fatalf("missing target-a: %v", body)
	}
	if cb.State != "CLOSED" {
		t.Errorf("state=%q want CLOSED", cb.State)
	}
}

func TestCircuitBreakerState(t *testing.T) {
	api, _, _, _, _, _ := setupAPI(t)
	resp := api.Get("/monitoring/circuit-breakers/target-a/state")
	if resp.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", resp.Code, resp.Body.String())
	}
	var body routerapi.CircuitBreakerStateResponse
	decodeBody(t, resp.Body.Bytes(), &body)
	if body.Name != "target-a" || body.State != "CLOSED" {
		t.Errorf("body=%+v", body)
	}
}

func TestDashboardInFlight(t *testing.T) {
	api, _, _, _, _, _ := setupAPI(t)
	resp := api.Get("/monitoring/in-flight-messages?limit=10")
	if resp.Code != http.StatusOK {
		t.Fatalf("status %d", resp.Code)
	}
	var arr []routerapi.InFlightMessageInfo
	decodeBody(t, resp.Body.Bytes(), &arr)
	if len(arr) != 1 || arr[0].MessageID != "msg-1" {
		t.Fatalf("arr=%+v", arr)
	}
}

func TestDashboardInFlight_PoolFilter(t *testing.T) {
	api, _, _, _, _, _ := setupAPI(t)
	resp := api.Get("/monitoring/in-flight-messages?poolCode=other")
	var arr []routerapi.InFlightMessageInfo
	decodeBody(t, resp.Body.Bytes(), &arr)
	if len(arr) != 0 {
		t.Errorf("expected 0 with filter mismatch, got %d", len(arr))
	}
}

func TestInFlightCheck(t *testing.T) {
	api, _, _, _, _, _ := setupAPI(t)
	resp := api.Get("/monitoring/in-flight-messages/check?messageId=msg-1")
	if resp.Code != http.StatusOK {
		t.Fatalf("status %d", resp.Code)
	}
	var body routerapi.InFlightCheckResponse
	decodeBody(t, resp.Body.Bytes(), &body)
	if !body.InPipeline {
		t.Errorf("expected InPipeline=true, got %+v", body)
	}
}

func TestInFlightCheckBatch(t *testing.T) {
	api, _, _, _, _, _ := setupAPI(t)
	resp := api.Post("/monitoring/in-flight-messages/check-batch",
		map[string]any{"messageIds": []string{"msg-1", "missing"}})
	if resp.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", resp.Code, resp.Body.String())
	}
	var out map[string]bool
	decodeBody(t, resp.Body.Bytes(), &out)
	if !out["msg-1"] || out["missing"] {
		t.Errorf("out=%+v", out)
	}
}

// ── Mutations ────────────────────────────────────────────────────────────

func TestPoolUpdate(t *testing.T) {
	api, _, _, _, updater, _ := setupAPI(t)
	resp := api.Put("/monitoring/pools/demo",
		map[string]any{"concurrency": 20, "rate_limit_per_minute": 120})
	if resp.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", resp.Code, resp.Body.String())
	}
	if updater.lastCode != "demo" || updater.lastConc != 20 {
		t.Errorf("updater=%+v", updater)
	}
	if updater.lastRate == nil || *updater.lastRate != 120 {
		t.Errorf("rate=%v want 120", updater.lastRate)
	}
}

func TestPoolUpdate_NotFound(t *testing.T) {
	api, _, _, _, updater, _ := setupAPI(t)
	updater.ok = false
	resp := api.Put("/monitoring/pools/missing", map[string]any{"concurrency": 5})
	if resp.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404", resp.Code)
	}
}

func TestBrokerStatsRefresh(t *testing.T) {
	api, _, _, bstats, _, _ := setupAPI(t)
	resp := api.Post("/monitoring/broker-stats/refresh")
	if resp.Code != http.StatusOK {
		t.Fatalf("status %d", resp.Code)
	}
	if bstats.refresh != 1 {
		t.Errorf("refresh=%d want 1", bstats.refresh)
	}
}

func TestBreakerReset_Single(t *testing.T) {
	api, _, breakers, _, _, _ := setupAPI(t)
	resp := api.Post("/monitoring/circuit-breakers/target-a/reset")
	if resp.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", resp.Code, resp.Body.String())
	}
	if breakers.resetN != 1 {
		t.Errorf("reset count=%d want 1", breakers.resetN)
	}
}

func TestBreakerReset_All(t *testing.T) {
	api, _, breakers, _, _, _ := setupAPI(t)
	resp := api.Post("/monitoring/circuit-breakers/reset-all")
	if resp.Code != http.StatusOK {
		t.Fatalf("status %d", resp.Code)
	}
	if breakers.resetAll != 1 {
		t.Errorf("reset-all=%d want 1", breakers.resetAll)
	}
}

// ── Publish + seed ───────────────────────────────────────────────────────

func TestPublishMessage(t *testing.T) {
	api, _, _, _, _, pub := setupAPI(t)
	resp := api.Post("/messages",
		map[string]any{"pool_code": "demo", "mediation_target": "https://example.com/hook"})
	if resp.Code != http.StatusCreated {
		t.Fatalf("status %d body=%s", resp.Code, resp.Body.String())
	}
	if pub.lastMsg.MediationTarget != "https://example.com/hook" {
		t.Errorf("publish target=%q", pub.lastMsg.MediationTarget)
	}
	if pub.lastMsg.PoolCode != "demo" {
		t.Errorf("pool_code=%q", pub.lastMsg.PoolCode)
	}
	if pub.lastMsg.ID == "" {
		t.Errorf("ID should be auto-generated when omitted")
	}
}

func TestPublishMessage_MissingPoolCode(t *testing.T) {
	api, _, _, _, _, _ := setupAPI(t)
	resp := api.Post("/messages", map[string]any{"mediation_target": "https://x.test"})
	// huma schema-validates first: missing required field → 422.
	if resp.Code != http.StatusUnprocessableEntity {
		t.Errorf("status=%d want 422 body=%s", resp.Code, resp.Body.String())
	}
}

func TestSeedMessages(t *testing.T) {
	api, _, _, _, _, _ := setupAPI(t)
	resp := api.Post("/api/seed/messages", map[string]any{"pool_code": "demo", "count": 3})
	if resp.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", resp.Code, resp.Body.String())
	}
	var body routerapi.SeedMessagesResponse
	decodeBody(t, resp.Body.Bytes(), &body)
	if body.Published != 3 {
		t.Errorf("Published=%d want 3", body.Published)
	}
}

// ── Mock endpoints ───────────────────────────────────────────────────────

func TestMock_FastIncrementsCounter(t *testing.T) {
	api, _, _, _, _, _ := setupAPI(t)
	for i := 0; i < 3; i++ {
		resp := api.Post("/api/test/fast")
		if resp.Code != http.StatusOK {
			t.Fatalf("fast iter %d: status %d", i, resp.Code)
		}
	}
	stats := api.Get("/api/test/stats")
	var body routerapi.MockStatsResponse
	decodeBody(t, stats.Body.Bytes(), &body)
	if body.Fast != 3 {
		t.Errorf("Fast=%d want 3", body.Fast)
	}
}

func TestMock_FailReturns500(t *testing.T) {
	api, _, _, _, _, _ := setupAPI(t)
	resp := api.Post("/api/test/fail")
	if resp.Code != http.StatusInternalServerError {
		t.Errorf("status=%d want 500", resp.Code)
	}
}

func TestMock_ClientError400(t *testing.T) {
	api, _, _, _, _, _ := setupAPI(t)
	resp := api.Post("/api/test/client-error")
	if resp.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", resp.Code)
	}
}

func TestMock_StatsReset(t *testing.T) {
	api, _, _, _, _, _ := setupAPI(t)
	api.Post("/api/test/fast")
	resp := api.Post("/api/test/stats/reset")
	if resp.Code != http.StatusOK {
		t.Fatalf("status %d", resp.Code)
	}
	stats := api.Get("/api/test/stats")
	var body routerapi.MockStatsResponse
	decodeBody(t, stats.Body.Bytes(), &body)
	if body.Fast != 0 {
		t.Errorf("after reset Fast=%d want 0", body.Fast)
	}
}

// ── Misc endpoints ───────────────────────────────────────────────────────

func TestStandbyStatus(t *testing.T) {
	api, _, _, _, _, _ := setupAPI(t)
	resp := api.Get("/monitoring/standby-status")
	if resp.Code != http.StatusOK {
		t.Fatalf("status %d", resp.Code)
	}
	var body routerapi.StandbyStatusResponse
	decodeBody(t, resp.Body.Bytes(), &body)
	if !body.IsLeader {
		t.Errorf("IsLeader=false want true (stub)")
	}
}

func TestLocalConfig(t *testing.T) {
	api, _, _, _, _, _ := setupAPI(t)
	resp := api.Get("/api/config")
	if resp.Code != http.StatusOK {
		t.Fatalf("status %d", resp.Code)
	}
	var body routerapi.LocalConfigResponse
	decodeBody(t, resp.Body.Bytes(), &body)
	if body.Version == "" {
		t.Errorf("missing version")
	}
}

// ── Warnings ─────────────────────────────────────────────────────────────

func TestWarnings_AckLifecycle(t *testing.T) {
	api, _, _, _, _, _ := setupAPI(t)
	// Seed via the WarningService directly. setupAPI builds an empty
	// service so we add via the State path; for that we'd need to
	// expose it. Easier: call WarningService.Add through a fresh setup.
	ws := router.NewWarningService(router.WarningServiceConfig{})
	hs := router.NewHealthService(router.DefaultHealthServiceConfig(), ws)
	id := ws.Add(router.WarningCategoryConfiguration, router.WarningWarning, "test", "x")

	_, api2 := humatest.New(t)
	routerapi.Register(api2, &routerapi.State{
		Warnings: ws, Health: hs, Mocks: routerapi.NewMockState(),
	})

	resp := api2.Post("/monitoring/warnings/" + id + "/acknowledge")
	if resp.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", resp.Code, resp.Body.String())
	}
	var body routerapi.AcknowledgedResponse
	decodeBody(t, resp.Body.Bytes(), &body)
	if !body.Acknowledged {
		t.Errorf("not acknowledged")
	}
	_ = api // setup helper not used in this sub-test
}

// ── Dashboard HTML ───────────────────────────────────────────────────────

func TestDashboardHTML_ServesEmbedded(t *testing.T) {
	_, htmlRouter, _, _, _, _ := setupAPI(t)

	req := httptest.NewRequest("GET", "/monitoring/dashboard", nil)
	rec := httptest.NewRecorder()
	htmlRouter.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "FlowCatalyst Dashboard") {
		t.Errorf("expected title in body")
	}
	if strings.Contains(body, "__FC_API_BASE__") {
		t.Errorf("placeholder not substituted")
	}
}

func TestDashboardHTML_PrefixSubstitution(t *testing.T) {
	_, htmlRouter, _, _, _, _ := setupAPI(t)
	parent := chi.NewRouter()
	parent.Mount("/router", htmlRouter)

	req := httptest.NewRequest("GET", "/router/dashboard.html", nil)
	rec := httptest.NewRecorder()
	parent.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `window.__API_BASE__ = "/router";`) {
		t.Errorf("expected prefix injection, body excerpt:\n%s", excerpt(rec.Body.String(), "__API_BASE__"))
	}
}

func excerpt(s, marker string) string {
	idx := strings.Index(s, marker)
	if idx < 0 {
		return "<marker missing>"
	}
	start, end := idx-20, idx+80
	if start < 0 {
		start = 0
	}
	if end > len(s) {
		end = len(s)
	}
	return s[start:end]
}
