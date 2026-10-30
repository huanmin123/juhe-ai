package accounts

import (
	"net/http"
	"testing"
)

// AI 账户后台自动探测总开关 W1 读写面回归（总开关设计 §2/§4/§7/§8）：
//   - 创建手动模式账户默认 active + schedulable，不写 J1 快照意图；
//   - 关闭翻转同事务二态状态归一化（pending_test → active，
//     temporary_unavailable / rate_limited 无论有无探针 fence 一律 → error，
//     2026-10-30 复审修订：显式策略/系统额度冷却与探针冷却共用同一 fence，
//     来源标记不可靠；quality_isolated → error）+ 特供/锁死/enforcement/
//     J1 outbox 清理 + dispatch fence 推进入队 circuit 关闭事件；
//   - 同请求显式 status 输入优先，不被归一化覆盖；
//   - 媒体域 PATCH 幂等忽略该字段；
//   - 开启翻转重播种 J1 候选版本行，active 账户即时补检查；
//   - 授权实例行只读跟随来源（含锁死解除与 circuit 关闭事件），提交即 400。

// manualModeCreatePayload 是 gpt api_key 创建载荷（b1GPTFragment 同形态），
// extra 追加顶层键。
func manualModeCreatePayload(name, extra string) string {
	return `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"` + name +
		`","type":"api_key","credentials":{"api_key":"sk-live-secret-1234567890","base_url":"https://api.openai.com/v1"},` +
		`"supportedModels":["gpt-4o-mini","gpt-4.1"],` +
		`"connections":[{"connectionPresetCode":"gpt_openai_v1","baseUrl":"https://api.openai.com/v1","endpointModes":["chat_json","chat_sse"],"enabled":true}],` +
		`"healthCheckConnectionPresetCode":"gpt_openai_v1","healthCheckEndpointMode":"chat_json","healthCheckModel":"gpt-4o-mini"` +
		extra + `}`
}

// seedManualModeAccount 创建一个默认 flag=1 账户并按需改写运行态列
// （模拟状态机派生的冷却 / 探针代际 / 特供 / 锁死前置事实）。
func seedManualModeAccount(t *testing.T, env *testEnv, name string) (string, int64) {
	t.Helper()
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", manualModeCreatePayload(name, `,"status":"active"`))
	if code != http.StatusCreated {
		t.Fatalf("create %s: %d %v", name, code, payload)
	}
	id := dataMap(t, payload)["id"].(string)
	var revision int64
	if err := env.db.QueryRow(env.bindFixture(`SELECT config_revision FROM accounts WHERE id = ?`), id).Scan(&revision); err != nil {
		t.Fatalf("read config revision: %v", err)
	}
	return id, revision
}

func TestCreateManualModeAccountDefaultsToActive(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	// 缺省状态 + 显式关闭：active + 可调度，无 J1 快照意图、无候选版本行。
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", manualModeCreatePayload("manual-default", `,"backgroundProbeEnabled":false`))
	if code != http.StatusCreated {
		t.Fatalf("create manual account: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND status = 'active'
		AND schedulable = 1 AND background_probe_enabled = 0`, id) != 1 {
		t.Fatal("manual-mode create must land active + schedulable with the switch off")
	}
	if env.count(t, `SELECT COUNT(*) FROM account_health_jobs_input_outbox WHERE account_id = ?`, id) != 0 {
		t.Fatal("manual-mode create must not land the J1 snapshot intent")
	}
	if env.count(t, `SELECT COUNT(*) FROM account_health_jobs_input_versions WHERE account_id = ?`, id) != 0 {
		t.Fatal("manual-mode create must not reserve the J1 input version row")
	}

	// 显式 error：保持异常、停调度。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts", manualModeCreatePayload("manual-error", `,"backgroundProbeEnabled":false,"status":"error"`))
	if code != http.StatusCreated {
		t.Fatalf("create manual error account: %d %v", code, payload)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND status = 'error' AND schedulable = 0`,
		dataMap(t, payload)["id"].(string)) != 1 {
		t.Fatal("manual-mode explicit error must persist as error")
	}

	// 显式过程态：400。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts", manualModeCreatePayload("manual-pending", `,"backgroundProbeEnabled":false,"status":"pending_test"`))
	if code != http.StatusBadRequest {
		t.Fatalf("manual-mode explicit pending_test must 400: %d %v", code, payload)
	}

	// 手动模式 + 特供：400（自动恢复功能没有闭环）。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts", manualModeCreatePayload("manual-expedited", `,"backgroundProbeEnabled":false,"expeditedRecoveryEnabled":true`))
	if code != http.StatusBadRequest {
		t.Fatalf("manual-mode expedited create must 400: %d %v", code, payload)
	}
}

func TestPatchBackgroundProbeDisableNormalizesStatuses(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	// (a) pending_test（创建默认 + J1 快照在位）→ active，清 J1 意图与版本行。
	pendingID, revision := seedManualModeAccount(t, env, "disable-pending")
	// 创建链已推进过一次 dispatch fence 并入队创建事件，禁用臂断言按增量对比。
	var fenceBefore int64
	if err := env.db.QueryRow(env.bindFixture(`SELECT dispatch_revision FROM accounts WHERE id = ?`), pendingID).Scan(&fenceBefore); err != nil {
		t.Fatalf("read dispatch revision: %v", err)
	}
	var outboxBefore int
	if err := env.db.QueryRow(env.bindFixture(`SELECT COUNT(*) FROM account_circuit_outbox WHERE account_id = ?
		AND event_type = 'dispatch_revision_changed' AND status = 'pending'`), pendingID).Scan(&outboxBefore); err != nil {
		t.Fatalf("count circuit outbox: %v", err)
	}
	env.exec(t, `UPDATE accounts SET status = 'pending_test', schedulable = 0,
		last_error_message = '账户已保存，等待后台健康检查' WHERE id = ?`, pendingID)
	code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+pendingID,
		`{"expectedConfigRevision": `+itoa64(revision)+`, "backgroundProbeEnabled": false}`)
	if code != http.StatusOK {
		t.Fatalf("disable pending account: %d %v", code, payload)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND background_probe_enabled = 0
		AND status = 'active' AND schedulable = 1 AND config_revision = ?
		AND cooldown_until IS NULL AND cooldown_retest_generation IS NULL
		AND next_health_check_at IS NULL AND health_check_failure_count = 0`, pendingID, revision+1) != 1 {
		t.Fatal("pending_test must normalize to active + schedulable with cooldown/health columns cleared")
	}
	if env.count(t, `SELECT COUNT(*) FROM account_health_jobs_input_outbox WHERE account_id = ? AND status = 'pending'`, pendingID) != 0 {
		t.Fatal("disable must delete pending J1 outbox rows")
	}
	if env.count(t, `SELECT COUNT(*) FROM account_health_jobs_input_versions WHERE account_id = ?`, pendingID) != 0 {
		t.Fatal("disable must delete the J1 input version row")
	}
	// circuit 关闭链（复审 M1）：dispatch fence 推进 + dispatch_revision_changed
	// 事件在位（jobs 控制面投影消费后按账户关闭全部非 CLOSED circuit 运行态）。
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND dispatch_revision = ?`, pendingID, fenceBefore+1) != 1 {
		t.Fatal("disable must advance the dispatch revision fence")
	}
	if env.count(t, `SELECT COUNT(*) FROM account_circuit_outbox WHERE account_id = ? AND account_runtime_key = ?
		AND event_type = 'dispatch_revision_changed' AND status = 'pending'`, pendingID, pendingID) != outboxBefore+1 {
		t.Fatal("disable must enqueue exactly one more circuit close outbox event")
	}

	// (b) 显式策略派生冷却（无 fence）→ error + 人工恢复原因（复审修订后与
	// 有 fence 同判——来源标记不可靠，见 (c)）。
	explicitID, revision := seedManualModeAccount(t, env, "disable-explicit")
	env.exec(t, `UPDATE accounts SET status = 'temporary_unavailable', schedulable = 0,
		cooldown_until = '2026-09-01T00:05:00.000Z',
		last_error_message = '手动设置为临时不可调用'
		WHERE id = ?`, explicitID)
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+explicitID,
		`{"expectedConfigRevision": `+itoa64(revision)+`, "backgroundProbeEnabled": false}`)
	if code != http.StatusOK {
		t.Fatalf("disable explicit-policy cooling: %d %v", code, payload)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND status = 'error' AND schedulable = 0
		AND last_error_message = '手动模式：原冷却/限流状态转为异常，需人工恢复'
		AND cooldown_until IS NULL`, explicitID) != 1 {
		t.Fatal("explicit-policy cooling must normalize to error with the manual-mode recovery reason")
	}

	// (c) 探针 fence 在位的 rate_limited → error（2026-10-30 复审修订：显式
	// 策略 / 系统额度冷却与探针冷却共用同一 cooldown_retest_generation fence
	// （markCooldown 同写 "cooldown:" 代际），来源标记不可靠，无论有无 fence
	// 一律转异常保留人工恢复）。
	fencedID, revision := seedManualModeAccount(t, env, "disable-fenced")
	env.exec(t, `UPDATE accounts SET status = 'rate_limited', schedulable = 0,
		cooldown_until = '2026-09-01T00:05:00.000Z',
		cooldown_retest_failure_count = 1,
		cooldown_retest_generation = 'cooldown:gen-fence',
		cooldown_retest_observation_started_at = '2026-09-01T00:00:00.000Z'
		WHERE id = ?`, fencedID)
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+fencedID,
		`{"expectedConfigRevision": `+itoa64(revision)+`, "backgroundProbeEnabled": false}`)
	if code != http.StatusOK {
		t.Fatalf("disable probe-fenced cooling: %d %v", code, payload)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND status = 'error' AND schedulable = 0
		AND last_error_message = '手动模式：原冷却/限流状态转为异常，需人工恢复'
		AND cooldown_retest_generation IS NULL AND cooldown_until IS NULL`, fencedID) != 1 {
		t.Fatal("fenced cooling must normalize to error with the fence cleared and the manual-mode recovery reason")
	}

	// (c2) 同请求显式 status=active 优先（复审 Minor）：显式提交的状态是该次
	// 操作意图，不被行内现值推导的归一化覆盖为 error；冷却列族仍按禁用臂清理。
	explicitActiveID, revision := seedManualModeAccount(t, env, "disable-explicit-active")
	env.exec(t, `UPDATE accounts SET status = 'temporary_unavailable', schedulable = 0,
		cooldown_until = '2026-09-01T00:05:00.000Z', cooldown_retest_generation = 'cooldown:gen-x'
		WHERE id = ?`, explicitActiveID)
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+explicitActiveID,
		`{"expectedConfigRevision": `+itoa64(revision)+`, "backgroundProbeEnabled": false, "status": "active"}`)
	if code != http.StatusOK {
		t.Fatalf("disable with explicit active: %d %v", code, payload)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND status = 'active' AND schedulable = 1
		AND background_probe_enabled = 0 AND cooldown_until IS NULL
		AND cooldown_retest_generation IS NULL
		AND last_error_message IS NULL`, explicitActiveID) != 1 {
		t.Fatal("explicit status=active in the same request must win over normalization while cooldown columns still clear")
	}

	// (d) quality_isolated → error，enforcement 活动行清理。
	qualityID, revision := seedManualModeAccount(t, env, "disable-quality")
	env.exec(t, `UPDATE accounts SET status = 'quality_isolated', schedulable = 0 WHERE id = ?`, qualityID)
	env.exec(t, `INSERT INTO account_quality_enforcements (account_id, system_account_id, enforcement_id,
		generation, state, updated_at) VALUES (?, ?, 'enf-1', 0, 'active', '2026-09-01T00:00:00.000Z')`, qualityID, adminID)
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+qualityID,
		`{"expectedConfigRevision": `+itoa64(revision)+`, "backgroundProbeEnabled": false}`)
	if code != http.StatusOK {
		t.Fatalf("disable quality isolated: %d %v", code, payload)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND status = 'error'`, qualityID) != 1 {
		t.Fatal("quality_isolated must normalize to error")
	}
	if env.count(t, `SELECT COUNT(*) FROM account_quality_enforcements WHERE account_id = ? AND state = 'cleared'`, qualityID) != 1 {
		t.Fatal("quality enforcement active row must be cleared on manual-mode entry")
	}

	// (e) 特供自动取消 + 锁死自动解除。
	cleanupID, revision := seedManualModeAccount(t, env, "disable-cleanup")
	env.exec(t, `UPDATE accounts SET expedited_recovery_enabled = 1 WHERE id = ?`, cleanupID)
	env.exec(t, `INSERT INTO account_lock_states (account_id, enabled, lock_state,
		lock_death_timeout_seconds, lock_retry_interval_seconds, generation, updated_at)
		VALUES (?, 1, 'LOCKED_IDLE', 300, 5, 3, '2026-09-01T00:00:00.000Z')`, cleanupID)
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+cleanupID,
		`{"expectedConfigRevision": `+itoa64(revision)+`, "backgroundProbeEnabled": false}`)
	if code != http.StatusOK {
		t.Fatalf("disable with expedited/lock: %d %v", code, payload)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND expedited_recovery_enabled = 0`, cleanupID) != 1 {
		t.Fatal("manual-mode entry must auto-cancel the expedited flag")
	}
	if env.count(t, `SELECT COUNT(*) FROM account_lock_states WHERE account_id = ?
		AND enabled = 0 AND lock_state = 'UNLOCKED' AND generation = 4`, cleanupID) != 1 {
		t.Fatal("manual-mode entry must auto-release the lock with a generation advance")
	}

	// (f) 手动模式再设特供：400。
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+cleanupID,
		`{"expectedConfigRevision": `+itoa64(revision+1)+`, "expeditedRecoveryEnabled": true}`)
	if code != http.StatusBadRequest {
		t.Fatalf("manual-mode expedited set must 400: %d %v", code, payload)
	}

	// (g) 授权实例行提交开关：400 授权实例跟随来源账户。
	instanceID := adminID + "-instance"
	env.seedProbePropagationInstance(t, adminID, cleanupID, instanceID, "active")
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+instanceID,
		`{"expectedConfigRevision": 1, "backgroundProbeEnabled": true}`)
	if code != http.StatusBadRequest {
		t.Fatalf("instance backgroundProbeEnabled submission must 400: %d %v", code, payload)
	}
}

// TestPatchBackgroundProbeMediaDomainIdempotentIgnore 钉住媒体域 PATCH 幂等
// 忽略分支（总开关设计 §2：创建/编辑拒绝，PATCH 幂等忽略）：media 域账户
// 提交 backgroundProbeEnabled 不产生变更、不翻转开关、不递增 revision。
func TestPatchBackgroundProbeMediaDomainIdempotentIgnore(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	id, revision := seedManualModeAccount(t, env, "media-probe-ignore")
	env.exec(t, `UPDATE accounts SET capability_domain = 'video' WHERE id = ?`, id)

	code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision": `+itoa64(revision)+`, "backgroundProbeEnabled": false}`)
	if code != http.StatusOK {
		t.Fatalf("media patch: %d %v", code, payload)
	}
	changed, _ := dataMap(t, payload)["changedFields"].([]any)
	if len(changed) != 0 {
		t.Fatalf("media domain must ignore backgroundProbeEnabled, changedFields=%v", changed)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND background_probe_enabled = 1
		AND config_revision = ?`, id, revision) != 1 {
		t.Fatal("media domain must keep the switch on and the revision unchanged")
	}
}

// TestPatchClearFailureStateManualModeErrorRestoresDirectlyToActive 钉住手动
// 模式 chat 账户「异常恢复」直达 active（总开关设计 §7，二轮复审 Major-1）：
// clearFailureState 对 error 账户不再落 pending_test（永不复检的滞留态），
// 不投递任何后台检查。
func TestPatchClearFailureStateManualModeErrorRestoresDirectlyToActive(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	// 原生手动模式创建（flag=0 + error）：创建链不落 J1 快照意图与版本行，
	// 后续「无健康投递」断言才有因果意义。
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		manualModeCreatePayload("manual-clear-failure", `,"backgroundProbeEnabled":false,"status":"error"`))
	if code != http.StatusCreated {
		t.Fatalf("create manual error account: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)
	var revision int64
	if err := env.db.QueryRow(env.bindFixture(`SELECT config_revision FROM accounts WHERE id = ?`), id).Scan(&revision); err != nil {
		t.Fatalf("read config revision: %v", err)
	}
	env.exec(t, `UPDATE accounts SET last_error_message = '上游凭据失效' WHERE id = ?`, id)

	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision": `+itoa64(revision)+`, "clearFailureState": true}`)
	if code != http.StatusOK {
		t.Fatalf("manual-mode clear failure: %d %v", code, payload)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND status = 'active'
		AND schedulable = 1 AND background_probe_enabled = 0
		AND last_error_message IS NULL AND next_health_check_at IS NULL`, id) != 1 {
		t.Fatal("manual-mode error account must restore directly to active via clearFailureState")
	}
	if env.count(t, `SELECT COUNT(*) FROM account_health_jobs_input_outbox WHERE account_id = ?`, id) != 0 {
		t.Fatal("manual-mode restore must not enqueue J1 snapshot intents")
	}
	if env.count(t, `SELECT COUNT(*) FROM account_health_jobs_input_versions WHERE account_id = ?`, id) != 0 {
		t.Fatal("manual-mode restore must not reserve the J1 input version row")
	}
}

func TestPatchBackgroundProbeEnableRearmsActiveAccount(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	id, revision := seedManualModeAccount(t, env, "rearm")
	env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision": `+itoa64(revision)+`, "backgroundProbeEnabled": false}`)
	// 重新开启（active）：重播种 J1 候选版本行并安排即时检查（next 清空 +
	// 提交后派发），revision 再 +1。
	code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision": `+itoa64(revision+1)+`, "backgroundProbeEnabled": true}`)
	if code != http.StatusOK {
		t.Fatalf("re-enable: %d %v", code, payload)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND background_probe_enabled = 1
		AND config_revision = ? AND status = 'active'`, id, revision+2) != 1 {
		t.Fatal("re-enable must persist the switch with a revision bump")
	}
	if env.count(t, `SELECT COUNT(*) FROM account_health_jobs_input_versions WHERE account_id = ?`, id) != 1 {
		t.Fatal("re-enable must re-seed the J1 input version row")
	}
}

func TestPatchBackgroundProbeDisablePropagatesToAuthorizationInstances(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	sourceID, revision := seedManualModeAccount(t, env, "propagate-source")
	// pending_test 实例（归一 active，带锁死运行态）与 error 实例（保持）。
	env.seedProbePropagationInstance(t, adminID, sourceID, "inst-pending", "pending_test")
	env.seedProbePropagationInstance(t, adminID, sourceID, "inst-error", "error")
	env.exec(t, `INSERT INTO account_lock_states (account_id, enabled, lock_state,
		lock_death_timeout_seconds, lock_retry_interval_seconds, generation, updated_at)
		VALUES ('inst-pending', 1, 'ENGAGED', 300, 5, 2, '2026-09-01T00:00:00.000Z')`)

	code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+sourceID,
		`{"expectedConfigRevision": `+itoa64(revision)+`, "backgroundProbeEnabled": false}`)
	if code != http.StatusOK {
		t.Fatalf("disable source: %d %v", code, payload)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = 'inst-pending'
		AND background_probe_enabled = 0 AND status = 'active' AND schedulable = 1
		AND cooldown_retest_generation IS NULL AND config_revision = 2`) != 1 {
		t.Fatal("pending_test instance must follow the source and normalize to active")
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = 'inst-error'
		AND background_probe_enabled = 0 AND status = 'error' AND config_revision = 2`) != 1 {
		t.Fatal("error instance must follow the source and keep its status")
	}
	// 实例锁死解除（复审 M1 补漏）：实例行 account_lock_states 残留 enabled 行
	// 会让实例滞留 ENGAGED，禁用传播必须同款解除并 generation+1。
	if env.count(t, `SELECT COUNT(*) FROM account_lock_states WHERE account_id = 'inst-pending'
		AND enabled = 0 AND lock_state = 'UNLOCKED' AND generation = 3`) != 1 {
		t.Fatal("instance lock state must be released with a generation advance on manual-mode entry")
	}
	// 实例 circuit 关闭事件（复审 M1）：每个实例行各自入队
	// dispatch_revision_changed（幂等键按实例派生），dispatch fence 推进。
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = 'inst-pending' AND dispatch_revision = 2`) != 1 ||
		env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = 'inst-error' AND dispatch_revision = 2`) != 1 {
		t.Fatal("instances must advance their dispatch revision fence")
	}
	if env.count(t, `SELECT COUNT(*) FROM account_circuit_outbox WHERE account_id = 'inst-pending'
		AND event_type = 'dispatch_revision_changed' AND status = 'pending'`) != 1 ||
		env.count(t, `SELECT COUNT(*) FROM account_circuit_outbox WHERE account_id = 'inst-error'
			AND event_type = 'dispatch_revision_changed' AND status = 'pending'`) != 1 {
		t.Fatal("each instance must enqueue its own circuit close outbox event")
	}

	// 重开方向不做状态归一化（二轮复审 Minor-3）：手动模式期间被显式策略写成
	// temporary_unavailable 的实例在重新开启时保持现状，由用户人工处理；与来源
	// 臂「归一化仅关闭方向推导」对称。
	env.exec(t, `UPDATE accounts SET status = 'temporary_unavailable', schedulable = 0,
		last_error_message = '手动设置为临时不可调用' WHERE id = 'inst-error'`)
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+sourceID,
		`{"expectedConfigRevision": `+itoa64(revision+1)+`, "backgroundProbeEnabled": true}`)
	if code != http.StatusOK {
		t.Fatalf("re-enable source: %d %v", code, payload)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = 'inst-error'
		AND background_probe_enabled = 1 AND status = 'temporary_unavailable' AND schedulable = 0
		AND last_error_message = '手动设置为临时不可调用' AND config_revision = 3`) != 1 {
		t.Fatal("re-enable must keep the instance status untouched (no silent normalization)")
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND background_probe_enabled = 1`, sourceID) != 1 {
		t.Fatal("re-enable must persist the source switch")
	}
}
