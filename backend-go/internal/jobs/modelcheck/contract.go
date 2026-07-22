package modelcheck

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
)

const (
	TaskTypeRun       = "model-check:run"
	PayloadVersionV1  = 1
	QueueName         = "model-check"
	DefaultMaxRetry   = 3
	DeadlinePolicyV1  = "probe-plan-budget-v1"
	LeaseFencingV1    = "claim-epoch-v1"
	EnqueueHandoffV1  = "transactional-outbox-v1"
	WorkerFinishCASV1 = "running-active-claim-no-stop-v1"
	StopCASV1         = "intent-first-cancel-v1"

	MaxIdentifierBytes = 200
	MaxModelBytes      = 200
	MaxTraceIDBytes    = 200
	MaxPayloadBytes    = 8 * 1024
)

var ErrInvalidPayload = errors.New("invalid model-check task payload")

type RunStatus string

const (
	RunStatusRunning   RunStatus = "running"
	RunStatusCompleted RunStatus = "completed"
	RunStatusFailed    RunStatus = "failed"
	RunStatusCanceled  RunStatus = "canceled"
)

type TransitionDecision string

const (
	TransitionApply  TransitionDecision = "apply"
	TransitionNoop   TransitionDecision = "noop"
	TransitionReject TransitionDecision = "reject"
)

const (
	TargetTypeAccount = "account"
	ProfileFull       = "full"
)

// RunTaskPayload contains stable identifiers only. Credentials, upstream URLs,
// proxy secrets, and request/response bodies are resolved inside the worker.
type RunTaskPayload struct {
	Version                         int       `json:"version"`
	RunID                           string    `json:"runId"`
	SystemAccountID                 string    `json:"systemAccountId"`
	ActorSystemAccountID            string    `json:"actorSystemAccountId"`
	TargetType                      string    `json:"targetType"`
	TargetID                        string    `json:"targetId"`
	TargetConfigRevision            int       `json:"targetConfigRevision"`
	Model                           string    `json:"model"`
	Profile                         string    `json:"profile"`
	ProbeSetVersion                 string    `json:"probeSetVersion"`
	TrustedComparison               bool      `json:"trustedComparison"`
	TrustedComparisonAccountID      string    `json:"trustedComparisonAccountId,omitempty"`
	TrustedComparisonConfigRevision int       `json:"trustedComparisonConfigRevision,omitempty"`
	TraceID                         string    `json:"traceId"`
	RequestedAt                     time.Time `json:"requestedAt"`
}

func ValidateRunTaskPayload(payload RunTaskPayload) error {
	if payload.Version != PayloadVersionV1 {
		return fmt.Errorf("unsupported model-check payload version %d", payload.Version)
	}
	if err := validateRequiredBounded("run id", payload.RunID, MaxIdentifierBytes); err != nil {
		return err
	}
	if err := validateRequiredBounded("system account id", payload.SystemAccountID, MaxIdentifierBytes); err != nil {
		return err
	}
	if err := validateRequiredBounded("actor system account id", payload.ActorSystemAccountID, MaxIdentifierBytes); err != nil {
		return err
	}
	if payload.TargetType != TargetTypeAccount {
		return fmt.Errorf("target type must be %q", TargetTypeAccount)
	}
	if err := validateRequiredBounded("target id", payload.TargetID, MaxIdentifierBytes); err != nil {
		return err
	}
	if payload.TargetConfigRevision < 1 {
		return fmt.Errorf("target config revision must be positive")
	}
	if err := validateRequiredBounded("model", payload.Model, MaxModelBytes); err != nil {
		return err
	}
	if payload.Profile != ProfileFull {
		return fmt.Errorf("profile must be %q", ProfileFull)
	}
	if err := validateRequiredBounded("probe set version", payload.ProbeSetVersion, MaxIdentifierBytes); err != nil {
		return err
	}
	if payload.TrustedComparison {
		if err := validateRequiredBounded("trusted comparison account id", payload.TrustedComparisonAccountID, MaxIdentifierBytes); err != nil {
			return err
		}
		if strings.TrimSpace(payload.TrustedComparisonAccountID) == strings.TrimSpace(payload.TargetID) {
			return fmt.Errorf("trusted comparison account id must differ from target id")
		}
		if payload.TrustedComparisonConfigRevision < 1 {
			return fmt.Errorf("trusted comparison config revision must be positive")
		}
	} else if strings.TrimSpace(payload.TrustedComparisonAccountID) != "" || payload.TrustedComparisonConfigRevision != 0 {
		return fmt.Errorf("trusted comparison account id requires trusted comparison")
	}
	if err := validateRequiredBounded("trace id", payload.TraceID, MaxTraceIDBytes); err != nil {
		return err
	}
	if payload.RequestedAt.IsZero() {
		return fmt.Errorf("requested at is required")
	}
	return nil
}

func NormalizeRunTaskPayload(payload RunTaskPayload) (RunTaskPayload, error) {
	payload.RunID = strings.TrimSpace(payload.RunID)
	payload.SystemAccountID = strings.TrimSpace(payload.SystemAccountID)
	payload.ActorSystemAccountID = strings.TrimSpace(payload.ActorSystemAccountID)
	payload.TargetType = strings.TrimSpace(payload.TargetType)
	payload.TargetID = strings.TrimSpace(payload.TargetID)
	payload.Model = strings.TrimSpace(payload.Model)
	payload.Profile = strings.TrimSpace(payload.Profile)
	payload.ProbeSetVersion = strings.TrimSpace(payload.ProbeSetVersion)
	payload.TrustedComparisonAccountID = strings.TrimSpace(payload.TrustedComparisonAccountID)
	payload.TraceID = strings.TrimSpace(payload.TraceID)
	if !payload.RequestedAt.IsZero() {
		payload.RequestedAt = payload.RequestedAt.UTC()
	}
	if err := ValidateRunTaskPayload(payload); err != nil {
		return RunTaskPayload{}, err
	}
	return payload, nil
}

func EncodeRunTaskPayload(payload RunTaskPayload) ([]byte, error) {
	normalized, err := NormalizeRunTaskPayload(payload)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("%w: encode payload: %v", ErrInvalidPayload, err)
	}
	if len(encoded) > MaxPayloadBytes {
		return nil, fmt.Errorf("%w: payload exceeds %d bytes", ErrInvalidPayload, MaxPayloadBytes)
	}
	return encoded, nil
}

func DecodeRunTaskPayload(data []byte) (RunTaskPayload, error) {
	if len(data) == 0 || len(data) > MaxPayloadBytes {
		return RunTaskPayload{}, fmt.Errorf("%w: payload size must be between 1 and %d bytes", ErrInvalidPayload, MaxPayloadBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var payload RunTaskPayload
	if err := decoder.Decode(&payload); err != nil {
		return RunTaskPayload{}, fmt.Errorf("%w: decode payload: %v", ErrInvalidPayload, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return RunTaskPayload{}, fmt.Errorf("%w: trailing payload data", ErrInvalidPayload)
	}
	normalized, err := NormalizeRunTaskPayload(payload)
	if err != nil {
		return RunTaskPayload{}, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	return normalized, nil
}

func UniqueKey(payload RunTaskPayload) (string, error) {
	normalized, err := NormalizeRunTaskPayload(payload)
	if err != nil {
		return "", err
	}
	return TaskTypeRun + ":" + normalized.RunID, nil
}

// RequestFingerprint binds a durable run ID to its immutable request facts.
// It is safe to persist because it is derived only from non-secret identifiers.
func RequestFingerprint(payload RunTaskPayload) (string, error) {
	normalized, err := NormalizeRunTaskPayload(payload)
	if err != nil {
		return "", err
	}
	canonical, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("marshal model-check request fingerprint: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func IsTerminal(status RunStatus) bool {
	switch status {
	case RunStatusCompleted, RunStatusFailed, RunStatusCanceled:
		return true
	default:
		return false
	}
}

// DecideTransition distinguishes a real terminal update from an idempotent
// replay. Callers must not rewrite score, message, or summaries for no-op.
func DecideTransition(from, to RunStatus) TransitionDecision {
	if from == to {
		if from == RunStatusRunning || IsTerminal(from) {
			return TransitionNoop
		}
		return TransitionReject
	}
	if from == RunStatusRunning && IsTerminal(to) {
		return TransitionApply
	}
	return TransitionReject
}

func validateRequiredBounded(name, value string, maxBytes int) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fmt.Errorf("%s is required", name)
	}
	if value != trimmed {
		return fmt.Errorf("%s must not contain surrounding whitespace", name)
	}
	if len(value) > maxBytes {
		return fmt.Errorf("%s exceeds %d bytes", name, maxBytes)
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return fmt.Errorf("%s must not contain control characters", name)
	}
	return nil
}
