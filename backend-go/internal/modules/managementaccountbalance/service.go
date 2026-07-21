package managementaccountbalance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

var (
	ErrBalanceQueryMissing = errors.New("management account balance query is not configured")
)

type Snapshot struct {
	Status        string `json:"status"`
	Balance       string `json:"balance,omitempty"`
	Currency      string `json:"currency,omitempty"`
	Unit          string `json:"unit,omitempty"`
	Used          string `json:"used,omitempty"`
	Total         string `json:"total,omitempty"`
	ErrorMessage  string `json:"errorMessage,omitempty"`
	LastAttemptAt string `json:"lastAttemptAt,omitempty"`
	LastSuccessAt string `json:"lastSuccessAt,omitempty"`
}

type Input struct {
	AccountID       string
	SystemAccountID string
}

type Query func(context.Context, port.ManagementAccountBalanceCandidate) (Snapshot, error)

type ServiceOptions struct {
	Reader port.ManagementAccountBalanceReader
	Writer port.ManagementAccountBalanceWriter
	Now    func() time.Time
	Query  Query
}

type Service struct {
	reader port.ManagementAccountBalanceReader
	writer port.ManagementAccountBalanceWriter
	now    func() time.Time
	query  Query
}

func NewService(opts ServiceOptions) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{reader: opts.Reader, writer: opts.Writer, now: now, query: opts.Query}
}

func (s *Service) Get(ctx context.Context, input Input) (Snapshot, bool, error) {
	if s.reader == nil {
		return Snapshot{}, false, fmt.Errorf("management account balance reader is required")
	}
	row, found, err := s.reader.GetManagementAccountBalanceSnapshot(ctx, normalizeInput(input))
	if err != nil || !found {
		return Snapshot{}, found, err
	}
	var snapshot Snapshot
	if err := json.Unmarshal([]byte(row.SnapshotJSON), &snapshot); err != nil {
		return Snapshot{}, false, fmt.Errorf("decode management account balance snapshot: %w", err)
	}
	return snapshot, true, nil
}

func (s *Service) Refresh(ctx context.Context, input Input) (Snapshot, bool, error) {
	if s.reader == nil {
		return Snapshot{}, false, fmt.Errorf("management account balance reader is required")
	}
	if s.writer == nil {
		return Snapshot{}, false, fmt.Errorf("management account balance writer is required")
	}
	candidate, found, err := s.reader.GetManagementAccountBalanceCandidate(ctx, normalizeInput(input))
	if err != nil || !found {
		return Snapshot{}, found, err
	}
	if s.query == nil {
		return Snapshot{}, false, ErrBalanceQueryMissing
	}
	snapshot, err := s.query(ctx, candidate)
	if err != nil {
		return Snapshot{}, true, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	snapshot.LastAttemptAt = now
	if snapshot.Status == "fresh" || snapshot.Status == "unlimited" {
		snapshot.LastSuccessAt = now
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return Snapshot{}, true, fmt.Errorf("encode management account balance snapshot: %w", err)
	}
	if err := s.writer.UpsertManagementAccountBalanceSnapshot(ctx, port.ManagementAccountBalanceSnapshot{
		AccountID:       candidate.AccountID,
		SystemAccountID: candidate.SystemAccountID,
		Status:          snapshot.Status,
		SnapshotJSON:    string(payload),
	}); err != nil {
		return Snapshot{}, true, err
	}
	return snapshot, true, nil
}

func normalizeInput(input Input) port.ManagementAccountBalanceInput {
	return port.ManagementAccountBalanceInput{
		AccountID:       strings.TrimSpace(input.AccountID),
		SystemAccountID: strings.TrimSpace(input.SystemAccountID),
	}
}
