// Package sqs is the AWS SQS-backed queue backend.
//
// Mirrors fc-queue/src/sqs.rs:
//   - 20s long-poll (AWS max) for low API-call rate.
//   - Visibility timeout configurable per queue.
//   - Pending-delete guard for at-least-once redeliveries: once we
//     successfully (or unsuccessfully) DeleteMessage for a MessageId,
//     subsequent redeliveries within PendingDeleteTTL are deleted
//     immediately on poll instead of being routed to the mediator.
package sqs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	neturl "net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/flowcatalyst/flowcatalyst-go/internal/common"
	"github.com/flowcatalyst/flowcatalyst-go/internal/queue"
)

// PendingDeleteTTL is how long we remember an acked MessageId so
// redeliveries (SQS standard queues are at-least-once) are
// short-circuited to DeleteMessage. Matches Rust 15 minutes.
const PendingDeleteTTL = 15 * time.Minute

// DefaultWaitSeconds is the long-poll wait time. AWS max is 20s.
const DefaultWaitSeconds = 20

func init() {
	queue.RegisterConsumer("sqs", consumerFactory)
	queue.RegisterPublisher("sqs", publisherFactory)
}

func consumerFactory(ctx context.Context, cfg common.QueueConfig) (queue.Consumer, error) {
	q, err := build(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return q, nil
}

func publisherFactory(ctx context.Context, cfg common.QueueConfig) (queue.Publisher, error) {
	q, err := build(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return q, nil
}

func build(ctx context.Context, cfg common.QueueConfig) (*Queue, error) {
	// The queue's own URL encodes its region (sqs.<region>.amazonaws.com), so
	// use it: an SQS queue must be reached in its own region, and this works
	// even when AWS_REGION/AWS_DEFAULT_REGION isn't set in the environment
	// (e.g. an ECS task def that doesn't export it). Falls back to the SDK's
	// default region chain when the URL isn't a recognisable SQS endpoint.
	var opts []func(*awsconfig.LoadOptions) error
	if region := regionFromSQSURL(cfg.URI); region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}
	client := sqs.NewFromConfig(awsCfg)
	queueName := cfg.Name
	if queueName == "" {
		queueName = queueNameFromURL(cfg.URI)
	}
	vt := cfg.VisibilityTimeout
	if vt == 0 {
		vt = 30
	}
	q := &Queue{
		client:             client,
		queueURL:           cfg.URI,
		queueName:          queueName,
		visibilityTimeout:  int32(vt),
		waitSeconds:        DefaultWaitSeconds,
		pendingDelete:      make(map[string]time.Time),
		receiptToMessageID: make(map[string]receiptMapping),
	}
	q.running.Store(true)
	return q, nil
}

// regionFromSQSURL extracts the AWS region from an SQS queue URL whose host is
// sqs.<region>.amazonaws.com (or sqs-fips.<region>.amazonaws.com[.cn]). Returns
// "" when uri is not a recognisable SQS endpoint (e.g. a non-AWS test URI).
func regionFromSQSURL(uri string) string {
	u, err := neturl.Parse(uri)
	if err != nil || u.Host == "" {
		return ""
	}
	parts := strings.Split(u.Host, ".")
	if len(parts) >= 4 && strings.HasPrefix(parts[0], "sqs") && parts[2] == "amazonaws" {
		return parts[1]
	}
	return ""
}

func queueNameFromURL(url string) string {
	parts := strings.Split(url, "/")
	if len(parts) == 0 {
		return "unknown"
	}
	return parts[len(parts)-1]
}

type receiptMapping struct {
	MessageID string
	At        time.Time
}

// Queue is the SQS-backed queue. Implements both Consumer and Publisher.
type Queue struct {
	client            *sqs.Client
	queueURL          string
	queueName         string
	visibilityTimeout int32
	waitSeconds       int32

	mu                 sync.Mutex
	pendingDelete      map[string]time.Time
	receiptToMessageID map[string]receiptMapping

	running atomic.Bool

	polled   atomic.Uint64
	acked    atomic.Uint64
	nacked   atomic.Uint64
	deferred atomic.Uint64
}

// Identifier returns the queue name.
func (q *Queue) Identifier() string { return q.queueName }

// Poll fetches up to maxMessages via ReceiveMessage with long-polling.
func (q *Queue) Poll(ctx context.Context, maxMessages uint32) ([]common.QueuedMessage, error) {
	if !q.running.Load() {
		return nil, errors.New("sqs: stopped")
	}
	max := int32(maxMessages)
	if max > 10 {
		max = 10 // SQS hard limit
	}

	out, err := q.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:                    aws.String(q.queueURL),
		MaxNumberOfMessages:         max,
		VisibilityTimeout:           q.visibilityTimeout,
		WaitTimeSeconds:             q.waitSeconds,
		MessageSystemAttributeNames: []sqstypes.MessageSystemAttributeName{sqstypes.MessageSystemAttributeNameAll},
		MessageAttributeNames:       []string{"All"},
	})
	if err != nil {
		return nil, fmt.Errorf("sqs ReceiveMessage: %w", err)
	}
	if len(out.Messages) == 0 {
		return nil, nil
	}

	q.evictExpiredPendingDeletesLocked()
	results := make([]common.QueuedMessage, 0, len(out.Messages))
	for _, sm := range out.Messages {
		if sm.MessageId != nil {
			q.mu.Lock()
			_, alreadyAcked := q.pendingDelete[*sm.MessageId]
			q.mu.Unlock()
			if alreadyAcked {
				// Redelivery of an acked message — delete immediately.
				if sm.ReceiptHandle != nil {
					_, _ = q.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
						QueueUrl:      aws.String(q.queueURL),
						ReceiptHandle: sm.ReceiptHandle,
					})
				}
				continue
			}
		}

		msg, receipt, brokerID, perr := q.parseMessage(sm)
		if perr != nil {
			// Malformed — ACK it so it doesn't keep coming back.
			if sm.ReceiptHandle != nil {
				_ = q.Ack(ctx, *sm.ReceiptHandle)
			}
			continue
		}
		if brokerID != "" {
			q.mu.Lock()
			q.receiptToMessageID[receipt] = receiptMapping{MessageID: brokerID, At: time.Now()}
			q.mu.Unlock()
		}
		results = append(results, common.QueuedMessage{
			Message:         msg,
			ReceiptHandle:   receipt,
			BrokerMessageID: brokerID,
			QueueIdentifier: q.queueName,
		})
	}

	q.polled.Add(uint64(len(results)))
	return results, nil
}

func (q *Queue) parseMessage(sm sqstypes.Message) (common.Message, string, string, error) {
	if sm.Body == nil {
		return common.Message{}, "", "", errors.New("empty body")
	}
	var m common.Message
	if err := json.Unmarshal([]byte(*sm.Body), &m); err != nil {
		return common.Message{}, "", "", fmt.Errorf("unmarshal: %w", err)
	}
	if sm.ReceiptHandle == nil {
		return common.Message{}, "", "", errors.New("missing receipt handle")
	}
	brokerID := ""
	if sm.MessageId != nil {
		brokerID = *sm.MessageId
	}
	return m, *sm.ReceiptHandle, brokerID, nil
}

// Ack deletes the message and records the MessageId in the pending-delete map.
func (q *Queue) Ack(ctx context.Context, receipt string) error {
	q.mu.Lock()
	mapping, ok := q.receiptToMessageID[receipt]
	if ok {
		delete(q.receiptToMessageID, receipt)
		q.pendingDelete[mapping.MessageID] = time.Now()
	}
	q.mu.Unlock()

	_, err := q.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(q.queueURL),
		ReceiptHandle: aws.String(receipt),
	})
	if err != nil {
		return fmt.Errorf("sqs DeleteMessage: %w", err)
	}
	q.acked.Add(1)
	return nil
}

// Nack is intentionally a NO-OP for SQS. The router retries failed messages
// in-process — it keeps them in the message-group pipeline with an internal
// backoff rather than releasing them to the broker — so it must NOT shorten
// the visibility timeout here. Doing so would let SQS redeliver the message
// (to this or another replica) while the router is still retrying it. Instead
// the message stays invisible until its visibility timeout lapses naturally;
// any such redelivery is deduplicated by broker MessageId (the router swaps the
// receipt handle onto the in-flight copy and drops the duplicate). The message
// only leaves SQS when the router DeleteMessages it on success. delaySeconds is
// ignored by design. The counter is kept for observability.
func (q *Queue) Nack(_ context.Context, _ string, _ *uint32) error {
	q.nacked.Add(1)
	return nil
}

// Defer is a NO-OP for the same reason as Nack — 429 / circuit-open retries are
// also driven in-process. See Nack.
func (q *Queue) Defer(_ context.Context, _ string, _ *uint32) error {
	q.deferred.Add(1)
	return nil
}

// ExtendVisibility prolongs the visibility timeout for in-flight processing.
func (q *Queue) ExtendVisibility(ctx context.Context, receipt string, seconds uint32) error {
	return q.changeVisibility(ctx, receipt, &seconds)
}

func (q *Queue) changeVisibility(ctx context.Context, receipt string, delaySeconds *uint32) error {
	v := int32(0)
	if delaySeconds != nil {
		v = int32(*delaySeconds)
	}
	_, err := q.client.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl:          aws.String(q.queueURL),
		ReceiptHandle:     aws.String(receipt),
		VisibilityTimeout: v,
	})
	if err != nil {
		return fmt.Errorf("sqs ChangeMessageVisibility: %w", err)
	}
	return nil
}

// Publish sends a single message via SendMessage.
func (q *Queue) Publish(ctx context.Context, m common.Message) (string, error) {
	body, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	in := &sqs.SendMessageInput{
		QueueUrl:    aws.String(q.queueURL),
		MessageBody: aws.String(string(body)),
	}
	if m.MessageGroupID != nil {
		in.MessageGroupId = aws.String(*m.MessageGroupID)
	}
	out, err := q.client.SendMessage(ctx, in)
	if err != nil {
		return "", fmt.Errorf("sqs SendMessage: %w", err)
	}
	if out.MessageId == nil {
		return "", nil
	}
	return *out.MessageId, nil
}

// PublishBatch sends in batches of 10 (SQS hard limit).
func (q *Queue) PublishBatch(ctx context.Context, msgs []common.Message) ([]string, error) {
	ids := make([]string, 0, len(msgs))
	for start := 0; start < len(msgs); start += 10 {
		end := start + 10
		if end > len(msgs) {
			end = len(msgs)
		}
		entries := make([]sqstypes.SendMessageBatchRequestEntry, 0, end-start)
		for i := start; i < end; i++ {
			body, err := json.Marshal(msgs[i])
			if err != nil {
				return ids, err
			}
			e := sqstypes.SendMessageBatchRequestEntry{
				Id:          aws.String(strconv.Itoa(i)),
				MessageBody: aws.String(string(body)),
			}
			if msgs[i].MessageGroupID != nil {
				e.MessageGroupId = aws.String(*msgs[i].MessageGroupID)
			}
			entries = append(entries, e)
		}
		out, err := q.client.SendMessageBatch(ctx, &sqs.SendMessageBatchInput{
			QueueUrl: aws.String(q.queueURL),
			Entries:  entries,
		})
		if err != nil {
			return ids, fmt.Errorf("sqs SendMessageBatch: %w", err)
		}
		for _, r := range out.Successful {
			if r.MessageId != nil {
				ids = append(ids, *r.MessageId)
			}
		}
		if len(out.Failed) > 0 {
			return ids, fmt.Errorf("sqs SendMessageBatch: %d failures", len(out.Failed))
		}
	}
	return ids, nil
}

// Healthy reports running state.
func (q *Queue) Healthy() bool { return q.running.Load() }

// Stop marks the consumer stopped.
func (q *Queue) Stop() { q.running.Store(false) }

// Metrics calls GetQueueAttributes for pending + in-flight counts.
func (q *Queue) Metrics(ctx context.Context) (*queue.Metrics, error) {
	out, err := q.client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String(q.queueURL),
		AttributeNames: []sqstypes.QueueAttributeName{
			sqstypes.QueueAttributeNameApproximateNumberOfMessages,
			sqstypes.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("sqs GetQueueAttributes: %w", err)
	}
	parse := func(k sqstypes.QueueAttributeName) uint64 {
		s, ok := out.Attributes[string(k)]
		if !ok {
			return 0
		}
		v, _ := strconv.ParseUint(s, 10, 64)
		return v
	}
	return &queue.Metrics{
		QueueIdentifier:  q.queueName,
		PendingMessages:  parse(sqstypes.QueueAttributeNameApproximateNumberOfMessages),
		InFlightMessages: parse(sqstypes.QueueAttributeNameApproximateNumberOfMessagesNotVisible),
		TotalPolled:      q.polled.Load(),
		TotalAcked:       q.acked.Load(),
		TotalNacked:      q.nacked.Load(),
		TotalDeferred:    q.deferred.Load(),
	}, nil
}

// Counters returns process-local counters only.
func (q *Queue) Counters() *queue.Metrics {
	return &queue.Metrics{
		QueueIdentifier: q.queueName,
		TotalPolled:     q.polled.Load(),
		TotalAcked:      q.acked.Load(),
		TotalNacked:     q.nacked.Load(),
		TotalDeferred:   q.deferred.Load(),
	}
}

// evictExpiredPendingDeletesLocked prunes old entries. Holds the lock.
func (q *Queue) evictExpiredPendingDeletesLocked() {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now()
	for id, ts := range q.pendingDelete {
		if now.Sub(ts) > PendingDeleteTTL {
			delete(q.pendingDelete, id)
		}
	}
	if len(q.receiptToMessageID) > 1000 {
		for h, m := range q.receiptToMessageID {
			if now.Sub(m.At) > PendingDeleteTTL {
				delete(q.receiptToMessageID, h)
			}
		}
	}
}
