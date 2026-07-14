//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"juhe-ai/backend-go/internal/modules/managementclientipstats"
)

const (
	w6ManagementClientIPStatsPrimaryAccountID     = "acct_node_go_ip_stats_primary"
	w6ManagementClientIPStatsPrimaryAccountName   = "Node Go IP Stats Primary"
	w6ManagementClientIPStatsPrimaryOwnerID       = "sys_node_go_ip_stats_primary"
	w6ManagementClientIPStatsPrimaryOwnerName     = "Node Go IP Stats Primary Owner"
	w6ManagementClientIPStatsSecondaryAccountID   = "acct_node_go_ip_stats_secondary"
	w6ManagementClientIPStatsSecondaryAccountName = "Node Go IP Stats Secondary"
	w6ManagementClientIPStatsSecondaryOwnerID     = "sys_node_go_ip_stats_secondary"
	w6ManagementClientIPStatsSecondaryOwnerName   = "Node Go IP Stats Secondary Owner"
)

type w6ManagementClientIPStatsNodeWriterDetailUsageExpected struct {
	RequestCount        int64   `json:"requestCount"`
	SuccessCount        int64   `json:"successCount"`
	ErrorCount          int64   `json:"errorCount"`
	ErrorRate           float64 `json:"errorRate"`
	InputTokens         int64   `json:"inputTokens"`
	OutputTokens        int64   `json:"outputTokens"`
	CacheReadTokens     int64   `json:"cacheReadTokens"`
	CacheReadCost       float64 `json:"cacheReadCost"`
	CacheWriteTokens    int64   `json:"cacheWriteTokens"`
	CacheWrite1hTokens  int64   `json:"cacheWrite1hTokens"`
	CacheWriteCost      float64 `json:"cacheWriteCost"`
	ThinkingTokens      int64   `json:"thinkingTokens"`
	InputImageTokens    int64   `json:"inputImageTokens"`
	OutputImageTokens   int64   `json:"outputImageTokens"`
	TotalTokens         int64   `json:"totalTokens"`
	TotalCost           float64 `json:"totalCost"`
	ActiveDays          int     `json:"activeDays"`
	AverageDurationMs   float64 `json:"averageDurationMs"`
	AverageFirstTokenMs float64 `json:"averageFirstTokenMs"`
	MaxDurationMs       int64   `json:"maxDurationMs"`
	LastUsedAt          string  `json:"lastUsedAt"`
	LastErrorAt         *string `json:"lastErrorAt,omitempty"`
}

type w6ManagementClientIPStatsNodeWriterDetailAccountExpected struct {
	AccountID                     string                                                 `json:"accountId"`
	AccountName                   string                                                 `json:"accountName"`
	AccountOwnerSystemAccountID   string                                                 `json:"accountOwnerSystemAccountId"`
	AccountOwnerSystemAccountName string                                                 `json:"accountOwnerSystemAccountName"`
	RangeUsage                    w6ManagementClientIPStatsNodeWriterDetailUsageExpected `json:"rangeUsage"`
}

type w6ManagementClientIPStatsNodeWriterDetailEnvelope struct {
	Data json.RawMessage `json:"data"`
}

func w6ManagementClientIPStatsAssertNodeWriterDetail(
	t *testing.T,
	ctx context.Context,
	server *httptest.Server,
	fixture w6ManagementClientIPStatsNodeWriterFixture,
) {
	t.Helper()

	detailAccounts := w6ManagementClientIPStatsNodeWriterDetailExpected(fixture)
	primary := detailAccounts[0]
	secondary := detailAccounts[1]

	firstPage, firstRaw := w6ManagementClientIPStatsRequestNodeWriterDetail(
		t, ctx, server, fixture, 1, 1, "requestCount", "desc",
	)
	w6ManagementClientIPStatsAssertNodeWriterDetailPage(t, firstPage, fixture, 1, 1, 2, true, primary)
	w6ManagementClientIPStatsAssertNodeWriterDetailRaw(t, firstRaw, primary)

	secondPage, secondRaw := w6ManagementClientIPStatsRequestNodeWriterDetail(
		t, ctx, server, fixture, 2, 1, "requestCount", "desc",
	)
	w6ManagementClientIPStatsAssertNodeWriterDetailPage(t, secondPage, fixture, 2, 1, 2, false, secondary)
	w6ManagementClientIPStatsAssertNodeWriterDetailRaw(t, secondRaw, secondary)

	ascending, _ := w6ManagementClientIPStatsRequestNodeWriterDetail(
		t, ctx, server, fixture, 1, 2, "", "asc",
	)
	if ascending.Page != 1 || ascending.PageSize != 2 || ascending.PageUpperBound != 2 || ascending.HasMore {
		t.Fatalf("Go client IP stats detail ascending pagination = %+v", ascending)
	}
	if len(ascending.Items) != 2 ||
		ascending.Items[0].AccountID != secondary.AccountID ||
		ascending.Items[1].AccountID != primary.AccountID {
		t.Fatalf(
			"Go client IP stats detail default ascending order = %+v, want %q then %q",
			ascending.Items,
			secondary.AccountID,
			primary.AccountID,
		)
	}
}

func w6ManagementClientIPStatsNodeWriterDetailExpected(
	fixture w6ManagementClientIPStatsNodeWriterFixture,
) [2]w6ManagementClientIPStatsNodeWriterDetailAccountExpected {
	lastErrorAt := fixture.Expected.LastErrorAt
	return [2]w6ManagementClientIPStatsNodeWriterDetailAccountExpected{
		{
			AccountID:                     w6ManagementClientIPStatsPrimaryAccountID,
			AccountName:                   w6ManagementClientIPStatsPrimaryAccountName,
			AccountOwnerSystemAccountID:   w6ManagementClientIPStatsPrimaryOwnerID,
			AccountOwnerSystemAccountName: w6ManagementClientIPStatsPrimaryOwnerName,
			RangeUsage: w6ManagementClientIPStatsNodeWriterDetailUsageExpected{
				RequestCount:        2,
				SuccessCount:        1,
				ErrorCount:          1,
				ErrorRate:           0.5,
				InputTokens:         303,
				OutputTokens:        33,
				CacheReadTokens:     39,
				CacheReadCost:       0.0039,
				CacheWriteTokens:    51,
				CacheWrite1hTokens:  57,
				CacheWriteCost:      0.0051,
				ThinkingTokens:      69,
				InputImageTokens:    87,
				OutputImageTokens:   93,
				TotalTokens:         336,
				TotalCost:           0.0303,
				ActiveDays:          1,
				AverageDurationMs:   178.5,
				AverageFirstTokenMs: 23,
				MaxDurationMs:       246,
				LastUsedAt:          fixture.Expected.LastErrorAt,
				LastErrorAt:         &lastErrorAt,
			},
		},
		{
			AccountID:                     w6ManagementClientIPStatsSecondaryAccountID,
			AccountName:                   w6ManagementClientIPStatsSecondaryAccountName,
			AccountOwnerSystemAccountID:   w6ManagementClientIPStatsSecondaryOwnerID,
			AccountOwnerSystemAccountName: w6ManagementClientIPStatsSecondaryOwnerName,
			RangeUsage: w6ManagementClientIPStatsNodeWriterDetailUsageExpected{
				RequestCount:        1,
				SuccessCount:        1,
				ErrorCount:          0,
				ErrorRate:           0,
				InputTokens:         303,
				OutputTokens:        33,
				CacheReadTokens:     39,
				CacheReadCost:       0.0039,
				CacheWriteTokens:    51,
				CacheWrite1hTokens:  57,
				CacheWriteCost:      0.0051,
				ThinkingTokens:      69,
				InputImageTokens:    87,
				OutputImageTokens:   93,
				TotalTokens:         336,
				TotalCost:           0.0303,
				ActiveDays:          1,
				AverageDurationMs:   357,
				AverageFirstTokenMs: 43,
				MaxDurationMs:       357,
				LastUsedAt:          fixture.Expected.LastUsedAt,
			},
		},
	}
}

func w6ManagementClientIPStatsRequestNodeWriterDetail(
	t *testing.T,
	ctx context.Context,
	server *httptest.Server,
	fixture w6ManagementClientIPStatsNodeWriterFixture,
	page int,
	pageSize int,
	sortField string,
	sortOrder string,
) (managementclientipstats.DetailResult, map[string]any) {
	t.Helper()

	query := url.Values{
		"page":      {fmt.Sprint(page)},
		"pageSize":  {fmt.Sprint(pageSize)},
		"startDate": {fixture.StartDate},
		"endDate":   {fixture.EndDate},
		"sortOrder": {sortOrder},
	}
	if sortField != "" {
		query.Set("sortField", sortField)
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		server.URL+"/__aisys__/api/ip-stats/"+url.PathEscape(fixture.IPHash)+"/detail?"+query.Encode(),
		nil,
	)
	if err != nil {
		t.Fatalf("build Go client IP stats detail request: %v", err)
	}
	request.Header.Set("Cookie", "juhe_ai_session=node-go-ip-stats-session")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("request production Go client IP stats detail route: %v", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, w6ManagementClientIPStatsNodeWriterBodyLimit+1))
	if err != nil {
		t.Fatalf("read Go client IP stats detail response: %v", err)
	}
	if len(body) > w6ManagementClientIPStatsNodeWriterBodyLimit {
		t.Fatalf("Go client IP stats detail response exceeded %d bytes", w6ManagementClientIPStatsNodeWriterBodyLimit)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Go client IP stats detail status = %d; body = %s", response.StatusCode, body)
	}
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("Go client IP stats detail Cache-Control = %q, want no-store", response.Header.Get("Cache-Control"))
	}

	var envelope w6ManagementClientIPStatsNodeWriterDetailEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode Go client IP stats detail envelope: %v; body = %s", err, body)
	}
	var result managementclientipstats.DetailResult
	if err := json.Unmarshal(envelope.Data, &result); err != nil {
		t.Fatalf("decode Go client IP stats detail result: %v; body = %s", err, body)
	}
	var raw map[string]any
	if err := json.Unmarshal(envelope.Data, &raw); err != nil {
		t.Fatalf("decode raw Go client IP stats detail result: %v; body = %s", err, body)
	}
	return result, raw
}

func w6ManagementClientIPStatsAssertNodeWriterDetailPage(
	t *testing.T,
	result managementclientipstats.DetailResult,
	fixture w6ManagementClientIPStatsNodeWriterFixture,
	wantPage int,
	wantPageSize int,
	wantPageUpperBound int,
	wantHasMore bool,
	want w6ManagementClientIPStatsNodeWriterDetailAccountExpected,
) {
	t.Helper()

	if !result.RangeReady {
		t.Fatal("Go client IP stats detail rangeReady = false after Node production refresh")
	}
	if result.IPHash != fixture.IPHash || result.AggregateIPKey != fixture.AggregateIPKey {
		t.Fatalf("Go client IP stats detail identity = %+v", result)
	}
	w6ManagementClientIPStatsAssertStringPointer(t, "detail lastSeenAt", result.LastSeenAt, fixture.Expected.LastSeenAt)
	if result.Page != wantPage || result.PageSize != wantPageSize ||
		result.PageUpperBound != wantPageUpperBound || result.HasMore != wantHasMore {
		t.Fatalf("Go client IP stats detail pagination = %+v", result)
	}
	if result.Range.StartDate != fixture.StartDate || result.Range.EndDate != fixture.EndDate ||
		result.Range.Days != 1 || result.Range.MaxDays != 31 {
		t.Fatalf("Go client IP stats detail range = %+v", result.Range)
	}
	if len(result.Items) != 1 {
		t.Fatalf("Go client IP stats detail items = %d, want 1: %+v", len(result.Items), result.Items)
	}
	item := result.Items[0]
	if item.AccountID != want.AccountID {
		t.Fatalf("Go client IP stats detail account ID = %q, want %q", item.AccountID, want.AccountID)
	}
	w6ManagementClientIPStatsAssertStringPointer(t, "detail accountName", item.AccountName, want.AccountName)
	w6ManagementClientIPStatsAssertStringPointer(
		t, "detail accountOwnerSystemAccountId", item.AccountOwnerSystemAccountID, want.AccountOwnerSystemAccountID,
	)
	w6ManagementClientIPStatsAssertStringPointer(
		t, "detail accountOwnerSystemAccountName", item.AccountOwnerSystemAccountName, want.AccountOwnerSystemAccountName,
	)
	w6ManagementClientIPStatsAssertNodeWriterDetailUsage(t, item.RangeUsage, want.RangeUsage)
}

func w6ManagementClientIPStatsAssertNodeWriterDetailUsage(
	t *testing.T,
	usage managementclientipstats.UsageSummary,
	want w6ManagementClientIPStatsNodeWriterDetailUsageExpected,
) {
	t.Helper()

	if usage.RequestCount != want.RequestCount ||
		usage.SuccessCount != want.SuccessCount ||
		usage.ErrorCount != want.ErrorCount ||
		usage.InputTokens != want.InputTokens ||
		usage.OutputTokens != want.OutputTokens ||
		usage.CacheReadTokens != want.CacheReadTokens ||
		usage.CacheWriteTokens != want.CacheWriteTokens ||
		usage.CacheWrite1hTokens != want.CacheWrite1hTokens ||
		usage.ThinkingTokens != want.ThinkingTokens ||
		usage.InputImageTokens != want.InputImageTokens ||
		usage.OutputImageTokens != want.OutputImageTokens ||
		usage.TotalTokens != want.TotalTokens ||
		usage.ActiveDays != want.ActiveDays {
		t.Fatalf("Go client IP stats detail integer metrics = %+v, want %+v", usage, want)
	}
	w6ManagementClientIPStatsAssertFloat(t, "detail errorRate", usage.ErrorRate, want.ErrorRate)
	w6ManagementClientIPStatsAssertFloat(t, "detail cacheReadCost", usage.CacheReadCost, want.CacheReadCost)
	w6ManagementClientIPStatsAssertFloat(t, "detail cacheWriteCost", usage.CacheWriteCost, want.CacheWriteCost)
	w6ManagementClientIPStatsAssertFloat(t, "detail totalCost", usage.TotalCost, want.TotalCost)
	w6ManagementClientIPStatsAssertFloatPointer(t, "detail averageDurationMs", usage.AverageDurationMs, want.AverageDurationMs)
	w6ManagementClientIPStatsAssertFloatPointer(
		t, "detail averageFirstTokenMs", usage.AverageFirstTokenMs, want.AverageFirstTokenMs,
	)
	if usage.MaxDurationMs == nil || *usage.MaxDurationMs != want.MaxDurationMs {
		t.Fatalf("Go client IP stats detail maxDurationMs = %v, want %d", usage.MaxDurationMs, want.MaxDurationMs)
	}
	w6ManagementClientIPStatsAssertStringPointer(t, "detail lastUsedAt", usage.LastUsedAt, want.LastUsedAt)
	if want.LastErrorAt == nil {
		if usage.LastErrorAt != nil {
			t.Fatalf("Go client IP stats detail lastErrorAt = %v, want omitted", usage.LastErrorAt)
		}
		return
	}
	w6ManagementClientIPStatsAssertStringPointer(t, "detail lastErrorAt", usage.LastErrorAt, *want.LastErrorAt)
}

func w6ManagementClientIPStatsAssertNodeWriterDetailRaw(
	t *testing.T,
	raw map[string]any,
	want w6ManagementClientIPStatsNodeWriterDetailAccountExpected,
) {
	t.Helper()

	if _, ok := raw["lastSeenAt"].(string); !ok {
		t.Fatalf("Go client IP stats detail raw lastSeenAt = %#v", raw["lastSeenAt"])
	}
	items, ok := raw["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("Go client IP stats detail raw items = %#v", raw["items"])
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("Go client IP stats detail raw item = %#v", items[0])
	}
	for key, expected := range map[string]string{
		"accountName":                   want.AccountName,
		"accountOwnerSystemAccountId":   want.AccountOwnerSystemAccountID,
		"accountOwnerSystemAccountName": want.AccountOwnerSystemAccountName,
	} {
		if item[key] != expected {
			t.Fatalf("Go client IP stats detail raw %s = %#v, want %q", key, item[key], expected)
		}
	}
	usage, ok := item["rangeUsage"].(map[string]any)
	if !ok {
		t.Fatalf("Go client IP stats detail raw rangeUsage = %#v", item["rangeUsage"])
	}
	for _, key := range []string{"averageDurationMs", "averageFirstTokenMs", "maxDurationMs", "lastUsedAt"} {
		if value, found := usage[key]; !found || value == nil {
			t.Fatalf("Go client IP stats detail raw optional field %s = %#v, found %v", key, value, found)
		}
	}
	lastErrorAt, found := usage["lastErrorAt"]
	if want.RangeUsage.LastErrorAt == nil {
		if found {
			t.Fatalf("Go client IP stats detail raw lastErrorAt = %#v, want omitted", lastErrorAt)
		}
		return
	}
	if !found || lastErrorAt != *want.RangeUsage.LastErrorAt {
		t.Fatalf("Go client IP stats detail raw lastErrorAt = %#v, want %q", lastErrorAt, *want.RangeUsage.LastErrorAt)
	}
}
