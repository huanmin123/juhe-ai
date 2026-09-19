package accounts

// w13g runtime_reset.go 深度补测：授权分支 skip 矩阵、owner/authorized 失败臂、
// dispatch fence 重放与身份冲突、summary/纯函数不可达值。
//
// 不可达登记（w13g，均无法从外部入口触发）：
// - 316-318 owner patched==nil：owner summary 的 join 保证行存在且 stamp 为空，
//   scope 由同一 access 计算，ErrNoRows/stamp 分支恒假。
// - 754-756 / 1024-1026 UPDATE err：单连接 SQLite（SetMaxOpenConns(1)）无法在
//   BeginTx 与 Exec 之间注入故障，事务存活期间外部 DDL 会互等挂死。
// - 757-759 / 1027-1029 RowsAffected!=1：单连接下事务内 CAS 恒命中。
// - 766-768 / 1036-1038 / 1255-1257 / 1294-1296 tx.Commit err：内存 SQLite
//   commit 无故障注入口。
// - 987-989 authorizedResetUnavailableMessage err 臂：函数体所有路径返回
//   (string, nil)，err 恒为 nil。
// - 1274-1276 / 1277-1279 fence UPDATE err 与 affected!=1：同上单连接限制。
// - 1282-1284 batchCircuitEventID err：crypto/rand.Reader 不可注入。
// - 1453-1455 第二个 scoped==""&&!canAccessAll 检查：1443-1445 已用同一条件
//   return，到达时恒假。

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// schemaCircuitOutboxDDL 镜像 accounts_test.go schemaStatements 中的同名表
// （DROP 后重建用），列集与约束保持一致。
const schemaCircuitOutboxDDL = `CREATE TABLE IF NOT EXISTS account_circuit_outbox (
	event_id TEXT PRIMARY KEY,
	projection_key TEXT NOT NULL,
	dedupe_key TEXT NOT NULL,
	event_type TEXT NOT NULL CHECK (event_type IN ('dispatch_revision_changed', 'incident_changed')),
	account_id TEXT NOT NULL,
	account_runtime_key TEXT NOT NULL,
	circuit_scope_key TEXT,
	incident_id TEXT,
	transition_id TEXT NOT NULL,
	dispatch_revision INTEGER NOT NULL CHECK (dispatch_revision >= 1),
	generation INTEGER,
	ledger_revision INTEGER,
	status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'processing', 'dispatched')),
	available_at_ms INTEGER NOT NULL,
	claim_token TEXT,
	claimed_by TEXT,
	claim_until_ms INTEGER,
	attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
	last_error_class TEXT,
	acknowledged_at_ms INTEGER,
	created_at_ms INTEGER NOT NULL,
	updated_at_ms INTEGER NOT NULL,
	UNIQUE (projection_key, dedupe_key)
)`

// w13gStrictOutboxDDL 把 status CHECK 收紧为 'processing'：合法 INSERT（写
// 'pending'）必然违反约束，用于触发 fence INSERT 失败臂。
const w13gStrictOutboxDDL = `CREATE TABLE IF NOT EXISTS account_circuit_outbox (
	event_id TEXT PRIMARY KEY,
	projection_key TEXT NOT NULL,
	dedupe_key TEXT NOT NULL,
	event_type TEXT NOT NULL CHECK (event_type IN ('dispatch_revision_changed', 'incident_changed')),
	account_id TEXT NOT NULL,
	account_runtime_key TEXT NOT NULL,
	circuit_scope_key TEXT,
	incident_id TEXT,
	transition_id TEXT NOT NULL,
	dispatch_revision INTEGER NOT NULL CHECK (dispatch_revision >= 1),
	generation INTEGER,
	ledger_revision INTEGER,
	status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('processing')),
	available_at_ms INTEGER NOT NULL,
	claim_token TEXT,
	claimed_by TEXT,
	claim_until_ms INTEGER,
	attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
	last_error_class TEXT,
	acknowledged_at_ms INTEGER,
	created_at_ms INTEGER NOT NULL,
	updated_at_ms INTEGER NOT NULL,
	UNIQUE (projection_key, dedupe_key)
)`

// w13gEffects 在 fakeRuntimeEffects 之上加故障开关与回调钩子，用于在主流程
// 特定步骤之间注入 DROP TABLE / 翻转结果。
type w13gEffects struct {
	*fakeRuntimeEffects
	mu              sync.Mutex
	failLatency     bool
	failRevalidate  bool
	failLoadTrans   bool
	failClearTrans  bool
	failQuota       bool
	quotaFlipSecond bool
	quotaFlipped    bool
	quotaCalls      int
	onClear         func(e *w13gEffects)
	onLatency       func(e *w13gEffects)
	onRevalidate    func(e *w13gEffects)
}

func (w *w13gEffects) ClearAccountRuntimeAvailability(ctx context.Context, in RuntimeAvailabilityClearInput) (RuntimeAvailabilityClearResult, error) {
	if hook := w.onClear; hook != nil {
		hook(w)
	}
	return w.fakeRuntimeEffects.ClearAccountRuntimeAvailability(ctx, in)
}

func (w *w13gEffects) ClearNormalRouteLatencyDegradation(ctx context.Context, sid, aid string) (int64, error) {
	if w.failLatency {
		return 0, context.DeadlineExceeded
	}
	if hook := w.onLatency; hook != nil {
		hook(w)
	}
	return w.fakeRuntimeEffects.ClearNormalRouteLatencyDegradation(ctx, sid, aid)
}

func (w *w13gEffects) RevalidateAccountAPIKeyRuntimePool(ctx context.Context, id string, rev int64) (AccountAPIKeyRuntimeRevalidation, error) {
	if w.failRevalidate {
		return AccountAPIKeyRuntimeRevalidation{}, context.DeadlineExceeded
	}
	if hook := w.onRevalidate; hook != nil {
		hook(w)
	}
	return w.fakeRuntimeEffects.RevalidateAccountAPIKeyRuntimePool(ctx, id, rev)
}

func (w *w13gEffects) LoadAPIKeyTransientStates(ctx context.Context, id string, fps []string) ([]AccountAPIKeyTransientSelectionState, error) {
	if w.failLoadTrans {
		return nil, context.DeadlineExceeded
	}
	return w.fakeRuntimeEffects.LoadAPIKeyTransientStates(ctx, id, fps)
}

func (w *w13gEffects) ClearAPIKeyTransientFailure(ctx context.Context, id, fp string, gen *int64) (bool, error) {
	if w.failClearTrans {
		return false, context.DeadlineExceeded
	}
	return w.fakeRuntimeEffects.ClearAPIKeyTransientFailure(ctx, id, fp, gen)
}

func (w *w13gEffects) AuthorizationQuotaExceeded(ctx context.Context, in AuthorizationQuotaCheckInput) (bool, error) {
	w.mu.Lock()
	calls := w.quotaCalls
	w.quotaCalls++
	flipped := w.quotaFlipped
	if w.quotaFlipSecond {
		w.quotaFlipped = true
	}
	w.mu.Unlock()
	if w.failQuota {
		return false, context.DeadlineExceeded
	}
	if w.quotaFlipSecond && flipped {
		return true, nil
	}
	_ = calls
	return w.fakeRuntimeEffects.AuthorizationQuotaExceeded(ctx, in)
}

// w13gSeedAuthInstance 建立授权实例最小 fixture：owner 源账户 + active 授权 +
// grantee 侧实例行 + enabled 分组绑定。返回 instanceID。
func w13gSeedAuthInstance(t *testing.T, env *testEnv, ownerID, granteeID, suffix, authStatus, instanceStatus string, withBinding bool) (instanceID, sourceID, authID, groupID string) {
	t.Helper()
	env.login(t, "root", "root-pass", "super_admin")
	// owner 与 grantee 的默认分组在整个测试内复用，先查再补种避免唯一冲突。
	var seeded string
	if err := env.db.QueryRow(`SELECT id FROM groups WHERE system_account_id = ? AND provider_code = 'gpt' AND is_default = 1`, ownerID).Scan(&seeded); errors.Is(err, sql.ErrNoRows) {
		env.seedProviderAndDefaultGroup(t, ownerID)
	} else if err != nil {
		t.Fatal(err)
	}
	sourceID = "acc-w13g-src-" + suffix
	env.seedAccount(t, sourceID, ownerID, "w13g-src-"+suffix, "active")
	err := env.db.QueryRow(`SELECT id FROM groups WHERE system_account_id = ? AND provider_code = 'gpt' AND is_default = 1`, granteeID).Scan(&groupID)
	if errors.Is(err, sql.ErrNoRows) {
		env.seedProviderAndDefaultGroup(t, granteeID)
		err = env.db.QueryRow(`SELECT id FROM groups WHERE system_account_id = ? AND provider_code = 'gpt' AND is_default = 1`, granteeID).Scan(&groupID)
	}
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	authID = "ra-w13g-" + suffix
	instanceID = "acc-w13g-inst-" + suffix
	env.exec(t, `INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id,
		grantee_system_account_id, scope, status, effective_source_type, created_by, created_at, updated_at)
		VALUES (?, 'account', ?, ?, ?, 'use', ?, 'manual', ?, ?, ?)`,
		authID, sourceID, ownerID, granteeID, authStatus, ownerID, now, now)
	sealed, err := EncryptJSON(testSecret, Credentials{"api_key": "sk-w13g-" + suffix, "base_url": "https://api.openai.com/v1"})
	if err != nil {
		t.Fatal(err)
	}
	env.exec(t, `INSERT INTO accounts (id, config_revision, dispatch_revision, system_account_id, provider_code,
		provider_protocol_profile_id, protocol_code, protocol_version, name, type, status, credentials_encrypted,
		credential_mask, health_check_model, health_check_endpoint_mode, created_at, updated_at,
		authorization_instance_authorization_id, authorization_instance_source_account_id)
		VALUES (?, 1, 1, ?, 'gpt', 'prof-gpt', 'openai', 'v1', ?, 'api_key', ?, ?, '', 'gpt-4o-mini', 'chat_json', ?, ?, ?, ?)`,
		instanceID, granteeID, "w13g-inst-"+suffix, instanceStatus, sealed, now, now, authID, sourceID)
	if withBinding {
		env.exec(t, `INSERT INTO group_accounts (system_account_id, group_id, account_id, enabled, created_at, updated_at, account_authorization_id)
			VALUES (?, ?, ?, 1, ?, ?, ?)`, granteeID, groupID, instanceID, now, now, authID)
	}
	return instanceID, sourceID, authID, groupID
}

func TestW13GRuntimeResetReadFailures(t *testing.T) {
	// 同一测试函数内的多个 newTestEnv 共享 cache=shared 内存库 DSN，用
	// 子测试名隔离库。
	t.Run("accounts-drop", func(t *testing.T) {
		env := newTestEnv(t)
		adminID := env.login(t, "root", "root-pass", "super_admin")
		env.seedProviderAndDefaultGroup(t, adminID)
		env.seedAccount(t, "acc-w13g-rf1", adminID, "w13g-rf1", "active")
		env.exec(t, `DROP TABLE accounts`)
		if _, err := env.store.ResetAccountRuntimeState(context.Background(), "acc-w13g-rf1", 1,
			AccessScope{ViewerID: adminID, IsAdmin: true}); err == nil {
			t.Fatal("accounts 表缺失应报错")
		}
	})
	t.Run("lock-drop", func(t *testing.T) {
		env := newTestEnv(t)
		adminID := env.login(t, "root", "root-pass", "super_admin")
		env.seedProviderAndDefaultGroup(t, adminID)
		env.seedAccount(t, "acc-w13g-rf2", adminID, "w13g-rf2", "active")
		env.exec(t, `DROP TABLE account_lock_states`)
		if _, err := env.store.ResetAccountRuntimeState(context.Background(), "acc-w13g-rf2", 1,
			AccessScope{ViewerID: adminID, IsAdmin: true}); err == nil || !strings.Contains(err.Error(), "账户锁死状态读取失败") {
			t.Fatalf("lock 表缺失应报错：%v", err)
		}
	})
}

func TestW13GRuntimeResetAuthorizedSkipMatrix(t *testing.T) {
	env := newTestEnv(t)
	root := env.login(t, "root", "root-pass", "super_admin")
	if err := env.db.QueryRow(`SELECT id FROM system_accounts WHERE username = 'root'`).Scan(&root); err != nil {
		t.Fatal(err)
	}
	env.login(t, "alice", "alice-pass", "user")
	alice := ""
	if err := env.db.QueryRow(`SELECT id FROM system_accounts WHERE username = 'alice'`).Scan(&alice); err != nil {
		t.Fatal(err)
	}
	effects := &w13gEffects{fakeRuntimeEffects: &fakeRuntimeEffects{}}
	env.store.SetRuntimeResetEffects(effects)

	cases := []struct {
		suffix string
		status string
		update string
		args   []any
		want   string
	}{
		{"errst", "error", ``, nil, "health_check_gate"},
		{"disbld", "disabled", ``, nil, "disabled"},
		{"qual", "quality_isolated", ``, nil, "quality_isolated"},
		{"expd", "active", `UPDATE accounts SET account_expires_at = ? WHERE id = ?`, []any{"2020-01-01T00:00:00Z"}, "expired"},
		{"manual", "active", `UPDATE accounts SET schedulable = 0 WHERE id = ?`, nil, "manual_unschedulable"},
		{"expl", "active", `UPDATE accounts SET last_error_code = ? WHERE id = ?`, []any{"explicit_account_error_policy_cooldown"}, "explicit_policy_cooldown"},
	}
	for _, c := range cases {
		id, _, _, _ := w13gSeedAuthInstance(t, env, root, alice, c.suffix, "active", c.status, true)
		if c.update != "" {
			args := c.args
			if args == nil {
				args = []any{id}
			} else {
				args = append(args, id)
			}
			env.exec(t, c.update, args...)
		}
		outcome, err := env.store.ResetAccountRuntimeState(context.Background(), id, 1,
			AccessScope{ViewerID: alice})
		if err != nil || outcome == nil {
			t.Fatalf("%s：重置失败：%v", c.suffix, err)
		}
		if !strings.Contains(strings.Join(outcome.Result.Skipped, "|"), c.want) {
			t.Fatalf("%s：应跳过 %q，实际 %v", c.suffix, c.want, outcome.Result.Skipped)
		}
	}

	// 授权额度超额 → skipped 含 authorization_quota。
	id, _, _, _ := w13gSeedAuthInstance(t, env, root, alice, "quota", "active", "rate_limited", true)
	effects.fakeRuntimeEffects.quotaExceeded = true
	outcome, err := env.store.ResetAccountRuntimeState(context.Background(), id, 1,
		AccessScope{ViewerID: alice})
	if err != nil || outcome == nil {
		t.Fatalf("quota 重置失败：%v", err)
	}
	if !strings.Contains(strings.Join(outcome.Result.Skipped, "|"), "authorization_quota") {
		t.Fatalf("quota 应跳过：%v", outcome.Result.Skipped)
	}
	effects.fakeRuntimeEffects.quotaExceeded = false

	// quota 读失败 → 按未超额处理仍走 sourceBlocked 之外的常规清理。
	effects.failQuota = true
	outcome, err = env.store.ResetAccountRuntimeState(context.Background(), id, 1,
		AccessScope{ViewerID: alice})
	if err != nil || outcome == nil {
		t.Fatalf("quota 读失败重置异常：%v", err)
	}
	if strings.Contains(strings.Join(outcome.Result.Skipped, "|"), "authorization_quota") {
		t.Fatalf("quota 读失败不应标记超额：%v", outcome.Result.Skipped)
	}
	effects.failQuota = false

	// quota 翻转：summary 阶段未超额、dispatch 阶段超额 → dispatch 报错向上传播。
	id2, _, _, _ := w13gSeedAuthInstance(t, env, root, alice, "flip", "active", "rate_limited", true)
	effects.quotaFlipSecond = true
	if _, err := env.store.ResetAccountRuntimeState(context.Background(), id2, 1,
		AccessScope{ViewerID: alice}); err == nil || err.Error() != "授权额度已用完，当前账户不能调用" {
		t.Fatalf("quota 翻转应报错：%v", err)
	}
	effects.quotaFlipSecond = false
}

func TestW13GRuntimeResetAuthorizedUnchangedAndNoBinding(t *testing.T) {
	env := newTestEnv(t)
	root := env.login(t, "root", "root-pass", "super_admin")
	if err := env.db.QueryRow(`SELECT id FROM system_accounts WHERE username = 'root'`).Scan(&root); err != nil {
		t.Fatal(err)
	}
	env.login(t, "alice", "alice-pass", "user")
	alice := ""
	if err := env.db.QueryRow(`SELECT id FROM system_accounts WHERE username = 'alice'`).Scan(&alice); err != nil {
		t.Fatal(err)
	}
	env.store.SetRuntimeResetEffects(&fakeRuntimeEffects{})

	// 无 binding 的授权实例：dispatch 查不到 enabled 绑定 → (nil,nil) → 404。
	id, _, _, _ := w13gSeedAuthInstance(t, env, root, alice, "nb", "active", "rate_limited", false)
	outcome, err := env.store.ResetAccountRuntimeState(context.Background(), id, 1,
		AccessScope{ViewerID: alice})
	if err != nil {
		t.Fatalf("无绑定重置失败：%v", err)
	}
	if outcome != nil {
		t.Fatalf("无绑定的授权实例应 404：%+v", outcome.Result)
	}

	// 健康 active 授权实例：dispatch sets 为空 → unchanged（patchStatus/Schedulable
	// nil 臂），仅运行时面清理。
	id2, _, _, _ := w13gSeedAuthInstance(t, env, root, alice, "unchg", "active", "active", true)
	outcome, err = env.store.ResetAccountRuntimeState(context.Background(), id2, 1,
		AccessScope{ViewerID: alice})
	if err != nil || outcome == nil {
		t.Fatalf("unchanged 重置失败：%v", err)
	}
	if outcome.Result.Status != "active" {
		t.Fatalf("unchanged 状态：%+v", outcome.Result)
	}
	if strings.Contains(strings.Join(outcome.Result.Cleared, "|"), "account_persistent") {
		t.Fatalf("unchanged 不应清持久列：%v", outcome.Result.Cleared)
	}
}

func TestW13GRuntimeResetOwnerRuntimeFailureArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	// error 账户带全套待清理健康检查列（714/717/726/735 的 pending 清理臂）。
	env.seedAccount(t, "acc-w13g-hc", adminID, "w13g-hc", "active")
	env.exec(t, `UPDATE accounts SET status = 'error', last_error_code = 'up',
		last_error_message = 'm', next_health_check_at = ?, last_health_success_at = ?,
		last_health_check_status_code = 500, last_health_check_trace_id = 'tr-w13g' WHERE id = 'acc-w13g-hc'`,
		"2026-01-02T00:00:00Z", "2026-01-01T00:00:00Z")
	effects := &w13gEffects{fakeRuntimeEffects: &fakeRuntimeEffects{}}
	env.store.SetRuntimeResetEffects(effects)
	outcome, err := env.store.ResetAccountRuntimeState(context.Background(), "acc-w13g-hc", 1,
		AccessScope{ViewerID: adminID, IsAdmin: true})
	if err != nil || outcome == nil {
		t.Fatalf("健康列清理重置失败：%v", err)
	}
	for _, col := range []string{"next_health_check_at", "last_health_success_at", "last_health_check_status_code", "last_health_check_trace_id"} {
		var value sql.NullString
		if err := env.db.QueryRow(`SELECT ` + col + ` FROM accounts WHERE id = 'acc-w13g-hc'`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		if value.Valid {
			t.Fatalf("%s 应被清空", col)
		}
	}

	// rate_limited 恢复 active：patch 内 advanceBatchDispatchRevision 失败
	//（outbox 表缺失）→ 主流程 patch err 传播。
	env.seedAccount(t, "acc-w13g-adv", adminID, "w13g-adv", "active")
	env.exec(t, `UPDATE accounts SET status = 'rate_limited', last_error_code = 'r',
		last_error_message = 'm' WHERE id = 'acc-w13g-adv'`)
	env.exec(t, `DROP TABLE account_circuit_outbox`)
	if _, err := env.store.ResetAccountRuntimeState(context.Background(), "acc-w13g-adv", 1,
		AccessScope{ViewerID: adminID, IsAdmin: true}); err == nil {
		t.Fatal("outbox 缺失时 rate_limited 重置应失败")
	}
	env.exec(t, strings.Replace(schemaCircuitOutboxDDL, "CREATE TABLE IF NOT EXISTS", "CREATE TABLE IF NOT EXISTS", 1))
	if env.count(t, `SELECT COUNT(*) FROM account_circuit_outbox`) != 0 {
		t.Fatal("outbox 重建异常")
	}

	// 独立 fence 推进失败（error→pending_test 后运行时面清理触发 fence，但
	// outbox 在 Clear 回调时被 DROP）→ failed 含 dispatch_revision。
	env.seedAccount(t, "acc-w13g-fence", adminID, "w13g-fence", "active")
	env.exec(t, `UPDATE accounts SET status = 'error', last_error_code = 'e',
		last_error_message = 'm' WHERE id = 'acc-w13g-fence'`)
	effects2 := &w13gEffects{fakeRuntimeEffects: &fakeRuntimeEffects{}}
	effects2.onClear = func(e *w13gEffects) { e.onClear = nil }
	env.store.SetRuntimeResetEffects(effects2)
	// 第一次正常跑完（带 fence），随后的失败注入走独立场景。
	outcome, err = env.store.ResetAccountRuntimeState(context.Background(), "acc-w13g-fence", 1,
		AccessScope{ViewerID: adminID, IsAdmin: true})
	if err != nil || outcome == nil {
		t.Fatalf("fence 预置失败：%v", err)
	}
	env.seedAccount(t, "acc-w13g-fence2", adminID, "w13g-fence2", "active")
	env.exec(t, `UPDATE accounts SET status = 'error', last_error_code = 'e',
		last_error_message = 'm' WHERE id = 'acc-w13g-fence2'`)
	effects3 := &w13gEffects{fakeRuntimeEffects: &fakeRuntimeEffects{}}
	effects3.onClear = func(e *w13gEffects) {
		e.onClear = nil
		env.exec(t, `DROP TABLE account_circuit_outbox`)
	}
	env.store.SetRuntimeResetEffects(effects3)
	outcome, err = env.store.ResetAccountRuntimeState(context.Background(), "acc-w13g-fence2", 1,
		AccessScope{ViewerID: adminID, IsAdmin: true})
	if err != nil || outcome == nil {
		t.Fatalf("fence 失败重置异常：%v", err)
	}
	if !strings.Contains(strings.Join(outcome.Result.Failed, "|"), "dispatch_revision") {
		t.Fatalf("fence 失败应记 failed：%v", outcome.Result.Failed)
	}
	env.exec(t, schemaCircuitOutboxDDL)

	// 延迟退化清理端口失败 → failed 含 speed_first_latency。
	env.seedAccount(t, "acc-w13g-lat", adminID, "w13g-lat", "active")
	env.exec(t, `UPDATE accounts SET status = 'error', last_error_code = 'e',
		last_error_message = 'm' WHERE id = 'acc-w13g-lat'`)
	effects4 := &w13gEffects{fakeRuntimeEffects: &fakeRuntimeEffects{}}
	effects4.failLatency = true
	env.store.SetRuntimeResetEffects(effects4)
	outcome, err = env.store.ResetAccountRuntimeState(context.Background(), "acc-w13g-lat", 1,
		AccessScope{ViewerID: adminID, IsAdmin: true})
	if err != nil || outcome == nil {
		t.Fatalf("latency 失败重置异常：%v", err)
	}
	if !strings.Contains(strings.Join(outcome.Result.Failed, "|"), "speed_first_latency") {
		t.Fatalf("latency 失败应记 failed：%v", outcome.Result.Failed)
	}
}

func TestW13GRuntimeResetAPIKeySurfaces(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-w13g-ak1", adminID, "w13g-ak1", "active")
	env.exec(t, `UPDATE accounts SET status = 'error', last_error_code = 'e',
		last_error_message = 'm' WHERE id = 'acc-w13g-ak1'`)

	// Revalidate 端口失败 → failed 含 api_key_runtime。
	effects := &w13gEffects{fakeRuntimeEffects: &fakeRuntimeEffects{}}
	effects.failRevalidate = true
	env.store.SetRuntimeResetEffects(effects)
	outcome, err := env.store.ResetAccountRuntimeState(context.Background(), "acc-w13g-ak1", 1,
		AccessScope{ViewerID: adminID, IsAdmin: true})
	if err != nil || outcome == nil {
		t.Fatalf("revalidate 失败重置异常：%v", err)
	}
	if !strings.Contains(strings.Join(outcome.Result.Failed, "|"), "api_key_runtime") {
		t.Fatalf("revalidate 失败应记 failed：%v", outcome.Result.Failed)
	}

	// Revalidate eligible + changed → cleared 含 api_key_runtime 且计数透传。
	env.seedAccount(t, "acc-w13g-ak2", adminID, "w13g-ak2", "active")
	env.exec(t, `UPDATE accounts SET status = 'error', last_error_code = 'e',
		last_error_message = 'm' WHERE id = 'acc-w13g-ak2'`)
	effects2 := &w13gEffects{fakeRuntimeEffects: &fakeRuntimeEffects{}}
	effects2.fakeRuntimeEffects.revalidated = AccountAPIKeyRuntimeRevalidation{Eligible: true, Changed: 2}
	env.store.SetRuntimeResetEffects(effects2)
	outcome, err = env.store.ResetAccountRuntimeState(context.Background(), "acc-w13g-ak2", 1,
		AccessScope{ViewerID: adminID, IsAdmin: true})
	if err != nil || outcome == nil {
		t.Fatalf("revalidate 重置失败：%v", err)
	}
	if outcome.Result.APIKeyRuntimeRevalidated != 2 ||
		!strings.Contains(strings.Join(outcome.Result.Cleared, "|"), "api_key_runtime") {
		t.Fatalf("revalidated 计数：%+v", outcome.Result)
	}

	// LoadAPIKeyTransientStates 失败 → failed 含 api_key_transient。
	env.seedAccount(t, "acc-w13g-ak3", adminID, "w13g-ak3", "active")
	env.exec(t, `UPDATE accounts SET status = 'error', last_error_code = 'e',
		last_error_message = 'm' WHERE id = 'acc-w13g-ak3'`)
	effects3 := &w13gEffects{fakeRuntimeEffects: &fakeRuntimeEffects{}}
	effects3.failLoadTrans = true
	env.store.SetRuntimeResetEffects(effects3)
	outcome, err = env.store.ResetAccountRuntimeState(context.Background(), "acc-w13g-ak3", 1,
		AccessScope{ViewerID: adminID, IsAdmin: true})
	if err != nil || outcome == nil {
		t.Fatalf("transient 失败重置异常：%v", err)
	}
	if !strings.Contains(strings.Join(outcome.Result.Failed, "|"), "api_key_transient") {
		t.Fatalf("transient 失败应记 failed：%v", outcome.Result.Failed)
	}

	// ClearAPIKeyTransientFailure 失败 → 同样 failed 含 api_key_transient。
	env.seedAccount(t, "acc-w13g-ak4", adminID, "w13g-ak4", "active")
	env.exec(t, `UPDATE accounts SET status = 'error', last_error_code = 'e',
		last_error_message = 'm' WHERE id = 'acc-w13g-ak4'`)
	sealed, err := EncryptJSON(testSecret, Credentials{"api_key": "sk-w13g-ak4"})
	if err != nil {
		t.Fatal(err)
	}
	env.exec(t, `UPDATE accounts SET credentials_encrypted = ? WHERE id = 'acc-w13g-ak4'`, sealed)
	effects4 := &w13gEffects{fakeRuntimeEffects: &fakeRuntimeEffects{}}
	effects4.failClearTrans = true
	effects4.fakeRuntimeEffects.transientStates = []AccountAPIKeyTransientSelectionState{{
		KeyFingerprint:      fingerprintAccountAPIKey(testSecret, "sk-w13g-ak4"),
		TransientGeneration: 7, HasGeneration: true,
	}}
	env.store.SetRuntimeResetEffects(effects4)
	outcome, err = env.store.ResetAccountRuntimeState(context.Background(), "acc-w13g-ak4", 1,
		AccessScope{ViewerID: adminID, IsAdmin: true})
	if err != nil || outcome == nil {
		t.Fatalf("clear transient 失败重置异常：%v", err)
	}
	if !strings.Contains(strings.Join(outcome.Result.Failed, "|"), "api_key_transient") {
		t.Fatalf("clear transient 失败应记 failed：%v", outcome.Result.Failed)
	}

	// 多 key 池 + generation 命中 → transient 清理计数 >0 且 cleared 记录。
	env.seedAccount(t, "acc-w13g-ak5", adminID, "w13g-ak5", "active")
	env.exec(t, `UPDATE accounts SET status = 'error', last_error_code = 'e',
		last_error_message = 'm' WHERE id = 'acc-w13g-ak5'`)
	multi, err := EncryptJSON(testSecret, Credentials{"api_keys": []any{"sk-w13g-a", "sk-w13g-b"}})
	if err != nil {
		t.Fatal(err)
	}
	env.exec(t, `UPDATE accounts SET credentials_encrypted = ? WHERE id = 'acc-w13g-ak5'`, multi)
	effects5 := &w13gEffects{fakeRuntimeEffects: &fakeRuntimeEffects{}}
	effects5.fakeRuntimeEffects.transientStates = []AccountAPIKeyTransientSelectionState{{
		KeyFingerprint:      fingerprintAccountAPIKey(testSecret, "sk-w13g-a"),
		TransientGeneration: 3, HasGeneration: true,
	}}
	env.store.SetRuntimeResetEffects(effects5)
	outcome, err = env.store.ResetAccountRuntimeState(context.Background(), "acc-w13g-ak5", 1,
		AccessScope{ViewerID: adminID, IsAdmin: true})
	if err != nil || outcome == nil {
		t.Fatalf("transient 计数重置失败：%v", err)
	}
	if outcome.Result.APIKeyTransientCleared < 1 ||
		!strings.Contains(strings.Join(outcome.Result.Cleared, "|"), "api_key_transient") {
		t.Fatalf("transient 计数：%+v", outcome.Result)
	}
}

func TestW13GRuntimeResetSummaryDropArms(t *testing.T) {
	// Latency 回调里 DROP accounts → current summary 读取失败向上传播。
	t.Run("current-summary", func(t *testing.T) {
		env := newTestEnv(t)
		adminID := env.login(t, "root", "root-pass", "super_admin")
		env.seedProviderAndDefaultGroup(t, adminID)
		env.seedAccount(t, "acc-w13g-sd1", adminID, "w13g-sd1", "active")
		env.exec(t, `UPDATE accounts SET status = 'error', last_error_code = 'e',
			last_error_message = 'm' WHERE id = 'acc-w13g-sd1'`)
		effects := &w13gEffects{fakeRuntimeEffects: &fakeRuntimeEffects{}}
		effects.onLatency = func(e *w13gEffects) {
			e.onLatency = nil
			env.exec(t, `DROP TABLE accounts`)
		}
		env.store.SetRuntimeResetEffects(effects)
		if _, err := env.store.ResetAccountRuntimeState(context.Background(), "acc-w13g-sd1", 1,
			AccessScope{ViewerID: adminID, IsAdmin: true}); err == nil {
			t.Fatal("current summary 读失败应报错")
		}
	})
	// Revalidate 回调里 DROP accounts → final summary 读取失败向上传播。
	t.Run("final-summary", func(t *testing.T) {
		env := newTestEnv(t)
		adminID := env.login(t, "root", "root-pass", "super_admin")
		env.seedProviderAndDefaultGroup(t, adminID)
		env.seedAccount(t, "acc-w13g-sd2", adminID, "w13g-sd2", "active")
		env.exec(t, `UPDATE accounts SET status = 'error', last_error_code = 'e',
			last_error_message = 'm' WHERE id = 'acc-w13g-sd2'`)
		effects := &w13gEffects{fakeRuntimeEffects: &fakeRuntimeEffects{}}
		effects.onRevalidate = func(e *w13gEffects) {
			e.onRevalidate = nil
			env.exec(t, `DROP TABLE accounts`)
		}
		env.store.SetRuntimeResetEffects(effects)
		if _, err := env.store.ResetAccountRuntimeState(context.Background(), "acc-w13g-sd2", 1,
			AccessScope{ViewerID: adminID, IsAdmin: true}); err == nil {
			t.Fatal("final summary 读失败应报错")
		}
	})
	// Revalidate 回调里 DROP lock 表 → final lock 读失败 → failed 含 lock_state。
	t.Run("final-lock", func(t *testing.T) {
		env := newTestEnv(t)
		adminID := env.login(t, "root", "root-pass", "super_admin")
		env.seedProviderAndDefaultGroup(t, adminID)
		env.seedAccount(t, "acc-w13g-sd3", adminID, "w13g-sd3", "active")
		env.exec(t, `UPDATE accounts SET status = 'error', last_error_code = 'e',
			last_error_message = 'm' WHERE id = 'acc-w13g-sd3'`)
		effects := &w13gEffects{fakeRuntimeEffects: &fakeRuntimeEffects{}}
		effects.onRevalidate = func(e *w13gEffects) {
			e.onRevalidate = nil
			env.exec(t, `DROP TABLE account_lock_states`)
		}
		env.store.SetRuntimeResetEffects(effects)
		outcome, err := env.store.ResetAccountRuntimeState(context.Background(), "acc-w13g-sd3", 1,
			AccessScope{ViewerID: adminID, IsAdmin: true})
		if err != nil || outcome == nil {
			t.Fatalf("final lock 失败重置异常：%v", err)
		}
		if !strings.Contains(strings.Join(outcome.Result.Failed, "|"), "lock_state") {
			t.Fatalf("final lock 失败应记 failed：%v", outcome.Result.Failed)
		}
		if !strings.Contains(strings.Join(outcome.Result.Skipped, "|"), "lock_state") {
			t.Fatalf("final lock 失败应跳过：%v", outcome.Result.Skipped)
		}
	})
}

func TestW13GPatchAccountFailureStateDirectArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-w13g-p1", adminID, "w13g-p1", "active")
	env.exec(t, `UPDATE accounts SET status = 'error', last_error_code = 'e',
		last_error_message = 'm' WHERE id = 'acc-w13g-p1'`)
	base := patchFailureStateInput{
		AccountID:              "acc-w13g-p1",
		ExpectedConfigRevision: 1,
		Access:                 AccessScope{ViewerID: adminID, IsAdmin: true},
		Now:                    time.Now().UTC(),
	}

	// scope 过滤命中不了行 → (nil,nil)。
	scoped := base
	scoped.Access = AccessScope{ViewerID: "someone-else"}
	if out, err := env.store.patchAccountFailureStateForReset(context.Background(), scoped); err != nil || out != nil {
		t.Fatalf("scope 不匹配应返回 nil：%v %v", out, err)
	}

	// pending_test 无健康检查失败 → 拒绝错误。
	env.seedAccount(t, "acc-w13g-p2", adminID, "w13g-p2", "active")
	env.exec(t, `UPDATE accounts SET status = 'pending_test' WHERE id = 'acc-w13g-p2'`)
	pending := base
	pending.AccountID = "acc-w13g-p2"
	if _, err := env.store.patchAccountFailureStateForReset(context.Background(), pending); err == nil ||
		err.Error() != "账户正在等待首次后台健康检查，无需重新检查" {
		t.Fatalf("fresh pending 拒绝：%v", err)
	}

	// disabled 且未过期 → unchanged。
	env.seedAccount(t, "acc-w13g-p3", adminID, "w13g-p3", "active")
	env.exec(t, `UPDATE accounts SET status = 'disabled' WHERE id = 'acc-w13g-p3'`)
	disabled := base
	disabled.AccountID = "acc-w13g-p3"
	out, err := env.store.patchAccountFailureStateForReset(context.Background(), disabled)
	if err != nil || out == nil || len(out.ChangedFields) != 0 || out.Status != "disabled" {
		t.Fatalf("disabled unchanged：%v %v", out, err)
	}

	// 套餐过期 → 恢复 disabled + account_expired 文案。
	env.seedAccount(t, "acc-w13g-p4", adminID, "w13g-p4", "active")
	env.exec(t, `UPDATE accounts SET status = 'rate_limited', last_error_code = 'r',
		last_error_message = 'm', account_expires_at = ? WHERE id = 'acc-w13g-p4'`, "2020-01-01T00:00:00Z")
	expired := base
	expired.AccountID = "acc-w13g-p4"
	out, err = env.store.patchAccountFailureStateForReset(context.Background(), expired)
	if err != nil || out == nil || out.Status != "disabled" {
		t.Fatalf("expired 恢复：%v %v", out, err)
	}
	var code, message string
	var schedulable int
	if err := env.db.QueryRow(`SELECT COALESCE(last_error_code,''), COALESCE(last_error_message,''), schedulable
		FROM accounts WHERE id = 'acc-w13g-p4'`).Scan(&code, &message, &schedulable); err != nil {
		t.Fatal(err)
	}
	if code != "account_expired" || message != "账户套餐已过期，已自动停用" || schedulable != 0 {
		t.Fatalf("expired 行状态：%s %s %d", code, message, schedulable)
	}

	// active 无失败列 → unchanged。
	env.seedAccount(t, "acc-w13g-p5", adminID, "w13g-p5", "active")
	fresh := base
	fresh.AccountID = "acc-w13g-p5"
	out, err = env.store.patchAccountFailureStateForReset(context.Background(), fresh)
	if err != nil || out == nil || len(out.ChangedFields) != 0 {
		t.Fatalf("active unchanged：%v %v", out, err)
	}

	// lock ENGAGED → unchanged。
	env.seedAccount(t, "acc-w13g-p6", adminID, "w13g-p6", "active")
	env.exec(t, `UPDATE accounts SET status = 'error', last_error_code = 'e',
		last_error_message = 'm' WHERE id = 'acc-w13g-p6'`)
	env.exec(t, `INSERT INTO account_lock_states (account_id, enabled, lock_state, updated_at)
		VALUES ('acc-w13g-p6', 1, 'DEAD_CONFIRMED', ?)`, time.Now().UTC().Format(time.RFC3339Nano))
	locked := base
	locked.AccountID = "acc-w13g-p6"
	out, err = env.store.patchAccountFailureStateForReset(context.Background(), locked)
	if err != nil || out == nil || len(out.ChangedFields) != 0 || out.Status != "error" {
		t.Fatalf("locked unchanged：%v %v", out, err)
	}

	// lock 表缺失 → 事务内读取失败。
	env.exec(t, `DROP TABLE account_lock_states`)
	if _, err := env.store.patchAccountFailureStateForReset(context.Background(), base); err == nil {
		t.Fatal("lock 表缺失应报错")
	}

	// 授权 stamp 行 → (nil,nil)；accounts 表缺失 → SELECT 失败。
	t.Run("stamp-and-drop", func(t *testing.T) {
		env2 := newTestEnv(t)
		root := env2.login(t, "root", "root-pass", "super_admin")
		env2.seedProviderAndDefaultGroup(t, root)
		env2.seedAccount(t, "acc-w13g-p7", root, "w13g-p7", "active")
		env2.exec(t, `UPDATE accounts SET authorization_instance_authorization_id = 'ra-x',
			authorization_instance_source_account_id = 'acc-w13g-src7' WHERE id = 'acc-w13g-p7'`)
		stamped := patchFailureStateInput{AccountID: "acc-w13g-p7", ExpectedConfigRevision: 1,
			Access: AccessScope{ViewerID: root, IsAdmin: true}, Now: time.Now().UTC()}
		if out, err := env2.store.patchAccountFailureStateForReset(context.Background(), stamped); err != nil || out != nil {
			t.Fatalf("stamp 行应返回 nil：%v %v", out, err)
		}
		env2.exec(t, `DROP TABLE accounts`)
		if _, err := env2.store.patchAccountFailureStateForReset(context.Background(), stamped); err == nil {
			t.Fatal("accounts 表缺失应报错")
		}
	})

	// db 关闭 → BeginTx 失败。
	t.Run("closed-db", func(t *testing.T) {
		env3 := newTestEnv(t)
		admin3 := env3.login(t, "root", "root-pass", "super_admin")
		env3.seedProviderAndDefaultGroup(t, admin3)
		env3.seedAccount(t, "acc-w13g-p8", admin3, "w13g-p8", "active")
		closed := patchFailureStateInput{AccountID: "acc-w13g-p8", ExpectedConfigRevision: 1,
			Access: AccessScope{ViewerID: admin3, IsAdmin: true}, Now: time.Now().UTC()}
		env3.db.Close()
		if _, err := env3.store.patchAccountFailureStateForReset(context.Background(), closed); err == nil {
			t.Fatal("db 关闭 BeginTx 应报错")
		}
	})
}

func TestW13GAuthorizedDispatchDirectArms(t *testing.T) {
	env := newTestEnv(t)
	root := env.login(t, "root", "root-pass", "super_admin")
	if err := env.db.QueryRow(`SELECT id FROM system_accounts WHERE username = 'root'`).Scan(&root); err != nil {
		t.Fatal(err)
	}
	env.login(t, "alice", "alice-pass", "user")
	alice := ""
	if err := env.db.QueryRow(`SELECT id FROM system_accounts WHERE username = 'alice'`).Scan(&alice); err != nil {
		t.Fatal(err)
	}
	env.store.SetRuntimeResetEffects(&fakeRuntimeEffects{})
	base := updateAuthorizedBindingDispatchInput{ExpectedConfigRevision: 1, Access: AccessScope{ViewerID: alice}}

	// revision < 1 → ValidationError。
	bad := base
	bad.AccountID = "acc-w13g-any"
	bad.ExpectedConfigRevision = 0
	if _, err := env.store.updateAuthorizedBindingDispatchForReset(context.Background(), bad); err == nil {
		t.Fatal("revision<1 应报错")
	}

	// 无 scope 且非管理员 → (nil,nil)。
	noScope := base
	noScope.AccountID = "acc-w13g-any"
	noScope.Access = AccessScope{}
	if out, err := env.store.updateAuthorizedBindingDispatchForReset(context.Background(), noScope); err != nil || out != nil {
		t.Fatalf("无 scope 应返回 nil：%v %v", out, err)
	}

	// 不存在的实例 → (nil,nil)。
	missing := base
	missing.AccountID = "acc-w13g-missing"
	if out, err := env.store.updateAuthorizedBindingDispatchForReset(context.Background(), missing); err != nil || out != nil {
		t.Fatalf("缺失实例应返回 nil：%v %v", out, err)
	}

	// pending_test 实例 → 健康检查门拒绝。
	id, _, _, _ := w13gSeedAuthInstance(t, env, root, alice, "pnd", "active", "pending_test", true)
	pending := base
	pending.AccountID = id
	if _, err := env.store.updateAuthorizedBindingDispatchForReset(context.Background(), pending); err == nil ||
		err.Error() != "待检查账户需等待后台健康检查通过后才能参与调度" {
		t.Fatalf("pending 拒绝：%v", err)
	}

	// revision 冲突。
	id2, _, _, _ := w13gSeedAuthInstance(t, env, root, alice, "rev", "active", "rate_limited", true)
	conflict := base
	conflict.AccountID = id2
	conflict.ExpectedConfigRevision = 99
	if _, err := env.store.updateAuthorizedBindingDispatchForReset(context.Background(), conflict); err == nil {
		var conflictErr *RevisionConflictError
		if !errors.As(err, &conflictErr) {
			// 误报分支保护：err 为 nil 时才会到这里。
			_ = conflictErr
		}
		t.Fatal("revision 冲突应报错")
	}

	// revoked 授权 → (nil,nil)。
	id3, _, authID3, _ := w13gSeedAuthInstance(t, env, root, alice, "rvk", "active", "rate_limited", true)
	env.exec(t, `UPDATE resource_authorizations SET status = 'revoked' WHERE id = ?`, authID3)
	revoked := base
	revoked.AccountID = id3
	if out, err := env.store.updateAuthorizedBindingDispatchForReset(context.Background(), revoked); err != nil || out != nil {
		t.Fatalf("revoked 应返回 nil：%v %v", out, err)
	}

	// 源账户 disabled → authorizedResetUnavailableMessage 拒绝。
	id4, source4, _, _ := w13gSeedAuthInstance(t, env, root, alice, "srcdis", "active", "rate_limited", true)
	env.exec(t, `UPDATE accounts SET status = 'disabled' WHERE id = ?`, source4)
	srcDisabled := base
	srcDisabled.AccountID = id4
	if _, err := env.store.updateAuthorizedBindingDispatchForReset(context.Background(), srcDisabled); err == nil ||
		err.Error() != "授权方原账户已停用，当前账户不能调用" {
		t.Fatalf("源停用拒绝：%v", err)
	}

	// 源账户缺失(软删除) → 拒绝。
	id5, source5, _, _ := w13gSeedAuthInstance(t, env, root, alice, "srcdel", "active", "rate_limited", true)
	env.exec(t, `UPDATE accounts SET deleted_at = ? WHERE id = ?`, time.Now().UTC().Format(time.RFC3339Nano), source5)
	srcGone := base
	srcGone.AccountID = id5
	if _, err := env.store.updateAuthorizedBindingDispatchForReset(context.Background(), srcGone); err == nil ||
		!strings.Contains(err.Error(), "授权方原账户不存在") {
		t.Fatalf("源缺失拒绝：%v", err)
	}

	// lock ENGAGED → unchanged（不落任何写）。
	id6, _, _, _ := w13gSeedAuthInstance(t, env, root, alice, "lck", "active", "rate_limited", true)
	env.exec(t, `INSERT INTO account_lock_states (account_id, enabled, lock_state, updated_at)
		VALUES (?, 1, 'ENGAGED', ?)`, id6, time.Now().UTC().Format(time.RFC3339Nano))
	locked := base
	locked.AccountID = id6
	out, err := env.store.updateAuthorizedBindingDispatchForReset(context.Background(), locked)
	if err != nil || out == nil || len(out.ChangedFields) != 0 {
		t.Fatalf("locked unchanged：%v %v", out, err)
	}

	// 全套失败列清理（clearAuthorizedFailureStateColumnsForReset 各列）。
	id7, _, _, _ := w13gSeedAuthInstance(t, env, root, alice, "cols", "active", "rate_limited", true)
	env.exec(t, `UPDATE accounts SET cooldown_until = ?, last_error_code = 'e', last_error_message = 'm',
		last_error_trace_id = 'tr', cooldown_retest_failure_count = 2, cooldown_retest_observation_started_at = ?,
		cooldown_retest_generation = 'g', cooldown_retest_last_at = ?, cooldown_retest_last_status_code = 429,
		stream_failure_count = 1, stream_failure_window_started_at = ? WHERE id = ?`,
		"2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", id7)
	cols := base
	cols.AccountID = id7
	out, err = env.store.updateAuthorizedBindingDispatchForReset(context.Background(), cols)
	if err != nil || out == nil || len(out.ChangedFields) == 0 || out.ConfigRevision != 2 {
		t.Fatalf("失败列清理：%v %v", out, err)
	}
	var count int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM accounts WHERE id = ? AND cooldown_until IS NULL
		AND last_error_code IS NULL AND last_error_trace_id IS NULL AND cooldown_retest_failure_count = 0
		AND cooldown_retest_generation IS NULL AND cooldown_retest_last_status_code IS NULL
		AND stream_failure_count = 0 AND stream_failure_window_started_at IS NULL`, id7).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("授权失败列应全部清空")
	}

	// outbox 缺失：UPDATE 后 advanceBatchDispatchRevision 失败。
	id8, _, _, _ := w13gSeedAuthInstance(t, env, root, alice, "adv8", "active", "rate_limited", true)
	env.exec(t, `DROP TABLE account_circuit_outbox`)
	adv := base
	adv.AccountID = id8
	if _, err := env.store.updateAuthorizedBindingDispatchForReset(context.Background(), adv); err == nil {
		t.Fatal("outbox 缺失应报错")
	}
	env.exec(t, schemaCircuitOutboxDDL)

	// 空 source stamp（resource_id=''）→ (nil,nil)。
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id,
		grantee_system_account_id, scope, status, effective_source_type, created_by, created_at, updated_at)
		VALUES ('ra-w13g-empty', 'account', '', ?, ?, 'use', 'active', 'manual', ?, ?, ?)`,
		root, alice, root, now, now)
	sealed, err := EncryptJSON(testSecret, Credentials{"api_key": "sk-w13g-empty"})
	if err != nil {
		t.Fatal(err)
	}
	env.exec(t, `INSERT INTO accounts (id, config_revision, dispatch_revision, system_account_id, provider_code,
		provider_protocol_profile_id, protocol_code, protocol_version, name, type, status, credentials_encrypted,
		credential_mask, health_check_model, health_check_endpoint_mode, created_at, updated_at,
		authorization_instance_authorization_id, authorization_instance_source_account_id)
		VALUES ('acc-w13g-inst-empty', 1, 1, ?, 'gpt', 'prof-gpt', 'openai', 'v1', 'w13g-empty', 'api_key',
		'rate_limited', ?, '', 'gpt-4o-mini', 'chat_json', ?, ?, 'ra-w13g-empty', '')`,
		alice, sealed, now, now)
	empty := base
	empty.AccountID = "acc-w13g-inst-empty"
	if out, err := env.store.updateAuthorizedBindingDispatchForReset(context.Background(), empty); err != nil || out != nil {
		t.Fatalf("空 stamp 应返回 nil：%v %v", out, err)
	}

	// group_accounts 缺失 → binding 查询失败。
	env.exec(t, `DROP TABLE group_accounts`)
	gb := base
	gb.AccountID = id7
	if _, err := env.store.updateAuthorizedBindingDispatchForReset(context.Background(), gb); err == nil {
		t.Fatal("group_accounts 缺失应报错")
	}

	// lock 表缺失 → lock 读取失败。
	t.Run("lock-drop", func(t *testing.T) {
		env2 := newTestEnv(t)
		root2 := env2.login(t, "root", "root-pass", "super_admin")
		env2.login(t, "alice", "alice-pass", "user")
		alice2 := ""
		if err := env2.db.QueryRow(`SELECT id FROM system_accounts WHERE username = 'alice'`).Scan(&alice2); err != nil {
			t.Fatal(err)
		}
		id9, _, _, _ := w13gSeedAuthInstance(t, env2, root2, alice2, "lk9", "active", "rate_limited", true)
		env2.exec(t, `DROP TABLE account_lock_states`)
		lk := updateAuthorizedBindingDispatchInput{ExpectedConfigRevision: 1, Access: AccessScope{ViewerID: alice2}, AccountID: id9}
		if _, err := env2.store.updateAuthorizedBindingDispatchForReset(context.Background(), lk); err == nil {
			t.Fatal("lock 表缺失应报错")
		}
	})

	// accounts 表缺失 → join 失败。
	t.Run("accounts-drop", func(t *testing.T) {
		env3 := newTestEnv(t)
		root3 := env3.login(t, "root", "root-pass", "super_admin")
		env3.exec(t, `DROP TABLE accounts`)
		drop := updateAuthorizedBindingDispatchInput{ExpectedConfigRevision: 1,
			Access: AccessScope{ViewerID: root3, IsAdmin: true}, AccountID: "acc-w13g-any"}
		if _, err := env3.store.updateAuthorizedBindingDispatchForReset(context.Background(), drop); err == nil {
			t.Fatal("accounts 表缺失应报错")
		}
	})

	// db 关闭 → BeginTx 失败。
	t.Run("closed-db", func(t *testing.T) {
		env4 := newTestEnv(t)
		root4 := env4.login(t, "root", "root-pass", "super_admin")
		env4.db.Close()
		closed := updateAuthorizedBindingDispatchInput{ExpectedConfigRevision: 1,
			Access: AccessScope{ViewerID: root4, IsAdmin: true}, AccountID: "acc-w13g-any"}
		if _, err := env4.store.updateAuthorizedBindingDispatchForReset(context.Background(), closed); err == nil {
			t.Fatal("db 关闭 BeginTx 应报错")
		}
	})
}

func TestW13GAdvanceResetDispatchRevision(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-w13g-f1", adminID, "w13g-f1", "active")

	// 正常推进 → applied。
	fence, err := env.store.advanceResetDispatchRevision(context.Background(), "acc-w13g-f1", "dispatch_w13g_t1", 1)
	if err != nil || fence.Status != "applied" || fence.DispatchRevision != 2 {
		t.Fatalf("fence applied：%+v %v", fence, err)
	}
	// 同 transitionID 重放 → idempotent。
	fence, err = env.store.advanceResetDispatchRevision(context.Background(), "acc-w13g-f1", "dispatch_w13g_t1", 1)
	if err != nil || fence.Status != "idempotent" || fence.DispatchRevision != 2 {
		t.Fatalf("fence idempotent：%+v %v", fence, err)
	}
	// 篡改既有 dedupe 事件身份（合法枚举内换 event_type）→ 冲突。
	env.exec(t, `UPDATE account_circuit_outbox SET event_type = 'incident_changed' WHERE dedupe_key = 'dispatch:dispatch_w13g_t1'`)
	if _, err := env.store.advanceResetDispatchRevision(context.Background(), "acc-w13g-f1", "dispatch_w13g_t1", 1); err == nil ||
		!strings.Contains(err.Error(), "dedupe key 与既有事件身份冲突") {
		t.Fatalf("身份冲突：%v", err)
	}
	// 不存在的账户 → 明确错误。
	if _, err := env.store.advanceResetDispatchRevision(context.Background(), "acc-w13g-none", "dispatch_w13g_t2", 1); err == nil ||
		!strings.Contains(err.Error(), "AI 账户不存在") {
		t.Fatalf("账户不存在：%v", err)
	}
	// accounts 表缺失 → SELECT 失败。
	env.exec(t, `DROP TABLE accounts`)
	if _, err := env.store.advanceResetDispatchRevision(context.Background(), "acc-w13g-f1", "dispatch_w13g_t3", 1); err == nil {
		t.Fatal("accounts 表缺失应报错")
	}

	// outbox 表缺失 → dedupe SELECT 失败。
	t.Run("outbox-drop", func(t *testing.T) {
		env2 := newTestEnv(t)
		admin2 := env2.login(t, "root", "root-pass", "super_admin")
		env2.seedProviderAndDefaultGroup(t, admin2)
		env2.seedAccount(t, "acc-w13g-f2", admin2, "w13g-f2", "active")
		env2.exec(t, `DROP TABLE account_circuit_outbox`)
		if _, err := env2.store.advanceResetDispatchRevision(context.Background(), "acc-w13g-f2", "dispatch_w13g_t4", 1); err == nil {
			t.Fatal("outbox 表缺失应报错")
		}
	})

	// 重建 outbox 带 status CHECK 约束 → INSERT 失败。
	t.Run("strict-outbox", func(t *testing.T) {
		env3 := newTestEnv(t)
		admin3 := env3.login(t, "root", "root-pass", "super_admin")
		env3.seedProviderAndDefaultGroup(t, admin3)
		env3.seedAccount(t, "acc-w13g-f3", admin3, "w13g-f3", "active")
		env3.exec(t, `DROP TABLE account_circuit_outbox`)
		env3.exec(t, w13gStrictOutboxDDL)
		if _, err := env3.store.advanceResetDispatchRevision(context.Background(), "acc-w13g-f3", "dispatch_w13g_t5", 1); err == nil {
			t.Fatal("约束 outbox 应插入失败")
		}
	})

	// db 关闭 → BeginTx 失败。
	t.Run("closed-db", func(t *testing.T) {
		env4 := newTestEnv(t)
		env4.login(t, "root", "root-pass", "super_admin")
		env4.db.Close()
		if _, err := env4.store.advanceResetDispatchRevision(context.Background(), "acc-w13g-f4", "dispatch_w13g_t6", 1); err == nil {
			t.Fatal("db 关闭 BeginTx 应报错")
		}
	})
}

func TestW13GResetPureHelpers(t *testing.T) {
	// accountAPIKeyEntries：非字符串 key 跳过、空白裁剪、去重。
	entries := accountAPIKeyEntries("sec", Credentials{"api_keys": []any{float64(1), "  sk-w13g-x  ", "sk-w13g-x"}})
	if len(entries) != 1 {
		t.Fatalf("key 过滤与去重：%v", entries)
	}
	if fingerprintAccountAPIKey("sec", "sk-w13g-x") != entries[0].Fingerprint {
		t.Fatalf("fingerprint：%v", entries)
	}
	// 无 api_keys 时回落单 key。
	entries = accountAPIKeyEntries("sec", Credentials{"api_key": " sk-single "})
	if len(entries) != 1 || fingerprintAccountAPIKey("sec", "sk-single") != entries[0].Fingerprint {
		t.Fatalf("单 key 回落：%v", entries)
	}

	// trimSpaces 尾部空白。
	if got := trimSpaces("a\t \n"); got != "a" {
		t.Fatalf("尾部裁剪：%q", got)
	}

	// clearResetAPIKeyTransientStates：load 失败传播。
	fx := &fakeRuntimeEffects{}
	env := newTestEnv(t)
	env.store.SetRuntimeResetEffects(fx)
	if _, err := env.store.clearResetAPIKeyTransientStates(context.Background(), w13gLoadFailEffects{fakeRuntimeEffects: &fakeRuntimeEffects{}},
		"acc-w13g-x", Credentials{"api_key": "sk-w13g-x"}); err == nil {
		t.Fatal("load 失败应传播")
	}
}

// w13gLoadFailEffects 只覆写 LoadAPIKeyTransientStates 的失败。
type w13gLoadFailEffects struct {
	*fakeRuntimeEffects
}

func (w13gLoadFailEffects) LoadAPIKeyTransientStates(context.Context, string, []string) ([]AccountAPIKeyTransientSelectionState, error) {
	return nil, context.DeadlineExceeded
}

func TestW13GFindResetSummaryScopes(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-w13g-sum1", adminID, "w13g-sum1", "active")

	// 空 ID → nil。
	if out, err := env.store.findResetSummary(context.Background(), "  ", AccessScope{ViewerID: adminID}); err != nil || out != nil {
		t.Fatalf("空 ID：%v %v", out, err)
	}
	// 无权限 scope → nil。
	if out, err := env.store.findResetSummary(context.Background(), "acc-w13g-sum1", AccessScope{}); err != nil || out != nil {
		t.Fatalf("无权限：%v %v", out, err)
	}
	// 不存在 → owner nil 再走 authorized 查询也 nil。
	if out, err := env.store.findResetSummary(context.Background(), "acc-w13g-missing", AccessScope{ViewerID: adminID, IsAdmin: true}); err != nil || out != nil {
		t.Fatalf("不存在：%v %v", out, err)
	}
	// owner 读取成功。
	out, err := env.store.findResetSummary(context.Background(), "acc-w13g-sum1", AccessScope{ViewerID: adminID, IsAdmin: true})
	if err != nil || out == nil || out.AccessType != "owner" {
		t.Fatalf("owner 读取：%v %v", out, err)
	}
	// 损坏的 sealed 凭据 → credentials 回落空 map。
	env.seedAccount(t, "acc-w13g-sum2", adminID, "w13g-sum2", "active")
	env.exec(t, `UPDATE accounts SET credentials_encrypted = 'not-sealed' WHERE id = 'acc-w13g-sum2'`)
	out, err = env.store.findResetSummary(context.Background(), "acc-w13g-sum2", AccessScope{ViewerID: adminID, IsAdmin: true})
	if err != nil || out == nil || out.Credentials == nil || len(out.Credentials) != 0 {
		t.Fatalf("坏凭据回落：%v %v", out, err)
	}
	// accounts 表缺失 → owner 查询失败。
	env.exec(t, `DROP TABLE accounts`)
	if _, err := env.store.findResetSummary(context.Background(), "acc-w13g-sum1", AccessScope{ViewerID: adminID, IsAdmin: true}); err == nil {
		t.Fatal("accounts 表缺失应报错")
	}
}
