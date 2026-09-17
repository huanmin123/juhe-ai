package accounts

// w13a patch.go 未覆盖臂补齐：Store.Patch 校验臂（名称长度、并发/优先级边界、
// 超级优先/降级互斥、过期时间、时间计划、代理三态、支持模型空集、检查模型/
// 协议、凭据损坏解密、余额身份保持、重复名唯一性、授权实例跟随、候选名阶梯
// 与可用性探测）以及 helper 纯函数直测。HTTP 臂走内核；事务内不可注入的
// 错误臂通过 pg 方言存储（juhe_business 前缀在 sqlite 缺表）与假 queryer 触发。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// w13aFakeQueryer 以可插拔探针替换查询：probe 供 QueryRowContext，rows 供
// QueryContext，execFunc 按调用序供 ExecContext；未注入的通道恒报错。
type w13aFakeQueryer struct {
	probe    func() *sql.Row
	rows     func() (*sql.Rows, error)
	execFunc func(n int) (sql.Result, error)
	execN    int
}

func (q *w13aFakeQueryer) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	if q.rows != nil {
		return q.rows()
	}
	return nil, errors.New("w13a: 查询不支持")
}

func (q *w13aFakeQueryer) QueryRowContext(context.Context, string, ...any) *sql.Row {
	return q.probe()
}

func (q *w13aFakeQueryer) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	q.execN++
	if q.execFunc != nil {
		return q.execFunc(q.execN)
	}
	return nil, errors.New("w13a: 执行不支持")
}

func TestW13AAccountPatchChangeLabel(t *testing.T) {
	// credentials.* 前缀臂与既有 case 臂直测（该函数是操作日志标签契约）。
	if got := accountPatchChangeLabel("credentials.api_key"); got != "凭据" {
		t.Fatalf("credentials.* 前缀应折叠为凭据：%q", got)
	}
	for field, want := range map[string]string{
		"superPriorityEnabled":    "超级优先",
		"fallbackEnabled":         "降级备用",
		"healthCheckEndpointMode": "检查协议",
	} {
		if got := accountPatchChangeLabel(field); got != want {
			t.Fatalf("field %s：got %q want %q", field, got, want)
		}
	}
}

func TestW13APatchValidationArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-w13a-a", adminID, "w13a-alpha", "active")
	path := "/__aisys__/api/accounts/acc-w13a-a"

	// 名称超过 128 rune（362-364）。
	long := strings.Repeat("名", 129)
	if code, payload := env.do(t, http.MethodPatch, path,
		`{"expectedConfigRevision":1,"name":"`+long+`"}`); code != http.StatusBadRequest {
		t.Fatalf("超长名称应 400：%d %v", code, payload)
	}
	// 并发限制 < 1（397-399）。
	if code, payload := env.do(t, http.MethodPatch, path,
		`{"expectedConfigRevision":1,"concurrencyLimit":0}`); code != http.StatusBadRequest {
		t.Fatalf("并发限制 0 应 400：%d %v", code, payload)
	}
	// 优先级 < 0（407-409）。
	if code, payload := env.do(t, http.MethodPatch, path,
		`{"expectedConfigRevision":1,"priority":-1}`); code != http.StatusBadRequest {
		t.Fatalf("优先级 -1 应 400：%d %v", code, payload)
	}
	// 超级优先 + 降级备用互斥（419-421 / 430-432 两个方向）。
	if code, payload := env.do(t, http.MethodPatch, path,
		`{"expectedConfigRevision":1,"superPriorityEnabled":true,"fallbackEnabled":true}`); code != http.StatusBadRequest {
		t.Fatalf("超级优先+降级应 400：%d %v", code, payload)
	}
	// 单开超级优先成功（416-424），随后反向互斥（430-432），单开降级成功（427-435）。
	if code, payload := env.do(t, http.MethodPatch, path,
		`{"expectedConfigRevision":1,"superPriorityEnabled":true}`); code != http.StatusOK {
		t.Fatalf("单开超级优先应 200：%d %v", code, payload)
	}
	if code, payload := env.do(t, http.MethodPatch, path,
		`{"expectedConfigRevision":2,"fallbackEnabled":true,"superPriorityEnabled":true}`); code != http.StatusBadRequest {
		t.Fatalf("降级+超级优先应 400：%d %v", code, payload)
	}
	if code, payload := env.do(t, http.MethodPatch, path,
		`{"expectedConfigRevision":2,"fallbackEnabled":true}`); code != http.StatusOK {
		t.Fatalf("单开降级应 200：%d %v", code, payload)
	}
	if env.count(t, `SELECT super_priority_enabled + fallback_enabled FROM accounts WHERE id = 'acc-w13a-a'`) != 2 {
		t.Fatal("两个开关都应落库")
	}
	// 非法过期时间（450-452）。
	if code, payload := env.do(t, http.MethodPatch, path,
		`{"expectedConfigRevision":3,"accountExpiresAt":"not-a-time"}`); code != http.StatusBadRequest {
		t.Fatalf("非法过期时间应 400：%d %v", code, payload)
	}
	// 非法时间计划（465-467 NormalizeSchedule 错误）。
	if code, payload := env.do(t, http.MethodPatch, path,
		`{"expectedConfigRevision":3,"availabilitySchedule":"x"}`); code != http.StatusBadRequest {
		t.Fatalf("非法时间计划应 400：%d %v", code, payload)
	}
	// 代理三态：空白（488-490）与不存在的代理（500-502）。
	if code, payload := env.do(t, http.MethodPatch, path,
		`{"expectedConfigRevision":3,"proxyProfileId":"   "}`); code != http.StatusBadRequest {
		t.Fatalf("空白代理应 400：%d %v", code, payload)
	}
	if code, payload := env.do(t, http.MethodPatch, path,
		`{"expectedConfigRevision":3,"proxyProfileId":"pp-w13a-missing"}`); code != http.StatusBadRequest {
		t.Fatalf("缺失代理应 400：%d %v", code, payload)
	}
	// 检查模型不在支持模型集合（567-569）。
	if code, payload := env.do(t, http.MethodPatch, path,
		`{"expectedConfigRevision":3,"healthCheckModel":"gpt-x"}`); code != http.StatusBadRequest {
		t.Fatalf("检查模型越界应 400：%d %v", code, payload)
	}
	// 检查协议非法（577-580）与合法变化（581-586）。
	if code, payload := env.do(t, http.MethodPatch, path,
		`{"expectedConfigRevision":3,"healthCheckEndpointMode":"bogus"}`); code != http.StatusBadRequest {
		t.Fatalf("非法检查协议应 400：%d %v", code, payload)
	}
	if code, payload := env.do(t, http.MethodPatch, path,
		`{"expectedConfigRevision":3,"healthCheckEndpointMode":"responses_json"}`); code != http.StatusOK {
		t.Fatalf("合法检查协议应 200：%d %v", code, payload)
	}
	// 支持模型变化成功路径（变化臂 + 最终凭据断言入口）。
	if code, payload := env.do(t, http.MethodPatch, path,
		`{"expectedConfigRevision":4,"supportedModels":["gpt-4o","gpt-4.1"]}`); code != http.StatusOK {
		t.Fatalf("支持模型更新应 200：%d %v", code, payload)
	}
	if env.count(t, `SELECT COUNT(*) FROM account_supported_models WHERE account_id = 'acc-w13a-a'`) != 2 {
		t.Fatal("支持模型应替换")
	}
	// 分组绑定：空白（1086-1088）与未知分组（assertGroupCanBind ErrNoRows）。
	if code, payload := env.do(t, http.MethodPatch, path,
		`{"expectedConfigRevision":5,"groupId":"   "}`); code != http.StatusBadRequest {
		t.Fatalf("空白分组应 400：%d %v", code, payload)
	}
	if code, payload := env.do(t, http.MethodPatch, path,
		`{"expectedConfigRevision":5,"groupId":"grp-w13a-missing"}`); code != http.StatusBadRequest {
		t.Fatalf("缺失分组应 400：%d %v", code, payload)
	}
	// 标签超长（1044-1046）。
	if code, payload := env.do(t, http.MethodPatch, path,
		`{"expectedConfigRevision":5,"tags":["`+strings.Repeat("签", 41)+`"]}`); code != http.StatusBadRequest {
		t.Fatalf("超长标签应 400：%d %v", code, payload)
	}
	// credentialsPatch null 删键后凭据归一化拒绝（644-646）。
	if code, payload := env.do(t, http.MethodPatch, path,
		`{"expectedConfigRevision":5,"credentialsPatch":{"api_key":null}}`); code != http.StatusBadRequest {
		t.Fatalf("删空 api_key 应 400：%d %v", code, payload)
	}
}

func TestW13APatchCorruptedCredentialsArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-w13a-bad", adminID, "w13a-bad", "active")
	env.exec(t, `UPDATE accounts SET credentials_encrypted = 'w13a-not-sealed' WHERE id = 'acc-w13a-bad'`)
	path := "/__aisys__/api/accounts/acc-w13a-bad"

	// 凭据输入触发的解密失败（623-625）。
	if code, payload := env.do(t, http.MethodPatch, path,
		`{"expectedConfigRevision":1,"credentials":{"base_url":"https://x"}}`); code != http.StatusInternalServerError {
		t.Fatalf("损坏凭据应 500：%d %v", code, payload)
	}
	// 支持模型路径的最终凭据解密失败（698-700 → 708-710 resolveFinalCredentials）。
	if code, payload := env.do(t, http.MethodPatch, path,
		`{"expectedConfigRevision":1,"supportedModels":["gpt-4o"]}`); code != http.StatusInternalServerError {
		t.Fatalf("最终凭据解密失败应 500：%d %v", code, payload)
	}
	// 余额开关在无凭据输入时的解密失败（954-956）。
	if code, payload := env.do(t, http.MethodPatch, path,
		`{"expectedConfigRevision":1,"balanceQueryEnabled":false}`); code != http.StatusInternalServerError {
		t.Fatalf("余额路径解密失败应 500：%d %v", code, payload)
	}
}

func TestW13APatchBalanceIdentityPreserved(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-w13a-bal", adminID, "w13a-bal", "active")
	config := `{"adapter":"builtin","preferredBuiltinAdapter":"newapi"}`

	// 首次开启：身份变化，next_refresh 提前到当下（1029）。
	if code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/acc-w13a-bal",
		`{"expectedConfigRevision":1,"balanceQueryEnabled":true,"balanceQueryConfig":`+config+`}`); code != http.StatusOK {
		t.Fatalf("开启余额查询应 200：%d %v", code, payload)
	}
	first := env.queryCell(t, `SELECT balance_query_next_refresh_at FROM accounts WHERE id = 'acc-w13a-bal'`)
	if first == "" {
		t.Fatal("开启后应写入 next_refresh")
	}
	// 同配置同开关：身份未变，保留既有 next_refresh（1030-1032）。
	if code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/acc-w13a-bal",
		`{"expectedConfigRevision":2,"balanceQueryEnabled":true,"balanceQueryConfig":`+config+`}`); code != http.StatusOK {
		t.Fatalf("同配置余额更新应 200：%d %v", code, payload)
	}
	if got := env.queryCell(t, `SELECT balance_query_next_refresh_at FROM accounts WHERE id = 'acc-w13a-bal'`); got != first {
		t.Fatalf("身份未变应保留 next_refresh：%q vs %q", got, first)
	}
	// 配置变化：身份变化，next_refresh 再次提前（同配置更新未递增版本，仍为 2）。
	if code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/acc-w13a-bal",
		`{"expectedConfigRevision":2,"balanceQueryConfig":{"adapter":"builtin","preferredBuiltinAdapter":"litellm"}}`); code != http.StatusOK {
		t.Fatalf("配置变化应 200：%d %v", code, payload)
	}
	if got := env.queryCell(t, `SELECT balance_query_next_refresh_at FROM accounts WHERE id = 'acc-w13a-bal'`); got < first {
		t.Fatalf("身份变化应刷新 next_refresh（ISO 字典序不早于旧值）：%q vs %q", got, first)
	}
}

func TestW13APatchDuplicateNameError(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-w13a-d1", adminID, "w13a-dup-one", "active")
	env.seedAccount(t, "acc-w13a-d2", adminID, "w13a-dup-two", "active")

	// 重命名为同 owner 下已有名称 → 唯一索引冲突 → duplicateAccountNameError（1154-1157，409 冲突）。
	if code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/acc-w13a-d1",
		`{"expectedConfigRevision":1,"name":"w13a-dup-two"}`); code != http.StatusConflict {
		t.Fatalf("重名应 409：%d %v", code, payload)
	}
}

func TestW13APatchRenameAuthorizationInstanceFollow(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-w13a-src", adminID, "w13a-src", "active")
	// 实例 1：授权 ID 为空 → 跳过（1344-1345）。
	// 实例 2：属于其他 owner 且名称已等于目标名 → 继续跳过（1351-1352）。
	// 实例 3：名称不同 → 跟随唯一性阶梯改名（1354-1361）。
	env.seedAccount(t, "acc-w13a-i1", adminID, "w13a-i1-old", "active")
	env.exec(t, `UPDATE accounts SET authorization_instance_source_account_id = 'acc-w13a-src',
		authorization_instance_authorization_id = '' WHERE id = 'acc-w13a-i1'`)
	env.seedAccount(t, "acc-w13a-i2", "w13a-other-owner", "w13a-newname", "active")
	env.exec(t, `UPDATE accounts SET authorization_instance_source_account_id = 'acc-w13a-src',
		authorization_instance_authorization_id = 'authz-w13a-2' WHERE id = 'acc-w13a-i2'`)
	env.seedAccount(t, "acc-w13a-i3", adminID, "w13a-i3-old", "active")
	env.exec(t, `UPDATE accounts SET authorization_instance_source_account_id = 'acc-w13a-src',
		authorization_instance_authorization_id = 'authz-w13a-3' WHERE id = 'acc-w13a-i3'`)

	if code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/acc-w13a-src",
		`{"expectedConfigRevision":1,"name":"w13a-newname"}`); code != http.StatusOK {
		t.Fatalf("重命名应 200：%d %v", code, payload)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = 'acc-w13a-i1' AND name = 'w13a-i1-old'`) != 1 {
		t.Fatal("空授权 ID 实例不应改名")
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = 'acc-w13a-i2' AND name = 'w13a-newname'`) != 1 {
		t.Fatal("同名实例不应重复改名")
	}
	// 实例 3：候选一被源账户新名占用 → 阶梯落到候选二 `w13a-newname-` + shortID。
	if got := env.queryCell(t, `SELECT name FROM accounts WHERE id = 'acc-w13a-i3'`); got != "w13a-newname-authz-" {
		t.Fatalf("实例 3 应跟随阶梯命名：%q", got)
	}
	if env.count(t, `SELECT COUNT(*) FROM account_name_search_terms WHERE account_id = 'acc-w13a-i3'`) == 0 {
		t.Fatal("实例改名应刷新搜索词")
	}
}

func TestW13AUniqueAuthorizedInstanceNameLadder(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedAccount(t, "acc-w13a-n1", adminID, "w13a-base", "active")
	env.seedAccount(t, "acc-w13a-n2", adminID, "w13a-base-auth", "active")

	// 候选名两级都被占用 → 阶梯 -2（1399-1407）。
	got, err := env.store.uniqueAuthorizedInstanceName(context.Background(), env.db, "w13a-base",
		adminID, "authz_auth", "acc-w13a-other")
	if err != nil {
		t.Fatalf("阶梯命名失败：%v", err)
	}
	if got != "w13a-base-auth-2" {
		t.Fatalf("应落到阶梯第二级：%q", got)
	}
	// 空白来源名回退 授权账户（1373-1375）；shortID 为空回退授权 ID（1383-1387）。
	got, err = env.store.uniqueAuthorizedInstanceName(context.Background(), env.db, "  ",
		adminID, "", "acc-w13a-other")
	if err != nil || got != "授权账户" {
		t.Fatalf("空来源名应回退授权账户：%q %v", got, err)
	}
}

func TestW13AUniqueAuthorizedInstanceNameTimestampFallback(t *testing.T) {
	env := newTestEnv(t)
	// 行探测恒命中（SELECT 1），999 个阶梯候选全部不可用 → 时间戳兜底（1409）。
	fake := &w13aFakeQueryer{probe: func() *sql.Row { return env.db.QueryRow("SELECT 1") }}
	got, err := env.store.uniqueAuthorizedInstanceName(context.Background(), fake,
		"w13a-taken", "owner", "authz_w13a", "acc-self")
	if err != nil {
		t.Fatalf("时间戳兜底失败：%v", err)
	}
	if !strings.HasPrefix(got, "w13a-taken-w13a-") {
		t.Fatalf("应返回时间戳兜底名：%q", got)
	}
}

func TestW13AAuthorizedInstanceNameAvailableArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedAccount(t, "acc-w13a-av", adminID, "w13a-av", "active")

	// 名称被占用（1426 return false）。
	available, err := env.store.authorizedInstanceNameAvailable(context.Background(), env.db,
		adminID, "w13a-av", "acc-other")
	if err != nil || available {
		t.Fatalf("占用名应不可用：%v %v", available, err)
	}
	// 查询错误（1423-1425）。
	fake := &w13aFakeQueryer{probe: func() *sql.Row { return env.db.QueryRow("SELECT nope FROM missing_table") }}
	if _, err := env.store.authorizedInstanceNameAvailable(context.Background(), fake,
		adminID, "w13a-av", "acc-other"); err == nil {
		t.Fatal("探测查询错误应上抛")
	}
}

func TestW13APatchStoreErrorArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-w13a-err", adminID, "w13a-err", "active")

	// PG 方言在 sqlite 上缺 juhe_business 前缀 → 行扫描错误（266-268）。
	pgStore, err := NewStore(env.db, true, testSecret, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pgStore.Patch(context.Background(), "acc-w13a-err", PatchInput{ExpectedConfigRevision: 1}, AccessScope{ViewerID: adminID, IsAdmin: true}); err == nil {
		t.Fatal("PG 方言查询应失败")
	}

	// 非管理员访问他人账户：行已被 owner 过滤，返回 nil, nil（ErrNoRows 臂）。
	result, err := env.store.Patch(context.Background(), "acc-w13a-err", PatchInput{ExpectedConfigRevision: 1},
		AccessScope{ViewerID: "w13a-stranger"})
	if err != nil || result != nil {
		t.Fatalf("越权访问应返回 nil：%v %v", result, err)
	}

	// 支持模型仅空白 → 归一化后空集 → assertSupportedModelsRequired（540-542）。
	if _, err := env.store.Patch(context.Background(), "acc-w13a-err", PatchInput{
		ExpectedConfigRevision: 1, SupportedModels: []string{"  "}, SupportedModelsPresent: true,
	}, AccessScope{ViewerID: adminID, IsAdmin: true}); err == nil {
		t.Fatal("空支持模型应拒绝")
	}

	// BeginTx 失败（164-166）：关闭数据库后的存储（放在用例末尾）。
	if err := env.db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.Patch(context.Background(), "acc-w13a-err", PatchInput{ExpectedConfigRevision: 1}, AccessScope{ViewerID: adminID, IsAdmin: true}); err == nil {
		t.Fatal("关闭库后 Patch 应报错")
	}
}

func TestW13APatchHelperArms(t *testing.T) {
	env := newTestEnv(t)
	now := time.Date(2026, 9, 17, 8, 0, 0, 0, time.UTC)
	nowISO := now.Format(time.RFC3339Nano)

	// scheduleNextCheckArg 无计划 → !ok（1433）。
	if arg := scheduleNextCheckArg(nil, now); arg.Valid {
		t.Fatalf("nil 计划应返回 NULL：%v", arg)
	}
	// parseStoredBalanceConfig：null 解析为空 map → nil（1524-1526）。
	if parsed, err := parseStoredBalanceConfig("null"); err != nil || parsed != nil {
		t.Fatalf("null 应返回 nil：%v %v", parsed, err)
	}
	// credentialValueJSONText 不可序列化值（1565-1567）。
	if got := credentialValueJSONText(make(chan int)); got != "unmarshalable" {
		t.Fatalf("不可序列化值应返回占位文本：%q", got)
	}
	// loadEnabledGroupBindingID：无绑定 → ""（1626-1628）；PG 方言查询错误（1629-1631）。
	got, err := env.store.loadEnabledGroupBindingID(context.Background(), env.db, "acc-w13a-none", "owner")
	if err != nil || got != "" {
		t.Fatalf("无绑定应返回空：%q %v", got, err)
	}
	pgStore, err := NewStore(env.db, true, testSecret, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pgStore.loadEnabledGroupBindingID(context.Background(), env.db, "acc", "owner"); err == nil {
		t.Fatal("PG 方言应失败")
	}
	// assertGroupCanBind：PG 锁后缀臂（1641-1643）与查询错误（1650-1652）。
	if err := pgStore.assertGroupCanBind(context.Background(), env.db, "grp", "owner", "gpt"); err == nil {
		t.Fatal("PG 方言分组断言应失败")
	}
	// replaceGroupBinding：PG 方言 DELETE 失败（1665-1667）。
	if err := pgStore.replaceGroupBinding(context.Background(), env.db, "acc", "owner",
		sql.NullString{}, "grp", 0, false, false, nowISO); err == nil {
		t.Fatal("PG 方言替换绑定应失败")
	}
	// propagateProbeSwitchToAuthorizationInstances：PG 后缀（1241-1243）+ 查询错误（1248-1250）。
	// syncAuthorizationInstanceNames：PG 方言查询错误（1320-1322）。两者收
	// *sql.Tx，走真实事务让方言差异在查询期报错。
	tx, err := env.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := pgStore.propagateProbeSwitchToAuthorizationInstances(context.Background(), tx,
		"acc", 0, false, "", now, nowISO); err == nil {
		t.Fatal("PG 方言传播应失败")
	}
	if _, err := pgStore.syncAuthorizationInstanceNames(context.Background(), tx,
		"acc", "name", nowISO); err == nil {
		t.Fatal("PG 方言实例同步应失败")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	// loadAccountModelMappings：查询错误（1468-1470）与扫描错误（1477-1479）。
	if _, err := env.store.loadAccountModelMappings(context.Background(), &w13aFakeQueryer{
		probe: func() *sql.Row { return env.db.QueryRow("SELECT 1") },
	}, "acc"); err == nil {
		t.Fatal("查询错误应上抛")
	}
	if _, err := env.store.loadAccountModelMappings(context.Background(), &w13aFakeQueryer{
		rows: func() (*sql.Rows, error) { return env.db.Query("SELECT 1 AS one") },
	}, "acc"); err == nil {
		t.Fatal("扫描错误应上抛")
	}
	// replaceGroupBinding：INSERT 失败臂（1682-1684）——DELETE 成功后 INSERT 报错。
	if err := env.store.replaceGroupBinding(context.Background(), &w13aFakeQueryer{
		execFunc: func(n int) (sql.Result, error) {
			if n == 1 {
				return driver.RowsAffected(1), nil
			}
			return nil, errors.New("w13a: 插入失败")
		},
	}, "acc", "owner", sql.NullString{}, "grp", 0, false, false, nowISO); err == nil {
		t.Fatal("INSERT 失败应上抛")
	}
	// uniqueAuthorizedInstanceName：候选探测中途失败（1392-1394）。
	midFail := &w13aFakeQueryer{probe: func() *sql.Row {
		return env.db.QueryRow("SELECT nope FROM missing_table")
	}}
	if _, err := env.store.uniqueAuthorizedInstanceName(context.Background(), midFail,
		"w13a-x", "owner", "authz", "acc"); err == nil {
		t.Fatal("候选探测失败应上抛")
	}
	probes := 0
	ladderFail := &w13aFakeQueryer{probe: func() *sql.Row {
		probes++
		if probes <= 2 {
			return env.db.QueryRow("SELECT 1")
		}
		return env.db.QueryRow("SELECT nope FROM missing_table")
	}}
	if _, err := env.store.uniqueAuthorizedInstanceName(context.Background(), ladderFail,
		"w13a-y", "owner", "authz", "acc"); err == nil {
		t.Fatal("阶梯探测失败应上抛")
	}
	// 授权 ID 以 _ 结尾且超长 → shortID 截断臂（1385-1387）；候选一可用则直接返回。
	got, err = env.store.uniqueAuthorizedInstanceName(context.Background(), env.db,
		"w13a-cut", "owner-unused", "abcdefg_", "acc")
	if err != nil || got != "w13a-cut" {
		t.Fatalf("候选一可用应直接返回：%q %v", got, err)
	}
}
