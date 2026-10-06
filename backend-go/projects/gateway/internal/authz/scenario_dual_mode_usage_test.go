// scenario_dual_mode_usage_test.go —— 双模式套件的读路径与口径场景（交付物 1
// 场景矩阵 B）：详情 limits 回显、keyword 六维、列表 direction/scope、usage 读
// 矩阵、冻结窗口表免疫与到期 reconcile。夹具/清理/造数 helpers 定义在
// scenario_dual_mode_test.go。
package authz

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

// B7 详情 limits 回显：设置 limits 的授权 Find 返回等值 limits；未设置返回 null。
func TestDualDetailLimitsEcho(t *testing.T) {
	runDual(t, func(e *dualEnv) {
		ctx := context.Background()
		owner := e.p + "owner"
		grantee := e.p + "grantee"
		e.seedSystemAccount(owner, "active")
		e.seedSystemAccount(grantee, "active")
		chain := e.seedProviderChain()
		grpWith := e.p + "grp-limits"
		grpWithout := e.p + "grp-plain"
		e.seedGroup(grpWith, owner, "有限制分组", chain, false)
		e.seedGroup(grpWithout, owner, "无限制分组", chain, false)

		limits := `{"total":{"enabled":true,"limit":42}}`
		withLimits, err := e.store.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: grpWith,
			GranteeType: "system_account", GranteeID: grantee, LimitsJSON: &limits,
		}, owner)
		if err != nil {
			e.t.Fatalf("创建有限制授权失败: %v", err)
		}
		withoutLimits, err := e.store.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: grpWithout,
			GranteeType: "system_account", GranteeID: grantee,
		}, owner)
		if err != nil {
			e.t.Fatalf("创建无限制授权失败: %v", err)
		}
		detail, err := e.store.Find(ctx, withLimits.Item.ID)
		if err != nil || detail == nil {
			e.t.Fatalf("Find 有限制授权失败: %v", err)
		}
		dualAssertLimitsJSON(e, detail.Limits, limits)
		plain, err := e.store.Find(ctx, withoutLimits.Item.ID)
		if err != nil || plain == nil {
			e.t.Fatalf("Find 无限制授权失败: %v", err)
		}
		if plain.Limits != nil {
			e.t.Fatalf("未设置 limits 的详情应回显 null，实际 %v", plain.Limits)
		}
	})
}

// B8 keyword 六维前缀命中：资源名（分组/账户）、被授权用户 display_name、团队名、
// 归属人、remark，以及未命中空集。关键字全部取 ASCII 前缀，避免 PG/SQLite 排序
// 规则差异，同时避开以 '-' 结尾的前缀（keywordUpperBound 的上界语义）。
func TestDualKeywordSixDimensions(t *testing.T) {
	runDual(t, func(e *dualEnv) {
		ctx := context.Background()
		owner := e.p + "owner"
		grantee := e.p + "grantee"
		teamA := e.p + "teamA"
		e.seedSystemAccount(owner, "active")
		e.seedSystemAccount(grantee, "active")
		e.seedTeamWithMembers(teamA, "kwteam-唯一", grantee, owner)
		chain := e.seedProviderChain()
		grp := e.p + "grp-kw"
		e.seedGroup(grp, owner, "kwgroup-唯一资源名", chain, false)
		sourceAcc := e.p + "acc-kw"
		e.seedSourceAccount(sourceAcc, owner, "kwaccount-唯一账户名", chain)
		e.seedGroup(e.p+"grp-kw-default", grantee, "kwgrantee默认分组", chain, true)

		// 被授权用户 / 归属人 display_name 改为 kw 前缀（六维之二/之四）。
		e.exec(`UPDATE `+e.tbl("system_accounts")+` SET display_name = ? WHERE id = ?`, "kwgrantee-唯一显示名", grantee)
		e.exec(`UPDATE `+e.tbl("system_accounts")+` SET display_name = ? WHERE id = ?`, "kwowner-唯一归属人", owner)

		remark := "kwremark-唯一备注"
		groupGrant, err := e.store.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: grp,
			GranteeType: "system_account", GranteeID: grantee, Remark: &remark,
		}, owner)
		if err != nil {
			e.t.Fatalf("创建分组授权失败: %v", err)
		}
		accountGrant, err := e.store.Create(ctx, CreateInput{
			ResourceType: "account", ResourceID: sourceAcc,
			GranteeType: "system_account", GranteeID: grantee,
		}, owner)
		if err != nil {
			e.t.Fatalf("创建账户授权失败: %v", err)
		}
		teamGrant, err := e.store.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: grp,
			GranteeType: "team", GranteeID: teamA,
		}, owner)
		if err != nil {
			e.t.Fatalf("创建团队授权失败: %v", err)
		}
		teamGrantID := teamGrant.Item.ID

		expect := func(keyword string, wantIDs ...string) {
			e.t.Helper()
			items, _, _, err := e.store.ListPage(ctx, Filters{Keyword: keyword, IsAdmin: true}, 1, 50)
			if err != nil {
				e.t.Fatalf("keyword=%s 查询失败: %v", keyword, err)
			}
			got := map[string]bool{}
			for _, item := range items {
				got[item.ID] = true
			}
			for _, want := range wantIDs {
				if !got[want] {
					e.t.Fatalf("keyword=%s 未命中授权 %s（结果 %+v）", keyword, want, items)
				}
			}
			if len(items) != len(wantIDs) {
				e.t.Fatalf("keyword=%s 命中 %d 行，want %d（结果 %+v）", keyword, len(items), len(wantIDs), items)
			}
		}
		// 资源名两臂（分组 / 账户）。分组名维度对直授权与团队授权两条授权同时命中。
		expect("kwgroup", groupGrant.Item.ID, teamGrantID)
		expect("kwaccount", accountGrant.Item.ID)
		// 被授权用户 display_name（仅两条直授权带 grantee_system_account_id）。
		expect("kwgrantee", groupGrant.Item.ID, accountGrant.Item.ID)
		// 团队名。
		expect("kwteam", teamGrantID)
		// 归属人 display_name（全部授权共同命中）。
		expect("kwowner", groupGrant.Item.ID, accountGrant.Item.ID, teamGrantID)
		// remark 前缀。
		expect("kwremark", groupGrant.Item.ID)
		// 未命中 → 空集。
		expect("zwnomatch")
	})
}

// B9 列表 direction 与 scope：self outbound/inbound 行集、admin 带 systemAccountId、
// admin 详情带不匹配 systemAccountId → 404（Find 层 outcome）。
func TestDualListDirectionScopes(t *testing.T) {
	runDual(t, func(e *dualEnv) {
		ctx := context.Background()
		owner := e.p + "owner"
		g1 := e.p + "g1"
		g2 := e.p + "g2"
		teamA := e.p + "teamA"
		e.seedSystemAccount(owner, "active")
		e.seedSystemAccount(g1, "active")
		e.seedSystemAccount(g2, "active")
		e.seedTeamWithMembers(teamA, "方向团队", g2, owner)
		chain := e.seedProviderChain()
		grp1 := e.p + "grp-dir1"
		grp2 := e.p + "grp-dir2"
		e.seedGroup(grp1, owner, "方向分组1", chain, false)
		e.seedGroup(grp2, owner, "方向分组2", chain, false)

		direct, err := e.store.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: grp1,
			GranteeType: "system_account", GranteeID: g1,
		}, owner)
		if err != nil {
			e.t.Fatalf("创建直授权失败: %v", err)
		}
		if _, err := e.store.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: grp2,
			GranteeType: "team", GranteeID: teamA,
		}, owner); err != nil {
			e.t.Fatalf("创建团队授权失败: %v", err)
		}

		ids := func(filters Filters) map[string]bool {
			e.t.Helper()
			items, _, _, err := e.store.ListPage(ctx, filters, 1, 50)
			if err != nil {
				e.t.Fatalf("列表查询失败: %v", err)
			}
			out := map[string]bool{}
			for _, item := range items {
				out[item.ID] = true
			}
			return out
		}
		// self 方向。
		if out := ids(Filters{ViewerSystemAccountID: owner, Direction: "outbound"}); !out[direct.Item.ID] || len(out) != 2 {
			e.t.Fatalf("owner outbound 应含 2 行（个人+团队，owner 视角），实际 %v", out)
		}
		if out := ids(Filters{ViewerSystemAccountID: g1, Direction: "inbound"}); !out[direct.Item.ID] || len(out) != 1 {
			e.t.Fatalf("g1 inbound 应只含个人授权，实际 %v", out)
		}
		if out := ids(Filters{ViewerSystemAccountID: g1, Direction: "outbound"}); len(out) != 0 {
			e.t.Fatalf("g1 outbound 应为空集，实际 %v", out)
		}
		if out := ids(Filters{ViewerSystemAccountID: g2, Direction: "inbound"}); len(out) != 1 {
			e.t.Fatalf("g2 inbound 应经团队成员关系命中 1 行，实际 %v", out)
		}
		// admin 带 systemAccountId 的方向过滤。
		if out := ids(Filters{IsAdmin: true, ViewerSystemAccountID: owner, Direction: "outbound"}); len(out) != 2 {
			e.t.Fatalf("admin scope outbound 应 2 行，实际 %v", out)
		}
		if out := ids(Filters{IsAdmin: true, ViewerSystemAccountID: g1, Direction: "inbound"}); len(out) != 1 {
			e.t.Fatalf("admin scope inbound 应 1 行，实际 %v", out)
		}

		// admin 详情带不匹配 systemAccountId → 404（Find 层 outcome，走路由层）。
		deps := &Deps{Store: e.store}
		req := httptest.NewRequest(http.MethodGet, "/authorizations/"+direct.Item.ID+"?systemAccountId="+e.p+"other", nil)
		req.SetPathValue("id", direct.Item.ID)
		req = req.WithContext(authsys.WithAuthContext(req.Context(), &authsys.AuthContext{SystemAccountID: e.p + "admin", Role: "admin"}))
		rec := httptest.NewRecorder()
		deps.find(rec, req, false)
		if rec.Code != http.StatusNotFound {
			e.t.Fatalf("admin 详情 scope 不匹配应 404，实际 %d body=%s", rec.Code, rec.Body.String())
		}
	})
}

// B10 usage 读矩阵（重点）：3 个连续 stat_date × {teamA/teamB/teamless('')} ×
// 资源三档（all / account / account+id），全零行被 HAVING 排除，'' 合计键显式
// 造行（读侧不汇总），admin-filter/self 命中 owner 键、global 键命中（仅 SQLite
// 臂，避免向共享库种 system_account_id='global' 行），分页上界语义、超页钳制与
// 超 31 天入参钳制。
func TestDualUsageReadMatrix(t *testing.T) {
	runDual(t, func(e *dualEnv) {
		ctx := context.Background()
		ownerKey := e.p + "usage-owner"
		teamA := e.p + "teamA"
		teamB := e.p + "teamB"
		m1 := e.p + "m1"
		m2 := e.p + "m2"
		e.seedTeam(teamA, "用量团队A")
		e.seedTeam(teamB, "用量团队B")
		e.seedSystemAccount(m1, "active")
		e.seedSystemAccount(m2, "active")
		// 真实 PG 的 accounts INSERT 触发器会把账户归属人写进
		// account_list_availability_dirty（FK → system_accounts），归属人必须先存在。
		e.seedSystemAccount(ownerKey, "active")
		chain := e.seedProviderChain()
		accR := e.p + "accR"
		e.seedSourceAccount(accR, ownerKey, "用量资源账户", chain)

		dates := []string{"2026-09-29", "2026-09-30", "2026-10-01"}
		lastUsed := []string{"2026-09-29T10:00:00.000Z", "2026-09-30T10:00:00.000Z", "2026-10-01T10:00:00.000Z"}
		type tier struct {
			resourceType string
			resourceID   string
		}
		tiers := []tier{{"all", ""}, {"account", ""}, {"account", accR}}
		// 期望值累加器（team 表按 team+tier，user 表按 team+grantee+tier）。
		teamExpect := map[string]struct{ requests, cost float64 }{}
		userExpect := map[string]struct{ requests, cost float64 }{}
		expectKey := func(parts ...string) string { return strings.Join(parts, "\x00") }

		insertTeam := func(systemAccountID, date, teamFilter string, tm tier, requests, cost float64, lastUsedAt string) {
			e.exec(`INSERT INTO `+e.stbl("authorization_team_usage_summary_daily")+`
				(system_account_id, stat_date, team_filter_id, resource_filter_type, resource_filter_id,
				 request_count, input_tokens, output_tokens, total_cost_usd, last_used_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '2026-10-01T00:00:00.000Z')`,
				systemAccountID, date, teamFilter, tm.resourceType, tm.resourceID, requests, requests*10, requests*5, cost, lastUsedAt)
		}
		insertUser := func(systemAccountID, date, teamFilter, grantee string, tm tier, requests, cost float64, lastUsedAt string) {
			e.exec(`INSERT INTO `+e.stbl("authorization_user_usage_summary_daily")+`
				(system_account_id, stat_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id,
				 request_count, input_tokens, output_tokens, total_cost_usd, last_used_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '2026-10-01T00:00:00.000Z')`,
				systemAccountID, date, teamFilter, grantee, tm.resourceType, tm.resourceID, requests, requests*10, requests*5, cost, lastUsedAt)
		}

		for dateIdx, date := range dates {
			for tierIdx, tm := range tiers {
				teamAReq := float64(10 + dateIdx + tierIdx*100)
				teamACost := 1.0 + 0.5*float64(dateIdx) + float64(tierIdx)*10
				teamBReq := float64(20 + dateIdx + tierIdx*100)
				teamBCost := 2.0 + 0.5*float64(dateIdx) + float64(tierIdx)*10
				// teamA / teamB 行。
				insertTeam(ownerKey, date, teamA, tm, teamAReq, teamACost, lastUsed[dateIdx])
				insertTeam(ownerKey, date, teamB, tm, teamBReq, teamBCost, lastUsed[dateIdx])
				// '' 合计键行（读侧不汇总，必须显式造）。
				insertTeam(ownerKey, date, "", tm, float64(1+dateIdx), 0.1*float64(dateIdx+1), lastUsed[dateIdx])
				// user 表：m1 挂 teamA，m2 挂 teamB。user 读的 team_filter_id 是精确
				// 匹配（空 = 只读 teamless 行），teamless 维度另种具体资源档行供
				// details 读，'' + all 档行供 summary 键直读。
				insertUser(ownerKey, date, teamA, m1, tm, teamAReq, teamACost, lastUsed[dateIdx])
				insertUser(ownerKey, date, teamB, m2, tm, teamBReq, teamBCost, lastUsed[dateIdx])
				if tierIdx == 0 {
					insertUser(ownerKey, date, "", m1, tm, float64(1+dateIdx), 0.1*float64(dateIdx+1), lastUsed[dateIdx])
				}
				if tierIdx == 2 {
					insertUser(ownerKey, date, "", m1, tm, float64(2+dateIdx), 0.2*float64(dateIdx+1), lastUsed[dateIdx])
				}
				teamExpect[expectKey(teamA, tm.resourceType, tm.resourceID)] = struct{ requests, cost float64 }{
					teamExpect[expectKey(teamA, tm.resourceType, tm.resourceID)].requests + teamAReq,
					teamExpect[expectKey(teamA, tm.resourceType, tm.resourceID)].cost + teamACost,
				}
				teamExpect[expectKey(teamB, tm.resourceType, tm.resourceID)] = struct{ requests, cost float64 }{
					teamExpect[expectKey(teamB, tm.resourceType, tm.resourceID)].requests + teamBReq,
					teamExpect[expectKey(teamB, tm.resourceType, tm.resourceID)].cost + teamBCost,
				}
				userExpect[expectKey(teamA, m1, tm.resourceType, tm.resourceID)] = struct{ requests, cost float64 }{
					userExpect[expectKey(teamA, m1, tm.resourceType, tm.resourceID)].requests + teamAReq,
					userExpect[expectKey(teamA, m1, tm.resourceType, tm.resourceID)].cost + teamACost,
				}
				userExpect[expectKey(teamB, m2, tm.resourceType, tm.resourceID)] = struct{ requests, cost float64 }{
					userExpect[expectKey(teamB, m2, tm.resourceType, tm.resourceID)].requests + teamBReq,
					userExpect[expectKey(teamB, m2, tm.resourceType, tm.resourceID)].cost + teamBCost,
				}
				if tierIdx == 0 {
					userExpect[expectKey("", m1, tm.resourceType, tm.resourceID)] = struct{ requests, cost float64 }{6, 0.6}
				}
			}
			// 全零行（HAVING 应排除）与 global 键行（仅 SQLite 臂参与断言）。
			insertTeam(ownerKey, date, teamA, tier{"group", e.p + "grp-zero"}, 0, 0, "")
			if !e.pg && dateIdx == 0 {
				// global 行放在具体资源档：details 谓词只显示具体资源行。
				insertTeam("global", date, teamA, tiers[2], 100, 55.5, lastUsed[dateIdx])
			}
		}

		rng := UsageStatsRange{StartDate: dates[0], EndDate: dates[2], Days: 3, MaxDays: 31}
		ownerAccess := accessInfo{IsAdmin: true, FilterID: ownerKey}
		selfAccess := accessInfo{ViewerID: ownerKey}

		findTeamRow := func(rows []TeamUsageRow, teamID, resourceType, resourceID string) *TeamUsageRow {
			e.t.Helper()
			for i := range rows {
				if rows[i].TeamID == teamID && rows[i].ResourceType == resourceType && rows[i].ResourceID == resourceID {
					return &rows[i]
				}
			}
			return nil
		}

		// 1) 自定义 3 日窗：details 精确求和（SUM / MAX(last_used)）。读侧谓词
		// 只显示具体资源行（resource_filter_type IN (account,group) 且
		// resource_filter_id <> ''），'all' 档与 '' 资源档只服务 summary。
		rows, err := e.store.teamUsageRows(ctx, UsageFilters{}, ownerAccess, rng, 1, 20)
		if err != nil {
			e.t.Fatalf("team details 读取失败: %v", err)
		}
		if len(rows.Rows) != 2 {
			e.t.Fatalf("team details 应为 2 团队 × 具体资源档 = 2 行，实际 %d（全零/''/all 档行不应出现）: %+v", len(rows.Rows), rows.Rows)
		}
		for _, teamID := range []string{teamA, teamB} {
			want := teamExpect[expectKey(teamID, "account", accR)]
			row := findTeamRow(rows.Rows, teamID, "account", accR)
			if row == nil {
				e.t.Fatalf("team details 缺少行 team=%s tier=account/%s", teamID, accR)
			}
			if !dualNearlyEqual(row.Usage.RequestCount, want.requests) || !dualNearlyEqual(row.Usage.TotalCost, want.cost) {
				e.t.Fatalf("team=%s 具体资源档求和 = (%.2f, %.2f)，want (%.2f, %.2f)",
					teamID, row.Usage.RequestCount, row.Usage.TotalCost, want.requests, want.cost)
			}
			if row.LastUsedAt != lastUsed[2] {
				e.t.Fatalf("team=%s lastUsed = %s，want MAX=%s", teamID, row.LastUsedAt, lastUsed[2])
			}
		}
		// 排序口径：total_cost DESC → teamB 在前。
		if rows.Rows[0].TeamID != teamB || rows.Rows[1].TeamID != teamA {
			e.t.Fatalf("team details 排序 = %s,%s，want %s,%s（cost desc）", rows.Rows[0].TeamID, rows.Rows[1].TeamID, teamB, teamA)
		}
		// teamName / 资源名投影。
		if row := findTeamRow(rows.Rows, teamA, "account", accR); row == nil || row.TeamName != "用量团队A" || row.ResourceName != "用量资源账户" {
			e.t.Fatalf("teamName/资源名投影异常: %+v", findTeamRow(rows.Rows, teamA, "account", accR))
		}
		// 2) summary 三档精确求和（'all' 档经 summary 键直读）。
		assertTeamSummary := func(filters UsageFilters, want struct{ requests, cost float64 }) {
			e.t.Helper()
			got, err := e.store.teamUsageSummary(ctx, filters, ownerAccess, rng)
			if err != nil {
				e.t.Fatalf("team summary 读取失败: %v", err)
			}
			if !dualNearlyEqual(got.Summary.RequestCount, want.requests) || !dualNearlyEqual(got.Summary.TotalCost, want.cost) {
				e.t.Fatalf("team summary filters=%+v = (%.2f, %.2f)，want (%.2f, %.2f)",
					filters, got.Summary.RequestCount, got.Summary.TotalCost, want.requests, want.cost)
			}
			if got.Summary.LastUsedAt == nil || *got.Summary.LastUsedAt != lastUsed[2] {
				e.t.Fatalf("team summary lastUsed = %v，want %s", got.Summary.LastUsedAt, lastUsed[2])
			}
		}
		assertTeamSummary(UsageFilters{TeamID: teamA}, teamExpect[expectKey(teamA, "all", "")])
		assertTeamSummary(UsageFilters{TeamID: teamA, ResourceType: "account"}, teamExpect[expectKey(teamA, "account", "")])
		assertTeamSummary(UsageFilters{TeamID: teamA, ResourceType: "account", ResourceID: accR}, teamExpect[expectKey(teamA, "account", accR)])
		// 3) team-summary 无 teamId 读 '' 合计键行。
		blankSummary, err := e.store.teamUsageSummary(ctx, UsageFilters{TeamID: "", ResourceType: "all"}, ownerAccess, rng)
		if err != nil {
			e.t.Fatalf("team summary('') 读取失败: %v", err)
		}
		if !dualNearlyEqual(blankSummary.Summary.RequestCount, 6) || !dualNearlyEqual(blankSummary.Summary.TotalCost, 0.6) {
			e.t.Fatalf("'' 合计键 summary = (%.2f, %.2f)，want (6, 0.6)",
				blankSummary.Summary.RequestCount, blankSummary.Summary.TotalCost)
		}
		// 4) details 不含 '' team 行（'' 行只服务 summary）。
		for _, row := range rows.Rows {
			if row.TeamID == "" {
				e.t.Fatalf("details 不应包含 '' team 行: %+v", row)
			}
		}
		// 5) 资源档筛选：details 谓词下 account+id 与 account（仅类型）都只命中
		// 具体资源行（'' id 行被排除）。
		filtered, err := e.store.teamUsageRows(ctx, UsageFilters{ResourceType: "account", ResourceID: accR}, ownerAccess, rng, 1, 20)
		if err != nil {
			e.t.Fatalf("资源筛选读取失败: %v", err)
		}
		if len(filtered.Rows) != 2 {
			e.t.Fatalf("account+id 筛选应 2 行，实际 %d", len(filtered.Rows))
		}
		broad, err := e.store.teamUsageRows(ctx, UsageFilters{ResourceType: "account"}, ownerAccess, rng, 1, 20)
		if err != nil {
			e.t.Fatalf("类型筛选读取失败: %v", err)
		}
		if len(broad.Rows) != 2 {
			e.t.Fatalf("account 类型筛选应 2 行（'' id 档被谓词排除），实际 %d", len(broad.Rows))
		}
		// 6) self 的 owner 键命中与 admin-filter 等值。
		selfRows, err := e.store.teamUsageRows(ctx, UsageFilters{}, selfAccess, rng, 1, 20)
		if err != nil {
			e.t.Fatalf("self 读取失败: %v", err)
		}
		if !reflect.DeepEqual(selfRows.Rows, rows.Rows) {
			e.t.Fatalf("self 与 admin-filter 行集应一致")
		}
		// 7) global 键命中（仅 SQLite 臂：PG 臂不向共享库种 system_account_id='global' 行，
		//    SQL 无方言差异，SQLite 臂已覆盖）。
		if !e.pg {
			globalRows, err := e.store.teamUsageRows(ctx, UsageFilters{}, accessInfo{IsAdmin: true}, rng, 1, 20)
			if err != nil {
				e.t.Fatalf("global 读取失败: %v", err)
			}
			if len(globalRows.Rows) != 1 || globalRows.Rows[0].TeamID != teamA || !dualNearlyEqual(globalRows.Rows[0].Usage.RequestCount, 100) {
				e.t.Fatalf("global 键读取异常: %+v", globalRows.Rows)
			}
		}
		// 8) 分页 pageSize=1 的上界语义。
		paged, err := e.store.teamUsageRows(ctx, UsageFilters{}, ownerAccess, rng, 1, 1)
		if err != nil {
			e.t.Fatalf("分页读取失败: %v", err)
		}
		if len(paged.Rows) != 1 || !paged.HasMore || paged.Total != 2 || paged.Page != 1 || paged.PageSize != 1 {
			e.t.Fatalf("pageSize=1 上界语义异常: total=%d hasMore=%v page=%d pageSize=%d rows=%d",
				paged.Total, paged.HasMore, paged.Page, paged.PageSize, len(paged.Rows))
		}
		// 9) 超页钳制（normalize 到 1001 行窗口上界）。
		np, ns := normalizeUsagePageOptions(5000, 1)
		if np != 1000 || ns != 1 {
			e.t.Fatalf("超页钳制 = (%d, %d)，want (1000, 1)", np, ns)
		}
		farPage, err := e.store.teamUsageRows(ctx, UsageFilters{}, ownerAccess, rng, np, ns)
		if err != nil {
			e.t.Fatalf("超页读取失败: %v", err)
		}
		if len(farPage.Rows) != 0 || farPage.HasMore {
			e.t.Fatalf("超页应空集: %+v", farPage)
		}
		// 10) user 表读取矩阵：team 维度精确匹配（teamA 只读 teamA 行），空 team
		// 只读 teamless 行；details 谓词只显示具体资源档。
		userRowsA, err := e.store.userUsageRows(ctx, UsageFilters{TeamID: teamA}, ownerAccess, rng, 1, 20)
		if err != nil {
			e.t.Fatalf("user details(teamA) 读取失败: %v", err)
		}
		if len(userRowsA.Rows) != 1 {
			e.t.Fatalf("user details(teamA) 应为 1 行（m1 × 具体资源档），实际 %d: %+v", len(userRowsA.Rows), userRowsA.Rows)
		}
		if row := userRowsA.Rows[0]; row.UserName == "" || len(row.TeamNames) != 1 || row.TeamNames[0] != "用量团队A" {
			e.t.Fatalf("user details(teamA) 投影异常: %+v", row)
		}
		blankUserRows, err := e.store.userUsageRows(ctx, UsageFilters{}, ownerAccess, rng, 1, 20)
		if err != nil {
			e.t.Fatalf("user details(teamless) 读取失败: %v", err)
		}
		if len(blankUserRows.Rows) != 1 || !dualNearlyEqual(blankUserRows.Rows[0].Usage.RequestCount, 9) {
			e.t.Fatalf("user details(teamless) 应为 m1 具体资源档 1 行（3 日合计 9），实际 %+v", blankUserRows.Rows)
		}
		userSummary, err := e.store.userUsageSummary(ctx, UsageFilters{TeamID: teamA, GranteeID: m1, ResourceType: "account", ResourceID: accR}, ownerAccess, rng)
		if err != nil {
			e.t.Fatalf("user summary 读取失败: %v", err)
		}
		wantU := userExpect[expectKey(teamA, m1, "account", accR)]
		if !dualNearlyEqual(userSummary.Summary.RequestCount, wantU.requests) || !dualNearlyEqual(userSummary.Summary.TotalCost, wantU.cost) {
			e.t.Fatalf("user summary = (%.2f, %.2f)，want (%.2f, %.2f)",
				userSummary.Summary.RequestCount, userSummary.Summary.TotalCost, wantU.requests, wantU.cost)
		}
		blankUser, err := e.store.userUsageSummary(ctx, UsageFilters{TeamID: "", GranteeID: m1, ResourceType: "all"}, ownerAccess, rng)
		if err != nil {
			e.t.Fatalf("user summary('') 读取失败: %v", err)
		}
		if !dualNearlyEqual(blankUser.Summary.RequestCount, 6) {
			e.t.Fatalf("user '' 合计键 summary = %.2f，want 6", blankUser.Summary.RequestCount)
		}
		// 11) 超 31 天入参被钳制到 trailing 31 天（返回 range 断言，路由层入口）。
		deps := &Deps{Store: e.store}
		clamped, err := deps.resolveUsageRange("2000-01-01", "2030-12-31")
		if err != nil {
			e.t.Fatalf("range 钳制失败: %v", err)
		}
		if clamped.StartDate != "2026-09-01" || clamped.EndDate != "2026-10-01" || clamped.Days != 31 || clamped.MaxDays != 31 {
			e.t.Fatalf("超长 range 应钳制到 trailing 31 天: %+v", clamped)
		}
		inverted, err := deps.resolveUsageRange("2026-10-05", "2026-09-01")
		if err != nil {
			e.t.Fatalf("倒置 range 处理失败: %v", err)
		}
		// 先各自钳制（start 超未来 → today；end 在窗内保留），再折叠 start<=end。
		if inverted.StartDate != "2026-09-01" || inverted.EndDate != "2026-09-01" || inverted.Days != 1 {
			e.t.Fatalf("倒置 range 应钳制后折叠: %+v", inverted)
		}
	})
}

// B11 {id}/usage 冻结表免疫：日摘要行（真值）与 authorization_team_usage_range_windows
// 旧冻结行（诱饵值，键为热窗口锚点区间）并存时，UsageDetail 团队总量必须等于日摘要
// 聚合值。双臂都跑（PG 臂直接对真实 juhe_stats.authorization_team_usage_range_windows
// 造带前缀键的诱饵行）。
func TestDualFrozenRangeWindowImmunity(t *testing.T) {
	runDual(t, func(e *dualEnv) {
		ctx := context.Background()
		owner := e.p + "owner"
		m1 := e.p + "m1"
		teamA := e.p + "teamA"
		e.seedSystemAccount(owner, "active")
		e.seedSystemAccount(m1, "active")
		e.seedTeamWithMembers(teamA, "冻结表团队", m1, owner)
		grp := e.p + "grp-frozen"
		e.seedGroup(grp, owner, "冻结表分组", e.seedProviderChain(), false)

		created, err := e.store.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: grp,
			GranteeType: "team", GranteeID: teamA,
		}, owner)
		if err != nil {
			e.t.Fatalf("团队授权创建失败: %v", err)
		}
		dates := []string{"2026-09-29", "2026-09-30", "2026-10-01"}
		requests := 0
		for idx, date := range dates {
			requests += 2 + idx
			e.exec(`INSERT INTO `+e.stbl("authorization_team_usage_summary_daily")+`
				(system_account_id, stat_date, team_filter_id, resource_filter_type, resource_filter_id,
				 request_count, input_tokens, output_tokens, total_cost_usd, last_used_at, updated_at)
				VALUES (?, ?, ?, 'group', ?, ?, 100, 50, 1.5, ?, '2026-10-01T00:00:00.000Z')`,
				owner, date, teamA, grp, 2+idx, "2026-10-01T09:00:00.000Z")
		}
		// 冻结表诱饵行：同键（owner, 3 日窗, teamA, group, grp），值 999。
		e.exec(`INSERT INTO `+e.stbl("authorization_team_usage_range_windows")+`
			(system_account_id, start_date, end_date, team_filter_id, resource_filter_type, resource_filter_id,
			 request_count, input_tokens, output_tokens, total_cost_usd, last_used_at, updated_at)
			VALUES (?, ?, ?, ?, 'group', ?, 999, 999, 999, 99.9, '2026-10-01T08:00:00.000Z', '2026-10-01T00:00:00.000Z')`,
			owner, dates[0], dates[2], teamA, grp)

		rng := UsageStatsRange{StartDate: dates[0], EndDate: dates[2], Days: 3, MaxDays: 31}
		detail, err := e.store.usageDetailSummary(ctx, created.Item.ID, accessInfo{IsAdmin: true}, rng, 1, 200)
		if err != nil || detail == nil {
			e.t.Fatalf("usage detail 读取失败: detail=%v err=%v", detail, err)
		}
		if detail.Usage.RequestCount != float64(requests) {
			e.t.Fatalf("团队总量 = %.0f，want 日摘要聚合值 %d（冻结诱饵 999 不得被读取）",
				detail.Usage.RequestCount, requests)
		}
		if !dualNearlyEqual(detail.Usage.TotalCost, 4.5) {
			e.t.Fatalf("团队成本 = %.2f，want 4.5（3 天 × 1.5）", detail.Usage.TotalCost)
		}
		if detail.LastUsedAt != "2026-10-01T09:00:00.000Z" {
			e.t.Fatalf("lastUsed = %s，want 日摘要 MAX", detail.LastUsedAt)
		}
	})
}

// B12 到期 reconcile：jobs sweep 翻转 expired 后 ReconcileExpiredGrants 同步 runtime
// 投影（非 active）；usage 读取为纯读（两次结果一致）。
func TestDualExpiredReconcileAndPureUsageRead(t *testing.T) {
	runDual(t, func(e *dualEnv) {
		ctx := context.Background()
		owner := e.p + "owner"
		grantee := e.p + "grantee"
		e.seedSystemAccount(owner, "active")
		e.seedSystemAccount(grantee, "active")
		grp := e.p + "grp-reconcile"
		e.seedGroup(grp, owner, "对账分组", e.seedProviderChain(), false)

		expires := e.now.Add(time.Hour).UTC().Format(time.RFC3339Nano)
		created, err := e.store.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: grp,
			GranteeType: "system_account", GranteeID: grantee, ExpiresAt: &expires,
		}, owner)
		if err != nil {
			e.t.Fatalf("创建授权失败: %v", err)
		}
		rt := e.runtimeRow(grp, grantee)

		// 推进时钟 + 模拟 jobs sweep 只翻 grants 行（runtime 保持 active）。
		e.advance(2 * time.Hour)
		nowText := e.now.UTC().Format("2006-01-02T15:04:05.000Z")
		e.exec(`UPDATE `+e.tbl("resource_authorization_grants")+` SET status = 'expired', revoked_at = ?, updated_at = ? WHERE id = ?`,
			nowText, nowText, created.Item.ID)
		if status := e.runtimeStatus(grp, grantee); status != StatusActive {
			e.t.Fatalf("sweep 只翻 grants：runtime 应保持 active，实际 %s", status)
		}

		// ReconcileExpiredGrants（回看窗口覆盖）→ runtime 投影同步（非 active）。
		// 基数统计已含本 env 置为 expired 的这条；共享 dev 库中其他真实 expired
		// grant 若落在回看窗口内也会被幂等重放，因此断言与基数精确相等。
		eligible := e.count(`SELECT COUNT(*) FROM `+e.tbl("resource_authorization_grants")+`
			WHERE status = 'expired' AND updated_at >= ?`, nowText)
		reconciled, err := e.store.ReconcileExpiredGrants(ctx, 10*time.Minute, 100)
		if err != nil {
			e.t.Fatalf("reconcile 失败: %v", err)
		}
		if reconciled != eligible || eligible < 1 {
			e.t.Fatalf("reconcile 数 = %d，want 回看窗口基数 %d（含本 grant）", reconciled, eligible)
		}
		if status := e.runtimeStatus(grp, grantee); status != StatusExpired {
			e.t.Fatalf("reconcile 后 runtime status = %s，want expired", status)
		}
		if e.runtimeRow(grp, grantee).id != rt.id {
			e.t.Fatalf("reconcile 不得更换 runtime 行 ID")
		}

		// usage 读取不改状态：同参两次，结果一致且状态不变。
		rng := UsageStatsRange{StartDate: "2026-09-29", EndDate: "2026-10-01", Days: 3, MaxDays: 31}
		access := accessInfo{IsAdmin: true, FilterID: owner}
		first, err := e.store.teamUsageRows(ctx, UsageFilters{}, access, rng, 1, 20)
		if err != nil {
			e.t.Fatalf("usage 读取失败: %v", err)
		}
		second, err := e.store.teamUsageRows(ctx, UsageFilters{}, access, rng, 1, 20)
		if err != nil {
			e.t.Fatalf("usage 二次读取失败: %v", err)
		}
		if !reflect.DeepEqual(first, second) {
			e.t.Fatalf("usage 读取必须为纯读，两次结果不一致")
		}
		if status := e.runtimeStatus(grp, grantee); status != StatusExpired {
			e.t.Fatalf("usage 读取后 runtime status 变化: %s", status)
		}
	})
}
