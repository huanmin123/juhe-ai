package managementsystemaccounts

import (
	"context"
	"errors"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestListNormalizesInputAndMapsSummaries(t *testing.T) {
	createdAt := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	lastLoginAt := createdAt.Add(time.Minute)
	store := &systemAccountOptionStoreStub{
		listResult: port.ManagementSystemAccountListResult{
			Items: []port.ManagementSystemAccountSummary{{
				ID:                     "sys_admin",
				Username:               "admin",
				DisplayName:            "管理员",
				Description:            "系统管理员",
				Role:                   "admin",
				Status:                 "active",
				MustChangePassword:     true,
				ImageGenerationEnabled: true,
				LastLoginAt:            &lastLoginAt,
				CreatedAt:              createdAt,
				UpdatedAt:              createdAt.Add(time.Hour),
			}},
			HasMore: true,
		},
	}
	service := NewService(store)

	result, err := service.List(context.Background(), ListInput{
		Keyword:  " 管理 ",
		Page:     2,
		PageSize: 500,
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if store.listInput.Keyword != "管理" || store.listInput.Limit != 101 || store.listInput.Offset != 100 {
		t.Fatalf("store list input = %+v", store.listInput)
	}
	if result.Page != 2 || result.PageSize != 100 || result.Total != 102 || !result.HasMore {
		t.Fatalf("pagination = page %d size %d total %d hasMore %v", result.Page, result.PageSize, result.Total, result.HasMore)
	}
	if len(result.Items) != 1 {
		t.Fatalf("items = %+v", result.Items)
	}
	got := result.Items[0]
	if got.ID != "sys_admin" ||
		got.Description != "系统管理员" ||
		got.Role != "admin" ||
		got.MustChangePassword ||
		!got.ImageGenerationEnabled ||
		got.LastLoginAt != lastLoginAt.Format(time.RFC3339Nano) ||
		got.CreatedAt != createdAt.Format(time.RFC3339Nano) {
		t.Fatalf("summary = %+v", got)
	}
}

func TestListDefaultsAndClampsPageToWindow(t *testing.T) {
	store := &systemAccountOptionStoreStub{}
	service := NewService(store)

	result, err := service.List(context.Background(), ListInput{Page: 999, PageSize: -1})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if store.listInput.Limit != 21 || store.listInput.Offset != 980 {
		t.Fatalf("store list input = %+v", store.listInput)
	}
	if result.Page != 50 || result.PageSize != 20 {
		t.Fatalf("pagination = page %d size %d", result.Page, result.PageSize)
	}
}

func TestListReturnsStoreError(t *testing.T) {
	want := errors.New("postgres down")
	service := NewService(&systemAccountOptionStoreStub{listErr: want})

	_, err := service.List(context.Background(), ListInput{})

	if !errors.Is(err, want) {
		t.Fatalf("List() error = %v, want %v", err, want)
	}
}

func TestOptionsNormalizesInputAndMapsOptions(t *testing.T) {
	store := &systemAccountOptionStoreStub{
		options: []port.ManagementSystemAccountOption{{
			ID:          "sys_user",
			Username:    "user",
			DisplayName: "用户",
			Status:      "active",
		}},
	}
	service := NewService(store)

	got, err := service.Options(context.Background(), OptionListInput{
		IDs:     []string{" sys_user ", "sys_user", "", "sys_disabled"},
		Keyword: "  用户  ",
		Limit:   500,
	})
	if err != nil {
		t.Fatalf("Options() error = %v", err)
	}
	if store.input.Keyword != "用户" || store.input.Limit != 50 {
		t.Fatalf("store input = %+v, want trimmed keyword and limit 50", store.input)
	}
	if len(store.input.IDs) != 2 || store.input.IDs[0] != "sys_user" || store.input.IDs[1] != "sys_disabled" {
		t.Fatalf("ids = %#v", store.input.IDs)
	}
	if len(got) != 1 || got[0].ID != "sys_user" || got[0].Username != "user" || got[0].DisplayName != "用户" || got[0].Status != "active" {
		t.Fatalf("Options() = %+v", got)
	}
}

func TestOptionsDefaultsLimit(t *testing.T) {
	store := &systemAccountOptionStoreStub{}
	service := NewService(store)

	if _, err := service.Options(context.Background(), OptionListInput{Limit: -10}); err != nil {
		t.Fatalf("Options() error = %v", err)
	}
	if store.input.Limit != 50 {
		t.Fatalf("limit = %d, want 50", store.input.Limit)
	}
}

func TestOptionsReturnsStoreError(t *testing.T) {
	want := errors.New("postgres down")
	service := NewService(&systemAccountOptionStoreStub{err: want})

	_, err := service.Options(context.Background(), OptionListInput{})

	if !errors.Is(err, want) {
		t.Fatalf("Options() error = %v, want %v", err, want)
	}
}

type systemAccountOptionStoreStub struct {
	listInput  port.ManagementSystemAccountListInput
	listResult port.ManagementSystemAccountListResult
	listErr    error
	input      port.ManagementSystemAccountOptionListInput
	options    []port.ManagementSystemAccountOption
	err        error
}

func (s *systemAccountOptionStoreStub) ListManagementSystemAccounts(_ context.Context, input port.ManagementSystemAccountListInput) (port.ManagementSystemAccountListResult, error) {
	s.listInput = input
	return s.listResult, s.listErr
}

func (s *systemAccountOptionStoreStub) ListManagementSystemAccountOptions(_ context.Context, input port.ManagementSystemAccountOptionListInput) ([]port.ManagementSystemAccountOption, error) {
	s.input = input
	return s.options, s.err
}
