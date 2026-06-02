package router

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
)

// TrafficConfig configures ALB target-group traffic management. When
// Enabled is false, the rest of the fields are ignored and the
// resulting TrafficStrategy is a no-op.
//
// Mirrors the Rust `traffic` module: on leader-gain, register this
// instance; on leader-loss, deregister so the ALB stops routing traffic
// to a standing-by node.
type TrafficConfig struct {
	Enabled        bool
	TargetGroupARN string
	// InstanceIP is the IP the ALB target group will route to (typically
	// the pod's pod-IP in EKS). If empty, the strategy is disabled with a
	// warning at startup — the SDK cannot infer it safely.
	InstanceIP string
	Port       int32
	Region     string
	// DeregistrationDelaySeconds bounds how long Deregister waits for the ALB
	// to finish draining in-flight connections before returning. Mirrors
	// Rust's deregistration_delay_seconds. Defaults to 300s (the ALB default)
	// when <= 0.
	DeregistrationDelaySeconds int64
}

// TrafficStrategy registers / deregisters this instance with an ALB
// target group. Methods are safe to call concurrently. Always present —
// when Enabled is false every method becomes a successful no-op so
// callers can wire it unconditionally.
type TrafficStrategy struct {
	cfg    TrafficConfig
	client *elasticloadbalancingv2.Client

	mu         sync.Mutex
	registered bool
	lastChange time.Time
	lastError  string
}

// NewTrafficStrategy builds a strategy. Returns an error only when the
// AWS SDK config fails to load — disabling via cfg.Enabled=false is the
// expected "no traffic management" path.
func NewTrafficStrategy(ctx context.Context, cfg TrafficConfig) (*TrafficStrategy, error) {
	s := &TrafficStrategy{cfg: cfg}
	if !cfg.Enabled {
		return s, nil
	}
	if cfg.TargetGroupARN == "" || cfg.InstanceIP == "" {
		slog.Warn("traffic strategy enabled but missing required fields — disabling",
			"target_group", cfg.TargetGroupARN != "", "instance_ip", cfg.InstanceIP != "")
		s.cfg.Enabled = false
		return s, nil
	}
	opts := []func(*awsconfig.LoadOptions) error{}
	if cfg.Region != "" {
		opts = append(opts, awsconfig.WithRegion(cfg.Region))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}
	s.client = elasticloadbalancingv2.NewFromConfig(awsCfg)
	return s, nil
}

// Register adds this instance to the target group. Idempotent: a second
// Register is a no-op. Returns nil when disabled.
func (s *TrafficStrategy) Register(ctx context.Context) error {
	if !s.cfg.Enabled {
		return nil
	}
	s.mu.Lock()
	if s.registered {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	target := elbv2types.TargetDescription{
		Id:   ptrStr(s.cfg.InstanceIP),
		Port: ptrInt32(s.cfg.Port),
	}
	_, err := s.client.RegisterTargets(ctx, &elasticloadbalancingv2.RegisterTargetsInput{
		TargetGroupArn: ptrStr(s.cfg.TargetGroupARN),
		Targets:        []elbv2types.TargetDescription{target},
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.lastError = err.Error()
		return fmt.Errorf("register targets: %w", err)
	}
	s.registered = true
	s.lastChange = time.Now()
	s.lastError = ""
	slog.Info("traffic: registered with target group",
		"target_group", s.cfg.TargetGroupARN, "ip", s.cfg.InstanceIP)
	return nil
}

// Deregister removes this instance from the target group. Idempotent.
// Returns nil when disabled.
func (s *TrafficStrategy) Deregister(ctx context.Context) error {
	if !s.cfg.Enabled {
		return nil
	}
	s.mu.Lock()
	if !s.registered {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	target := elbv2types.TargetDescription{
		Id:   ptrStr(s.cfg.InstanceIP),
		Port: ptrInt32(s.cfg.Port),
	}
	_, err := s.client.DeregisterTargets(ctx, &elasticloadbalancingv2.DeregisterTargetsInput{
		TargetGroupArn: ptrStr(s.cfg.TargetGroupARN),
		Targets:        []elbv2types.TargetDescription{target},
	})
	s.mu.Lock()
	if err != nil {
		s.lastError = err.Error()
		s.mu.Unlock()
		return fmt.Errorf("deregister targets: %w", err)
	}
	s.registered = false
	s.lastChange = time.Now()
	s.lastError = ""
	s.mu.Unlock()
	slog.Info("traffic: deregistered from target group, waiting for drain",
		"target_group", s.cfg.TargetGroupARN, "ip", s.cfg.InstanceIP)

	// Mirror Rust wait_for_deregistration: block until the ALB finishes
	// draining in-flight connections (or the delay/ctx elapses) so a
	// leader-loss / shutdown doesn't kill the process mid-drain. Best-effort
	// — a timeout or ctx cancellation just logs and proceeds.
	if waitErr := s.waitForDeregistration(ctx); waitErr != nil {
		slog.Warn("traffic: drain wait did not confirm completion", "err", waitErr)
	}
	return nil
}

// waitForDeregistration polls DescribeTargetHealth every 5s until this target
// is no longer in the "draining" state or DeregistrationDelaySeconds elapses.
// Mirrors crates/fc-router/src/traffic.rs wait_for_deregistration.
func (s *TrafficStrategy) waitForDeregistration(ctx context.Context) error {
	delay := s.cfg.DeregistrationDelaySeconds
	if delay <= 0 {
		delay = 300
	}
	const pollInterval = 5 * time.Second
	deadline := time.Now().Add(time.Duration(delay) * time.Second)
	target := elbv2types.TargetDescription{
		Id:   ptrStr(s.cfg.InstanceIP),
		Port: ptrInt32(s.cfg.Port),
	}
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("deregistration drain wait timed out after %ds", delay)
		}
		out, err := s.client.DescribeTargetHealth(ctx, &elasticloadbalancingv2.DescribeTargetHealthInput{
			TargetGroupArn: ptrStr(s.cfg.TargetGroupARN),
			Targets:        []elbv2types.TargetDescription{target},
		})
		if err != nil {
			return fmt.Errorf("describe target health: %w", err)
		}
		draining := false
		for _, d := range out.TargetHealthDescriptions {
			if d.TargetHealth != nil && d.TargetHealth.State == elbv2types.TargetHealthStateEnumDraining {
				draining = true
				break
			}
		}
		if !draining {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// Status is the snapshot used by /monitoring/traffic-status.
type TrafficStatus struct {
	Enabled        bool
	Mode           string
	TargetGroupARN string
	Registered     bool
	LastChangedAt  time.Time
	LastError      string
}

// Status returns the current state. Cheap; only reads locked fields.
func (s *TrafficStrategy) Status() TrafficStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	mode := "alb-target-group"
	if !s.cfg.Enabled {
		mode = "disabled"
	}
	return TrafficStatus{
		Enabled:        s.cfg.Enabled,
		Mode:           mode,
		TargetGroupARN: s.cfg.TargetGroupARN,
		Registered:     s.registered,
		LastChangedAt:  s.lastChange,
		LastError:      s.lastError,
	}
}

// ErrTrafficDisabled is returned when callers ask the strategy to do
// something but it isn't configured — exposed for tests; production
// callers should just treat the no-op as success.
var ErrTrafficDisabled = errors.New("traffic strategy disabled")

func ptrStr(s string) *string { return &s }
func ptrInt32(n int32) *int32 { return &n }
