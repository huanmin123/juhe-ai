package managementusagerecords

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceListServesEligibleSelfGatewayFirstPageFromCache(t *testing.T) {
	now := time.Date(2026, 7, 21, 3, 4, 5, 0, time.UTC)
	cache := &usageRecordFirstPageCacheStub{values: map[string][]byte{}}
	cache.values[usageRecordFirstPageCacheKey("sys_user", "2026-07-21")] = mustMarshalUsageRecordFirstPageEntry(t, usageRecordFirstPageCacheEntry{
		Items: []Summary{{ID: "usage_2", SystemAccountID: "sys_user", SystemAccountName: "private", TrafficSource: "gateway", CreatedAt: "2026-07-21T03:04:05.000Z"}, {ID: "usage_1", TrafficSource: "gateway", CreatedAt: "2026-07-21T03:03:05.000Z"}},
		Total: 2,
	})
	store := &usageRecordReaderStub{timezone: "Asia/Shanghai"}
	service := NewServiceWithOptions(ServiceOptions{Store: store, FirstPageCache: cache, Now: func() time.Time { return now }})

	result, err := service.List(context.Background(), usageRecordFirstPageEligibleInput())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if store.listCalls != 0 {
		t.Fatalf("cache hit must not query usage records, calls = %d", store.listCalls)
	}
	if result.Page != 1 || result.PageSize != 20 || result.Total != 2 || result.HasMore || len(result.Items) != 2 || result.Items[0].ID != "usage_2" {
		t.Fatalf("result = %+v", result)
	}
	if result.Items[0].SystemAccountID != "" || result.Items[0].SystemAccountName != "" {
		t.Fatalf("self cache hit leaked system account projection: %+v", result.Items[0])
	}
}

func TestServiceListFallsBackToStoreWhenFirstPageCacheFailsAndSeedsMetadata(t *testing.T) {
	now := time.Date(2026, 7, 21, 3, 4, 5, 0, time.UTC)
	items := make([]port.ManagementUsageRecordSummary, 20)
	for index := range items {
		items[index] = port.ManagementUsageRecordSummary{
			ID: "usage_" + string(rune('a'+index)), TraceID: "trace", TrafficSource: "gateway", Success: true,
			CreatedAt: now.Add(-time.Duration(index) * time.Second),
		}
	}
	store := &usageRecordReaderStub{
		timezone:   "Asia/Shanghai",
		listResult: port.ManagementUsageRecordListResult{Items: items, HasMore: true},
	}
	cache := &usageRecordFirstPageCacheStub{getErr: errors.New("redis unavailable"), values: map[string][]byte{}}
	service := NewServiceWithOptions(ServiceOptions{Store: store, FirstPageCache: cache, Now: func() time.Time { return now }})

	result, err := service.List(context.Background(), usageRecordFirstPageEligibleInput())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if store.listCalls != 1 || result.Total != 21 || !result.HasMore {
		t.Fatalf("store calls = %d result = %+v", store.listCalls, result)
	}
	if len(cache.setValues) != 1 {
		t.Fatalf("cache writes = %d", len(cache.setValues))
	}
	if cache.acquireCalls != 1 || cache.leaseTTL != 2*time.Second || cache.setTTL != 36*time.Hour || cache.releaseCalls != 1 {
		t.Fatalf("lease calls = %d lease ttl = %s set ttl = %s releases = %d", cache.acquireCalls, cache.leaseTTL, cache.setTTL, cache.releaseCalls)
	}
	var entry usageRecordFirstPageCacheEntry
	if err := json.Unmarshal(cache.setValues[0], &entry); err != nil {
		t.Fatalf("unmarshal cache entry: %v", err)
	}
	if !entry.HasMore || entry.Total != 21 || len(entry.Items) != 20 {
		t.Fatalf("cache entry = %+v", entry)
	}
}

func TestServiceListDoesNotUseFirstPageCacheOutsideStrictEligibility(t *testing.T) {
	now := time.Date(2026, 7, 21, 3, 4, 5, 0, time.UTC)
	for name, input := range map[string]ListInput{
		"admin projection": {ScopeSystemAccountID: "sys_user", IncludeSystemAccount: true, TrafficSource: "gateway", Page: 1, PageSize: 20, PageSizeProvided: true, SortOrder: "desc"},
		"non gateway":      {ScopeSystemAccountID: "sys_user", TrafficSource: "manual_account_test", Page: 1, PageSize: 20, PageSizeProvided: true, SortOrder: "desc"},
		"filtered":         {ScopeSystemAccountID: "sys_user", TrafficSource: "gateway", Model: "gpt-5", Page: 1, PageSize: 20, PageSizeProvided: true, SortOrder: "desc"},
		"other page":       {ScopeSystemAccountID: "sys_user", TrafficSource: "gateway", Page: 2, PageSize: 20, PageSizeProvided: true, SortOrder: "desc"},
		"other page size":  {ScopeSystemAccountID: "sys_user", TrafficSource: "gateway", Page: 1, PageSize: 50, PageSizeProvided: true, SortOrder: "desc"},
	} {
		t.Run(name, func(t *testing.T) {
			store := &usageRecordReaderStub{timezone: "Asia/Shanghai"}
			cache := &usageRecordFirstPageCacheStub{values: map[string][]byte{}}
			service := NewServiceWithOptions(ServiceOptions{Store: store, FirstPageCache: cache, Now: func() time.Time { return now }})
			if _, err := service.List(context.Background(), input); err != nil {
				t.Fatalf("List: %v", err)
			}
			if cache.getCalls != 0 || len(cache.setValues) != 0 || store.listCalls != 1 {
				t.Fatalf("cache gets = %d writes = %d store calls = %d", cache.getCalls, len(cache.setValues), store.listCalls)
			}
		})
	}
}

func TestServiceListFallsBackWhenCachedPayloadIsInvalid(t *testing.T) {
	now := time.Date(2026, 7, 21, 3, 4, 5, 0, time.UTC)
	cache := &usageRecordFirstPageCacheStub{values: map[string][]byte{
		usageRecordFirstPageCacheKey("sys_user", "2026-07-21"): []byte("not-json"),
	}}
	store := &usageRecordReaderStub{timezone: "Asia/Shanghai"}
	service := NewServiceWithOptions(ServiceOptions{Store: store, FirstPageCache: cache, Now: func() time.Time { return now }})
	if _, err := service.List(context.Background(), usageRecordFirstPageEligibleInput()); err != nil {
		t.Fatalf("List: %v", err)
	}
	if store.listCalls != 1 {
		t.Fatalf("invalid cache payload must fall back to store, calls = %d", store.listCalls)
	}
}

func TestServiceListNormalizesCachedOrdering(t *testing.T) {
	now := time.Date(2026, 7, 21, 3, 4, 5, 0, time.UTC)
	cache := &usageRecordFirstPageCacheStub{values: map[string][]byte{}}
	cache.values[usageRecordFirstPageCacheKey("sys_user", "2026-07-21")] = mustMarshalUsageRecordFirstPageEntry(t, usageRecordFirstPageCacheEntry{
		Items: []Summary{
			{ID: "usage_a", TrafficSource: "gateway", CreatedAt: "2026-07-21T03:03:05.000Z"},
			{ID: "usage_b", TrafficSource: "gateway", CreatedAt: "2026-07-21T03:04:05.000Z"},
			{ID: "usage_c", TrafficSource: "gateway", CreatedAt: "2026-07-21T03:04:05.000Z"},
		},
		Total: 3,
	})
	store := &usageRecordReaderStub{timezone: "Asia/Shanghai"}
	service := NewServiceWithOptions(ServiceOptions{Store: store, FirstPageCache: cache, Now: func() time.Time { return now }})
	result, err := service.List(context.Background(), usageRecordFirstPageEligibleInput())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"usage_c", "usage_b", "usage_a"}
	for index, id := range want {
		if result.Items[index].ID != id {
			t.Fatalf("items = %+v", result.Items)
		}
	}
}

func TestServiceListTreatsLeaseFailuresAsBestEffort(t *testing.T) {
	now := time.Date(2026, 7, 21, 3, 4, 5, 0, time.UTC)
	store := &usageRecordReaderStub{timezone: "Asia/Shanghai"}
	cache := &usageRecordFirstPageCacheStub{values: map[string][]byte{}, acquireErr: errors.New("lease unavailable")}
	service := NewServiceWithOptions(ServiceOptions{Store: store, FirstPageCache: cache, Now: func() time.Time { return now }})
	result, err := service.List(context.Background(), usageRecordFirstPageEligibleInput())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if store.listCalls != 1 || result.Page != 1 || cache.acquireCalls != 1 || len(cache.setValues) != 0 {
		t.Fatalf("store calls = %d result = %+v lease calls = %d cache writes = %d", store.listCalls, result, cache.acquireCalls, len(cache.setValues))
	}
}

func TestMergeUsageRecordFirstPageEntriesKeepsProgressiveTotalBounded(t *testing.T) {
	for _, count := range []int{20, 21, 40, 50} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			items := make([]Summary, count)
			for index := range items {
				items[index] = Summary{
					ID:            "usage_" + time.Unix(int64(index), 0).UTC().Format("150405"),
					TrafficSource: "gateway",
					CreatedAt:     time.Date(2026, 7, 21, 3, 4, 5, index*int(time.Millisecond), time.UTC).Format(jsTimeLayout),
				}
			}
			entry := mergeUsageRecordFirstPageEntries(usageRecordFirstPageCacheEntry{Items: items}, usageRecordFirstPageCacheEntry{})
			wantTotal := count
			wantHasMore := count > usageRecordFirstPageResponseSize
			if entry.Total != wantTotal || entry.HasMore != wantHasMore {
				t.Fatalf("count %d entry = %+v", count, entry)
			}
		})
	}
}

func TestServiceListFallsBackForSemanticallyInvalidCacheEntry(t *testing.T) {
	now := time.Date(2026, 7, 21, 3, 4, 5, 0, time.UTC)
	for name, entry := range map[string]usageRecordFirstPageCacheEntry{
		"empty with more":        {Total: 21, HasMore: true},
		"partial claims more":    {Items: []Summary{{ID: "usage_1", TrafficSource: "gateway", CreatedAt: "2026-07-21T03:04:05.000Z"}}, Total: 2, HasMore: true},
		"total without has more": {Items: []Summary{{ID: "usage_1", TrafficSource: "gateway", CreatedAt: "2026-07-21T03:04:05.000Z"}}, Total: 2},
	} {
		t.Run(name, func(t *testing.T) {
			cache := &usageRecordFirstPageCacheStub{values: map[string][]byte{
				usageRecordFirstPageCacheKey("sys_user", "2026-07-21"): mustMarshalUsageRecordFirstPageEntry(t, entry),
			}}
			store := &usageRecordReaderStub{timezone: "Asia/Shanghai"}
			service := NewServiceWithOptions(ServiceOptions{Store: store, FirstPageCache: cache, Now: func() time.Time { return now }})
			if _, err := service.List(context.Background(), usageRecordFirstPageEligibleInput()); err != nil {
				t.Fatalf("List: %v", err)
			}
			if store.listCalls != 1 {
				t.Fatalf("semantically invalid cache must fall back, calls = %d", store.listCalls)
			}
		})
	}
}

func usageRecordFirstPageEligibleInput() ListInput {
	return ListInput{
		ScopeSystemAccountID: "sys_user", TrafficSource: "gateway", Page: 1, PageSize: 20,
		PageSizeProvided: true, SortOrder: "desc",
	}
}

func mustMarshalUsageRecordFirstPageEntry(t *testing.T, entry usageRecordFirstPageCacheEntry) []byte {
	t.Helper()
	value, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal cache entry: %v", err)
	}
	return value
}

type usageRecordFirstPageCacheStub struct {
	values       map[string][]byte
	getErr       error
	acquireErr   error
	getCalls     int
	acquireCalls int
	leaseTTL     time.Duration
	setTTL       time.Duration
	releaseCalls int
	setValues    [][]byte
}

func (s *usageRecordFirstPageCacheStub) Get(_ context.Context, key string) ([]byte, error) {
	s.getCalls++
	if s.getErr != nil {
		return nil, s.getErr
	}
	value, found := s.values[key]
	if !found {
		return nil, errUsageRecordFirstPageCacheMiss
	}
	return value, nil
}

func (s *usageRecordFirstPageCacheStub) AcquireLease(_ context.Context, _ string, _ string, ttl time.Duration) (bool, error) {
	s.acquireCalls++
	s.leaseTTL = ttl
	if s.acquireErr != nil {
		return false, s.acquireErr
	}
	return true, nil
}

func (s *usageRecordFirstPageCacheStub) SetIfLeaseOwner(_ context.Context, _ string, _ string, value []byte, ttl time.Duration) (bool, error) {
	s.setTTL = ttl
	s.setValues = append(s.setValues, append([]byte(nil), value...))
	return true, nil
}

func (s *usageRecordFirstPageCacheStub) ReleaseLease(context.Context, string, string) error {
	s.releaseCalls++
	return nil
}
