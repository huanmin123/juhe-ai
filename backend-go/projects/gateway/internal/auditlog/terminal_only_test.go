package auditlog

// terminal_only_test.go 是 F3 终态唯一写入的最小回归组（2026-11-11）：
// 请求进行中零落库（不再有 in_progress 占位信封），终态单行（成功或失败）
// 携带 attempts/payload refs/error group 子表，同 ID 重复终态输入 first-wins
// 忽略（ON CONFLICT DO NOTHING → PersistResult.Ignored）。真库用例经 pgtestkit
// 范式（openPostgresStore），JUHE_AI_TEST_EXTERNAL=1 门禁由 pgtestkit 统一执行。

import (
	"context"
	"testing"
	"time"
)

// TestTerminalOnlyProducerSeesExactlyOneTerminalCapture 固定采集序列契约：
// 流式请求进行中阶段零采集（信封入口已随撤回机器整体删除——Producer 编译
// 面不存在任何 in_progress 采集入口），终态 finalize 是唯一一条 Capture，
// store 侧恰好收到一次 Persist、零撤回面（RetractInProgress 已从 Store 接口
// 删除，任何残留调用都是编译错误）。
func TestTerminalOnlyProducerSeesExactlyOneTerminalCapture(t *testing.T) {
	fake := &fakeStore{renewOK: true}
	producer := NewProducer(fake, fixedLeaseSource{OwnerID: "terminal-owner", FenceToken: 1}, Config{OwnerLease: 30 * time.Second}, nil)
	producer.Capture(producerTestInput("terminal-only-stream"))
	fake.waitForCalls(t, 3)

	persisted := fake.persistedInputs()
	if len(persisted) != 1 {
		t.Fatalf("终态唯一写入：store 必须只收到 1 次持久化，实际 %d 次", len(persisted))
	}
	if persisted[0].LifecycleStatus != LifecycleFinalized {
		t.Fatalf("唯一一次持久化必须是终态，实际 lifecycle=%q", persisted[0].LifecycleStatus)
	}
	if renews, persists, hots := fake.counts(); renews != 1 || persists != 1 || hots != 1 {
		t.Fatalf("写序列必须恰为 renew→persist→hot 各一次: renew=%d persist=%d hot=%d", renews, persists, hots)
	}
}

// TestTerminalOnlyPersistWritesSingleFinalizedRowWithChildren 真库回归：终态
// Persist 落单行 finalized 且 attempts/payload refs/error group 子表齐备；
// 全程（新库→单次 Persist）audit_logs 只有终态一行，无占位行。
func TestTerminalOnlyPersistWritesSingleFinalizedRowWithChildren(t *testing.T) {
	store, implementation := openPostgresStore(t)
	defer store.Close()
	ctx := context.Background()
	lease := acquireLease(t, store)

	var before int
	if err := implementation.db.QueryRow(`SELECT count(*) FROM juhe_dataset.audit_logs`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before != 0 {
		t.Fatalf("新库必须为空后再写终态，实际已有 %d 行", before)
	}

	input := fixture("terminal-only-single", LifecycleFinalized)
	input.Success = false
	input.AuditOutcome = AuditOutcomeUpstreamFailed
	input.ErrorCode = "upstream_unavailable"
	input.ErrorMessage = "upstream unavailable 246813579"
	input.EndedAt = time.Date(2026, 8, 9, 12, 0, 1, 0, time.UTC).Format(time.RFC3339Nano)
	input.Attempts = []AuditLogAttemptInput{{
		TempID: "terminal-attempt-1", AttemptIndex: 0, ProviderCode: "gpt",
		UpstreamMethod: "POST", UpstreamURL: "https://upstream.example/v1/chat",
		Success: boolPtr(false), UpstreamStatusCode: intPointer(502),
		ErrorPhase: "upstream", StartedAt: input.StartedAt, EndedAt: input.EndedAt,
	}}
	input.Payloads = []AuditLogPayloadInput{{
		AttemptTempID: "terminal-attempt-1", PartType: PayloadPartGatewayError,
		SequenceIndex: intPointer(0), ContentType: "application/json",
		Body: PayloadBody{Bytes: []byte(`{"error":"terminal"}`), Present: true},
	}}
	if _, err := store.Persist(ctx, lease, input); err != nil {
		t.Fatal(err)
	}

	var lifecycle, outcome string
	var attemptCount, payloadCount int
	var errorGroupID string
	if err := implementation.db.QueryRow(`SELECT lifecycle_status,audit_outcome,attempt_count,payload_count,error_group_id FROM juhe_dataset.audit_logs WHERE id=$1`, input.ID).Scan(&lifecycle, &outcome, &attemptCount, &payloadCount, &errorGroupID); err != nil {
		t.Fatal(err)
	}
	if lifecycle != string(LifecycleFinalized) {
		t.Fatalf("主行必须恒终态: lifecycle=%q", lifecycle)
	}
	if outcome != string(AuditOutcomeUpstreamFailed) || attemptCount != 1 || payloadCount != 1 || errorGroupID == "" {
		t.Fatalf("终态行不完整: outcome=%q attempts=%d payloads=%d errorGroup=%q", outcome, attemptCount, payloadCount, errorGroupID)
	}

	var total, attempts, payloadRefs, groups int
	if err := implementation.db.QueryRow(`SELECT
		(SELECT count(*) FROM juhe_dataset.audit_logs),
		(SELECT count(*) FROM juhe_dataset.audit_log_attempts WHERE audit_log_id=$1),
		(SELECT count(*) FROM juhe_dataset.audit_payload_refs WHERE audit_log_id=$1),
		(SELECT count(*) FROM juhe_dataset.audit_error_groups WHERE id=$2)`,
		input.ID, errorGroupID).Scan(&total, &attempts, &payloadRefs, &groups); err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("终态唯一写入：audit_logs 必须只有 1 行，实际 %d 行", total)
	}
	if attempts != 1 || payloadRefs != 1 || groups != 1 {
		t.Fatalf("终态子表必须齐备: attempts=%d payloadRefs=%d errorGroups=%d", attempts, payloadRefs, groups)
	}
}

// TestTerminalOnlyDuplicateTerminalIgnoredWithChildrenIntact 真库回归：同 ID
// 重复终态输入被忽略（Ignored=true），主行与全部子表内容不被第二次输入改写。
func TestTerminalOnlyDuplicateTerminalIgnoredWithChildrenIntact(t *testing.T) {
	store, implementation := openPostgresStore(t)
	defer store.Close()
	ctx := context.Background()
	lease := acquireLease(t, store)

	first := fixture("terminal-only-dup", LifecycleFinalized)
	first.Success = false
	first.AuditOutcome = AuditOutcomeUpstreamFailed
	first.ErrorCode = "first_code"
	first.ErrorMessage = "first failure 1357924680"
	first.Attempts = []AuditLogAttemptInput{{
		TempID: "dup-attempt-1", AttemptIndex: 0, ProviderCode: "gpt",
		UpstreamMethod: "POST", UpstreamURL: "https://upstream.example/v1/chat",
		Success: boolPtr(false), UpstreamStatusCode: intPointer(500),
		StartedAt: first.StartedAt, EndedAt: first.StartedAt,
	}}
	first.Payloads = []AuditLogPayloadInput{{
		AttemptTempID: "dup-attempt-1", PartType: PayloadPartGatewayError,
		SequenceIndex: intPointer(0), ContentType: "application/json",
		Body: PayloadBody{Bytes: []byte(`{"error":"first"}`), Present: true},
	}}
	if _, err := store.Persist(ctx, lease, first); err != nil {
		t.Fatal(err)
	}

	second := fixture("terminal-only-dup", LifecycleFinalized)
	second.Success = true
	second.AuditOutcome = AuditOutcomeSuccess
	second.ErrorCode, second.ErrorMessage = "", ""
	result, err := store.Persist(ctx, lease, second)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Ignored {
		t.Fatalf("同 ID 重复终态必须被忽略（first-wins）: %+v", result)
	}

	var lifecycle, outcome, errorCode string
	var attemptCount, payloadCount int
	if err := implementation.db.QueryRow(`SELECT lifecycle_status,audit_outcome,error_code,attempt_count,payload_count FROM juhe_dataset.audit_logs WHERE id=$1`, first.ID).Scan(&lifecycle, &outcome, &errorCode, &attemptCount, &payloadCount); err != nil {
		t.Fatal(err)
	}
	if lifecycle != string(LifecycleFinalized) || outcome != string(AuditOutcomeUpstreamFailed) || errorCode != "first_code" || attemptCount != 1 || payloadCount != 1 {
		t.Fatalf("首行终态内容不得被改写: lifecycle=%q outcome=%q errorCode=%q attempts=%d payloads=%d", lifecycle, outcome, errorCode, attemptCount, payloadCount)
	}
	var attempts, payloadRefs int
	if err := implementation.db.QueryRow(`SELECT
		(SELECT count(*) FROM juhe_dataset.audit_log_attempts WHERE audit_log_id=$1),
		(SELECT count(*) FROM juhe_dataset.audit_payload_refs WHERE audit_log_id=$1)`,
		first.ID).Scan(&attempts, &payloadRefs); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || payloadRefs != 1 {
		t.Fatalf("重复终态不得增删子表行: attempts=%d payloadRefs=%d", attempts, payloadRefs)
	}
}
