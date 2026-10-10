package accounts

// AI账户特供快速恢复通道（设计 v3.1 §8/§9/§11）gateway accounts 侧验收：
// PATCH/创建通道名额校验（默认 3、0 禁止、limit=5 第 6 个拒绝）、超限整体
// 回滚、幂等与取消不校验、授权实例计入被授权方、并发不超卖（SQLite 单写者
// 等价）、上限下调后列表/详情"已超限"展示位、克隆/导出/导入/批量编辑不携带
// 标记，以及 temporaryUnavailableContinuousProbeEnabled 既有补丁字段回归。

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// expeditedSetOwnerLimit 直写 system_accounts.expedited_account_limit；nil
// 恢复 NULL（读侧 COALESCE 归一默认 3）。
func expeditedSetOwnerLimit(t *testing.T, env *testEnv, ownerID string, limit *int64) {
	t.Helper()
	if limit == nil {
		env.exec(t, `UPDATE system_accounts SET expedited_account_limit = NULL WHERE id = ?`, ownerID)
		return
	}
	env.exec(t, `UPDATE system_accounts SET expedited_account_limit = ? WHERE id = ?`, *limit, ownerID)
}

// expeditedAccountState 读取 (特供标记, config_revision, updated_at) 供回滚
// 断言比对。
func expeditedAccountState(t *testing.T, env *testEnv, id string) (string, string, string) {
	t.Helper()
	var flag, revision, updatedAt string
	if err := env.db.QueryRow(`SELECT CAST(expedited_recovery_enabled AS TEXT),
		CAST(config_revision AS TEXT), updated_at FROM accounts WHERE id = ?`, id).
		Scan(&flag, &revision, &updatedAt); err != nil {
		t.Fatal(err)
	}
	return flag, revision, updatedAt
}

// parseRevisionForTest 把查询到的修订号文本转成 PatchInput 需要的 int64。
func parseRevisionForTest(t *testing.T, raw string) int64 {
	t.Helper()
	var value int64
	if _, err := fmt.Sscanf(raw, "%d", &value); err != nil {
		t.Fatalf("修订号解析失败：%q %v", raw, err)
	}
	return value
}

// assertExpeditedQuotaError 断言错误为名额已满 ValidationError 且文案带上限。
func assertExpeditedQuotaError(t *testing.T, err error, wantLimit string) {
	t.Helper()
	if err == nil {
		t.Fatal("超限操作必须失败")
	}
	var validation *ValidationError
	if !errors.As(err, &validation) || validation.Message != "特供账户数量已达上限（"+wantLimit+"）" {
		t.Fatalf("超限错误 = %v, want 特供账户数量已达上限（%s）", err, wantLimit)
	}
}

// TestExpeditedRecoveryPatchQuotaDefaultThreeAndRollback 走 HTTP PATCH 面：
// NULL 上限归一默认 3；第 4 个置特供整体回滚（行值/修订号/更新时间不变）；
// 取消（true→false）不校验；与既有探活开关混交的补丁同样被名额门整体拒绝；
// 探活开关单独补丁行为不变（回归）。
func TestExpeditedRecoveryPatchQuotaDefaultThreeAndRollback(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	ids := []string{}
	for _, name := range []string{"特供甲", "特供乙", "特供丙", "特供丁"} {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload(name))
		if code != http.StatusCreated {
			t.Fatalf("create %s: %d %v", name, code, payload)
		}
		ids = append(ids, dataMap(t, payload)["id"].(string))
	}

	// 默认上限 3：前 3 个置特供成功，changedFields 与 config_revision +1。
	for index, id := range ids[:3] {
		code, payload := w2PatchBody(t, env, id, `"expeditedRecoveryEnabled":true`)
		if code != http.StatusOK {
			t.Fatalf("enable #%d: %d %v", index, code, payload)
		}
		data := dataMap(t, payload)
		if !containsChange(data["changedFields"], "expeditedRecoveryEnabled") {
			t.Fatalf("changedFields 缺少 expeditedRecoveryEnabled：%v", data["changedFields"])
		}
		if data["configRevision"] != float64(2) {
			t.Fatalf("configRevision = %v, want 2", data["configRevision"])
		}
		if flag, _, _ := expeditedAccountState(t, env, id); flag != "1" {
			t.Fatalf("特供标记未落库：%s", flag)
		}
	}

	// 第 4 个被拒：整体回滚（列值/config_revision/updated_at 均不变）。
	flag0, revision0, updated0 := expeditedAccountState(t, env, ids[3])
	code, payload := w2PatchBody(t, env, ids[3], `"expeditedRecoveryEnabled":true`)
	if code != http.StatusBadRequest || payload["message"] != "特供账户数量已达上限（3）" {
		t.Fatalf("第 4 个置特供：%d %v", code, payload)
	}
	if flag, revision, updated := expeditedAccountState(t, env, ids[3]); flag != flag0 || revision != revision0 || updated != updated0 {
		t.Fatalf("被拒补丁必须整体回滚：(%s,%s,%s) != (%s,%s,%s)", flag, revision, updated, flag0, revision0, updated0)
	}

	// 混交补丁（探活开关 + 特供）：名额门同样整体拒绝，探活开关不被写入。
	code, payload = w2PatchBody(t, env, ids[3], `"temporaryUnavailableContinuousProbeEnabled":false,"expeditedRecoveryEnabled":true`)
	if code != http.StatusBadRequest || payload["message"] != "特供账户数量已达上限（3）" {
		t.Fatalf("混交补丁超限：%d %v", code, payload)
	}
	if got := env.queryCell(t, `SELECT CAST(temporary_unavailable_continuous_probe_enabled AS TEXT) FROM accounts WHERE id = ?`, ids[3]); got != "1" {
		t.Fatalf("被拒混交补丁的探活开关被写入：%s", got)
	}

	// 取消（true→false）不校验；重新置回在限额内成功。
	code, payload = w2PatchBody(t, env, ids[0], `"expeditedRecoveryEnabled":false`)
	if code != http.StatusOK {
		t.Fatalf("取消特供：%d %v", code, payload)
	}
	if flag, _, _ := expeditedAccountState(t, env, ids[0]); flag != "0" {
		t.Fatalf("取消后标记未清零：%s", flag)
	}
	code, payload = w2PatchBody(t, env, ids[0], `"expeditedRecoveryEnabled":true`)
	if code != http.StatusOK {
		t.Fatalf("限额内重设特供：%d %v", code, payload)
	}

	// 回归：探活开关单独补丁行为不变（翻转成功并落库）。
	code, payload = w2PatchBody(t, env, ids[1], `"temporaryUnavailableContinuousProbeEnabled":false`)
	if code != http.StatusOK || !containsChange(dataMap(t, payload)["changedFields"], "temporaryUnavailableContinuousProbeEnabled") {
		t.Fatalf("探活开关回归：%d %v", code, payload)
	}
	if got := env.queryCell(t, `SELECT CAST(temporary_unavailable_continuous_probe_enabled AS TEXT) FROM accounts WHERE id = ?`, ids[1]); got != "0" {
		t.Fatalf("探活开关未落库：%s", got)
	}

	// 白名单仍严格：未知键照旧 400。
	if code, _ := w2PatchBody(t, env, ids[0], `"expeditedRecoveryEnabledX":true`); code != http.StatusBadRequest {
		t.Fatalf("未知键应拒绝：%d", code)
	}

	// 审计（§8.5）：操作日志沿用既有补丁链路，携带特供恢复变更条目。
	env.sink.mu.Lock()
	audited := false
	for _, entry := range env.sink.entries {
		if entry.Module != "accounts" || entry.Action != "update" || entry.ResourceID != ids[0] {
			continue
		}
		for _, change := range entry.Changes {
			if change.Field == "expeditedRecoveryEnabled" && change.Label == "特供恢复" &&
				change.Before == "false" && change.After == "true" {
				audited = true
			}
		}
	}
	env.sink.mu.Unlock()
	if !audited {
		t.Fatal("操作日志缺少特供恢复变更条目")
	}
}

// TestExpeditedRecoveryZeroLimitIdempotentAndCancel（store 层）：limit=0 禁止
// 新增/重新启用；存量 true→true 幂等不报错、不推进修订号；取消无条件允许。
func TestExpeditedRecoveryZeroLimitIdempotentAndCancel(t *testing.T) {
	env := newTestEnv(t)
	owner := "owner-exp-zero"
	env.seedProviderAndDefaultGroup(t, owner)
	env.seedOwnerWithLimit(t, owner, nil)
	expeditedSetOwnerLimit(t, env, owner, &[]int64{0}[0])
	env.seedAccount(t, "acc-exp-zero-a", owner, "零限存量", "active")
	env.seedAccount(t, "acc-exp-zero-b", owner, "零限新增", "active")

	enable := PatchInput{ExpectedConfigRevision: 1, ExpeditedRecoveryEnabled: boolPtr(true)}
	scope := AccessScope{ViewerID: owner}

	// 0 直接拒绝新增。
	_, err := env.store.Patch(context.Background(), "acc-exp-zero-b", enable, scope)
	assertExpeditedQuotaError(t, err, "0")
	if flag, revision, _ := expeditedAccountState(t, env, "acc-exp-zero-b"); flag != "0" || revision != "1" {
		t.Fatalf("被拒补丁必须回滚：flag=%s revision=%s", flag, revision)
	}

	// 存量标记（上限下调不回溯）：true→true 幂等成功且不重复占额、不推进修订。
	env.exec(t, `UPDATE accounts SET expedited_recovery_enabled = 1 WHERE id = 'acc-exp-zero-a'`)
	result, err := env.store.Patch(context.Background(), "acc-exp-zero-a", enable, scope)
	if err != nil {
		t.Fatalf("幂等 true→true 不应报错：%v", err)
	}
	if len(result.ChangedFields) != 0 || result.ConfigRevision != 1 {
		t.Fatalf("幂等补丁必须零变更：%+v", result)
	}

	// 取消不校验（0 也不阻止）。
	disable := PatchInput{ExpectedConfigRevision: 1, ExpeditedRecoveryEnabled: boolPtr(false)}
	if _, err := env.store.Patch(context.Background(), "acc-exp-zero-a", disable, scope); err != nil {
		t.Fatalf("取消特供不应校验：%v", err)
	}
	if flag, _, _ := expeditedAccountState(t, env, "acc-exp-zero-a"); flag != "0" {
		t.Fatalf("取消后标记未清零：%s", flag)
	}

	// 0 禁止重新启用（取消补丁已把修订号推进到 2，按实时修订号提交）。
	revision := env.queryCell(t, `SELECT config_revision FROM accounts WHERE id = 'acc-exp-zero-a'`)
	reenable := PatchInput{ExpectedConfigRevision: parseRevisionForTest(t, revision), ExpeditedRecoveryEnabled: boolPtr(true)}
	_, err = env.store.Patch(context.Background(), "acc-exp-zero-a", reenable, scope)
	assertExpeditedQuotaError(t, err, "0")
}

// TestExpeditedRecoveryCreateChannelQuota（store 层）：创建通道的待插入行计入
// 名额（当前计数 + 1 ≤ 上限），limit=2 第 3 个、limit=5 第 6 个被拒；不带标记
// 的创建不受特供名额限制。
func TestExpeditedRecoveryCreateChannelQuota(t *testing.T) {
	owner := "owner-exp-create"
	mkEnv := func(t *testing.T, limit int64) *testEnv {
		env := newTestEnv(t)
		env.seedProviderAndDefaultGroup(t, owner)
		env.seedOwnerWithLimit(t, owner, nil)
		expeditedSetOwnerLimit(t, env, owner, &limit)
		return env
	}
	expeditedCreate := func(name string) CreateInput {
		input := bug0174CreateInput(name)
		input.ExpeditedRecoveryEnabled = boolPtr(true)
		return input
	}

	t.Run("limit 2 rejects the third", func(t *testing.T) {
		env := mkEnv(t, 2)
		scope := AccessScope{ViewerID: owner}
		for _, name := range []string{"创建特供一", "创建特供二"} {
			if _, err := env.store.Create(context.Background(), expeditedCreate(name), scope); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := env.store.Create(context.Background(), expeditedCreate("创建特供三"), scope); err != nil {
			assertExpeditedQuotaError(t, err, "2")
		} else {
			t.Fatal("第 3 个特供创建必须被拒")
		}
		if got := env.count(t, `SELECT COUNT(*) FROM accounts WHERE system_account_id = ? AND expedited_recovery_enabled = 1`, owner); got != 2 {
			t.Fatalf("特供计数 = %d, want 2", got)
		}
		// 不带标记的创建不受特供名额限制，且行值恒 0。
		if _, err := env.store.Create(context.Background(), bug0174CreateInput("普通创建"), scope); err != nil {
			t.Fatal(err)
		}
		if got := env.count(t, `SELECT COUNT(*) FROM accounts WHERE system_account_id = ? AND name = '普通创建' AND expedited_recovery_enabled = 0`, owner); got != 1 {
			t.Fatalf("普通创建行特供值不为 0：%d", got)
		}
	})

	t.Run("limit 5 rejects the sixth", func(t *testing.T) {
		env := mkEnv(t, 5)
		scope := AccessScope{ViewerID: owner}
		for index := 1; index <= 5; index++ {
			if _, err := env.store.Create(context.Background(), expeditedCreate("五限特供"+string(rune('一'+index-1))), scope); err != nil {
				t.Fatal(err)
			}
		}
		_, err := env.store.Create(context.Background(), expeditedCreate("五限特供六"), scope)
		assertExpeditedQuotaError(t, err, "5")
		if got := env.count(t, `SELECT COUNT(*) FROM accounts WHERE name = '五限特供六'`); got != 0 {
			t.Fatalf("被拒创建残留行：%d", got)
		}
	})
}

// TestExpeditedRecoveryOverLimitDisplayAfterReduction（HTTP 面）：管理员下调
// 名额后，列表与详情对已特供行展示 expeditedOverLimit=true（管理员与归属人
// 视角一致），未特供行恒 false。
func TestExpeditedRecoveryOverLimitDisplayAfterReduction(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	ids := []string{}
	for _, name := range []string{"超限展示甲", "超限展示乙", "超限展示丙"} {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload(name))
		if code != http.StatusCreated {
			t.Fatalf("create %s: %d %v", name, code, payload)
		}
		ids = append(ids, dataMap(t, payload)["id"].(string))
	}
	for _, id := range ids[:2] {
		if code, payload := w2PatchBody(t, env, id, `"expeditedRecoveryEnabled":true`); code != http.StatusOK {
			t.Fatalf("enable: %d %v", code, payload)
		}
	}
	// 下调上限到 1：两行存量特供进入"已超限"。
	one := int64(1)
	expeditedSetOwnerLimit(t, env, adminID, &one)

	listItemByID := func(t *testing.T, path string) map[string]map[string]any {
		t.Helper()
		code, payload := env.do(t, http.MethodGet, path, "")
		if code != http.StatusOK {
			t.Fatalf("list %s: %d %v", path, code, payload)
		}
		items := dataMap(t, payload)["items"].([]any)
		byID := map[string]map[string]any{}
		for _, raw := range items {
			item := raw.(map[string]any)
			byID[item["id"].(string)] = item
		}
		return byID
	}

	// 管理员视角。expeditedOverLimit 按"归属系统账户"条件派生（任务钉死字段
	// 形状）：归属名下计数 2 > 上限 1 时，该归属的全部行（含未特供行）都输出
	// true；expeditedRecoveryEnabled 仍逐行取自身行值。
	adminItems := listItemByID(t, "/__aisys__/api/accounts")
	for index, id := range ids {
		item, ok := adminItems[id]
		if !ok {
			t.Fatalf("列表缺行 %s：%v", id, adminItems)
		}
		wantEnabled := index < 2
		if item["expeditedRecoveryEnabled"] != wantEnabled {
			t.Fatalf("管理员列表行 %s：enabled=%v, want %v", id, item["expeditedRecoveryEnabled"], wantEnabled)
		}
		if item["expeditedOverLimit"] != true {
			t.Fatalf("管理员列表行 %s 超限位 = %v, want true（归属超限对全部行可见）", id, item["expeditedOverLimit"])
		}
	}

	// 详情（advanced）投影一致。
	code, detail := env.do(t, http.MethodGet, "/__aisys__/api/accounts/"+ids[0]+"/advanced", "")
	if code != http.StatusOK {
		t.Fatalf("advanced: %d %v", code, detail)
	}
	detailData := dataMap(t, detail)
	if detailData["expeditedRecoveryEnabled"] != true || detailData["expeditedOverLimit"] != true {
		t.Fatalf("advanced 特供投影：%v %v", detailData["expeditedRecoveryEnabled"], detailData["expeditedOverLimit"])
	}
	code, detail = env.do(t, http.MethodGet, "/__aisys__/api/accounts/"+ids[2]+"/advanced", "")
	if code != http.StatusOK {
		t.Fatalf("advanced(plain): %d %v", code, detail)
	}
	detailData = dataMap(t, detail)
	if detailData["expeditedRecoveryEnabled"] != false || detailData["expeditedOverLimit"] != true {
		t.Fatalf("advanced 普通行投影：%v %v（归属超限时 overLimit 恒 true）",
			detailData["expeditedRecoveryEnabled"], detailData["expeditedOverLimit"])
	}

	// 归属人视角（my-accounts）：创建通道创建特供 + 下调后展示位可见。
	eveID := env.login(t, "eve", "eve-pass", "user")
	env.seedProviderAndDefaultGroup(t, eveID)
	createBody := `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"归属人特供",` +
		`"type":"api_key","credentials":{"api_key":"sk-live-secret-1234567890","base_url":"https://api.openai.com/v1"},` +
		`"supportedModels":["gpt-4o-mini"],"status":"active","expeditedRecoveryEnabled":true}`
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts", createBody)
	if code != http.StatusCreated {
		t.Fatalf("my-accounts create: %d %v", code, payload)
	}
	eveAccountID := dataMap(t, payload)["id"].(string)
	if flag, _, _ := expeditedAccountState(t, env, eveAccountID); flag != "1" {
		t.Fatalf("创建通道特供未落库：%s", flag)
	}
	zero := int64(0)
	expeditedSetOwnerLimit(t, env, eveID, &zero)
	eveItems := listItemByID(t, "/__aisys__/api/my-accounts")
	item, ok := eveItems[eveAccountID]
	if !ok {
		t.Fatalf("my-accounts 缺行：%v", eveItems)
	}
	if item["expeditedRecoveryEnabled"] != true || item["expeditedOverLimit"] != true {
		t.Fatalf("归属人视角超限展示：%v %v", item["expeditedRecoveryEnabled"], item["expeditedOverLimit"])
	}
}

// TestExpeditedRecoveryAuthorizedInstanceCountsGranteeQuota（store 层）：授权
// 实例行的特供计入被授权方名额（§3.11 有意偏离 aiAccountLimit 的排除口径）。
func TestExpeditedRecoveryAuthorizedInstanceCountsGranteeQuota(t *testing.T) {
	env := newTestEnv(t)
	sourceOwner := "owner-exp-src"
	grantee := "owner-exp-grantee"
	env.seedProviderAndDefaultGroup(t, sourceOwner)
	env.seedProviderAndDefaultGroup(t, grantee)
	env.seedOwnerWithLimit(t, sourceOwner, nil)
	env.seedOwnerWithLimit(t, grantee, nil)
	expeditedSetOwnerLimit(t, env, grantee, &[]int64{1}[0])
	env.seedAccount(t, "acc-exp-src-base", sourceOwner, "来源账户", "active")

	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, id := range []string{"acc-exp-inst-1", "acc-exp-inst-2"} {
		env.exec(t, `INSERT INTO accounts (id, system_account_id, provider_code, provider_protocol_profile_id,
			protocol_code, protocol_version, name, type, status, credentials_encrypted, credential_mask,
			health_check_model, authorization_instance_authorization_id, authorization_instance_source_account_id,
			authorization_instance_owner_system_account_id, created_at, updated_at)
			VALUES (?, ?, 'gpt', 'prof-gpt', 'openai', 'v1', ?, 'api_key', 'active', 'enc', 'm', 'gpt-4o-mini',
			'auth-exp-1', 'acc-exp-src-base', ?, ?, ?)`, id, grantee, id+"-实例名", sourceOwner, now, now)
	}

	scope := AccessScope{ViewerID: grantee}
	result, err := env.store.Patch(context.Background(), "acc-exp-inst-1",
		PatchInput{ExpectedConfigRevision: 1, ExpeditedRecoveryEnabled: boolPtr(true)}, scope)
	if err != nil {
		t.Fatalf("实例 1 置特供（被授权方名额 1 内）不应失败：%v", err)
	}
	if result.ConfigRevision != 2 {
		t.Fatalf("实例补丁修订号 = %d, want 2", result.ConfigRevision)
	}
	_, err = env.store.Patch(context.Background(), "acc-exp-inst-2",
		PatchInput{ExpectedConfigRevision: 1, ExpeditedRecoveryEnabled: boolPtr(true)}, scope)
	assertExpeditedQuotaError(t, err, "1")
	// 来源账户自己的特供不影响被授权方计数（本例来源侧无特供，计数按归属分池）。
	if got := env.count(t, `SELECT COUNT(*) FROM accounts WHERE system_account_id = ? AND expedited_recovery_enabled = 1`, grantee); got != 1 {
		t.Fatalf("被授权方特供计数 = %d, want 1", got)
	}
}

// TestExpeditedRecoveryConcurrencyNoOversell（store 层，SQLite 单写者等价于
// PG FOR UPDATE 串行化）：并发置特供恰好放行上限个，其余全部名额错误。
func TestExpeditedRecoveryConcurrencyNoOversell(t *testing.T) {
	env := newTestEnv(t)
	owner := "owner-exp-conc"
	env.seedProviderAndDefaultGroup(t, owner)
	env.seedOwnerWithLimit(t, owner, nil)
	two := int64(2)
	expeditedSetOwnerLimit(t, env, owner, &two)

	ids := []string{}
	for index := 1; index <= 6; index++ {
		id := "acc-exp-conc-" + itoa(index)
		env.seedAccount(t, id, owner, "并发特供"+itoa(index), "active")
		ids = append(ids, id)
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	successes := 0
	failures := []error{}
	for _, id := range ids {
		wg.Add(1)
		go func(accountID string) {
			defer wg.Done()
			_, err := env.store.Patch(context.Background(), accountID,
				PatchInput{ExpectedConfigRevision: 1, ExpeditedRecoveryEnabled: boolPtr(true)},
				AccessScope{ViewerID: owner})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures = append(failures, err)
				return
			}
			successes++
		}(id)
	}
	wg.Wait()

	if successes != 2 {
		t.Fatalf("并发成功数 = %d, want 2（上限）", successes)
	}
	if len(failures) != 4 {
		t.Fatalf("并发失败数 = %d, want 4", len(failures))
	}
	for _, err := range failures {
		assertExpeditedQuotaError(t, err, "2")
	}
	if got := env.count(t, `SELECT COUNT(*) FROM accounts WHERE system_account_id = ? AND expedited_recovery_enabled = 1`, owner); got != 2 {
		t.Fatalf("并发后特供计数 = %d, want 2", got)
	}
}

// TestExpeditedRecoveryCloneExportImportBatchDoNotCarry（HTTP/store 面）：
// 克隆上下文与克隆副本、导出载荷不携带标记；导入载荷含键按未知字段策略把该
// 条目标记失败且不落库；批量编辑上下文字段白名单拒绝该字段。
func TestExpeditedRecoveryCloneExportImportBatchDoNotCarry(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	createBody := `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"特供克隆源",` +
		`"type":"api_key","credentials":{"api_key":"sk-live-secret-1234567890","base_url":"https://api.openai.com/v1"},` +
		`"supportedModels":["gpt-4o-mini"],"status":"active","expeditedRecoveryEnabled":true}`
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createBody)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	sourceID := dataMap(t, payload)["id"].(string)
	if flag, _, _ := expeditedAccountState(t, env, sourceID); flag != "1" {
		t.Fatalf("源账户特供未落库：%s", flag)
	}

	// 克隆上下文不带 expeditedRecoveryEnabled 键。
	code, contextPayload := env.do(t, http.MethodGet, "/__aisys__/api/accounts/"+sourceID+"/clone-context", "")
	if code != http.StatusOK {
		t.Fatalf("clone-context: %d %v", code, contextPayload)
	}
	cloneContext := dataMap(t, contextPayload)
	if _, exists := cloneContext["expeditedRecoveryEnabled"]; exists {
		t.Fatalf("克隆上下文不得携带特供标记：%v", cloneContext)
	}

	// 克隆走创建面（体不含该键）：副本恒为未标记，且不受"绕过名额校验"影响
	// ——把上限调到 0 后克隆仍成功（未标记创建不经名额门）。
	zero := int64(0)
	expeditedSetOwnerLimit(t, env, adminID, &zero)
	cloneBody := `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"特供克隆源-副本",` +
		`"type":"api_key","credentials":{"api_key":"sk-live-secret-1234567890","base_url":"https://api.openai.com/v1"},` +
		`"supportedModels":["gpt-4o-mini"],"status":"active"}`
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts", cloneBody)
	if code != http.StatusCreated {
		t.Fatalf("clone create: %d %v", code, payload)
	}
	cloneID := dataMap(t, payload)["id"].(string)
	if flag, _, _ := expeditedAccountState(t, env, cloneID); flag != "0" {
		t.Fatalf("克隆副本必须为未标记：%s", flag)
	}
	// 同一 0 上限下显式置特供被拒（克隆通道没有旁路名额校验）。
	_, err := env.store.Patch(context.Background(), cloneID,
		PatchInput{ExpectedConfigRevision: 1, ExpeditedRecoveryEnabled: boolPtr(true)},
		AccessScope{ViewerID: adminID, IsAdmin: true})
	assertExpeditedQuotaError(t, err, "0")

	// 导出载荷不含 expeditedRecoveryEnabled 键。
	code, exported := env.do(t, http.MethodPost, "/__aisys__/api/accounts/export",
		`{"accountIds":["`+sourceID+`"]}`)
	if code != http.StatusOK {
		t.Fatalf("export: %d %v", code, exported)
	}
	document := dataMap(t, exported)["document"].(map[string]any)
	accounts := document["accounts"].([]any)
	if len(accounts) != 1 {
		t.Fatalf("export accounts: %v", document)
	}
	exportedAccount := accounts[0].(map[string]any)
	if _, exists := exportedAccount["expeditedRecoveryEnabled"]; exists {
		t.Fatalf("导出载荷不得携带特供标记：%v", exportedAccount)
	}

	// 批量编辑上下文字段白名单拒绝该字段。路由体解析先于 store 白名单，
	// 返回既有 batchContextPrompt 文案；store 层 LoadBatchEditContext 直接
	// 暴露"不支持字段"的精确错误——两层既有拒绝逻辑都钉死，不改行为。
	code, batchContext := env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-edit-context",
		`{"accountIds":["`+sourceID+`","`+cloneID+`"],"fields":["expeditedRecoveryEnabled"]}`)
	if code != http.StatusBadRequest || batchContext["message"] != "批量编辑上下文参数无效" {
		t.Fatalf("批量编辑上下文应拒绝：%d %v", code, batchContext)
	}
	_, err = env.store.LoadBatchEditContext(context.Background(), []string{sourceID, cloneID},
		[]string{"expeditedRecoveryEnabled"}, AccessScope{ViewerID: adminID, IsAdmin: true})
	if err == nil {
		t.Fatal("store 层批量编辑上下文必须拒绝特供字段")
	}
	var batchValidation *ValidationError
	if !errors.As(err, &batchValidation) || batchValidation.Message != "不支持的批量编辑上下文字段：expeditedRecoveryEnabled" {
		t.Fatalf("批量编辑不支持字段错误 = %v", err)
	}

	// 导入载荷含键 → 该条目被"包含未知字段"错误标记失败且不落库。
	seedOpenAICompatibleProvider(t, env)
	planData := w2NativeDoc(
		[]map[string]any{w2WithField(w2APIKeyAccount("导入特供", "导入分组"), "expeditedRecoveryEnabled", true)},
		nil,
	)
	result := w2Preview(t, env, planData, importSourceNative, ImportOptions{}, adminID)
	item := w2Item(t, result, 0)
	if item.Action != importActionFailed {
		t.Fatalf("导入条目必须标记失败：%s %s", item.Action, strings.Join(item.Messages, "|"))
	}
	if !strings.Contains(strings.Join(item.Messages, "|"), "账户配置包含未知字段：expeditedRecoveryEnabled") {
		t.Fatalf("未知键错误缺失：%v", item.Messages)
	}
	if result.CanImport {
		t.Fatalf("存在失败条目时不得可导入：%v", result.Summary)
	}
	if got := env.count(t, `SELECT COUNT(*) FROM accounts WHERE name = '导入特供'`); got != 0 {
		t.Fatalf("失败的导入条目不得落库：%d", got)
	}
}
