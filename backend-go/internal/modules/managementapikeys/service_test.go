package managementapikeys

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceListMapsAdminSummaryAndLoadsCurrentPageUsageOnce(t *testing.T) {
	updatedAt := time.Date(2026, 7, 10, 2, 2, 3, 0, time.UTC)
	expiresAt := updatedAt.Add(24 * time.Hour)
	lastUsedAt := updatedAt.Add(2 * time.Hour)
	description := "管理 Key"
	quotaJSON := `{"daily":{"enabled":true,"limit":12.5}}`
	scheduleJSON := `{"enabled":true,"timezone":"Asia/Shanghai","mode":"allow_windows","windows":[{"daysOfWeek":[1,2],"start":"09:00","end":"18:00"}]}`
	store := &managementAPIKeyStoreStub{
		page: port.ManagementAPIKeyListPage{
			Rows: []port.ManagementAPIKeyListRow{{
				ID:                       "key_1",
				SystemAccountID:          "sys_owner",
				SystemAccountName:        "所有者",
				Name:                     "Key% literal",
				Description:              &description,
				KeyPrefix:                "sk-prefix",
				KeySuffix:                "suffix",
				Status:                   "active",
				IsDefault:                true,
				RouteStrategyID:          "route_1",
				RouteStrategyName:        "默认策略",
				RouteStrategyMode:        "normal",
				RouteStrategyStatus:      "active",
				ExpiresAt:                &expiresAt,
				QuotaLimitsJSON:          &quotaJSON,
				AvailabilityScheduleJSON: &scheduleJSON,
			}},
			HasMore: true,
		},
		usage: []port.ManagementAPIKeyUsageRow{{
			APIKeyID: "key_1",
			Usage: port.ManagementAccountUsageSummary{
				RequestCount: 12,
				InputTokens:  20,
				OutputTokens: 30,
				TotalTokens:  50,
				TotalCost:    1.25,
				LastUsedAt:   &lastUsedAt,
			},
		}},
	}
	service := NewService(store)

	result, err := service.List(context.Background(), ListInput{
		ActorSystemAccountID: "sys_admin",
		ActorRole:            "admin",
		SystemAccountID:      " all ",
		Page:                 1,
		PageSize:             1,
		PageSizeProvided:     true,
		Keyword:              "  Key%  ",
		Status:               "active",
		RouteStrategyID:      " route_1 ",
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if store.listCalls != 1 {
		t.Fatalf("list calls = %d, want 1", store.listCalls)
	}
	if got := store.listInput; got.SystemAccountID != "" ||
		got.Keyword != "Key%" ||
		got.Status != "active" ||
		got.RouteStrategyID != "route_1" ||
		got.Limit != 2 ||
		got.Offset != 0 {
		t.Fatalf("list input = %+v", got)
	}
	if store.usageCalls != 1 || !reflect.DeepEqual(store.usageIDs, []string{"key_1"}) {
		t.Fatalf("usage calls=%d ids=%#v", store.usageCalls, store.usageIDs)
	}
	if len(result.Items) != 1 || result.Total != 2 || !result.HasMore || result.Page != 1 || result.PageSize != 1 {
		t.Fatalf("result = %+v", result)
	}
	item := result.Items[0]
	if item.SystemAccountID != "sys_owner" ||
		item.SystemAccountName != "所有者" ||
		item.Name != "Key% literal" ||
		item.RouteStrategyMode != "normal" ||
		item.RouteStrategyStatus != "active" ||
		!item.IsDefault ||
		item.Usage.RequestCount != 12 ||
		item.Usage.TotalTokens != 50 {
		t.Fatalf("item = %+v", item)
	}
	if item.QuotaLimits.Daily == nil || item.QuotaLimits.Daily.Limit != 12.5 {
		t.Fatalf("quota limits = %+v", item.QuotaLimits)
	}
	if item.AvailabilitySchedule == nil ||
		item.AvailabilitySchedule["timezone"] != "Asia/Shanghai" {
		t.Fatalf("availability schedule = %#v", item.AvailabilitySchedule)
	}
}

func TestServiceListForcesSelfScopeAndHidesManagementOwnerFields(t *testing.T) {
	store := &managementAPIKeyStoreStub{
		page: port.ManagementAPIKeyListPage{Rows: []port.ManagementAPIKeyListRow{{
			ID:                "key_self",
			SystemAccountID:   "sys_current",
			SystemAccountName: "当前用户",
			Name:              "个人 Key",
			KeyPrefix:         "sk-self",
			KeySuffix:         "self",
			Status:            "disabled",
			RouteStrategyID:   "route_self",
		}}},
	}
	service := NewService(store)

	result, err := service.List(context.Background(), ListInput{
		ActorSystemAccountID: " sys_current ",
		ActorRole:            "admin",
		SystemAccountID:      "sys_forged",
		SelfOnly:             true,
		Status:               "invalid",
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if store.listInput.SystemAccountID != "sys_current" || store.listInput.Status != "" {
		t.Fatalf("list input = %+v", store.listInput)
	}
	if len(result.Items) != 1 {
		t.Fatalf("items = %+v", result.Items)
	}
	if result.Items[0].SystemAccountID != "" || result.Items[0].SystemAccountName != "" {
		t.Fatalf("self item leaked management owner fields: %+v", result.Items[0])
	}
}

func TestServiceListUsesProgressivePaginationBoundsAndSkipsEmptyUsageRead(t *testing.T) {
	tests := []struct {
		name         string
		page         int
		pageSize     int
		provided     bool
		wantPage     int
		wantPageSize int
		wantOffset   int
		wantLimit    int
	}{
		{name: "defaults", wantPage: 1, wantPageSize: 50, wantOffset: 0, wantLimit: 51},
		{name: "explicit zero", page: -1, pageSize: 0, provided: true, wantPage: 1, wantPageSize: 1, wantOffset: 0, wantLimit: 2},
		{name: "caps page size and page", page: 99, pageSize: 500, provided: true, wantPage: 5, wantPageSize: 200, wantOffset: 800, wantLimit: 201},
		{name: "caps small page size window", page: 999, pageSize: 3, provided: true, wantPage: 333, wantPageSize: 3, wantOffset: 996, wantLimit: 4},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &managementAPIKeyStoreStub{}
			service := NewService(store)
			result, err := service.List(context.Background(), ListInput{
				ActorSystemAccountID: "sys_admin",
				ActorRole:            "super_admin",
				Page:                 test.page,
				PageSize:             test.pageSize,
				PageSizeProvided:     test.provided,
			})
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			if store.listInput.Offset != test.wantOffset || store.listInput.Limit != test.wantLimit {
				t.Fatalf("list input = %+v", store.listInput)
			}
			if result.Page != test.wantPage || result.PageSize != test.wantPageSize || result.Items == nil {
				t.Fatalf("result = %+v", result)
			}
			if store.usageCalls != 0 {
				t.Fatalf("usage calls = %d, want 0", store.usageCalls)
			}
		})
	}
}

func TestServiceListRejectsMissingActorAndDoesNotMaskStoreOrJSONErrors(t *testing.T) {
	t.Run("missing actor", func(t *testing.T) {
		store := &managementAPIKeyStoreStub{}
		_, err := NewService(store).List(context.Background(), ListInput{ActorRole: "admin"})
		if !errors.Is(err, ErrAPIKeyListInvalid) {
			t.Fatalf("List() error = %v, want %v", err, ErrAPIKeyListInvalid)
		}
		if store.listCalls != 0 {
			t.Fatalf("list calls = %d, want 0", store.listCalls)
		}
	})

	t.Run("store error", func(t *testing.T) {
		wantErr := errors.New("postgres unavailable")
		store := &managementAPIKeyStoreStub{listErr: wantErr}
		_, err := NewService(store).List(context.Background(), ListInput{
			ActorSystemAccountID: "sys_admin",
			ActorRole:            "admin",
		})
		if !errors.Is(err, wantErr) {
			t.Fatalf("List() error = %v, want %v", err, wantErr)
		}
	})

	t.Run("malformed stored json", func(t *testing.T) {
		badJSON := "{"
		store := &managementAPIKeyStoreStub{
			page: port.ManagementAPIKeyListPage{Rows: []port.ManagementAPIKeyListRow{{
				ID:                       "key_bad",
				SystemAccountID:          "sys_admin",
				Name:                     "bad",
				KeyPrefix:                "sk-bad",
				KeySuffix:                "bad",
				Status:                   "active",
				RouteStrategyID:          "route_bad",
				AvailabilityScheduleJSON: &badJSON,
			}}},
		}
		_, err := NewService(store).List(context.Background(), ListInput{
			ActorSystemAccountID: "sys_admin",
			ActorRole:            "admin",
		})
		if err == nil || !strings.Contains(err.Error(), "availability schedule") {
			t.Fatalf("List() error = %v, want stored schedule error", err)
		}
	})
}

type managementAPIKeyStoreStub struct {
	listInput  port.ManagementAPIKeyListInput
	usageIDs   []string
	page       port.ManagementAPIKeyListPage
	usage      []port.ManagementAPIKeyUsageRow
	listErr    error
	usageErr   error
	listCalls  int
	usageCalls int
}

func (s *managementAPIKeyStoreStub) ListManagementAPIKeys(
	_ context.Context,
	input port.ManagementAPIKeyListInput,
) (port.ManagementAPIKeyListPage, error) {
	s.listCalls++
	s.listInput = input
	return s.page, s.listErr
}

func (s *managementAPIKeyStoreStub) ListManagementAPIKeyUsageTotals(
	_ context.Context,
	apiKeyIDs []string,
) ([]port.ManagementAPIKeyUsageRow, error) {
	s.usageCalls++
	s.usageIDs = append([]string(nil), apiKeyIDs...)
	return s.usage, s.usageErr
}

var _ port.ManagementAPIKeyListReader = (*managementAPIKeyStoreStub)(nil)
