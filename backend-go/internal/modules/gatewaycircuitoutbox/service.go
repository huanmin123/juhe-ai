package gatewaycircuitoutbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	DefaultBatchLimit = 100
	DefaultLease      = 30 * time.Second
	DefaultRetryDelay = time.Second
)

type RunOnceInput struct {
	OwnerID    string
	Now        time.Time
	Lease      time.Duration
	RetryDelay time.Duration
	Limit      int
}

type RunOnceResult struct {
	Claimed      int
	Projected    int
	Applied      int
	Idempotent   int
	Stale        int
	Acknowledged int
	Released     int
	LeaseLost    int
	Failed       int
}

type Service struct {
	store     port.GatewayAccountCircuitOutboxStore
	projector port.GatewayAccountCircuitRevisionProjector
	now       func() time.Time
}

func NewService(store port.GatewayAccountCircuitOutboxStore, projector port.GatewayAccountCircuitRevisionProjector) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("gateway account circuit outbox store is required")
	}
	if projector == nil {
		return nil, fmt.Errorf("gateway account circuit revision projector is required")
	}
	return &Service{store: store, projector: projector, now: time.Now}, nil
}

func (s *Service) WithNow(now func() time.Time) *Service {
	if now != nil {
		s.now = now
	}
	return s
}

func (s *Service) RunOnce(ctx context.Context, input RunOnceInput) (RunOnceResult, error) {
	if ctx == nil {
		return RunOnceResult{}, fmt.Errorf("gateway account circuit outbox context is required")
	}
	if err := normalizeRunOnceInput(&input, s.now); err != nil {
		return RunOnceResult{}, err
	}
	events, err := s.store.ClaimGatewayAccountCircuitOutbox(ctx, port.GatewayAccountCircuitOutboxClaimInput{
		OwnerID: input.OwnerID, Now: input.Now, Lease: input.Lease, Limit: input.Limit,
	})
	if err != nil {
		return RunOnceResult{}, fmt.Errorf("claim gateway account circuit outbox: %w", err)
	}
	result := RunOnceResult{Claimed: len(events)}
	var batchErr error
	for _, event := range events {
		if err := validateClaimedEvent(event); err != nil {
			result.Failed++
			batchErr = errors.Join(batchErr, releaseAfterFailure(ctx, s.store, event, s.now().UTC(), input.RetryDelay, "invalid_event", &result, err))
			continue
		}
		projection, projectErr := s.projector.ProjectGatewayAccountCircuitRevision(ctx, event)
		if projectErr != nil {
			result.Failed++
			batchErr = errors.Join(batchErr, releaseAfterFailure(ctx, s.store, event, s.now().UTC(), input.RetryDelay, "projection_failed", &result, projectErr))
			continue
		}
		if err := validateProjection(projection); err != nil {
			result.Failed++
			batchErr = errors.Join(batchErr, releaseAfterFailure(ctx, s.store, event, s.now().UTC(), input.RetryDelay, "invalid_projection", &result, err))
			continue
		}
		result.Projected++
		switch projection.Status {
		case port.GatewayAccountCircuitRevisionApplied:
			result.Applied++
		case port.GatewayAccountCircuitRevisionIdempotent:
			result.Idempotent++
		case port.GatewayAccountCircuitRevisionStale:
			result.Stale++
		}
		acknowledged, ackErr := s.store.AcknowledgeGatewayAccountCircuitOutbox(ctx, port.GatewayAccountCircuitOutboxAcknowledgeInput{
			EventID: event.EventID, ProjectionKey: event.ProjectionKey, ClaimToken: event.ClaimToken, AcknowledgedAt: s.now().UTC(),
		})
		if ackErr != nil {
			result.Failed++
			batchErr = errors.Join(batchErr, releaseAfterFailure(ctx, s.store, event, s.now().UTC(), input.RetryDelay, "ack_failed", &result, ackErr))
			continue
		}
		if !acknowledged {
			result.LeaseLost++
			continue
		}
		result.Acknowledged++
	}
	return result, batchErr
}

func releaseAfterFailure(ctx context.Context, store port.GatewayAccountCircuitOutboxStore, event port.GatewayAccountCircuitOutboxEvent, failedAt time.Time, retryDelay time.Duration, errorClass string, result *RunOnceResult, cause error) error {
	released, err := store.ReleaseGatewayAccountCircuitOutbox(ctx, port.GatewayAccountCircuitOutboxReleaseInput{
		EventID: event.EventID, ClaimToken: event.ClaimToken, ErrorClass: errorClass,
		Now: failedAt, RetryDelay: retryDelay,
	})
	if err != nil {
		return errors.Join(cause, fmt.Errorf("release gateway account circuit outbox: %w", err))
	}
	if released {
		result.Released++
	} else {
		result.LeaseLost++
	}
	return cause
}

func ValidateRunOnceInput(input RunOnceInput) error {
	return normalizeRunOnceInput(&input, time.Now)
}

func normalizeRunOnceInput(input *RunOnceInput, now func() time.Time) error {
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	if input.OwnerID == "" || len(input.OwnerID) > 128 {
		return fmt.Errorf("gateway account circuit outbox owner id is invalid")
	}
	for _, char := range input.OwnerID {
		if unicode.IsControl(char) {
			return fmt.Errorf("gateway account circuit outbox owner id contains control character")
		}
	}
	if input.Now.IsZero() {
		input.Now = now().UTC()
	} else {
		input.Now = input.Now.UTC()
	}
	if input.Lease == 0 {
		input.Lease = DefaultLease
	}
	if input.Lease <= 0 || input.Lease > time.Hour {
		return fmt.Errorf("gateway account circuit outbox lease is invalid")
	}
	if input.RetryDelay == 0 {
		input.RetryDelay = DefaultRetryDelay
	}
	if input.RetryDelay < 0 || input.RetryDelay > 24*time.Hour {
		return fmt.Errorf("gateway account circuit outbox retry delay is invalid")
	}
	if input.Limit == 0 {
		input.Limit = DefaultBatchLimit
	}
	if input.Limit < 1 || input.Limit > port.GatewayAccountCircuitOutboxMaxBatch {
		return fmt.Errorf("gateway account circuit outbox limit is invalid")
	}
	return nil
}

func validateClaimedEvent(event port.GatewayAccountCircuitOutboxEvent) error {
	if strings.TrimSpace(event.EventID) == "" || event.ProjectionKey != port.GatewayAccountCircuitProjectionKey || strings.TrimSpace(event.AccountID) == "" || event.AccountRuntimeKey != event.AccountID || strings.TrimSpace(event.TransitionID) == "" || strings.TrimSpace(event.ClaimToken) == "" || event.DispatchRevision < 1 || event.AttemptCount < 1 {
		return fmt.Errorf("claimed gateway account circuit outbox event is invalid")
	}
	return nil
}

func validateProjection(value port.GatewayAccountCircuitRevisionProjection) error {
	if value.CurrentRevision < 1 || value.ClosedStates < 0 {
		return fmt.Errorf("gateway account circuit revision projection is invalid")
	}
	switch value.Status {
	case port.GatewayAccountCircuitRevisionApplied, port.GatewayAccountCircuitRevisionIdempotent, port.GatewayAccountCircuitRevisionStale:
		return nil
	default:
		return fmt.Errorf("gateway account circuit revision projection status is invalid")
	}
}
