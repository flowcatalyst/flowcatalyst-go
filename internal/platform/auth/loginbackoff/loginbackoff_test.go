package loginbackoff

import (
	"context"
	"testing"
	"time"
)

func defaultPolicy() Policy {
	return Policy{
		FreeAttempts:     3,
		BaseDelaySecs:    2,
		MaxDelaySecs:     300,
		GlobalWindowSecs: 3600,
		GlobalCeiling:    100,
		GlobalLockSecs:   900,
	}
}

func TestComputeDelaySecs(t *testing.T) {
	p := defaultPolicy()
	cases := map[uint32]uint32{
		0: 0, 1: 0, 2: 0, 3: 0, // free
		4: 2, 5: 4, 6: 8, 7: 16, 8: 32, 9: 64, 10: 128, 11: 256,
		12: 300, 20: 300, 100: 300, // capped
	}
	for in, want := range cases {
		if got := p.ComputeDelaySecs(in); got != want {
			t.Errorf("ComputeDelaySecs(%d) = %d, want %d", in, got, want)
		}
	}
	if got := p.ComputeDelaySecs(^uint32(0)); got != 300 {
		t.Errorf("ComputeDelaySecs(max) = %d, want 300 (no overflow)", got)
	}
}

// fakeRepo implements statsRepo for Check tests.
type fakeRepo struct {
	lastSuccess  *time.Time
	pairCount    int
	pairLastFail *time.Time
	globalCount  int
}

func (f *fakeRepo) LastSuccessAt(context.Context, string) (*time.Time, error) {
	return f.lastSuccess, nil
}

func (f *fakeRepo) FailureStatsByIdentifierIPSince(context.Context, string, string, time.Time) (int, *time.Time, error) {
	return f.pairCount, f.pairLastFail, nil
}

func (f *fakeRepo) FailureCountByIdentifierSince(context.Context, string, time.Time) (int, error) {
	return f.globalCount, nil
}

func TestCheckAllowsCleanIdentifier(t *testing.T) {
	d, err := Check(context.Background(), &fakeRepo{}, defaultPolicy(), "a@b.com", "1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	if !d.Allowed {
		t.Errorf("clean identifier should be allowed, got %+v", d)
	}
}

func TestCheckPairBackoffRejects(t *testing.T) {
	// 4 failures (curve start = 2s required) with the last failure just now
	// → elapsed ~0 < 2 → reject.
	now := time.Now().UTC()
	d, err := Check(context.Background(), &fakeRepo{pairCount: 4, pairLastFail: &now}, defaultPolicy(), "a@b.com", "1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	if d.Allowed || d.Reason != ReasonPairBackoff {
		t.Errorf("want pair_backoff reject, got %+v", d)
	}
	if d.RetryAfterSecs == 0 || d.RetryAfterSecs > 2 {
		t.Errorf("retry_after = %d, want (0,2]", d.RetryAfterSecs)
	}
}

func TestCheckPairBackoffElapsedAllows(t *testing.T) {
	// 4 failures but the last was 10s ago (> 2s required) → allowed.
	old := time.Now().UTC().Add(-10 * time.Second)
	d, err := Check(context.Background(), &fakeRepo{pairCount: 4, pairLastFail: &old}, defaultPolicy(), "a@b.com", "1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	if !d.Allowed {
		t.Errorf("elapsed past required delay should allow, got %+v", d)
	}
}

func TestCheckGlobalCeilingRejects(t *testing.T) {
	d, err := Check(context.Background(), &fakeRepo{globalCount: 100}, defaultPolicy(), "a@b.com", "")
	if err != nil {
		t.Fatal(err)
	}
	if d.Allowed || d.Reason != ReasonGlobalCeiling {
		t.Errorf("want global_ceiling reject, got %+v", d)
	}
	if d.RetryAfterSecs != 900 {
		t.Errorf("retry_after = %d, want 900", d.RetryAfterSecs)
	}
}

func TestCheckNoIPSkipsPairBackoff(t *testing.T) {
	// Even with a high pair count, an empty IP skips the per-pair gate;
	// only the global ceiling applies (here under the ceiling → allowed).
	now := time.Now().UTC()
	d, err := Check(context.Background(), &fakeRepo{pairCount: 50, pairLastFail: &now, globalCount: 5}, defaultPolicy(), "a@b.com", "")
	if err != nil {
		t.Fatal(err)
	}
	if !d.Allowed {
		t.Errorf("empty IP should skip pair backoff, got %+v", d)
	}
}
