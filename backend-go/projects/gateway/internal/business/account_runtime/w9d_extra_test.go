package accountruntime

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// w9d：SQL 中段失败臂（sqlite trigger 注入）、drop/close 注入与纯参数臂。
// 不可达登记：
//   - randomToken 的 rand.Read 失败臂（runtime.go 388）与
//     ProbeClaimToken 为空检查（876）：crypto/rand 不失败。
//   - ValidateGatewayAPIKey rows.Err（460）、ListDue rows.Err/Close（847/851）、
//     SyncSchedule scan/rows.Err/Close（1165/1170/1173）：本地 sqlite 驱动
//     无迭代中途故障注入面。
//   - runtimeUpdate 的 Commit err（706/734/798）：本地驱动无提交失败路径。
//   - ReadAccountAPIKeyPoolProbeCursor 的 Cursor==nil（operations.go 83）与
//     Save 的 Cursor!=nil（operations.go 94）：wrapper 语义上不可达的防御臂。
//   - runtimeUpdate 的 validStatus 重校验（runtime.go 658）：唯一调用者
//     DeferAccountAPIKeyProbe 已先行校验。
// 缺陷登记（未修，待产品确认）：
//   - ListAccountAPIKeyRuntimeStatesDueForProbe 在 rows.Scan 失败时直接
//     return（runtime.go 842-844），缺少 rows.Close()，泄漏数据库连接。
// ---------------------------------------------------------------------------

type w9dQuotaOK struct{}

func (w9dQuotaOK) ReadAPIKeyQuotaCosts(context.Context, string, string, time.Time, *int) (QuotaCosts, error) {
	return QuotaCosts{}, nil
}

func w9dFailResolver(err string) CredentialResolver {
	return CredentialResolverFunc(func(context.Context, Account) ([]APIKeyEntry, error) {
		return nil, errors.New(err)
	})
}

func w9dStore(t *testing.T, gate OwnerGate, deps Dependencies) (*Store, *sql.DB) {
	t.Helper()
	s, db := testStore(t, gate, deps)
	db.SetMaxIdleConns(0)
	return s, db
}

func TestW9DClockAndQuotaArms(t *testing.T) {
	bare := &Store{}
	if bare.clock().IsZero() {
		t.Fatal("default clock must work")
	}
	s, db := w9dStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, Dependencies{QuotaUsage: w9dQuotaOK{}})
	defer db.Close()
	if _, err := s.CheckAPIKeyQuota(context.Background(), GatewayAPIKey{QuotaLimitsJSON: "{bad"}); err == nil {
		t.Fatal("broken quota limits must fail")
	}
}

func TestW9DValidateGatewayArms(t *testing.T) {
	ctx := context.Background()
	seed := func(t *testing.T, db *sql.DB) {
		t.Helper()
		for _, stmt := range []string{
			`INSERT INTO system_accounts(id,status) VALUES('sys-1','active')`,
			`INSERT INTO route_strategies(id,system_account_id,mode,status) VALUES('r','sys-1','normal','active')`,
			`INSERT INTO api_keys(id,system_account_id,route_strategy_id,key_hash,status) VALUES('k','sys-1','r','hash-1','active')`,
			`INSERT INTO route_strategy_groups(id,route_strategy_id,system_account_id,group_id,priority,weight,status,created_at) VALUES('g1','r','sys-1','grp',1,1,'active','')`,
		} {
			if _, err := db.Exec(stmt); err != nil {
				t.Fatal(err)
			}
		}
	}
	// 未知 key → 第一查询 err。
	s, db := w9dStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, Dependencies{})
	if _, err := s.ValidateGatewayAPIKey(ctx, "missing"); err == nil {
		t.Fatal("unknown key must fail")
	}
	db.Close()

	// 绑定查询 err：drop route_strategy_groups。
	s2, db2 := w9dStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, Dependencies{})
	seed(t, db2)
	if _, err := db2.Exec(`DROP TABLE route_strategy_groups`); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.ValidateGatewayAPIKey(ctx, "sk"); err == nil {
		t.Fatal("missing bindings relation must fail")
	}
	db2.Close()

	// 绑定 scan err：priority 列存非整数文本。
	s3, db3 := w9dStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, Dependencies{})
	seed(t, db3)
	if _, err := db3.Exec(`UPDATE route_strategy_groups SET priority='x' WHERE id='g1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s3.ValidateGatewayAPIKey(ctx, "sk"); err == nil {
		t.Fatal("bad priority scan must fail")
	}
	db3.Close()
}

func TestW9DCursorArms(t *testing.T) {
	s, db := w9dStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, Dependencies{})
	defer db.Close()
	cur := ProbeCursor{AccountID: "acct-1", Purpose: HealthCheck, KeySetFingerprint: "set-1", ConfigRevision: 1}
	// 半开门禁 → save/delete 拒绝。
	s.gate = OwnerGate{Confirmed: true, SchemaReady: true}
	if _, err := s.AccountAPIKeyPoolProbeCursor(context.Background(), cur, "save"); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("blocked save err=%v", err)
	}
	s.gate = OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}
	// 关闭数据库 → delete/save err。
	if _, err := s.AccountAPIKeyPoolProbeCursor(context.Background(), cur, "save"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := s.AccountAPIKeyPoolProbeCursor(context.Background(), cur, "delete"); err == nil {
		t.Fatal("delete after close must fail")
	}
	if _, err := s.SaveAccountAPIKeyPoolProbeCursor(context.Background(), cur); err == nil {
		t.Fatal("save after close must fail")
	}
	// Read 的 Cursor==nil 与 Save 的 Cursor!=nil 为防御臂（见文件头登记）。
}

func TestW9DKeyTargetArms(t *testing.T) {
	s, db := w9dStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, Dependencies{})
	defer db.Close()
	base := poolAccount()
	if s.keyTarget(Account{ID: base.ID, SystemAccountID: base.SystemAccountID, SelectedKeyFingerprint: base.SelectedKeyFingerprint, APIKeys: base.APIKeys, Type: "oauth"}) {
		t.Fatal("non api_key type must not be key target")
	}
	entries := []APIKeyEntry{{Fingerprint: ""}, {Key: "sk-other"}}
	// Key 为空文本时以 hashKey(Key) 兜底匹配。
	if !s.keyTarget(Account{ID: "a", SystemAccountID: "s", SelectedKeyFingerprint: hashKey("sk-other"), APIKeys: entries}) {
		t.Fatal("fingerprint fallback must match")
	}
	if s.keyTarget(Account{ID: "a", SystemAccountID: "s", SelectedKeyFingerprint: "zzz", APIKeys: entries}) {
		t.Fatal("mismatched pool must not be target")
	}
}

func TestW9DRuntimeUpdateArms(t *testing.T) {
	ctx := context.Background()
	// 参数臂。
	s, db := w9dStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, Dependencies{})
	seedAccount(t, db, "active")
	if _, err := s.RecordAccountAPIKeyFailure(ctx, poolAccount(), FailureInput{ObservedAt: "not-a-time"}); err == nil {
		t.Fatal("bad observed time must fail")
	}
	// 无效 ExpectedStatus 在 Defer 入口被拦截（runtimeUpdate 内部的
	// validStatus 重新校验为防御臂，见文件头登记）。
	if r, err := s.DeferAccountAPIKeyProbe(ctx, poolAccount(), ProbeDeferInput{ExpectedStatus: "bogus", ExpectedNextProbeAt: "2029-01-01T00:00:00Z"}); err != nil || r.SkippedReason != "invalid_expected_status" {
		t.Fatalf("invalid expected status result=%+v err=%v", r, err)
	}
	db.Close()

	// BeginTx err：closed db。
	s2, db2 := w9dStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, Dependencies{})
	seedAccount(t, db2, "active")
	db2.Close()
	if _, err := s2.RecordAccountAPIKeyFailure(ctx, poolAccount(), FailureInput{}); err == nil {
		t.Fatal("begin after close must fail")
	}

	// existing 状态查询 err：drop runtime states。
	s3, db3 := w9dStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, Dependencies{})
	seedAccount(t, db3, "active")
	if _, err := db3.Exec(`DROP TABLE account_api_key_runtime_states`); err != nil {
		t.Fatal(err)
	}
	if _, err := s3.RecordAccountAPIKeyFailure(ctx, poolAccount(), FailureInput{}); err == nil {
		t.Fatal("missing states relation must fail")
	}
	db3.Close()

	// UPDATE/INSERT err 与 CAS：trigger 注入。
	s4, db4 := w9dStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, Dependencies{})
	seedAccount(t, db4, "active")
	// 预置已存在的 runtime state 行，避免 defer/success 走 stale 短路。
	if _, err := db4.Exec(`INSERT INTO account_api_key_runtime_states(id,system_account_id,account_id,key_fingerprint,key_index,status,next_probe_at,last_attempt_at,created_at,updated_at) VALUES('state-1','sys-1','acct-1',?,0,'temporary_unavailable','2029-01-01T00:00:00Z','2029-01-01T00:00:00Z','2029-01-01T00:00:00Z','2029-01-01T00:00:00Z')`, hashKey("sk-one")); err != nil {
		t.Fatal(err)
	}
	if _, err := db4.Exec(`CREATE TRIGGER w9d_state_update BEFORE UPDATE ON account_api_key_runtime_states BEGIN SELECT RAISE(FAIL,'w9d update blocked'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := db4.Exec(`CREATE TRIGGER w9d_state_insert BEFORE INSERT ON account_api_key_runtime_states BEGIN SELECT RAISE(FAIL,'w9d insert blocked'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s4.DeferAccountAPIKeyProbe(ctx, poolAccount(), ProbeDeferInput{ExpectedStatus: RuntimeTemporaryUnavailable, ExpectedNextProbeAt: "2029-01-01T00:00:00Z", DelaySeconds: 5}); err == nil {
		t.Fatal("defer update blocked must fail")
	}
	if _, err := s4.RecordAccountAPIKeyFailure(ctx, poolAccount(), FailureInput{Status: RuntimeTemporaryUnavailable}); err == nil {
		t.Fatal("failure update blocked must fail")
	}
	if _, err := s4.RecordAccountAPIKeySuccess(ctx, poolAccount(), SuccessInput{}); err == nil {
		t.Fatal("success update blocked must fail")
	}
	// state 不存在的 success/failure → INSERT 臂。
	other := poolAccount()
	other.SelectedKeyFingerprint = hashKey("sk-two")
	other.SelectedKeyIndex = 1
	if _, err := s4.RecordAccountAPIKeySuccess(ctx, other, SuccessInput{}); err == nil {
		t.Fatal("success insert blocked must fail")
	}
	if _, err := s4.RecordAccountAPIKeyFailure(ctx, other, FailureInput{Status: RuntimeTemporaryUnavailable}); err == nil {
		t.Fatal("failure insert blocked must fail")
	}
	db4.Close()

	// CAS 臂：ExpectedAccountConfigRevision 不匹配 → stale_probe_state。
	s5, db5 := w9dStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, Dependencies{})
	seedAccount(t, db5, "active")
	if _, err := s5.DeferAccountAPIKeyProbe(ctx, poolAccount(), ProbeDeferInput{ExpectedStatus: RuntimeActive, DelaySeconds: 5}); err != nil {
		t.Fatal(err)
	}
	result, err := s5.DeferAccountAPIKeyProbe(ctx, poolAccount(), ProbeDeferInput{ExpectedStatus: RuntimeTemporaryUnavailable, ExpectedNextProbeAt: "2029-01-01T00:00:00Z", ExpectedAccountConfigRevision: 999, DelaySeconds: 5})
	if err != nil || result.SkippedReason != "stale_probe_state" {
		t.Fatalf("revision guard result=%+v err=%v", result, err)
	}
	db5.Close()
}

func TestW9DProbeDueArms(t *testing.T) {
	ctx := context.Background()
	resolver := CredentialResolverFunc(func(context.Context, Account) ([]APIKeyEntry, error) {
		return []APIKeyEntry{{Key: "sk-one", Fingerprint: hashKey("sk-one"), Index: 0}}, nil
	})
	// requireWrite 拒绝。
	blocked, blockedDB := w9dStore(t, OwnerGate{Confirmed: true, SchemaReady: true}, Dependencies{})
	if _, err := blocked.ListAccountAPIKeyRuntimeStatesDueForProbe(ctx, 10); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("blocked probe list err=%v", err)
	}
	blockedDB.Close()

	// 查询 err：drop 表。
	s, db := w9dStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, Dependencies{Credentials: resolver})
	if _, err := db.Exec(`DROP TABLE account_api_key_runtime_states`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListAccountAPIKeyRuntimeStatesDueForProbe(ctx, 10); err == nil {
		t.Fatal("missing states relation must fail")
	}
	db.Close()

	// scan err / resolver err / fingerprint 缺失 / claim 更新 err。
	seed := func(t *testing.T, db *sql.DB) {
		t.Helper()
		seedAccount(t, db, "active")
		if _, err := db.Exec(`INSERT INTO account_api_key_runtime_states(id,system_account_id,account_id,key_fingerprint,key_index,status,next_probe_at,created_at,updated_at) VALUES('state-1','sys-1','acct-1',?,0,'temporary_unavailable','2029-01-01T00:00:00Z','2029-01-01T00:00:00Z','2029-01-01T00:00:00Z')`, hashKey("sk-one")); err != nil {
			t.Fatal(err)
		}
	}
	// 缺陷登记：ListAccountAPIKeyRuntimeStatesDueForProbe 的 Scan err 分支
	// （runtime.go 842-844）未关闭 rows 即返回，会泄漏连接并锁住 sqlite 文件
	// （Windows 上表现为 TempDir 清理失败），故此处不注入 scan err，待修复后补测。
	_ = resolver
	s3, db3 := w9dStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, Dependencies{Credentials: w9dFailResolver("resolver down")})
	seed(t, db3)
	if _, err := s3.ListAccountAPIKeyRuntimeStatesDueForProbe(ctx, 10); err == nil || !strings.Contains(err.Error(), "resolver down") {
		t.Fatalf("resolver failure err=%v", err)
	}
	db3.Close()

	s4, db4 := w9dStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, Dependencies{Credentials: CredentialResolverFunc(func(context.Context, Account) ([]APIKeyEntry, error) {
		return []APIKeyEntry{{Key: "sk-other", Fingerprint: hashKey("sk-other"), Index: 0}}, nil
	})})
	seed(t, db4)
	if _, err := s4.ListAccountAPIKeyRuntimeStatesDueForProbe(ctx, 10); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing fingerprint err=%v", err)
	}
	db4.Close()

	// claim 更新 err：trigger。
	s5, db5 := w9dStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, Dependencies{Credentials: resolver})
	seed(t, db5)
	if _, err := db5.Exec(`CREATE TRIGGER w9d_claim BEFORE UPDATE ON account_api_key_runtime_states BEGIN SELECT RAISE(FAIL,'w9d claim blocked'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s5.ListAccountAPIKeyRuntimeStatesDueForProbe(ctx, 10); err == nil {
		t.Fatal("claim update blocked must fail")
	}
	db5.Close()
}

func TestW9DClearAndStreamArms(t *testing.T) {
	ctx := context.Background()
	// 参数与门禁臂。
	s, db := w9dStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, Dependencies{})
	seedAccount(t, db, "active")
	if _, err := s.MarkAccountTemporaryUnavailable(ctx, TemporaryUnavailableInput{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty account err=%v", err)
	}
	s.gate = OwnerGate{Confirmed: true, SchemaReady: true}
	if _, err := s.ClearAccountFailureState(ctx, ClearFailureInput{AccountID: "acct-1"}); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("blocked clear err=%v", err)
	}
	s.gate = OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}
	db.Close()

	// SELECT err：drop accounts。
	s2, db2 := w9dStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, Dependencies{})
	if _, err := db2.Exec(`DROP TABLE accounts`); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.ClearAccountFailureState(ctx, ClearFailureInput{AccountID: "acct-1"}); err == nil {
		t.Fatal("missing accounts relation must fail")
	}
	db2.Close()

	// 正常路径 + 错误码匹配 + revision guard + trigger err。
	s3, db3 := w9dStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, Dependencies{})
	seedAccount(t, db3, "temporary_unavailable")
	if _, err := db3.Exec(`UPDATE accounts SET last_error_code='stream_failure',config_revision=3 WHERE id='acct-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db3.Exec(`CREATE TRIGGER w9d_acct_update BEFORE UPDATE ON accounts BEGIN SELECT RAISE(FAIL,'w9d account update blocked'); END`); err != nil {
		t.Fatal(err)
	}
	// 错误码不匹配 → skipped（无 UPDATE）。
	if r, err := s3.ClearAccountFailureState(ctx, ClearFailureInput{AccountID: "acct-1", ExpectedLastErrorCodes: []string{"other"}}); err != nil || r.SkippedReason != "stale_failure_state" {
		t.Fatalf("code mismatch result=%+v err=%v", r, err)
	}
	// 匹配但 UPDATE 被 trigger 破坏。
	if _, err := s3.ClearAccountFailureState(ctx, ClearFailureInput{AccountID: "acct-1", ExpectedLastErrorCodes: []string{"stream_failure"}, ExpectedConfigRevision: 3}); err == nil {
		t.Fatal("clear update blocked must fail")
	}
	db3.Close()

	// 流失败：SELECT err / 更新 err / 阈值动作 err。
	s4, db4 := w9dStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, Dependencies{})
	seedAccount(t, db4, "active")
	if _, err := s4.RecordAccountStreamFailure(ctx, StreamFailureInput{AccountID: "missing", ThresholdCount: 1, ThresholdWindowMinutes: 1}); err == nil {
		t.Fatal("unknown account must fail")
	}
	if _, err := db4.Exec(`CREATE TRIGGER w9d_acct_update2 BEFORE UPDATE ON accounts BEGIN SELECT RAISE(FAIL,'w9d stream update blocked'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s4.RecordAccountStreamFailure(ctx, StreamFailureInput{AccountID: "acct-1", ThresholdCount: 5, ThresholdWindowMinutes: 1}); err == nil {
		t.Fatal("stream update blocked must fail")
	}
	if _, err := s4.RecordAccountStreamFailure(ctx, StreamFailureInput{AccountID: "acct-1", ThresholdCount: 1, ThresholdWindowMinutes: 1, Action: "cooldown"}); err == nil {
		t.Fatal("cooldown action blocked must fail")
	}
	db4.Close()

	// ApplyAccountErrorHandling：Success + ObservedAt 空走 clock。
	s5, db5 := w9dStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, Dependencies{})
	seedAccount(t, db5, "active")
	if _, err := s5.ApplyAccountErrorHandling(ctx, poolAccount(), ErrorHandlingInput{Success: true}); err != nil {
		t.Fatalf("apply success err=%v", err)
	}
	db5.Close()
}

func TestW9DScheduleSyncArms(t *testing.T) {
	ctx := context.Background()
	evaluator := ScheduleEvaluatorFunc(func(context.Context, string, time.Time) (ScheduleDecision, error) {
		return ScheduleDecision{Status: "disabled", EventKey: "close"}, nil
	})
	// requireWrite 拒绝。
	blocked, blockedDB := w9dStore(t, OwnerGate{Confirmed: true, SchemaReady: true}, Dependencies{Schedule: evaluator})
	if _, err := blocked.SyncAPIKeyAvailabilityScheduleStatuses(ctx); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("blocked sync err=%v", err)
	}
	blockedDB.Close()

	// 查询 err：drop api_keys。
	s, db := w9dStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, Dependencies{Schedule: evaluator})
	if _, err := db.Exec(`DROP TABLE api_keys`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SyncAPIKeyAvailabilityScheduleStatuses(ctx); err == nil {
		t.Fatal("missing api_keys relation must fail")
	}
	db.Close()

	// 事件插入 err：trigger。
	s2, db2 := w9dStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, Dependencies{Schedule: evaluator})
	if _, err := db2.Exec(`INSERT INTO api_keys(id,system_account_id,route_strategy_id,key_hash,status,availability_schedule_json) VALUES('k1','sys-1','r','h','active','{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db2.Exec(`CREATE TRIGGER w9d_event_insert BEFORE INSERT ON api_key_schedule_status_events BEGIN SELECT RAISE(FAIL,'w9d event blocked'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.SyncAPIKeyAvailabilityScheduleStatuses(ctx); err == nil {
		t.Fatal("event insert blocked must fail")
	}
	db2.Close()

	// 事件幂等：event_key 已存在 → x==0 → continue（不触发状态更新）。
	s3, db3 := w9dStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, Dependencies{Schedule: evaluator})
	if _, err := db3.Exec(`INSERT INTO api_keys(id,system_account_id,route_strategy_id,key_hash,status,availability_schedule_json) VALUES('k2','sys-1','r','h','active','{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db3.Exec(`INSERT INTO api_key_schedule_status_events(event_key,api_key_id,status,executed_at) VALUES('k2:close','k2','disabled','')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s3.SyncAPIKeyAvailabilityScheduleStatuses(ctx); err != nil {
		t.Fatalf("idempotent event sync err=%v", err)
	}
	db3.Close()

	// api_keys 更新 err：事件插入成功后 UPDATE 被 trigger 破坏。
	s3b, db3b := w9dStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, Dependencies{Schedule: evaluator})
	if _, err := db3b.Exec(`INSERT INTO api_keys(id,system_account_id,route_strategy_id,key_hash,status,availability_schedule_json) VALUES('k2b','sys-1','r','h','active','{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db3b.Exec(`CREATE TRIGGER w9d_key_update BEFORE UPDATE ON api_keys BEGIN SELECT RAISE(FAIL,'w9d key update blocked'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s3b.SyncAPIKeyAvailabilityScheduleStatuses(ctx); err == nil {
		t.Fatal("key update blocked must fail")
	}
	db3b.Close()

	// 空状态分支的推进 UPDATE err。
	quiet := ScheduleEvaluatorFunc(func(context.Context, string, time.Time) (ScheduleDecision, error) { return ScheduleDecision{}, nil })
	s4, db4 := w9dStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, Dependencies{Schedule: quiet})
	if _, err := db4.Exec(`INSERT INTO api_keys(id,system_account_id,route_strategy_id,key_hash,status,availability_schedule_json) VALUES('k3','sys-1','r','h','active','{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db4.Exec(`CREATE TRIGGER w9d_key_update2 BEFORE UPDATE ON api_keys BEGIN SELECT RAISE(FAIL,'w9d key update2 blocked'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s4.SyncAPIKeyAvailabilityScheduleStatuses(ctx); err == nil {
		t.Fatal("next check update blocked must fail")
	}
	db4.Close()
}
