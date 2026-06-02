// Package common is the Go port of the Rust fc-common crate. It holds
// types shared across the router, queue, stream, outbox, and platform
// packages: messages, dispatch modes, mediation results, outbox status,
// configuration shapes, and warnings.
//
// JSON tags match the Rust serde posture exactly (camelCase, omitempty
// for Option<T>, SCREAMING_SNAKE_CASE for enums) so wire format is
// byte-compatible.
package common

import "time"

// MediationType is the kind of mediation (currently only HTTP).
type MediationType string

const (
	MediationTypeHTTP MediationType = "HTTP"
)

// DispatchMode controls ordering behavior within a message group.
// Shared across platform, scheduler, and router.
type DispatchMode string

const (
	DispatchImmediate    DispatchMode = "IMMEDIATE"
	DispatchNextOnError  DispatchMode = "NEXT_ON_ERROR"
	DispatchBlockOnError DispatchMode = "BLOCK_ON_ERROR"
)

// ParseDispatchMode is the lenient parser matching Rust's from_str:
// unknown input maps to Immediate.
func ParseDispatchMode(s string) DispatchMode {
	switch s {
	case "NEXT_ON_ERROR":
		return DispatchNextOnError
	case "BLOCK_ON_ERROR":
		return DispatchBlockOnError
	default:
		return DispatchImmediate
	}
}

// RequiresOrdering reports whether the mode demands FIFO processing.
func (d DispatchMode) RequiresOrdering() bool {
	return d == DispatchNextOnError || d == DispatchBlockOnError
}

// Message is the core message structure that flows through the system.
// Compatible with Java's MessagePointer.
type Message struct {
	ID              string        `json:"id"`
	PoolCode        string        `json:"poolCode,omitempty"`
	AuthToken       *string       `json:"authToken,omitempty"`
	SigningSecret   *string       `json:"signingSecret,omitempty"`
	MediationType   MediationType `json:"mediationType"`
	MediationTarget string        `json:"mediationTarget"`
	MessageGroupID  *string       `json:"messageGroupId,omitempty"`
	HighPriority    bool          `json:"highPriority,omitempty"`
	DispatchMode    DispatchMode  `json:"dispatchMode,omitempty"`
}

// QueuedMessage is a Message received from a queue with broker tracking.
type QueuedMessage struct {
	Message         Message
	ReceiptHandle   string
	BrokerMessageID string // empty if not provided
	QueueIdentifier string
	// BatchID is a router-assigned grouping over messages received in the
	// same poll batch (Rust BatchMessage.batch_id). It is set by the pool's
	// poll loop, not the broker, and is informational only.
	BatchID string
	// Attempts counts how many in-pipeline mediation attempts this delivery
	// has already had (0 on first dispatch). The pool increments it on each
	// retry so it can recognise a re-dispatch (skip re-tracking) and grow the
	// backoff. Internal-only; never crosses the wire.
	Attempts uint
}

// InFlightMessage tracks a message currently being processed.
type InFlightMessage struct {
	MessageID       string
	BrokerMessageID string
	PoolCode        string
	QueueIdentifier string
	StartedAt       time.Time
	MessageGroupID  string
	BatchID         string
	ReceiptHandle   string
	// Attempts is >0 once the message has failed at least once and is being
	// retried in-pipeline. The stall detector and the in-flight reaper skip
	// entries with Attempts>0 — they are legitimately retrying, not stuck.
	Attempts uint
}

// NewInFlightMessage constructs a tracker.
func NewInFlightMessage(m *Message, brokerID, queueID, batchID, receipt string) *InFlightMessage {
	groupID := ""
	if m.MessageGroupID != nil {
		groupID = *m.MessageGroupID
	}
	return &InFlightMessage{
		MessageID:       m.ID,
		BrokerMessageID: brokerID,
		PoolCode:        m.PoolCode,
		QueueIdentifier: queueID,
		StartedAt:       time.Now(),
		MessageGroupID:  groupID,
		BatchID:         batchID,
		ReceiptHandle:   receipt,
	}
}

// ElapsedSeconds returns how long the message has been in flight.
func (m *InFlightMessage) ElapsedSeconds() int64 {
	return int64(time.Since(m.StartedAt).Seconds())
}

// UpdateReceiptHandle replaces the receipt handle on broker redelivery.
func (m *InFlightMessage) UpdateReceiptHandle(h string) { m.ReceiptHandle = h }
