package managementclientipstats

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceDetailNormalizesNodeBoundariesAndMapsResult(t *testing.T) {
	accountName := "账号一"
	ownerID := "sys_owner"
	ownerName := "所有者"
	lastUsedAt := "2026-07-14T00:00:03.000Z"
	store := &clientIPStatsDetailStoreStub{
		timezone:      "Asia/Shanghai",
		timezoneFound: true,
		page: port.ManagementClientIPStatsDetailPage{
			Found:          true,
			IPHash:         strings.Repeat("a", 64),
			AggregateIPKey: "198.18.20",
			LastSeenAt:     "2026-07-14T00:00:04.000Z",
			RangeReady:     true,
			HasMore:        true,
			Rows: []port.ManagementClientIPAccountUsageRow{{
				AccountID:                     "account_1",
				AccountName:                   &accountName,
				AccountOwnerSystemAccountID:   &ownerID,
				AccountOwnerSystemAccountName: &ownerName,
				RangeUsage: port.ManagementClientIPUsageSummary{
					RequestCount: 5,
					SuccessCount: 4,
					ErrorCount:   1,
					InputTokens:  10,
					OutputTokens: 4,
					LastUsedAt:   &lastUsedAt,
				},
			}},
		},
	}
	service := NewServiceWithOptions(ServiceOptions{
		DetailReader:             store,
		UsageStatsTimezoneReader: store,
		Now: func() time.Time {
			return time.Date(2026, 7, 14, 1, 2, 3, 0, time.UTC)
		},
	})

	result, err := service.Detail(context.Background(), DetailInput{
		IPHash:    "\u00a0" + strings.Repeat("A", 64) + "\t",
		Page:      999,
		PageSize:  3,
		StartDate: "2026-01-01",
		EndDate:   "2026-07-14",
		SortOrder: "asc",
	})
	if err != nil {
		t.Fatalf("Detail() error = %v", err)
	}
	if store.calls != 1 {
		t.Fatalf("detail calls = %d, want 1", store.calls)
	}
	if store.input.IPHash != strings.Repeat("a", 64) || store.input.StartDate != "2026-06-14" || store.input.EndDate != "2026-07-14" || store.input.SortField != port.ManagementClientIPStatsSortRequestCount || store.input.SortOrder != port.ManagementClientIPStatsSortAscending || store.input.Limit != 4 || store.input.Offset != 996 {
		t.Fatalf("detail input = %+v", store.input)
	}
	if result.Page != 333 || result.PageSize != 3 || result.PageUpperBound != 998 || !result.HasMore || !result.RangeReady {
		t.Fatalf("detail pagination = %+v", result)
	}
	if result.Range.Days != 31 || result.Range.MaxDays != 31 || result.LastSeenAt == nil || *result.LastSeenAt != "2026-07-14T00:00:04.000Z" {
		t.Fatalf("detail range/last seen = %+v / %v", result.Range, result.LastSeenAt)
	}
	if len(result.Items) != 1 || result.Items[0].AccountName == nil || *result.Items[0].AccountName != accountName || result.Items[0].RangeUsage.ErrorRate != 0.2 || result.Items[0].RangeUsage.TotalTokens != 14 {
		t.Fatalf("detail items = %+v", result.Items)
	}
}

func TestServiceDetailReturnsReadyMetadataWithoutRows(t *testing.T) {
	store := &clientIPStatsDetailStoreStub{
		timezone:      "UTC",
		timezoneFound: true,
		page: port.ManagementClientIPStatsDetailPage{
			Found:          true,
			IPHash:         strings.Repeat("b", 64),
			AggregateIPKey: "203.0.113",
			LastSeenAt:     "2026-07-14T00:00:00.000Z",
			RangeReady:     false,
		},
	}
	service := NewServiceWithOptions(ServiceOptions{
		DetailReader:             store,
		UsageStatsTimezoneReader: store,
		Now: func() time.Time {
			return time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
		},
	})

	result, err := service.Detail(context.Background(), DetailInput{
		IPHash: strings.Repeat("b", 64),
		Page:   2,
	})
	if err != nil {
		t.Fatalf("Detail() error = %v", err)
	}
	if result.RangeReady || result.Items == nil || len(result.Items) != 0 || result.PageUpperBound != 0 || result.HasMore || result.Page != 2 || result.PageSize != 20 {
		t.Fatalf("not-ready detail = %+v", result)
	}
}

func TestServiceDetailMapsMissingRegistryAndInvalidHash(t *testing.T) {
	store := &clientIPStatsDetailStoreStub{
		timezone:      "UTC",
		timezoneFound: true,
	}
	service := NewServiceWithOptions(ServiceOptions{
		DetailReader:             store,
		UsageStatsTimezoneReader: store,
	})

	_, err := service.Detail(context.Background(), DetailInput{IPHash: strings.Repeat("c", 64)})
	if !errors.Is(err, ErrIPNotFound) || store.calls != 1 {
		t.Fatalf("missing registry error/calls = %v / %d", err, store.calls)
	}
	store.calls = 0
	store.timezoneCalls = 0
	_, err = service.Detail(context.Background(), DetailInput{IPHash: "not-a-hash"})
	if !errors.Is(err, ErrIPNotFound) || store.calls != 0 || store.timezoneCalls != 0 {
		t.Fatalf("invalid hash error/calls = %v / detail %d timezone %d", err, store.calls, store.timezoneCalls)
	}
}

func TestServiceDetailRequiresReader(t *testing.T) {
	service := NewServiceWithOptions(ServiceOptions{})
	_, err := service.Detail(context.Background(), DetailInput{IPHash: strings.Repeat("d", 64)})
	if err == nil || !strings.Contains(err.Error(), "detail reader is required") {
		t.Fatalf("Detail() error = %v", err)
	}
}

type clientIPStatsDetailStoreStub struct {
	timezone      string
	timezoneFound bool
	timezoneErr   error
	timezoneCalls int
	page          port.ManagementClientIPStatsDetailPage
	err           error
	input         port.ManagementClientIPStatsDetailInput
	calls         int
}

func (s *clientIPStatsDetailStoreStub) GetManagementUsageStatsTimezone(context.Context) (string, bool, error) {
	s.timezoneCalls++
	return s.timezone, s.timezoneFound, s.timezoneErr
}

func (s *clientIPStatsDetailStoreStub) GetManagementClientIPStatsDetail(
	_ context.Context,
	input port.ManagementClientIPStatsDetailInput,
) (port.ManagementClientIPStatsDetailPage, error) {
	s.calls++
	s.input = input
	return s.page, s.err
}

var _ port.ManagementClientIPStatsDetailReader = (*clientIPStatsDetailStoreStub)(nil)
var _ port.ManagementUsageStatsTimezoneReader = (*clientIPStatsDetailStoreStub)(nil)
