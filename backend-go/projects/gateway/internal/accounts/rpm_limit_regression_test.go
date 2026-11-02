package accounts

// AI 账户每分钟请求上限（AI账户每分钟请求上限设计 §3/§6.1/§6.3/§6.4）最小回归：
// 字段契约（创建缺省 0、显式 0 = 不限制、负数/浮点 400）、PATCH 三态与授权实例
// 只读守卫、列表/详情/克隆投影、批量编辑白名单与校验、导入导出 round-trip、
// 列表运行态 hydrate（授权实例行按来源账户键读）。

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// rpmAuthorizedReader 是 authorizedReadableIDs 的最小桩：恒返回固定实例 id 集。
type rpmAuthorizedReader struct {
	ids map[string]bool
}

func (r rpmAuthorizedReader) AuthorizedReadableAccountIDs(context.Context, string) (map[string]bool, error) {
	return r.ids, nil
}

func rpmCreatePayload(name, rpmFragment string) string {
	return strings.Replace(createPayload(name), `"tags":`, rpmFragment+`"tags":`, 1)
}

// TestRPMLimitCreateDefaultsAndValidation 锁定创建面契约：缺省落 0、正整数落列、
// 负数 400（写面）、浮点 400（body 解析器，禁止浮点截断静默通过）。
func TestRPMLimitCreateDefaultsAndValidation(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	// 缺省：rpm_limit = 0（不限制，设计 §3 不套用 concurrency 默认值）。
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("rpm-默认"))
	if code != http.StatusCreated {
		t.Fatalf("缺省创建应成功：%d %v", code, payload)
	}
	defaultID := dataMap(t, payload)["id"].(string)
	if got := env.queryCell(t, `SELECT rpm_limit FROM accounts WHERE id = ?`, defaultID); got != "0" {
		t.Fatalf("缺省 rpm_limit 应为 0，got %s", got)
	}

	// 显式正整数落列。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		rpmCreatePayload("rpm-正整数", `"rpmLimit":60,`))
	if code != http.StatusCreated {
		t.Fatalf("正整数创建应成功：%d %v", code, payload)
	}
	positiveID := dataMap(t, payload)["id"].(string)
	if got := env.queryCell(t, `SELECT rpm_limit FROM accounts WHERE id = ?`, positiveID); got != "60" {
		t.Fatalf("rpm_limit 应为 60，got %s", got)
	}

	// 显式 0 = 不限制（合法，缺省同语义）。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		rpmCreatePayload("rpm-显式零", `"rpmLimit":0,`))
	if code != http.StatusCreated {
		t.Fatalf("显式 0 创建应成功：%d %v", code, payload)
	}
	zeroID := dataMap(t, payload)["id"].(string)
	if got := env.queryCell(t, `SELECT rpm_limit FROM accounts WHERE id = ?`, zeroID); got != "0" {
		t.Fatalf("显式 0 应落 0，got %s", got)
	}

	// 负数：写面 400。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		rpmCreatePayload("rpm-负数", `"rpmLimit":-5,`))
	if code != http.StatusBadRequest || !strings.Contains(payload["message"].(string), "每分钟请求上限") {
		t.Fatalf("负数应 400：%d %v", code, payload)
	}
	// 浮点：body 解析器 400，不静默截断。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		rpmCreatePayload("rpm-浮点", `"rpmLimit":60.5,`))
	if code != http.StatusBadRequest {
		t.Fatalf("浮点应 400：%d %v", code, payload)
	}
}

// TestRPMLimitPatchArms 锁定 PATCH 三态：正整数更新、显式 0 解除、缺失键不改、
// 负数 400、浮点 400。
func TestRPMLimitPatchArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		rpmCreatePayload("rpm-patch", `"rpmLimit":30,`))
	if code != http.StatusCreated {
		t.Fatalf("创建应成功：%d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)

	patch := func(body string) (int, map[string]any) {
		return env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id, body)
	}

	// 正整数更新 + 变更记录。
	code, payload = patch(`{"expectedConfigRevision":1,"rpmLimit":90}`)
	if code != http.StatusOK {
		t.Fatalf("更新应成功：%d %v", code, payload)
	}
	if changed := dataMap(t, payload)["changedFields"].([]any); len(changed) == 0 || changed[0] != "rpmLimit" {
		t.Fatalf("变更字段应含 rpmLimit：%v", changed)
	}
	if got := env.queryCell(t, `SELECT rpm_limit FROM accounts WHERE id = ?`, id); got != "90" {
		t.Fatalf("rpm_limit 应为 90，got %s", got)
	}

	// 同值幂等：无变更（提前返回，修订号不推进，仍是 2）。
	code, payload = patch(`{"expectedConfigRevision":2,"rpmLimit":90}`)
	if code != http.StatusOK {
		t.Fatalf("幂等更新应成功：%d %v", code, payload)
	}
	if changed := dataMap(t, payload)["changedFields"].([]any); len(changed) != 0 {
		t.Fatalf("同值不应产生变更：%v", changed)
	}

	// 显式 0 = 解除限制。
	code, payload = patch(`{"expectedConfigRevision":2,"rpmLimit":0}`)
	if code != http.StatusOK {
		t.Fatalf("显式 0 应成功：%d %v", code, payload)
	}
	if got := env.queryCell(t, `SELECT rpm_limit FROM accounts WHERE id = ?`, id); got != "0" {
		t.Fatalf("显式 0 应落 0，got %s", got)
	}

	// 缺失键不改：只改备注，rpm_limit 保持 0。
	code, payload = patch(`{"expectedConfigRevision":3,"notes":"仅备注"}`)
	if code != http.StatusOK {
		t.Fatalf("备注更新应成功：%d %v", code, payload)
	}
	if got := env.queryCell(t, `SELECT rpm_limit FROM accounts WHERE id = ?`, id); got != "0" {
		t.Fatalf("缺失键不应改动 rpm_limit，got %s", got)
	}

	// 负数 400（store 校验）与浮点 400（body 解析）。
	code, payload = patch(`{"expectedConfigRevision":4,"rpmLimit":-1}`)
	if code != http.StatusBadRequest || !strings.Contains(payload["message"].(string), "每分钟请求上限") {
		t.Fatalf("负数应 400：%d %v", code, payload)
	}
	code, payload = patch(`{"expectedConfigRevision":4,"rpmLimit":1.5}`)
	if code != http.StatusBadRequest {
		t.Fatalf("浮点应 400：%d %v", code, payload)
	}
}

// TestRPMLimitAuthorizedInstanceRejectsPatch 锁定授权实例只读守卫（设计 §3）：
// 实例行 PATCH 提交 rpmLimit 走与并发同款的 400 code。
func TestRPMLimitAuthorizedInstanceRejectsPatch(t *testing.T) {
	env := newTestEnv(t)
	env.seedAccount(t, "acc-rpm-inst", "user-rpm", "RPM授权实例", "active")
	env.exec(t, `UPDATE accounts SET authorization_instance_authorization_id = 'authz-rpm' WHERE id = 'acc-rpm-inst'`)

	_, err := env.store.Patch(context.Background(), "acc-rpm-inst", PatchInput{
		ExpectedConfigRevision: 1,
		RPMLimit:               bug0258Int(120),
	}, AccessScope{ViewerID: "user-rpm"})
	var valErr *ValidationError
	if !errors.As(err, &valErr) || valErr.Code != "authorized_instance_dispatch_field_rejected" {
		t.Fatalf("授权实例提交 rpmLimit 应 400 authorized_instance_dispatch_field_rejected，got %v", err)
	}
}

// TestRPMLimitListHydrateAndSourceOverride 锁定列表投影与运行态叠加（设计 §6.1）：
// 授权实例行配置值取来源账户、RPM 运行态按来源账户键读；owner 行按行 ID 读；
// Used=0/缺读 ResetAt 省略；RPM 读失败降级 0 不阻断。
func TestRPMLimitListHydrateAndSourceOverride(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-rpm-owner", adminID, "RPM来源", "active")
	env.exec(t, `UPDATE accounts SET rpm_limit = 60 WHERE id = 'acc-rpm-owner'`)
	// 实例行挂在 grantee 名下，来源指向 owner 账户（实例行自身 rpm_limit=0）。
	env.seedAccount(t, "acc-rpm-inst", "user-rpm-g", "RPM实例", "active")
	env.exec(t, `UPDATE accounts SET authorization_instance_source_account_id = 'acc-rpm-owner' WHERE id = 'acc-rpm-inst'`)

	resetAtMs := time.Date(2026, 11, 3, 12, 1, 0, 0, time.UTC).UnixMilli()
	env.store.SetConcurrencyReader(&fakeAccountConcurrencyReader{
		currents: map[string]int{"acc-rpm-owner": 2},
		rpmStates: map[string]AccountRPMState{
			"acc-rpm-owner": {Used: 12, ResetAtMs: resetAtMs},
			"acc-rpm-zero":  {Used: 0, ResetAtMs: resetAtMs},
		},
	})
	env.store.SetAuthorizedReader(rpmAuthorizedReader{ids: map[string]bool{"acc-rpm-inst": true}})

	// grantee 视图：实例行 authorized 分支生效。
	result, err := env.store.ListPage(context.Background(), AccessScope{ViewerID: "user-rpm-g"}, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("grantee 应只见实例行：%d", len(result.Items))
	}
	instance := result.Items[0]
	if instance.RPMLimit != 60 {
		t.Fatalf("实例行 rpmLimit 应取来源值 60，got %d", instance.RPMLimit)
	}
	if instance.RPMCurrent != 12 {
		t.Fatalf("实例行 rpmCurrent 应按来源键读 12，got %d", instance.RPMCurrent)
	}
	wantReset := time.UnixMilli(resetAtMs).UTC().Format(time.RFC3339)
	if instance.RPMResetAt == nil || *instance.RPMResetAt != wantReset {
		t.Fatalf("rpmResetAt 应为 %s，got %v", wantReset, instance.RPMResetAt)
	}

	// owner 视图：来源行按自身行 ID 读。
	ownerResult, err := env.store.ListPage(context.Background(), AccessScope{ViewerID: adminID}, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var owner *ListItem
	for index := range ownerResult.Items {
		if ownerResult.Items[index].ID == "acc-rpm-owner" {
			owner = &ownerResult.Items[index]
		}
	}
	if owner == nil {
		t.Fatal("owner 视图应见来源行")
	}
	if owner.RPMLimit != 60 || owner.RPMCurrent != 12 || owner.RPMResetAt == nil {
		t.Fatalf("owner 行投影不一致：%d %d %v", owner.RPMLimit, owner.RPMCurrent, owner.RPMResetAt)
	}

	// 配置了 RPM 的账户即使本分钟尚未派发（Used=0），也应返回窗口结束时刻：
	// 前端 tooltip 据此给出真实倒计时，不出现「约 0 秒后重置」。
	env.seedAccount(t, "acc-rpm-zero", adminID, "RPM零态", "active")
	zeroResult, err := env.store.ListPage(context.Background(), AccessScope{ViewerID: adminID, IsAdmin: true}, ListOptions{IDs: []string{"acc-rpm-zero"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(zeroResult.Items) != 1 || zeroResult.Items[0].RPMCurrent != 0 ||
		zeroResult.Items[0].RPMResetAt == nil || *zeroResult.Items[0].RPMResetAt != wantReset {
		t.Fatalf("Used=0 也应带窗口重置时刻：%+v", zeroResult.Items)
	}
}

// TestRPMLimitHydrateErrorDegradesToZero 锁定 RPM 读失败降级：页面不失败，
// Used 落 0、ResetAt 缺席，并发 overlay 不受影响。
func TestRPMLimitHydrateErrorDegradesToZero(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-rpm-err", adminID, "RPM降级", "active")
	env.store.SetConcurrencyReader(&fakeAccountConcurrencyReader{
		currents: map[string]int{"acc-rpm-err": 5},
		rpmErr:   errors.New("rpm tracker down"),
	})

	result, err := env.store.ListPage(context.Background(), AccessScope{ViewerID: adminID, IsAdmin: true}, ListOptions{})
	if err != nil {
		t.Fatalf("RPM 读失败不应阻断列表：%v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("应见 1 行：%d", len(result.Items))
	}
	item := result.Items[0]
	if item.CurrentConcurrency != 5 || item.RPMCurrent != 0 || item.RPMResetAt != nil {
		t.Fatalf("降级投影不一致：cc=%d rpm=%d reset=%v", item.CurrentConcurrency, item.RPMCurrent, item.RPMResetAt)
	}
}

// TestRPMLimitDetailAndCloneProjection 锁定编辑详情与克隆上下文的配置值回显。
func TestRPMLimitDetailAndCloneProjection(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-rpm-detail", adminID, "RPM详情", "active")
	env.exec(t, `UPDATE accounts SET rpm_limit = 44 WHERE id = 'acc-rpm-detail'`)
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}

	detail, err := env.store.FindEditBasicDetail(context.Background(), "acc-rpm-detail", scope)
	if err != nil || detail == nil {
		t.Fatalf("详情读取失败：%v %v", detail, err)
	}
	if detail.RPMLimit != 44 {
		t.Fatalf("详情 rpmLimit 应为 44，got %d", detail.RPMLimit)
	}

	cloneContext, err := env.store.FindCloneContext(context.Background(), "acc-rpm-detail", scope)
	if err != nil || cloneContext == nil {
		t.Fatalf("克隆上下文读取失败：%v %v", cloneContext, err)
	}
	if cloneContext.RPMLimit != 44 {
		t.Fatalf("克隆上下文 rpmLimit 应为 44，got %d", cloneContext.RPMLimit)
	}
}

// TestRPMLimitBatchArms 锁定批量编辑：白名单接受 rpmLimit、校验 >= 0、
// 变更落列；非法值按 BatchFieldsPrompt 400。
func TestRPMLimitBatchArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	first, second, rev1, rev2 := w2BatchAccounts(t, env, adminID)

	if !batchUpdateFields["rpmLimit"] {
		t.Fatal("批量白名单应包含 rpmLimit")
	}
	if message := validateBatchUpdateValue("rpmLimit", float64(0)); message != "" {
		t.Fatalf("rpmLimit 0 应合法：%q", message)
	}
	for _, invalid := range []any{float64(-1), float64(1.5), "60"} {
		if message := validateBatchUpdateValue("rpmLimit", invalid); message == "" {
			t.Fatalf("rpmLimit %v 应拒绝", invalid)
		}
	}

	result, err := w2BatchUpdate(t, env, adminID, BatchUpdateInput{
		Targets: []BatchUpdateTarget{{AccountID: first, ConfigRevision: rev1}, {AccountID: second, ConfigRevision: rev2}},
		Updates: w2EnabledFields(map[string]any{"rpmLimit": float64(88)}),
	})
	if err != nil {
		t.Fatalf("批量更新错误：%v", err)
	}
	for _, item := range result.Items {
		found := false
		for _, field := range item.ChangedFields {
			if field == "rpmLimit" {
				found = true
			}
		}
		if !found {
			t.Fatalf("批量变更字段应含 rpmLimit：%v", item.ChangedFields)
		}
	}
	if got := env.queryCell(t, `SELECT rpm_limit FROM accounts WHERE id = ?`, first); got != "88" {
		t.Fatalf("批量 rpm_limit 应为 88，got %s", got)
	}
}

// TestRPMLimitImportExportRoundTrip 锁定导入导出 round-trip（设计 §6.4）：
// 导入可选非负整数（0 合法、缺失落 0、负数报 per-account message 且整体
// 不执行）；导出仅 >0 输出。
func TestRPMLimitImportExportRoundTrip(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}

	// 合法导入（30 / 0 / 缺省）执行落库。
	result, err := env.store.ExecuteImport(context.Background(), w2NativeDoc(
		[]map[string]any{
			withAPIKeyAccountOverrides(w2APIKeyAccount("rpm-imp-30", "rpm-g"), map[string]any{"rpmLimit": float64(30)}),
			withAPIKeyAccountOverrides(w2APIKeyAccount("rpm-imp-zero", "rpm-g"), map[string]any{"rpmLimit": float64(0)}),
			w2APIKeyAccount("rpm-imp-absent", "rpm-g"),
		},
		nil,
	), "", ImportOptions{}, scope)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Imported {
		t.Fatalf("合法导入应已执行（canImport=%v）：%+v", result.CanImport, result.Summary)
	}
	createdByRef := map[string]string{}
	for index := range result.Accounts {
		item := result.Accounts[index]
		if item.AccountID == nil {
			t.Fatalf("账户应创建成功（messages=%v）：%+v", item.Messages, item)
		}
		createdByRef[*item.Name] = *item.AccountID
	}
	if got := env.queryCell(t, `SELECT rpm_limit FROM accounts WHERE id = ?`, createdByRef["rpm-imp-30"]); got != "30" {
		t.Fatalf("rpmLimit 30 应落库，got %s", got)
	}
	if got := env.queryCell(t, `SELECT rpm_limit FROM accounts WHERE id = ?`, createdByRef["rpm-imp-zero"]); got != "0" {
		t.Fatalf("rpmLimit 0 应落 0，got %s", got)
	}
	if got := env.queryCell(t, `SELECT rpm_limit FROM accounts WHERE id = ?`, createdByRef["rpm-imp-absent"]); got != "0" {
		t.Fatalf("缺失键应落 0，got %s", got)
	}

	// 负数：per-account message 置 failed，整包 CanImport=false 不执行。
	negative, err := env.store.ExecuteImport(context.Background(), w2NativeDoc(
		[]map[string]any{
			withAPIKeyAccountOverrides(w2APIKeyAccount("rpm-imp-neg", "rpm-g-neg"), map[string]any{"rpmLimit": float64(-3)}),
		},
		nil,
	), "", ImportOptions{}, scope)
	if err != nil {
		t.Fatal(err)
	}
	if negative.Imported || negative.CanImport {
		t.Fatalf("负数 rpmLimit 应使整包停在预览不执行：imported=%v canImport=%v", negative.Imported, negative.CanImport)
	}
	if len(negative.Accounts) != 1 || negative.Accounts[0].Action != importActionFailed {
		t.Fatalf("负数条目应 failed：%+v", negative.Accounts)
	}
	if joined := strings.Join(negative.Accounts[0].Messages, ";"); !strings.Contains(joined, "rpmLimit") {
		t.Fatalf("失败消息应提及 rpmLimit：%v", negative.Accounts[0].Messages)
	}

	// 导出：仅 >0 输出。
	env.seedAccount(t, "acc-rpm-exp-50", adminID, "rpm-exp-50", "active")
	env.exec(t, `UPDATE accounts SET rpm_limit = 50 WHERE id = 'acc-rpm-exp-50'`)
	env.seedAccount(t, "acc-rpm-exp-0", adminID, "rpm-exp-0", "active")
	export, err := env.store.ExportAccounts(context.Background(), ExportOptions{AccountIDs: []string{"acc-rpm-exp-50", "acc-rpm-exp-0"}}, scope)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*ExportAccount{}
	for index := range export.Document.Accounts {
		byName[export.Document.Accounts[index].Name] = &export.Document.Accounts[index]
	}
	if byName["rpm-exp-50"] == nil || byName["rpm-exp-50"].RPMLimit == nil || *byName["rpm-exp-50"].RPMLimit != 50 {
		t.Fatalf("rpmLimit 50 应导出：%+v", byName["rpm-exp-50"])
	}
	if byName["rpm-exp-0"] == nil || byName["rpm-exp-0"].RPMLimit != nil {
		t.Fatalf("rpmLimit 0 应省略：%+v", byName["rpm-exp-0"])
	}
}
