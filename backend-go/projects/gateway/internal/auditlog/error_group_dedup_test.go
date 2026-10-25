package auditlog

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// dedupFailureFixture 构造同一错误指纹（account 可变）的失败事件：带 headers
// 和 body 的 gateway_error payload，错误消息固定使 errorFingerprint 一致。
func dedupFailureFixture(id, createdAt, body, accountID string) AuditLogInput {
	input := fixture(id, LifecycleFinalized)
	input.Success = false
	input.AuditOutcome = AuditOutcomeUpstreamFailed
	input.ErrorCode = "upstream_unavailable"
	input.ErrorMessage = "upstream unavailable 123456789"
	input.CreatedAt = createdAt
	input.AccountID = accountID
	input.Attempts = []AuditLogAttemptInput{{
		TempID: "attempt-" + id, AttemptIndex: 0, AccountID: accountID,
		UpstreamMethod: "POST", UpstreamURL: "https://upstream.example",
		StartedAt: input.StartedAt, EndedAt: input.EndedAt,
		Success: &[]bool{false}[0],
	}}
	input.Payloads = []AuditLogPayloadInput{{
		AttemptTempID: "attempt-" + id,
		PartType:      PayloadPartGatewayError,
		SequenceIndex: intPointer(0),
		ContentType:   "application/json",
		Headers:       map[string]HeaderValues{"x-request-id": {Values: []string{id}}},
		Body:          PayloadBody{Bytes: []byte(body), Present: true},
	}}
	return input
}

func dedupRefState(t *testing.T, implementation *sqlStore, auditLogID string) (headersBlobID, bodyBlobID, headersSHA, bodySHA sql.NullString, capture string, drop sql.NullString) {
	t.Helper()
	query := implementation.bind(`SELECT headers_blob_id,body_blob_id,headers_sha256,body_sha256,capture_status,drop_reason FROM ` + implementation.table("audit_payload_refs") + ` WHERE audit_log_id=?`)
	err := implementation.db.QueryRow(query, auditLogID).Scan(&headersBlobID, &bodyBlobID, &headersSHA, &bodySHA, &capture, &drop)
	if err != nil {
		t.Fatal(err)
	}
	return headersBlobID, bodyBlobID, headersSHA, bodySHA, capture, drop
}

// TestErrorGroupDedupKeepsOnlyLatestSampleAcrossWindows 契约：同 fingerprint
// 跨窗口第二条失败事件 persist 后，第一条 refs 全部降级（blob 置 NULL、
// capture_status=deduped、sha 保留），第二条 refs 完整，两个窗口组的
// sample_event_id 均指向第二条，释放 blob 登记 GC 意图，成功事件不受影响。

// TestErrorGroupDedupHandsOverOnlyAcrossWindows 契约：样本只在跨窗口时移交。
// 同窗口第二条失败事件出生即降级——响应部件 blob 从不落盘（refs 置 NULL、
// capture_status=deduped、sha 与大小保留），窗口首条保持完整并继续作为样本，
// 零 GC 往返；跨窗口第三条触发移交：旧样本（窗口首条）在同一事务内降级、
// 释放 blob 登记 GC 意图，两个窗口组的 sample_event_id 都指向第三条。

func refState(t *testing.T, implementation *sqlStore, auditLogID string) string {
	t.Helper()
	var capture string
	query := implementation.bind(`SELECT capture_status FROM ` + implementation.table("audit_payload_refs") + ` WHERE audit_log_id=?`)
	if err := implementation.db.QueryRow(query, auditLogID).Scan(&capture); err != nil {
		t.Fatal(err)
	}
	return capture
}

// TestErrorGroupDedupWindowIsTenMinutes 契约：窗口参数钉死为 10 分钟——12:06
// 在 5 分钟窗口假设下已跨窗（会触发移交），10 分钟窗口下必须仍是同窗口重复：
// 首条保持样本与完整引用，第二条出生即降级，零 GC 意图。
func TestErrorGroupDedupWindowIsTenMinutes(t *testing.T) {
	store, implementation := openPostgresStore(t)
	defer store.Close()
	ctx := context.Background()
	lease := acquireLease(t, store)

	first := dedupFailureFixture("window-pin-first", "2026-08-09T12:01:00.000Z", "first failure body", "account-1")
	if _, err := store.Persist(ctx, lease, first); err != nil {
		t.Fatal(err)
	}
	second := dedupFailureFixture("window-pin-second", "2026-08-09T12:06:00.000Z", "second failure body", "account-1")
	if _, err := store.Persist(ctx, lease, second); err != nil {
		t.Fatal(err)
	}

	if refState(t, implementation, first.ID) != string(PayloadCaptureComplete) {
		t.Fatal("10 分钟窗口下 12:06 与 12:01 同窗口，首条不得降级")
	}
	if refState(t, implementation, second.ID) != string(PayloadCaptureDeduped) {
		t.Fatal("同窗口重复必须出生即降级")
	}
	var count int
	var sample string
	if err := implementation.db.QueryRow(`SELECT count,sample_event_id FROM juhe_dataset.audit_error_groups`).Scan(&count, &sample); err != nil {
		t.Fatal(err)
	}
	if count != 2 || sample != first.ID {
		t.Fatalf("样本必须保持窗口首条: count=%d sample=%q", count, sample)
	}
	var gcCount int
	if err := implementation.db.QueryRow(`SELECT COUNT(*) FROM juhe_dataset.audit_payload_blob_gc`).Scan(&gcCount); err != nil {
		t.Fatal(err)
	}
	if gcCount != 0 {
		t.Fatalf("同窗口重复零 GC 往返: got %d", gcCount)
	}
}

// TestErrorGroupDedupIdempotentReplaySkipsHandover 契约：已 finalization 事件
// 的重放（同 ID 再 Persist）被 upsertLog 幂等忽略，不重复触发跨窗口移交——
// 窗口组数、样本指针与 GC 意图都保持移交后状态。
func TestErrorGroupDedupIdempotentReplaySkipsHandover(t *testing.T) {
	store, implementation := openPostgresStore(t)
	defer store.Close()
	ctx := context.Background()
	lease := acquireLease(t, store)

	first := dedupFailureFixture("replay-first", "2026-08-09T12:01:00.000Z", "first failure body", "account-1")
	if _, err := store.Persist(ctx, lease, first); err != nil {
		t.Fatal(err)
	}
	second := dedupFailureFixture("replay-second", "2026-08-09T12:11:00.000Z", "second failure body", "account-1")
	if _, err := store.Persist(ctx, lease, second); err != nil {
		t.Fatal(err)
	}

	result, err := store.Persist(ctx, lease, second)
	if err != nil || !result.Ignored {
		t.Fatalf("重放已 finalization 事件必须被忽略: result=%+v err=%v", result, err)
	}

	var groupCount int
	if err := implementation.db.QueryRow(`SELECT COUNT(*) FROM juhe_dataset.audit_error_groups`).Scan(&groupCount); err != nil {
		t.Fatal(err)
	}
	if groupCount != 2 {
		t.Fatalf("重放不得新增窗口组: got %d", groupCount)
	}
	var sampleRows int
	if err := implementation.db.QueryRow(`SELECT COUNT(*) FROM juhe_dataset.audit_error_groups WHERE sample_event_id=$1`, second.ID).Scan(&sampleRows); err != nil {
		t.Fatal(err)
	}
	if sampleRows != 2 {
		t.Fatalf("样本指针必须保持移交后状态: rows=%d", sampleRows)
	}
	var gcCount int
	if err := implementation.db.QueryRow(`SELECT COUNT(*) FROM juhe_dataset.audit_payload_blob_gc`).Scan(&gcCount); err != nil {
		t.Fatal(err)
	}
	if gcCount != 2 {
		t.Fatalf("重放不得追加 GC 意图: got %d want 2", gcCount)
	}
}

// TestErrorGroupDedupSharedKeyWithRetainedParts 契约：同事件内被剥离的响应
// 部件与保留侧请求部件内容共键（同 sha+contentType）时，不得误删保留侧的
// canonical 计划——误删会让引用指向从不解析的 canonical 行，canonical 不存在
// 时 FK 违例使整条 Persist 失败（独立复审 major 项回归）。

// TestErrorGroupDedupDoesNotTouchDifferentFingerprints 契约：不同 fingerprint
// （不同账户）互不降级。

// TestErrorGroupDedupKeepsSharedCanonicalBlobForNewestEvent 契约：新旧事件正文
// 完全相同时共享 canonical blob，释放引用后 ref_count 2→1，blob 不得登记 GC
// 意图、文件必须保留；只有真正无引用的 headers blob 进入 GC。

// TestErrorGroupDedupedBlobsArePhysicallyCollectedByRetention 契约：降级后的
// blob 在下一轮 CleanupRetention + cleanupScheduledBlobFiles 后文件被物理删除。
func TestErrorGroupDedupedBlobsArePhysicallyCollectedByRetention(t *testing.T) {
	store, implementation := openPostgresStore(t)
	defer store.Close()
	ctx := context.Background()
	lease := acquireLease(t, store)

	first := dedupFailureFixture("gc-first", "2026-08-09T12:01:00.000Z", "old failure body", "account-1")
	if _, err := store.Persist(ctx, lease, first); err != nil {
		t.Fatal(err)
	}
	firstHeaders, firstBody, _, _, _, _ := dedupRefState(t, implementation, first.ID)
	released := []string{firstHeaders.String, firstBody.String}
	paths := make([]string, 0, len(released))
	for _, blobID := range released {
		var storageKey string
		if err := implementation.db.QueryRow(`SELECT storage_key FROM juhe_dataset.audit_payload_blobs WHERE id=$1`, blobID).Scan(&storageKey); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, filepath.Join(implementation.blobDir, filepath.FromSlash(storageKey)))
	}

	second := dedupFailureFixture("gc-second", "2026-08-09T12:11:00.000Z", "new failure body", "account-1")
	if _, err := store.Persist(ctx, lease, second); err != nil {
		t.Fatal(err)
	}

	// 截晚于事件时间之前：仅触发无引用 blob 兜底扫描，不动仍在保留期内的事件与组。
	cutoff := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	result, err := store.CleanupRetention(ctx, lease, RetentionConfig{
		SuccessHotCutoff: cutoff, SuccessCutoff: cutoff, FailureCutoff: cutoff, ErrorGroupCutoff: cutoff, BatchSize: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedPayloadBlobs != 2 {
		t.Fatalf("retention result=%+v, want DeletedPayloadBlobs=2", result)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("降级 blob 文件必须被物理删除: %s err=%v", path, err)
		}
	}
	var blobRows, gcRows, logRows int
	if err := implementation.db.QueryRow(`SELECT COUNT(*) FROM juhe_dataset.audit_payload_blobs WHERE id IN ($1,$2)`, released[0], released[1]).Scan(&blobRows); err != nil {
		t.Fatal(err)
	}
	if err := implementation.db.QueryRow(`SELECT COUNT(*) FROM juhe_dataset.audit_payload_blob_gc`).Scan(&gcRows); err != nil {
		t.Fatal(err)
	}
	if err := implementation.db.QueryRow(`SELECT COUNT(*) FROM juhe_dataset.audit_logs WHERE id IN ($1,$2)`, first.ID, second.ID).Scan(&logRows); err != nil {
		t.Fatal(err)
	}
	if blobRows != 0 || gcRows != 0 || logRows != 2 {
		t.Fatalf("GC 后状态: blobs=%d gc=%d logs=%d", blobRows, gcRows, logRows)
	}
	_, _, _, _, capture, _ := dedupRefState(t, implementation, first.ID)
	if capture != string(PayloadCaptureDeduped) {
		t.Fatalf("降级 refs 行必须保留: capture=%q", capture)
	}
}

// TestErrorGroupDedupPreservesRequestSidePayloads 契约：降级范围仅覆盖响应/
// 错误类部件；client_request（及 upstream_request/gateway_metadata）保留原
// blob 引用与 complete 状态——同组请求体 sha 一致共享 canonical blob，历史
// 记录的请求原文打开即可见（读侧无感，BUG-0320 用户裁决）。
func TestErrorGroupDedupPreservesRequestSidePayloads(t *testing.T) {
	store, implementation := openPostgresStore(t)
	defer store.Close()
	ctx := context.Background()
	lease := acquireLease(t, store)

	requestPayload := func() AuditLogPayloadInput {
		return AuditLogPayloadInput{
			PartType:      PayloadPartClientRequest,
			SequenceIndex: intPointer(0),
			ContentType:   "application/json",
			Headers:       map[string]HeaderValues{"content-type": {Values: []string{"application/json"}}},
			Body:          PayloadBody{Bytes: []byte(`{"model":"grok-4.7","input":"stable request body"}`), Present: true},
		}
	}
	errorPayload := func(id string) AuditLogPayloadInput {
		return AuditLogPayloadInput{
			AttemptTempID: "attempt-" + id,
			PartType:      PayloadPartGatewayError,
			SequenceIndex: intPointer(1),
			ContentType:   "application/json",
			Body:          PayloadBody{Bytes: []byte("error body " + id), Present: true},
		}
	}
	build := func(id, createdAt string) AuditLogInput {
		input := fixture(id, LifecycleFinalized)
		input.Success = false
		input.AuditOutcome = AuditOutcomeUpstreamFailed
		input.ErrorCode = "upstream_unavailable"
		input.ErrorMessage = "upstream unavailable 123456789"
		input.CreatedAt = createdAt
		input.AccountID = "account-1"
		input.Attempts = []AuditLogAttemptInput{{
			TempID: "attempt-" + id, AttemptIndex: 0, AccountID: "account-1",
			UpstreamMethod: "POST", UpstreamURL: "https://upstream.example",
			StartedAt: input.StartedAt, EndedAt: input.EndedAt,
			Success: &[]bool{false}[0],
		}}
		input.Payloads = []AuditLogPayloadInput{requestPayload(), errorPayload(id)}
		return input
	}

	first := build("reqside-first", "2026-08-09T12:01:00.000Z")
	if _, err := store.Persist(ctx, lease, first); err != nil {
		t.Fatal(err)
	}
	second := build("reqside-second", "2026-08-09T12:11:00.000Z")
	if _, err := store.Persist(ctx, lease, second); err != nil {
		t.Fatal(err)
	}

	var firstRequestCapture, firstErrorCapture string
	var requestHeaders, requestBody sql.NullString
	if err := implementation.db.QueryRow(`SELECT headers_blob_id,body_blob_id,capture_status FROM juhe_dataset.audit_payload_refs WHERE audit_log_id=$1 AND part_type='client_request'`, first.ID).Scan(&requestHeaders, &requestBody, &firstRequestCapture); err != nil {
		t.Fatal(err)
	}
	if !requestHeaders.Valid || !requestBody.Valid {
		t.Fatalf("请求部件引用不得降级: headers=%v body=%v", requestHeaders, requestBody)
	}
	if firstRequestCapture != string(PayloadCaptureComplete) {
		t.Fatalf("请求部件 capture_status=%q, want complete", firstRequestCapture)
	}
	if err := implementation.db.QueryRow(`SELECT capture_status FROM juhe_dataset.audit_payload_refs WHERE audit_log_id=$1 AND part_type='gateway_error'`, first.ID).Scan(&firstErrorCapture); err != nil {
		t.Fatal(err)
	}
	if firstErrorCapture != string(PayloadCaptureDeduped) {
		t.Fatalf("错误部件必须降级: capture=%q", firstErrorCapture)
	}

	var secondRequestCapture, secondErrorCapture string
	if err := implementation.db.QueryRow(`SELECT capture_status FROM juhe_dataset.audit_payload_refs WHERE audit_log_id=$1 AND part_type='client_request'`, second.ID).Scan(&secondRequestCapture); err != nil {
		t.Fatal(err)
	}
	if err := implementation.db.QueryRow(`SELECT capture_status FROM juhe_dataset.audit_payload_refs WHERE audit_log_id=$1 AND part_type='gateway_error'`, second.ID).Scan(&secondErrorCapture); err != nil {
		t.Fatal(err)
	}
	if secondRequestCapture != string(PayloadCaptureComplete) || secondErrorCapture != string(PayloadCaptureComplete) {
		t.Fatalf("最新样本全部部件必须完整: request=%q error=%q", secondRequestCapture, secondErrorCapture)
	}

	// 同组请求体 sha 一致：两条 client_request 指向同一 canonical blob。
	var sharedBlobs int
	if err := implementation.db.QueryRow(`SELECT COUNT(DISTINCT COALESCE(body_blob_id,'')) FROM juhe_dataset.audit_payload_refs WHERE audit_log_id IN ($1,$2) AND part_type='client_request'`, first.ID, second.ID).Scan(&sharedBlobs); err != nil {
		t.Fatal(err)
	}
	if sharedBlobs != 1 {
		t.Fatalf("同组请求体必须共享 canonical blob: distinct=%d", sharedBlobs)
	}

	var requestBlobGC int
	if err := implementation.db.QueryRow(`SELECT COUNT(*) FROM juhe_dataset.audit_payload_blob_gc WHERE blob_id=$1`, requestBody.String).Scan(&requestBlobGC); err != nil {
		t.Fatal(err)
	}
	if requestBlobGC != 0 {
		t.Fatal("保留引用的请求 blob 不得登记 GC 意图")
	}
}

// TestPostgresErrorGroupHandoverSmokeWhenExplicitlyConfigured 契约：PG 形态下
// 窗口移交与同窗口出生降级成立——timestamptz 列回读的窗口文本与内存
// nodeISOString 同形比较（latestErrorGroupSample 归一路径的真实形态覆盖）。
// 需显式设置 JUHE_AI_AUDIT_LOG_TEST_POSTGRES_URL，未设置时跳过。
func TestPostgresErrorGroupHandoverSmokeWhenExplicitlyConfigured(t *testing.T) {
	postgresURL := strings.TrimSpace(os.Getenv("JUHE_AI_AUDIT_LOG_TEST_POSTGRES_URL"))
	if postgresURL == "" {
		t.Skip("未设置 JUHE_AI_AUDIT_LOG_TEST_POSTGRES_URL；跳过真实 PostgreSQL 错误组移交 smoke")
	}
	assertDestructivePostgresSmokeTarget(t, postgresURL)
	blobRoot := t.TempDir()
	store, err := OpenStore(Config{PostgresURL: postgresURL, PostgresMaxOpenConns: 4, PostgresMaxIdleConns: 2, PayloadBlobDirectory: blobRoot})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	impl := store.(*sqlStore)
	assertEmptyPostgresF3Tables(t, ctx, impl.db)
	defer cleanupPostgresSmokeRows(t, ctx, impl.db)
	runID := "f3pghandover" + strconv.FormatInt(time.Now().UnixNano(), 10)
	owner := runID + "-owner"
	lease, acquired, err := store.AcquireOwnerLease(ctx, owner, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("真实 PostgreSQL acquire 失败: acquired=%v err=%v", acquired, err)
	}
	defer func() { _ = store.ReleaseOwnerLease(ctx, lease) }()

	assertErrorRefState := func(auditLogID, wantCapture string, wantBlob bool) {
		t.Helper()
		var blobID sql.NullString
		var capture string
		if err := impl.db.QueryRowContext(ctx, `SELECT body_blob_id,capture_status FROM juhe_dataset.audit_payload_refs WHERE audit_log_id=$1 AND part_type='gateway_error'`, auditLogID).Scan(&blobID, &capture); err != nil {
			t.Fatal(err)
		}
		if capture != wantCapture || blobID.Valid != wantBlob {
			t.Fatalf("refs=%s: blob=%v capture=%q, want capture=%q blob=%v", auditLogID, blobID, capture, wantCapture, wantBlob)
		}
	}

	// 同窗口（12:01/12:04 同属 [12:00,12:10)）：第二条出生即降级，样本保持首条。
	first := dedupFailureFixture(runID+"-first", "2026-08-09T12:01:00.000Z", "pg first failure body", "account-1")
	if _, err := store.Persist(ctx, lease, first); err != nil {
		t.Fatal(err)
	}
	second := dedupFailureFixture(runID+"-second", "2026-08-09T12:04:00.000Z", "pg second failure body", "account-1")
	if _, err := store.Persist(ctx, lease, second); err != nil {
		t.Fatal(err)
	}
	assertErrorRefState(first.ID, string(PayloadCaptureComplete), true)
	assertErrorRefState(second.ID, string(PayloadCaptureDeduped), false)
	var groupCount int
	if err := impl.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM juhe_dataset.audit_error_groups`).Scan(&groupCount); err != nil {
		t.Fatal(err)
	}
	if groupCount != 1 {
		t.Fatalf("真实 PostgreSQL 窗口组数=%d, want 1", groupCount)
	}
	var sample string
	if err := impl.db.QueryRowContext(ctx, `SELECT sample_event_id FROM juhe_dataset.audit_error_groups`).Scan(&sample); err != nil {
		t.Fatal(err)
	}
	if sample != first.ID {
		t.Fatalf("真实 PostgreSQL 同窗口样本=%q, want 窗口首条 %q", sample, first.ID)
	}

	// 跨窗口（12:11）：首条降级、GC 意图登记，两个窗口组的样本指针都移交。
	third := dedupFailureFixture(runID+"-third", "2026-08-09T12:11:00.000Z", "pg third failure body", "account-1")
	if _, err := store.Persist(ctx, lease, third); err != nil {
		t.Fatal(err)
	}
	assertErrorRefState(first.ID, string(PayloadCaptureDeduped), false)
	assertErrorRefState(third.ID, string(PayloadCaptureComplete), true)
	if err := impl.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM juhe_dataset.audit_error_groups`).Scan(&groupCount); err != nil {
		t.Fatal(err)
	}
	if groupCount != 2 {
		t.Fatalf("真实 PostgreSQL 窗口组数=%d, want 2", groupCount)
	}
	var sampleRows int
	if err := impl.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM juhe_dataset.audit_error_groups WHERE sample_event_id=$1`, third.ID).Scan(&sampleRows); err != nil {
		t.Fatal(err)
	}
	if sampleRows != 2 {
		t.Fatalf("真实 PostgreSQL 移交后样本指针行=%d, want 2", sampleRows)
	}
	var gcCount int
	if err := impl.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM juhe_dataset.audit_payload_blob_gc`).Scan(&gcCount); err != nil {
		t.Fatal(err)
	}
	if gcCount != 2 {
		t.Fatalf("真实 PostgreSQL 跨窗口移交 GC 意图=%d, want 2（仅旧样本两个 blob）", gcCount)
	}
}
