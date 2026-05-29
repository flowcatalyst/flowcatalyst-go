package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

// WarningCategory mirrors the Rust enum.
type WarningCategory string

const (
	WarningCategoryConfiguration WarningCategory = "CONFIGURATION"
	WarningCategoryConnection    WarningCategory = "CONNECTION"
	WarningCategoryRateLimit     WarningCategory = "RATE_LIMIT"
	WarningCategoryCircuitBreak  WarningCategory = "CIRCUIT_BREAKER"
	WarningCategoryStall         WarningCategory = "STALL"
	WarningCategoryResource      WarningCategory = "RESOURCE"
)

// WarningSeverity mirrors the Rust enum.
type WarningSeverity string

const (
	WarningInfo     WarningSeverity = "INFO"
	WarningWarning  WarningSeverity = "WARNING"
	WarningError    WarningSeverity = "ERROR"
	WarningCritical WarningSeverity = "CRITICAL"
)

// Warning is a structured operational notice. Mirrors the Rust
// `fc_common::Warning` shape so it can be persisted by WarningService
// and forwarded to NotificationService consumers without translation.
type Warning struct {
	ID             string          `json:"id"`
	Category       WarningCategory `json:"category"`
	Severity       WarningSeverity `json:"severity"`
	Message        string          `json:"message"`
	Source         string          `json:"source"`
	CreatedAt      time.Time       `json:"createdAt"`
	Acknowledged   bool            `json:"acknowledged"`
	AcknowledgedAt *time.Time      `json:"acknowledgedAt,omitempty"`
}

// NewWarning constructs a Warning with a freshly-minted UUID and the
// current time. Matches Rust's `Warning::new`.
func NewWarning(category WarningCategory, severity WarningSeverity, message, source string) Warning {
	return Warning{
		ID:        uuid.NewString(),
		Category:  category,
		Severity:  severity,
		Message:   message,
		Source:    source,
		CreatedAt: time.Now().UTC(),
	}
}

// AgeMinutes returns the warning's age in whole minutes.
func (w Warning) AgeMinutes() int64 {
	return int64(time.Since(w.CreatedAt).Minutes())
}

// Notifier delivers warnings to an external channel (Teams, Slack, etc.).
// Batches warnings to avoid hammering the destination during incidents.
type Notifier struct {
	webhookURL string
	batchSize  int
	interval   time.Duration
	client     *http.Client

	mu    sync.Mutex
	queue []Warning

	stopOnce sync.Once
	stopCh   chan struct{}
}

// NewNotifier builds a notifier. webhookURL empty → noop.
func NewNotifier(webhookURL string, batchSize int, interval time.Duration) *Notifier {
	return &Notifier{
		webhookURL: webhookURL,
		batchSize:  batchSize,
		interval:   interval,
		client:     &http.Client{Timeout: 10 * time.Second},
		stopCh:     make(chan struct{}),
	}
}

// Run starts the flush loop. Returns when ctx is cancelled or Stop is called.
func (n *Notifier) Run(ctx context.Context) {
	if n.webhookURL == "" {
		return // noop
	}
	tick := time.NewTicker(n.interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			n.flush(ctx)
			return
		case <-n.stopCh:
			n.flush(ctx)
			return
		case <-tick.C:
			n.flush(ctx)
		}
	}
}

// Add enqueues a warning. Flushed by the next tick or when the batch is full.
// Fills in ID + CreatedAt if the caller passed a bare-literal Warning.
func (n *Notifier) Add(w Warning) {
	if w.ID == "" {
		w.ID = uuid.NewString()
	}
	if w.CreatedAt.IsZero() {
		w.CreatedAt = time.Now().UTC()
	}
	n.mu.Lock()
	n.queue = append(n.queue, w)
	overflow := len(n.queue) >= n.batchSize
	n.mu.Unlock()
	if overflow {
		go n.flush(context.Background())
	}
}

// Stop signals the loop to exit and flushes any pending warnings.
func (n *Notifier) Stop() {
	n.stopOnce.Do(func() { close(n.stopCh) })
}

func (n *Notifier) flush(ctx context.Context) {
	n.mu.Lock()
	if len(n.queue) == 0 || n.webhookURL == "" {
		n.mu.Unlock()
		return
	}
	batch := n.queue
	n.queue = nil
	n.mu.Unlock()

	body, err := json.Marshal(map[string]any{"warnings": batch})
	if err != nil {
		slog.Warn("notifier: marshal failed", "err", err)
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.webhookURL, bytes.NewReader(body))
	if err != nil {
		slog.Warn("notifier: build req failed", "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		slog.Warn("notifier: post failed", "err", err, "batch_size", len(batch))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		slog.Warn("notifier: non-2xx", "status", resp.StatusCode)
	}
}

// String formats a warning for diagnostic logs.
func (w Warning) String() string {
	return fmt.Sprintf("[%s/%s] %s (from %s)", w.Category, w.Severity, w.Message, w.Source)
}
