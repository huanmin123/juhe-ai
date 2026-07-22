package managementaccountbalance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/modules/managementaccountdraft"
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

type DraftCandidate struct {
	AccountID       string
	SystemAccountID string
	ProviderCode    string
	ProtocolCode    string
	ProtocolVersion string
	Type            string
	Credentials     map[string]any
	ProxyProfileID  string
	Config          managementaccountdraft.BalanceQueryConfig
}

type DraftQuery func(context.Context, DraftCandidate) (Snapshot, error)

type DraftPreparer interface {
	Prepare(context.Context, managementaccountdraft.Input) (managementaccountdraft.Snapshot, error)
}

type ServiceOptions struct {
	Reader     port.ManagementAccountBalanceReader
	Writer     port.ManagementAccountBalanceWriter
	Now        func() time.Time
	Query      Query
	Drafts     DraftPreparer
	DraftQuery DraftQuery
}

type Service struct {
	reader     port.ManagementAccountBalanceReader
	writer     port.ManagementAccountBalanceWriter
	now        func() time.Time
	query      Query
	drafts     DraftPreparer
	draftQuery DraftQuery
}

func NewService(opts ServiceOptions) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{reader: opts.Reader, writer: opts.Writer, now: now, query: opts.Query, drafts: opts.Drafts, draftQuery: opts.DraftQuery}
}

type DraftInput struct {
	Account managementaccountdraft.Account
	Config  managementaccountdraft.BalanceQueryConfig
	Access  port.ManagementAccountTestAccess
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

func (s *Service) TestDraft(ctx context.Context, input DraftInput) (Snapshot, error) {
	if s.drafts == nil {
		return Snapshot{}, fmt.Errorf("management account balance draft preparer is required")
	}
	snapshot, err := s.drafts.Prepare(ctx, managementaccountdraft.Input{Access: input.Access, Account: input.Account})
	if err != nil {
		return Snapshot{}, err
	}
	config, err := managementaccountdraft.NormalizeBalanceConfig(input.Config)
	if err != nil {
		return Snapshot{}, err
	}
	if err := managementaccountdraft.ValidateBalanceDraft(snapshot, config); err != nil {
		return Snapshot{}, err
	}
	if s.draftQuery == nil {
		return Snapshot{}, ErrBalanceQueryMissing
	}
	return s.draftQuery(ctx, DraftCandidate{
		AccountID: snapshot.ID, SystemAccountID: snapshot.OwnerSystemAccountID,
		ProviderCode: snapshot.ProviderCode, ProtocolCode: snapshot.ProtocolCode, ProtocolVersion: snapshot.ProtocolVersion,
		Type: snapshot.Type, Credentials: snapshot.Credentials, ProxyProfileID: snapshot.ProxyProfileID, Config: config,
	})
}

func normalizeInput(input Input) port.ManagementAccountBalanceInput {
	return port.ManagementAccountBalanceInput{
		AccountID:       strings.TrimSpace(input.AccountID),
		SystemAccountID: strings.TrimSpace(input.SystemAccountID),
	}
}
