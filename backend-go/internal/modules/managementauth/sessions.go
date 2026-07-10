package managementauth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	defaultSessionListPage     = 1
	defaultSessionListPageSize = 20
	maxSessionListPageSize     = 100
	maxSessionListWindowRows   = 1001
)

var (
	ErrSessionInputInvalid = errors.New("management session input invalid")
	ErrSessionNotFound     = errors.New("management session not found")
)

type SessionService struct {
	store port.ManagementSessionManager
	now   func() time.Time
}

type SessionServiceOptions struct {
	Store port.ManagementSessionManager
	Now   func() time.Time
}

type SessionListInput struct {
	SystemAccountID  string
	CurrentSessionID string
	Page             int
	PageSize         int
}

type SessionListResult struct {
	Items    []SessionSummary `json:"items"`
	Total    int              `json:"total"`
	HasMore  bool             `json:"hasMore"`
	Page     int              `json:"page"`
	PageSize int              `json:"pageSize"`
}

type SessionSummary struct {
	ID         string `json:"id"`
	Current    bool   `json:"current"`
	CreatedAt  string `json:"createdAt"`
	LastSeenAt string `json:"lastSeenAt"`
	ExpiresAt  string `json:"expiresAt"`
}

type SessionRevokeInput struct {
	SystemAccountID  string
	SessionID        string
	CurrentSessionID string
}

type SessionRevokeResult struct {
	ID      string `json:"id"`
	Revoked bool   `json:"revoked"`
	Current bool   `json:"current"`
}

func NewSessionService(store port.ManagementSessionManager) *SessionService {
	return NewSessionServiceWithOptions(SessionServiceOptions{Store: store})
}

func NewSessionServiceWithOptions(opts SessionServiceOptions) *SessionService {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &SessionService{store: opts.Store, now: now}
}

func (s *SessionService) List(ctx context.Context, input SessionListInput) (SessionListResult, error) {
	if s.store == nil {
		return SessionListResult{}, fmt.Errorf("management session store is required")
	}
	systemAccountID := strings.TrimSpace(input.SystemAccountID)
	if systemAccountID == "" {
		return SessionListResult{}, ErrSessionInputInvalid
	}
	pageSize := sessionListPageSize(input.PageSize)
	page := sessionListPage(input.Page, pageSize)
	result, err := s.store.ListManagementSessionsForAccount(ctx, port.ManagementSessionListInput{
		SystemAccountID: systemAccountID,
		Now:             s.now().UTC(),
		Limit:           pageSize + 1,
		Offset:          (page - 1) * pageSize,
	})
	if err != nil {
		return SessionListResult{}, err
	}
	items := make([]SessionSummary, 0, len(result.Items))
	for _, row := range result.Items {
		items = append(items, sessionSummaryFromPort(row, strings.TrimSpace(input.CurrentSessionID)))
	}
	return SessionListResult{
		Items:    items,
		Total:    sessionPagedTotalUpperBound(page, pageSize, len(items), result.HasMore),
		HasMore:  result.HasMore,
		Page:     page,
		PageSize: pageSize,
	}, nil
}

func (s *SessionService) Revoke(ctx context.Context, input SessionRevokeInput) (SessionRevokeResult, error) {
	if s.store == nil {
		return SessionRevokeResult{}, fmt.Errorf("management session store is required")
	}
	systemAccountID := strings.TrimSpace(input.SystemAccountID)
	sessionID := strings.TrimSpace(input.SessionID)
	if systemAccountID == "" || sessionID == "" {
		return SessionRevokeResult{}, ErrSessionInputInvalid
	}
	found, err := s.store.RevokeManagementSessionForAccount(ctx, port.ManagementSessionRevokeInput{
		SystemAccountID: systemAccountID,
		SessionID:       sessionID,
	})
	if err != nil {
		return SessionRevokeResult{}, err
	}
	if !found {
		return SessionRevokeResult{}, ErrSessionNotFound
	}
	return SessionRevokeResult{
		ID:      sessionID,
		Revoked: true,
		Current: sessionID == strings.TrimSpace(input.CurrentSessionID),
	}, nil
}

func sessionSummaryFromPort(row port.ManagementSessionSummary, currentSessionID string) SessionSummary {
	return SessionSummary{
		ID:         row.ID,
		Current:    row.ID == currentSessionID,
		CreatedAt:  formatSessionTime(row.CreatedAt),
		LastSeenAt: formatSessionTime(row.LastSeenAt),
		ExpiresAt:  formatSessionTime(row.ExpiresAt),
	}
}

func sessionListPageSize(pageSize int) int {
	if pageSize <= 0 {
		return defaultSessionListPageSize
	}
	return min(pageSize, maxSessionListPageSize)
}

func sessionListPage(page int, pageSize int) int {
	if page <= 0 {
		return defaultSessionListPage
	}
	maxPage := max(1, (maxSessionListWindowRows-1)/max(1, pageSize))
	return min(page, maxPage)
}

func sessionPagedTotalUpperBound(page int, pageSize int, itemCount int, hasMore bool) int {
	total := (max(1, page) - 1) * max(0, pageSize)
	total += max(0, itemCount)
	if hasMore {
		total++
	}
	return total
}

func formatSessionTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
