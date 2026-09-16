package accountkeystates

import (
	"context"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// w9d（二）：store 方言臂、summary/details/states 读面分支、revalidate 门禁
// 推理与纯函数臂。
// 不可达登记：
//   - RevalidatePool changed==0 后 gateReason 逆转 / retried>0（revalidate.go
//     96-106）：gate 读与 UPDATE 之间存在并发窗口，单线程测试无法注入。
//   - LoadSelectionStatesByAccountIds 的 rows.Err（states.go 84-87）与
//     ClaimDueForProbe 的 rows.Err（probe.go）：本地 sqlite 驱动无迭代故障面。
//   - RecordFailure 的 PG upsert 分支（probe.go 511-553）：需要真实 PG 方言。
// ---------------------------------------------------------------------------

func TestW9DStoreDialectArms(t *testing.T) {
	if _, err := NewStore(Config{Secret: testSecret}); err == nil {
		t.Fatal("nil db must fail")
	}
	if _, err := NewStore(Config{DB: openTestDB(t).db}); err == nil {
		t.Fatal("empty secret must fail")
	}
	pg := &Store{postgres: true}
	if got := pg.businessTable("accounts"); got != "juhe_business.accounts" {
		t.Fatalf("pg table=%q", got)
	}
	if got := pg.bind("a = ? AND b = ?"); got != "a = $1 AND b = $2" {
		t.Fatalf("pg bind=%q", got)
	}
	plain := &Store{}
	if got := plain.bind("a = ?"); got != "a = ?" {
		t.Fatalf("sqlite bind=%q", got)
	}
	if _, ok := pg.timeParam(time.UnixMilli(1_000)).(time.Time); !ok {
		t.Fatal("pg timeParam must be native time")
	}
	if _, ok := plain.timeParam(time.UnixMilli(1_000)).(string); !ok {
		t.Fatal("sqlite timeParam must be text")
	}
	if _, ok := pg.instantParam(nowMillisText()).(time.Time); !ok {
		t.Fatal("pg instantParam must parse")
	}
	if _, ok := pg.instantParam("junk").(string); !ok {
		t.Fatal("pg instantParam fallback must keep text")
	}
	if chunkValues([]string{"a"}, 900) == nil {
		t.Fatal("single id chunk must not be nil")
	}
	if ids := normalizeAccountIds([]string{" a ", "", "a", "b"}); len(ids) != 2 || ids[0] != "a" {
		t.Fatalf("normalize=%v", ids)
	}
	if _, err := instantMS("junk"); err == nil {
		t.Fatal("junk instant must fail")
	}
}

func TestW9DStatesReaderArms(t *testing.T) {
	ctx := context.Background()
	h := openTestDB(t)
	h.seedSchema(t)
	var fps []string
	h.seedAccount(t, struct {
		id           string
		keyCount     int
		status       string
		schedulable  int
		configRev    int64
		providerCode string
		protocolCode string
		protocolVer  string
		fingerprints *[]string
	}{id: "acc", keyCount: 2, status: "active", schedulable: 1, configRev: 1,
		providerCode: "openai", protocolCode: "openai", protocolVer: "v1", fingerprints: &fps})
	h.seedState(t, "acc", fps[0], map[string]any{"status": "rate_limited", "next_probe_at": plusMillis(-1_000)})
	// 空归一化入参。
	if states, err := h.store.LoadSelectionStatesByAccountIds(ctx, nil); err != nil || len(states) != 0 {
		t.Fatalf("empty ids=%v err=%v", states, err)
	}
	if states, err := h.store.LoadSelectionStatesForAccount(ctx, ""); err != nil || len(states) != 0 {
		t.Fatalf("empty account states=%v err=%v", states, err)
	}
	// scanSelectionState 的 scan 失败臂（单测注入）。
	if _, err := scanSelectionState(func(dest ...any) error { return context.DeadlineExceeded }); err == nil {
		t.Fatal("injected scan failure must propagate")
	}
	// 循环内 scan err：key_index 存非整数文本。
	h.exec(t, `UPDATE account_api_key_runtime_states SET key_index='x' WHERE account_id='acc'`)
	if _, err := h.store.LoadSelectionStatesByAccountIds(ctx, []string{"acc"}); err == nil {
		t.Fatal("bad key_index batch scan must fail")
	}
	if _, err := h.store.LoadSelectionStatesForAccount(ctx, "acc"); err == nil {
		t.Fatal("bad key_index single scan must fail")
	}
	// 查询 err：drop 表。
	if _, err := h.db.Exec(`DROP TABLE account_api_key_runtime_states`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.LoadSelectionStatesByAccountIds(ctx, []string{"acc"}); err == nil {
		t.Fatal("missing states relation batch must fail")
	}
	if _, err := h.store.LoadSelectionStatesForAccount(ctx, "acc"); err == nil {
		t.Fatal("missing states relation single must fail")
	}
}

func TestW9DSummaryArms(t *testing.T) {
	ctx := context.Background()
	h := openTestDB(t)
	h.seedSchema(t)
	var fps []string
	h.seedAccount(t, struct {
		id           string
		keyCount     int
		status       string
		schedulable  int
		configRev    int64
		providerCode string
		protocolCode string
		protocolVer  string
		fingerprints *[]string
	}{id: "acc", keyCount: 2, status: "active", schedulable: 1, configRev: 1,
		providerCode: "openai", protocolCode: "openai", protocolVer: "v1", fingerprints: &fps})
	h.seedState(t, "acc", fps[0], map[string]any{"status": "rate_limited", "next_probe_at": plusMillis(-1_000), "last_failure_at": plusMillis(-500)})
	// 空 ids。
	if out, err := h.store.LoadSummariesByAccountIds(ctx, nil); err != nil || len(out) != 0 {
		t.Fatalf("empty ids=%v err=%v", out, err)
	}
	// source rows err。
	if _, err := h.db.Exec(`DROP TABLE accounts`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.LoadSummariesByAccountIds(ctx, []string{"acc"}); err == nil {
		t.Fatal("missing accounts relation must fail")
	}
	if _, err := h.store.LoadAPIKeyRuntimeDetails(ctx, "acc"); err == nil {
		t.Fatal("details missing accounts relation must fail")
	}
	db := h.db
	db.Exec(`CREATE TABLE accounts (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL DEFAULT '', type TEXT NOT NULL DEFAULT 'api_key', status TEXT NOT NULL DEFAULT 'active', schedulable INTEGER NOT NULL DEFAULT 1, provider_code TEXT NOT NULL DEFAULT 'openai', protocol_code TEXT, protocol_version TEXT, config_revision INTEGER NOT NULL DEFAULT 1, account_expires_at TEXT, credentials_encrypted TEXT NOT NULL DEFAULT '{}', authorization_instance_source_account_id TEXT, deleted_at TEXT)`)
	db.Exec(`INSERT INTO accounts (id, system_account_id, name, type, status, schedulable, provider_code, protocol_code, protocol_version, config_revision, credentials_encrypted) VALUES ('acc', 'sys-owner', 'account-acc', 'api_key', 'active', 1, 'openai', 'openai', 'v1', 1, '{}')`)
	// detail rows err。
	if _, err := db.Exec(`DROP TABLE account_api_key_runtime_states`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.LoadSummariesByAccountIds(ctx, []string{"acc"}); err == nil {
		t.Fatal("missing states relation must fail")
	}
	if _, err := h.store.LoadAPIKeyRuntimeDetails(ctx, "acc"); err == nil {
		t.Fatal("details missing states relation must fail")
	}
	db.Exec(`DROP TABLE accounts`)
	db.Exec(`CREATE TABLE accounts (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL DEFAULT '', type TEXT NOT NULL DEFAULT 'api_key', status TEXT NOT NULL DEFAULT 'active', schedulable INTEGER NOT NULL DEFAULT 1, provider_code TEXT NOT NULL DEFAULT 'openai', protocol_code TEXT, protocol_version TEXT, config_revision INTEGER NOT NULL DEFAULT 1, account_expires_at TEXT, credentials_encrypted TEXT NOT NULL DEFAULT '{}', authorization_instance_source_account_id TEXT, deleted_at TEXT)`)
	db.Exec(`INSERT INTO accounts (id, system_account_id, name, type, status, schedulable, provider_code, protocol_code, protocol_version, config_revision, credentials_encrypted) VALUES ('acc', 'sys-owner', 'account-acc', 'api_key', 'active', 1, 'openai', 'openai', 'v1', 1, '{}')`)
	db.Exec(`CREATE TABLE account_api_key_runtime_states (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, account_id TEXT NOT NULL, key_fingerprint TEXT NOT NULL, key_index INTEGER NOT NULL DEFAULT 0, status TEXT NOT NULL, failure_count INTEGER NOT NULL DEFAULT 0, consecutive_failures INTEGER NOT NULL DEFAULT 0, success_count INTEGER NOT NULL DEFAULT 0, cooldown_until TEXT, next_probe_at TEXT, probe_backoff_seconds INTEGER NOT NULL DEFAULT 0, recovery_started_at TEXT, last_attempt_at TEXT, last_success_at TEXT, last_failure_at TEXT, last_error_code TEXT, last_error_message TEXT, last_trace_id TEXT, last_probe_at TEXT, probe_claim_token TEXT, probe_claimed_until TEXT, created_at TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL DEFAULT '', UNIQUE(account_id, key_fingerprint))`)
	// 解密失败 continue（summary）与空列表（details）。
	h.exec(t, `UPDATE accounts SET credentials_encrypted='broken-envelope' WHERE id='acc'`)
	if out, err := h.store.LoadSummariesByAccountIds(ctx, []string{"acc"}); err != nil || len(out) != 0 {
		t.Fatalf("decrypt continue out=%v err=%v", out, err)
	}
	if items, err := h.store.LoadAPIKeyRuntimeDetails(ctx, "acc"); err != nil || len(items) != 0 {
		t.Fatalf("details decrypt continue items=%v err=%v", items, err)
	}
}

func TestW9DDetailsArms(t *testing.T) {
	ctx := context.Background()
	h := openTestDB(t)
	h.seedSchema(t)
	var fps []string
	h.seedAccount(t, struct {
		id           string
		keyCount     int
		status       string
		schedulable  int
		configRev    int64
		providerCode string
		protocolCode string
		protocolVer  string
		fingerprints *[]string
	}{id: "acc", keyCount: 2, status: "active", schedulable: 1, configRev: 1,
		providerCode: "openai", protocolCode: "openai", protocolVer: "v1", fingerprints: &fps})
	// 空 id 归一化。
	if items, err := h.store.LoadAPIKeyRuntimeDetails(ctx, "  "); err != nil || len(items) != 0 {
		t.Fatalf("empty id items=%v err=%v", items, err)
	}
	// 单 Key 池 → 渲染空列表。
	h.exec(t, `UPDATE accounts SET credentials_encrypted=? WHERE id='acc'`, mustSealedKeys(t, []string{"only-one"}))
	if items, err := h.store.LoadAPIKeyRuntimeDetails(ctx, "acc"); err != nil || len(items) != 0 {
		t.Fatalf("single key items=%v err=%v", items, err)
	}
	// 视图账户字段登记：loadSummarySourceRows 的 WHERE accounts.id IN (ids)
	// 使 viewAccountID 恒等于入参 id，LoadAPIKeyRuntimeDetails 的
	// row.viewAccountID != ids[0] continue 臂（details.go 63-64）不可达。
	h.restoreCredentials(t, "acc", []string{"sk-test-acc-a", "sk-test-acc-b"})
	h.exec(t, `UPDATE accounts SET authorization_instance_source_account_id='src' WHERE id='acc'`)
	h.seedAccount(t, struct {
		id           string
		keyCount     int
		status       string
		schedulable  int
		configRev    int64
		providerCode string
		protocolCode string
		protocolVer  string
		fingerprints *[]string
	}{id: "src", keyCount: 2, status: "active", schedulable: 1, configRev: 1,
		providerCode: "openai", protocolCode: "openai", protocolVer: "v1"})
	h.exec(t, `UPDATE accounts SET authorization_instance_source_account_id=NULL WHERE id='src'`)
	// 来源账户视角：视图账户 acc 的明细取自 src 凭据池。
	items, err := h.store.LoadAPIKeyRuntimeDetails(ctx, "acc")
	if err != nil || len(items) == 0 {
		t.Fatalf("source view items=%v err=%v", items, err)
	}
}

func mustSealedKeys(t *testing.T, keys []string) string {
	t.Helper()
	sealed, err := accountsEncryptKeys(keys)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func TestW9DRevalidateArms(t *testing.T) {
	ctx := context.Background()
	h := openTestDB(t)
	h.seedSchema(t)
	var fps []string
	h.seedAccount(t, struct {
		id           string
		keyCount     int
		status       string
		schedulable  int
		configRev    int64
		providerCode string
		protocolCode string
		protocolVer  string
		fingerprints *[]string
	}{id: "acc", keyCount: 2, status: "active", schedulable: 1, configRev: 1,
		providerCode: "openai", protocolCode: "openai", protocolVer: "v1", fingerprints: &fps})
	// 参数臂。
	if _, err := h.store.RevalidatePool(ctx, "  ", 1); err == nil {
		t.Fatal("blank account must fail")
	}
	if _, err := h.store.RevalidatePool(ctx, "acc", 0); err == nil {
		t.Fatal("zero revision must fail")
	}
	// 账户缺失。
	if r, err := h.store.RevalidatePool(ctx, "ghost", 1); err != nil || r.Reason != ReasonAccountNotFound {
		t.Fatalf("ghost reason=%q err=%v", r.Reason, err)
	}
	// 门禁推理：revision 冲突 / 非 active / 不可调度。
	if r, err := h.store.RevalidatePool(ctx, "acc", 99); err != nil || r.Reason != ReasonConfigRevisionConflict {
		t.Fatalf("conflict reason=%q err=%v", r.Reason, err)
	}
	h.exec(t, `UPDATE accounts SET status='paused' WHERE id='acc'`)
	if r, err := h.store.RevalidatePool(ctx, "acc", 1); err != nil || r.Reason != ReasonAccountNotActive {
		t.Fatalf("inactive reason=%q err=%v", r.Reason, err)
	}
	h.exec(t, `UPDATE accounts SET status='active', schedulable=0 WHERE id='acc'`)
	if r, err := h.store.RevalidatePool(ctx, "acc", 1); err != nil || r.Reason != ReasonAccountUnschedulable {
		t.Fatalf("unschedulable reason=%q err=%v", r.Reason, err)
	}
	h.exec(t, `UPDATE accounts SET schedulable=1 WHERE id='acc'`)
	// 不支持（oauth）。
	h.exec(t, `UPDATE accounts SET type='oauth' WHERE id='acc'`)
	if r, err := h.store.RevalidatePool(ctx, "acc", 1); err != nil || r.Reason != ReasonNotSupported {
		t.Fatalf("unsupported reason=%q err=%v", r.Reason, err)
	}
	h.exec(t, `UPDATE accounts SET type='api_key' WHERE id='acc'`)
	// changed==0：两个 key 均 active → 无可重试候选。
	if r, err := h.store.RevalidatePool(ctx, "acc", 1); err != nil || r.Reason != ReasonNoRevalidatableKey {
		t.Fatalf("no candidate reason=%q err=%v", r.Reason, err)
	}
	// 出现候选（error 行、租约已过）→ changed>0 + 标脏链路。
	h.exec(t, `INSERT INTO account_api_key_runtime_states (id,system_account_id,account_id,key_fingerprint,key_index,status,next_probe_at,probe_claimed_until,created_at,updated_at) VALUES ('st-err','sys-owner','acc',?,1,'error',?,NULL,?,?)`,
		fps[1], plusMillis(-1_000), nowMillisText(), nowMillisText())
	h.exec(t, `INSERT INTO group_accounts (system_account_id,group_id,account_id,enabled) VALUES ('sys-owner','grp-1','acc',1)`)
	result, err := h.store.RevalidatePool(ctx, "acc", 1)
	if err != nil || !result.Eligible || result.Changed != 1 {
		t.Fatalf("revalidate result=%+v err=%v", result, err)
	}
	var dirty int
	if err := h.db.QueryRow(`SELECT count(*) FROM group_account_stats_dirty WHERE group_id='grp-1'`).Scan(&dirty); err != nil || dirty != 1 {
		t.Fatalf("dirty=%d err=%v", dirty, err)
	}
	// markGroupAccountStatsDirty 空 ids 早退。
	if err := h.store.markGroupAccountStatsDirty(ctx, nil); err != nil {
		t.Fatal(err)
	}
	// accountIdsAffectedBySourceAccount：同源账户聚合。
	affected, err := h.store.accountIdsAffectedBySourceAccount(ctx, "src")
	if err != nil || len(affected) != 0 {
		t.Fatalf("affected=%v err=%v", affected, err)
	}
}

func TestW9DProbePureArms(t *testing.T) {
	if got := normalizeFailureStatus("rate_limited"); got != "rate_limited" {
		t.Fatalf("rate_limited=%q", got)
	}
	if got := normalizeFailureStatus("weird"); got != "temporary_unavailable" {
		t.Fatalf("weird=%q", got)
	}
	if nextProbeBackoffSeconds(0) != 3 || nextProbeBackoffSeconds(100) != 200 || nextProbeBackoffSeconds(4_000_000) != 3_600 {
		t.Fatal("backoff arms")
	}
	if normalizeProbeDeferSeconds(-1) != 3 || normalizeProbeDeferSeconds(9_000_000) != 3_600 {
		t.Fatal("defer seconds arms")
	}
	if passiveJitterWindowMS(0) != 0 || passiveJitterWindowMS(1) != 0 || passiveJitterWindowMS(60_000) <= 0 {
		t.Fatal("jitter window arms")
	}
	if got := passiveProbeRetryAt(3, func() time.Time { return testNow }); !strings.HasSuffix(got, "Z") {
		t.Fatalf("retry at=%q", got)
	}
	if got := passiveProbeNotBeforeAt("junk", func() time.Time { return testNow }); got != "junk" {
		t.Fatalf("not before junk=%q", got)
	}
	if got := passiveProbeNotBeforeAt(plusMillis(60_000), func() time.Time { return testNow }); got == "" {
		t.Fatal("not before must keep future deadline")
	}
	if quotaRecoveryStartedAt("", nil, nowMillisText(), true) != nil {
		t.Fatal("break window must null recovery")
	}
	if newStateID() == "" || randomToken(8) == "" {
		t.Fatal("id/token shape")
	}
}
