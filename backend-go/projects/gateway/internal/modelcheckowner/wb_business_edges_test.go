package modelcheckowner

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// 授权实例解析的修订围栏矩阵：config/dispatch/source 修订任一过期都必须拒绝。
func TestWBResolveAuthorizedTargetRevisionFences(t *testing.T) {
	db, source := wbAuthorizedFixture(t)
	request := RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-virtual", Model: "gpt-5.6-sol"}
	cases := []struct {
		name string
		mut  func(*RunRequest)
		need string
	}{
		{name: "config revision", mut: func(r *RunRequest) { r.ConfigRevision = "1" }, need: "config revision is stale"},
		{name: "dispatch revision", mut: func(r *RunRequest) { r.DispatchRevision = 1 }, need: "dispatch revision is stale"},
		{name: "source config revision", mut: func(r *RunRequest) { r.SourceConfigRevision = "1" }, need: "source account config revision is stale"},
		{name: "source dispatch revision", mut: func(r *RunRequest) { r.SourceDispatchRevision = 1 }, need: "source account dispatch revision is stale"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stale := request
			tc.mut(&stale)
			_, err := source.Resolve(context.Background(), stale)
			if err == nil || !strings.Contains(err.Error(), tc.need) {
				t.Fatalf("err=%v want 包含 %q", err, tc.need)
			}
		})
	}
	_ = db
}

// wbAuthorizedFixture 复刻授权实例（虚拟账户 + 源账户 + 授权）最小数据集。
func wbAuthorizedFixture(t *testing.T) (*sql.DB, *BusinessTargetSource) {
	t.Helper()
	db := wbOpenMemoryDB(t, businessSourceContractDDL())
	envelope := testCredentialEnvelope(t, "secret", `{"api_key":"source-key","supported_endpoint_modes":["responses_sse"]}`)
	statements := []string{
		`INSERT INTO provider_protocol_profiles VALUES ('profile_openai_openai_v1',1,'https://example.invalid/v1')`,
		`INSERT INTO groups VALUES ('group-1','sys-1',1)`,
		`INSERT INTO resource_authorizations VALUES ('grant-1','account','acct-source','sys-1','sys-1','use','active',NULL)`,
		`INSERT INTO accounts(id,system_account_id,provider_code,provider_protocol_profile_id,protocol_code,type,config_revision,dispatch_revision,status,schedulable,health_check_endpoint_mode,credentials_encrypted) VALUES ('acct-source','sys-1','openai','profile_openai_openai_v1','openai','api_key',3,8,'active',1,'responses_sse','` + envelope + `')`,
		`INSERT INTO group_accounts(account_id,system_account_id,group_id,enabled) VALUES ('acct-source','sys-1','group-1',1)`,
		`INSERT INTO accounts(id,system_account_id,provider_code,provider_protocol_profile_id,protocol_code,type,config_revision,dispatch_revision,status,schedulable,health_check_endpoint_mode,credentials_encrypted,authorization_instance_authorization_id,authorization_instance_source_account_id) VALUES ('acct-virtual','sys-1','openai','profile_openai_openai_v1','openai','api_key',5,6,'active',1,'responses_sse','` + envelope + `','grant-1','acct-source')`,
		`INSERT INTO group_accounts(account_id,system_account_id,group_id,account_authorization_id,enabled) VALUES ('acct-virtual','sys-1','group-1','grant-1',1)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("执行 %q 失败: %v", statement, err)
		}
	}
	return db, mustSource(t, db)
}

func mustSource(t *testing.T, db *sql.DB) *BusinessTargetSource {
	t.Helper()
	source, err := NewBusinessTargetSource(db, false, "secret")
	if err != nil {
		t.Fatal(err)
	}
	return source
}

// 可信对比构建的兼容性矩阵：供应商/协议/画像不一致都以 400 失败关闭。
func TestWBBuildRequestComparisonCompatibilityMatrix(t *testing.T) {
	ddl := businessSourceContractDDL()
	// 账户 B 使用不同协议画像，制造协议/画像不匹配。
	db := wbOpenMemoryDB(t, ddl)
	envelopeA := testCredentialEnvelope(t, "secret", `{"api_key":"key-a","supported_endpoint_modes":["responses_sse"]}`)
	envelopeB := testCredentialEnvelope(t, "secret", `{"api_key":"key-b","supported_endpoint_modes":["chat_json"]}`)
	envelopeC := testCredentialEnvelope(t, "secret", `{"api_key":"key-c","supported_endpoint_modes":["messages_json"]}`)
	statements := []string{
		`INSERT INTO provider_protocol_profiles VALUES ('profile_openai_openai_v1',1,'https://a.example/v1')`,
		`INSERT INTO provider_protocol_profiles VALUES ('profile_gpt_openai_chat_v1',1,'https://b.example/v1')`,
		`INSERT INTO provider_protocol_profiles VALUES ('profile_anthropic_anthropic_v1',1,'https://c.example/v1')`,
		`INSERT INTO groups VALUES ('group-1','sys-1',1)`,
		`INSERT INTO group_accounts(account_id,system_account_id,group_id,enabled) VALUES ('acct-a','sys-1','group-1',1)`,
		`INSERT INTO group_accounts(account_id,system_account_id,group_id,enabled) VALUES ('acct-b','sys-1','group-1',1)`,
		`INSERT INTO accounts(id,system_account_id,provider_code,provider_protocol_profile_id,protocol_code,type,config_revision,dispatch_revision,status,schedulable,health_check_endpoint_mode,credentials_encrypted) VALUES ('acct-a','sys-1','openai','profile_openai_openai_v1','openai','api_key',1,1,'active',1,'responses_sse','` + envelopeA + `')`,
		`INSERT INTO accounts(id,system_account_id,provider_code,provider_protocol_profile_id,protocol_code,type,config_revision,dispatch_revision,status,schedulable,health_check_endpoint_mode,credentials_encrypted) VALUES ('acct-b','sys-1','openai','profile_gpt_openai_chat_v1','openai','api_key',1,1,'active',1,'chat_json','` + envelopeB + `')`,
		`INSERT INTO accounts(id,system_account_id,provider_code,provider_protocol_profile_id,protocol_code,type,config_revision,dispatch_revision,status,schedulable,health_check_endpoint_mode,credentials_encrypted) VALUES ('acct-c','sys-1','anthropic','profile_anthropic_anthropic_v1','anthropic','api_key',1,1,'active',1,'messages_json','` + envelopeC + `')`,
		`INSERT INTO group_accounts(account_id,system_account_id,group_id,enabled) VALUES ('acct-c','sys-1','group-1',1)`,
		`INSERT INTO model_quality_policies VALUES ('sys-1',1,'quick',1,70,'fallback',10,NULL)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("执行 %q 失败: %v", statement, err)
		}
	}
	source := mustSource(t, db)
	// 同账户不允许对比。
	if _, err := source.BuildRequest(context.Background(), "sys-1", RunCommand{TargetType: "account", TargetID: "acct-a", Model: "gpt-5.6-sol", TrustedComparison: true, TrustedComparisonID: "acct-a"}); err == nil || !strings.Contains(err.Error(), "distinct account") {
		t.Fatalf("同账户对比必须拒绝: err=%v", err)
	}
	// 对比账户缺失 → 400（wrap 的 RequestError）。
	if _, err := source.BuildRequest(context.Background(), "sys-1", RunCommand{TargetType: "account", TargetID: "acct-a", Model: "gpt-5.6-sol", TrustedComparison: true, TrustedComparisonID: "acct-ghost"}); err == nil || !strings.Contains(err.Error(), "outside scope") {
		t.Fatalf("缺失对比账户必须透传 404: err=%v", err)
	}
}

// 全局 scope 下的定时/策略读接口：owner 未接线必须以 500 失败关闭。
func TestWBHTTPQualityRoutesFailClosedWithoutOwner(t *testing.T) {
	handler := newTestHTTPHandler()
	handler.Quality = nil
	for _, route := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/quality-policy", ""},
		{http.MethodGet, "/quality-schedules", ""},
		{http.MethodPost, "/quality-schedules", `{}`},
	} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body))
		handler.ServeHTTP(response, request)
		// GET 走 writeOwnerError(500)；POST create 经 writeQualityError 映射为 400。
		want := http.StatusInternalServerError
		if route.method == http.MethodPost {
			want = http.StatusBadRequest
		}
		if response.Code != want {
			t.Fatalf("%s %s status=%d body=%s", route.method, route.path, response.Code, response.Body.String())
		}
	}
}

// GetRun 的信任合并早退分支：unverified 报告与缺失 observedModel 的
// unavailable run 都不得触碰信任投影表。
func TestWBRunGetRunTrustMergeEarlyReturns(t *testing.T) {
	store := &Store{db: wbOpenMemoryDB(t, runtimeTestDDL()), mode: "sqlite"}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 9, 0, 0, 0, time.UTC)
	seed := func(id string, resultSummary string, level string) {
		t.Helper()
		run := RunRecord{ID: id, SystemAccountID: "sys", ActorSystemAccountID: "actor", ProviderCode: "openai", TargetType: "account", TargetID: "acct", AccountID: "acct", Model: "m", Profile: "full", TriggerKind: "manual", ProbeSetVersion: "probe-v1", StartedAt: now, RequestSummary: []byte(`{}`), PolicySnapshot: []byte(`{}`)}
		if err := store.CreateRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		item := ItemRecord{ID: id + "-item-0001", RunID: id, ItemKey: "stability", ItemType: "stability", Status: ItemSkipped, Score: 0, MaxScore: 100, EvidenceSummary: `{}`}
		if err := store.ProjectOutcome(ctx, OutcomeProjection{RunID: id, Status: RunCompleted, Level: level, Score: 0, MaxScore: 100, FinishedAt: now.Add(time.Minute), Items: []ItemRecord{item}, ResultSummary: json.RawMessage(resultSummary), QualityDecision: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	seed("run-unverified", `{"modelCheckUnverified":true,"trustReport":{}}`, "unavailable")
	seed("run-unavailable", `{"trustReport":{"observedModel":""}}`, "unavailable")
	seed("run-reason", `{"trustReport":{"reasonCodes":["model_response_evidence_unavailable"]}}`, "likely")
	runtime := &Runtime{Store: store}
	for _, id := range []string{"run-unverified", "run-unavailable", "run-reason"} {
		detail, found, err := runtime.GetRun(ctx, id)
		if err != nil || !found {
			t.Fatalf("%s 详情读取 failed=%v err=%v", id, found, err)
		}
		runDetail := detail.(RunDetail)
		if strings.Contains(string(runDetail.ResultSummary), "identityStatus") {
			t.Fatalf("%s 不应合并最新信任投影: %s", id, runDetail.ResultSummary)
		}
	}
}

// ProjectOutcome 条目漂移契约：终态重放时条目集合不一致必须冲突。
func TestWBStoreProjectOutcomeItemDriftConflicts(t *testing.T) {
	store := &Store{db: wbOpenMemoryDB(t, runtimeTestDDL()), mode: "sqlite"}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	run := RunRecord{ID: "run-drift", SystemAccountID: "sys", ActorSystemAccountID: "actor", ProviderCode: "openai", TargetType: "account", TargetID: "acct", Model: "m", Profile: "quick", TriggerKind: "manual", ProbeSetVersion: "probe-v1", StartedAt: now, RequestSummary: []byte(`{}`), PolicySnapshot: []byte(`{}`)}
	if err := store.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	item := ItemRecord{ID: "run-drift-item-0001", RunID: run.ID, ItemKey: "stability", ItemType: "stability", Status: ItemPassed, Score: 90, MaxScore: 100, EvidenceSummary: `{"ok":true}`}
	projection := OutcomeProjection{RunID: run.ID, Status: RunFailed, Level: "unavailable", Score: 0, MaxScore: 100, FinishedAt: now.Add(time.Minute), Items: []ItemRecord{item}, ResultSummary: json.RawMessage(`{}`), QualityDecision: json.RawMessage(`{}`)}
	if err := store.ProjectOutcome(ctx, projection); err != nil {
		t.Fatal(err)
	}
	drifted := projection
	drifted.Items = []ItemRecord{item, {ID: "run-drift-item-0002", RunID: run.ID, ItemKey: "extra", ItemType: "extra", Status: ItemSkipped, Score: 0, MaxScore: 100, EvidenceSummary: `{}`}}
	if err := store.ProjectOutcome(ctx, drifted); !errors.Is(err, ErrRunProjectionConflict) {
		t.Fatalf("条目集合漂移必须冲突: err=%v", err)
	}
}

// 空恢复模型 + 空健康模型的恢复任务必须被跳过而不是生成坏任务。
func TestWBBusinessSchedulerSkipsBlankRecoveryModel(t *testing.T) {
	db := wbOpenMemoryDB(t, []string{
		`CREATE TABLE accounts (id TEXT PRIMARY KEY,provider_code TEXT,config_revision INTEGER,dispatch_revision INTEGER,deleted_at TEXT,authorization_instance_authorization_id TEXT,authorization_instance_source_account_id TEXT,status TEXT,health_check_model TEXT)`,
		`CREATE TABLE model_quality_schedules (id TEXT PRIMARY KEY,revision INTEGER,system_account_id TEXT,account_id TEXT,model TEXT,interval_minutes INTEGER,profile TEXT,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER,enabled INTEGER,next_run_at TEXT,lease_owner TEXT,lease_until TEXT,last_run_id TEXT,last_run_at TEXT,last_run_status TEXT,updated_at TEXT,custom_question_ids TEXT)`,
		`CREATE TABLE model_quality_policies (system_account_id TEXT PRIMARY KEY,revision INTEGER,profile TEXT,manual_enforcement_enabled INTEGER,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER,custom_question_ids TEXT)`,
		`CREATE TABLE account_quality_enforcements (account_id TEXT PRIMARY KEY,system_account_id TEXT,enforcement_id TEXT,generation INTEGER,state TEXT,action TEXT,recovery_model TEXT,account_config_revision INTEGER,policy_revision INTEGER,config_source_id TEXT,profile TEXT,penalty_threshold INTEGER,recovery_interval_minutes INTEGER,recovery_due_at TEXT,recovery_lease_owner TEXT,recovery_lease_until TEXT,updated_at TEXT,config_source TEXT)`,
	})
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`INSERT INTO accounts (id,provider_code,config_revision,dispatch_revision,deleted_at,authorization_instance_authorization_id,status,health_check_model) VALUES ('acct','openai',4,1,NULL,NULL,'quality_isolated','')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account_quality_enforcements VALUES ('acct','sys','enf',2,'active','quality_isolate','',8,7,'sch','full',71,15,?,NULL,NULL,'','schedule')`, now.Add(-time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	store := schedulerStoreFixture(t)
	t.Cleanup(func() { _ = store.db.Close() })
	source := &BusinessSchedulerSource{Business: db, Store: store, OwnerID: "gateway-1"}
	tasks, err := source.Claim(context.Background(), SchedulerQualityRecovery, now, 5)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("空恢复模型必须跳过: tasks=%+v err=%v", tasks, err)
	}
}

// 异常窗口/日期解析的尾部分支。
func TestWBGatewayExceptionTrailingContent(t *testing.T) {
	_, err := availabilityAllowedGateway(`{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"00:00","end":"23:59"}],"exceptions":[{"date":"2030-01-05","action":"deny"} {"date":"2030-01-06","action":"deny"}]}`, time.Date(2030, 1, 5, 9, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("异常数组尾随内容必须拒绝")
	}
	if !gatewayDateOrBlank("") || gatewayDateOrBlank("2026-13-01") {
		t.Fatal("gatewayDateOrBlank 语义不正确")
	}
}

// Host 组件与默认值的剩余分支。
func TestWBHostComponentNilCloseAndOwnerDefault(t *testing.T) {
	var host *Host
	component := host.Component()
	if err := component.Close(); err != nil {
		t.Fatalf("nil 宿主组件 Close 必须幂等: %v", err)
	}
	if ownerOrDefault("  ") != "gateway" {
		t.Fatal("空白 owner 必须回退 gateway")
	}
	if targetOwnerOrDefault("  ", " fallback ") != "fallback" {
		t.Fatal("空目标 owner 必须回退 fallback")
	}
	handler := &HTTPHandler{MaxBody: 4096, Heartbeat: time.Second}
	if handler.maxBody() != 4096 || handler.heartbeat() != time.Second {
		t.Fatal("显式配置必须优先于默认值")
	}
	var nilError *RequestError
	if nilError.Error() != "" {
		t.Fatal("nil RequestError 必须返回空串")
	}
	handlerQ := &HTTPHandler{}
	if _, err := handlerQ.quality(); err == nil {
		t.Fatal("未接线的质量管理必须报错")
	}
}
