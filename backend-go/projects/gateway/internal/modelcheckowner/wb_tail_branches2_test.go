package modelcheckowner

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

// 凭据信封认证契约：信封结构、base64、nonce/tag 长度、GCM 认证与
// 空明文都必须失败关闭，错误不区分具体形态以防枚举。
func TestWBCredentialEnvelopeAuthenticationMatrix(t *testing.T) {
	secret := "secret"
	valid := testCredentialEnvelope(t, secret, "sk-plain")
	if plain, err := decryptCredentialPlaintext(secret, valid); err != nil || plain != "sk-plain" {
		t.Fatalf("合法信封解密=%q err=%v", plain, err)
	}
	cases := []struct {
		name     string
		envelope string
	}{
		{name: "empty", envelope: ""},
		{name: "part count", envelope: "v1:only:two"},
		{name: "wrong version", envelope: "v2:" + strings.Join(strings.Split(valid, ":")[1:], ":")},
		{name: "bad iv", envelope: "v1:!!!" + valid[9:]},
		{name: "short iv", envelope: "v1:AAAA:" + strings.Split(valid, ":")[2] + ":" + strings.Split(valid, ":")[3]},
		{name: "bad tag", envelope: strings.Join([]string{"v1", strings.Split(valid, ":")[1], "!!!", strings.Split(valid, ":")[3]}, ":")},
		{name: "bad ciphertext", envelope: strings.Join([]string{"v1", strings.Split(valid, ":")[1], strings.Split(valid, ":")[2], "!!!"}, ":")},
		{name: "tampered ciphertext", envelope: func() string {
			parts := strings.Split(valid, ":")
			plain := parts[3]
			bytes := []byte(plain)
			bytes[0] = 'A'
			return strings.Join([]string{"v1", parts[1], parts[2], string(bytes)}, ":")
		}()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := decryptCredentialPlaintext(secret, tc.envelope); err == nil {
				t.Fatalf("%s 必须失败关闭", tc.name)
			}
		})
	}
	t.Run("empty plaintext", func(t *testing.T) {
		if _, err := decryptCredentialPlaintext(secret, testCredentialEnvelope(t, secret, "   ")); err == nil {
			t.Fatal("空明文必须报错")
		}
	})
}

// ActivateTokenInterceptBaseline 候选资格矩阵：状态/证据/独立来源数/q90/
// 阈值任一不满足都必须冲突，不得部分激活。
func TestWBActivateBaselineEligibilityMatrix(t *testing.T) {
	open := func(t *testing.T) (*Store, *sql.DB) {
		t.Helper()
		db := wbOpenMemoryDB(t, []string{`CREATE TABLE model_token_intercept_baseline_versions (
			cohort_key_hmac TEXT NOT NULL, requested_model TEXT NOT NULL, tokenizer_version TEXT NOT NULL,
			probe_set_version TEXT NOT NULL, baseline_version INTEGER NOT NULL, version_status TEXT NOT NULL,
			evidence_status TEXT NOT NULL, independent_source_count INTEGER NOT NULL, q90_intercept REAL,
			strong_threshold_intercept REAL, strong_gate_enabled INTEGER NOT NULL, calibration_note TEXT,
			updated_at TEXT NOT NULL, PRIMARY KEY(cohort_key_hmac,requested_model,tokenizer_version,probe_set_version,baseline_version))`})
		store := &Store{db: db, mode: "sqlite"}
		return store, db
	}
	input := func(strong float64) TokenInterceptBaselineActivation {
		return TokenInterceptBaselineActivation{CohortKeyHMAC: "hmac-sha256-v1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", RequestedModel: "m", TokenizerVersion: "t", ProbeSetVersion: "p", BaselineVersion: 2, StrongThresholdIntercept: strong, CalibrationNote: "note"}
	}
	cases := []struct {
		name     string
		status   string
		evidence string
		sources  int
		q90      any
		strong   float64
	}{
		{name: "wrong status", status: "retired", evidence: "stable", sources: 10, q90: 120.0, strong: 130},
		{name: "unstable evidence", status: "calibration_pending", evidence: "shaky", sources: 10, q90: 120.0, strong: 130},
		{name: "too few sources", status: "calibration_pending", evidence: "stable", sources: 9, q90: 120.0, strong: 130},
		{name: "missing q90", status: "calibration_pending", evidence: "stable", sources: 10, q90: nil, strong: 130},
		{name: "strong below q90", status: "calibration_pending", evidence: "stable", sources: 10, q90: 120.0, strong: 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, db := open(t)
			const cohort = "hmac-sha256-v1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			if _, err := db.Exec(`INSERT INTO model_token_intercept_baseline_versions (cohort_key_hmac,requested_model,tokenizer_version,probe_set_version,baseline_version,version_status,evidence_status,independent_source_count,q90_intercept,strong_gate_enabled,updated_at) VALUES (?,?,?,?,?,?,?,?,?,'',?)`, cohort, "m", "t", "p", 2, tc.status, tc.evidence, tc.sources, tc.q90, "2026-09-06T00:00:00Z"); err != nil {
				t.Fatal(err)
			}
			if err := store.ActivateTokenInterceptBaseline(context.Background(), input(tc.strong)); !errors.Is(err, ErrTokenInterceptBaselineConflict) {
				t.Fatalf("%s 必须冲突: err=%v", tc.name, err)
			}
		})
	}
}

// 详情读取契约：损坏的检查项证据必须让详情失败关闭而不是输出半份数据。
func TestWBRunGetRunFailsClosedOnMalformedCheckEvidence(t *testing.T) {
	store := &Store{db: wbOpenMemoryDB(t, runtimeTestDDL()), mode: "sqlite"}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	run := RunRecord{ID: "run-bad-check", SystemAccountID: "sys", ActorSystemAccountID: "actor", ProviderCode: "openai", TargetType: "account", TargetID: "acct", Model: "m", Profile: "quick", TriggerKind: "manual", ProbeSetVersion: "probe-v1", StartedAt: now, RequestSummary: []byte(`{}`), PolicySnapshot: []byte(`{}`)}
	if err := store.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO model_check_items (id,run_id,item_key,item_type,status,score,max_score,evidence_summary_json,created_at,updated_at) VALUES ('item-1','run-bad-check','stability','stability','passed',1,100,'[1]','2026-09-07T09:00:00Z','2026-09-07T09:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{Store: store}
	if _, _, err := runtime.GetRun(ctx, run.ID); err == nil || !strings.Contains(err.Error(), "must be a JSON object") {
		t.Fatalf("损坏证据必须失败关闭: err=%v", err)
	}
}

// ProjectTrust 收据冲突契约：同观测不同创建时间的重放必须失败关闭。
func TestWBStoreProjectTrustReceiptConflict(t *testing.T) {
	store := &Store{db: wbOpenMemoryDB(t, runtimeTestDDL()), mode: "sqlite"}
	defer store.Close()
	if _, err := store.db.Exec(`INSERT INTO model_check_observations (id,run_id,system_account_id,account_id,provider_code,requested_model,mapped_upstream_model,probe_family,observation_status,identity_status,mapping_status,protocol_status,evidence_coverage,created_at) VALUES ('obs-x','run-t','sys','acct','openai','m','m','stability','complete','consistent','direct','consistent',10,'2026-09-07T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO model_trust_observation_receipts (observation_id,observation_created_at,processed_at) VALUES ('obs-x','1999-01-01T00:00:00Z','1999-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	projection := TrustProjection{RunID: "run-t", SystemAccountID: "sys", AccountID: "acct", RequestedModel: "m", Report: wbValidTrustReport()}
	if err := store.ProjectTrust(context.Background(), projection); err == nil || !strings.Contains(err.Error(), "receipt replay conflicts") {
		t.Fatalf("收据冲突必须失败关闭: err=%v", err)
	}
	t.Run("malformed instant", func(t *testing.T) {
		if _, err := store.db.Exec(`INSERT INTO model_check_observations (id,run_id,system_account_id,account_id,provider_code,requested_model,mapped_upstream_model,probe_family,observation_status,identity_status,mapping_status,protocol_status,evidence_coverage,created_at) VALUES ('obs-y','run-t2','sys','acct','openai','m','m','stability','complete','consistent','direct','consistent',10,'not-a-time')`); err != nil {
			t.Fatal(err)
		}
		bad := projection
		bad.RunID = "run-t2"
		if err := store.ProjectTrust(context.Background(), bad); err == nil || !strings.Contains(err.Error(), "malformed") {
			t.Fatalf("畸形时间戳必须失败关闭: err=%v", err)
		}
	})
}

// 执行适配器失败契约：健康发布中的执行错误必须保留可重试的 failed 状态。
func TestWBRunHealthPublishingSurvivesEnforcementFailure(t *testing.T) {
	store := &Store{db: wbOpenMemoryDB(t, wbHealthRuntimeDDL(t)), mode: "sqlite"}
	defer store.Close()
	statHour, err := NewHealthStatHourFunc("UTC")
	if err != nil {
		t.Fatal(err)
	}
	store.HealthStatHour = statHour
	projector := &QualityProjector{Store: store, Enforcement: EnforcementApplierFunc(func(context.Context, QualityEnforcement) error {
		return errors.New("Business 执行冲突")
	})}
	events := make(chan ProgressEvent, 4)
	result, _ := wbRunQualityFailingProbeWithEvents(t, store, projector, events)
	var syncStatus string
	if err := store.db.QueryRow(`SELECT quality_health_sync_status FROM model_check_runs WHERE id=?`, result.RunID).Scan(&syncStatus); err != nil || syncStatus != "failed" {
		t.Fatalf("执行失败后的同步状态=%q err=%v", syncStatus, err)
	}
	found := false
	for {
		select {
		case event := <-events:
			if event.Kind == "health_sync_failed" {
				found = true
			}
		default:
			if !found {
				t.Fatal("执行失败必须发出 health_sync_failed")
			}
			return
		}
	}
}

// full profile 运行契约：观测行随 run 一起落库，供信任聚合消费。
func TestWBRunFullProfilePersistsObservations(t *testing.T) {
	store := &Store{db: wbOpenMemoryDB(t, runtimeTestDDL()), mode: "sqlite"}
	defer store.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"OK-MODEL-CHECK","usage":{"total_tokens":2}}`))
	}))
	defer server.Close()
	now := time.Date(2026, 9, 7, 11, 0, 0, 0, time.UTC)
	runtime := &Runtime{
		Store: store, OwnerID: "wb-obs-conflict", Now: func() time.Time { return now },
		Resolve: func(context.Context, RunRequest) (Target, error) {
			return Target{Endpoint: server.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Prompt: "hello", DispatchRevision: 1}, nil
		},
	}
	// 观测冲突分支由 TestWBAppendObservationIdempotentErrorBranches 直接覆盖；
	// 这里验证 full profile 的完整落库链路。
	result, err := runtime.Run(context.Background(), RunRequest{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "full", ProviderCode: "openai", Threshold: 40, ConfigRevision: "cfg-1", PolicyRevision: "pol-1", ManualEnforcementEnabled: true, OwnPhysicalAccount: true})
	if err != nil || result.Status != string(RunCompleted) {
		t.Fatalf("full profile result=%+v err=%v", result, err)
	}
	var observations int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM model_check_observations WHERE run_id=?`, result.RunID).Scan(&observations); err != nil || observations == 0 {
		t.Fatalf("full profile 观测数=%d err=%v", observations, err)
	}
}

// wbRunQualityFailingProbeWithEvents 与 wbRunQualityFailingProbe 等价，
// 但通过 channel 把进度事件交给调用方断言。
func wbRunQualityFailingProbeWithEvents(t *testing.T, store *Store, projector *QualityProjector, events chan<- ProgressEvent) (RunResult, []ProgressEvent) {
	t.Helper()
	collected := make([]ProgressEvent, 0)
	result, _ := wbRunQualityFailingProbe(t, store, projector, func(event ProgressEvent) {
		events <- event
		collected = append(collected, event)
	})
	return result, collected
}

// memorySchedulerSource limit 边界与空任务族。
func TestWBMemorySchedulerSourceEmptyKind(t *testing.T) {
	source := &memorySchedulerSource{}
	tasks, err := source.Claim(context.Background(), SchedulerScheduled, time.Now(), 3)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("空源认领=%+v err=%v", tasks, err)
	}
}
