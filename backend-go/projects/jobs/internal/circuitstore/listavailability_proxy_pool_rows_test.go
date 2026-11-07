package circuitstore

// 代理池投影（P0-1b）行映射测试：池绑定账户的 §8.5 payload 字段族、池级不
// 可用状态区分（已停用/无可用成员/当前成员不可用）与待分配缺省，以及
// managementPageSQL 的池 join/有效绑定解析构造。不涉存储（loader PG 臂不迁
// PG，SQL 仅断言构造文本），形状依据 docs/functions/代理池设计.md §5.2/§8.5。

import (
	"database/sql"
	"strings"
	"testing"
)

// poolBoundRow 构造一行池绑定账户（池 P 启用、可分配成员数默认 3、无租约），
// direct 代理列保持 join 未命中（effective NULL）的零值形状。
func poolBoundRow() managementRow {
	return managementRow{
		id:                             "pool-acc-1",
		systemAccountID:                "sys-1",
		ownerSystemAccountID:           "sys-1",
		status:                         "active",
		schedulable:                    1,
		effectiveProxyPoolID:           mustNullString("pool-1"),
		proxyPoolName:                  mustNullString("出口池A"),
		proxyPoolEnabled:               true,
		proxyPoolAssignableMemberCount: mustNullInt64(3),
	}
}

func mustNullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: true}
}

func mustNullInt64(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

// TestBuildBasePayloadPoolPendingAssignment 覆盖“待分配”：池启用、有可分配
// 成员、无租约 → 不可用标记必须缺省，member 字段族不写。
func TestBuildBasePayloadPoolPendingAssignment(t *testing.T) {
	payload := buildBasePayload(poolBoundRow(), nil, nil)
	if payload["proxyTargetType"] != "pool" {
		t.Fatalf("池绑定必须写 proxyTargetType=pool: %v", payload["proxyTargetType"])
	}
	if payload["proxyPoolId"] != "pool-1" || payload["proxyPoolName"] != "出口池A" || payload["proxyPoolEnabled"] != true {
		t.Fatalf("池字段族不符: %v", payload)
	}
	if payload["proxyPoolAssignableMemberCount"] != float64(3) {
		t.Fatalf("可分配成员数必须进 payload: %v", payload["proxyPoolAssignableMemberCount"])
	}
	for _, key := range []string{"proxyMemberProfileId", "proxyMemberProfileName", "proxyMemberProfileType", "proxyMemberProfileEnabled"} {
		if _, exists := payload[key]; exists {
			t.Fatalf("无租约不得写 %s: %v", key, payload[key])
		}
	}
	if _, exists := payload["proxyProfileUnavailable"]; exists {
		t.Fatalf("待分配（无租约且计数>0）不可用标记必须缺省: %v", payload["proxyProfileUnavailable"])
	}
	if _, exists := payload["proxyProfileErrorMessage"]; exists {
		t.Fatalf("待分配不得写 proxyProfileErrorMessage: %v", payload["proxyProfileErrorMessage"])
	}
	// 池绑定时 direct 代理字段族不得写。
	for _, key := range []string{"proxyProfileId", "proxyProfileName", "proxyProfileType", "proxyProfileEnabled"} {
		if _, exists := payload[key]; exists {
			t.Fatalf("池绑定不得写 %s: %v", key, payload[key])
		}
	}
}

// TestBuildBasePayloadPoolWithHealthyLease 覆盖健康租约：member 字段族齐写且
// 不标记不可用。
func TestBuildBasePayloadPoolWithHealthyLease(t *testing.T) {
	row := poolBoundRow()
	row.leaseMemberProfileID = mustNullString("member-9")
	row.leaseMemberProfileName = mustNullString("成员代理9")
	row.leaseMemberProfileType = mustNullString("socks5")
	row.leaseMemberProfileEnabled = true
	row.leaseMemberEnabled = true
	row.leaseMemberPoolHealthStatus = mustNullString("healthy")
	payload := buildBasePayload(row, nil, nil)
	if payload["proxyMemberProfileId"] != "member-9" || payload["proxyMemberProfileName"] != "成员代理9" ||
		payload["proxyMemberProfileType"] != "socks5" || payload["proxyMemberProfileEnabled"] != true {
		t.Fatalf("租约成员字段族不符: %v", payload)
	}
	if _, exists := payload["proxyProfileUnavailable"]; exists {
		t.Fatalf("健康租约不得标记不可用: %v", payload["proxyProfileUnavailable"])
	}
}

// TestBuildBasePayloadPoolUnavailableStates 覆盖三种池级不可用的状态区分与
// 判定优先级（池停用 > 无可用成员 > 当前成员不可用）。
func TestBuildBasePayloadPoolUnavailableStates(t *testing.T) {
	memberUnavailableRow := func() managementRow {
		row := poolBoundRow()
		row.leaseMemberProfileID = mustNullString("member-9")
		row.leaseMemberProfileEnabled = true
		row.leaseMemberEnabled = true
		return row
	}
	cases := []struct {
		name        string
		mutate      func(row managementRow) managementRow
		wantMessage string
	}{
		{
			name:        "池停用",
			mutate:      func(row managementRow) managementRow { row.proxyPoolEnabled = false; return row },
			wantMessage: "代理池「出口池A」已停用",
		},
		{
			name: "池停用优先于成员不可用",
			mutate: func(row managementRow) managementRow {
				row = memberUnavailableRow()
				row.proxyPoolEnabled = false
				row.leaseMemberPoolHealthStatus = mustNullString("dead")
				return row
			},
			wantMessage: "代理池「出口池A」已停用",
		},
		{
			name: "无可分配成员",
			mutate: func(row managementRow) managementRow {
				row.proxyPoolAssignableMemberCount = mustNullInt64(0)
				return row
			},
			wantMessage: "代理池「出口池A」无可用成员",
		},
		{
			name: "无可用成员优先于成员不可用",
			mutate: func(row managementRow) managementRow {
				row = memberUnavailableRow()
				row.proxyPoolAssignableMemberCount = mustNullInt64(0)
				row.leaseMemberPoolHealthStatus = mustNullString("dead")
				return row
			},
			wantMessage: "代理池「出口池A」无可用成员",
		},
		{
			name: "健康 dead 成员",
			mutate: func(row managementRow) managementRow {
				row = memberUnavailableRow()
				row.leaseMemberPoolHealthStatus = mustNullString("dead")
				return row
			},
			wantMessage: "代理池「出口池A」当前成员不可用，将在下次请求重新分配",
		},
		{
			name: "成员行人工下架",
			mutate: func(row managementRow) managementRow {
				row = memberUnavailableRow()
				row.leaseMemberEnabled = false
				return row
			},
			wantMessage: "代理池「出口池A」当前成员不可用，将在下次请求重新分配",
		},
		{
			name: "成员代理档案停用",
			mutate: func(row managementRow) managementRow {
				row = memberUnavailableRow()
				row.leaseMemberProfileEnabled = false
				return row
			},
			wantMessage: "代理池「出口池A」当前成员不可用，将在下次请求重新分配",
		},
		{
			name: "连续失败 streak 达阈值",
			mutate: func(row managementRow) managementRow {
				row = memberUnavailableRow()
				row.leaseMemberPoolHealthStatus = mustNullString("healthy")
				row.leaseMemberPoolHealthStreak = mustNullInt64(3)
				return row
			},
			wantMessage: "代理池「出口池A」当前成员不可用，将在下次请求重新分配",
		},
		{
			name: "健康 NULL 按 unknown（可分配）",
			mutate: func(row managementRow) managementRow {
				row = memberUnavailableRow()
				row.leaseMemberPoolHealthStatus = sql.NullString{}
				return row
			},
			wantMessage: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := buildBasePayload(tc.mutate(poolBoundRow()), nil, nil)
			unavailable, exists := payload["proxyProfileUnavailable"]
			if tc.wantMessage == "" {
				if exists {
					t.Fatalf("健康 NULL 按 unknown 不得标记不可用: %v", payload)
				}
				return
			}
			if unavailable != true {
				t.Fatalf("%s 必须标记 proxyProfileUnavailable: %v", tc.name, payload)
			}
			if got, _ := payload["proxyProfileErrorMessage"].(string); got != tc.wantMessage {
				t.Fatalf("%s 文案不符: got %q want %q", tc.name, got, tc.wantMessage)
			}
		})
	}
}

// TestProxyPoolAvailabilityMissingPoolName 覆盖池名缺失（LEFT JOIN 未命中）
// 时的文案回退。
func TestProxyPoolAvailabilityMissingPoolName(t *testing.T) {
	row := poolBoundRow()
	row.proxyPoolName = sql.NullString{}
	row.proxyPoolEnabled = false
	status := proxyPoolAvailability(row)
	if !status.unavailable || status.message != "代理池已停用" {
		t.Fatalf("无池名文案应回退: %+v", status)
	}
}

// TestResolvedProxyProfileIDPoolOverride 覆盖池绑定对直接代理配置的覆盖：
// proxyProfileId 只写直接代理有效 profile。
func TestResolvedProxyProfileIDPoolOverride(t *testing.T) {
	row := poolBoundRow()
	row.configuredProxyProfileID = mustNullString("own-proxy")
	if got := resolvedProxyProfileID(row, false); got != "" {
		t.Fatalf("池绑定 owner 行不得回落配置代理: %q", got)
	}
	row.sourceProxyProfileID = mustNullString("src-proxy")
	if got := resolvedProxyProfileID(row, true); got != "" {
		t.Fatalf("池绑定 authorized 行不得回落来源代理: %q", got)
	}
	row.effectiveProxyPoolID = sql.NullString{}
	if got := resolvedProxyProfileID(row, true); got != "src-proxy" {
		t.Fatalf("非池绑定 authorized 行保持来源代理: %q", got)
	}
	if got := resolvedProxyProfileID(row, false); got != "own-proxy" {
		t.Fatalf("非池绑定 owner 行保持配置代理: %q", got)
	}
}

// TestResolvedProxyProfileIDInstanceFallback 覆盖 §5.2 的实例回落：source 无池
// 无直接代理时使用实例自身的直接代理；source 有池时实例直接代理不参与。
func TestResolvedProxyProfileIDInstanceFallback(t *testing.T) {
	row := poolBoundRow()
	row.effectiveProxyPoolID = sql.NullString{}
	row.authorizationInstanceSourceID = mustNullString("src-1")
	row.sourceProxyProfileID = sql.NullString{}
	row.configuredProxyProfileID = mustNullString("instance-proxy")
	if got := resolvedProxyProfileID(row, true); got != "instance-proxy" {
		t.Fatalf("source 无绑定时应回落实例直接代理: %q", got)
	}
	row.sourceProxyProfileID = mustNullString("src-proxy")
	if got := resolvedProxyProfileID(row, true); got != "src-proxy" {
		t.Fatalf("source 直接代理必须优先: %q", got)
	}
	row.effectiveProxyPoolID = mustNullString("pool-1")
	row.sourceProxyProfileID = sql.NullString{}
	if got := resolvedProxyProfileID(row, true); got != "" {
		t.Fatalf("source 绑池时实例直接代理不得参与: %q", got)
	}
}

// TestBuildBasePayloadDirectTargetType 覆盖 direct 行的 proxyTargetType 与
// 无代理行的缺省。
func TestBuildBasePayloadDirectTargetType(t *testing.T) {
	direct := managementRow{
		id: "acc-direct", systemAccountID: "sys-1", ownerSystemAccountID: "sys-1",
		providerCode: "openai", status: "active", schedulable: 1, accountType: "api_key",
		configuredProxyProfileID: mustNullString("proxy-1"),
		resolvedProxyProfileID:   mustNullString("proxy-1"),
		proxyProfileName:         mustNullString("主代理"),
		proxyProfileType:         mustNullString("http"),
		proxyProfileEnabled:      1,
	}
	payload := buildBasePayload(direct, nil, nil)
	if payload["proxyTargetType"] != "profile" || payload["proxyProfileId"] != "proxy-1" {
		t.Fatalf("direct 行 target 判别不符: %v", payload)
	}
	if _, exists := payload["proxyProfileUnavailable"]; exists {
		t.Fatalf("启用直接代理不得标记不可用: %v", payload)
	}
	empty := managementRow{id: "acc-none", systemAccountID: "sys-1", ownerSystemAccountID: "sys-1",
		status: "active", schedulable: 1, accountType: "api_key"}
	payload = buildBasePayload(empty, nil, nil)
	if _, exists := payload["proxyTargetType"]; exists {
		t.Fatalf("无池无直接代理不得写 proxyTargetType: %v", payload)
	}
}

// TestManagementPageSQLProxyPoolConstruction 断言分页 SQL 的池绑定面构造：
// CTE 带出池列与租约键、有效绑定 CASE、池/租约/成员/档案 join、可分配子查询，
// 且直接代理 join 不再使用旧 COALESCE。
func TestManagementPageSQLProxyPoolConstruction(t *testing.T) {
	loader := &ProjectionItemLoader{}
	sql := loader.managementPageSQL(1)
	needles := []string{
		"accounts.proxy_pool_id AS configured_proxy_pool_id",
		"source_accounts.proxy_pool_id AS source_proxy_pool_id",
		"COALESCE(accounts.authorization_instance_source_account_id, accounts.id) AS proxy_lease_account_id",
		"WHEN source_accounts.proxy_pool_id IS NOT NULL THEN source_accounts.proxy_pool_id",
		"WHEN accounts.authorization_instance_source_account_id IS NOT NULL THEN NULL",
		"WHEN source_accounts.proxy_pool_id IS NOT NULL THEN NULL",
		"WHEN source_accounts.proxy_profile_id IS NOT NULL THEN source_accounts.proxy_profile_id",
		"AS effective_proxy_pool_id",
		"AS effective_proxy_profile_id",
		"juhe_business.proxy_pools proxy_pools\n      ON proxy_pools.id = account_rows.effective_proxy_pool_id",
		"juhe_business.account_proxy_leases lease\n      ON lease.account_id = account_rows.proxy_lease_account_id\n      AND lease.pool_id = account_rows.effective_proxy_pool_id",
		"juhe_business.proxy_pool_members lease_member\n      ON lease_member.pool_id = lease.pool_id\n      AND lease_member.proxy_profile_id = lease.proxy_profile_id",
		"juhe_business.proxy_profiles lease_profile\n      ON lease_profile.id = lease.proxy_profile_id",
		"juhe_business.proxy_profiles proxy_profiles\n      ON proxy_profiles.id = account_rows.effective_proxy_profile_id",
		"proxy_profiles.id AS resolved_proxy_profile_id",
		"AS proxy_pool_name",
		"AS proxy_pool_enabled",
		"AS proxy_pool_assignable_member_count",
		"AS lease_member_profile_id",
		"AS lease_member_profile_name",
		"AS lease_member_profile_type",
		"AS lease_member_profile_enabled",
		"AS lease_member_enabled",
		"AS lease_member_pool_health_status",
		"assignable_members.enabled = TRUE",
		"assignable_profiles.enabled = TRUE",
		"assignable_pools.enabled = TRUE",
		"COALESCE(assignable_profiles.pool_health_status, 'unknown') <> 'dead'",
	}
	for _, needle := range needles {
		if !strings.Contains(sql, needle) {
			t.Fatalf("分页 SQL 缺少池绑定片段 %q", needle)
		}
	}
	if strings.Contains(sql, "COALESCE(account_rows.source_proxy_profile_id, account_rows.configured_proxy_profile_id)") {
		t.Fatal("直接代理 join 不得再使用旧 COALESCE(source, configured)")
	}
	if !strings.Contains(sql, "LEFT JOIN LATERAL") {
		t.Fatal("既有 LATERAL 绑定选择不得被移除")
	}
}
