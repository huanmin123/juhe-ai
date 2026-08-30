// Package gatewaydispatch owns one Go-side upstream attempt. It is transport
// focused: routing, credential lookup, account policy and response framing are
// injected interfaces, so this package never calls Node or another process.
package gatewaydispatch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	keymodelruntime "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/key_model_runtime"
)

var (
	ErrClientRequired        = errors.New("gateway dispatch HTTP client is required")
	ErrAttemptBlocked        = errors.New("gateway dispatch key-model admission blocked")
	ErrAccountCircuitBlocked = errors.New("gateway dispatch account circuit admission blocked")
	ErrAttemptSettled        = errors.New("gateway dispatch attempt already settled")
)

type Client interface {
	Do(*http.Request) (*http.Response, error)
}

type KeyModelGate interface {
	AdmitForeground(context.Context, keymodelruntime.Capability, string) (keymodelruntime.ForegroundDecision, keymodelruntime.ForegroundPermit, uint64, error)
	ReleaseForeground(context.Context, keymodelruntime.ForegroundPermit) (bool, error)
	RenewForeground(context.Context, keymodelruntime.ForegroundPermit) (keymodelruntime.ForegroundPermit, bool, error)
	RecordFailureIntent(context.Context, keymodelruntime.FailureIntent) (keymodelruntime.MutationStatus, keymodelruntime.State, error)
}

// AccountCircuitGate is the in-process account/protocol-model circuit owner.
// Implementations may use the Redis runtime directly; this package deliberately
// does not know about storage, routing, credentials, or Node bridges.
type AccountCircuitGate interface {
	Prepare(context.Context, AccountCircuitInput) (AccountCircuitDecision, AccountCircuitAttempt, error)
}

type AccountCircuitInput struct {
	AccountID                 string
	RequestLane               string
	Model                     string
	DispatchRevision          int64
	ConfirmationLeaseDuration time.Duration
	ConfirmationEligible      bool
	FailureEvidenceKey        string
}

type AccountCircuitDecision string

const (
	AccountCircuitDispatchable AccountCircuitDecision = "dispatchable"
	AccountCircuitBlocked      AccountCircuitDecision = "blocked"
)

// AccountCircuitAttempt owns exactly one prepared circuit attempt. The first
// terminal report wins; implementations must fence late reports by generation,
// revision, and lease identity.
type AccountCircuitAttempt interface {
	ReportFramingComplete(context.Context) error
	ReportTransportFailure(context.Context, error) error
	ReportUnknown(context.Context) error
}

type Request struct {
	HTTP *http.Request
	// Client optionally overrides Dispatcher.Client for this single attempt.
	// The resolved model-check target supplies this when it has a scoped proxy.
	Client         Client
	Capability     keymodelruntime.Capability
	AttemptID      string
	AccountCircuit *AccountCircuitInput
}

type Result struct {
	Response *http.Response
	Permit   keymodelruntime.ForegroundPermit
	Attempt  *Attempt
}

type Attempt struct {
	gate           KeyModelGate
	permit         keymodelruntime.ForegroundPermit
	cap            keymodelruntime.Capability
	attemptID      string
	circuit        AccountCircuitAttempt
	settled        bool
	circuitSettled bool
}

func (a *Attempt) Permit() keymodelruntime.ForegroundPermit { return a.permit }

// Renew is intentionally explicit so callers can tie it to a request context
// and stop renewing immediately when the upstream attempt ends.
func (a *Attempt) Renew(ctx context.Context) (bool, error) {
	if a == nil || a.gate == nil || a.settled {
		return false, ErrAttemptSettled
	}
	permit, ok, err := a.gate.RenewForeground(ctx, a.permit)
	if err != nil || !ok {
		return ok, err
	}
	a.permit = permit
	return true, nil
}

func (a *Attempt) Release(ctx context.Context) error {
	if a == nil || a.gate == nil {
		return nil
	}
	if a.settled {
		return nil
	}
	a.settled = true
	_, err := a.gate.ReleaseForeground(ctx, a.permit)
	return err
}

func (a *Attempt) ReportFramingComplete(ctx context.Context) error {
	if a == nil || a.circuit == nil || a.circuitSettled {
		return nil
	}
	a.circuitSettled = true
	return a.circuit.ReportFramingComplete(ctx)
}

// CompleteSuccess records a complete upstream frame and releases the
// foreground key-model permit. Both operations are attempted even when one
// fails so a transient circuit-store error cannot strand the permit.
func (a *Attempt) CompleteSuccess(ctx context.Context) error {
	if a == nil {
		return nil
	}
	var errs []error
	if err := a.ReportFramingComplete(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := a.Release(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (a *Attempt) ReportTransportFailure(ctx context.Context, cause error) error {
	if a == nil || a.circuit == nil || a.circuitSettled {
		return nil
	}
	a.circuitSettled = true
	return a.circuit.ReportTransportFailure(ctx, cause)
}

func (a *Attempt) ReportUnknown(ctx context.Context) error {
	if a == nil || a.circuit == nil || a.circuitSettled {
		return nil
	}
	a.circuitSettled = true
	return a.circuit.ReportUnknown(ctx)
}

func (a *Attempt) Unknown(ctx context.Context, now time.Time, requestID string) error {
	if a == nil || a.gate == nil {
		return nil
	}
	if a.settled {
		return ErrAttemptSettled
	}
	a.settled = true
	_, _, err := a.gate.RecordFailureIntent(ctx, keymodelruntime.FailureIntent{
		IntentID: requestID, RequestID: requestID, AttemptID: a.attemptID,
		Capability: a.cap, ObservedAt: now, Permit: &a.permit,
	})
	return err
}

type Dispatcher struct {
	Client   Client
	KeyModel KeyModelGate
	Circuit  AccountCircuitGate
}

func (d Dispatcher) Dispatch(ctx context.Context, input Request) (Result, error) {
	client := input.Client
	if client == nil {
		client = d.Client
	}
	if client == nil {
		return Result{}, ErrClientRequired
	}
	if input.HTTP == nil || input.HTTP.Context() == nil {
		return Result{}, errors.New("gateway dispatch request is required")
	}
	if strings.TrimSpace(input.AttemptID) == "" {
		return Result{}, errors.New("gateway dispatch attempt id is required")
	}
	if d.KeyModel == nil {
		return Result{}, errors.New("gateway dispatch key-model gate is required")
	}
	var circuitAttempt AccountCircuitAttempt
	if d.Circuit != nil {
		if input.AccountCircuit == nil {
			return Result{}, errors.New("gateway dispatch account circuit input is required")
		}
		decision, prepared, err := d.Circuit.Prepare(ctx, *input.AccountCircuit)
		if err != nil {
			return Result{}, fmt.Errorf("prepare account circuit attempt: %w", err)
		}
		if decision != AccountCircuitDispatchable {
			return Result{}, ErrAccountCircuitBlocked
		}
		if prepared == nil {
			return Result{}, errors.New("account circuit dispatchable decision has no attempt")
		}
		circuitAttempt = prepared
	}
	decision, permit, _, err := d.KeyModel.AdmitForeground(ctx, input.Capability, input.AttemptID)
	if err != nil {
		_ = circuitAttempt.ReportUnknown(context.WithoutCancel(ctx))
		return Result{}, fmt.Errorf("admit key-model foreground: %w", err)
	}
	if decision != keymodelruntime.ForegroundAdmitted {
		_ = circuitAttempt.ReportUnknown(context.WithoutCancel(ctx))
		return Result{}, ErrAttemptBlocked
	}
	attempt := &Attempt{gate: d.KeyModel, permit: permit, cap: input.Capability, attemptID: input.AttemptID, circuit: circuitAttempt}
	response, err := client.Do(input.HTTP.WithContext(ctx))
	if err != nil {
		_ = attempt.Unknown(context.WithoutCancel(ctx), time.Now().UTC(), input.AttemptID)
		_ = attempt.ReportTransportFailure(context.WithoutCancel(ctx), err)
		return Result{Attempt: attempt, Permit: permit}, fmt.Errorf("gateway upstream transport: %w", err)
	}
	if response == nil || response.Body == nil {
		_ = attempt.Unknown(context.WithoutCancel(ctx), time.Now().UTC(), input.AttemptID)
		_ = attempt.ReportUnknown(context.WithoutCancel(ctx))
		return Result{Attempt: attempt, Response: response, Permit: permit}, errors.New("gateway upstream response/body is missing")
	}
	return Result{Attempt: attempt, Response: response, Permit: permit}, nil
}

// CloseResponse releases the foreground permit after the caller has consumed
// the body. It preserves body close errors and permit release errors.
func (r Result) CloseResponse() error {
	var errs []error
	if r.Response != nil && r.Response.Body != nil {
		if err := r.Response.Body.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if r.Attempt != nil {
		if err := r.Attempt.Release(context.Background()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ReportFramingComplete settles both circuit and key-model state after the
// caller has consumed a complete upstream frame. It does not close the body;
// callers may still need the body for response transformation.
func (r Result) ReportFramingComplete(ctx context.Context) error {
	if r.Attempt == nil {
		return nil
	}
	return r.Attempt.ReportFramingComplete(ctx)
}

func (r Result) CompleteSuccess(ctx context.Context) error {
	if r.Attempt == nil {
		return nil
	}
	return r.Attempt.CompleteSuccess(ctx)
}

func (r Result) ReportTransportFailure(ctx context.Context, cause error) error {
	if r.Attempt == nil {
		return nil
	}
	return r.Attempt.ReportTransportFailure(ctx, cause)
}

func (r Result) ReportUnknown(ctx context.Context) error {
	if r.Attempt == nil {
		return nil
	}
	return r.Attempt.ReportUnknown(ctx)
}

func ReadBody(response *http.Response, limit int64) ([]byte, error) {
	if response == nil || response.Body == nil {
		return nil, errors.New("gateway response body is missing")
	}
	if limit <= 0 {
		limit = 64 << 20
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, limit+1))
	closeErr := response.Body.Close()
	if int64(len(data)) > limit {
		return nil, errors.Join(fmt.Errorf("gateway response body exceeds %d bytes", limit), closeErr)
	}
	return data, errors.Join(readErr, closeErr)
}
