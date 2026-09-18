package accounts

// w14l 覆盖率收尾（二）：纯函数臂、批量字段臂、授权调度消息臂、
// 失效回调错误臂与 retryQueue 状态臂。全部为包内直驱，不依赖网络。

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"testing"
	"time"
)

// --- 授权调度 body 解析臂 ---

func TestW14LAuthorizedDispatchBodyArms(t *testing.T) {
	if _, message := parseAuthorizedDispatchBody(map[string]any{"bogus": 1}); message == "" {
		t.Fatal("未知键应拒绝")
	}
	if _, message := parseAuthorizedDispatchBody(map[string]any{}); message == "" {
		t.Fatal("空 body 应拒绝")
	}
	if _, message := parseAuthorizedDispatchBody(map[string]any{"clearFailureState": false}); message == "" {
		t.Fatal("单独 clearFailureState=false 应拒绝")
	}
	invalidBodies := []map[string]any{
		{"priority": float64(1)},
		{"expectedConfigRevision": "x", "priority": float64(1)},
		{"expectedConfigRevision": float64(0.5), "priority": float64(1)},
		{"expectedConfigRevision": float64(0), "priority": float64(1)},
		{"expectedConfigRevision": float64(1), "status": 3},
		{"expectedConfigRevision": float64(1), "status": "bogus"},
		{"expectedConfigRevision": float64(1), "priority": "x"},
		{"expectedConfigRevision": float64(1), "priority": float64(1.5)},
		{"expectedConfigRevision": float64(1), "priority": float64(-1)},
		{"expectedConfigRevision": float64(1), "superPriorityEnabled": "yes"},
		{"expectedConfigRevision": float64(1), "fallbackEnabled": 1},
		{"expectedConfigRevision": float64(1), "clearFailureState": "true"},
	}
	for index, body := range invalidBodies {
		if _, message := parseAuthorizedDispatchBody(body); message == "" {
			t.Fatalf("非法 body #%d 应拒绝：%v", index, body)
		}
	}
	input, message := parseAuthorizedDispatchBody(map[string]any{
		"expectedConfigRevision": float64(2), "status": "active", "priority": float64(3),
		"superPriorityEnabled": true, "clearFailureState": true,
	})
	if message != "" || input.ExpectedConfigRevision != 2 || input.Status == nil || input.Priority == nil ||
		input.SuperPriorityEnabled == nil || !input.ClearFailureState {
		t.Fatalf("合法 body 应解析成功：%+v %q", input, message)
	}
}

func TestW14LUnavailableMessageArms(t *testing.T) {
	f := newW14LFaultFixture(t)
	strNull := func(value string) sql.NullString { return sql.NullString{String: value, Valid: true} }
	intNull := func(value int64) sql.NullInt64 { return sql.NullInt64{Int64: value, Valid: true} }
	base := func(mutate func(*authorizedDispatchRow)) *authorizedDispatchRow {
		row := &authorizedDispatchRow{
			id:          "acc-w14l-msg",
			status:      "active",
			schedulable: 1,
			sourceID:    strNull("src-1"),
			sourceStatus: strNull("active"),
			sourceSchedulable: intNull(1),
		}
		if mutate != nil {
			mutate(row)
		}
		return row
	}
	cases := []struct {
		name    string
		row     *authorizedDispatchRow
		recover bool
	}{
		{"expired", base(func(r *authorizedDispatchRow) { r.authorizationStatus = strNull("expired") }), true},
		{"paused", base(func(r *authorizedDispatchRow) { r.authorizationStatus = strNull("paused") }), true},
		{"revoked", base(func(r *authorizedDispatchRow) { r.authorizationStatus = strNull("revoked") }), true},
		{"auth-expired-at", base(func(r *authorizedDispatchRow) { r.authorizationExpiresAt = strNull("2020-01-01T00:00:00.000Z") }), true},
		{"missing-source", base(func(r *authorizedDispatchRow) { r.sourceID = sql.NullString{} }), true},
		{"source-expired", base(func(r *authorizedDispatchRow) { r.sourceLastErrorCode = strNull("account_expired") }), true},
		{"source-disabled", base(func(r *authorizedDispatchRow) { r.sourceStatus = strNull("disabled") }), true},
		{"source-pending", base(func(r *authorizedDispatchRow) { r.sourceStatus = strNull("pending_test") }), true},
		{"source-error", base(func(r *authorizedDispatchRow) { r.sourceStatus = strNull("error") }), true},
		{"source-rate-limited", base(func(r *authorizedDispatchRow) { r.sourceStatus = strNull("rate_limited") }), true},
		{"source-temp-unavailable", base(func(r *authorizedDispatchRow) { r.sourceStatus = strNull("temporary_unavailable") }), true},
		{"source-quality-isolated", base(func(r *authorizedDispatchRow) { r.sourceStatus = strNull("quality_isolated") }), true},
		{"source-cooling", base(func(r *authorizedDispatchRow) { r.sourceCooldownUntil = strNull("2099-01-01T00:00:00.000Z") }), true},
		{"source-unschedulable", base(func(r *authorizedDispatchRow) { r.sourceSchedulable = intNull(0) }), true},
		{"account-expired", base(func(r *authorizedDispatchRow) { r.lastErrorCode = strNull("account_expired") }), true},
		{"local-disabled", base(func(r *authorizedDispatchRow) { r.status = "disabled" }), false},
		{"local-pending", base(func(r *authorizedDispatchRow) { r.status = "pending_test" }), false},
		{"local-error", base(func(r *authorizedDispatchRow) { r.status = "error" }), false},
		{"local-rate-limited", base(func(r *authorizedDispatchRow) { r.status = "rate_limited" }), false},
		{"local-temp-unavailable", base(func(r *authorizedDispatchRow) { r.status = "temporary_unavailable" }), false},
		{"local-quality-isolated", base(func(r *authorizedDispatchRow) { r.status = "quality_isolated" }), false},
		{"local-cooling", base(func(r *authorizedDispatchRow) { r.cooldownUntil = strNull("2099-01-01T00:00:00.000Z") }), false},
		{"local-unschedulable", base(func(r *authorizedDispatchRow) { r.schedulable = 0 }), false},
		{"available", base(nil), false},
	}
	for _, testCase := range cases {
		message := f.store.authorizedDispatchUnavailableMessage(testCase.row, testCase.recover)
		if testCase.name == "available" {
			if message != "" {
				t.Fatalf("%s 应可用：%s", testCase.name, message)
			}
			continue
		}
		if message == "" {
			t.Fatalf("%s 应有不可用消息", testCase.name)
		}
	}
}

// --- 授权调度事务补丁臂（直接驱动，真实事务末尾回滚） ---

func TestW14LPatchAuthorizedDispatchTxArms(t *testing.T) {
	f := newW14LFaultFixture(t)
	strNull := func(value string) sql.NullString { return sql.NullString{String: value, Valid: true} }
	ptrString := func(value string) *string { return &value }
	ptrBool := func(value bool) *bool { return &value }
	ptrInt := func(value int) *int { return &value }
	activeRow := func() *authorizedDispatchRow {
		return &authorizedDispatchRow{
			id: "acc-w14l-dispatch", configRevision: 1, systemAccountID: f.owner,
			name: "w14l-dispatch", status: "active", schedulable: 1,
			sourceID:     strNull("src-1"),
			sourceStatus: strNull("active"),
		}
	}
	binding := func() *authorizedDispatchBinding {
		return &authorizedDispatchBinding{groupID: "grp-1", accountAuthorizationID: "aa-1"}
	}

	// 待检查账户拒绝。
	tx, err := f.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	pending := activeRow()
	pending.status = "pending_test"
	if _, err := f.store.patchAuthorizedDispatchTx(context.Background(), tx, pending, binding(),
		AuthorizedDispatchInput{ExpectedConfigRevision: 1, Status: ptrString("active")}); err == nil {
		t.Fatal("待检查账户应拒绝激活")
	}
	// 超级优先 + 降级互斥。
	if _, err := f.store.patchAuthorizedDispatchTx(context.Background(), tx, activeRow(), binding(),
		AuthorizedDispatchInput{ExpectedConfigRevision: 1, SuperPriorityEnabled: ptrBool(true), FallbackEnabled: ptrBool(true)}); err == nil {
		t.Fatal("超级优先 + 降级应互斥")
	}
	// 状态停用：状态与可调度变更入 changes。
	result, err := f.store.patchAuthorizedDispatchTx(context.Background(), tx, activeRow(), binding(),
		AuthorizedDispatchInput{ExpectedConfigRevision: 1, Status: ptrString("disabled")})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Changes) == 0 {
		t.Fatal("停用应产生变更")
	}
	// 优先级 / 超级 / 降级各字段变更与补丁。
	result, err = f.store.patchAuthorizedDispatchTx(context.Background(), tx, activeRow(), binding(),
		AuthorizedDispatchInput{ExpectedConfigRevision: 1, Priority: ptrInt(7),
			SuperPriorityEnabled: ptrBool(true), FallbackEnabled: ptrBool(false)})
	if err != nil {
		t.Fatal(err)
	}
	if result.Patch.Priority == nil || result.Patch.SuperPriorityEnabled == nil {
		t.Fatalf("优先级与超级优先应进入补丁：%+v", result.Patch)
	}
	// 清除失败状态。
	if _, err := f.store.patchAuthorizedDispatchTx(context.Background(), tx, activeRow(), binding(),
		AuthorizedDispatchInput{ExpectedConfigRevision: 1, ClearFailureState: true}); err != nil {
		t.Fatal(err)
	}
	// 无实际变更。
	if _, err := f.store.patchAuthorizedDispatchTx(context.Background(), tx, activeRow(), binding(),
		AuthorizedDispatchInput{ExpectedConfigRevision: 1, Priority: ptrInt(0)}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

// --- 时间计划解析臂 ---

func TestW14LScheduleNormalizeArms(t *testing.T) {
	denyAll := func(extra map[string]any) map[string]any {
		object := map[string]any{
			"enabled": true, "timezone": "UTC", "mode": "deny_windows",
			"windows": []any{map[string]any{"daysOfWeek": []any{1, 2, 3, 4, 5, 6, 7}, "start": "00:00", "end": "23:59"}},
		}
		for key, value := range extra {
			object[key] = value
		}
		return object
	}
	// 拒绝例外：非法动作 / deny 带窗口 / allow 无窗口 / 缺日期 / 多余键 / 非对象 / 非列表。
	badSchedules := []map[string]any{
		denyAll(map[string]any{"exceptions": "not-a-list"}),
		denyAll(map[string]any{"exceptions": []any{"not-an-object"}}),
		denyAll(map[string]any{"exceptions": []any{map[string]any{"date": "2026-03-01", "action": "allow", "windows": []any{}, "extra": 1}}}),
		denyAll(map[string]any{"exceptions": []any{map[string]any{"date": "", "action": "allow", "windows": []any{map[string]any{"start": "01:00", "end": "02:00"}}}}}),
		denyAll(map[string]any{"exceptions": []any{map[string]any{"date": "2026-03-01", "action": "toggle"}}}),
		denyAll(map[string]any{"exceptions": []any{map[string]any{"date": "2026-03-01", "action": "deny", "windows": []any{map[string]any{"start": "01:00", "end": "02:00"}}}}}),
		denyAll(map[string]any{"exceptions": []any{map[string]any{"date": "2026-03-01", "action": "allow", "windows": []any{}}}}),
		denyAll(map[string]any{"exceptions": []any{map[string]any{"date": "2026-13-01", "action": "deny"}}}),
	}
	for index, object := range badSchedules {
		if _, err := NormalizeSchedule(object); err == nil {
			t.Fatalf("非法时间计划 #%d 应拒绝", index)
		}
	}
	// 合法：允许例外 + 日期范围。
	schedule, err := NormalizeSchedule(denyAll(map[string]any{
		"dateRange":  map[string]any{"startDate": "2026-01-01", "endDate": "2026-12-31"},
		"exceptions": []any{map[string]any{"date": "2026-03-01", "action": "allow", "windows": []any{map[string]any{"start": "01:00", "end": "02:00"}}}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if schedule.DateRange == nil || len(schedule.Exceptions) != 1 {
		t.Fatalf("例外与日期范围应保留：%+v", schedule)
	}
	// 日期范围倒置与时段错误。
	if _, err := NormalizeSchedule(map[string]any{
		"enabled": true, "timezone": "UTC", "mode": "allow_windows",
		"windows":   []any{map[string]any{"daysOfWeek": []any{1}, "start": "01:00", "end": "02:00"}},
		"dateRange": map[string]any{"startDate": "2026-12-31", "endDate": "2026-01-01"},
	}); err == nil {
		t.Fatal("日期范围倒置应拒绝")
	}
	if _, err := NormalizeSchedule(map[string]any{
		"enabled": true, "timezone": "UTC", "mode": "allow_windows",
		"windows": []any{map[string]any{"daysOfWeek": []any{1}, "start": "01:00", "end": "01:00"}},
	}); err == nil {
		t.Fatal("开始等于停止应拒绝")
	}
	if _, err := NormalizeSchedule(map[string]any{
		"enabled": true, "timezone": "Bogus/Zone", "mode": "allow_windows",
		"windows": []any{map[string]any{"daysOfWeek": []any{1}, "start": "01:00", "end": "02:00"}},
	}); err == nil {
		t.Fatal("非法时区应拒绝")
	}
	// 全天拒绝计划：ScheduleStatus 命中 disabled 覆盖，NextScheduleCheckAt 无边界。
	denied, err := NormalizeSchedule(denyAll(nil))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 3, 10, 8, 0, 0, 0, time.UTC)
	if override, ok := ScheduleStatus(denied, now); !ok || override != "disabled" {
		t.Fatalf("全天拒绝应覆盖为 disabled：%q %v", override, ok)
	}
	if _, ok := NextScheduleCheckAt(denied, now); ok {
		t.Fatal("全天拒绝不应有下一检查点")
	}
	// JSON 往返与解析失败。
	raw, ok := ScheduleJSON(denied)
	if !ok || raw == "" {
		t.Fatal("计划应可序列化")
	}
	parsed, err := ParseScheduleJSON(raw)
	if err != nil || parsed == nil {
		t.Fatalf("JSON 应可解析：%v", err)
	}
	if _, err := ParseScheduleJSON("{bogus"); err == nil {
		t.Fatal("非法 JSON 应拒绝")
	}
	// 日期键进阶（跨天窗口边界）。
	if nextDateKey("2026-12-31") != "2027-01-01" {
		t.Fatalf("日期进阶不符：%s", nextDateKey("2026-12-31"))
	}
}

// --- 测试选项纯函数臂 ---

func TestW14LTestOptionPureArms(t *testing.T) {
	badJSON := sql.NullString{String: "not-json", Valid: true}
	if got := testParseJSONArray(badJSON); len(got) != 0 {
		t.Fatalf("非法 JSON 应为空：%v", got)
	}
	mixed := sql.NullString{String: `["a", 2, "b"]`, Valid: true}
	if got := testParseJSONArray(mixed); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("非字符串成员应跳过：%v", got)
	}
	if got, err := endpointModeProtocolFamily("images_json"); got != "" || err == nil {
		t.Fatal("images_json 应报错")
	}
	if family, err := endpointModeProtocolFamily("mystery_mode"); err != nil || family != "generate_content" {
		t.Fatalf("未列模式应走 default 臂：%q %v", family, err)
	}
	if normalizedOptionReleaseDate("2026-01-XY") != "" {
		t.Fatal("非法日期字符应为空")
	}
	if normalizedOptionReleaseDate("2026-13-01") != "" {
		t.Fatal("非法月份应为空")
	}
	if normalizedOptionReleaseDate("2026-03-01T10:00:00Z") != "2026-03-01" {
		t.Fatal("时间戳应截断到日期")
	}
	rows := []testOptionRow{
		{provider: "  ", model: "m1"},
		{provider: "p1", model: ""},
		{provider: "p1", model: "gpt-4o", scope: catalogScopeBuiltIn, releaseDate: "2026-01-01"},
		{provider: "p2", model: "gpt-4o", scope: catalogScopePersonal, releaseDate: "2026-02-01T00:00:00Z"},
		{provider: "p3", model: "zzz", scope: catalogScopeGlobal},
	}
	merged := mergeTestOptionRows(rows, ManualTestOptionsQuery{Keyword: "gpt", SelectedIDs: []string{"zzz"}, Limit: 5})
	if len(merged) != 2 || merged[0].model != "gpt-4o" || merged[1].model != "zzz" {
		t.Fatalf("合并结果不符：%+v", merged)
	}
	limited := mergeTestOptionRows(rows, ManualTestOptionsQuery{Limit: 0})
	if len(limited) != 0 {
		t.Fatalf("limit=0 应全部排除：%+v", limited)
	}
}

// --- 上游 URL/IP 纯函数臂 ---

func TestW14LUpstreamIPArms(t *testing.T) {
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("非法 IPv6 网段应 panic")
			}
		}()
		mustIPv6Network("not-an-ip")
	}()
	if !isBlockedIPv6(nil) {
		t.Fatal("nil IP 应视为被阻断")
	}
	if isBlockedIPv6(net.ParseIP("2001:4860:4860::8888")) {
		t.Fatal("公网 IPv6 不应被阻断")
	}
	if !isBlockedIPv6(net.ParseIP("fc00::1")) {
		t.Fatal("ULA IPv6 应被阻断")
	}
	if !isBlockedIPv4(net.ParseIP("10.1.2.3")) || isBlockedIPv4(net.ParseIP("8.8.8.8")) {
		t.Fatal("IPv4 私网判定不符")
	}
	// ipv4MatchesPrefix 边界：前缀 0 立即通过，前缀 32 全匹配，中途失配。
	if !ipv4MatchesPrefix([4]byte{1, 2, 3, 4}, [4]byte{9, 9, 9, 9}, 0) {
		t.Fatal("前缀 0 应匹配")
	}
	if !ipv4MatchesPrefix([4]byte{1, 2, 3, 4}, [4]byte{1, 2, 3, 4}, 32) {
		t.Fatal("前缀 32 全等应匹配")
	}
	if ipv4MatchesPrefix([4]byte{1, 2, 3, 4}, [4]byte{1, 2, 9, 9}, 24) {
		t.Fatal("第三字节失配不应匹配")
	}
}

// --- 批量字段臂（经真实 BatchUpdate 驱动） ---

func TestW14LBatchFieldArms(t *testing.T) {
	f := newW14LFaultFixture(t)
	scope := f.scope()
	// 余额查询开启的账户 + 属主代理行。
	input := w14lCreateInput("w14l-balance")
	input.BalanceQueryEnabled = true
	canonical := `{"adapter":"builtin"}`
	input.BalanceQueryConfigCanonical = &canonical
	created, err := f.store.Create(context.Background(), input, scope)
	if err != nil {
		t.Fatal(err)
	}
	f.created = append(f.created, created.ID)
	f.exec(`INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, enabled, test_status, created_at, updated_at)
		VALUES ('pp-w14l-batch', ?, 'w14l 批量代理', 'socks5', '127.0.0.1', 1080, 1, 'unknown', '2026-03-01T00:00:00.000Z', '2026-03-01T00:00:00.000Z')`, f.owner)

	target := func() []BatchUpdateTarget {
		return []BatchUpdateTarget{{AccountID: created.ID, ConfigRevision: 1}}
	}
	// 非管理员无过滤域 → 批量访问错误。
	if _, err := f.store.BatchUpdate(context.Background(), BatchUpdateInput{
		Targets: target(), Updates: map[string]BatchUpdateField{"notes": w14lField("x")},
	}, AccessScope{ViewerID: f.owner}); err == nil {
		t.Fatal("非管理员无过滤域应拒绝")
	}
	// 版本冲突。
	if _, err := f.store.BatchUpdate(context.Background(), BatchUpdateInput{
		Targets: []BatchUpdateTarget{{AccountID: created.ID, ConfigRevision: 99}},
		Updates: map[string]BatchUpdateField{"notes": w14lField("x")},
	}, scope); err == nil {
		t.Fatal("过期版本应冲突")
	}
	// 未知代理 → 校验错误；空文本 → 校验错误。
	for _, value := range []any{"pp-missing-w14l", "   "} {
		if _, err := f.store.BatchUpdate(context.Background(), BatchUpdateInput{
			Targets: target(), Updates: map[string]BatchUpdateField{"proxyProfileId": w14lField(value)},
		}, scope); err == nil {
			t.Fatalf("代理 %q 应拒绝", value)
		}
	}
	// 合法代理 → 变更（含余额查询的下次刷新列）。
	if _, err := f.store.BatchUpdate(context.Background(), BatchUpdateInput{
		Targets: target(), Updates: map[string]BatchUpdateField{"proxyProfileId": w14lField("pp-w14l-batch")},
	}, scope); err != nil {
		t.Fatal(err)
	}
	// 空支持模型 → 校验错误；nil 标签 → 清空；非法到期 → 校验错误；
	// 全天拒绝计划 → 状态自动停用 + 无下一检查点；notes 清空。
	updates := []map[string]BatchUpdateField{
		{"supportedModels": w14lField([]any{})},
		{"tags": w14lField(nil)},
		{"accountExpiresAt": w14lField("not-a-time")},
		{"accountExpiresAt": w14lField("2027-01-01T00:00:00.000Z")},
		{"accountExpiresAt": w14lField(nil)},
		{"availabilitySchedule": w14lField(map[string]any{
			"enabled": true, "timezone": "UTC", "mode": "deny_windows",
			"windows": []any{map[string]any{"daysOfWeek": []any{1, 2, 3, 4, 5, 6, 7}, "start": "00:00", "end": "23:59"}},
		})},
		{"notes": w14lField("")},
		{"fallbackEnabled": w14lField(false)},
	}
	for index, update := range updates {
		fresh := target()
		fresh[0].ConfigRevision = int64(index + 2)
		if _, err := f.store.BatchUpdate(context.Background(), BatchUpdateInput{Targets: fresh, Updates: update}, scope); err != nil {
			t.Fatalf("更新 #%d 应成功：%v", index, err)
		}
	}
	// 加载上下文（含支持模型 / 映射 / 标签）。
	if _, err := f.store.LoadBatchEditContext(context.Background(), []string{created.ID},
		[]string{"supportedModels", "modelMappings", "tags", "notes"}, scope); err != nil {
		t.Fatal(err)
	}
	// 空集合早退臂。
	if models, err := f.store.loadBatchSupportedModels(context.Background(), f.db, nil); err != nil || len(models) != 0 {
		t.Fatalf("空集合应早退：%v %v", models, err)
	}
	if mappings, err := f.store.loadBatchModelMappings(context.Background(), f.db, nil); err != nil || len(mappings) != 0 {
		t.Fatalf("空集合应早退：%v %v", mappings, err)
	}
	if tags, err := f.store.loadBatchTags(context.Background(), f.db, nil); err != nil || len(tags) != 0 {
		t.Fatalf("空集合应早退：%v %v", tags, err)
	}
}

// --- 失效回调错误臂 ---

type w14lErrorInvalidator struct{}

func (w14lErrorInvalidator) InvalidateAccountLookup(string) error                  { return errors.New("w14l lookup 失效失败") }
func (w14lErrorInvalidator) InvalidateGatewayRuntime(string) error                 { return errors.New("w14l runtime 失效失败") }
func (w14lErrorInvalidator) InvalidateGroupAccountIds() error                      { return errors.New("w14l 分组失效失败") }
func (w14lErrorInvalidator) ClearResourceAuthorizationLookupCaches() error         { return errors.New("w14l 授权清理失败") }
func (w14lErrorInvalidator) InvalidateAuthorizationQuota(string) error             { return errors.New("w14l 额度失效失败") }

func TestW14LInvalidatorErrorArms(t *testing.T) {
	f := newW14LFaultFixture(t)
	f.store.SetCacheInvalidator(w14lErrorInvalidator{})
	scope := f.scope()
	created, err := f.store.Create(context.Background(), w14lCreateInput("w14l-inval"), scope)
	if err != nil {
		t.Fatal(err)
	}
	status := "disabled"
	if _, err := f.store.Patch(context.Background(), created.ID, PatchInput{
		ExpectedConfigRevision: 1, Status: &status,
	}, scope); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.BatchUpdate(context.Background(), BatchUpdateInput{
		Targets: []BatchUpdateTarget{{AccountID: created.ID, ConfigRevision: 2}},
		Updates: map[string]BatchUpdateField{"notes": w14lField("w14l 批量")},
	}, scope); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Delete(context.Background(), created.ID, scope); err != nil {
		t.Fatal(err)
	}
}

// --- retryQueue 状态臂 ---

func TestW14LRetryQueueArms(t *testing.T) {
	exhausted := 0
	queue := newRetryQueue[string]("w14l-queue", []int64{1, 1}, 2,
		func(item string, attemptIndex int) error { return errors.New("w14l 恒败") },
		retryQueueCallbacks[string]{OnExhausted: func(retryQueueEvent[string]) { exhausted++ }},
	)
	options := retryQueueEnqueueOptions{DelayMs: -5}
	if !queue.enqueue("a", "a", options) {
		t.Fatal("入队应成功")
	}
	queue.waitRunning()
	deadline := time.Now().Add(2 * time.Second)
	for exhausted == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if exhausted == 0 {
		t.Fatal("重试耗尽应触发回调")
	}
	// 停止后入队被拒；重复 stop 与 clear 安全。
	queue.stop()
	if queue.enqueue("b", "b", retryQueueEnqueueOptions{}) {
		t.Fatal("停止后入队应拒绝")
	}
	queue.stop()
	queue.clear()
}
