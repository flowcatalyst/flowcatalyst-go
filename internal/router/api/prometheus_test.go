package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flowcatalyst/flowcatalyst-go/internal/common"
	"github.com/flowcatalyst/flowcatalyst-go/internal/router"
	routerapi "github.com/flowcatalyst/flowcatalyst-go/internal/router/api"
)

func TestPrometheusHandler_EmitsExpectedMetrics(t *testing.T) {
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
			TotalSuccess: 100, TotalFailure: 2, TotalRateLimited: 1,
			SuccessRate: 100.0 / 102.0,
			ProcessingTime: common.ProcessingTimeMetrics{
				AvgMs: 25.0, P50Ms: 20, P95Ms: 80, P99Ms: 95, SampleCount: 102,
			},
		},
		Histogram: router.MediationHistogram{
			Bounds: []float64{0.1, 1}, Counts: []uint64{80, 100}, SumSeconds: 2.5, Count: 102,
		},
	}}}
	state := &routerapi.State{PoolStats: pools, Mocks: routerapi.NewMockState()}

	h := routerapi.PrometheusHandler(state)
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// Spot-check a representative slice of the exposition. Each line
	// here exercises a different code path (gauge / counter / labeled).
	want := []string{
		`fc_pool_active_workers{pool="demo"} 3`,
		`fc_pool_queue_size{pool="demo"} 5`,
		`fc_pool_message_groups{pool="demo"} 2`,
		`fc_messages_processed_total{pool="demo",success="true"} 100`,
		`fc_messages_processed_total{pool="demo",success="false"} 2`,
		`fc_rate_limit_exceeded_total{pool="demo"} 1`,
		`fc_mediation_duration_seconds_bucket{pool="demo",le="0.1"} 80`,
		`fc_mediation_duration_seconds_sum{pool="demo"} 2.5`,
		`fc_mediation_duration_seconds_count{pool="demo"} 102`,
	}
	for _, s := range want {
		if !strings.Contains(body, s) {
			t.Errorf("expected body to contain %q; got:\n%s", s, body)
		}
	}
}

func TestPrometheusHandler_EmitsBreakerAndInFlight(t *testing.T) {
	breakers := &stubBreakerSnapshotProvider{
		snap: map[string]router.BreakerStats{
			"target-a": {State: router.CircuitOpen, Successes: 5, Failures: 7},
		},
	}
	inflight := stubInFlightProvider{entries: []common.InFlightMessage{
		{MessageID: "m1"}, {MessageID: "m2"},
	}}
	state := &routerapi.State{
		Breakers: breakers, InFlight: inflight, Mocks: routerapi.NewMockState(),
	}

	h := routerapi.PrometheusHandler(state)
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()

	for _, s := range []string{
		`fc_circuit_breaker_open{target="target-a"} 1`,
		`fc_circuit_breaker_calls_total{outcome="success",target="target-a"} 5`,
		`fc_circuit_breaker_calls_total{outcome="failure",target="target-a"} 7`,
		`fc_in_pipeline_messages 2`,
	} {
		if !strings.Contains(body, s) {
			t.Errorf("missing %q in:\n%s", s, body)
		}
	}
}
