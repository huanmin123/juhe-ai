package accountbalanceautodetect

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

const detectionInterval = 5 * time.Minute

type Result string

const (
	ResultEnabled     Result = "enabled"
	ResultUnsupported Result = "unsupported"
	ResultStale       Result = "stale"
)

type Input struct {
	AccountID      string
	ConfigRevision int
}

type Snapshot struct {
	Status                       string `json:"status"`
	RemainingUSD                 string `json:"remainingUsd,omitempty"`
	RawRemaining                 string `json:"rawRemaining,omitempty"`
	RawUnit                      string `json:"rawUnit,omitempty"`
	Basis                        string `json:"basis,omitempty"`
	ErrorMessage                 string `json:"errorMessage,omitempty"`
	LastAttemptAt                string `json:"lastAttemptAt,omitempty"`
	LastSuccessAt                string `json:"lastSuccessAt,omitempty"`
	ConsecutiveTransientFailures int    `json:"consecutiveTransientFailures,omitempty"`
	LastTransientErrorMessage    string `json:"lastTransientErrorMessage,omitempty"`
	LastTransientFailureAt       string `json:"lastTransientFailureAt,omitempty"`
}

type ProbeCandidate struct {
	AccountID       string
	SystemAccountID string
	ConfigRevision  int
	Credentials     map[string]any
	ProxyProfileID  string
}

type ProbeResult struct {
	Supported bool
	Adapter   string
	Snapshot  Snapshot
}

type BuiltinDetector interface {
	DetectBuiltin(ctx context.Context, candidate ProbeCandidate) (ProbeResult, error)
}

type CredentialCodec interface {
	DecryptJSON(value string) (map[string]any, error)
}

type ServiceOptions struct {
	Store    port.AccountBalanceAutoDetectStore
	Codec    CredentialCodec
	Detector BuiltinDetector
	Now      func() time.Time
}

type Service struct {
	store    port.AccountBalanceAutoDetectStore
	codec    CredentialCodec
	detector BuiltinDetector
	now      func() time.Time
}

func NewService(opts ServiceOptions) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{store: opts.Store, codec: opts.Codec, detector: opts.Detector, now: now}
}

func (s *Service) Run(ctx context.Context, input Input) (Result, error) {
	accountID := strings.TrimSpace(input.AccountID)
	if accountID == "" || input.ConfigRevision < 1 {
		return "", fmt.Errorf("account balance auto detect input is invalid")
	}
	if s.store == nil {
		return "", fmt.Errorf("account balance auto detect store is required")
	}
	candidate, found, err := s.store.LoadAccountBalanceAutoDetectCandidate(ctx, port.AccountBalanceAutoDetectLookup{
		AccountID: accountID, ConfigRevision: input.ConfigRevision,
	})
	if err != nil {
		return "", fmt.Errorf("load account balance auto detect candidate: %w", err)
	}
	if !found {
		return ResultStale, nil
	}
	if s.codec == nil {
		return "", fmt.Errorf("account balance auto detect credential codec is required")
	}
	credentials, err := s.codec.DecryptJSON(candidate.CredentialsEncrypted)
	if err != nil {
		return "", fmt.Errorf("decrypt account balance auto detect credentials: %w", err)
	}
	if len(effectiveAPIKeys(credentials)) != 1 {
		return ResultStale, nil
	}
	if s.detector == nil {
		return "", fmt.Errorf("account balance builtin detector is required")
	}
	detected, err := s.detector.DetectBuiltin(ctx, ProbeCandidate{
		AccountID: candidate.AccountID, SystemAccountID: candidate.SystemAccountID,
		ConfigRevision: candidate.ConfigRevision, Credentials: credentials, ProxyProfileID: candidate.ProxyProfileID,
	})
	if err != nil {
		return "", fmt.Errorf("detect builtin account balance adapter: %w", err)
	}
	if !detected.Supported || detected.Snapshot.Status == "unsupported" {
		return ResultUnsupported, nil
	}
	if strings.TrimSpace(detected.Adapter) == "" || (detected.Snapshot.Status != "fresh" && detected.Snapshot.Status != "unlimited") {
		return "", fmt.Errorf("builtin account balance detector returned an invalid successful result")
	}

	completedAt := s.now().UTC()
	completedAtText := completedAt.Format(time.RFC3339Nano)
	detected.Snapshot.LastAttemptAt = completedAtText
	detected.Snapshot.LastSuccessAt = completedAtText
	configJSON, err := json.Marshal(struct {
		Adapter                 string `json:"adapter"`
		IntervalMinutes         int    `json:"intervalMinutes"`
		PreferredBuiltinAdapter string `json:"preferredBuiltinAdapter"`
	}{Adapter: "builtin", IntervalMinutes: int(detectionInterval / time.Minute), PreferredBuiltinAdapter: detected.Adapter})
	if err != nil {
		return "", fmt.Errorf("encode detected account balance config: %w", err)
	}
	snapshotJSON, err := json.Marshal(detected.Snapshot)
	if err != nil {
		return "", fmt.Errorf("encode detected account balance snapshot: %w", err)
	}
	committed, err := s.store.CommitAccountBalanceAutoDetect(ctx, port.AccountBalanceAutoDetectCommit{
		AccountID: candidate.AccountID, SystemAccountID: candidate.SystemAccountID,
		ExpectedConfigRevision: candidate.ConfigRevision, ConfigJSON: string(configJSON),
		SnapshotStatus: detected.Snapshot.Status, SnapshotJSON: string(snapshotJSON),
		CompletedAt: completedAt, NextRefreshAt: completedAt.Add(detectionInterval),
	})
	if err != nil {
		return "", fmt.Errorf("commit account balance auto detect: %w", err)
	}
	if !committed {
		return ResultStale, nil
	}
	return ResultEnabled, nil
}

func effectiveAPIKeys(credentials map[string]any) []string {
	keys := make([]string, 0, 1)
	seen := map[string]struct{}{}
	if values, ok := credentials["api_keys"].([]any); ok {
		for _, value := range values {
			key, ok := value.(string)
			key = strings.TrimSpace(key)
			if !ok || key == "" {
				continue
			}
			if _, exists := seen[key]; !exists {
				seen[key] = struct{}{}
				keys = append(keys, key)
			}
		}
	}
	if len(keys) > 0 {
		return keys
	}
	if key, ok := credentials["api_key"].(string); ok {
		key = strings.TrimSpace(key)
		if key != "" {
			return []string{key}
		}
	}
	return nil
}
