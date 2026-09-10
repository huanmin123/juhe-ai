package modelcheckowner

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

// wbRunStartedServer 提供模型检测探针所需的最小上游应答，
// 按 probe 家族返回不同的固定内容，保证结果可重放。
type wbRunStartedServer struct {
	t *testing.T
}

func (s *wbRunStartedServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.t.Errorf("读取探针请求失败: %v", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	response := `{"model":"gpt-5.6-sol","output_text":"OK-MODEL-CHECK","usage":{"total_tokens":2}}`
	switch {
	case strings.Contains(string(body), "record_model_check"):
		response = `{"model":"gpt-5.6-sol","output":[{"type":"function_call","name":"record_model_check","arguments":"{\"code\":\"ok\",\"count\":1}"}],"usage":{"total_tokens":2}}`
	case strings.Contains(string(body), "VECTOR"):
		response = `{"model":"gpt-5.6-sol","output_text":"VECTOR","usage":{"total_tokens":2}}`
	case strings.Contains(string(body), "status"):
		response = `{"model":"gpt-5.6-sol","output_text":"{\"status\":\"ok\",\"value\":7}","usage":{"total_tokens":2}}`
	}
	_, _ = w.Write([]byte(response))
}

// RunStream 契约：与 Run 共享同一执行管线，但必须把 run_started /
// run_completed 进度同时投递给请求级回调与 Runtime.OnEvent 订阅者，
// 并落库同质的 run/outcome 记录。
func TestWBRunStreamDeliversProgressAndPersistsOutcome(t *testing.T) {
	store := &Store{db: wbOpenMemoryDB(t, runtimeTestDDL()), mode: "sqlite"}
	defer store.Close()
	var mu sync.Mutex
	server := httptest.NewServer(&wbRunStartedServer{t: t})
	defer server.Close()
	now := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	var callbackEvents, sinkEvents []string
	runtime := &Runtime{
		Store:   store,
		OwnerID: "wb-stream",
		Now:     func() time.Time { return now },
		OnEvent: func(event ProgressEvent) {
			mu.Lock()
			sinkEvents = append(sinkEvents, event.Kind)
			mu.Unlock()
		},
		Resolve: func(context.Context, RunRequest) (Target, error) {
			return Target{Endpoint: server.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Prompt: "hello", DispatchRevision: 1}, nil
		},
	}
	result, err := runtime.RunStream(context.Background(), RunRequest{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "quick", ConfigRevision: "cfg-1", PolicyRevision: "pol-1"}, func(event ProgressEvent) {
		mu.Lock()
		callbackEvents = append(callbackEvents, event.Kind)
		mu.Unlock()
	})
	if err != nil || result.Status != string(RunCompleted) || result.RunID == "" {
		t.Fatalf("RunStream result=%+v err=%v", result, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(callbackEvents) < 2 || callbackEvents[0] != "run_started" || callbackEvents[len(callbackEvents)-1] != "run_completed" {
		t.Fatalf("请求级进度事件=%v", callbackEvents)
	}
	if len(sinkEvents) != len(callbackEvents) {
		t.Fatalf("Runtime.OnEvent 事件=%v 与请求级回调=%v 不一致", sinkEvents, callbackEvents)
	}
	var status string
	if err := store.db.QueryRow(`SELECT status FROM model_check_runs WHERE id=?`, result.RunID).Scan(&status); err != nil || status != string(RunCompleted) {
		t.Fatalf("流式执行后的持久状态=%q err=%v", status, err)
	}
}

// AppendItem 契约：只允许向 running 状态的 run 追加条目；
// 终态 run、缺失 run 与非法条目都必须失败关闭。
func TestWBStoreAppendItemOnlyPersistsWhileRunRunning(t *testing.T) {
	store := &Store{db: wbOpenMemoryDB(t, runtimeTestDDL()), mode: "sqlite"}
	defer store.Close()
	now := time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)
	run := RunRecord{ID: "run-append", SystemAccountID: "sys", ActorSystemAccountID: "actor", ProviderCode: "openai", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "quick", TriggerKind: "manual", ProbeSetVersion: "probe-v1", StartedAt: now, RequestSummary: []byte(`{}`), PolicySnapshot: []byte(`{}`)}
	if err := store.CreateRun(context.Background(), run); err != nil {
		t.Fatalf("创建 run 失败: %v", err)
	}
	item := ItemRecord{ID: "run-append-item-0001", RunID: run.ID, ItemKey: "stability", ItemType: "stability", Status: ItemPassed, Score: 100, MaxScore: 100, EvidenceSummary: `{"ok":true}`}
	if err := store.AppendItem(context.Background(), item); err != nil {
		t.Fatalf("running 状态追加条目失败: %v", err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM model_check_items WHERE run_id=? AND status='passed'`, run.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("条目落库数量=%d err=%v", count, err)
	}
	t.Run("invalid item", func(t *testing.T) {
		invalid := item
		invalid.ID = "run-append-item-0002"
		invalid.Status = ItemStatus("bogus")
		if err := store.AppendItem(context.Background(), invalid); err == nil {
			t.Fatal("非法条目必须被拒绝")
		}
	})
	t.Run("missing run", func(t *testing.T) {
		orphan := item
		orphan.ID = "run-append-item-0003"
		orphan.RunID = "run-missing"
		if err := store.AppendItem(context.Background(), orphan); err == nil || !strings.Contains(err.Error(), "run not found") {
			t.Fatalf("缺失 run 必须报错: err=%v", err)
		}
	})
	t.Run("terminal run", func(t *testing.T) {
		if _, err := store.db.Exec(`UPDATE model_check_runs SET status='completed',finished_at=? WHERE id=?`, now.Add(time.Minute).Format(time.RFC3339Nano), run.ID); err != nil {
			t.Fatal(err)
		}
		late := item
		late.ID = "run-append-item-0004"
		if err := store.AppendItem(context.Background(), late); err == nil || !strings.Contains(err.Error(), "not running") {
			t.Fatalf("终态 run 追加条目必须被拒绝: err=%v", err)
		}
	})
}

// wbHealthRuntimeDDL 在运行时 DDL 之上补齐健康表的两个错误列，
// 使 ApplyHealthFact 的完整列契约可以在内存库中执行。
func wbHealthRuntimeDDL(t *testing.T) []string {
	t.Helper()
	return append(runtimeTestDDL(),
		`ALTER TABLE account_quality_health_hourly ADD COLUMN error_code TEXT`,
		`ALTER TABLE account_quality_health_hourly ADD COLUMN error_message TEXT`,
	)
}

// 健康发布契约：quick 质量失败 + 明确允许执行时必须发布健康事实并标记 applied；
// 统计小时不可用时必须保留可重试的 failed 状态并发出 health_sync_failed 事件。
func TestWBRunQuickQualityFailurePublishesHealthFactOrRetryableFailure(t *testing.T) {
	t.Run("applied", func(t *testing.T) {
		store := &Store{db: wbOpenMemoryDB(t, wbHealthRuntimeDDL(t)), mode: "sqlite"}
		defer store.Close()
		statHour, err := NewHealthStatHourFunc("UTC")
		if err != nil {
			t.Fatal(err)
		}
		store.HealthStatHour = statHour
		enforced := make(chan QualityEnforcement, 1)
		projector := &QualityProjector{Store: store, Enforcement: EnforcementApplierFunc(func(_ context.Context, input QualityEnforcement) error {
			enforced <- input
			return nil
		})}
		result, events := wbRunQualityFailingProbe(t, store, projector, nil)
		if result.Status != string(RunCompleted) {
			t.Fatalf("result=%+v", result)
		}
		select {
		case input := <-enforced:
			if input.AccountID != "acct" || input.Action != "quality_isolate" {
				t.Fatalf("质量执行输入=%+v", input)
			}
		default:
			t.Fatal("质量失败必须触发执行适配器")
		}
		var factCount int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM account_quality_health_hourly WHERE account_id='acct'`).Scan(&factCount); err != nil || factCount != 1 {
			t.Fatalf("健康事实行数=%d err=%v", factCount, err)
		}
		var syncStatus string
		if err := store.db.QueryRow(`SELECT quality_health_sync_status FROM model_check_runs WHERE id=?`, result.RunID).Scan(&syncStatus); err != nil || syncStatus != "applied" {
			t.Fatalf("健康同步状态=%q err=%v", syncStatus, err)
		}
		for _, event := range events {
			if event.Kind == "health_sync_failed" {
				t.Fatalf("成功发布不得发出失败事件: %v", events)
			}
		}
	})
	t.Run("stat hour failure keeps retryable state", func(t *testing.T) {
		store := &Store{db: wbOpenMemoryDB(t, wbHealthRuntimeDDL(t)), mode: "sqlite"}
		defer store.Close()
		store.HealthStatHour = func(time.Time) (string, error) { return "", errors.New("统计时区不可用") }
		result, events := wbRunQualityFailingProbe(t, store, &QualityProjector{Store: store}, nil)
		var syncStatus string
		if err := store.db.QueryRow(`SELECT quality_health_sync_status FROM model_check_runs WHERE id=?`, result.RunID).Scan(&syncStatus); err != nil || syncStatus != "failed" {
			t.Fatalf("健康同步状态=%q err=%v, 必须保留 failed 以便重试", syncStatus, err)
		}
		found := false
		for _, event := range events {
			if event.Kind == "health_sync_failed" {
				found = true
			}
		}
		if !found {
			t.Fatalf("必须发出 health_sync_failed 事件: %v", events)
		}
	})
}

// wbRunQualityFailingProbe 以固定上游内容触发一次 quick 质量失败 run，
// 返回执行结果与全部进度事件，供健康发布断言复用。
func wbRunQualityFailingProbe(t *testing.T, store *Store, projector *QualityProjector, onEvent func(ProgressEvent)) (RunResult, []ProgressEvent) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"WRONG-CONTENT","usage":{"total_tokens":2}}`))
	}))
	t.Cleanup(server.Close)
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	events := make([]ProgressEvent, 0, 2)
	sink := onEvent
	if sink == nil {
		sink = func(ProgressEvent) {}
	}
	runtime := &Runtime{
		Store:     store,
		OwnerID:   "wb-health",
		Projector: projector,
		Now:       func() time.Time { return now },
		OnEvent: func(event ProgressEvent) {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
			sink(event)
		},
		Resolve: func(context.Context, RunRequest) (Target, error) {
			return Target{Endpoint: server.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Prompt: "hello", DispatchRevision: 1}, nil
		},
	}
	result, err := runtime.Run(context.Background(), RunRequest{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "quick", TriggerKind: "manual", ProviderCode: "openai", Threshold: 70, PenaltyAction: "quality_isolate", RecoveryIntervalMinutes: 15, ManualEnforcementEnabled: true, OwnPhysicalAccount: true, ConfigRevision: "cfg-1", PolicyRevision: "pol-1"})
	if err != nil {
		t.Fatalf("质量失败 run 不应返回传输错误: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	return result, events
}

// EnforcementApplierFunc 让测试内联实现 Business 执行端口。
type EnforcementApplierFunc func(context.Context, QualityEnforcement) error

func (f EnforcementApplierFunc) Apply(ctx context.Context, input QualityEnforcement) error {
	return f(ctx, input)
}
