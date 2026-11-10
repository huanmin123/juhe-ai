// authorization_preaggregate_test.go —— 授权摘要批内预聚合的行为锁：
// 同批同键只保留一个累计条目，跨 statDate 不合并；折叠后的累计值与逐条
// UPSERT 的 ON CONFLICT 增量语义一致（请求数、token、成本、max/last_used_at）。
package statsagg

import "testing"

func authorizationPreaggregateRow(id, createdAt string, inputTokens float64) UsageStatsRecordRow {
	row := UsageStatsRecordRow{
		ID:                               id,
		SystemAccountID:                  "grantee-1",
		AccountID:                        strPtr("account-1"),
		AccountOwnerSystemAccountID:      strPtr("owner-1"),
		AccountAuthorizationID:           strPtr("authorization-1"),
		AccountAuthorizationSourceType:   strPtr("team"),
		AccountAuthorizationSourceTeamID: strPtr("team-1"),
		Success:                          1,
		InputTokens:                      f64Ptr(inputTokens),
		CreatedAt:                        createdAt,
	}
	row.DurationMs = f64Ptr(100)
	return row
}

func TestAddAuthorizationUsageReportEntriesPreaggregatesByOutputKey(t *testing.T) {
	teamEntries := map[authorizationSummaryEntryKey]*UsageStatsAccumulator{}
	userEntries := map[authorizationSummaryEntryKey]*UsageStatsAccumulator{}
	rows := []UsageStatsRecordRow{
		authorizationPreaggregateRow("row-1", "2026-10-01T01:00:00.000Z", 100),
		authorizationPreaggregateRow("row-2", "2026-10-01T02:00:00.000Z", 40),
	}
	for _, row := range rows {
		if err := addAuthorizationUsageReportEntries(teamEntries, userEntries, row, "2026-10-01", nil); err != nil {
			t.Fatalf("预聚合第 %s 行失败: %v", row.ID, err)
		}
	}

	// 单条记录（account 授权、team 来源、owner≠grantee、owner 与 global 两个
	// 维度、3 个资源过滤）展开为 12 条 team 键和 24 条 user 键；两条记录键
	// 相同，折叠后数量必须与单条一致，而不是翻倍。
	if len(teamEntries) != 12 {
		t.Fatalf("team 预聚合键数 = %d，want 12（同键未折叠）", len(teamEntries))
	}
	if len(userEntries) != 24 {
		t.Fatalf("user 预聚合键数 = %d，want 24（同键未折叠）", len(userEntries))
	}

	teamKey := authorizationSummaryEntryKey{
		systemAccountID: "owner-1",
		statDate:        "2026-10-01",
		summary: authorizationSummaryKey{
			teamFilterID:       "team-1",
			resourceFilterType: "account",
			resourceFilterID:   "account-1",
		},
	}
	teamStats, ok := teamEntries[teamKey]
	if !ok {
		t.Fatalf("缺少 owner/team 维度的 account 资源过滤键: %+v", teamKey)
	}
	if teamStats.RequestCount != 2 || teamStats.InputTokens != 140 {
		t.Fatalf("team 折叠累计 = (request_count %.0f, input_tokens %.0f)，want (2, 140)",
			teamStats.RequestCount, teamStats.InputTokens)
	}
	if teamStats.LastUsedAt != "2026-10-01T02:00:00.000Z" {
		t.Fatalf("team 折叠 last_used_at = %s，want 最新记录时间", teamStats.LastUsedAt)
	}

	userKey := authorizationSummaryEntryKey{
		systemAccountID: GlobalStatsSystemAccountID,
		statDate:        "2026-10-01",
		summary: authorizationSummaryKey{
			teamFilterID:                 "team-1",
			granteeFilterSystemAccountID: "grantee-1",
			resourceFilterType:           "all",
		},
	}
	userStats, ok := userEntries[userKey]
	if !ok {
		t.Fatalf("缺少 global/team/grantee 的全资源过滤键: %+v", userKey)
	}
	if userStats.RequestCount != 2 || userStats.InputTokens != 140 {
		t.Fatalf("user 折叠累计 = (request_count %.0f, input_tokens %.0f)，want (2, 140)",
			userStats.RequestCount, userStats.InputTokens)
	}

	// 另一个 statDate 必须生成独立条目，不能把跨日记录合并到同一天。
	if err := addAuthorizationUsageReportEntries(teamEntries, userEntries, rows[0], "2026-10-02", nil); err != nil {
		t.Fatalf("跨日预聚合失败: %v", err)
	}
	nextDayKey := teamKey
	nextDayKey.statDate = "2026-10-02"
	nextDayStats, ok := teamEntries[nextDayKey]
	if !ok {
		t.Fatalf("缺少次日 team 键: %+v", nextDayKey)
	}
	if nextDayStats.RequestCount != 1 || teamStats.RequestCount != 2 {
		t.Fatalf("跨日合并串行: 次日 request_count = %.0f，原日 request_count = %.0f，want (1, 2)",
			nextDayStats.RequestCount, teamStats.RequestCount)
	}
}

func BenchmarkAddAuthorizationUsageReportEntries(b *testing.B) {
	rows := make([]UsageStatsRecordRow, 2000)
	for index := range rows {
		rows[index] = authorizationPreaggregateRow("row", "2026-10-01T01:00:00.000Z", 100)
	}
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		teamEntries := map[authorizationSummaryEntryKey]*UsageStatsAccumulator{}
		userEntries := map[authorizationSummaryEntryKey]*UsageStatsAccumulator{}
		for _, row := range rows {
			if err := addAuthorizationUsageReportEntries(teamEntries, userEntries, row, "2026-10-01", nil); err != nil {
				b.Fatal(err)
			}
		}
		if len(teamEntries) != 12 || len(userEntries) != 24 {
			b.Fatalf("unexpected entry counts: team=%d user=%d", len(teamEntries), len(userEntries))
		}
	}
}
