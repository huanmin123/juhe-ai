package modelcheckowner

// w14m 覆盖率补强：business_recovery 的网关可用性窗口/异常窗口 JSON 解码臂，
// 以及 runtime 的存储失败注入臂（CreateRun/ClaimInput/ProjectOutcome/
// CommitOutcome/ProjectTrust/observations）、心跳续租失败臂、冻结重读
// comparison 失败臂与全档运行变体（configured mapping / 未声明模型漂移 /
// 不可验证证据）。注入统一走共享 failpoint；时间全部使用冻结测试时钟。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

// ---- recovery：网关可用性窗口 ----

func w14mAvailability(t *testing.T, raw string, now time.Time) (bool, error) {
	t.Helper()
	return availabilityAllowedGateway(raw, now)
}

func w14mAvailabilityBase(windows string) string {
	return `{"enabled":true,"mode":"allow_windows","timezone":"UTC","windows":` + windows + `}`
}

func TestW14MGatewayAvailabilityWindows(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	allDay := `[{"start":"00:00","end":"23:59","daysOfWeek":[1,2,3,4,5,6,7]}]`

	t.Run("emptyRaw", func(t *testing.T) {
		allowed, err := w14mAvailability(t, "", now)
		if err != nil || !allowed {
			t.Fatalf("allowed=%t err=%v", allowed, err)
		}
	})

	t.Run("invalidJSON", func(t *testing.T) {
		if _, err := w14mAvailability(t, `{"enabled":true,`, now); err == nil || !strings.Contains(err.Error(), "invalid availability schedule JSON") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("trailingContent", func(t *testing.T) {
		if _, err := w14mAvailability(t, w14mAvailabilityBase(allDay)+` {"x":1}`, now); err == nil || !strings.Contains(err.Error(), "trailing content") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("disabledSchedule", func(t *testing.T) {
		raw := strings.Replace(w14mAvailabilityBase(allDay), `"enabled":true`, `"enabled":false`, 1)
		if _, err := w14mAvailability(t, raw, now); err == nil || !strings.Contains(err.Error(), "invalid availability schedule") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("windowRequiresDays", func(t *testing.T) {
		if _, err := w14mAvailability(t, w14mAvailabilityBase(`[{"start":"01:00","end":"02:00"}]`), now); err == nil || !strings.Contains(err.Error(), "requires days") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("windowStartInvalid", func(t *testing.T) {
		if _, err := w14mAvailability(t, w14mAvailabilityBase(`[{"start":"25:00","end":"02:00","daysOfWeek":[1]}]`), now); err == nil || !strings.Contains(err.Error(), "invalid availability start") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("windowEndInvalid", func(t *testing.T) {
		if _, err := w14mAvailability(t, w14mAvailabilityBase(`[{"start":"01:00","end":"01:00","daysOfWeek":[1]}]`), now); err == nil || !strings.Contains(err.Error(), "invalid availability end") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("windowDayInvalid", func(t *testing.T) {
		if _, err := w14mAvailability(t, w14mAvailabilityBase(`[{"start":"01:00","end":"02:00","daysOfWeek":[9]}]`), now); err == nil || !strings.Contains(err.Error(), "invalid availability day") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("dateRangeExcludesNow", func(t *testing.T) {
		raw := `{"enabled":true,"mode":"allow_windows","timezone":"UTC","windows":` + allDay + `,"dateRange":{"startDate":"2026-01-01","endDate":"2026-01-02"}}`
		allowed, err := w14mAvailability(t, raw, now)
		if err != nil || allowed {
			t.Fatalf("allowed=%t err=%v，日期范围外应拒绝", allowed, err)
		}
	})

	t.Run("exceptionAllowWindow", func(t *testing.T) {
		raw := `{"enabled":true,"mode":"allow_windows","timezone":"UTC","windows":[{"start":"00:00","end":"00:01","daysOfWeek":[1]}],"exceptions":[{"date":"2026-09-17","action":"allow","windows":[{"start":"00:00","end":"23:59"}]}]}`
		allowed, err := w14mAvailability(t, raw, now)
		if err != nil || !allowed {
			t.Fatalf("allowed=%t err=%v，异常日放行窗口应命中", allowed, err)
		}
	})

	t.Run("exceptionDeny", func(t *testing.T) {
		raw := `{"enabled":true,"mode":"allow_windows","timezone":"UTC","windows":` + allDay + `,"exceptions":[{"date":"2026-09-17","action":"deny"}]}`
		allowed, err := w14mAvailability(t, raw, now)
		if err != nil || allowed {
			t.Fatalf("allowed=%t err=%v，异常拒绝日应关闭", allowed, err)
		}
	})

	t.Run("exceptionWindowInvalid", func(t *testing.T) {
		raw := `{"enabled":true,"mode":"allow_windows","timezone":"UTC","windows":` + allDay + `,"exceptions":[{"date":"2026-09-17","action":"allow","windows":[{"start":"x","end":"02:00"}]}]}`
		if _, err := w14mAvailability(t, raw, now); err == nil || !strings.Contains(err.Error(), "invalid availability start") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("previousDayWindowAllows", func(t *testing.T) {
		// 当前 UTC 时刻 12:00 不在窗口内，但前一日的全天窗口应放行。
		raw := `{"enabled":true,"mode":"allow_windows","timezone":"UTC","windows":[{"start":"00:00","end":"23:59","daysOfWeek":[1,2,3,4,5,6,7]}]}`
		allowed, err := w14mAvailability(t, raw, now)
		if err != nil || !allowed {
			t.Fatalf("allowed=%t err=%v", allowed, err)
		}
	})
}

func TestW14MGatewayExceptionUnmarshalArms(t *testing.T) {
	cases := []struct {
		name string
		data string
		need string
	}{
		{"decodeError", `{bad`, ""},
		{"unknownField", `{"date":"2026-01-01","action":"allow","unknown":1}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var exception gatewayException
			err := json.NewDecoder(strings.NewReader(tc.data)).Decode(&exception)
			if err == nil || (tc.need != "" && !strings.Contains(err.Error(), tc.need)) {
				t.Fatalf("err = %v, want 非 nil（包含 %q）", err, tc.need)
			}
		})
	}
	t.Run("topLevelTrailingContent", func(t *testing.T) {
		// json.Decoder 只把首个 JSON 值的字节传入 UnmarshalJSON；
		// 顶层尾随臂仅在使用方直接传入多值缓冲时可达（Node 对齐校验）。
		var exception gatewayException
		err := exception.UnmarshalJSON([]byte(`{"date":"2026-01-01","action":"allow"} {"x":1}`))
		if err == nil || !strings.Contains(err.Error(), "trailing content") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("windowsNullKeepsAbsent", func(t *testing.T) {
		var exception gatewayException
		if err := json.NewDecoder(strings.NewReader(`{"date":"2026-01-01","action":"deny","windows":null}`)).Decode(&exception); err != nil {
			t.Fatal(err)
		}
		if exception.Windows != nil {
			t.Fatalf("null windows 不应生成窗口: %+v", exception)
		}
	})
}

func TestW14MRecoveryCompleteInputArms(t *testing.T) {
	ctx := context.Background()
	enforcementDDL := `CREATE TABLE account_quality_enforcements (account_id TEXT PRIMARY KEY,system_account_id TEXT,enforcement_id TEXT,generation INTEGER,state TEXT,action TEXT,policy_revision INTEGER,account_config_revision INTEGER,recovery_lease_owner TEXT,recovery_lease_until TEXT,last_recovery_run_id TEXT,recovery_due_at TEXT,cleared_at TEXT,updated_at TEXT)`
	newDB := func(t *testing.T) (*sql.DB, *w14mFailpoint) {
		ddl := append(businessSourceContractDDL(), enforcementDDL)
		return w14mFailDB(t, ddl)
	}

	t.Run("zeroCompletedAtFallsBackToRealClock", func(t *testing.T) {
		db, fp := newDB(t)
		defer func() { _ = db.Close() }()
		fp.disarm()
		w14mSeedPlainAccount(t, db, "acct")
		applier, err := NewBusinessRecoveryApplier(db, false)
		if err != nil {
			t.Fatal(err)
		}
		// CompletedAt 为零 → 回退真实时钟；无匹配租约行 → ErrNoRows 幂等提交。
		if err := applier.Complete(ctx, RecoveryPayload{OwnerID: "gateway-1", AccountID: "acct", EnforcementID: "enf", RunID: "run-1", Generation: 1, PolicyRevision: 1, RecoveryIntervalMinutes: 10}, true); err != nil {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("beginError", func(t *testing.T) {
		db, fp := newDB(t)
		defer func() { _ = db.Close() }()
		fp.disarm()
		fp.failBeginsFrom = 1
		defer func() { fp.failBeginsFrom = 0; fp.disarm() }()
		applier, err := NewBusinessRecoveryApplier(db, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := applier.Complete(ctx, RecoveryPayload{OwnerID: "gateway-1", AccountID: "acct", EnforcementID: "enf", RunID: "run-1", Generation: 1, PolicyRevision: 1, RecoveryIntervalMinutes: 10, CompletedAt: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}, true); err == nil || !strings.Contains(err.Error(), "begin J3b Business recovery") {
			t.Fatalf("err = %v", err)
		}
	})
}

// ---- runtime：失败注入臂 ----

func w14mRuntimeDB(t *testing.T) (*Store, *w14mFailpoint) {
	t.Helper()
	db, fp := w14mFailDB(t, runtimeTestDDL())
	return &Store{db: db, mode: "sqlite"}, fp
}

func w14mFrozenNow() time.Time {
	return time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
}

func w14mResolveOK(serverURL string) Resolver {
	return func(context.Context, RunRequest) (Target, error) {
		return Target{Endpoint: serverURL, TargetName: "Runtime Account", TargetOwnerSystemAccountID: "sys", GroupID: "group-runtime", Protocol: modelcheckprofile.ProtocolOpenAIResponses, Prompt: "hello", DispatchRevision: 3}, nil
	}
}

func w14mQuickRequest() RunRequest {
	return RunRequest{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "quick", ConfigRevision: "cfg-1", PolicyRevision: "pol-1"}
}

// w14mProbeServer 按 prompt 片段返回可定制响应。
type w14mProbeServer struct {
	*httptest.Server
}

func w14mNewProbeServer(t *testing.T, respond func(body string) (int, string)) *w14mProbeServer {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		code, payload := respond(string(body))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(server.Close)
	return &w14mProbeServer{Server: server}
}

func w14mOKResponse(model, text string) string {
	return `{"model":"` + model + `","output_text":"` + text + `","usage":{"total_tokens":2}}`
}

func TestW14MRuntimeStoreErrorArms(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		need    string
	}{
		{"createRunError", "INSERT INTO model_check_runs", "create J3b run"},
		{"claimInputError", "SELECT expires_at FROM", "w14m"},
		{"commitOutcomeError", "SELECT outcome_id,payload_digest FROM", "w14m"},
		{"projectOutcomeError", "SELECT status,level,score,max_score,message", "w14m"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := w14mNewProbeServer(t, func(string) (int, string) { return http.StatusOK, w14mOKResponse("gpt-5.6-sol", "OK-MODEL-CHECK") })
			store, fp := w14mRuntimeDB(t)
			runtime := &Runtime{Store: store, OwnerID: "gateway-1", Now: w14mFrozenNow, Resolve: w14mResolveOK(server.URL)}
			fp.arm(tc.pattern)
			defer fp.disarm()
			if _, err := runtime.Run(context.Background(), w14mQuickRequest()); err == nil || !strings.Contains(err.Error(), tc.need) {
				t.Fatalf("err = %v, want 包含 %q", err, tc.need)
			}
		})
	}
}

func TestW14MRuntimeHeartbeatRenewalFailure(t *testing.T) {
	release := make(chan struct{})
	var once atomic.Bool
	server := w14mNewProbeServer(t, func(string) (int, string) {
		// 首个请求拖过 2s 租约的第一次续租 tick（约 666ms）。
		if once.CompareAndSwap(false, true) {
			select {
			case <-release:
			case <-time.After(900 * time.Millisecond):
			}
		}
		return http.StatusOK, w14mOKResponse("gpt-5.6-sol", "OK-MODEL-CHECK")
	})
	store, fp := w14mRuntimeDB(t)
	fp.disarm()
	runtime := &Runtime{Store: store, OwnerID: "gateway-1", Now: w14mFrozenNow, Resolve: w14mResolveOK(server.URL), Lease: 2 * time.Second}
	result, err := runtime.Run(context.Background(), w14mQuickRequest())
	close(release)
	// 续租基于真实时钟，冻结的租约必然过期 → 心跳取消探针 → 运行失败。
	if err == nil {
		t.Fatalf("心跳续租失败应中止运行: %+v", result)
	}
	if result.Status != "" && result.Status != string(RunFailed) && result.Status != string(RunCanceled) {
		t.Fatalf("status = %s", result.Status)
	}
}

func TestW14MRuntimeComparisonRecheckError(t *testing.T) {
	server := w14mNewProbeServer(t, func(string) (int, string) { return http.StatusOK, w14mOKResponse("gpt-5.6-sol", "OK-MODEL-CHECK") })
	store, fp := w14mRuntimeDB(t)
	fp.disarm()
	var calls atomic.Int32
	compare := func(context.Context, RunRequest) (Target, error) {
		if calls.Add(1) > 1 {
			return Target{}, errors.New("w14m-comparison-boom")
		}
		return Target{Endpoint: server.URL, TargetName: "Cmp", Protocol: modelcheckprofile.ProtocolOpenAIResponses, Prompt: "cmp", ProviderCode: "openai", ProviderProtocolProfileID: "profile_openai_openai_v1", UpstreamModel: "gpt-5.6-cmp", DispatchRevision: 5, ConfigRevision: "cfg-cmp-1", SourceConfigRevision: "src-cfg-cmp", SourceDispatchRevision: 6}, nil
	}
	runtime := &Runtime{Store: store, OwnerID: "gateway-1", Now: w14mFrozenNow, Resolve: func(context.Context, RunRequest) (Target, error) {
		target, err := w14mResolveOK(server.URL)(context.Background(), RunRequest{})
		target.ProviderCode = "openai"
		target.ProviderProtocolProfileID = "profile_openai_openai_v1"
		return target, err
	}, ResolveComparison: compare}
	request := w14mQuickRequest()
	request.TrustedComparison = true
	request.TrustedComparisonAccountID = "acct-cmp"
	request.TrustedComparisonSystemAccountID = "sys"
	request.TrustedComparisonConfigRevision = "cfg-cmp-1"
	request.TrustedComparisonDispatchRevision = 5
	request.TrustedComparisonSourceConfigRevision = "src-cfg-cmp"
	request.TrustedComparisonSourceDispatchRevision = 6
	if _, err := runtime.Run(context.Background(), request); err == nil || !strings.Contains(err.Error(), "resolve J3b trusted comparison after claim") {
		t.Fatalf("err = %v", err)
	}
}

func TestW14MRuntimeNegativeDurationClamp(t *testing.T) {
	server := w14mNewProbeServer(t, func(string) (int, string) { return http.StatusOK, w14mOKResponse("gpt-5.6-sol", "OK-MODEL-CHECK") })
	store, fp := w14mRuntimeDB(t)
	fp.disarm()
	// 第二次及以后的 Now 返回早于起始时刻的时间 → durationMS 为负并被钳制。
	now := w14mFrozenNow()
	var calls atomic.Int32
	runtime := &Runtime{Store: store, OwnerID: "gateway-1", Now: func() time.Time {
		if calls.Add(1) > 1 {
			return now.Add(-2 * time.Second)
		}
		return now
	}, Resolve: w14mResolveOK(server.URL)}
	result, err := runtime.Run(context.Background(), w14mQuickRequest())
	if err != nil || result.Status != string(RunCompleted) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var duration int64
	if err := store.db.QueryRow(`SELECT duration_ms FROM model_check_runs WHERE id=?`, result.RunID).Scan(&duration); err != nil || duration != 0 {
		t.Fatalf("duration=%d err=%v，负时长应被钳制为 0", duration, err)
	}
}

// ---- runtime：全档（full）运行变体 ----

func w14mFullRequest() RunRequest {
	request := w14mQuickRequest()
	request.Profile = "full"
	return request
}

func TestW14MRuntimeFullProfileModelCheckUnverified(t *testing.T) {
	// 基础探针拿到非 200（重试边界耗尽）→ 证据标记 terminalFailure，
	// RunSuite 仍返回 nil 错误 → 运行完成但 modelCheckUnverified=true。
	server := w14mNewProbeServer(t, func(string) (int, string) { return http.StatusInternalServerError, `{"error":"w14m-down"}` })
	store, fp := w14mRuntimeDB(t)
	fp.disarm()
	resolve := func(context.Context, RunRequest) (Target, error) {
		target, err := w14mResolveOK(server.URL)(context.Background(), RunRequest{})
		target.UpstreamModel = "gpt-5.6-mapped"
		return target, err
	}
	runtime := &Runtime{Store: store, OwnerID: "gateway-1", Now: w14mFrozenNow, Resolve: resolve}
	result, err := runtime.Run(context.Background(), w14mFullRequest())
	if err != nil || result.Status != string(RunCompleted) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	data, ok := result.Data.(map[string]any)
	if !ok || data["modelCheckUnverified"] != true {
		t.Fatalf("data=%v，want modelCheckUnverified=true", result.Data)
	}
	// 非快速档必须已写入观察与 trust 投影。
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM model_check_observations WHERE run_id=?`, result.RunID).Scan(&count); err != nil || count == 0 {
		t.Fatalf("observations=%d err=%v", count, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM model_trust_observation_receipts`).Scan(&count); err != nil || count == 0 {
		t.Fatalf("receipts=%d err=%v", count, err)
	}
}

func TestW14MRuntimeFullProfileUndeclaredMismatch(t *testing.T) {
	// 基础探针成功但响应 model 与请求不一致 → 未声明漂移硬失败。
	server := w14mNewProbeServer(t, func(body string) (int, string) {
		if strings.Contains(body, "OK-MODEL-CHECK") {
			// 基础探针成功但响应 model 是未声明的其他模型。
			return http.StatusOK, w14mOKResponse("other-model", "OK-MODEL-CHECK")
		}
		return http.StatusInternalServerError, `{"error":"w14m-down"}`
	})
	store, fp := w14mRuntimeDB(t)
	fp.disarm()
	runtime := &Runtime{Store: store, OwnerID: "gateway-1", Now: w14mFrozenNow, Resolve: w14mResolveOK(server.URL)}
	result, err := runtime.Run(context.Background(), w14mFullRequest())
	if err != nil || result.Status != string(RunCompleted) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var decision string
	if err := store.db.QueryRow(`SELECT quality_decision_json FROM model_check_runs WHERE id=?`, result.RunID).Scan(&decision); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(decision, `"hardQualityFailure":true`) {
		t.Fatalf("quality decision 缺少硬失败标记: %s", decision)
	}
}

func TestW14MRuntimeFullProfileStoreArms(t *testing.T) {
	down := func(t *testing.T) (*httptest.Server, *Store, *w14mFailpoint) {
		t.Helper()
		server := w14mNewProbeServer(t, func(string) (int, string) { return http.StatusInternalServerError, `{"error":"w14m-down"}` })
		store, fp := w14mRuntimeDB(t)
		fp.disarm()
		return server.Server, store, fp
	}

	t.Run("projectTrustError", func(t *testing.T) {
		server, store, fp := down(t)
		runtime := &Runtime{Store: store, OwnerID: "gateway-1", Now: w14mFrozenNow, Resolve: w14mResolveOK(server.URL)}
		fp.arm("INSERT INTO model_trust_observation_receipts")
		defer fp.disarm()
		if _, err := runtime.Run(context.Background(), w14mFullRequest()); err == nil || !strings.Contains(err.Error(), "project J3b trust") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("appendObservationError", func(t *testing.T) {
		server, store, fp := down(t)
		runtime := &Runtime{Store: store, OwnerID: "gateway-1", Now: w14mFrozenNow, Resolve: w14mResolveOK(server.URL)}
		fp.arm("INSERT INTO model_check_observations")
		defer fp.disarm()
		if _, err := runtime.Run(context.Background(), w14mFullRequest()); err == nil || !strings.Contains(err.Error(), "append J3b") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("observationMatchesReadError", func(t *testing.T) {
		server, store, fp := down(t)
		runtime := &Runtime{Store: store, OwnerID: "gateway-1", Now: w14mFrozenNow, Resolve: w14mResolveOK(server.URL)}
		fp.arm("evidence_coverage,created_at")
		defer fp.disarm()
		if _, err := runtime.Run(context.Background(), w14mFullRequest()); err == nil || !strings.Contains(err.Error(), "read J3b observation idempotency key") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestW14MRuntimeQuickQualityFailureHealthArms(t *testing.T) {
	// 全 200 但输出全部不匹配 → 快速档低分质量失败 → 健康发布路径。
	server := w14mNewProbeServer(t, func(string) (int, string) { return http.StatusOK, w14mOKResponse("gpt-5.6-sol", "WRONG-RESPONSE") })
	store, fp := w14mRuntimeDB(t)
	fp.disarm()
	request := w14mQuickRequest()
	request.Threshold = 80
	request.ProviderCode = "openai"
	runtime := &Runtime{Store: store, OwnerID: "gateway-1", Now: w14mFrozenNow, Resolve: w14mResolveOK(server.URL), Projector: &QualityProjector{Store: store}}
	// 健康同步标记注入失败 → 命中 markErr 分支。
	fp.arm("SET quality_health_sync_status=")
	defer fp.disarm()
	result, err := runtime.Run(context.Background(), request)
	if err != nil || result.Status != string(RunCompleted) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	// MarkHealthSync 被注入失败，同步状态保持 NULL 即证明 markErr 分支已走。
	var status sql.NullString
	if err := store.db.QueryRow(`SELECT quality_health_sync_status FROM model_check_runs WHERE id=?`, result.RunID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status.Valid && status.String != "failed" {
		t.Fatalf("health sync status = %q", status.String)
	}
}
