package managementauth

import (
	"context"
	"errors"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestSessionServiceListNormalizesPaginationAndMarksCurrent(t *testing.T) {
	now := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	store := &sessionStoreStub{
		listResult: port.ManagementSessionListResult{
			Items: []port.ManagementSessionSummary{
				{
					ID:         "sess_current",
					CreatedAt:  now.Add(-2 * time.Hour),
					LastSeenAt: now.Add(-time.Minute),
					ExpiresAt:  now.Add(24 * time.Hour),
				},
			},
			HasMore: true,
		},
	}
	service := NewSessionServiceWithOptions(SessionServiceOptions{
		Store: store,
		Now:   func() time.Time { return now },
	})

	result, err := service.List(context.Background(), SessionListInput{
		SystemAccountID:  " sys_user ",
		CurrentSessionID: "sess_current",
		Page:             2,
		PageSize:         10,
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if store.listInput.SystemAccountID != "sys_user" ||
		!store.listInput.Now.Equal(now) ||
		store.listInput.Limit != 11 ||
		store.listInput.Offset != 10 {
		t.Fatalf("list input = %+v", store.listInput)
	}
	if result.Page != 2 || result.PageSize != 10 || result.Total != 12 || !result.HasMore {
		t.Fatalf("pagination = %+v", result)
	}
	if len(result.Items) != 1 || !result.Items[0].Current || result.Items[0].ID != "sess_current" {
		t.Fatalf("items = %+v", result.Items)
	}
	if result.Items[0].CreatedAt != "2026-07-09T08:00:00Z" ||
		result.Items[0].LastSeenAt != "2026-07-09T09:59:00Z" ||
		result.Items[0].ExpiresAt != "2026-07-10T10:00:00Z" {
		t.Fatalf("formatted item = %+v", result.Items[0])
	}
}

func TestSessionServiceListRejectsInvalidInput(t *testing.T) {
	service := NewSessionServiceWithOptions(SessionServiceOptions{Store: &sessionStoreStub{}})
	if _, err := service.List(context.Background(), SessionListInput{}); !errors.Is(err, ErrSessionInputInvalid) {
		t.Fatalf("List() error = %v, want ErrSessionInputInvalid", err)
	}
}

func TestSessionServiceRevokeCurrentSession(t *testing.T) {
	store := &sessionStoreStub{revokeFound: true}
	service := NewSessionServiceWithOptions(SessionServiceOptions{Store: store})

	result, err := service.Revoke(context.Background(), SessionRevokeInput{
		SystemAccountID:  " sys_user ",
		SessionID:        " sess_current ",
		CurrentSessionID: "sess_current",
	})
	if err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}
	if store.revokeInput.SystemAccountID != "sys_user" || store.revokeInput.SessionID != "sess_current" {
		t.Fatalf("revoke input = %+v", store.revokeInput)
	}
	if !result.Revoked || !result.Current || result.ID != "sess_current" {
		t.Fatalf("result = %+v", result)
	}
}

func TestSessionServiceRevokeMapsNotFoundAndInvalidInput(t *testing.T) {
	service := NewSessionServiceWithOptions(SessionServiceOptions{Store: &sessionStoreStub{}})
	if _, err := service.Revoke(context.Background(), SessionRevokeInput{SystemAccountID: "sys_user"}); !errors.Is(err, ErrSessionInputInvalid) {
		t.Fatalf("Revoke() invalid error = %v, want ErrSessionInputInvalid", err)
	}
	if _, err := service.Revoke(context.Background(), SessionRevokeInput{
		SystemAccountID: "sys_user",
		SessionID:       "sess_missing",
	}); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Revoke() not found error = %v, want ErrSessionNotFound", err)
	}
}

type sessionStoreStub struct {
	listInput   port.ManagementSessionListInput
	listResult  port.ManagementSessionListResult
	listErr     error
	revokeInput port.ManagementSessionRevokeInput
	revokeFound bool
	revokeErr   error
}

func (s *sessionStoreStub) ListManagementSessionsForAccount(_ context.Context, input port.ManagementSessionListInput) (port.ManagementSessionListResult, error) {
	s.listInput = input
	return s.listResult, s.listErr
}

func (s *sessionStoreStub) RevokeManagementSessionForAccount(_ context.Context, input port.ManagementSessionRevokeInput) (bool, error) {
	s.revokeInput = input
	return s.revokeFound, s.revokeErr
}
