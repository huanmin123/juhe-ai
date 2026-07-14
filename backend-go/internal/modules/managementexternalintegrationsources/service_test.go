package managementexternalintegrationsources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/publicapi"
	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceListTruncatesSentinelBeforeTokenEnrichment(t *testing.T) {
	baseTime := time.Date(2026, 7, 15, 8, 9, 10, 345678901, time.FixedZone("UTC+8", 8*60*60))
	rows := make([]port.ManagementExternalIntegrationSourceListRow, 0, defaultPageSize+1)
	for i := 0; i < defaultPageSize+1; i++ {
		id := fmt.Sprintf("source_%02d", i)
		if i == 0 {
			id = publicapi.BuiltInTestSourceID
		}
		if i == defaultPageSize {
			id = "source_sentinel"
		}
		rows = append(rows, validSourceRow(id, baseTime.Add(-time.Duration(i)*time.Minute)))
	}
	notes := "built-in notes"
	expiresAt := baseTime.Add(24 * time.Hour)
	lastUsedAt := baseTime.Add(-time.Minute)
	rows[0].ScopesJSON = fmt.Sprintf(
		`[%q,%q,%q,%q]`,
		" "+publicapi.ScopeGroupListRead+" ",
		"unknown:scope",
		publicapi.ScopeAccountListRead,
		publicapi.ScopeGroupListRead,
	)
	rows[0].RateLimitsJSON = `[{"windowSeconds":60.0,"maxRequests":100},{"maxRequests":2,"windowSeconds":1e0}]`
	rows[0].ExpiresAt = &expiresAt
	rows[0].Notes = &notes
	rows[0].LastUsedAt = &lastUsedAt

	tokenExpiresAt := baseTime.Add(-time.Hour)
	tokenLastUsedAt := baseTime.Add(-time.Minute)
	store := &externalIntegrationSourceStoreStub{
		sourceRows: rows,
		statsRows: []port.ManagementExternalIntegrationSourceTokenStatsRow{{
			SourceRefID:      publicapi.BuiltInTestSourceID,
			TokenCount:       3,
			ActiveTokenCount: 2,
		}},
		primaryRows: []port.ManagementExternalIntegrationSourcePrimaryTokenRow{{
			SourceRefID: publicapi.BuiltInTestSourceID,
			ID:          publicapi.BuiltInTestTokenID,
			Name:        "Expired active token",
			TokenPrefix: "juis_pre",
			TokenSuffix: "suffix01",
			Status:      publicapi.TokenStatusActive,
			ScopesJSON:  fmt.Sprintf(`[%q,%q]`, publicapi.ScopeGroupListRead, "removed:scope"),
			ExpiresAt:   &tokenExpiresAt,
			LastUsedAt:  &tokenLastUsedAt,
			CreatedAt:   baseTime.Add(-2 * time.Hour),
			UpdatedAt:   baseTime,
		}},
	}

	result, err := NewService(store).List(context.Background(), ListInput{Keyword: "  MiXeD%_  "})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(store.sourceCalls) != 1 {
		t.Fatalf("source calls = %d, want 1", len(store.sourceCalls))
	}
	if got := store.sourceCalls[0]; got.Status != "all" || got.Keyword != "mixed%_" || got.Limit != 21 || got.Offset != 0 {
		t.Fatalf("source list input = %#v", got)
	}
	if len(store.statsCalls) != 1 || len(store.primaryCalls) != 1 {
		t.Fatalf("token enrichment calls = stats:%d primary:%d", len(store.statsCalls), len(store.primaryCalls))
	}
	wantIDs := make([]string, 0, defaultPageSize)
	for _, row := range rows[:defaultPageSize] {
		wantIDs = append(wantIDs, row.ID)
	}
	if !reflect.DeepEqual(store.statsCalls[0], wantIDs) || !reflect.DeepEqual(store.primaryCalls[0], wantIDs) {
		t.Fatalf("token enrichment IDs = stats:%#v primary:%#v, want %#v", store.statsCalls[0], store.primaryCalls[0], wantIDs)
	}
	if containsString(store.statsCalls[0], "source_sentinel") || containsString(store.primaryCalls[0], "source_sentinel") {
		t.Fatal("pageSize+1 sentinel must not enter token enrichment queries")
	}
	if len(result.Items) != defaultPageSize || !result.HasMore || result.Page != 1 || result.PageSize != 20 || result.PageUpperBound != 21 {
		t.Fatalf("list result = %#v", result)
	}

	item := result.Items[0]
	wantScopes := []string{publicapi.ScopeAccountListRead, publicapi.ScopeGroupListRead}
	if !reflect.DeepEqual(item.Scopes, wantScopes) {
		t.Fatalf("source scopes = %#v, want %#v", item.Scopes, wantScopes)
	}
	wantRateLimits := []RateLimitRule{{WindowSeconds: 1, MaxRequests: 2}, {WindowSeconds: 60, MaxRequests: 100}}
	if !reflect.DeepEqual(item.RateLimits, wantRateLimits) {
		t.Fatalf("rate limits = %#v, want %#v", item.RateLimits, wantRateLimits)
	}
	if item.TokenCount != 3 || item.ActiveTokenCount != 2 || !item.IsBuiltIn {
		t.Fatalf("source summary = %#v", item)
	}
	if item.ExpiresAt == nil || *item.ExpiresAt != "2026-07-16T00:09:10.345Z" ||
		item.LastUsedAt == nil || *item.LastUsedAt != "2026-07-15T00:08:10.345Z" ||
		item.CreatedAt != "2026-07-15T00:09:10.345Z" ||
		item.UpdatedAt != "2026-07-15T00:09:10.345Z" {
		t.Fatalf("source times = %#v", item)
	}
	if item.Notes == nil || *item.Notes != notes {
		t.Fatalf("source notes = %#v", item.Notes)
	}
	if item.PrimaryToken == nil {
		t.Fatal("primary token is nil")
	}
	primary := item.PrimaryToken
	if primary.ID != publicapi.BuiltInTestTokenID || primary.Status != publicapi.TokenStatusActive || !primary.IsBuiltIn {
		t.Fatalf("primary token = %#v", primary)
	}
	if primary.ExpiresAt == nil || *primary.ExpiresAt != "2026-07-14T23:09:10.345Z" {
		t.Fatalf("expired active primary token expiry = %#v", primary.ExpiresAt)
	}
	if !reflect.DeepEqual(primary.Scopes, []string{publicapi.ScopeGroupListRead}) {
		t.Fatalf("primary token scopes = %#v", primary.Scopes)
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal list result: %v", err)
	}
	for _, forbidden := range []string{"tokenHash", "tokenSecretEncrypted", "\"token\":"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("list DTO leaked forbidden field %q: %s", forbidden, encoded)
		}
	}
}

func TestServiceListNormalizesThreeStatusesAndPaginationWindow(t *testing.T) {
	tests := []struct {
		name           string
		input          ListInput
		wantStatus     string
		wantKeyword    string
		wantPage       int
		wantPageSize   int
		wantOffset     int
		wantLimit      int
		wantUpperBound int
	}{
		{
			name:           "all defaults",
			input:          ListInput{Status: " all "},
			wantStatus:     "all",
			wantPage:       1,
			wantPageSize:   20,
			wantLimit:      21,
			wantUpperBound: 0,
		},
		{
			name:           "active floor page cap",
			input:          ListInput{Status: "active", Keyword: "  Prefix  ", Page: 999, PageSize: 64},
			wantStatus:     "active",
			wantKeyword:    "prefix",
			wantPage:       15,
			wantPageSize:   64,
			wantOffset:     896,
			wantLimit:      65,
			wantUpperBound: 896,
		},
		{
			name:           "disabled page size cap",
			input:          ListInput{Status: "disabled", Page: 99, PageSize: 500},
			wantStatus:     "disabled",
			wantPage:       10,
			wantPageSize:   100,
			wantOffset:     900,
			wantLimit:      101,
			wantUpperBound: 900,
		},
		{
			name:           "explicit zero page size clamps",
			input:          ListInput{Status: "all", Page: 2000, PageSizeProvided: true},
			wantStatus:     "all",
			wantPage:       1000,
			wantPageSize:   1,
			wantOffset:     999,
			wantLimit:      2,
			wantUpperBound: 999,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &externalIntegrationSourceStoreStub{}
			result, err := NewService(store).List(context.Background(), test.input)
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			if len(store.sourceCalls) != 1 {
				t.Fatalf("source calls = %d, want 1", len(store.sourceCalls))
			}
			call := store.sourceCalls[0]
			if call.Status != test.wantStatus || call.Keyword != test.wantKeyword || call.Offset != test.wantOffset || call.Limit != test.wantLimit {
				t.Fatalf("source input = %#v", call)
			}
			if result.Page != test.wantPage || result.PageSize != test.wantPageSize || result.PageUpperBound != test.wantUpperBound || result.HasMore {
				t.Fatalf("result = %#v", result)
			}
			if result.Items == nil {
				t.Fatal("empty result items must be an empty array")
			}
			if len(store.statsCalls) != 0 || len(store.primaryCalls) != 0 {
				t.Fatal("empty page must not query token stats or primary tokens")
			}
		})
	}
}

func TestServiceListRejectsInvalidFilterStatus(t *testing.T) {
	store := &externalIntegrationSourceStoreStub{}
	_, err := NewService(store).List(context.Background(), ListInput{Status: "ACTIVE"})
	if !errors.Is(err, ErrInvalidListStatus) {
		t.Fatalf("List() error = %v, want invalid status", err)
	}
	if len(store.sourceCalls) != 0 {
		t.Fatal("invalid status must fail before storage")
	}
}

func TestServiceListReturnsSourceJSONAndStatusErrorsWithoutTokenFallback(t *testing.T) {
	now := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		mutate    func(*port.ManagementExternalIntegrationSourceListRow)
		wantError string
	}{
		{name: "invalid source status", mutate: func(row *port.ManagementExternalIntegrationSourceListRow) { row.Status = "pending" }, wantError: "来源系统状态无效"},
		{name: "scopes must be array", mutate: func(row *port.ManagementExternalIntegrationSourceListRow) { row.ScopesJSON = `{}` }, wantError: "scopes_json 必须是数组"},
		{name: "scope must be string", mutate: func(row *port.ManagementExternalIntegrationSourceListRow) { row.ScopesJSON = `[1]` }, wantError: "scopes_json 必须是字符串数组"},
		{name: "rate limits must be array", mutate: func(row *port.ManagementExternalIntegrationSourceListRow) { row.RateLimitsJSON = `{}` }, wantError: "rate_limits_json 必须是数组"},
		{name: "rate limit count", mutate: func(row *port.ManagementExternalIntegrationSourceListRow) { row.RateLimitsJSON = nineRateLimitsJSON() }, wantError: "最多 8 条"},
		{name: "rate limit object", mutate: func(row *port.ManagementExternalIntegrationSourceListRow) { row.RateLimitsJSON = `[1]` }, wantError: "必须是对象"},
		{name: "rate limit unknown key", mutate: func(row *port.ManagementExternalIntegrationSourceListRow) {
			row.RateLimitsJSON = `[{"windowSeconds":1,"maxRequests":2,"extra":3}]`
		}, wantError: "只能包含"},
		{name: "rate limit missing key", mutate: func(row *port.ManagementExternalIntegrationSourceListRow) {
			row.RateLimitsJSON = `[{"windowSeconds":1}]`
		}, wantError: "只能包含"},
		{name: "rate limit fractional", mutate: func(row *port.ManagementExternalIntegrationSourceListRow) {
			row.RateLimitsJSON = `[{"windowSeconds":1.5,"maxRequests":2}]`
		}, wantError: "必须是整数"},
		{name: "rate limit window range", mutate: func(row *port.ManagementExternalIntegrationSourceListRow) {
			row.RateLimitsJSON = `[{"windowSeconds":86401,"maxRequests":2}]`
		}, wantError: "1 到 86400"},
		{name: "rate limit request range", mutate: func(row *port.ManagementExternalIntegrationSourceListRow) {
			row.RateLimitsJSON = `[{"windowSeconds":1,"maxRequests":100001}]`
		}, wantError: "1 到 100000"},
		{name: "duplicate window", mutate: func(row *port.ManagementExternalIntegrationSourceListRow) {
			row.RateLimitsJSON = `[{"windowSeconds":1,"maxRequests":2},{"windowSeconds":1,"maxRequests":3}]`
		}, wantError: "窗口不能重复"},
		{name: "malformed JSON", mutate: func(row *port.ManagementExternalIntegrationSourceListRow) { row.ScopesJSON = `[` }, wantError: "unexpected EOF"},
		{name: "invalid required time", mutate: func(row *port.ManagementExternalIntegrationSourceListRow) { row.UpdatedAt = time.Time{} }, wantError: "updatedAt"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			row := validSourceRow("source_invalid", now)
			test.mutate(&row)
			store := &externalIntegrationSourceStoreStub{sourceRows: []port.ManagementExternalIntegrationSourceListRow{row}}
			_, err := NewService(store).List(context.Background(), ListInput{})
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("List() error = %v, want %q", err, test.wantError)
			}
			if len(store.statsCalls) != 0 || len(store.primaryCalls) != 0 {
				t.Fatal("invalid source row must fail before token enrichment")
			}
		})
	}
}

func TestServiceListReturnsPrimaryTokenJSONStatusAndTimeErrors(t *testing.T) {
	now := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		mutate    func(*port.ManagementExternalIntegrationSourcePrimaryTokenRow)
		wantError string
	}{
		{name: "invalid token status", mutate: func(row *port.ManagementExternalIntegrationSourcePrimaryTokenRow) { row.Status = "pending" }, wantError: "token 状态无效"},
		{name: "token scopes must be array", mutate: func(row *port.ManagementExternalIntegrationSourcePrimaryTokenRow) { row.ScopesJSON = `{}` }, wantError: "scopes_json 必须是数组"},
		{name: "token scope must be string", mutate: func(row *port.ManagementExternalIntegrationSourcePrimaryTokenRow) { row.ScopesJSON = `[false]` }, wantError: "scopes_json 必须是字符串数组"},
		{name: "invalid token time", mutate: func(row *port.ManagementExternalIntegrationSourcePrimaryTokenRow) { row.CreatedAt = time.Time{} }, wantError: "createdAt"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			primary := validPrimaryTokenRow("source_1", "token_1", now)
			test.mutate(&primary)
			store := &externalIntegrationSourceStoreStub{
				sourceRows:  []port.ManagementExternalIntegrationSourceListRow{validSourceRow("source_1", now)},
				primaryRows: []port.ManagementExternalIntegrationSourcePrimaryTokenRow{primary},
			}
			_, err := NewService(store).List(context.Background(), ListInput{})
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("List() error = %v, want %q", err, test.wantError)
			}
			if len(store.statsCalls) != 1 || len(store.primaryCalls) != 1 {
				t.Fatalf("token calls = stats:%d primary:%d", len(store.statsCalls), len(store.primaryCalls))
			}
		})
	}
}

func TestServiceListPropagatesStorageErrors(t *testing.T) {
	now := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	wantErr := errors.New("postgres unavailable")
	tests := []struct {
		name             string
		store            *externalIntegrationSourceStoreStub
		wantStatsCalls   int
		wantPrimaryCalls int
	}{
		{name: "source", store: &externalIntegrationSourceStoreStub{sourceErr: wantErr}},
		{
			name:           "stats",
			store:          &externalIntegrationSourceStoreStub{sourceRows: []port.ManagementExternalIntegrationSourceListRow{validSourceRow("source_1", now)}, statsErr: wantErr},
			wantStatsCalls: 1,
		},
		{
			name:             "primary",
			store:            &externalIntegrationSourceStoreStub{sourceRows: []port.ManagementExternalIntegrationSourceListRow{validSourceRow("source_1", now)}, primaryErr: wantErr},
			wantStatsCalls:   1,
			wantPrimaryCalls: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewService(test.store).List(context.Background(), ListInput{})
			if !errors.Is(err, wantErr) {
				t.Fatalf("List() error = %v, want wrapped storage error", err)
			}
			if len(test.store.statsCalls) != test.wantStatsCalls || len(test.store.primaryCalls) != test.wantPrimaryCalls {
				t.Fatalf("token calls = stats:%d primary:%d", len(test.store.statsCalls), len(test.store.primaryCalls))
			}
		})
	}
}

func TestServiceListPageUpperBoundWithoutSentinel(t *testing.T) {
	now := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	store := &externalIntegrationSourceStoreStub{sourceRows: []port.ManagementExternalIntegrationSourceListRow{
		validSourceRow("source_21", now),
		validSourceRow("source_22", now),
	}}
	result, err := NewService(store).List(context.Background(), ListInput{Page: 2, PageSize: 20})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if result.PageUpperBound != 22 || result.HasMore {
		t.Fatalf("page upper bound = %d, hasMore = %t", result.PageUpperBound, result.HasMore)
	}
}

func validSourceRow(id string, now time.Time) port.ManagementExternalIntegrationSourceListRow {
	return port.ManagementExternalIntegrationSourceListRow{
		ID:             id,
		Name:           id,
		Status:         publicapi.SourceStatusActive,
		ScopesJSON:     `[]`,
		RateLimitsJSON: `[]`,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

func validPrimaryTokenRow(sourceID string, tokenID string, now time.Time) port.ManagementExternalIntegrationSourcePrimaryTokenRow {
	return port.ManagementExternalIntegrationSourcePrimaryTokenRow{
		SourceRefID: sourceID,
		ID:          tokenID,
		Name:        tokenID,
		TokenPrefix: "juis_pre",
		TokenSuffix: "suffix01",
		Status:      publicapi.TokenStatusActive,
		ScopesJSON:  `[]`,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

func nineRateLimitsJSON() string {
	rules := make([]string, 0, 9)
	for i := 1; i <= 9; i++ {
		rules = append(rules, fmt.Sprintf(`{"windowSeconds":%d,"maxRequests":1}`, i))
	}
	return "[" + strings.Join(rules, ",") + "]"
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

type externalIntegrationSourceStoreStub struct {
	sourceRows   []port.ManagementExternalIntegrationSourceListRow
	sourceErr    error
	sourceCalls  []port.ManagementExternalIntegrationSourceListInput
	statsRows    []port.ManagementExternalIntegrationSourceTokenStatsRow
	statsErr     error
	statsCalls   [][]string
	primaryRows  []port.ManagementExternalIntegrationSourcePrimaryTokenRow
	primaryErr   error
	primaryCalls [][]string
}

func (s *externalIntegrationSourceStoreStub) ListManagementExternalIntegrationSources(
	_ context.Context,
	input port.ManagementExternalIntegrationSourceListInput,
) ([]port.ManagementExternalIntegrationSourceListRow, error) {
	s.sourceCalls = append(s.sourceCalls, input)
	return s.sourceRows, s.sourceErr
}

func (s *externalIntegrationSourceStoreStub) ListManagementExternalIntegrationSourceTokenStats(
	_ context.Context,
	sourceIDs []string,
) ([]port.ManagementExternalIntegrationSourceTokenStatsRow, error) {
	s.statsCalls = append(s.statsCalls, append([]string(nil), sourceIDs...))
	return s.statsRows, s.statsErr
}

func (s *externalIntegrationSourceStoreStub) ListManagementExternalIntegrationSourcePrimaryTokens(
	_ context.Context,
	sourceIDs []string,
) ([]port.ManagementExternalIntegrationSourcePrimaryTokenRow, error) {
	s.primaryCalls = append(s.primaryCalls, append([]string(nil), sourceIDs...))
	return s.primaryRows, s.primaryErr
}

var _ port.ManagementExternalIntegrationSourceListReader = (*externalIntegrationSourceStoreStub)(nil)
