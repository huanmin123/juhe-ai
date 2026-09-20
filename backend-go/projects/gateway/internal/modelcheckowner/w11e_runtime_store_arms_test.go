package modelcheckowner

// w11e runtime.go / store.go 错误臂：run 前置校验、可信比对 CAS、
// 存储失败传播、健康事实与固定截距基线激活的失败分支。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprobe"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

func w11eValidTarget() Target {
	return Target{Endpoint: "https://w11e.invalid", Protocol: modelcheckprofile.ProtocolOpenAIResponses, Prompt: "hello", DispatchRevision: 3, SourceConfigRevision: "5", SourceDispatchRevision: 2}
}

func w11eValidRequest() RunRequest {
	return RunRequest{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "quick"}
}

func TestW11ERuntimeValidationArms(t *testing.T) {
	var nilRuntime *Runtime
	if _, err := nilRuntime.Run(context.Background(), w11eValidRequest()); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("nil runtime 必须拒绝: %v", err)
	}
	store := newRuntimeTestStore(t)
	defer store.Close()
	target := w11eValidTarget()
	runtime := &Runtime{Store: store, Resolve: func(context.Context, RunRequest) (Target, error) { return target, nil }}
	for name, request := range map[string]RunRequest{
		"scope":   {TargetType: "account", TargetID: "acct", Model: "m", Profile: "quick"},
		"request": {SystemAccountID: "sys", ActorSystemAccountID: "actor", Model: "m", Profile: "quick"},
		"profile": {SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "m", Profile: "medium"},
	} {
		if _, err := runtime.Run(context.Background(), request); err == nil {
			t.Fatalf("%s 不完整必须拒绝", name)
		}
	}
	// 解析失败传播。
	failed := &Runtime{Store: store, Resolve: func(context.Context, RunRequest) (Target, error) { return Target{}, errors.New("w11e-resolve-failed") }}
	if _, err := failed.Run(context.Background(), w11eValidRequest()); err == nil || !strings.Contains(err.Error(), "resolve J3b target") {
		t.Fatalf("解析失败必须传播: %v", err)
	}
	// 目标契约不完整。
	for name, mutate := range map[string]func(*Target){
		"endpoint": func(t *Target) { t.Endpoint = "" },
		"prompt":   func(t *Target) { t.Prompt = "" },
		"dispatch": func(t *Target) { t.DispatchRevision = 0 },
	} {
		broken := w11eValidTarget()
		mutate(&broken)
		brokenRuntime := &Runtime{Store: store, Resolve: func(context.Context, RunRequest) (Target, error) { return broken, nil }}
		if _, err := brokenRuntime.Run(context.Background(), w11eValidRequest()); err == nil {
			t.Fatalf("目标 %s 不完整必须拒绝", name)
		}
	}
	// 请求侧 CAS 失配。
	for name, mutate := range map[string]func(*RunRequest){
		"dispatch":          func(r *RunRequest) { r.DispatchRevision = 99 },
		"source config":     func(r *RunRequest) { r.SourceConfigRevision = "stale" },
		"source dispatch":   func(r *RunRequest) { r.SourceDispatchRevision = 99 },
		"source family":     func(r *RunRequest) { r.SourceEndpointFamily = "chat_completions" },
		"upstream family":   func(r *RunRequest) { r.UpstreamEndpointFamily = "chat_completions" },
		"upstream protocol": func(r *RunRequest) { r.UpstreamProtocol = "openai_chat" },
		"upstream mode":     func(r *RunRequest) { r.UpstreamEndpointMode = "chat_json" },
	} {
		request := w11eValidRequest()
		mutate(&request)
		if _, err := runtime.Run(context.Background(), request); err == nil {
			t.Fatalf("请求 %s 失配必须拒绝", name)
		}
	}
	// 上游族/协议/模式与目标一致时通过到 IssueInput 前（provider 默认）。
	aligned := w11eValidRequest()
	aligned.ConfigRevision = "cfg-1"
	aligned.PolicyRevision = "pol-1"
	aligned.SourceEndpointFamily = string(target.SourceEndpointFamily)
	aligned.UpstreamEndpointFamily = string(target.UpstreamEndpointFamily)
	aligned.UpstreamProtocol = string(target.UpstreamProtocol)
	aligned.UpstreamEndpointMode = target.UpstreamEndpointMode
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runtime.Run(canceled, aligned); err == nil || !strings.Contains(err.Error(), "context") {
		t.Fatalf("canceled 输入签发必须失败: %v", err)
	}
}

func TestW11ERuntimeComparisonAndPolicyArms(t *testing.T) {
	store := newRuntimeTestStore(t)
	defer store.Close()
	target := w11eValidTarget()
	target.ProviderCode = "openai"
	target.ProviderProtocolProfileID = "profile_openai_openai_v1"
	comparison := w11eValidTarget()
	comparison.ProviderCode = "openai"
	comparison.ProviderProtocolProfileID = "profile_openai_openai_v1"
	comparison.UpstreamModel = "gpt-5.6-sol"
	comparison.ConfigRevision = "7"
	comparison.SourceConfigRevision = "5"
	comparison.SourceDispatchRevision = 2
	runtime := &Runtime{
		Store: store, Resolve: func(context.Context, RunRequest) (Target, error) { return target, nil },
		ResolveComparison: func(context.Context, RunRequest) (Target, error) { return comparison, nil },
	}
	request := w11eValidRequest()
	request.TrustedComparison = true
	request.TrustedComparisonAccountID = "acct-2"
	request.TrustedComparisonSystemAccountID = "sys"
	request.TrustedComparisonConfigRevision = "7"
	request.TrustedComparisonDispatchRevision = 3
	request.TrustedComparisonSourceConfigRevision = "5"
	request.TrustedComparisonSourceDispatchRevision = 2
	// 比对修订不完整。
	incomplete := request
	incomplete.TrustedComparisonDispatchRevision = 0
	if _, err := runtime.Run(context.Background(), incomplete); err == nil || !strings.Contains(err.Error(), "comparison revisions are incomplete") {
		t.Fatalf("比对修订不完整必须拒绝: %v", err)
	}
	// 比对目标契约不完整。
	emptyComparison := &Runtime{
		Store: store, Resolve: runtime.Resolve,
		ResolveComparison: func(context.Context, RunRequest) (Target, error) {
			return Target{Endpoint: "https://w11e.invalid", Prompt: "p", DispatchRevision: 1}, nil
		},
	}
	if _, err := emptyComparison.Run(context.Background(), request); err == nil || !strings.Contains(err.Error(), "comparison target is incomplete") {
		t.Fatalf("比对目标不完整必须拒绝: %v", err)
	}
	// 比对修订失配。
	for name, mutate := range map[string]func(*RunRequest){
		"config":          func(r *RunRequest) { r.TrustedComparisonConfigRevision = "stale" },
		"dispatch":        func(r *RunRequest) { r.TrustedComparisonDispatchRevision = 99 },
		"source config":   func(r *RunRequest) { r.TrustedComparisonSourceConfigRevision = "stale" },
		"source dispatch": func(r *RunRequest) { r.TrustedComparisonSourceDispatchRevision = 99 },
	} {
		stale := request
		mutate(&stale)
		if _, err := runtime.Run(context.Background(), stale); err == nil || !strings.Contains(err.Error(), "stale") {
			t.Fatalf("比对 %s 失配必须拒绝: %v", name, err)
		}
	}
	// 比对 profile 不兼容。
	divergent := comparison
	divergent.ProviderProtocolProfileID = "profile_w11e_other"
	divergentRuntime := &Runtime{Store: store, Resolve: runtime.Resolve, ResolveComparison: func(context.Context, RunRequest) (Target, error) { return divergent, nil }}
	if _, err := divergentRuntime.Run(context.Background(), request); err == nil || !strings.Contains(err.Error(), "provider protocol profile is incompatible") {
		t.Fatalf("比对 profile 失配必须拒绝: %v", err)
	}
	// 缺比对解析器。
	noResolver := &Runtime{Store: store, Resolve: runtime.Resolve}
	if _, err := noResolver.Run(context.Background(), request); err == nil || !strings.Contains(err.Error(), "comparison contract is incomplete") {
		t.Fatalf("缺比对解析器必须拒绝: %v", err)
	}
	// 策略/触发器非法。
	plain := w11eValidRequest()
	plain.PenaltyAction = "w11e-action"
	if _, err := runtime.Run(context.Background(), plain); err == nil || !strings.Contains(err.Error(), "penalty action is invalid") {
		t.Fatalf("非法 penalty action 必须拒绝: %v", err)
	}
	plain = w11eValidRequest()
	plain.RecoveryIntervalMinutes = 5
	if _, err := runtime.Run(context.Background(), plain); err == nil || !strings.Contains(err.Error(), "recovery interval is invalid") {
		t.Fatalf("非法恢复间隔必须拒绝: %v", err)
	}
	plain = w11eValidRequest()
	plain.RecoveryIntervalMinutes = 10081
	if _, err := runtime.Run(context.Background(), plain); err == nil || !strings.Contains(err.Error(), "recovery interval is invalid") {
		t.Fatalf("超大恢复间隔必须拒绝: %v", err)
	}
	plain = w11eValidRequest()
	plain.TriggerKind = "w11e-trigger"
	if _, err := runtime.Run(context.Background(), plain); err == nil || !strings.Contains(err.Error(), "trigger is invalid") {
		t.Fatalf("非法触发器必须拒绝: %v", err)
	}
}

func TestW11ERuntimeStorageFailureArms(t *testing.T) {
	store := newRuntimeTestStore(t)
	defer store.Close()
	target := w11eValidTarget()
	// CreateRun 失败：在解析器内删除 runs 表。
	droppedRuns := &Runtime{Store: store, Resolve: func(context.Context, RunRequest) (Target, error) {
		if _, err := store.db.Exec(`DROP TABLE IF EXISTS model_check_runs`); err != nil {
			t.Fatal(err)
		}
		return target, nil
	}}
	if _, err := droppedRuns.Run(context.Background(), w11eValidRequest()); err == nil {
		t.Fatal("CreateRun 失败必须传播")
	}
	// ClaimInput 失败：删除 claims 表。
	store2 := newRuntimeTestStore(t)
	defer store2.Close()
	droppedClaims := &Runtime{Store: store2, Resolve: func(context.Context, RunRequest) (Target, error) {
		if _, err := store2.db.Exec(`DROP TABLE IF EXISTS model_check_execution_claims`); err != nil {
			t.Fatal(err)
		}
		return target, nil
	}}
	if _, err := droppedClaims.Run(context.Background(), w11eValidRequest()); err == nil {
		t.Fatal("ClaimInput 失败必须传播")
	}
	// 探测失败 + CommitOutcome 失败（run_started 事件中删除 outcomes 表）。
	store3 := newRuntimeTestStore(t)
	defer store3.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	failing := w11eValidTarget()
	failing.Endpoint = server.URL
	failingRuntime := &Runtime{Store: store3, Resolve: func(context.Context, RunRequest) (Target, error) { return failing, nil }}
	_, err := failingRuntime.RunStream(context.Background(), w11eValidRequest(), func(event ProgressEvent) {
		if event.Kind == "run_started" {
			if _, dropErr := store3.db.Exec(`DROP TABLE IF EXISTS model_check_outcomes`); dropErr != nil {
				t.Fatal(dropErr)
			}
		}
	})
	if err == nil {
		t.Fatal("CommitOutcome 失败必须传播")
	}
}

func TestW11ERuntimeFullProfileObservationArms(t *testing.T) {
	store := newRuntimeTestStore(t)
	defer store.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	target := w11eValidTarget()
	target.Endpoint = server.URL
	request := w11eValidRequest()
	request.Profile = "full"
	// 观察表缺失 → full profile 观察写入失败。
	dropped := &Runtime{Store: store, Resolve: func(context.Context, RunRequest) (Target, error) {
		if _, err := store.db.Exec(`DROP TABLE IF EXISTS model_check_observations`); err != nil {
			t.Fatal(err)
		}
		return target, nil
	}}
	if _, err := dropped.Run(context.Background(), request); err == nil {
		t.Fatal("观察写入失败必须传播")
	}
	// 空评估与空 family 的直接校验（用未删除表的独立 store）。
	direct := newRuntimeTestStore(t)
	defer direct.Close()
	if err := appendEvaluationObservations(context.Background(), direct, "run-1", "sys", "acct", "openai", "m", "m", "direct", "ok", "ok", 3, nil, time.Now().UTC()); err == nil || !strings.Contains(err.Error(), "observations are empty") {
		t.Fatalf("空评估必须拒绝: %v", err)
	}
	if err := appendEvaluationObservations(context.Background(), direct, "run-1", "sys", "acct", "openai", "m", "m", "direct", "ok", "ok", 3, []modelcheckprobe.Evaluation{{Kind: "  ", Status: "passed"}}, time.Now().UTC()); err == nil || !strings.Contains(err.Error(), "family is empty") {
		t.Fatalf("空 family 必须拒绝: %v", err)
	}
}

func TestW11EAppendObservationIdempotentArms(t *testing.T) {
	store := newRuntimeTestStore(t)
	defer store.Close()
	now := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	nowText := now.Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`INSERT INTO model_check_runs (id,system_account_id,actor_system_account_id,provider_code,target_type,target_id,model,profile,trigger_kind,status,request_summary_json,result_summary_json,policy_snapshot_json,quality_decision_json,probe_set_version,started_at,created_at,updated_at,level,score,max_score,message) VALUES ('w11e-run-obs','sys','actor','openai','account','acct','m','full','manual','running','{}','{}','{}','{}','probe-v1',?,?,?,'unavailable',0,100,'w11e')`, nowText, nowText, nowText); err != nil {
		t.Fatal(err)
	}
	record := ObservationRecord{ID: "w11e-obs-1", RunID: "w11e-run-obs", SystemAccountID: "sys", AccountID: "acct", ProviderCode: "openai", RequestedModel: "m", MappedUpstreamModel: "m", ProbeFamily: "behavior", ObservationStatus: "complete", IdentityStatus: "ok", MappingStatus: "direct", ProtocolStatus: "ok", EvidenceCoverage: 3, CreatedAt: now}
	if err := appendObservationIdempotent(context.Background(), store, record); err != nil {
		t.Fatalf("首次追加必须成功: %v", err)
	}
	if err := appendObservationIdempotent(context.Background(), store, record); err != nil {
		t.Fatalf("幂等重放必须成功: %v", err)
	}
	// created_at 损坏的既有行必须报解析错误。
	if _, err := store.db.Exec(`UPDATE model_check_observations SET created_at='not-a-time' WHERE id=?`, record.ID); err != nil {
		t.Fatal(err)
	}
	if err := appendObservationIdempotent(context.Background(), store, record); err == nil || !strings.Contains(err.Error(), "parse J3b observation created_at") {
		t.Fatalf("损坏 created_at 必须失败: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE model_check_observations SET created_at=? WHERE id=?`, now.Format(time.RFC3339Nano), record.ID); err != nil {
		t.Fatal(err)
	}
	// 字段漂移必须冲突。
	drifted := record
	drifted.EvidenceCoverage = 9
	if err := appendObservationIdempotent(context.Background(), store, drifted); err == nil || !strings.Contains(err.Error(), "conflicts with existing row") {
		t.Fatalf("字段漂移必须冲突: %v", err)
	}
	// 表缺失 → 追加失败且查证失败返回原始错误。
	if _, err := store.db.Exec(`DROP TABLE model_check_observations`); err != nil {
		t.Fatal(err)
	}
	fresh := record
	fresh.ID = "w11e-obs-2"
	if err := appendObservationIdempotent(context.Background(), store, fresh); err == nil {
		t.Fatal("表缺失必须失败")
	}
	// canceled 上下文查询失败。
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	lookupStore := newRuntimeTestStore(t)
	defer lookupStore.Close()
	if err := appendObservationIdempotent(canceled, lookupStore, record); err == nil {
		t.Fatal("canceled 查证必须失败")
	}
	// nil store 防御。
	if err := appendObservationIdempotent(context.Background(), &Store{}, record); err == nil || !strings.Contains(err.Error(), "store is not open") {
		t.Fatalf("未打开 store 必须拒绝: %v", err)
	}
}

func TestW11EHasResponseModelEvidenceArms(t *testing.T) {
	items := []map[string]any{
		{"kind": "trusted_comparison.behavior", "evidence": map[string]any{"success": true, "responseModel": "m"}},
		{"kind": "behavior", "evidence": map[string]any{"success": false, "responseModel": "m"}},
		{"kind": "behavior", "evidence": map[string]any{"success": true}},
	}
	if hasResponseModelEvidence(items) {
		t.Fatal("可信比对与失败项不得构成响应模型证据")
	}
	if hasUndeclaredResponseModelMismatch(items) {
		t.Fatal("无可信响应模型时不得判定未声明失配")
	}
	withEvidence := append(items, map[string]any{"kind": "behavior", "evidence": map[string]any{"success": true, "responseModel": "gpt-5.6-terra"}})
	if !hasResponseModelEvidence(withEvidence) {
		t.Fatal("成功响应模型必须构成证据")
	}
	mismatch := []map[string]any{
		{"kind": "cross_model", "evidence": map[string]any{"success": true, "modelMismatch": true, "responseModel": "other"}},
		{"kind": "behavior", "evidence": map[string]any{"success": true, "modelMismatch": true, "responseModel": ""}},
		{"kind": "behavior", "evidence": map[string]any{"success": true, "modelMismatch": false, "responseModel": "upstream-x"}},
		{"kind": "behavior", "evidence": map[string]any{"success": true, "modelMismatch": true, "responseModel": "upstream-x"}},
	}
	if hasUndeclaredResponseModelMismatch(mismatch[:3]) {
		t.Fatal("前三种形态不得触发未声明失配")
	}
	if !hasUndeclaredResponseModelMismatch(mismatch) {
		t.Fatal("非空响应模型失配必须触发")
	}
	terminal := []map[string]any{
		{"kind": "token_integrity", "evidence": map[string]any{"terminalFailure": true}},
		{"kind": "behavior", "evidence": map[string]any{"terminalFailure": true}},
	}
	if !hasTerminalEvidence(terminal) {
		t.Fatal("非 token 终态证据必须被识别")
	}
	if hasTerminalEvidence(terminal[:1]) {
		t.Fatal("token_integrity 终态证据必须被忽略")
	}
}

func TestW11EStoreOpenSchemaAndHealthArms(t *testing.T) {
	if _, err := OpenStore(Config{}); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("未启用配置必须拒绝: %v", err)
	}
	if _, err := OpenStore(Config{Enabled: true}); err == nil || !strings.Contains(err.Error(), "readiness gates") {
		t.Fatalf("readiness 门禁缺失必须拒绝: %v", err)
	}
	if _, err := OpenStore(Config{Enabled: true, BusinessHandoffConfirmed: true, NodeWriterStopped: true, SchemaReady: true, HealthBoundaryReady: true, RuntimeReady: true, StoreMode: "oracle"}); err == nil || !strings.Contains(err.Error(), "unsupported J3b store mode") {
		t.Fatalf("非法模式必须拒绝: %v", err)
	}
	var nilStore *Store
	if err := nilStore.CheckSchema(context.Background()); err == nil || !strings.Contains(err.Error(), "not open") {
		t.Fatalf("nil store 必须拒绝: %v", err)
	}
	if err := nilStore.Close(); err != nil {
		t.Fatalf("nil store 关闭必须无错: %v", err)
	}
	store := newRuntimeTestStore(t)
	defer store.Close()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.CheckSchema(canceled); err == nil || !strings.Contains(err.Error(), "ping J3b store") {
		t.Fatalf("canceled ping 必须失败: %v", err)
	}
	if err := store.CheckSchema(context.Background()); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("schema 缺失必须失败: %v", err)
	}
}

func TestW11EStoreHealthFactArms(t *testing.T) {
	store := newRuntimeTestStore(t)
	defer store.Close()
	now := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	fact := HealthFact{AccountID: "acct", SystemAccountID: "sys", ProviderCode: "openai", StatHour: "2026-09-16T10:00:00Z", RunID: "run-1", Model: "m", Profile: "quick", ObservedAt: now, Score: 50, Threshold: 70, Level: "warning"}
	var nilStore *Store
	if _, err := nilStore.ApplyHealthFact(context.Background(), fact); err == nil || !strings.Contains(err.Error(), "not open") {
		t.Fatalf("nil store 健康写入必须拒绝: %v", err)
	}
	broken := fact
	broken.AccountID = " "
	if _, err := store.ApplyHealthFact(context.Background(), broken); err == nil || !strings.Contains(err.Error(), "identity is incomplete") {
		t.Fatalf("身份不完整必须拒绝: %v", err)
	}
	broken = fact
	broken.Score = 101
	if _, err := store.ApplyHealthFact(context.Background(), broken); err == nil || !strings.Contains(err.Error(), "identity is incomplete") {
		t.Fatalf("越界分数必须拒绝: %v", err)
	}
	broken = fact
	broken.StatHour = "2026-09-16T10:30:00Z"
	if _, err := store.ApplyHealthFact(context.Background(), broken); err == nil || !strings.Contains(err.Error(), "stat hour is invalid") {
		t.Fatalf("非整点小时必须拒绝: %v", err)
	}
	// 表缺失时写入失败传播。
	if _, err := store.db.Exec(`DROP TABLE account_quality_health_hourly`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyHealthFact(context.Background(), fact); err == nil || !strings.Contains(err.Error(), "upsert J3b health fact") {
		t.Fatalf("表缺失必须失败: %v", err)
	}
	// ReadHealthFact 边臂。
	if _, _, err := nilStore.ReadHealthFact(context.Background(), "acct", "h"); err == nil || !strings.Contains(err.Error(), "not open") {
		t.Fatalf("nil store 读取必须拒绝: %v", err)
	}
	if _, _, err := store.ReadHealthFact(context.Background(), " ", "h"); err == nil || !strings.Contains(err.Error(), "scope is incomplete") {
		t.Fatalf("空作用域必须拒绝: %v", err)
	}
	// MarkHealthSync 边臂。
	if err := store.MarkHealthSync(context.Background(), "run-1", "w11e-state"); err == nil || !strings.Contains(err.Error(), "state is invalid") {
		t.Fatalf("非法状态必须拒绝: %v", err)
	}
	if err := store.MarkHealthSync(context.Background(), "", "failed"); err == nil || !strings.Contains(err.Error(), "identity is incomplete") {
		t.Fatalf("空 run 必须拒绝: %v", err)
	}
	if err := nilStore.MarkHealthSync(context.Background(), "run-1", "failed"); err == nil || !strings.Contains(err.Error(), "identity is incomplete") {
		t.Fatalf("nil store 同步标记必须拒绝: %v", err)
	}
	if err := store.MarkHealthSync(context.Background(), "w11e-missing-run", "failed"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("缺 run 必须拒绝: %v", err)
	}
	// ListHealthSyncRetries 输入校验。
	if _, err := store.ListHealthSyncRetries(context.Background(), 0); err == nil || !strings.Contains(err.Error(), "input is invalid") {
		t.Fatalf("非法 limit 必须拒绝: %v", err)
	}
	if _, err := nilStore.ListHealthSyncRetries(context.Background(), 10); err == nil {
		t.Fatal("nil store 重试扫描必须拒绝")
	}
}

func TestW11ETokenInterceptBaselineArms(t *testing.T) {
	store := newRuntimeTestStore(t)
	defer store.Close()
	valid := TokenInterceptBaselineActivation{CohortKeyHMAC: "hmac-sha256-v1:" + strings.Repeat("a", 64), RequestedModel: "gpt-5.6", TokenizerVersion: "o200k_base@1", ProbeSetVersion: "probe-v1", BaselineVersion: 2, StrongThresholdIntercept: 128, CalibrationNote: "calibrated"}
	// 校验分支。
	invalid := valid
	invalid.CohortKeyHMAC = "hmac-sha256-v1:" + strings.Repeat("g", 64)
	if err := validateTokenInterceptBaselineActivation(invalid); err == nil || !strings.Contains(err.Error(), "cohort key") {
		t.Fatalf("非十六进制 cohort key 必须拒绝: %v", err)
	}
	invalid = valid
	invalid.RequestedModel = strings.Repeat("m", 201)
	if err := validateTokenInterceptBaselineActivation(invalid); err == nil || !strings.Contains(err.Error(), "作用域无效") {
		t.Fatalf("超长模型必须拒绝: %v", err)
	}
	invalid = valid
	invalid.BaselineVersion = 0
	if err := validateTokenInterceptBaselineActivation(invalid); err == nil || !strings.Contains(err.Error(), "版本或阈值无效") {
		t.Fatalf("非法版本必须拒绝: %v", err)
	}
	invalid = valid
	invalid.CalibrationNote = ""
	if err := validateTokenInterceptBaselineActivation(invalid); err == nil || !strings.Contains(err.Error(), "校准记录") {
		t.Fatalf("空记录必须拒绝: %v", err)
	}
	// 存储侧失败。
	var nilStore *Store
	if err := nilStore.ActivateTokenInterceptBaseline(context.Background(), valid); err == nil || !errors.Is(err, ErrTokenInterceptBaselineUnavailable) {
		t.Fatalf("nil store 必须返回 unavailable: %v", err)
	}
	if _, err := store.db.Exec(`CREATE TABLE model_token_intercept_baseline_versions (cohort_key_hmac TEXT,requested_model TEXT,tokenizer_version TEXT,probe_set_version TEXT,baseline_version INTEGER,version_status TEXT,evidence_status TEXT,independent_source_count INTEGER,q90_intercept REAL,strong_threshold_intercept REAL,strong_gate_enabled INTEGER,calibration_note TEXT,updated_at TEXT,PRIMARY KEY(cohort_key_hmac,requested_model,tokenizer_version,probe_set_version,baseline_version))`); err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateTokenInterceptBaseline(context.Background(), valid); err == nil || !errors.Is(err, ErrTokenInterceptBaselineConflict) {
		t.Fatalf("缺失候选必须冲突: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO model_token_intercept_baseline_versions VALUES (?,?,?,?,?,'calibration_pending','stable',11,100,NULL,0,'','2026-09-16T10:00:00Z')`, valid.CohortKeyHMAC, valid.RequestedModel, valid.TokenizerVersion, valid.ProbeSetVersion, valid.BaselineVersion); err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateTokenInterceptBaseline(context.Background(), valid); err != nil {
		t.Fatalf("合格候选必须激活: %v", err)
	}
	if err := store.ActivateTokenInterceptBaseline(context.Background(), valid); err == nil || !errors.Is(err, ErrTokenInterceptBaselineConflict) {
		t.Fatalf("已激活候选再次激活必须冲突: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.ActivateTokenInterceptBaseline(canceled, valid); err == nil {
		t.Fatal("canceled 事务必须失败")
	}
}

func TestW11EEvaluationObservationStatusAndHelpers(t *testing.T) {
	if evaluationObservationStatus("passed") != "complete" || evaluationObservationStatus("skipped") != "partial" || evaluationObservationStatus("w11e-unknown") != "partial" {
		t.Fatal("观察状态映射错误")
	}
	if ownerOrDefault("  ") != "gateway" || ownerOrDefault("owner-1") != "owner-1" {
		t.Fatal("owner 默认值错误")
	}
	if targetOwnerOrDefault("  ", "fallback") != "fallback" || targetOwnerOrDefault(" owner ", "fallback") != "owner" {
		t.Fatal("目标属主默认值错误")
	}
	if endpointFingerprint("  https://x  ") != endpointFingerprint("https://x") {
		t.Fatal("端点指纹必须去除空白")
	}
	if runtimeEnforcementAllowed("manual", RunRequest{}) {
		t.Fatal("manual 未启用物理账户不得执行强制")
	}
	if !runtimeEnforcementAllowed("manual", RunRequest{ManualEnforcementEnabled: true, OwnPhysicalAccount: true}) {
		t.Fatal("manual 完整契约必须允许强制")
	}
	if !runtimeEnforcementAllowed("scheduled", RunRequest{}) || !runtimeEnforcementAllowed("quality_recovery", RunRequest{}) {
		t.Fatal("自动化触发器必须允许强制")
	}
}
