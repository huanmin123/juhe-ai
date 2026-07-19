package managementclientipstats

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceListDefaultsAndBoundsPagination(t *testing.T) {
	tests := []struct {
		name       string
		input      ListInput
		wantPage   int
		wantSize   int
		wantLimit  int
		wantOffset int
		wantUpper  int
	}{
		{
			name:      "defaults",
			wantPage:  1,
			wantSize:  defaultListPageSize,
			wantLimit: defaultListPageSize + 1,
		},
		{
			name: "deep page is clamped to the one thousand row window",
			input: ListInput{
				Page:     999,
				PageSize: 20,
			},
			wantPage:   50,
			wantSize:   20,
			wantLimit:  21,
			wantOffset: 980,
			wantUpper:  980,
		},
		{
			name: "maximum validated page size keeps offset below one thousand",
			input: ListInput{
				Page:     99,
				PageSize: 100,
			},
			wantPage:   10,
			wantSize:   100,
			wantLimit:  101,
			wantOffset: 900,
			wantUpper:  900,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := readyClientIPStatsStore("UTC")
			service := newClientIPStatsTestService(store, fixedClientIPStatsNow)

			result, err := service.List(context.Background(), test.input)
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			if result.Page != test.wantPage || result.PageSize != test.wantSize ||
				result.PageUpperBound != test.wantUpper {
				t.Fatalf("List() pagination = page %d size %d upper %d", result.Page, result.PageSize, result.PageUpperBound)
			}
			if store.listInput.Limit != test.wantLimit || store.listInput.Offset != test.wantOffset {
				t.Fatalf("store pagination = limit %d offset %d", store.listInput.Limit, store.listInput.Offset)
			}
		})
	}
}

func TestServiceListKeepsDefaultSortDescendingWhenOnlyOrderIsProvided(t *testing.T) {
	tests := []struct {
		name      string
		input     ListInput
		wantField port.ManagementClientIPStatsSortField
		wantOrder port.ManagementClientIPStatsSortOrder
	}{
		{
			name: "order alone cannot change the default",
			input: ListInput{
				SortOrder: "asc",
			},
			wantField: port.ManagementClientIPStatsSortRequestCount,
			wantOrder: port.ManagementClientIPStatsSortDescending,
		},
		{
			name: "explicit field accepts ascending order",
			input: ListInput{
				SortField: "successCount",
				SortOrder: "asc",
			},
			wantField: port.ManagementClientIPStatsSortSuccessCount,
			wantOrder: port.ManagementClientIPStatsSortAscending,
		},
		{
			name: "explicit field defaults to descending order",
			input: ListInput{
				SortField: "totalCost",
			},
			wantField: port.ManagementClientIPStatsSortTotalCost,
			wantOrder: port.ManagementClientIPStatsSortDescending,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := readyClientIPStatsStore("UTC")
			service := newClientIPStatsTestService(store, fixedClientIPStatsNow)
			if _, err := service.List(context.Background(), test.input); err != nil {
				t.Fatalf("List() error = %v", err)
			}
			if store.listInput.SortField != test.wantField || store.listInput.SortOrder != test.wantOrder {
				t.Fatalf("store sort = %q %q", store.listInput.SortField, store.listInput.SortOrder)
			}
		})
	}
}

func TestNormalizeUsageRangeMatchesNodeFallbackAndClampRules(t *testing.T) {
	today := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		startDate string
		endDate   string
		want      UsageRange
	}{
		{
			name: "missing values default to today",
			want: UsageRange{StartDate: "2026-07-14", EndDate: "2026-07-14", Days: 1, MaxDays: 31},
		},
		{
			name:      "invalid calendar dates silently default to today",
			startDate: "2026-02-30",
			endDate:   "not-a-date",
			want:      UsageRange{StartDate: "2026-07-14", EndDate: "2026-07-14", Days: 1, MaxDays: 31},
		},
		{
			name:      "non ECMAScript whitespace does not reveal a hidden date",
			startDate: "\u00852026-07-01\u0085",
			endDate:   "\u00852026-07-01\u0085",
			want:      UsageRange{StartDate: "2026-07-14", EndDate: "2026-07-14", Days: 1, MaxDays: 31},
		},
		{
			name:      "range is limited to the latest thirty one days",
			startDate: "2020-01-01",
			endDate:   "2026-07-14",
			want:      UsageRange{StartDate: "2026-06-14", EndDate: "2026-07-14", Days: 31, MaxDays: 31},
		},
		{
			name:    "old end date is clamped before start is reconciled",
			endDate: "2026-05-01",
			want:    UsageRange{StartDate: "2026-06-14", EndDate: "2026-06-14", Days: 1, MaxDays: 31},
		},
		{
			name:      "start after end is reduced to end",
			startDate: "2026-07-12",
			endDate:   "2026-07-10",
			want:      UsageRange{StartDate: "2026-07-10", EndDate: "2026-07-10", Days: 1, MaxDays: 31},
		},
		{
			name:      "future dates are clamped to today",
			startDate: "2027-01-01",
			endDate:   "2027-02-01",
			want:      UsageRange{StartDate: "2026-07-14", EndDate: "2026-07-14", Days: 1, MaxDays: 31},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := normalizeUsageRange(test.startDate, test.endDate, today, time.UTC)
			if got != test.want {
				t.Fatalf("normalizeUsageRange() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestServiceListUsesConfiguredTimezoneAfterReadAcrossUTCDateBoundary(t *testing.T) {
	now := time.Date(2026, 7, 8, 15, 59, 59, 0, time.UTC)
	store := readyClientIPStatsStore("Asia/Shanghai")
	store.onTimezoneRead = func() {
		now = time.Date(2026, 7, 8, 16, 0, 1, 0, time.UTC)
	}
	service := newClientIPStatsTestService(store, func() time.Time { return now })

	result, err := service.List(context.Background(), ListInput{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if result.Range.StartDate != "2026-07-09" || result.Range.EndDate != "2026-07-09" {
		t.Fatalf("List() range = %+v", result.Range)
	}
	if !store.listInput.Now.Equal(now) {
		t.Fatalf("store now = %s, want %s", store.listInput.Now, now)
	}
}

func TestServiceListBuildsLastUsedTimezoneBoundariesAcrossDST(t *testing.T) {
	tests := []struct {
		name      string
		date      string
		now       time.Time
		wantStart string
		wantEnd   string
		wantHours time.Duration
	}{
		{
			name:      "spring forward day",
			date:      "2026-03-08",
			now:       time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC),
			wantStart: "2026-03-08T05:00:00.000Z",
			wantEnd:   "2026-03-09T04:00:00.000Z",
			wantHours: 23 * time.Hour,
		},
		{
			name:      "fall back day",
			date:      "2026-11-01",
			now:       time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC),
			wantStart: "2026-11-01T04:00:00.000Z",
			wantEnd:   "2026-11-02T05:00:00.000Z",
			wantHours: 25 * time.Hour,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := readyClientIPStatsStore("America/New_York")
			service := newClientIPStatsTestService(store, func() time.Time {
				return test.now
			})

			_, err := service.List(context.Background(), ListInput{
				LastUsedStartDate: test.date,
				LastUsedEndDate:   test.date,
			})
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			start := store.listInput.LastUsedStartAt
			end := store.listInput.LastUsedEndExclusiveAt
			if start == nil || end == nil {
				t.Fatal("last-used boundaries are nil")
			}
			if formatNodeISO(*start) != test.wantStart || formatNodeISO(*end) != test.wantEnd {
				t.Fatalf("last-used window = %s .. %s", formatNodeISO(*start), formatNodeISO(*end))
			}
			if end.Sub(*start) != test.wantHours {
				t.Fatalf("last-used duration = %s, want %s", end.Sub(*start), test.wantHours)
			}
		})
	}
}

func TestServiceListOnlyEnablesLastUsedFilterWhenBoundaryIsProvided(t *testing.T) {
	store := readyClientIPStatsStore("UTC")
	service := newClientIPStatsTestService(store, fixedClientIPStatsNow)
	if _, err := service.List(context.Background(), ListInput{}); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if store.listInput.LastUsedStartAt != nil || store.listInput.LastUsedEndExclusiveAt != nil {
		t.Fatalf("last-used window = %v .. %v, want disabled", store.listInput.LastUsedStartAt, store.listInput.LastUsedEndExclusiveAt)
	}

	if _, err := service.List(context.Background(), ListInput{LastUsedEndDate: "2026-07-10"}); err != nil {
		t.Fatalf("List() with end date error = %v", err)
	}
	if formatNodeISO(*store.listInput.LastUsedStartAt) != "2026-07-10T00:00:00.000Z" ||
		formatNodeISO(*store.listInput.LastUsedEndExclusiveAt) != "2026-07-11T00:00:00.000Z" {
		t.Fatalf("last-used end-only window = %s .. %s", formatNodeISO(*store.listInput.LastUsedStartAt), formatNodeISO(*store.listInput.LastUsedEndExclusiveAt))
	}
}

func TestServiceListKeepsRegistryRowsWhenRangeIsNotReady(t *testing.T) {
	store := readyClientIPStatsStore("UTC")
	store.listPage = port.ManagementClientIPStatsListPage{
		Rows: []port.ManagementClientIPStatsListRow{
			{IPHash: "registry_only", AggregateIPKey: "192.0.2.77"},
		},
		HasMore:    true,
		RangeReady: false,
	}
	service := newClientIPStatsTestService(store, fixedClientIPStatsNow)

	result, err := service.List(context.Background(), ListInput{Page: 2, PageSize: 10})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if result.Items == nil || len(result.Items) != 1 ||
		result.Items[0].IPHash != "registry_only" ||
		result.Items[0].RangeUsage.RequestCount != 0 ||
		result.PageUpperBound != 12 || !result.HasMore || result.RangeReady {
		t.Fatalf("List() not-ready result = %+v", result)
	}
	if result.Page != 2 || result.PageSize != 10 {
		t.Fatalf("List() page = %d size = %d", result.Page, result.PageSize)
	}
}

func TestServiceListMapsNodeDTOAndOptionalFields(t *testing.T) {
	averageDuration := 12.5
	notFinite := math.Inf(1)
	maxDuration := int64(42)
	zeroDuration := int64(0)
	lastUsedAt := "2026-07-14T10:00:00.000Z"
	lastErrorAt := "2026-07-14T09:00:00.000Z"
	store := readyClientIPStatsStore("UTC")
	store.listPage = port.ManagementClientIPStatsListPage{
		Rows: []port.ManagementClientIPStatsListRow{
			{
				IPHash:         "hash-blacklisted",
				AggregateIPKey: "203.0.113.0/24",
				LastSeenAt:     "2026-07-14T11:00:00.000Z",
				Status:         port.ManagementClientIPStatsStatusBlacklisted,
				RangeUsage: port.ManagementClientIPUsageSummary{
					RequestCount:        4,
					SuccessCount:        3,
					ErrorCount:          1,
					ErrorRate:           99,
					InputTokens:         10,
					OutputTokens:        5,
					CacheReadTokens:     6,
					CacheReadCost:       0.1,
					CacheWriteTokens:    7,
					CacheWrite1hTokens:  2,
					CacheWriteCost:      0.2,
					ThinkingTokens:      8,
					InputImageTokens:    9,
					OutputImageTokens:   10,
					TotalTokens:         999,
					TotalCost:           1.25,
					ActiveDays:          3,
					AverageDurationMs:   &averageDuration,
					AverageFirstTokenMs: &notFinite,
					MaxDurationMs:       &maxDuration,
					LastUsedAt:          &lastUsedAt,
					LastErrorAt:         &lastErrorAt,
				},
			},
			{
				IPHash:         "hash-allowlisted",
				AggregateIPKey: "198.51.100.0/24",
				Status:         port.ManagementClientIPStatsStatusAllowlisted,
				RangeUsage: port.ManagementClientIPUsageSummary{
					MaxDurationMs: &zeroDuration,
				},
			},
		},
		HasMore:    true,
		RangeReady: true,
	}
	service := newClientIPStatsTestService(store, fixedClientIPStatsNow)

	result, err := service.List(context.Background(), ListInput{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(result.Items) != 2 || result.PageUpperBound != 3 || !result.HasMore || !result.RangeReady {
		t.Fatalf("List() result = %+v", result)
	}
	first := result.Items[0]
	if first.Status != "blacklisted" || first.RangeUsage.ErrorRate != 0.25 || first.RangeUsage.TotalTokens != 15 {
		t.Fatalf("first item derived fields = %+v", first)
	}
	if first.RangeUsage.AverageDurationMs == nil || *first.RangeUsage.AverageDurationMs != averageDuration ||
		first.RangeUsage.AverageFirstTokenMs != nil || first.RangeUsage.MaxDurationMs == nil ||
		*first.RangeUsage.MaxDurationMs != maxDuration {
		t.Fatalf("first item optional metrics = %+v", first.RangeUsage)
	}
	if result.Items[1].Status != "allowlisted" || result.Items[1].LastSeenAt == nil ||
		*result.Items[1].LastSeenAt != "" ||
		result.Items[1].RangeUsage.MaxDurationMs != nil {
		t.Fatalf("second item optional fields = %+v", result.Items[1])
	}

	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	items := payload["items"].([]any)
	firstUsage := items[0].(map[string]any)["rangeUsage"].(map[string]any)
	if _, exists := firstUsage["averageFirstTokenMs"]; exists {
		t.Fatalf("non-finite averageFirstTokenMs leaked into JSON: %s", raw)
	}
	second := items[1].(map[string]any)
	if value, exists := second["lastSeenAt"]; !exists || value != "" {
		t.Fatalf("empty lastSeenAt was not preserved in JSON: %s", raw)
	}
}

func TestServiceListTrimsStoreFiltersAndDefaultsStatus(t *testing.T) {
	store := readyClientIPStatsStore("UTC")
	service := newClientIPStatsTestService(store, fixedClientIPStatsNow)
	if _, err := service.List(context.Background(), ListInput{Keyword: "  203.0.113  "}); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if store.listInput.Keyword != "203.0.113" || store.listInput.Status != port.ManagementClientIPStatsStatusAll {
		t.Fatalf("store filters = keyword %q status %q", store.listInput.Keyword, store.listInput.Status)
	}
}

func TestServiceListPreservesNonECMAScriptWhitespaceFilters(t *testing.T) {
	const nonECMAScriptWhitespace = "\u0085"
	store := readyClientIPStatsStore("UTC")
	service := newClientIPStatsTestService(store, fixedClientIPStatsNow)
	if _, err := service.List(context.Background(), ListInput{
		Keyword:           nonECMAScriptWhitespace,
		LastUsedStartDate: nonECMAScriptWhitespace,
	}); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if store.listInput.Keyword != nonECMAScriptWhitespace {
		t.Fatalf("keyword = %q, want preserved U+0085", store.listInput.Keyword)
	}
	if store.listInput.LastUsedStartAt == nil || store.listInput.LastUsedEndExclusiveAt == nil {
		t.Fatal("U+0085 last-used input must remain provided and normalize to today's window")
	}
}

func TestServiceListPropagatesDependenciesAndRejectsMissingTimezone(t *testing.T) {
	timezoneErr := errors.New("timezone read failed")
	listErr := errors.New("list failed")
	tests := []struct {
		name      string
		store     *clientIPStatsStoreStub
		service   *Service
		wantError error
		wantText  string
	}{
		{
			name:     "missing list reader",
			service:  NewServiceWithOptions(ServiceOptions{UsageStatsTimezoneReader: readyClientIPStatsStore("UTC")}),
			wantText: "list reader is required",
		},
		{
			name:     "missing timezone reader",
			service:  NewServiceWithOptions(ServiceOptions{ListReader: readyClientIPStatsStore("UTC")}),
			wantText: "timezone reader is required",
		},
		{
			name: "timezone error",
			store: &clientIPStatsStoreStub{
				timezoneErr: timezoneErr,
			},
			wantError: timezoneErr,
		},
		{
			name: "missing timezone setting",
			store: &clientIPStatsStoreStub{
				timezoneFound: false,
			},
			wantText: "缺少 usageStatsTimezone",
		},
		{
			name: "invalid timezone setting",
			store: &clientIPStatsStoreStub{
				timezone:      "Invalid/Timezone",
				timezoneFound: true,
			},
			wantText: "usageStatsTimezone 无效",
		},
		{
			name: "list error",
			store: &clientIPStatsStoreStub{
				timezone:      "UTC",
				timezoneFound: true,
				listErr:       listErr,
			},
			wantError: listErr,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := test.service
			if service == nil {
				service = newClientIPStatsTestService(test.store, fixedClientIPStatsNow)
			}
			_, err := service.List(context.Background(), ListInput{})
			if test.wantError != nil && !errors.Is(err, test.wantError) {
				t.Fatalf("List() error = %v, want %v", err, test.wantError)
			}
			if test.wantText != "" && (err == nil || !strings.Contains(err.Error(), test.wantText)) {
				t.Fatalf("List() error = %v, want text %q", err, test.wantText)
			}
		})
	}
}

var fixedClientIPStatsNow = func() time.Time {
	return time.Date(2026, 7, 14, 12, 34, 56, 789000000, time.UTC)
}

type clientIPStatsStoreStub struct {
	timezone       string
	timezoneFound  bool
	timezoneErr    error
	onTimezoneRead func()
	listPage       port.ManagementClientIPStatsListPage
	listErr        error
	listInput      port.ManagementClientIPStatsListInput
	listCalls      int
}

func readyClientIPStatsStore(timezone string) *clientIPStatsStoreStub {
	return &clientIPStatsStoreStub{
		timezone:      timezone,
		timezoneFound: true,
		listPage: port.ManagementClientIPStatsListPage{
			Rows:       []port.ManagementClientIPStatsListRow{},
			RangeReady: true,
		},
	}
}

func newClientIPStatsTestService(store *clientIPStatsStoreStub, now func() time.Time) *Service {
	return NewServiceWithOptions(ServiceOptions{
		ListReader:               store,
		UsageStatsTimezoneReader: store,
		Now:                      now,
	})
}

func (s *clientIPStatsStoreStub) GetManagementUsageStatsTimezone(context.Context) (string, bool, error) {
	if s.onTimezoneRead != nil {
		s.onTimezoneRead()
	}
	return s.timezone, s.timezoneFound, s.timezoneErr
}

func (s *clientIPStatsStoreStub) ListManagementClientIPStats(
	_ context.Context,
	input port.ManagementClientIPStatsListInput,
) (port.ManagementClientIPStatsListPage, error) {
	s.listCalls++
	s.listInput = input
	return s.listPage, s.listErr
}

func formatNodeISO(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}

var _ port.ManagementClientIPStatsListReader = (*clientIPStatsStoreStub)(nil)
var _ port.ManagementUsageStatsTimezoneReader = (*clientIPStatsStoreStub)(nil)
