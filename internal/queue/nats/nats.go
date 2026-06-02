// Package nats is the NATS JetStream queue backend. Mirrors the Rust
// crates/fc-queue/src/nats.rs behaviour:
//
//   - Pull-based JetStream consumer with configurable batch + timeout.
//   - WorkQueue retention (messages removed after ack).
//   - Durable consumer auto-provisioned at startup.
//   - Receipt handles are `streamName:streamSequence` (Java-format).
//   - Defer maps to NAK-with-delay (same as Nack with delay >0).
//
// URI scheme: `nats://host:port` (optionally with comma-separated hosts).
// Stream + consumer + subject come from query params:
//
//	nats://localhost:4222?stream=FLOWCATALYST&consumer=fc-router&subject=flowcatalyst.>
//
// Defaults match Rust: stream=FLOWCATALYST, consumer=fc-router,
// subject=flowcatalyst.>, max-messages=10, poll-timeout=20s, ack-wait=120s,
// max-deliver=10, max-ack-pending=1000, storage=file, replicas=1,
// max-age-days=7.
package nats

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/flowcatalyst/flowcatalyst-go/internal/common"
	"github.com/flowcatalyst/flowcatalyst-go/internal/queue"
)

func init() {
	queue.RegisterConsumer("nats", consumerFactory)
	queue.RegisterPublisher("nats", publisherFactory)
}

// Config controls the NATS connection + stream provisioning.
type Config struct {
	Servers            string // comma-separated nats URLs
	StreamName         string
	ConsumerName       string
	Subject            string
	MaxMessagesPerPoll int
	PollTimeout        time.Duration
	AckWait            time.Duration
	MaxDeliver         int
	MaxAckPending      int
	Storage            string // "file" | "memory"
	Replicas           int
	MaxAge             time.Duration // 0 = unlimited
}

// DefaultConfig matches Rust's NatsConfig::default().
func DefaultConfig() Config {
	return Config{
		Servers:            "nats://localhost:4222",
		StreamName:         "FLOWCATALYST",
		ConsumerName:       "fc-router",
		Subject:            "flowcatalyst.>",
		MaxMessagesPerPoll: 10,
		PollTimeout:        20 * time.Second,
		AckWait:            120 * time.Second,
		MaxDeliver:         10,
		MaxAckPending:      1000,
		Storage:            "file",
		Replicas:           1,
		MaxAge:             7 * 24 * time.Hour,
	}
}

func consumerFactory(ctx context.Context, cfg common.QueueConfig) (queue.Consumer, error) {
	q, err := newQueue(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return q, nil
}

func publisherFactory(ctx context.Context, cfg common.QueueConfig) (queue.Publisher, error) {
	q, err := newQueue(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return q, nil
}

// Queue is a NATS JetStream-backed queue (consumer + publisher).
type Queue struct {
	cfg        Config
	identifier string

	nc       *natsgo.Conn
	js       jetstream.JetStream
	consumer jetstream.Consumer

	running atomic.Bool

	pendingMu sync.Mutex
	pending   map[string]jetstream.Msg

	totalPolled   atomic.Uint64
	totalAcked    atomic.Uint64
	totalNacked   atomic.Uint64
	totalDeferred atomic.Uint64
}

func newQueue(ctx context.Context, qc common.QueueConfig) (*Queue, error) {
	cfg, err := parseURI(qc.URI)
	if err != nil {
		return nil, err
	}
	nc, err := natsgo.Connect(cfg.Servers,
		natsgo.Timeout(10*time.Second),
		natsgo.ReconnectWait(2*time.Second),
		natsgo.MaxReconnects(-1),
	)
	if err != nil {
		return nil, fmt.Errorf("nats: connect: %w", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("nats: jetstream: %w", err)
	}

	storage := jetstream.FileStorage
	if strings.EqualFold(cfg.Storage, "memory") {
		storage = jetstream.MemoryStorage
	}
	stream, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      cfg.StreamName,
		Subjects:  []string{cfg.Subject},
		Retention: jetstream.WorkQueuePolicy,
		Storage:   storage,
		Replicas:  cfg.Replicas,
		MaxAge:    cfg.MaxAge,
	})
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("nats: get/create stream %q: %w", cfg.StreamName, err)
	}
	consumer, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Name:          cfg.ConsumerName,
		Durable:       cfg.ConsumerName,
		AckWait:       cfg.AckWait,
		MaxDeliver:    cfg.MaxDeliver,
		MaxAckPending: cfg.MaxAckPending,
		FilterSubject: cfg.Subject,
	})
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("nats: get/create consumer %q: %w", cfg.ConsumerName, err)
	}

	q := &Queue{
		cfg:        cfg,
		identifier: cfg.StreamName + "/" + cfg.ConsumerName,
		nc:         nc,
		js:         js,
		consumer:   consumer,
		pending:    make(map[string]jetstream.Msg),
	}
	q.running.Store(true)
	return q, nil
}

// parseURI accepts `nats://host:port[?stream=...&consumer=...&subject=...&...]`.
func parseURI(uri string) (Config, error) {
	cfg := DefaultConfig()
	u, err := url.Parse(uri)
	if err != nil {
		return cfg, fmt.Errorf("nats: parse URI: %w", err)
	}
	if u.Scheme != "nats" {
		return cfg, fmt.Errorf("nats: expected scheme nats://, got %q", u.Scheme)
	}
	// Rebuild server list from the URI host (and any commas inside).
	servers := u.Scheme + "://" + u.Host
	cfg.Servers = servers
	q := u.Query()
	if v := q.Get("stream"); v != "" {
		cfg.StreamName = v
	}
	if v := q.Get("consumer"); v != "" {
		cfg.ConsumerName = v
	}
	if v := q.Get("subject"); v != "" {
		cfg.Subject = v
	}
	if v := q.Get("max-messages"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.MaxMessagesPerPoll = n
		}
	}
	if v := q.Get("poll-timeout-ms"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil {
			cfg.PollTimeout = time.Duration(ms) * time.Millisecond
		}
	}
	if v := q.Get("ack-wait-secs"); v != "" {
		if s, err := strconv.Atoi(v); err == nil {
			cfg.AckWait = time.Duration(s) * time.Second
		}
	}
	if v := q.Get("max-deliver"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.MaxDeliver = n
		}
	}
	if v := q.Get("max-ack-pending"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.MaxAckPending = n
		}
	}
	if v := q.Get("storage"); v != "" {
		cfg.Storage = v
	}
	if v := q.Get("replicas"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Replicas = n
		}
	}
	if v := q.Get("max-age-days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			if n > 0 {
				cfg.MaxAge = time.Duration(n) * 24 * time.Hour
			} else {
				cfg.MaxAge = 0
			}
		}
	}
	return cfg, nil
}

// Identifier returns "stream/consumer" — matches Rust + Java format.
func (q *Queue) Identifier() string { return q.identifier }

// Poll fetches up to max messages with the configured poll timeout.
func (q *Queue) Poll(ctx context.Context, max uint32) ([]common.QueuedMessage, error) {
	if !q.running.Load() {
		return nil, errors.New("nats: consumer stopped")
	}
	batch := int(max)
	if batch <= 0 || batch > q.cfg.MaxMessagesPerPoll {
		batch = q.cfg.MaxMessagesPerPoll
	}
	// Expiring fetch: block up to the poll timeout for a full batch,
	// matching Rust's consumer.fetch().expires(timeout). FetchNoWait would
	// return immediately and hot-spin the router's poll loop.
	msgs, err := q.consumer.Fetch(batch, jetstream.FetchMaxWait(q.cfg.PollTimeout))
	if err != nil {
		return nil, fmt.Errorf("nats: fetch: %w", err)
	}
	var out []common.QueuedMessage
	for msg := range msgs.Messages() {
		meta, err := msg.Metadata()
		if err != nil {
			// Can't track — term so the server stops redelivering.
			_ = msg.Term()
			continue
		}
		receipt := fmt.Sprintf("%s:%d", q.cfg.StreamName, meta.Sequence.Stream)
		var m common.Message
		if err := json.Unmarshal(msg.Data(), &m); err != nil {
			_ = msg.Term() // malformed
			continue
		}
		q.pendingMu.Lock()
		q.pending[receipt] = msg
		q.pendingMu.Unlock()
		out = append(out, common.QueuedMessage{
			Message:         m,
			ReceiptHandle:   receipt,
			BrokerMessageID: fmt.Sprintf("%d:%d", meta.Sequence.Stream, meta.Sequence.Consumer),
			QueueIdentifier: q.identifier,
		})
	}
	if msgsErr := msgs.Error(); msgsErr != nil && !errors.Is(msgsErr, natsgo.ErrTimeout) {
		// Real fetch error (timeout is fine — just means nothing available).
		// Drop through; we still return whatever we successfully drained.
		_ = msgsErr
	}
	if len(out) > 0 {
		q.totalPolled.Add(uint64(len(out)))
	}
	return out, nil
}

// Ack consumes the receipt and ACKs the underlying JetStream message.
func (q *Queue) Ack(_ context.Context, receipt string) error {
	msg := q.popPending(receipt)
	if msg == nil {
		return fmt.Errorf("nats: no pending message for receipt %q", receipt)
	}
	if err := msg.Ack(); err != nil {
		return fmt.Errorf("nats: ack: %w", err)
	}
	q.totalAcked.Add(1)
	return nil
}

// Nack NAKs with optional delay. Counts as a failure.
func (q *Queue) Nack(_ context.Context, receipt string, delaySeconds *uint32) error {
	msg := q.popPending(receipt)
	if msg == nil {
		return fmt.Errorf("nats: no pending message for receipt %q", receipt)
	}
	if err := q.nakWith(msg, delaySeconds); err != nil {
		return err
	}
	q.totalNacked.Add(1)
	return nil
}

// Defer NAKs with delay but doesn't count as a failure (backpressure /
// rate-limit signal). Same wire effect as Nack-with-delay in NATS.
func (q *Queue) Defer(_ context.Context, receipt string, delaySeconds *uint32) error {
	msg := q.popPending(receipt)
	if msg == nil {
		return fmt.Errorf("nats: no pending message for receipt %q", receipt)
	}
	if err := q.nakWith(msg, delaySeconds); err != nil {
		return err
	}
	q.totalDeferred.Add(1)
	return nil
}

func (q *Queue) nakWith(msg jetstream.Msg, delaySeconds *uint32) error {
	if delaySeconds != nil && *delaySeconds > 0 {
		return msg.NakWithDelay(time.Duration(*delaySeconds) * time.Second)
	}
	return msg.Nak()
}

// ExtendVisibility resets the ack-wait timer via InProgress.
func (q *Queue) ExtendVisibility(_ context.Context, receipt string, _ uint32) error {
	q.pendingMu.Lock()
	msg, ok := q.pending[receipt]
	q.pendingMu.Unlock()
	if !ok {
		return fmt.Errorf("nats: no pending message for receipt %q", receipt)
	}
	if err := msg.InProgress(); err != nil {
		return fmt.Errorf("nats: in-progress: %w", err)
	}
	return nil
}

// Healthy reports whether the consumer is running and the underlying
// connection is up.
func (q *Queue) Healthy() bool {
	if !q.running.Load() {
		return false
	}
	return q.nc.Status() == natsgo.CONNECTED
}

// Stop signals the consumer to wind down. In-flight messages are
// dropped from the local tracker; the server will redeliver after the
// ack-wait expires.
func (q *Queue) Stop() {
	q.running.Store(false)
	q.pendingMu.Lock()
	q.pending = make(map[string]jetstream.Msg)
	q.pendingMu.Unlock()
	q.nc.Close()
}

// Metrics queries JetStream for the consumer's pending + in-flight counts.
func (q *Queue) Metrics(ctx context.Context) (*queue.Metrics, error) {
	info, err := q.consumer.Info(ctx)
	if err != nil {
		return nil, fmt.Errorf("nats: consumer info: %w", err)
	}
	return &queue.Metrics{
		QueueIdentifier:  q.identifier,
		PendingMessages:  info.NumPending,
		InFlightMessages: uint64(info.NumAckPending),
		TotalPolled:      q.totalPolled.Load(),
		TotalAcked:       q.totalAcked.Load(),
		TotalNacked:      q.totalNacked.Load(),
		TotalDeferred:    q.totalDeferred.Load(),
	}, nil
}

// Counters returns the process-local counters (no broker round-trip).
func (q *Queue) Counters() *queue.Metrics {
	return &queue.Metrics{
		QueueIdentifier: q.identifier,
		TotalPolled:     q.totalPolled.Load(),
		TotalAcked:      q.totalAcked.Load(),
		TotalNacked:     q.totalNacked.Load(),
		TotalDeferred:   q.totalDeferred.Load(),
	}
}

// Publish marshals m to JSON and publishes to the configured subject.
// The returned id is the JetStream stream sequence.
func (q *Queue) Publish(ctx context.Context, m common.Message) (string, error) {
	body, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("nats: marshal: %w", err)
	}
	ack, err := q.js.Publish(ctx, subjectFor(q.cfg.Subject, m), body)
	if err != nil {
		return "", fmt.Errorf("nats: publish: %w", err)
	}
	return strconv.FormatUint(ack.Sequence, 10), nil
}

// PublishBatch publishes each message sequentially. NATS doesn't have
// true batch publish; we accept the round-trip cost for simplicity.
func (q *Queue) PublishBatch(ctx context.Context, msgs []common.Message) ([]string, error) {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		id, err := q.Publish(ctx, m)
		if err != nil {
			return out, err
		}
		out = append(out, id)
	}
	return out, nil
}

func (q *Queue) popPending(receipt string) jetstream.Msg {
	q.pendingMu.Lock()
	defer q.pendingMu.Unlock()
	msg, ok := q.pending[receipt]
	if !ok {
		return nil
	}
	delete(q.pending, receipt)
	return msg
}

// subjectFor picks the subject for a published message. If the
// configured filter is a wildcard like `flowcatalyst.>`, we substitute
// the message's pool code for the wildcard part; otherwise we use the
// filter verbatim.
func subjectFor(filter string, m common.Message) string {
	if strings.HasSuffix(filter, ".>") || strings.HasSuffix(filter, ".*") {
		base := strings.TrimSuffix(strings.TrimSuffix(filter, ".>"), ".*")
		if m.PoolCode != "" {
			return base + "." + m.PoolCode
		}
		return base + ".default"
	}
	return filter
}
