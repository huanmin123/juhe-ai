package modelcheckowner

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// wbInputFixture 构造一个合法的不可变输入快照。
func wbInputFixture() InputRecord {
	now := time.Date(2026, 9, 5, 8, 0, 0, 0, time.UTC)
	return InputRecord{InputID: "input-1", IdentityKey: "sys:acct:model", TargetID: "acct", ConfigRevision: "cfg-1", PolicyRevision: "pol-1", Trigger: "manual", IssuedAt: now, ExpiresAt: now.Add(time.Hour), Payload: json.RawMessage(`{"model":"gpt-5.6-sol"}`)}
}

// IssueInput/LoadInput 契约：同一不可变输入重放幂等；ID 复用、
// 摘要篡改与过期读取都必须失败关闭。
func TestWBStoreIssueInputAndLoadInputRoundTrip(t *testing.T) {
	store := &Store{db: wbOpenMemoryDB(t, runtimeTestDDL()), mode: "sqlite"}
	defer store.Close()
	ctx := context.Background()
	first, err := store.IssueInput(ctx, wbInputFixture())
	if err != nil || first.InputVersion != 1 || first.InputDigest == "" {
		t.Fatalf("首次签发 input=%+v err=%v", first, err)
	}
	// 键序不同的等价 JSON 必须被规范化后判定为同一次输入。
	reordered := wbInputFixture()
	reordered.Payload = json.RawMessage(`{ "model" : "gpt-5.6-sol" }`)
	replay, err := store.IssueInput(ctx, reordered)
	if err != nil || replay.InputID != first.InputID || replay.InputDigest != first.InputDigest {
		t.Fatalf("等价重放必须幂等: got=%+v want=%+v err=%v", replay, first, err)
	}
	t.Run("reused id with different payload", func(t *testing.T) {
		drifted := wbInputFixture()
		drifted.Payload = json.RawMessage(`{"model":"gpt-5.6-terra"}`)
		if _, err := store.IssueInput(ctx, drifted); !errors.Is(err, ErrInputConflict) {
			t.Fatalf("ID 复用必须冲突: err=%v", err)
		}
	})
	t.Run("preset digest mismatch", func(t *testing.T) {
		tampered := wbInputFixture()
		tampered.InputID = "input-tampered"
		tampered.InputDigest = "deadbeef"
		if _, err := store.IssueInput(ctx, tampered); !errors.Is(err, ErrInputTampered) {
			t.Fatalf("预置摘要不一致必须报篡改: err=%v", err)
		}
	})
	t.Run("validation", func(t *testing.T) {
		base := wbInputFixture()
		for name, mutate := range map[string]func(*InputRecord){
			"missing input id":    func(in *InputRecord) { in.InputID = " " },
			"missing trigger":     func(in *InputRecord) { in.Trigger = "" },
			"expiry before issue": func(in *InputRecord) { in.ExpiresAt = in.IssuedAt.Add(-time.Second) },
			"invalid payload":     func(in *InputRecord) { in.Payload = json.RawMessage(`{`) },
		} {
			t.Run(name, func(t *testing.T) {
				invalid := base
				invalid.InputID = name + "-id"
				mutate(&invalid)
				if _, err := store.IssueInput(ctx, invalid); err == nil {
					t.Fatal("非法输入必须被拒绝")
				}
			})
		}
	})
	t.Run("load and expiry", func(t *testing.T) {
		loaded, err := store.LoadInput(ctx, "input-1", time.Date(2026, 9, 5, 8, 30, 0, 0, time.UTC))
		if err != nil || loaded.InputID != "input-1" || loaded.Payload == nil {
			t.Fatalf("读取输入=%+v err=%v", loaded, err)
		}
		if _, err := store.LoadInput(ctx, "input-1", time.Date(2026, 9, 6, 8, 0, 0, 0, time.UTC)); err == nil || !strings.Contains(err.Error(), "expired") {
			t.Fatalf("过期输入必须拒绝执行: err=%v", err)
		}
		if _, err := store.LoadInput(ctx, " ", time.Now()); err == nil {
			t.Fatal("空 input ID 必须报错")
		}
	})
	t.Run("stored payload tampering", func(t *testing.T) {
		if _, err := store.db.Exec(`UPDATE model_check_inputs SET payload=? WHERE input_id='input-1'`, `{"model":"hacked"}`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LoadInput(ctx, "input-1", time.Date(2026, 9, 5, 8, 30, 0, 0, time.UTC)); !errors.Is(err, ErrInputTampered) {
			t.Fatalf("存储载荷被篡改必须报篡改: err=%v", err)
		}
	})
}

// ClaimInput 租约契约：新认领分配递增 fence；租约内存活期内同身份幂等，
// 异主拒绝、同主异 outcome 冲突；过期后可被新 fence 接管。
func TestWBStoreClaimLeaseLifecycle(t *testing.T) {
	store := &Store{db: wbOpenMemoryDB(t, runtimeTestDDL()), mode: "sqlite"}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 9, 5, 8, 10, 0, 0, time.UTC)
	input, err := store.IssueInput(ctx, wbInputFixture())
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimInput(ctx, input.InputID, "claim-1", "outcome-1", "owner-a", time.Minute, now)
	if err != nil || claim.FenceToken != 1 {
		t.Fatalf("首次认领 claim=%+v err=%v", claim, err)
	}
	replay, err := store.ClaimInput(ctx, input.InputID, "claim-1", "outcome-1", "owner-a", time.Minute, now.Add(time.Second))
	if err != nil || replay.ClaimToken != "claim-1" || replay.FenceToken != 1 {
		t.Fatalf("同身份重放必须幂等: claim=%+v err=%v", replay, err)
	}
	if _, err := store.ClaimInput(ctx, input.InputID, "claim-2", "outcome-2", "owner-b", time.Minute, now.Add(2*time.Second)); !errors.Is(err, ErrClaimBusy) {
		t.Fatalf("异主认领必须 Busy: err=%v", err)
	}
	if _, err := store.ClaimInput(ctx, input.InputID, "claim-1", "outcome-other", "owner-a", time.Minute, now.Add(3*time.Second)); !errors.Is(err, ErrClaimConflict) {
		t.Fatalf("同主异 outcome 必须冲突: err=%v", err)
	}
	if _, err := store.ClaimInput(ctx, input.InputID, "claim-2", "outcome-2", "owner-b", time.Minute, time.Time{}); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("非法时间必须被拒绝: err=%v", err)
	}
	// 过期租约：新认领推进 fence。
	if _, err := store.db.Exec(`UPDATE model_check_execution_claims SET claim_until=? WHERE input_id=?`, now.Add(-time.Second).Format(time.RFC3339Nano), input.InputID); err != nil {
		t.Fatal(err)
	}
	takeover, err := store.ClaimInput(ctx, input.InputID, "claim-2", "outcome-2", "owner-b", time.Minute, now.Add(4*time.Second))
	if err != nil || takeover.FenceToken != 2 {
		t.Fatalf("过期接管 claim=%+v err=%v", takeover, err)
	}
	t.Run("expired input", func(t *testing.T) {
		expired := wbInputFixture()
		expired.InputID = "input-expired"
		expired.ExpiresAt = now.Add(-time.Minute)
		if _, err := store.IssueInput(ctx, expired); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ClaimInput(ctx, expired.InputID, "claim-3", "outcome-3", "owner-a", time.Minute, now); err == nil || !strings.Contains(err.Error(), "expired") {
			t.Fatalf("过期输入必须拒绝认领: err=%v", err)
		}
	})
	t.Run("renew and release fences", func(t *testing.T) {
		if err := store.RenewClaim(ctx, takeover, time.Minute, now.Add(5*time.Second)); err != nil {
			t.Fatalf("租约续期失败: %v", err)
		}
		stale := takeover
		stale.FenceToken = 99
		if err := store.RenewClaim(ctx, stale, time.Minute, now.Add(5*time.Second)); !errors.Is(err, ErrStaleFence) {
			t.Fatalf("陈旧 fence 续期必须拒绝: err=%v", err)
		}
		if err := store.RenewClaim(ctx, takeover, 0, now.Add(5*time.Second)); err == nil {
			t.Fatal("非法续期参数必须报错")
		}
		if err := store.ReleaseClaim(ctx, takeover, now.Add(6*time.Second)); err != nil {
			t.Fatalf("租约释放失败: %v", err)
		}
		staleRelease := takeover
		staleRelease.FenceToken = 7
		if err := store.ReleaseClaim(ctx, staleRelease, now.Add(6*time.Second)); !errors.Is(err, ErrStaleFence) {
			t.Fatalf("陈旧 fence 释放必须拒绝: err=%v", err)
		}
	})
}

// CommitOutcome 契约：摘要/租约全部校验后一次性提交；同载荷重放幂等，
// 异载荷冲突，陈旧租约拒绝。ListCommittedOutcomes 校验存储完整性。
func TestWBStoreCommitOutcomeAndListCommittedOutcomes(t *testing.T) {
	store := &Store{db: wbOpenMemoryDB(t, runtimeTestDDL()), mode: "sqlite"}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 9, 5, 8, 0, 30, 0, time.UTC)
	input, err := store.IssueInput(ctx, wbInputFixture())
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimInput(ctx, input.InputID, "claim-1", "outcome-1", "owner-a", time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	outcome := Outcome{OutcomeID: "outcome-1", InputID: input.InputID, InputDigest: input.InputDigest, ObservedAt: now, StoredAt: now, Payload: json.RawMessage(`{"ok":true}`)}
	if err := store.CommitOutcome(ctx, outcome, claim, now); err != nil {
		t.Fatalf("首次提交失败: %v", err)
	}
	if err := store.CommitOutcome(ctx, outcome, claim, now); err != nil {
		t.Fatalf("同载荷重放必须幂等: %v", err)
	}
	if err := store.CommitOutcome(ctx, Outcome{OutcomeID: "outcome-1", InputID: input.InputID, InputDigest: input.InputDigest, Payload: json.RawMessage(`{"ok":false}`)}, claim, now); !errors.Is(err, ErrOutcomeConflict) {
		t.Fatalf("异载荷提交必须冲突: err=%v", err)
	}
	t.Run("rejections", func(t *testing.T) {
		if err := store.CommitOutcome(ctx, Outcome{OutcomeID: "outcome-x", InputID: input.InputID, InputDigest: "wrong", Payload: json.RawMessage(`{}`)}, claim, now); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
			t.Fatalf("输入摘要不一致必须报错: err=%v", err)
		}
		stale := claim
		stale.FenceToken = 42
		if err := store.CommitOutcome(ctx, Outcome{OutcomeID: "outcome-x", InputID: input.InputID, InputDigest: input.InputDigest, Payload: json.RawMessage(`{}`)}, stale, now); !errors.Is(err, ErrStaleFence) {
			t.Fatalf("陈旧租约提交必须拒绝: err=%v", err)
		}
		if err := store.CommitOutcome(ctx, Outcome{OutcomeID: "outcome-x", InputID: input.InputID, Payload: json.RawMessage(`{}`)}, claim, now); err == nil {
			t.Fatal("非法 outcome 必须报错")
		}
	})
	t.Run("listing verifies integrity", func(t *testing.T) {
		items, err := store.ListCommittedOutcomes(ctx, OutcomeCursor{}, 10)
		if err != nil || len(items) != 1 || items[0].Outcome.OutcomeID != "outcome-1" {
			t.Fatalf("提交清单=%+v err=%v", items, err)
		}
		if _, err := store.ListCommittedOutcomes(ctx, OutcomeCursor{StoredAt: now}, 10); err == nil || !strings.Contains(err.Error(), "cursor is incomplete") {
			t.Fatalf("不完整游标必须报错: err=%v", err)
		}
		if _, err := store.ListCommittedOutcomes(ctx, OutcomeCursor{}, 0); err == nil {
			t.Fatal("非法 limit 必须报错")
		}
		paged, err := store.ListCommittedOutcomes(ctx, OutcomeCursor{StoredAt: now.Add(-time.Minute), OutcomeID: "outcome-0"}, 10)
		if err != nil || len(paged) != 1 {
			t.Fatalf("游标后分页=%+v err=%v", paged, err)
		}
		if _, err := store.db.Exec(`UPDATE model_check_outcomes SET payload=? WHERE outcome_id='outcome-1'`, `{"ok":hacked}`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ListCommittedOutcomes(ctx, OutcomeCursor{}, 10); err == nil {
			t.Fatal("载荷完整性破坏必须报错")
		}
	})
}

// ProjectOutcome 契约：终态投影一次落库，精确重放幂等，任何漂移都冲突。
func TestWBStoreProjectOutcomeIdempotentReplay(t *testing.T) {
	store := &Store{db: wbOpenMemoryDB(t, runtimeTestDDL()), mode: "sqlite"}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 9, 5, 11, 0, 0, 0, time.UTC)
	run := RunRecord{ID: "run-proj", SystemAccountID: "sys", ActorSystemAccountID: "actor", ProviderCode: "openai", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "full", TriggerKind: "manual", ProbeSetVersion: "probe-v1", StartedAt: now, RequestSummary: []byte(`{}`), PolicySnapshot: []byte(`{}`)}
	if err := store.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	duration := int64(1500)
	item := ItemRecord{ID: "run-proj-item-0001", RunID: run.ID, ItemKey: "stability", ItemType: "stability", Status: ItemPassed, Score: 100, MaxScore: 100, DurationMS: &duration, EvidenceSummary: `{"ok":true}`}
	projection := OutcomeProjection{RunID: run.ID, Status: RunCompleted, Level: "likely", Message: "ok", Score: 100, MaxScore: 100, FinishedAt: now.Add(time.Minute), DurationMS: &duration, Items: []ItemRecord{item}, ResultSummary: json.RawMessage(`{"score":100}`), QualityDecision: json.RawMessage(`{"formed":true}`)}
	if err := store.ProjectOutcome(ctx, projection); err != nil {
		t.Fatalf("首次投影失败: %v", err)
	}
	if err := store.ProjectOutcome(ctx, projection); err != nil {
		t.Fatalf("精确重放必须幂等: %v", err)
	}
	drifted := projection
	drifted.Level = "high_confidence"
	if err := store.ProjectOutcome(ctx, drifted); !errors.Is(err, ErrRunProjectionConflict) {
		t.Fatalf("漂移重放必须冲突: err=%v", err)
	}
	driftedItems := projection
	driftedItems.Items = nil
	if err := store.ProjectOutcome(ctx, driftedItems); err == nil {
		t.Fatal("空条目投影必须拒绝")
	}
	if err := store.ProjectOutcome(ctx, OutcomeProjection{RunID: "run-missing", Status: RunCompleted, Level: "likely", FinishedAt: now, Items: []ItemRecord{item}}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("缺失 run 必须报错: err=%v", err)
	}
	nonTerminal := projection
	nonTerminal.Status = RunRunning
	if err := store.ProjectOutcome(ctx, nonTerminal); err == nil {
		t.Fatal("非终态投影必须拒绝")
	}
}

func wbValidHealthFact() HealthFact {
	return HealthFact{AccountID: "acct", SystemAccountID: "sys", ProviderCode: "openai", StatHour: "2026-09-05T10", RunID: "run-health", Model: "gpt-5.6-sol", Profile: "quick", ObservedAt: time.Date(2026, 9, 5, 10, 30, 0, 0, time.UTC), Score: 20, Threshold: 70, Level: "suspicious"}
}

// ApplyHealthFact/ReadHealthFact 契约：最新观测优先落库；
// 旧观测不得覆盖新事实；读取必须限定账户与小时。
func TestWBStoreHealthFactUpsertAndRead(t *testing.T) {
	store := &Store{db: wbOpenMemoryDB(t, wbHealthRuntimeDDL(t)), mode: "sqlite"}
	defer store.Close()
	ctx := context.Background()
	applied, err := store.ApplyHealthFact(ctx, wbValidHealthFact())
	if err != nil || !applied {
		t.Fatalf("首次发布 applied=%v err=%v", applied, err)
	}
	stale := wbValidHealthFact()
	stale.RunID = "run-older"
	stale.ObservedAt = stale.ObservedAt.Add(-time.Hour)
	if applied, err := store.ApplyHealthFact(ctx, stale); err != nil || applied {
		t.Fatalf("旧观测必须被拒绝: applied=%v err=%v", applied, err)
	}
	newer := wbValidHealthFact()
	newer.RunID = "run-newer"
	newer.ObservedAt = newer.ObservedAt.Add(time.Hour)
	newer.ErrorMessage = strings.Repeat("长", 1200)
	if applied, err := store.ApplyHealthFact(ctx, newer); err != nil || !applied {
		t.Fatalf("新观测必须覆盖: applied=%v err=%v", applied, err)
	}
	var storedErrorLen int
	if err := store.db.QueryRow(`SELECT LENGTH(error_message) FROM account_quality_health_hourly WHERE account_id='acct'`).Scan(&storedErrorLen); err != nil || storedErrorLen > 1000 {
		t.Fatalf("错误消息必须截断: len=%d err=%v", storedErrorLen, err)
	}
	fact, found, err := store.ReadHealthFact(ctx, "acct", "2026-09-05T10")
	if err != nil || !found || fact.RunID != "run-newer" {
		t.Fatalf("读取健康事实=%+v found=%v err=%v", fact, found, err)
	}
	if _, found, err := store.ReadHealthFact(ctx, "acct", "2026-09-05T11"); err != nil || found {
		t.Fatalf("缺失小时必须返回 found=false: found=%v err=%v", found, err)
	}
	if _, _, err := store.ReadHealthFact(ctx, " ", "2026-09-05T10"); err == nil {
		t.Fatal("空账户读取必须报错")
	}
	if _, err := store.ApplyHealthFact(ctx, HealthFact{}); err == nil {
		t.Fatal("不完整身份必须报错")
	}
	invalidHour := wbValidHealthFact()
	invalidHour.StatHour = "2026-09-05 10"
	if _, err := store.ApplyHealthFact(ctx, invalidHour); err == nil || !strings.Contains(err.Error(), "stat hour") {
		t.Fatalf("非法统计小时必须报错: err=%v", err)
	}
}

// MarkHealthSync 契约：按 run 记录发布结果，状态只允许 applied/failed。
func TestWBStoreMarkHealthSyncContract(t *testing.T) {
	store := &Store{db: wbOpenMemoryDB(t, runtimeTestDDL()), mode: "sqlite"}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	run := RunRecord{ID: "run-sync", SystemAccountID: "sys", ActorSystemAccountID: "actor", ProviderCode: "openai", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "quick", TriggerKind: "manual", ProbeSetVersion: "probe-v1", StartedAt: now, RequestSummary: []byte(`{}`), PolicySnapshot: []byte(`{}`)}
	if err := store.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkHealthSync(ctx, run.ID, "applied"); err != nil {
		t.Fatalf("标记 applied 失败: %v", err)
	}
	if err := store.MarkHealthSync(ctx, run.ID, "pending"); err == nil {
		t.Fatal("非法状态必须报错")
	}
	if err := store.MarkHealthSync(ctx, "run-missing", "applied"); err == nil {
		t.Fatal("缺失 run 必须报错")
	}
}

// wbSeedFailedHealthRun 写入一条失败的完整检测 run，供重试扫描读取。
func wbSeedFailedHealthRun(t *testing.T, store *Store, id string, fields map[string]any) {
	t.Helper()
	policy := fields["policy"].(string)
	decision := fields["decision"].(string)
	request := fields["request"].(string)
	finished := fields["finished"].(string)
	_, err := store.db.Exec(`INSERT INTO model_check_runs (id,system_account_id,actor_system_account_id,provider_code,target_type,target_id,account_id,model,profile,trigger_kind,status,level,score,max_score,message,request_summary_json,result_summary_json,policy_snapshot_json,quality_decision_json,probe_set_version,started_at,created_at,updated_at,finished_at,quality_health_sync_status) VALUES (?,'sys','actor','openai','account','acct',?, 'gpt-5.6-sol',?, 'scheduled','completed','suspicious',20,100,'',?,'{}',?,?, 'probe-v1','2026-09-05T09:00:00Z','2026-09-05T09:00:00Z','2026-09-05T09:30:00Z',?,'failed')`,
		id, fields["account"], fields["profile"], request, policy, decision, finished)
	if err != nil {
		t.Fatalf("写入失败 run %s: %v", id, err)
	}
}

// ListHealthSyncRetries 契约：只重新发现可安全重试的失败健康发布；
// 不完整或越界的快照必须被跳过而不是阻塞其他重试。
func TestWBStoreListHealthSyncRetriesFilters(t *testing.T) {
	store := &Store{db: wbOpenMemoryDB(t, wbHealthRuntimeDDL(t)), mode: "sqlite"}
	defer store.Close()
	statHour, err := NewHealthStatHourFunc("UTC")
	if err != nil {
		t.Fatal(err)
	}
	store.HealthStatHour = statHour
	policy := `{"revision":"7","threshold":70,"action":"quality_isolate","recoveryIntervalMinutes":15,"manualEnforcementEligible":true}`
	formedDecision := `{"evidenceFormed":true,"trustFormed":true,"hardQualityFailure":false}`
	quickDecision := `{"evidenceFormed":false,"trustFormed":false}`
	request := `{"configRevision":"cfg-4"}`
	finished := "2026-09-05T09:29:00Z"
	wbSeedFailedHealthRun(t, store, "run-quick", map[string]any{"account": "acct-quick", "profile": "quick", "policy": policy, "decision": quickDecision, "request": request, "finished": finished})
	wbSeedFailedHealthRun(t, store, "run-full", map[string]any{"account": "acct-full", "profile": "full", "policy": policy, "decision": formedDecision, "request": request, "finished": finished})
	wbSeedFailedHealthRun(t, store, "run-not-formed", map[string]any{"account": "acct-nf", "profile": "full", "policy": policy, "decision": quickDecision, "request": request, "finished": finished})
	wbSeedFailedHealthRun(t, store, "run-high-score", map[string]any{"account": "acct-high", "profile": "full", "policy": policy, "decision": formedDecision, "request": request, "finished": finished})
	// run-high-score 必须被过滤：把 score 提到阈值之上。
	if _, err := store.db.Exec(`UPDATE model_check_runs SET score=80 WHERE id='run-high-score'`); err != nil {
		t.Fatal(err)
	}
	wbSeedFailedHealthRun(t, store, "run-bad-policy", map[string]any{"account": "acct-bad", "profile": "quick", "policy": `{`, "decision": quickDecision, "request": request, "finished": finished})
	retries, err := store.ListHealthSyncRetries(context.Background(), 100)
	if err != nil {
		t.Fatalf("重试扫描失败: %v", err)
	}
	ids := make([]string, 0, len(retries))
	for _, retry := range retries {
		ids = append(ids, retry.RunID)
		if retry.AccountConfigRevision != "cfg-4" || retry.StatHour != "2026-09-05T09" || retry.PolicyRevision != "7" || retry.PenaltyAction != "quality_isolate" || !retry.EnforcementAllowed {
			t.Fatalf("重试事实不完整: %+v", retry)
		}
	}
	if len(ids) != 2 || ids[0] != "run-full" || ids[1] != "run-quick" {
		t.Fatalf("重试清单=%v, 只允许 quick 失败与 formed 完整失败（id 升序）", ids)
	}
	if _, err := store.ListHealthSyncRetries(context.Background(), 0); err == nil {
		t.Fatal("非法 limit 必须报错")
	}
}

// EnsureHealthRetryTasks 契约：按失败发布物化幂等重试任务。
func TestWBStoreEnsureHealthRetryTasksIdempotent(t *testing.T) {
	j3b := wbOpenMemoryDB(t, append(wbHealthRuntimeDDL(t),
		`CREATE TABLE model_check_scheduler_tasks (id TEXT PRIMARY KEY,kind TEXT,due_at TEXT,claim_owner TEXT,claim_until TEXT,fence_token INTEGER,state TEXT,last_error TEXT,completed_at TEXT,payload TEXT,updated_at TEXT)`))
	store := &Store{db: j3b, mode: "sqlite"}
	defer store.Close()
	statHour, err := NewHealthStatHourFunc("UTC")
	if err != nil {
		t.Fatal(err)
	}
	store.HealthStatHour = statHour
	wbSeedFailedHealthRun(t, store, "run-retry", map[string]any{"account": "acct", "profile": "quick", "policy": `{"revision":"1","threshold":70,"action":"fallback","recoveryIntervalMinutes":10}`, "decision": `{"evidenceFormed":false,"trustFormed":false}`, "request": `{"configRevision":"cfg-1"}`, "finished": "2026-09-05T09:29:00Z"})
	if err := store.EnsureHealthRetryTasks(context.Background(), 10); err != nil {
		t.Fatalf("物化重试任务失败: %v", err)
	}
	if err := store.EnsureHealthRetryTasks(context.Background(), 10); err != nil {
		t.Fatalf("重复物化必须幂等: %v", err)
	}
	var count int
	if err := j3b.QueryRow(`SELECT COUNT(*) FROM model_check_scheduler_tasks WHERE id='health:run-retry' AND kind='health_sync_retry'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("重试任务数量=%d err=%v", count, err)
	}
	if err := store.EnsureHealthRetryTasks(context.Background(), 0); err == nil {
		t.Fatal("非法 limit 必须报错")
	}
}

// ListRuns 契约：过滤器/分页/全局 scope 校验。
func TestWBRunListRunFiltersAndScopes(t *testing.T) {
	store := &Store{db: wbOpenMemoryDB(t, runtimeTestDDL()), mode: "sqlite"}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 9, 5, 13, 0, 0, 0, time.UTC)
	for index, id := range []string{"run-a", "run-b"} {
		run := RunRecord{ID: id, SystemAccountID: "sys", ActorSystemAccountID: "actor", ProviderCode: "openai", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "quick", TriggerKind: "manual", ProbeSetVersion: "probe-v1", StartedAt: now.Add(time.Duration(index) * time.Minute), RequestSummary: []byte(`{}`), PolicySnapshot: []byte(`{}`)}
		if err := store.CreateRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	runtime := &Runtime{Store: store}
	listed, err := runtime.ListRuns(ctx, RunListQuery{SystemAccountID: "sys", Page: 1, PageSize: 1})
	if err != nil {
		t.Fatalf("分页读取失败: %v", err)
	}
	result := listed.(RunListResult)
	if !result.HasMore || len(result.Items) != 1 || result.Total != 2 {
		t.Fatalf("分页结果=%+v", result)
	}
	filteredList, err := runtime.ListRuns(ctx, RunListQuery{SystemAccountID: "sys", Status: "running", Level: "unavailable", TargetID: "acct", Model: "gpt-5.6-sol", TriggerKind: "manual", StartAt: "2026-09-05T00:00:00Z", EndAt: "2026-09-06T00:00:00Z"})
	if err != nil {
		t.Fatalf("过滤读取失败: %v", err)
	}
	if filtered := filteredList.(RunListResult); len(filtered.Items) != 2 {
		t.Fatalf("过滤结果=%+v", filtered)
	}
	if _, err := runtime.ListRuns(ctx, RunListQuery{AllSystemAccounts: true, SystemAccountID: "sys"}); err == nil {
		t.Fatal("全局 scope 携带租户必须报错")
	}
	if _, err := runtime.ListRuns(ctx, RunListQuery{}); err == nil {
		t.Fatal("空 scope 必须报错")
	}
	globalList, err := runtime.ListRuns(ctx, RunListQuery{AllSystemAccounts: true})
	if err != nil {
		t.Fatalf("全局读取失败: %v", err)
	}
	if global := globalList.(RunListResult); len(global.Items) != 2 {
		t.Fatalf("全局读取=%+v", global)
	}
}

// GetRun 契约：完整详情必须带检查项，并把最新信任投影合并进结果摘要。
func TestWBRunGetRunMergesLatestTrustReport(t *testing.T) {
	store := &Store{db: wbOpenMemoryDB(t, runtimeTestDDL()), mode: "sqlite"}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 9, 5, 14, 0, 0, 0, time.UTC)
	run := RunRecord{ID: "run-trust", SystemAccountID: "sys", ActorSystemAccountID: "actor", ProviderCode: "openai", TargetType: "account", TargetID: "acct", AccountID: "acct", Model: "gpt-5.6-sol", Profile: "full", TriggerKind: "manual", ProbeSetVersion: "probe-v1", StartedAt: now, RequestSummary: []byte(`{}`), PolicySnapshot: []byte(`{}`)}
	if err := store.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	item := ItemRecord{ID: "run-trust-item-0001", RunID: run.ID, ItemKey: "stability", ItemType: "stability", Status: ItemPassed, Score: 100, MaxScore: 100, EvidenceSummary: `{"ok":true}`}
	if err := store.ProjectOutcome(ctx, OutcomeProjection{RunID: run.ID, Status: RunCompleted, Level: "likely", Score: 100, MaxScore: 100, FinishedAt: now.Add(time.Minute), Items: []ItemRecord{item}, ResultSummary: json.RawMessage(`{"score":100,"trustReport":{"requestedModel":"gpt-5.6-sol"}}`), QualityDecision: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO model_account_trust_results (system_account_id,account_id,requested_model,identity_status,mapping_status,usage_integrity_status,protocol_status,evidence_status,evidence_coverage,observation_count,reason_codes_json,last_observed_id,last_observed_at,updated_at) VALUES ('sys','acct','gpt-5.6-sol','verified','direct','intact','supported','formed',80,3,'[]','obs-1','2026-09-05T14:05:00Z','2026-09-05T14:05:00Z')`); err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{Store: store}
	detail, found, err := runtime.GetRun(ctx, run.ID)
	if err != nil || !found {
		t.Fatalf("详情 found=%v err=%v", found, err)
	}
	runDetail, ok := detail.(RunDetail)
	if !ok || len(runDetail.Checks) != 1 {
		t.Fatalf("详情=%T checks=%d", detail, len(runDetail.Checks))
	}
	var summary struct {
		TrustReport map[string]any `json:"trustReport"`
	}
	if err := json.Unmarshal(runDetail.ResultSummary, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.TrustReport["identityStatus"] != "verified" || summary.TrustReport["observationCount"] != float64(3) {
		t.Fatalf("最新信任投影未合并: %#v", summary.TrustReport)
	}
	if _, found, err := runtime.GetRun(ctx, "run-missing"); err != nil || found {
		t.Fatalf("缺失详情 found=%v err=%v", found, err)
	}
	if _, _, err := runtime.GetRun(ctx, " "); err == nil {
		t.Fatal("空 run ID 必须报错")
	}
}
