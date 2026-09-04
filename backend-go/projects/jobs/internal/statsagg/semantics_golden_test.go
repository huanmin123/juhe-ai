package statsagg

import (
	"testing"
	"time"
)

// 本文件为纯语义 golden 对账层：期望值全部从 Node 源码逻辑推导，
// 每个用例注明推导依据（Node 函数与语义）。双实现对账以这些期望为桥。

// TestDateKeysDSTAndZonesGolden 锁定 dateKey/hourKey/minuteKey/weekKey/monthKey
// 的时区与 DST 语义。
// 推导：Node usage-stats-helpers.ts dateKey/hourKey/minuteKey/weekKey/monthKey
// 基于 Intl.DateTimeFormat('en-CA',{timeZone}) 的时区日历分解；America/New_York
// 2026 年 3 月 8 日 02:00 本地（07:00Z）切入 EDT，11 月 1 日 06:00Z 切回 EST；
// Asia/Shanghai 恒为 +08。
func TestDateKeysDSTAndZonesGolden(t *testing.T) {
	newYork, err := LoadStatsTimezone("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	shanghai, err := LoadStatsTimezone("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	utc, _ := LoadStatsTimezone("UTC")
	cases := []struct {
		name    string
		instant string
		loc     *time.Location
		want    UsageStatsTimeKeys
	}{
		{
			// 06:30Z = 01:30 EST（切入 EDT 之前）：本地日历仍是 03-08 01 点。
			// 2026-03-08 是周日 → weekKey = 周一 2026-03-02。
			name:    "ny_spring_forward_before_switch",
			instant: "2026-03-08T06:30:00.000Z",
			loc:     newYork,
			want: UsageStatsTimeKeys{
				StatMinute: "2026-03-08T01:30", StatHour: "2026-03-08T01",
				StatDate: "2026-03-08", StatWeek: "2026-03-02", StatMonth: "2026-03",
			},
		},
		{
			// 07:30Z = 03:30 EDT（已切换；02:30 本地不存在）——证明按本地
			// 日历取小时而不是固定偏移。
			name:    "ny_spring_forward_after_switch",
			instant: "2026-03-08T07:30:00.000Z",
			loc:     newYork,
			want: UsageStatsTimeKeys{
				StatMinute: "2026-03-08T03:30", StatHour: "2026-03-08T03",
				StatDate: "2026-03-08", StatWeek: "2026-03-02", StatMonth: "2026-03",
			},
		},
		{
			// 05:30Z = 01:30 EDT（06:00Z 才回拨）：仍是 01 点。
			// 2026-11-01 是周日 → weekKey = 2026-10-26。
			name:    "ny_fall_back_before_switch",
			instant: "2026-11-01T05:30:00.000Z",
			loc:     newYork,
			want: UsageStatsTimeKeys{
				StatMinute: "2026-11-01T01:30", StatHour: "2026-11-01T01",
				StatDate: "2026-11-01", StatWeek: "2026-10-26", StatMonth: "2026-11",
			},
		},
		{
			// 06:30Z = 01:30 EST（回拨后第二次 01 点），同一本地小时桶继续
			// 累计（hourKey 相同）。
			name:    "ny_fall_back_after_switch",
			instant: "2026-11-01T06:30:00.000Z",
			loc:     newYork,
			want: UsageStatsTimeKeys{
				StatMinute: "2026-11-01T01:30", StatHour: "2026-11-01T01",
				StatDate: "2026-11-01", StatWeek: "2026-10-26", StatMonth: "2026-11",
			},
		},
		{
			// 2026-01-01T00:00Z 在 +08 为 08 点：年/月/周键按本地日历。
			// 2026-01-01 是周四 → weekKey = 2025-12-29（跨年周）。
			name:    "shanghai_new_year",
			instant: "2026-01-01T00:00:00.000Z",
			loc:     shanghai,
			want: UsageStatsTimeKeys{
				StatMinute: "2026-01-01T08:00", StatHour: "2026-01-01T08",
				StatDate: "2026-01-01", StatWeek: "2025-12-29", StatMonth: "2026-01",
			},
		},
		{
			// 2025-12-31T16:30Z 在 +08 已是 2026-01-01 00:30：date/hour/month
			// 全部落 2026-01-01，week 仍是 2025-12-29 起始周。
			name:    "shanghai_utc_year_boundary",
			instant: "2025-12-31T16:30:00.000Z",
			loc:     shanghai,
			want: UsageStatsTimeKeys{
				StatMinute: "2026-01-01T00:30", StatHour: "2026-01-01T00",
				StatDate: "2026-01-01", StatWeek: "2025-12-29", StatMonth: "2026-01",
			},
		},
		{
			name:    "utc_plain",
			instant: "2026-04-18T10:15:00.000Z",
			loc:     utc,
			want: UsageStatsTimeKeys{
				StatMinute: "2026-04-18T10:15", StatHour: "2026-04-18T10",
				StatDate: "2026-04-18", StatWeek: "2026-04-13", StatMonth: "2026-04",
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := UsageStatsTimeKeysFor(testCase.instant, testCase.loc)
			if err != nil {
				t.Fatal(err)
			}
			if got != testCase.want {
				t.Fatalf("keys mismatch\n got %+v\nwant %+v", got, testCase.want)
			}
		})
	}
}

// TestWindowPlansGolden 锁定 31 天固定窗口与热窗口计划。
// 推导：Node usage-stats-window-helpers.ts fixedUsageStatsDateKeys（today 结尾
// 连续 31 天）、hotUsageStatsRanges（今日/昨日/近7日/31天窗/本月截断，去重
// 保序，days = end-start 日历差 + 1）。
func TestWindowPlansGolden(t *testing.T) {
	dates := FixedUsageStatsDateKeys("2026-04-18")
	if len(dates) != 31 || dates[0] != "2026-03-19" || dates[len(dates)-1] != "2026-04-18" {
		t.Fatalf("fixed date keys mismatch: first=%s last=%s len=%d", dates[0], dates[len(dates)-1], len(dates))
	}
	hot := HotUsageStatsRanges("2026-04-18")
	wantHot := []StatsRange{
		{StartDate: "2026-04-18", EndDate: "2026-04-18", Days: 1, MaxDays: 31},
		{StartDate: "2026-04-17", EndDate: "2026-04-17", Days: 1, MaxDays: 31},
		{StartDate: "2026-04-12", EndDate: "2026-04-18", Days: 7, MaxDays: 31},
		{StartDate: "2026-03-19", EndDate: "2026-04-18", Days: 31, MaxDays: 31},
		{StartDate: "2026-04-01", EndDate: "2026-04-18", Days: 18, MaxDays: 31},
	}
	if len(hot) != len(wantHot) {
		t.Fatalf("hot ranges len=%d want=%d: %+v", len(hot), len(wantHot), hot)
	}
	for index := range wantHot {
		if hot[index] != wantHot[index] {
			t.Fatalf("hot[%d] = %+v want %+v", index, hot[index], wantHot[index])
		}
	}
	// 月初与 31 天窗起点重合时去重（2026-03-31 的 fixedStart = 03-01 = monthStart）。
	deduped := HotUsageStatsRanges("2026-03-31")
	if len(deduped) != 4 {
		t.Fatalf("expected 4 deduped hot ranges, got %d: %+v", len(deduped), deduped)
	}
	if deduped[3] != (StatsRange{StartDate: "2026-03-01", EndDate: "2026-03-31", Days: 31, MaxDays: 31}) {
		t.Fatalf("deduped[3] = %+v", deduped[3])
	}
}

// TestTrendBucketKeysGolden 锁定趋势桶归并。
// 推导：Node trendBucketHours（days<=1 → 1h；<=3 → 6h；否则 24h）与
// trendBucketKey（hour/bucketHours 向下取整）。
func TestTrendBucketKeysGolden(t *testing.T) {
	if TrendBucketHours(1) != 1 || TrendBucketHours(3) != 6 || TrendBucketHours(7) != 24 {
		t.Fatal("trendBucketHours mismatch")
	}
	cases := []struct {
		statHour    string
		bucketHours int
		want        string
	}{
		{"2026-04-18T13", 6, "2026-04-18T12"},
		{"2026-04-18T13", 1, "2026-04-18T13"},
		{"2026-04-18T13", 24, "2026-04-18"},
		{"2026-04-18T00", 6, "2026-04-18T00"},
		{"2026-04-18T05", 6, "2026-04-18T00"},
	}
	for _, testCase := range cases {
		if got := TrendBucketKey(testCase.statHour, testCase.bucketHours); got != testCase.want {
			t.Fatalf("TrendBucketKey(%s,%d) = %s want %s", testCase.statHour, testCase.bucketHours, got, testCase.want)
		}
	}
}

func strPtr(value string) *string   { return &value }
func f64Ptr(value float64) *float64 { return &value }

// TestUsageStatsEntriesGolden 锁定 scope 扇出顺序与归属。
// 推导：Node usage-stats-aggregation.ts usageStatsEntries 的逐条 if 链；
// skipOwnerAccountStats/skipOwnerGroupStats 的三段布尔表达式见对应注释。
func TestUsageStatsEntriesGolden(t *testing.T) {
	type scopeKey struct{ system, scopeType, scopeID string }
	t.Run("owner_row", func(t *testing.T) {
		row := UsageStatsRecordRow{
			SystemAccountID: "alice", ProviderCode: strPtr("openai"), Model: strPtr("gpt-5"),
			APIKeyID: strPtr("key-1"), Endpoint: strPtr("/v1/chat/completions"),
			AccountID: strPtr("acc-1"), AccountOwnerSystemAccountID: strPtr("alice"), AccountAccessType: strPtr("owner"),
			GroupID: strPtr("grp-1"), GroupOwnerSystemAccountID: strPtr("alice"), GroupAccessType: strPtr("owner"),
		}
		entries := UsageStatsEntries(row, nil)
		got := make([]scopeKey, 0, len(entries))
		for _, entry := range entries {
			got = append(got, scopeKey{entry.SystemAccountID, entry.ScopeType, entry.ScopeID})
		}
		want := []scopeKey{
			{"alice", "system_account", "alice"},
			{"global", "system_account", "global"},
			{"alice", "provider", "openai"},
			{"alice", "group", "grp-1"},
			{"alice", "caller_account", "acc-1"},
			{"alice", "account", "acc-1"},
			{"global", "account", "acc-1"},
			{"alice", "api_key", "key-1"},
			{"alice", "model", "gpt-5"},
			{"alice", "endpoint", "/v1/chat/completions"},
		}
		if len(got) != len(want) {
			t.Fatalf("entry count %d want %d: %+v", len(got), len(want), got)
		}
		for index := range want {
			if got[index] != want[index] {
				t.Fatalf("entry[%d] = %+v want %+v", index, got[index], want[index])
			}
		}
	})
	t.Run("account_authorized", func(t *testing.T) {
		row := UsageStatsRecordRow{
			SystemAccountID: "dave", AccountID: strPtr("acc-1"),
			AccountOwnerSystemAccountID: strPtr("charlie"), AccountAccessType: strPtr("account_authorized"),
			AccountAuthorizationID: strPtr("auth-9"), AccountAuthorizationSourceType: strPtr("manual"),
		}
		entries := UsageStatsEntries(row, nil)
		type key struct{ system, scopeType, scopeID string }
		var got []key
		for _, entry := range entries {
			got = append(got, key{entry.SystemAccountID, entry.ScopeType, entry.ScopeID})
		}
		// 推导：account_authorized 时 accountStatsSystemAccountId = caller(dave)；
		// account_authorization 因 owner(charlie) ≠ caller(dave) 以 caller 记账；
		// caller_account 恒随 account_id 发出。
		wantContains := []key{
			{"dave", "caller_account", "acc-1"},
			{"dave", "account", "acc-1"},
			{"global", "account", "acc-1"},
			{"dave", "account_authorization", "auth-9"},
		}
		for _, wantKey := range wantContains {
			found := false
			for _, gotKey := range got {
				if gotKey == wantKey {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing entry %+v in %+v", wantKey, got)
			}
		}
		if len(got) != 6 { // system_account x2 + caller_account + account + global account + account_authorization
			t.Fatalf("entry count %d: %+v", len(got), got)
		}
	})
	t.Run("group_authorized_team_skips_owner_account_scope", func(t *testing.T) {
		row := UsageStatsRecordRow{
			SystemAccountID: "dave",
			AccountID:       strPtr("acc-2"), AccountOwnerSystemAccountID: strPtr("erin"), AccountAccessType: strPtr("group_authorized"),
			GroupID: strPtr("grp-2"), GroupOwnerSystemAccountID: strPtr("erin"), GroupAccessType: strPtr("authorized"),
			GroupAuthorizationID: strPtr("gauth-1"), GroupAuthorizationSourceType: strPtr("team"), GroupAuthorizationSourceTeamID: strPtr("team-7"),
		}
		entries := UsageStatsEntries(row, nil)
		for _, entry := range entries {
			// 推导：skipOwnerAccountStats = accessType!=='account_authorized'
			//   && groupMetadata.accessType==='authorized' && owner(erin)!==caller(dave)
			// → owner 维度的 account scope 不发（避免 owner 视角重复计 group_authorized 用量）。
			if entry.ScopeType == "account" && entry.SystemAccountID == "erin" {
				t.Fatalf("owner account scope must be skipped: %+v", entry)
			}
			// 同理 skipOwnerGroupStats（authorized 且 owner≠caller）→ owner 的
			// group scope 也不发，owner 视角由 group_authorization 系列承载。
			if entry.ScopeType == "group" && entry.SystemAccountID == "erin" {
				t.Fatalf("owner group scope must be skipped: %+v", entry)
			}
		}
		want := map[string]bool{
			"erin|group_authorization|gauth-1":           false,
			"erin|group_authorization_team|grp-2:team-7": false,
			"global|account|acc-2":                       false,
			"dave|caller_account|acc-2":                  false,
		}
		for _, entry := range entries {
			key := entry.SystemAccountID + "|" + entry.ScopeType + "|" + entry.ScopeID
			if _, ok := want[key]; ok {
				want[key] = true
			}
		}
		for key, found := range want {
			if !found {
				t.Fatalf("missing %s", key)
			}
		}
	})
	t.Run("team_lookup_instance_account", func(t *testing.T) {
		row := UsageStatsRecordRow{
			SystemAccountID: "dave",
			AccountID:       strPtr("acc-9"), AccountOwnerSystemAccountID: strPtr("frank"), AccountAccessType: strPtr("account_authorized"),
			AccountAuthorizationID: strPtr("auth-5"), AccountAuthorizationSourceType: strPtr("team"),
			AccountAuthorizationSourceTeamID: strPtr("team-3"),
		}
		lookup := &AuthorizationLookup{AccountAuthorizationInstanceAccountIDs: map[string]string{"auth-5": "instance-acc-1"}}
		entries := UsageStatsEntries(row, lookup)
		found := false
		for _, entry := range entries {
			// 推导：account_authorization_team scopeId = instanceAccountId:teamId
			//（lookup 命中时用 instance account，否则用 row.account_id）。
			if entry.ScopeType == "account_authorization_team" && entry.ScopeID == "instance-acc-1:team-3" {
				found = true
			}
		}
		if !found {
			t.Fatal("missing account_authorization_team with instance account id")
		}
	})
}

// TestShouldAggregateUsageStatsRecordGolden 锁定行过滤。
// 推导：Node usage-stats-aggregation.ts shouldAggregateUsageStatsRecord 的
// 逐条守卫（canonical 值、枚举、来源对、字段互斥）。
func TestShouldAggregateUsageStatsRecordGolden(t *testing.T) {
	validOwner := UsageStatsRecordRow{
		SystemAccountID: "alice",
		AccountID:       strPtr("acc-1"), AccountOwnerSystemAccountID: strPtr("alice"), AccountAccessType: strPtr("owner"),
	}
	cases := []struct {
		name   string
		mutate func(row *UsageStatsRecordRow)
		want   bool
	}{
		{"valid_owner", func(*UsageStatsRecordRow) {}, true},
		{"padded_owner_not_canonical", func(row *UsageStatsRecordRow) { row.AccountOwnerSystemAccountID = strPtr(" alice") }, false},
		{"invalid_access_type", func(row *UsageStatsRecordRow) { row.AccountAccessType = strPtr("friend") }, false},
		{"authorized_without_auth_id", func(row *UsageStatsRecordRow) {
			row.AccountAccessType = strPtr("account_authorized")
		}, false},
		{"group_authorized_requires_group", func(row *UsageStatsRecordRow) {
			row.AccountID, row.AccountAccessType = nil, nil
			row.AccountOwnerSystemAccountID = nil
			row.GroupAccessType = strPtr("authorized")
		}, false},
		{"team_source_requires_team_id", func(row *UsageStatsRecordRow) {
			row.AccountAccessType = strPtr("account_authorized")
			row.AccountAuthorizationID = strPtr("auth-1")
			row.AccountAuthorizationSourceType = strPtr("team")
		}, false},
		{"account_fields_without_account_id", func(row *UsageStatsRecordRow) {
			row.AccountID = nil
			row.AccountOwnerSystemAccountID = strPtr("alice")
		}, false},
		{"manual_source_with_team_id", func(row *UsageStatsRecordRow) {
			row.AccountAuthorizationID = strPtr("auth-1")
			row.AccountAuthorizationSourceType = strPtr("manual")
			row.AccountAuthorizationSourceTeamID = strPtr("team-1")
		}, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			row := validOwner
			testCase.mutate(&row)
			if got := ShouldAggregateUsageStatsRecord(row); got != testCase.want {
				t.Fatalf("shouldAggregate = %v want %v", got, testCase.want)
			}
		})
	}
}

// TestAccumulatorFromRecordAndMergeGolden 锁定累加器初值与合并。
// 推导：Node usageStatsAccumulatorFromRecord（null→0/0 计数、负数截 0）与
// mergePostgresUsageStatsAccumulator（sum 相加、max 取大、时间戳取最新）。
func TestAccumulatorFromRecordAndMergeGolden(t *testing.T) {
	row := UsageStatsRecordRow{
		Success: 0, DurationMs: nil, FirstTokenMs: f64Ptr(300),
		InputTokens: f64Ptr(10), CostUsd: f64Ptr(0.5), CreatedAt: "2026-04-18T10:00:00.000Z",
	}
	accumulator := UsageStatsAccumulatorFromRecord(row)
	if accumulator.RequestCount != 1 || accumulator.SuccessCount != 0 || accumulator.ErrorCount != 1 {
		t.Fatalf("counters mismatch: %+v", accumulator)
	}
	if accumulator.DurationMsSum != 0 || accumulator.DurationMsCount != 0 || accumulator.DurationMsMax != 0 {
		t.Fatalf("duration null semantics mismatch: %+v", accumulator)
	}
	if accumulator.FirstTokenMsSum != 300 || accumulator.FirstTokenMsCount != 1 || accumulator.FirstTokenMsMax != 300 {
		t.Fatalf("first token mismatch: %+v", accumulator)
	}
	if accumulator.LastUsedAt != "2026-04-18T10:00:00.000Z" || accumulator.LastErrorAt != "2026-04-18T10:00:00.000Z" {
		t.Fatalf("timestamps mismatch: %+v", accumulator)
	}
	later := UsageStatsAccumulatorFromRecord(UsageStatsRecordRow{
		Success: 1, DurationMs: f64Ptr(900), FirstTokenMs: f64Ptr(100),
		CreatedAt: "2026-04-18T10:05:00.000Z",
	})
	if err := MergeAccumulator(&accumulator, later); err != nil {
		t.Fatal(err)
	}
	if accumulator.RequestCount != 2 || accumulator.SuccessCount != 1 || accumulator.ErrorCount != 1 {
		t.Fatalf("merged counters mismatch: %+v", accumulator)
	}
	if accumulator.DurationMsMax != 900 {
		t.Fatalf("merged max mismatch: %+v", accumulator)
	}
	// last_error_at：r2 成功（LastErrorAt=undefined），Node maxOptionalIso
	// 保留 target 的 10:00 错误时间。
	if accumulator.LastErrorAt != "2026-04-18T10:00:00.000Z" {
		t.Fatalf("last error should keep earlier error when later row succeeds: %+v", accumulator)
	}
}

// TestRFC3339CanonicalizationGolden 锁定 RFC3339 规范化与比较。
// 推导：Node rfc3339.ts（offset 必需、toISOString 毫秒 Z 形态）与
// usage-stats.repository.ts compareUsageStatsTimestamp/maxOptionalIso。
func TestRFC3339CanonicalizationGolden(t *testing.T) {
	cases := []struct {
		input string
		want  string
		ok    bool
	}{
		{"2026-04-18T10:15:00Z", "2026-04-18T10:15:00.000Z", true},
		{"2026-04-18T10:15:00.5Z", "2026-04-18T10:15:00.500Z", true},
		{"2026-04-18T19:15:00+09:00", "2026-04-18T10:15:00.000Z", true},
		{"2026-04-18T10:15:00", "", false},
		{"2026-13-01T00:00:00Z", "", false},
		{"2026-02-30T00:00:00Z", "", false},
		{"not-a-time", "", false},
	}
	for _, testCase := range cases {
		got, ok := CanonicalizeRFC3339Instant(testCase.input)
		if ok != testCase.ok || (ok && got != testCase.want) {
			t.Fatalf("canonicalize(%s) = (%s,%v) want (%s,%v)", testCase.input, got, ok, testCase.want, testCase.ok)
		}
	}
	comparison, err := CompareUsageStatsTimestamp("2026-04-18T19:15:00+09:00", "2026-04-18T10:15:00.000Z")
	if err != nil || comparison != 0 {
		t.Fatalf("compare across offsets = %d,%v want 0,nil", comparison, err)
	}
	maxValue, err := MaxOptionalISO("", "2026-04-18T10:15:00.000Z")
	if err != nil || maxValue != "2026-04-18T10:15:00.000Z" {
		t.Fatalf("maxOptionalISO('', x) = %s,%v", maxValue, err)
	}
	if maxValue, _ := MaxOptionalISO("", ""); maxValue != "" {
		t.Fatalf("maxOptionalISO('','') = %s", maxValue)
	}
}
