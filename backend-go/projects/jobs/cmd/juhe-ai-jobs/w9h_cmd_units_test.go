package main

import (
	"encoding/json"
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountprobe"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/proberepo"
	"github.com/huanminabc/juhe-ai/backend-go-platform/ownermode"
)

// w9h_cmd_units_test.go 覆盖探针族适配器与恢复探针闭包的错误分支：
// 这些闭包经由 accountprobe.Service（sqlite 源）对缺失账户快速失败，
// 不发起上游 HTTP。

func w9hProbeStoreFixture(t *testing.T) (*proberepo.Store, *proberepoStoreHandle) {
	t.Helper()
	// PG 唯一形态：独立 PG 测试库 + sqldialect 改写（原 sqlite 文件夹具随
	// sqlite 形态退役删除）。
	db, _ := wgOpenPGDialectDB(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.Exec(probeCoreTablesPGDDL)
		return err
	})
	probeStore, err := proberepo.NewStore(proberepo.Config{DB: db, Postgres: true, Secret: wgBalanceSecret})
	if err != nil {
		t.Fatal(err)
	}
	if err := probeStore.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	handle := &proberepoStoreHandle{db: db}
	return probeStore, handle
}

// wgSeedRecoveryAccount 在探针夹具上写入恢复解析用的账户/分组/连接三元组，
// 返回 owner runtime key（原 sqlite 直名 SQL 随 sqlite 形态退役改 juhe_business
// 限定；列集合对齐 LoadAccountForTest/LoadAccountForGroup 读取面）。
func wgSeedRecoveryAccount(t *testing.T, store *proberepoStoreHandle) string {
	t.Helper()
	if _, err := store.db.Exec(`ALTER TABLE juhe_business.accounts ADD COLUMN IF NOT EXISTS proxy_profile_id TEXT`); err != nil {
		t.Fatal(err)
	}
	credentials, err := json.Marshal(map[string]any{"api_key": "sk-rt", "base_url": "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := accountbalance.EncryptV1Envelope(wgBalanceSecret, credentials)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`INSERT INTO juhe_business.groups (id, system_account_id) VALUES ('g-rt', 'sys-rt')`,
		`INSERT INTO juhe_business.group_accounts (group_id, system_account_id, account_id) VALUES ('g-rt', 'sys-rt', 'acc-rt')`,
		`INSERT INTO juhe_business.accounts (id, system_account_id, name, type, status, schedulable, vendor_code, connection_driver_code, credentials_encrypted, config_revision, dispatch_revision)
		 VALUES ('acc-rt', 'sys-rt', 'rt', 'api_key', 'active', 1, 'gpt', 'gpt', $1, 1, 2)`,
		`INSERT INTO juhe_business.models (id, vendor_code, public_model, upstream_model, scope, status, created_at)
		 VALUES ('model-acc-rt', 'gpt', 'gpt-test', 'gpt-test', 'global', 'active', '')`,
		`INSERT INTO juhe_business.account_supported_models (account_id, model_id, created_at) VALUES ('acc-rt', 'model-acc-rt', '')`,
		`INSERT INTO juhe_business.account_connections (id, account_id, connection_preset_code, protocol_surface_code, base_url, enabled, created_at, updated_at)
		 VALUES ('conn-acc-rt', 'acc-rt', 'gpt_openai_v1', 'openai_v1', 'http://127.0.0.1:1', 1, '', '')`,
		`INSERT INTO juhe_business.account_endpoint_modes (connection_id, endpoint_mode) VALUES ('conn-acc-rt', 'chat_json')`,
		`UPDATE juhe_business.accounts SET health_check_connection_id = 'conn-acc-rt' WHERE id = 'acc-rt'`,
	}
	for index, statement := range statements {
		var execErr error
		if index == 2 {
			_, execErr = store.db.Exec(statement, envelope)
		} else {
			_, execErr = store.db.Exec(statement)
		}
		if execErr != nil {
			t.Fatal(execErr)
		}
	}
	return "acc-rt"
}

func w9hProbeService(t *testing.T, store *proberepo.Store) *accountprobe.Service {
	t.Helper()
	service, err := accountprobe.NewService(accountprobe.Options{Source: store, Secret: wgBalanceSecret, Concurrency: 2})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestW9HSpeedFirstCandidateSourceArms(t *testing.T) {
	store, handle := w9hProbeStoreFixture(t)
	runtimeKey := wgSeedRecoveryAccount(t, handle)
	source := speedFirstCandidateSource{store: store}
	ctx := context.Background()

	// 命中账户：状态与可调度位透传。
	summary, err := source.FindAccountForTest(ctx, "acc-rt", runtimeKey)
	if err != nil || summary == nil {
		t.Fatalf("FindAccountForTest summary=%+v err=%v", summary, err)
	}
	if summary.Status != "active" || !summary.Schedulable {
		t.Fatalf("summary=%+v", summary)
	}
	// 缺失账户：nil, nil。
	missing, err := source.FindAccountForTest(ctx, "acc-absent", runtimeKey)
	if err != nil || missing != nil {
		t.Fatalf("missing summary=%+v err=%v", missing, err)
	}
	// 候选解析：命中与缺失。
	candidate, err := source.FindCandidateAccount(ctx, "g-rt", "acc-rt", "sys-rt")
	if err != nil || candidate == nil || candidate.AccountID != "acc-rt" || candidate.GroupID != "g-rt" {
		t.Fatalf("candidate=%+v err=%v", candidate, err)
	}
	absentCandidate, err := source.FindCandidateAccount(ctx, "g-rt", "acc-absent", "sys-rt")
	if err != nil || absentCandidate != nil {
		t.Fatalf("absent candidate=%+v err=%v", absentCandidate, err)
	}
	// 到期列损坏（非 RFC3339）→ proberepo 按错误上抛（BUG-0180：原 panic
	// 会使长驻 probe worker 进入崩溃循环，由调用方按读取失败处理）。
	if _, err := handle.db.Exec(`UPDATE juhe_business.accounts SET account_expires_at = 'not-a-time' WHERE id = 'acc-rt'`); err != nil {
		t.Fatal(err)
	}
	if summary, err := source.FindAccountForTest(ctx, "acc-rt", runtimeKey); err == nil {
		t.Fatalf("损坏的到期时间必须被拒绝: summary=%+v", summary)
	}
}

func TestW9HSpeedFirstProbeFuncTaskFailureBranch(t *testing.T) {
	store, handle := w9hProbeStoreFixture(t)
	wgSeedRecoveryAccount(t, handle)
	closure := speedFirstProbeFunc(w9hProbeService(t, store))
	// 缺失候选账户：诊断任务快速失败 → snapshot 失败 + unknown/task_failure。
	snapshot, outcome := closure(context.Background(), nil,
		opsjobs.ProbeCandidate{
			AccountID: "acc-absent",
			Scope:     opsjobs.ProbeScope{GroupID: "g-rt", SystemAccountID: "sys-rt"},
		}, nil)
	if snapshot.Success {
		t.Fatalf("失败诊断不得返回成功快照: %+v", snapshot)
	}
	if outcome.Kind != opsjobs.ProbeOutcomeUnknown || outcome.FailureKind != opsjobs.ProbeFailureTaskFailure {
		t.Fatalf("outcome=%+v", outcome)
	}
}

func TestW9HCircuitRecoveryTransportProbeTaskFailureBranch(t *testing.T) {
	store, handle := w9hProbeStoreFixture(t)
	runtimeKey := wgSeedRecoveryAccount(t, handle)
	_ = runtimeKey
	service := w9hProbeService(t, store)
	// 零值 scope 无 modelBucket → 不钉住（回退健康检查模型，既有行为）。
	outcome := circuitRecoveryTransportProbe(context.Background(), service,
		circuitRecoveryProbeRequest(
			opsjobs.RecoveryRuntimeIdentity{Kind: "owner", AccountID: "acc-absent", SystemAccountID: "sys-rt"},
			opsjobs.CircuitState{}, "g-rt", "sys-rt"))
	if outcome.Kind != opsjobs.ProbeOutcomeUnknown || outcome.FailureKind != opsjobs.ProbeFailureTaskFailure {
		t.Fatalf("outcome=%+v", outcome)
	}
}

// ---------------------------------------------------------------------------
// account-balance manual bridge secret 匹配臂
// ---------------------------------------------------------------------------

func TestW9HManualBridgeSecretArms(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef0123"
	if matchesAccountBalanceManualSecret(nil, secret) {
		t.Fatal("nil request must not match")
	}
	make := func(authorization string) *http.Request {
		request := httptest.NewRequest(http.MethodPost, "/account-balance/manual", nil)
		request.Header.Set("Authorization", authorization)
		return request
	}
	if matchesAccountBalanceManualSecret(make("Bearer short"), secret) {
		t.Fatal("short token must not match")
	}
	if matchesAccountBalanceManualSecret(make("Basic "+secret), secret) {
		t.Fatal("non-bearer scheme must not match")
	}
	if !matchesAccountBalanceManualSecret(make("Bearer "+secret), secret) {
		t.Fatal("matching bearer token must pass")
	}
	if matchesAccountBalanceManualSecret(make("Bearer wrong-token-wrong-token-wrong-token"), secret) {
		t.Fatal("wrong token must not pass")
	}
}

// ---------------------------------------------------------------------------
// misc helpers
// ---------------------------------------------------------------------------

func TestW9HMiscHelperArms(t *testing.T) {
	if ownermode.Active != "active" {
		t.Fatal("ownermode constant sanity")
	}
	if getenvFrom(map[string]string{"JUHE_AI_SECRET": "s"})("JUHE_AI_SECRET") != "s" {
		t.Fatal("getenvFrom passthrough")
	}
	if getenvFrom(map[string]string{})("JUHE_AI_ABSENT") != "" {
		t.Fatal("absent key reads empty")
	}
	var db *sql.DB
	if db != nil {
		t.Fatal("nil db")
	}
	_ = accountbalance.TriggerManual
}
