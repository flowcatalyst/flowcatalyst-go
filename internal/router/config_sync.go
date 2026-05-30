package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/internal/common"
)

// ConfigSource fetches the live RouterConfig from one or more remote
// endpoints. When FLOWCATALYST_CONFIG_URL is comma-separated, all URLs are
// fetched in parallel (each with its own retry) and the results are merged
// (union, first-wins) — 1:1 with the Rust ConfigSyncService. Per-URL
// failures are tolerated as long as at least one source succeeds.
type ConfigSource struct {
	URLs   []string
	Client *http.Client
	// MaxAttempts/RetryDelay govern per-URL retry (Java/Rust defaults: 12 / 5s).
	MaxAttempts int
	RetryDelay  time.Duration

	mu   sync.Mutex
	last []byte // last merged config (marshaled) for change detection
}

// NewConfigSource builds a source from a (possibly comma-separated) URL.
func NewConfigSource(url string) *ConfigSource {
	var urls []string
	for _, u := range strings.Split(url, ",") {
		if u = strings.TrimSpace(u); u != "" {
			urls = append(urls, u)
		}
	}
	return &ConfigSource{
		URLs:        urls,
		Client:      &http.Client{Timeout: 10 * time.Second},
		MaxAttempts: 12,
		RetryDelay:  5 * time.Second,
	}
}

// ErrUnchanged is returned by Fetch when the merged config matches the
// previous fetch — callers can skip reconfigure in that case.
var ErrUnchanged = errors.New("config unchanged")

type sourceConfig struct {
	url string
	cfg common.RouterConfig
}

// Fetch fetches every configured URL in parallel (each retried up to
// MaxAttempts) and returns the merged config. Returns ErrUnchanged when the
// merged result matches the previous fetch, or an error only when ALL sources
// fail.
func (cs *ConfigSource) Fetch(ctx context.Context) (*common.RouterConfig, error) {
	if len(cs.URLs) == 0 {
		return nil, errors.New("config: no URLs configured")
	}

	cfgs := make([]*common.RouterConfig, len(cs.URLs))
	errs := make([]error, len(cs.URLs))
	var wg sync.WaitGroup
	for i, u := range cs.URLs {
		wg.Add(1)
		go func(i int, u string) {
			defer wg.Done()
			cfgs[i], errs[i] = cs.fetchWithRetry(ctx, u)
		}(i, u)
	}
	wg.Wait()

	// Collect successes in URL order so the merge is deterministic (first-wins).
	var ok []sourceConfig
	for i := range cs.URLs {
		if errs[i] != nil {
			slog.Warn("config fetch failed for source", "url", cs.URLs[i], "err", errs[i])
			continue
		}
		ok = append(ok, sourceConfig{url: cs.URLs[i], cfg: *cfgs[i]})
	}
	if len(ok) == 0 {
		return nil, fmt.Errorf("config: all %d source(s) failed", len(cs.URLs))
	}

	merged := mergeConfigs(ok)

	// Change detection on the merged config (marshaled).
	body, err := json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("config marshal: %w", err)
	}
	cs.mu.Lock()
	unchanged := len(cs.last) > 0 && bytesEqual(cs.last, body)
	cs.last = body
	cs.mu.Unlock()
	if unchanged {
		return nil, ErrUnchanged
	}
	return &merged, nil
}

// fetchWithRetry fetches a single URL, retrying up to MaxAttempts with
// RetryDelay between attempts (ctx-aware). Mirrors Rust fetch_config_from_url.
func (cs *ConfigSource) fetchWithRetry(ctx context.Context, url string) (*common.RouterConfig, error) {
	var lastErr error
	for attempt := 1; attempt <= cs.MaxAttempts; attempt++ {
		cfg, err := cs.fetchOnce(ctx, url)
		if err == nil {
			return cfg, nil
		}
		lastErr = err
		if attempt < cs.MaxAttempts {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(cs.RetryDelay):
			}
		}
	}
	return nil, fmt.Errorf("after %d attempts: %w", cs.MaxAttempts, lastErr)
}

func (cs *ConfigSource) fetchOnce(ctx context.Context, url string) (*common.RouterConfig, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := cs.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("config fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("config fetch: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var cfg common.RouterConfig
	if err := json.Unmarshal(body, &cfg); err != nil {
		return nil, fmt.Errorf("config decode: %w", err)
	}
	return &cfg, nil
}

// mergeConfigs unions multiple source configs, first-wins: a pool is keyed by
// code, a queue by URI; the first source to define a key wins, later
// duplicates are dropped (with a warning on a value conflict). 1:1 with Rust
// merge_configs. A single source passes through unchanged.
func mergeConfigs(sources []sourceConfig) common.RouterConfig {
	if len(sources) == 1 {
		return sources[0].cfg
	}
	var merged common.RouterConfig
	poolOrigin := map[string]string{}
	queueOrigin := map[string]string{}
	for _, s := range sources {
		for _, p := range s.cfg.ProcessingPools {
			if orig, seen := poolOrigin[p.Code]; seen {
				if conflictingPool(merged.ProcessingPools, p) {
					slog.Warn("duplicate pool with conflicting values — keeping first",
						"pool_code", p.Code, "kept_source", orig, "dropped_source", s.url)
				}
				continue
			}
			poolOrigin[p.Code] = s.url
			merged.ProcessingPools = append(merged.ProcessingPools, p)
		}
		for _, q := range s.cfg.Queues {
			if orig, seen := queueOrigin[q.URI]; seen {
				if conflictingQueue(merged.Queues, q) {
					slog.Warn("duplicate queue with conflicting values — keeping first",
						"queue_uri", q.URI, "kept_source", orig, "dropped_source", s.url)
				}
				continue
			}
			queueOrigin[q.URI] = s.url
			merged.Queues = append(merged.Queues, q)
		}
	}
	return merged
}

func conflictingPool(existing []common.PoolConfig, p common.PoolConfig) bool {
	for _, e := range existing {
		if e.Code == p.Code {
			return e.Concurrency != p.Concurrency || !u32PtrEqual(e.RateLimitPerMinute, p.RateLimitPerMinute)
		}
	}
	return false
}

func conflictingQueue(existing []common.QueueConfig, q common.QueueConfig) bool {
	for _, e := range existing {
		if e.URI == q.URI {
			return e.Name != q.Name || e.Connections != q.Connections || e.VisibilityTimeout != q.VisibilityTimeout
		}
	}
	return false
}

func u32PtrEqual(a, b *uint32) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// Watch polls cs every interval and applies the result to manager.
// Blocks until ctx is cancelled.
func Watch(ctx context.Context, cs *ConfigSource, manager *Manager, interval time.Duration) {
	tick := time.NewTicker(interval)
	defer tick.Stop()

	apply := func() {
		cfg, err := cs.Fetch(ctx)
		if errors.Is(err, ErrUnchanged) {
			return
		}
		if err != nil {
			slog.Warn("config fetch failed", "err", err)
			return
		}
		if err := manager.Reconfigure(ctx, *cfg); err != nil {
			slog.Warn("manager reconfigure failed", "err", err)
		}
	}

	apply()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			apply()
		}
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
