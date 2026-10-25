package circuitstore

// 投影行映射（buildBasePayload 系列）、水合辅助（status seed 派生）与
// loader 小工具的纯函数/SQLite 测试。payload 形状与归档 Node
// accountManagementListItemFromRow / getAccountStatusSnapshotFromProjections 对齐。
// 组B说明：本文件的 loader 建库消费点（TestLoadAccountLockViewRecovery、
// TestNewProjectionItemLoaderValidation、TestLoaderSmallHelpers）随 loader 族
// 保留 SQLite（取证见 final_edges_test.go TestHydratePageFailClosed 注释）；
// 纯函数测试不涉存储，不在迁移面。

import (
	"database/sql"
	"strings"
	"testing"

)

func validScheduleJSON() string {
	// oauthrefresh.ParseScheduleJSON 接受的规范形状（与生产 availability_schedule_json 同构）。
	return `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1,2,3],"start":"01:00","end":"02:00"}]}`
}

// TestBuildBasePayloadOwnerRow 覆盖 owner 行的 payload 映射（含锁/代理/计划）。
func TestBuildBasePayloadOwnerRow(t *testing.T) {
	row := managementRow{
		id: "acc-1", systemAccountID: "sys-1", ownerSystemAccountID: "sys-1",
		configRevision: sql.NullInt64{Int64: 4, Valid: true},
		providerCode:   "openai", providerName: sql.NullString{String: "OpenAI", Valid: true},
		providerProtocolProfileID: "profile-1", protocolCode: "openai", protocolVersion: "v1",
		name: "账户甲", accountType: "api_key",
		concurrencyLimit: 10, priority: 5,
		superPriorityEnabled: 1, fallbackEnabled: 0,
		status: "active", schedulable: 1,
		healthCheckModel: " gpt-4o-mini ", healthCheckEndpointMode: "chat_completions",
		notes:                            sql.NullString{String: "备注", Valid: true},
		availabilityScheduleJSON:         sql.NullString{String: validScheduleJSON(), Valid: true},
		configuredProxyProfileID:         sql.NullString{String: "proxy-1", Valid: true},
		resolvedProxyProfileID:           sql.NullString{String: "proxy-1", Valid: true},
		proxyProfileName:                 sql.NullString{String: "主代理", Valid: true},
		proxyProfileType:                 sql.NullString{String: "socks5", Valid: true},
		proxyProfileEnabled:              0,
		authorizationInstanceSourceID:    sql.NullString{String: "src-9", Valid: true},
		boundGroupID:                     sql.NullString{String: "group-1", Valid: true},
		boundGroupName:                   sql.NullString{String: "默认分组", Valid: true},
		bindingSystemAccountID:           sql.NullString{String: "sys-other", Valid: true},
		boundGroupAccountAuthorizationID: sql.NullString{String: "", Valid: false},
	}
	tags := []map[string]any{{"id": "tag-1", "name": "alpha", "accountCount": 0}}
	lock := &accountLockView{enabled: true, lockState: "LOCKED_IDLE", lockDeathTimeoutSeconds: &[]int{3600}[0], lockRetryIntervalSeconds: &[]int{60}[0]}
	payload := buildBasePayload(row, tags, lock)

	if payload["id"] != "acc-1" || payload["accessType"] != "owner" {
		t.Fatalf("基础键不符: %v", payload)
	}
	if payload["configRevision"] != float64(4) {
		t.Fatalf("configRevision 数值化: %v", payload["configRevision"])
	}
	// BUG-0294/A4 修复后：sql.NullInt64 形状的 configRevision 经 nullInt64Number
	// 解包，Valid 行值 4 原样透传（修复前经 numberValue 被静默回退为默认 1）。
	if payload["concurrencyLimit"] != float64(10) || payload["priority"] != float64(5) {
		t.Fatalf("排序/容量键数值化不符: %v %v", payload["concurrencyLimit"], payload["priority"])
	}
	if payload["superPriorityEnabled"] != true || payload["fallbackEnabled"] != false {
		t.Fatalf("布尔数值化不符: %v %v", payload["superPriorityEnabled"], payload["fallbackEnabled"])
	}
	if payload["clientCompatibility"] != "openai_standard" {
		t.Fatalf("非 gpt 供应商固定 openai_standard: %v", payload["clientCompatibility"])
	}
	if payload["notes"] != "备注" || payload["healthCheckModel"] != "gpt-4o-mini" {
		t.Fatalf("可选文本键不符: %v", payload)
	}
	if payload["proxyProfileId"] != "proxy-1" || payload["proxyProfileName"] != "主代理" || payload["proxyProfileType"] != "socks5" {
		t.Fatalf("代理键不符: %v", payload)
	}
	if payload["proxyProfileEnabled"] != false || payload["proxyProfileUnavailable"] != true {
		t.Fatalf("代理停用必须标记 unavailable: %v", payload)
	}
	schedule, ok := payload["availabilitySchedule"].(map[string]any)
	if !ok {
		t.Fatalf("有效计划 JSON 必须进 payload: %v", payload["availabilitySchedule"])
	}
	if _, exists := schedule["windows"]; !exists {
		t.Fatalf("计划应保留 daily 窗口: %v", schedule)
	}
	if payload["boundGroupId"] != "group-1" || payload["boundGroupName"] != "默认分组" {
		t.Fatalf("分组键不符: %v", payload)
	}
	if _, exists := payload["groupBindStatus"]; exists {
		t.Fatalf("owner 行无授权时不输出 groupBindStatus: %v", payload)
	}
	if payload["authorizationInstanceSourceAccountId"] != "src-9" {
		t.Fatalf("来源账户键不符: %v", payload)
	}
	if payload["lockEnabled"] != true || payload["lockState"] != "LOCKED_IDLE" || payload["lockDeathTimeoutSeconds"] != 3600 {
		t.Fatalf("锁键不符: %v", payload)
	}
	if _, exists := payload["systemAccountId"]; exists {
		t.Fatalf("role=user 不得携带 systemAccountId: %v", payload)
	}
}

// TestBuildBasePayloadAuthorizedRow 覆盖 authorized 行的来源列覆盖与权限形状。
func TestBuildBasePayloadAuthorizedRow(t *testing.T) {
	row := managementRow{
		id: "acc-2", systemAccountID: "sys-2", ownerSystemAccountID: "sys-owner",
		providerCode: "openai", providerName: sql.NullString{String: "OpenAI", Valid: true},
		providerProtocolProfileID: "local-profile", protocolCode: "openai", protocolVersion: "v1",
		name: "授权实例", accountType: "api_key",
		concurrencyLimit: 2, priority: 1,
		status: "active", schedulable: 1,
		authorizationID:                     sql.NullString{String: "auth-1", Valid: true},
		sourceProviderCode:                  sql.NullString{String: "gpt", Valid: true},
		sourceProviderProfileID:             sql.NullString{String: "src-profile", Valid: true},
		sourceType:                          sql.NullString{String: "oauth", Valid: true},
		sourceConcurrencyLimit:              42,
		sourceProxyProfileID:                sql.NullString{String: "src-proxy", Valid: true},
		resolvedProxyProfileID:              sql.NullString{String: "src-proxy", Valid: true},
		proxyProfileEnabled:                 1,
		boundGroupID:                        sql.NullString{String: "group-1", Valid: true},
		bindingSystemAccountID:              sql.NullString{String: "sys-2", Valid: true},
		boundGroupAccountAuthorizationID:    sql.NullString{String: "auth-1", Valid: true},
		boundGroupLocalPriority:             7,
		boundGroupLocalSuperPriorityEnabled: 0,
		boundGroupLocalFallbackEnabled:      1,
		authorizationEffectiveSourceType:    sql.NullString{String: "manual", Valid: true},
		ownerSystemAccountName:              sql.NullString{String: "资源拥有者", Valid: true},
	}
	payload := buildBasePayload(row, nil, nil)
	if payload["accessType"] != "authorized" {
		t.Fatalf("authorized 行 accessType 不符: %v", payload)
	}
	if payload["providerCode"] != "gpt" || payload["type"] != "oauth" || payload["providerProtocolProfileId"] != "src-profile" {
		t.Fatalf("authorized 行应取来源列: %v", payload)
	}
	if payload["concurrencyLimit"] != float64(42) || payload["priority"] != float64(7) {
		t.Fatalf("authorized 行排序键取 group local 值: %v %v", payload["concurrencyLimit"], payload["priority"])
	}
	if payload["clientCompatibility"] != "codex_responses" {
		t.Fatalf("gpt + oauth 固定 codex_responses: %v", payload["clientCompatibility"])
	}
	permissions, ok := payload["permissions"].(map[string]any)
	if !ok || permissions["canEdit"] != false || permissions["canReturnAuthorization"] != true {
		t.Fatalf("authorized 权限形状不符: %v", permissions)
	}
	if payload["accountAuthorizationId"] != "auth-1" || payload["bindingSystemAccountId"] != "sys-2" {
		t.Fatalf("授权绑定键不符: %v", payload)
	}
	if payload["groupBindStatus"] != "bound" {
		t.Fatalf("绑定一致时应为 bound: %v", payload["groupBindStatus"])
	}
	if payload["proxyProfileId"] != "src-proxy" || payload["proxyProfileUnavailable"] != nil && payload["proxyProfileUnavailable"] != false {
		t.Fatalf("来源代理启用时不得标记 unavailable: %v", payload["proxyProfileUnavailable"])
	}
	if payload["ownerSystemAccountName"] != "资源拥有者" {
		t.Fatalf("owner 名称键不符: %v", payload)
	}
}

// TestRowMappingHelpers 覆盖行映射辅助函数的分支。
func TestRowMappingHelpers(t *testing.T) {
	row := managementRow{}
	// clientCompatibility 物理列随供应商解耦退役（设计 §8.11）：配置值恒 nil，
	// 兼容键全量由派生函数回退（clientCompatibilityValue 已删除）。
	// resolvedProxyProfileID。
	if got := resolvedProxyProfileID(row, true); got != "" {
		t.Fatalf("authorized 无来源代理为空: %s", got)
	}
	row.sourceProxyProfileID = sql.NullString{String: "src-proxy", Valid: true}
	if got := resolvedProxyProfileID(row, true); got != "src-proxy" {
		t.Fatalf("authorized 代理取来源: %s", got)
	}
	row.configuredProxyProfileID = sql.NullString{String: "own-proxy", Valid: true}
	if got := resolvedProxyProfileID(row, false); got != "own-proxy" {
		t.Fatalf("owner 代理取自身配置: %s", got)
	}
	// proxyUnavailable（先置 enabled=1 且已解析，保证初始不标记 unavailable）。
	row.proxyProfileEnabled = 1
	row.resolvedProxyProfileID = sql.NullString{String: "own-proxy", Valid: true}
	if proxyUnavailable(row, false) {
		t.Fatal("已解析且启用不得标记 unavailable")
	}
	row.proxyProfileEnabled = 0
	if !proxyUnavailable(row, false) {
		t.Fatal("代理停用必须标记 unavailable")
	}
	row.resolvedProxyProfileID = sql.NullString{}
	if !proxyUnavailable(row, false) {
		t.Fatal("配置代理无法解析必须标记 unavailable")
	}
	// proxyProfileType 白名单。
	for _, validType := range []string{"http", "https", "socks5", "socks5h"} {
		if proxyProfileType(sql.NullString{String: validType, Valid: true}) != validType {
			t.Fatalf("类型 %s 应保留", validType)
		}
	}
	if proxyProfileType(sql.NullString{String: "ftp", Valid: true}) != "" {
		t.Fatal("白名单外类型为空")
	}
	// accessTypeOf / permissionsOf。
	if accessTypeOf(true) != "authorized" || accessTypeOf(false) != "owner" {
		t.Fatal("accessTypeOf 语义不符")
	}
	ownerPermissions := permissionsOf(false, sql.NullString{})
	if ownerPermissions["canEdit"] != true || ownerPermissions["canViewCredentials"] != true {
		t.Fatalf("owner 权限形状不符: %v", ownerPermissions)
	}
	// groupBindStatusOf。
	groupRow := managementRow{systemAccountID: "sys-1"}
	if groupBindStatusOf(groupRow) != "" {
		t.Fatal("无绑定组为空")
	}
	groupRow.boundGroupID = sql.NullString{String: "g", Valid: true}
	groupRow.bindingSystemAccountID = sql.NullString{String: "sys-other", Valid: true}
	if groupBindStatusOf(groupRow) != "" {
		t.Fatal("绑定账户不匹配为空")
	}
	groupRow.bindingSystemAccountID = sql.NullString{String: "sys-1", Valid: true}
	groupRow.authorizationID = sql.NullString{String: "auth-1", Valid: true}
	// 绑定授权 ID 缺失（空串）与授权不一致同判：authorization_unavailable。
	if groupBindStatusOf(groupRow) != "authorization_unavailable" {
		t.Fatalf("绑定授权缺失应判 authorization_unavailable: %s", groupBindStatusOf(groupRow))
	}
	groupRow.boundGroupAccountAuthorizationID = sql.NullString{String: "auth-2", Valid: true}
	if groupBindStatusOf(groupRow) != "authorization_unavailable" {
		t.Fatalf("授权不一致应为 authorization_unavailable: %s", groupBindStatusOf(groupRow))
	}
	groupRow.boundGroupAccountAuthorizationID = sql.NullString{String: "auth-1", Valid: true}
	if groupBindStatusOf(groupRow) != "bound" {
		t.Fatalf("授权一致应为 bound: %s", groupBindStatusOf(groupRow))
	}
	// parseScheduleText。
	if parseScheduleText("", true) != nil || parseScheduleText(validScheduleJSON(), false) != nil {
		t.Fatal("无效输入返回 nil")
	}
	if parseScheduleText("{broken", true) != nil {
		t.Fatal("非法 JSON 返回 nil")
	}
	if got := parseScheduleText(validScheduleJSON(), true); got == nil {
		t.Fatal("有效计划必须解析")
	}
	if schedulePayload(nil) != nil {
		t.Fatal("nil 计划返回 nil")
	}
	// normalizeOpenAIAccountClientCompatibility。
	if got := normalizeOpenAIAccountClientCompatibility("gpt", "oauth", "openai_standard", "openai", "v1", "p"); got != "codex_responses" {
		t.Fatalf("gpt oauth 固定 codex_responses: %s", got)
	}
	if got := normalizeOpenAIAccountClientCompatibility("gpt", "api_key", "anthropic_native", "openai", "v1", "p"); got != "anthropic_native" {
		t.Fatalf("合法配置沿用: %s", got)
	}
	if got := normalizeOpenAIAccountClientCompatibility("gpt", "api_key", "bogus", "openai", "v1", "p"); got != "codex_responses" {
		t.Fatalf("非法配置回退 codex_responses: %s", got)
	}
	if got := normalizeOpenAIAccountClientCompatibility("openai", "oauth", nil, "openai", "v1", "p"); got != "openai_standard" {
		t.Fatalf("非 gpt 固定 openai_standard: %s", got)
	}
	if !isOpenAIProtocolProfile("OpenAI", "V1") || isOpenAIProtocolProfile("openai", "v2") {
		t.Fatal("协议 profile 判定不符")
	}
	for _, valid := range []string{"openai_standard", "codex_responses", "anthropic_native", "claude_code"} {
		if !isValidClientCompatibility(valid) {
			t.Fatalf("%s 应合法", valid)
		}
	}
	if isValidClientCompatibility("other") {
		t.Fatal("白名单外非法")
	}
	// nullableInt。
	if nullableInt(sql.NullInt64{}) != nil {
		t.Fatal("NULL 返回 nil")
	}
	if got := nullableInt(sql.NullInt64{Int64: 9, Valid: true}); got == nil || *got != 9 {
		t.Fatalf("数值转换: %v", got)
	}
}

func TestHydrateRowDerivations(t *testing.T) {
	now := effNow()
	// runtimeKeyOf。
	ownerRow := newSourcesRow("acc-1")
	if runtimeKeyOf(ownerRow) != "acc-1" {
		t.Fatalf("owner 行 runtime key 为账户 id: %s", runtimeKeyOf(ownerRow))
	}
	authorizedRow := authorizedSourcesRow("acc-1")
	authorizedRow.boundGroupID = sql.NullString{String: "group-1", Valid: true}
	authorizedRow.bindingSystemAccountID = sql.NullString{String: "sys-1", Valid: true}
	if got := runtimeKeyOf(authorizedRow); got != "acc-1:authorized:sys-1:group-1:auth-acc-1" {
		t.Fatalf("authorized runtime key 组合不符: %s", got)
	}
	// concurrencyAccountIDOf / apiKeyRuntimeAccountIDOf。
	if concurrencyAccountIDOf(ownerRow) != "acc-1" || apiKeyRuntimeAccountIDOf(ownerRow) != "acc-1" {
		t.Fatal("owner 行并发/汇总维度为自身")
	}
	authorizedRow.authorizationInstanceSourceID = sql.NullString{String: "src-1", Valid: true}
	if concurrencyAccountIDOf(authorizedRow) != "src-1" || apiKeyRuntimeAccountIDOf(authorizedRow) != "src-1" {
		t.Fatal("authorized 行并发/汇总维度为来源账户")
	}
	// groupBindingOf。
	if groupBindingOf(ownerRow) != nil {
		t.Fatal("无绑定组返回 nil")
	}
	boundRow := ownerRow
	boundRow.boundGroupID = sql.NullString{String: "group-1", Valid: true}
	boundRow.bindingSystemAccountID = sql.NullString{String: "sys-1", Valid: true}
	boundRow.boundGroupAccountAuthorizationID = sql.NullString{String: "auth-acc-1", Valid: true}
	boundRow.authorizationID = sql.NullString{String: "auth-acc-1", Valid: true}
	binding := groupBindingOf(boundRow)
	if binding == nil || binding.groupID != "group-1" || binding.groupBindStatus != "bound" {
		t.Fatalf("绑定一致: %+v", binding)
	}
	mismatched := boundRow
	mismatched.authorizationID = sql.NullString{String: "auth-other", Valid: true}
	binding = groupBindingOf(mismatched)
	if binding == nil || binding.groupBindStatus != "authorization_unavailable" {
		t.Fatalf("授权不一致: %+v", binding)
	}
	if bindingGroupID(nil) != "" || bindingStatus(nil) != "" {
		t.Fatal("nil binding 辅助返回空")
	}
	// sourceSchedulablePtr / apiKeyNextProbeAtString。
	if sourceSchedulablePtr(ownerRow) != nil {
		t.Fatal("无 source 调度值返回 nil")
	}
	ownerRow.sourceSchedulable = 1
	if got := sourceSchedulablePtr(ownerRow); got == nil || !*got {
		t.Fatalf("source 调度数值化: %v", got)
	}
	if apiKeyNextProbeAtString(nil) != "" {
		t.Fatal("nil probe 时间为空")
	}
	// authorizationExpired。
	if authorizationExpired("", now) || authorizationExpired("junk", now) {
		t.Fatal("空/非法时间不算过期")
	}
	if !authorizationExpired(effPast, now) {
		t.Fatal("过去时间应过期")
	}
	// ptrStringIf。
	if ptrStringIf("") != nil || ptrStringIf("x") == nil {
		t.Fatal("ptrStringIf 语义不符")
	}
	// maxInt。
	if maxInt(-1, 0) != 0 || maxInt(3, 2) != 3 {
		t.Fatal("maxInt 语义不符")
	}
}

// TestStatusBoundaryAtOfBranches 覆盖边界时间提取的各分支。
func TestStatusBoundaryAtOfBranches(t *testing.T) {
	row := managementRow{
		authorizationExpiresAt: sql.NullString{String: "2030-01-01T00:00:00.000Z", Valid: true},
		sourceAccountExpiresAt: sql.NullString{String: "2031-01-01T00:00:00.000Z", Valid: true},
		accountExpiresAt:       sql.NullString{String: "2032-01-01T00:00:00.000Z", Valid: true},
		sourceCooldownUntil:    sql.NullString{String: "2033-01-01T00:00:00.000Z", Valid: true},
		cooldownUntil:          sql.NullString{String: "2034-01-01T00:00:00.000Z", Valid: true},
	}
	cases := []struct {
		status       string
		quotaResetAt string
		recoveryAt   *string
		want         string
	}{
		{"authorization_expired", "", nil, "2030-01-01T00:00:00.000Z"},
		{"source_expired", "", nil, "2031-01-01T00:00:00.000Z"},
		{"instance_expired", "", nil, "2032-01-01T00:00:00.000Z"},
		{"authorization_quota_exceeded", "2035-01-01T00:00:00.000Z", nil, "2035-01-01T00:00:00.000Z"},
		{"authorization_quota_exceeded", "", nil, ""},
		{"source_cooldown", "", nil, "2033-01-01T00:00:00.000Z"},
		{"instance_cooldown", "", nil, "2034-01-01T00:00:00.000Z"},
		{"instance_error", "", nil, ""},
	}
	for _, tc := range cases {
		availability := blockedAvailability(tc.status, "", "", "", "", "")
		got := statusBoundaryAtOf(&availability, row, tc.quotaResetAt, tc.recoveryAt)
		if tc.want == "" {
			if got != nil {
				t.Fatalf("%s 不应有边界: %v", tc.status, *got)
			}
			continue
		}
		if got == nil || *got != tc.want {
			t.Fatalf("%s 边界不符: %v 期望 %s", tc.status, got, tc.want)
		}
	}
	// runtime_local_suppressed 走 recoveryAt。
	recovery := "2036-01-01T00:00:00.000Z"
	suppressed := blockedAvailability("runtime_local_suppressed", "", "", "", "", "")
	got := statusBoundaryAtOf(&suppressed, row, "", &recovery)
	if got == nil || *got != recovery {
		t.Fatalf("runtime suppression 边界取 recoveryAt: %v", got)
	}
	if statusBoundaryAtOf(nil, row, "", nil) != nil {
		t.Fatal("nil 可用性无边界")
	}
}

// TestHydratePayloadHelpers 覆盖 payload 形状辅助。
func TestHydratePayloadHelpers(t *testing.T) {
	if runtimeAvailabilityPayload(AccountRuntimeAvailability{})["status"] != "" {
		t.Fatal("status 恒输出")
	}
	runtime := AccountRuntimeAvailability{
		Status: "degraded", Reason: "recent_failures", Since: "2026-01-01T00:00:00.000Z",
		ProbePresentation: map[string]any{"schedule": map[string]any{"state": "none"}},
	}
	payload := runtimeAvailabilityPayload(runtime)
	if payload["reason"] != "recent_failures" || payload["since"] != runtime.Since {
		t.Fatalf("runtime payload 不符: %v", payload)
	}
	if payload["probePresentation"] == nil {
		t.Fatal("probePresentation 应透传")
	}
	// diagnosticText 截断（多字节安全：按字节切 95 + …）。
	if diagnosticText("  ") != "" {
		t.Fatal("空白为空串")
	}
	long := strings.Repeat("字", 60) // 180 字节
	got := diagnosticText(long)
	if len(got) != 98 {
		t.Fatalf("96 字符截断（95 字节 + 省略号）: %d 字节", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatal("截断以省略号结尾")
	}
	// circuitSummaryPayload。
	if circuitSummaryPayload(publicCircuitSummary{}) != nil {
		t.Fatal("空 status 返回 nil")
	}
	summary := publicCircuitSummary{Status: "avoided", Reason: "http_502", Since: "s", NextCheckAt: "n"}
	circuitPayload := circuitSummaryPayload(summary)
	if circuitPayload["status"] != "avoided" || circuitPayload["reason"] != "http_502" || circuitPayload["nextCheckAt"] != "n" {
		t.Fatalf("circuit payload 不符: %v", circuitPayload)
	}
	// effectiveAvailabilityPayload。
	blockedPayload := effectiveAvailabilityPayload(blockedAvailability("instance_cooldown", "冷却", "gold", "account", "冷却中", "2030-01-01T00:00:00.000Z"))
	if blockedPayload["retryAt"] != "2030-01-01T00:00:00.000Z" || blockedPayload["blockerScope"] != "account" {
		t.Fatalf("阻塞 payload 保留边界与 scope: %v", blockedPayload)
	}
	availablePayload := effectiveAvailabilityPayload(effectiveAvailability{available: true, status: "available", label: "可调度", color: "green", blockerScope: "account"})
	if _, exists := availablePayload["blockerScope"]; exists {
		t.Fatalf("available payload 不得携带 blockerScope: %v", availablePayload)
	}
	if _, exists := availablePayload["retryAt"]; exists {
		t.Fatalf("available payload 不得携带 retryAt: %v", availablePayload)
	}
}

// TestComposeEntrySynthesis 覆盖单账户合成（status seed + effective + 投影列）。
func TestComposeEntrySynthesis(t *testing.T) {
	loader := &ProjectionItemLoader{}
	now := effNow()
	row := newSourcesRow("acc-1")
	row.status = "active"
	row.providerCode = "openai"
	row.providerProtocolProfileID = "profile-1"
	row.protocolCode = "openai"
	row.protocolVersion = "v1"
	row.name = "账户甲"
	row.accountType = "api_key"
	row.schedulable = 1
	row.concurrencyLimit = 10
	row.priority = 5
	row.superPriorityEnabled = 1
	row.fallbackEnabled = 0
	entry, err := loader.composeEntry(composeInput{
		row:         row,
		payload:     buildBasePayload(row, nil, nil),
		now:         now,
		concurrency: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.effectiveStatus != "active" || !entry.effectiveAvailable {
		t.Fatalf("active 可调度账户分类不符: %+v", entry)
	}
	if entry.currentConcurrency != 4 || entry.providerCode != "openai" || entry.priority != 5 {
		t.Fatalf("投影列输入不符: %+v", entry)
	}
	if entry.payload["effectiveAvailability"] == nil || entry.payload["availabilityPresentation"] == nil {
		t.Fatalf("payload 必须携带可用性快照键: %v", entry.payload)
	}
	if entry.payload["currentConcurrency"] != 4 {
		t.Fatalf("payload 并发覆盖: %v", entry.payload["currentConcurrency"])
	}
	if _, exists := entry.payload["lastUsedAt"]; exists {
		t.Fatalf("无 last_used_at 不得输出该键: %v", entry.payload)
	}

	// 冷却中的账户：cooldownUntil 未来 → effectiveStatus=disabled + 边界时间。
	cooledRow := row
	cooledRow.cooldownUntil = sql.NullString{String: effFuture, Valid: true}
	entry, err = loader.composeEntry(composeInput{
		row:     cooledRow,
		payload: buildBasePayload(cooledRow, nil, nil),
		now:     now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.effectiveStatus != "temporary_unavailable" || entry.effectiveAvailable {
		t.Fatalf("冷却账户应不可用: %+v", entry)
	}
	if entry.statusBoundaryAt == nil || *entry.statusBoundaryAt != effFuture {
		t.Fatalf("冷却边界应取 cooldownUntil: %v", entry.statusBoundaryAt)
	}
	if entry.cooldownUntil == nil || *entry.cooldownUntil != effFuture {
		t.Fatalf("冷却候选应保留: %v", entry.cooldownUntil)
	}
	// payload 携带 todayUsage 与 effectiveAvailability。
	effective, ok := entry.payload["effectiveAvailability"].(map[string]any)
	if !ok || effective["available"] != false || effective["retryAt"] != effFuture {
		t.Fatalf("有效可用性 payload 不符: %v", effective)
	}

	// 无法唯一归类的组合由状态机内部矛盾产生，行级构造不可达
	// （空集合路径已在 effective_availability_matrix_test.go 直接覆盖）。
}

func TestBuildBasePayloadOmitsNullProviderName(t *testing.T) {
	row := managementRow{
		id: "acc-generic", systemAccountID: "sys-1", ownerSystemAccountID: "sys-1",
		providerCode: "", providerName: sql.NullString{},
		status: "active", schedulable: 1, accountType: "api_key",
	}
	payload := buildBasePayload(row, nil, nil)
	if _, exists := payload["providerName"]; exists {
		t.Fatalf("NULL provider_name 不得写入 providerName: %v", payload["providerName"])
	}
	if got := payload["providerCode"]; got != "" {
		t.Fatalf("providerCode = %v want empty", got)
	}
	row.providerName = sql.NullString{String: "OpenAI", Valid: true}
	payload = buildBasePayload(row, nil, nil)
	if got := payload["providerName"]; got != "OpenAI" {
		t.Fatalf("有效 provider_name 必须保留: %v", got)
	}
}
