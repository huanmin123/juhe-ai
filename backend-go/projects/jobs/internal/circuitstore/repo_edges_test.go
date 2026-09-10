package circuitstore

// 控制面与投影 repo 的边界补充测试：outbox claim 参数校验与租约过期再认领、
// incident 行映射可选字段透传、JSON 数组解析守卫、投影 Enqueue/Claim/Apply
// 的围栏与校验分支。全部走 SQLite fixture（PG 分支依赖数据库时钟，本地
// 不覆盖，与现有测试约定一致）。

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	_ "modernc.org/sqlite"
)

// TestControlPlaneClaimValidation 覆盖 outbox Claim 参数校验。
func TestControlPlaneClaimValidation(t *testing.T) {
	repo, _, _ := openControlPlaneFixture(t)
	ctx := context.Background()
	if _, err := repo.Claim(ctx, "  ", 1000, 30_000, 10); err == nil {
		t.Fatal("空 owner 必须报错")
	}
	if _, err := repo.Claim(ctx, "owner", -1, 30_000, 10); err == nil {
		t.Fatal("负 now 必须报错")
	}
	if _, err := repo.Claim(ctx, "owner", 1000, 0, 10); err == nil {
		t.Fatal("lease 0 必须报错")
	}
	if _, err := repo.Claim(ctx, "owner", 1000, 60*60_000+1, 10); err == nil {
		t.Fatal("lease 超上限必须报错")
	}
	if _, err := repo.Claim(ctx, "owner", 1000, 30_000, 0); err == nil {
		t.Fatal("limit 0 必须报错")
	}
	if _, err := repo.Claim(ctx, "owner", 1000, 30_000, 501); err == nil {
		t.Fatal("limit 超上限必须报错")
	}
}

// TestControlPlaneClaimExpiredLeaseReclaim 覆盖 processing 租约过期后的再认领。
func TestControlPlaneClaimExpiredLeaseReclaim(t *testing.T) {
	repo, _, db := openControlPlaneFixture(t)
	ctx := context.Background()
	seedOutboxEvent(t, db, "evt-exp", 0)
	// 第一次认领：租约 1000ms。
	claims, err := repo.Claim(ctx, "owner-1", 1000, 1000, 10)
	if err != nil || len(claims) != 1 {
		t.Fatalf("首次认领失败: %v %v", claims, err)
	}
	// 租约未到期 → 不可再认领。
	repeat, err := repo.Claim(ctx, "owner-2", 1500, 30_000, 10)
	if err != nil || len(repeat) != 0 {
		t.Fatalf("租约未到期不得再认领: %v %v", repeat, err)
	}
	// 租约到期（claim_until_ms <= now）→ 可被其他 owner 再认领。
	repeat, err = repo.Claim(ctx, "owner-2", 2100, 30_000, 10)
	if err != nil || len(repeat) != 1 {
		t.Fatalf("租约过期必须可再认领: %v %v", repeat, err)
	}
	if repeat[0].ClaimToken == claims[0].ClaimToken {
		t.Fatal("再认领必须换发新 claim token")
	}
	if repeat[0].EventID != "evt-exp" {
		t.Fatalf("再认领事件不符: %+v", repeat[0])
	}
}

func seedOutboxEvent(t *testing.T, db *sql.DB, eventID string, availableAtMS int64) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO account_circuit_outbox (
			event_id, projection_key, dedupe_key, event_type, account_id, account_runtime_key,
			circuit_scope_key, transition_id, dispatch_revision, status, available_at_ms,
			attempt_count, created_at_ms, updated_at_ms
		) VALUES (?, 'account_circuit_runtime_v1', ?, 'dispatch_revision_changed', 'acc-1', 'acc-1',
			'sk-1', 'tr-1', 9, 'pending', ?, 0, 1, 1)`, eventID, "dedupe:"+eventID, availableAtMS); err != nil {
		t.Fatal(err)
	}
}

// TestControlPlaneAckTokenMismatch 覆盖 Ack 的 token 围栏。
func TestControlPlaneAckTokenMismatch(t *testing.T) {
	repo, _, db := openControlPlaneFixture(t)
	ctx := context.Background()
	seedOutboxEvent(t, db, "evt-ack", 0)
	claims, err := repo.Claim(ctx, "owner-1", 1000, 30_000, 10)
	if err != nil || len(claims) != 1 {
		t.Fatalf("认领失败: %v %v", claims, err)
	}
	forged := claims[0]
	forged.ClaimToken = "forged-token"
	acknowledged, err := repo.Ack(ctx, forged, 2000)
	if err != nil || acknowledged {
		t.Fatalf("伪造 token 不得 ack: %v %v", acknowledged, err)
	}
	// ReleaseForReplay 用合法 token（非法 token 静默 0 行）。
	if err := repo.ReleaseForReplay(ctx, claims[0], "projector_error", 3000, 500); err != nil {
		t.Fatal(err)
	}
	var availableAt int64
	if err := db.QueryRow(`SELECT available_at_ms FROM account_circuit_outbox WHERE event_id = 'evt-ack'`).Scan(&availableAt); err != nil {
		t.Fatal(err)
	}
	if availableAt != 3500 {
		t.Fatalf("release 应写 available_at = now + retryDelay: %d", availableAt)
	}
}

// TestControlPlaneIncidentRowOptionalColumns 覆盖 incident 行所有可选列的透传。
func TestControlPlaneIncidentRowOptionalColumns(t *testing.T) {
	repo, _, db := openControlPlaneFixture(t)
	ctx := context.Background()
	if _, err := db.Exec(`
		INSERT INTO account_circuit_incidents (
			circuit_scope_key, account_id, account_runtime_key, scope_kind, key_fingerprint,
			protocol_code, request_lane, model_family, incident_id, parent_incident_id,
			child_incident_ids_json, state, generation, dispatch_revision, ledger_revision,
			transition_id, lease_id, lease_purpose, lease_until_ms, backoff_level,
			consecutive_failures, confirmation_failures_required,
			confirmation_failure_evidence_keys_json, recovering_successes,
			next_transition_at_ms, open_until_ms, last_failure_class, created_at_ms, updated_at_ms
		) VALUES ('sk-full', 'acc-1', 'acc-1', 'key', 'fp-1',
			'openai', 'text', 'gpt-4o', 'incident-full', 'incident-parent',
			'["child-b","child-a","child-b"]', 'OPEN', 3, 5, 2,
			'tr-full', 'lease-9', 'half_open', 88_888, 2,
			4, 2,
			'["AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"]', 1,
			77_777, 66_666, 'http_502', 1, 999)`); err != nil {
		t.Fatal(err)
	}
	record, err := repo.GetByScopeKey(ctx, "sk-full")
	if err != nil || record == nil {
		t.Fatalf("全字段行必须可读: %v %v", record, err)
	}
	if record.ParentIncidentID != "incident-parent" || record.KeyFingerprint != "fp-1" {
		t.Fatalf("父 incident / 指纹不符: %+v", record)
	}
	if record.ProtocolCode != "openai" || record.RequestLane != "text" || record.ModelFamily != "gpt-4o" {
		t.Fatalf("协议列不符: %+v", record)
	}
	if record.LeaseID != "lease-9" || record.LeasePurpose != "half_open" || record.LeaseUntilMS == nil || *record.LeaseUntilMS != 88_888 {
		t.Fatalf("租约列不符: %+v", record)
	}
	if len(record.ChildIncidentIDs) != 2 || record.ChildIncidentIDs[0] != "child-b" || record.ChildIncidentIDs[1] != "child-a" {
		t.Fatalf("child 数组应去重保持序: %v", record.ChildIncidentIDs)
	}
	if len(record.ConfirmationFailureEvidenceKeys) != 2 {
		t.Fatalf("evidence keys 应去重保留: %v", record.ConfirmationFailureEvidenceKeys)
	}
	if record.ConfirmationFailureEvidenceKeys[0] != strings.Repeat("a", 64) {
		t.Fatalf("evidence key 应归一小写: %v", record.ConfirmationFailureEvidenceKeys[0])
	}
	if record.NextTransitionAtMS == nil || *record.NextTransitionAtMS != 77_777 {
		t.Fatalf("nextTransitionAtMS 不符: %+v", record)
	}
	if record.OpenUntilMS == nil || *record.OpenUntilMS != 66_666 {
		t.Fatalf("openUntilMS 不符: %+v", record)
	}
	if record.LastFailureClass != "http_502" || record.BackoffLevel != 2 || record.ConsecutiveFailures != 4 {
		t.Fatalf("失败分类列不符: %+v", record)
	}
	// 未知 scopeKey → nil（不报错）。
	missing, err := repo.GetByScopeKey(ctx, "sk-none")
	if err != nil || missing != nil {
		t.Fatalf("未知 scopeKey 应返回 nil: %v %v", missing, err)
	}
}

// TestParseBoundedIDArrayGuards 覆盖 child 数组解析的守卫分支。
func TestParseBoundedIDArrayGuards(t *testing.T) {
	if _, err := parseBoundedIDArray("{broken"); err == nil {
		t.Fatal("非法 JSON 必须报错")
	}
	tooMany := `["` + strings.Repeat("a", 1) + `"]`
	tooMany = "[" + strings.TrimSuffix(strings.Repeat(`"x",`, 65), ",") + "]"
	if _, err := parseBoundedIDArray(tooMany); err == nil {
		t.Fatal("超过 64 项必须报错")
	}
	if _, err := parseBoundedIDArray(`[""]`); err == nil {
		t.Fatal("空项必须报错")
	}
	if _, err := parseBoundedIDArray(`["` + strings.Repeat("x", 257) + `"]`); err == nil {
		t.Fatal("超过 256 字符必须报错")
	}
	values, err := parseBoundedIDArray(`[" a ","a","b"]`)
	if err != nil || len(values) != 2 || values[0] != "a" || values[1] != "b" {
		t.Fatalf("修剪与去重不符: %v %v", values, err)
	}
}

// TestParseEvidenceKeysGuards 覆盖 evidence keys 解析的守卫分支。
func TestParseEvidenceKeysGuards(t *testing.T) {
	if _, err := parseEvidenceKeys("{broken", 1); err == nil {
		t.Fatal("非法 JSON 必须报错")
	}
	if _, err := parseEvidenceKeys(`["`+strings.Repeat("a", 64)+`","`+strings.Repeat("b", 64)+`","`+strings.Repeat("c", 64)+`"]`, 1); err == nil {
		t.Fatal("超过 required+1 必须报错")
	}
	if _, err := parseEvidenceKeys(`["short"]`, 1); err == nil {
		t.Fatal("非 SHA256 必须报错")
	}
	values, err := parseEvidenceKeys(`["`+strings.Repeat("A", 64)+`","`+strings.Repeat("a", 64)+`"]`, 1)
	if err != nil || len(values) != 1 || values[0] != strings.Repeat("a", 64) {
		t.Fatalf("大写应归一并去重: %v %v", values, err)
	}
}

// TestListByRuntimeKeysValidation 覆盖运行态键查询的校验与 tombstone 过滤。
func TestListByRuntimeKeysValidation(t *testing.T) {
	repo, _, db := openControlPlaneFixture(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO accounts (id, dispatch_revision) VALUES ('acc-1', 5)`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ListByRuntimeKeys(ctx, []string{"   "}, false, 1000); err == nil {
		t.Fatal("空键必须报错")
	}
	tooMany := make([]string, 101)
	for i := range tooMany {
		tooMany[i] = "k" + itoa(i)
	}
	if _, err := repo.ListByRuntimeKeys(ctx, tooMany, false, 1000); err == nil {
		t.Fatal("超过 100 键必须报错")
	}
	if _, err := repo.ListByRuntimeKeys(ctx, nil, false, 1000); err != nil {
		t.Fatalf("空列表返回空: %v", err)
	}

	// retained tombstone：CLOSED + retained_until_ms 未来 → include 时保留。
	if _, err := db.Exec(`
		INSERT INTO account_circuit_incidents (
			circuit_scope_key, account_id, account_runtime_key, scope_kind, incident_id,
			child_incident_ids_json, state, generation, dispatch_revision, ledger_revision,
			transition_id, retained_until_ms, created_at_ms, updated_at_ms
		) VALUES ('sk-closed', 'acc-1', 'acc-1', 'account', 'in-c', '[]', 'CLOSED', 1, 5, 1,
			'tr-c', 5000, 1, 100),
			('sk-closed-expired', 'acc-1', 'acc-1', 'account', 'in-e', '[]', 'CLOSED', 1, 5, 1,
			'tr-e', 50, 1, 90)`); err != nil {
		t.Fatal(err)
	}
	records, err := repo.ListByRuntimeKeys(ctx, []string{"acc-1"}, true, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].CircuitScopeKey != "sk-closed" {
		t.Fatalf("只有未过期的 retained tombstone 保留: %+v", records)
	}
	// include=false 时 CLOSED 全部过滤。
	records, err = repo.ListByRuntimeKeys(ctx, []string{"acc-1"}, false, 1000)
	if err != nil || len(records) != 0 {
		t.Fatalf("不含 retained 时 CLOSED 必须过滤: %v %v", records, err)
	}
}

// TestListForRebuildValidation 覆盖 rebuild 分页参数校验。
func TestListForRebuildValidation(t *testing.T) {
	repo, _, _ := openControlPlaneFixture(t)
	ctx := context.Background()
	if _, err := repo.ListForRebuild(ctx, opsjobs.RebuildPageQuery{NowMS: 1000, Limit: 0}); err == nil {
		t.Fatal("limit 0 必须报错")
	}
	longKey := strings.Repeat("k", 2049)
	if _, err := repo.ListForRebuild(ctx, opsjobs.RebuildPageQuery{
		NowMS: 1000, Limit: 10, AfterCircuitScopeKey: &longKey,
	}); err == nil {
		t.Fatal("afterCircuitScopeKey 超长必须报错")
	}
}

// TestNewControlPlaneRepoNilDB 覆盖构造校验。
func TestNewControlPlaneRepoNilDB(t *testing.T) {
	if _, err := NewControlPlaneRepo(ControlPlaneConfig{}); err == nil {
		t.Fatal("缺 DB 必须报错")
	}
	if _, err := NewReconcileCursorStore(ControlPlaneConfig{}); err == nil {
		t.Fatal("缺 DB 必须报错")
	}
}

// TestEnqueueDueTransitionsDueProjections 覆盖到期投影转 dirty。
func TestEnqueueDueTransitionsDueProjections(t *testing.T) {
	repo, db := openListAvailabilityFixture(t)
	ctx := context.Background()
	nowMS := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC).UnixMilli()
	if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id) VALUES ('acc-due', 'viewer-1'), ('acc-later', 'viewer-1')`); err != nil {
		t.Fatal(err)
	}
	insertProjection := func(t *testing.T, accountID, nextTransitionAt string) {
		t.Helper()
		if _, err := db.Exec(`
			INSERT INTO account_list_availability_projections (
				viewer_system_account_id, account_id, effective_status, schedulable_bucket,
				provider_code, provider_protocol_profile_id, account_type, name_sort_key,
				priority_sort_key, super_priority_sort_key, fallback_sort_key, concurrency_sort_key,
				created_at_sort_key, payload_json, source_generation, next_transition_at, projected_at
			) VALUES ('viewer-1', ?, 'active', 'enabled', 'openai', 'p', 'api_key', ?,
				0, 0, 0, 0, '2026-01-01T00:00:00.000Z', '{}', 1, ?, '2026-09-04T09:00:00.000Z')`,
			accountID, accountID, nextTransitionAt); err != nil {
			t.Fatal(err)
		}
	}
	// 到期（<= now）与未到期各一。
	insertProjection(t, "acc-due", "2026-09-04T09:59:59Z")
	insertProjection(t, "acc-later", "2026-09-04T11:00:00Z")

	enqueued, err := repo.EnqueueDue(ctx, 10, nowMS)
	if err != nil {
		t.Fatal(err)
	}
	if enqueued != 1 {
		t.Fatalf("只有到期投影应入队: %d", enqueued)
	}
	var reason string
	if err := db.QueryRow(`SELECT reason FROM account_list_availability_dirty WHERE account_id = 'acc-due'`).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "projection_due_transition" {
		t.Fatalf("到期入队 reason 不符: %s", reason)
	}
	// 重复入队推进 generation 且保留最早 available_at。
	enqueued, err = repo.EnqueueDue(ctx, 10, nowMS)
	if err != nil || enqueued != 0 {
		t.Fatalf("已有 dirty 的账户不得重复入队: %d %v", enqueued, err)
	}
}

// TestEnqueueDueAndMissingValidation 覆盖入队 limit 校验。
func TestEnqueueDueAndMissingValidation(t *testing.T) {
	repo, _ := openListAvailabilityFixture(t)
	ctx := context.Background()
	if _, err := repo.EnqueueDue(ctx, 0, 1000); err == nil {
		t.Fatal("limit 0 必须报错")
	}
	if _, err := repo.EnqueueMissing(ctx, 0, 1000); err == nil {
		t.Fatal("limit 0 必须报错")
	}
}

// TestEnqueueMissingFilters 覆盖 bootstrap 扫描的可见性过滤。
func TestEnqueueMissingFilters(t *testing.T) {
	repo, db := openListAvailabilityFixture(t)
	ctx := context.Background()
	nowMS := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC).UnixMilli()
	if _, err := db.Exec(`
		INSERT INTO accounts (id, system_account_id, created_at) VALUES
			('acc-keep', 'viewer-1', '2026-01-01T00:00:00.000Z'),
			('acc-deleted', 'viewer-1', '2026-01-02T00:00:00.000Z'),
			('acc-paused-auth', 'viewer-1', '2026-01-03T00:00:00.000Z'),
			('acc-revoked-auth', 'viewer-1', '2026-01-04T00:00:00.000Z');
		UPDATE accounts SET deleted_at = '2026-02-01T00:00:00.000Z' WHERE id = 'acc-deleted';
		UPDATE accounts SET authorization_instance_authorization_id = 'auth-paused' WHERE id = 'acc-paused-auth';
		UPDATE accounts SET authorization_instance_authorization_id = 'auth-revoked' WHERE id = 'acc-revoked-auth';
		INSERT INTO resource_authorizations (id, status) VALUES ('auth-paused', 'paused'), ('auth-revoked', 'revoked');`); err != nil {
		t.Fatal(err)
	}
	enqueued, err := repo.EnqueueMissing(ctx, 10, nowMS)
	if err != nil {
		t.Fatal(err)
	}
	if enqueued != 2 {
		t.Fatalf("只应入队 keep 与 paused-auth（revoked 不可见、deleted 跳过）: %d", enqueued)
	}
	var viewer string
	if err := db.QueryRow(`SELECT viewer_system_account_id FROM account_list_availability_dirty WHERE account_id = 'acc-keep'`).Scan(&viewer); err != nil {
		t.Fatal(err)
	}
	if viewer != "viewer-1" {
		t.Fatalf("dirty 应带 viewer 上下文: %s", viewer)
	}
}

// TestMarkDirtyTxGuards 覆盖脏标记的参数与存在性守卫。
func TestMarkDirtyTxGuards(t *testing.T) {
	repo, db := openListAvailabilityFixture(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id) VALUES ('acc-1', 'viewer-1')`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := repo.markDirtyTx(ctx, tx, "   ", "reason", 1, 1); err == nil {
		t.Fatal("空 accountID 必须报错")
	}
	if err := repo.markDirtyTx(ctx, tx, "acc-1", "  ", 1, 1); err == nil {
		t.Fatal("空 reason 必须报错")
	}
	if err := repo.markDirtyTx(ctx, tx, "acc-ghost", "reason", 1, 1); err == nil {
		t.Fatal("不存在账户必须报错（未写入）")
	}
	if err := repo.markDirtyTx(ctx, tx, "acc-1", "reason", 1, 1); err != nil {
		t.Fatalf("合法脏标记失败: %v", err)
	}
}

// TestClaimDirtyValidation 覆盖投影 dirty claim 的参数校验。
func TestClaimDirtyValidation(t *testing.T) {
	repo, _ := openListAvailabilityFixture(t)
	ctx := context.Background()
	if _, err := repo.ClaimDirty(ctx, "  ", 10, 1000, 1); err == nil {
		t.Fatal("空 owner 必须报错")
	}
	if _, err := repo.ClaimDirty(ctx, "owner", 0, 1000, 1); err == nil {
		t.Fatal("limit 0 必须报错")
	}
	if _, err := repo.ClaimDirty(ctx, "owner", 10, 0, 1); err == nil {
		t.Fatal("lease 0 必须报错")
	}
	if _, err := repo.ClaimDirty(ctx, "owner", 10, 1000, -1); err == nil {
		t.Fatal("负 now 必须报错")
	}
}

// TestApplyClaimsGuards 覆盖批量应用的围栏与覆盖冲突。
func TestApplyClaimsGuards(t *testing.T) {
	repo, db := openListAvailabilityFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)
	nowMS := now.UnixMilli()
	if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id) VALUES ('acc-1', 'viewer-1')`); err != nil {
		t.Fatal(err)
	}
	// 围栏无效（generation 0 / 空 token）→ 报错。
	_, err := repo.ApplyClaims(ctx, []opsjobs.ProjectionWrite{{}})
	if err == nil {
		t.Fatal("无效围栏必须报错")
	}
	// claim 已删（dirty 不存在）→ result false。
	result, err := repo.ApplyClaims(ctx, []opsjobs.ProjectionWrite{{
		Claim: opsjobs.DirtyClaim{AccountID: "acc-1", Generation: 1, ClaimToken: "token-gone"},
		Item:  validProjectionItem("acc-1"),
		Scope: opsjobs.ProjectionScope{ViewerSystemAccountID: "viewer-1", AccountID: "acc-1", CreatedAt: &[]string{"2026-01-01T00:00:00.000Z"}[0]},
		Now:   now,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result["token-gone"] != false {
		t.Fatalf("缺失 claim 必须记 false: %v", result)
	}
	// generation 被更高版本覆盖 → 报错。
	if _, err := db.Exec(`
		INSERT INTO account_list_availability_projections (
			viewer_system_account_id, account_id, effective_status, schedulable_bucket,
			provider_code, provider_protocol_profile_id, account_type, name_sort_key,
			priority_sort_key, super_priority_sort_key, fallback_sort_key, concurrency_sort_key,
			created_at_sort_key, payload_json, source_generation, projected_at
		) VALUES ('viewer-1', 'acc-1', 'active', 'enabled', 'openai', 'p', 'api_key', 'acc-1',
			0, 0, 0, 0, '2026-01-01T00:00:00.000Z', '{}', 9, '2026-09-04T09:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO account_list_availability_dirty (
			account_id, viewer_system_account_id, generation, applied_generation, reason,
			available_at_ms, attempt_count, created_at_ms, updated_at_ms
		) VALUES ('acc-1', 'viewer-1', 2, 0, 'projection_refresh_failed', ?, 0, ?, ?)`, nowMS, nowMS, nowMS); err != nil {
		t.Fatal(err)
	}
	claims, err := repo.ClaimDirty(ctx, "owner-1", 10, 30_000, nowMS)
	if err != nil || len(claims) != 1 {
		t.Fatalf("认领失败: %v %v", claims, err)
	}
	if _, err := repo.ApplyClaims(ctx, []opsjobs.ProjectionWrite{{
		Claim: claims[0],
		Item:  validProjectionItem("acc-1"),
		Scope: opsjobs.ProjectionScope{ViewerSystemAccountID: "viewer-1", AccountID: "acc-1", CreatedAt: &[]string{"2026-01-01T00:00:00.000Z"}[0]},
		Now:   now,
	}}); err == nil {
		t.Fatal("generation 低于现有投影必须报错（被更高版本覆盖）")
	}
}

// validProjectionItem 构造能通过 normalizeProjectionWrite 的最小载荷。
func validProjectionItem(accountID string) opsjobs.ProjectionItem {
	return opsjobs.ProjectionItem{
		AccountID:                 accountID,
		EffectiveStatus:           "active",
		ProviderCode:              "openai",
		ProviderProtocolProfileID: "p",
		AccountType:               "api_key",
		Name:                      "账户",
		Payload:                   map[string]any{"id": accountID},
	}
}

// TestApplyDeletionClaimWithoutProjection 覆盖无投影行的删除型 claim。
func TestApplyDeletionClaimWithoutProjection(t *testing.T) {
	repo, db := openListAvailabilityFixture(t)
	ctx := context.Background()
	nowMS := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC).UnixMilli()
	if _, err := db.Exec(`
		INSERT INTO account_list_availability_dirty (
			account_id, viewer_system_account_id, generation, applied_generation, reason,
			available_at_ms, attempt_count, created_at_ms, updated_at_ms
		) VALUES ('acc-2', 'viewer-1', 3, 0, 'projection_scope_removed', ?, 0, ?, ?)`, nowMS, nowMS, nowMS); err != nil {
		t.Fatal(err)
	}
	claims, err := repo.ClaimDirty(ctx, "owner-1", 10, 30_000, nowMS)
	if err != nil || len(claims) != 1 {
		t.Fatalf("认领失败: %v %v", claims, err)
	}
	// 无投影行 → 返回 false 但 dirty 仍被确认删除。
	applied, err := repo.ApplyDeletionClaim(ctx, claims[0])
	if err != nil {
		t.Fatal(err)
	}
	if applied {
		t.Fatal("无投影行的删除型 claim 应返回 false")
	}
	var dirtyCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM account_list_availability_dirty`).Scan(&dirtyCount); err != nil {
		t.Fatal(err)
	}
	if dirtyCount != 0 {
		t.Fatalf("删除型 claim 必须确认删除 dirty: %d", dirtyCount)
	}
}

// TestReleaseForReplayValidation 覆盖释放重放的参数校验与 token 围栏。
func TestReleaseForReplayValidation(t *testing.T) {
	repo, _ := openListAvailabilityFixture(t)
	ctx := context.Background()
	base := opsjobs.ListAvailabilityReplayInput{
		AccountID: "acc-1", Generation: 1, ClaimToken: "token",
		Reason: "projection_refresh_failed", RetryDelayMS: 100, NowMS: 1000,
	}
	cases := []struct {
		name   string
		mutate func(*opsjobs.ListAvailabilityReplayInput)
	}{
		{"空账户", func(i *opsjobs.ListAvailabilityReplayInput) { i.AccountID = " " }},
		{"generation 0", func(i *opsjobs.ListAvailabilityReplayInput) { i.Generation = 0 }},
		{"空 token", func(i *opsjobs.ListAvailabilityReplayInput) { i.ClaimToken = "" }},
		{"空 reason", func(i *opsjobs.ListAvailabilityReplayInput) { i.Reason = " " }},
		{"负重试延迟", func(i *opsjobs.ListAvailabilityReplayInput) { i.RetryDelayMS = -1 }},
		{"负 now", func(i *opsjobs.ListAvailabilityReplayInput) { i.NowMS = -1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := base
			tc.mutate(&input)
			if _, err := repo.ReleaseForReplay(ctx, input); err == nil {
				t.Fatal("非法参数必须报错")
			}
		})
	}
	// token 不匹配 → false（0 行更新）。
	released, err := repo.ReleaseForReplay(ctx, base)
	if err != nil || released {
		t.Fatalf("不存在的 claim 不得报告释放: %v %v", released, err)
	}
}

// TestViewerHealthValidation 覆盖 viewer 健康面的参数校验。
func TestViewerHealthValidation(t *testing.T) {
	repo, _ := openListAvailabilityFixture(t)
	ctx := context.Background()
	if _, err := repo.EnsureViewerHealth(ctx, 0, "2026-09-04T10:00:00.000Z"); err == nil {
		t.Fatal("limit 0 必须报错")
	}
	// 空白 updatedAt 回落当前时钟（requireUpdatedAt 契约），超长才报错。
	if _, err := repo.EnsureViewerHealth(ctx, 10, "  "); err != nil {
		t.Fatalf("空白 updatedAt 应回落当前时钟: %v", err)
	}
	if _, err := repo.EnsureViewerHealth(ctx, 10, strings.Repeat("x", 65)); err == nil {
		t.Fatal("超长 updatedAt 必须报错")
	}
	if _, err := repo.ListViewerHealthRefreshCandidates(ctx, 0); err == nil {
		t.Fatal("limit 0 必须报错")
	}
	if err := repo.RefreshViewerHealth(ctx, "  ", "2026-09-04T10:00:00.000Z"); err == nil {
		t.Fatal("空 viewer 必须报错")
	}
	if err := repo.RefreshViewerHealth(ctx, "viewer-1", strings.Repeat("x", 65)); err == nil {
		t.Fatal("超长 updatedAt 必须报错")
	}
}

// TestNormalizeProjectionWriteValidation 覆盖投影写归一化的校验矩阵。
func TestNormalizeProjectionWriteValidation(t *testing.T) {
	base := func() opsjobs.ProjectionWrite {
		return opsjobs.ProjectionWrite{
			Claim: opsjobs.DirtyClaim{AccountID: "acc-1", Generation: 2, ClaimToken: "token"},
			Scope: opsjobs.ProjectionScope{
				ViewerSystemAccountID: "viewer-1", AccountID: "acc-1",
				CreatedAt: &[]string{"2026-01-01T00:00:00.000Z"}[0],
			},
			Item: validProjectionItem("acc-1"),
			Now:  time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC),
		}
	}
	cases := []struct {
		name   string
		mutate func(*opsjobs.ProjectionWrite)
	}{
		{"空 viewer", func(w *opsjobs.ProjectionWrite) { w.Scope.ViewerSystemAccountID = " " }},
		{"空 account", func(w *opsjobs.ProjectionWrite) { w.Item.AccountID = " " }},
		{"空 provider", func(w *opsjobs.ProjectionWrite) { w.Item.ProviderCode = " " }},
		{"空 profile", func(w *opsjobs.ProjectionWrite) { w.Item.ProviderProtocolProfileID = " " }},
		{"空 type", func(w *opsjobs.ProjectionWrite) { w.Item.AccountType = " " }},
		{"空 name", func(w *opsjobs.ProjectionWrite) { w.Item.Name = "  " }},
		{"缺 createdAt", func(w *opsjobs.ProjectionWrite) { w.Scope.CreatedAt = nil }},
		{"负并发", func(w *opsjobs.ProjectionWrite) { w.Item.CurrentConcurrency = -1 }},
		{"generation 0", func(w *opsjobs.ProjectionWrite) { w.Claim.Generation = 0 }},
		{"空 tag", func(w *opsjobs.ProjectionWrite) { w.Item.TagIDs = []string{"  "} }},
		{"空 term", func(w *opsjobs.ProjectionWrite) { w.SearchTerms = []string{""} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			write := base()
			tc.mutate(&write)
			if _, err := normalizeProjectionWrite(write); err == nil {
				t.Fatal("非法输入必须报错")
			}
		})
	}

	// 合法写入：effectiveAvailable 显式优先、source 账户作为并发维度。
	write := base()
	available := false
	write.Item.EffectiveAvailable = &available
	write.Item.SourceAccountID = "src-1"
	write.Item.TagIDs = []string{"tag-b", "tag-a", "tag-b"}
	write.SearchTerms = []string{"账", "账"}
	normalized, err := normalizeProjectionWrite(write)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.schedulableBucket != "disabled" {
		t.Fatalf("active + 不可用应归 disabled 桶（SchedulableBucket 契约）: %s", normalized.schedulableBucket)
	}
	if normalized.concurrencyAccountID != "src-1" {
		t.Fatalf("有 source 时并发维度应为 source: %s", normalized.concurrencyAccountID)
	}
	if len(normalized.tagIDs) != 2 || normalized.tagIDs[0] != "tag-b" || normalized.tagIDs[1] != "tag-a" {
		t.Fatalf("tag 应去重保持序: %v", normalized.tagIDs)
	}
	if len(normalized.searchTerms) != 1 || normalized.searchTerms[0] != "账" {
		t.Fatalf("term 应去重: %v", normalized.searchTerms)
	}
	if !normalized.searchIndexComplete {
		t.Fatal("有 term 时索引必须标记 complete")
	}
	// payload 的 effectiveAvailable 仅在显式字段缺失时生效。
	write2 := base()
	write2.Item.Payload = map[string]any{"effectiveAvailable": false}
	normalized2, err := normalizeProjectionWrite(write2)
	if err != nil {
		t.Fatal(err)
	}
	if normalized2.schedulableBucket != "disabled" {
		t.Fatalf("payload 适配键 false 应生效（active+false = disabled 桶）: %s", normalized2.schedulableBucket)
	}
}

// TestListAvailabilityRepoSmallHelpers 覆盖 repo 小工具。
func TestListAvailabilityRepoSmallHelpers(t *testing.T) {
	if boolSortKey(true) != 1 || boolSortKey(false) != 0 {
		t.Fatal("boolSortKey 语义不符")
	}
	if textPtr("  x ") == nil || *textPtr("  x ") != "x" {
		t.Fatal("textPtr 应修剪")
	}
	if textPtr("   ") != nil {
		t.Fatal("空白应返回 nil")
	}
	if got := placeholdersFor(3); got != "?, ?, ?" {
		t.Fatalf("占位符不符: %s", got)
	}
	if _, err := normalizedIDList([]string{"  "}); err == nil {
		t.Fatal("空 id 必须报错")
	}
	if _, err := normalizedIDList([]string{strings.Repeat("x", 257)}); err == nil {
		t.Fatal("超长 id 必须报错")
	}
	ids, err := normalizedIDList([]string{" a ", "a", "b"})
	if err != nil || len(ids) != 2 || ids[0] != "a" {
		t.Fatalf("归一去重不符: %v %v", ids, err)
	}
	if got := instantParam(true, "bad-time", nil); got != "bad-time" {
		t.Fatalf("解析失败回退文本: %v", got)
	}
	if got := boolLit(true, true); got != "TRUE" {
		t.Fatalf("PG true 字面量: %s", got)
	}
	if got := boolLit(true, false); got != "FALSE" {
		t.Fatalf("PG false 字面量: %s", got)
	}
	if got := boolLit(false, true); got != "1" {
		t.Fatalf("SQLite true 字面量: %s", got)
	}
	if got := normalizeAccountNameSearchText("  Ａｃｃ "); got != "Acc" {
		t.Fatalf("NFKC 兼容折叠全角 + trim: %q", got)
	}
	// nextTransitionAtOrEmpty：全部候选过滤为严格未来。
	now := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)
	if got := nextTransitionAtOrEmpty([]string{"2026-09-04T09:00:00.000Z", "2020-01-01T00:00:00.000Z"}, now); got != "" {
		t.Fatalf("无未来候选应为空: %s", got)
	}
	if got := nextTransitionAtOrEmpty([]string{"2026-09-04T09:00:00.000Z", "2030-01-01T00:00:00.000Z"}, now); got != "2030-01-01T00:00:00.000Z" {
		t.Fatalf("应取最早未来候选: %s", got)
	}
}
