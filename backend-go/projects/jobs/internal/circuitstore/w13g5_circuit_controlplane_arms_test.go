package circuitstore

// w13g5_circuit_controlplane_arms_test.go 覆盖控制面 outbox claim/ack/release
// 与 reconcile 游标的深层 err 传播臂、输入校验臂与数据驱动扫描错误臂。
// 注入子串取自各语句唯一片段，保证命中精确、可回放。

import (
	"context"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

func TestW13g5ControlClaimValidationArms(t *testing.T) {
	_, repo, _, _ := w13g5CircuitFixture(t)
	ctx := context.Background()
	longOwner := strings.Repeat("w", 129)
	if _, err := repo.Claim(ctx, longOwner, 0, 1000, 10); err == nil {
		t.Fatal("超长 owner 必须报错")
	}
	if _, err := repo.Claim(ctx, "", 0, 1000, 10); err == nil {
		t.Fatal("空 owner 必须报错")
	}
	if _, err := repo.Claim(ctx, "w13g5-owner", -1, 1000, 10); err == nil {
		t.Fatal("负 nowMs 必须报错")
	}
	if _, err := repo.Claim(ctx, "w13g5-owner", 0, 0, 10); err == nil {
		t.Fatal("零 lease 必须报错")
	}
	if _, err := repo.Claim(ctx, "w13g5-owner", 0, 60*60_000+1, 10); err == nil {
		t.Fatal("超限 lease 必须报错")
	}
	if _, err := repo.Claim(ctx, "w13g5-owner", 0, 1000, 0); err == nil {
		t.Fatal("零 limit 必须报错")
	}
	if _, err := repo.Claim(ctx, "w13g5-owner", 0, 1000, 501); err == nil {
		t.Fatal("超限 limit 必须报错")
	}
}

func TestW13g5ControlClaimSQLiteArms(t *testing.T) {
	ctx := context.Background()
	// 每个注入阶段独立 fixture，避免失败注入后的连接级事务残留影响后续断言。
	t.Run("begin 失败", func(t *testing.T) {
		_, repo, _, spec := w13g5CircuitFixture(t)
		w13g5SeedOutbox(t, repo.db, "w13g5-evt-1", "dispatch_revision_changed", "pending", "", 9, 0)
		spec.arm("w13g5-BEGIN")
		if _, err := repo.Claim(ctx, "w13g5-owner", 1000, 30_000, 10); err == nil {
			t.Fatal("BeginTx 失败必须传播")
		}
	})
	t.Run("claim UPDATE 失败", func(t *testing.T) {
		_, repo, db, spec := w13g5CircuitFixture(t)
		w13g5SeedOutbox(t, db, "w13g5-evt-1", "dispatch_revision_changed", "pending", "", 9, 0)
		spec.armOnce("SET status = 'processing'")
		if _, err := repo.Claim(ctx, "w13g5-owner", 1000, 30_000, 10); err == nil {
			t.Fatal("claim UPDATE 失败必须传播")
		}
	})
	t.Run("提交失败", func(t *testing.T) {
		_, repo, db, spec := w13g5CircuitFixture(t)
		w13g5SeedOutbox(t, db, "w13g5-evt-1", "dispatch_revision_changed", "pending", "", 9, 0)
		spec.armOnce("w13g5-COMMIT")
		if _, err := repo.Claim(ctx, "w13g5-owner", 1000, 30_000, 10); err == nil {
			t.Fatal("提交失败必须传播")
		}
	})
	t.Run("成功路径", func(t *testing.T) {
		_, repo, db, _ := w13g5CircuitFixture(t)
		w13g5SeedOutbox(t, db, "w13g5-evt-1", "dispatch_revision_changed", "pending", "", 9, 0)
		claims, err := repo.Claim(ctx, "w13g5-owner", 2000, 30_000, 10)
		if err != nil || len(claims) != 1 {
			t.Fatalf("claim 必须成功: %+v %v", claims, err)
		}
	})
}

func TestW13g5ControlClaimScanErrorArm(t *testing.T) {
	_, repo, db, _ := w13g5CircuitFixture(t)
	ctx := context.Background()
	// dispatch_revision 写入非数值文本使 Scan 失败（SQLite 动态类型）。
	if _, err := db.Exec(`INSERT INTO account_circuit_outbox (
		event_id, projection_key, dedupe_key, event_type, account_id, account_runtime_key,
		transition_id, dispatch_revision, status, available_at_ms, created_at_ms, updated_at_ms
	) VALUES ('w13g5-evt-bad', 'account_circuit_runtime_v1', 'w13g5-d', 'dispatch_revision_changed',
		'w13g5-acc', 'w13g5-acc', 'w13g5-tr', 'w13g5-not-number', 'pending', 0, 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Claim(ctx, "w13g5-owner", 1000, 30_000, 10); err == nil {
		t.Fatal("dispatch_revision 扫描失败必须传播")
	}
}

func w13g5AckEvent(eventID, claimToken string) opsjobs.OutboxEvent {
	return opsjobs.OutboxEvent{
		EventID: eventID, ProjectionKey: ProjectionKey, ClaimToken: claimToken,
	}
}

func TestW13g5ControlAckArms(t *testing.T) {
	_, repo, db, spec := w13g5CircuitFixture(t)
	ctx := context.Background()
	// 输入校验臂。
	if _, err := repo.Ack(ctx, w13g5AckEvent("", ""), 0); err == nil {
		t.Fatal("空 eventId 必须报错")
	}
	if _, err := repo.Ack(ctx, w13g5AckEvent(strings.Repeat("e", 257), "w13g5-token"), 0); err == nil {
		t.Fatal("超长 eventId 必须报错")
	}
	if _, err := repo.Ack(ctx, opsjobs.OutboxEvent{EventID: "w13g5-evt", ProjectionKey: "", ClaimToken: "t"}, 0); err == nil {
		t.Fatal("空 projectionKey 必须报错")
	}
	if _, err := repo.Ack(ctx, opsjobs.OutboxEvent{EventID: "w13g5-evt", ProjectionKey: ProjectionKey, ClaimToken: ""}, 0); err == nil {
		t.Fatal("空 claimToken 必须报错")
	}
	// 行缺失 → false, nil。
	w13g5SeedAccount(t, db, "w13g5-acc", "w13g5-viewer")
	acknowledged, err := repo.Ack(ctx, w13g5AckEvent("w13g5-missing", "w13g5-token"), 0)
	if err != nil || acknowledged {
		t.Fatalf("缺失事件必须返回 false: %v %v", acknowledged, err)
	}
	// BeginTx 失败臂。
	spec.arm("w13g5-BEGIN")
	if _, err := repo.Ack(ctx, w13g5AckEvent("w13g5-missing", "w13g5-token"), 0); err == nil {
		t.Fatal("BeginTx 失败必须传播")
	}
	spec.disarm()
	// select 失败臂。
	spec.armOnce("WHERE event_id = ?")
	if _, err := repo.Ack(ctx, w13g5AckEvent("w13g5-missing", "w13g5-token"), 0); err == nil {
		t.Fatal("select 失败必须传播")
	}
	spec.disarm()
	// projectionKey 不匹配 → false, nil。
	w13g5SeedOutbox(t, db, "w13g5-evt-key", "dispatch_revision_changed", "processing", "w13g5-token", 9, 0)
	mismatch := opsjobs.OutboxEvent{EventID: "w13g5-evt-key", ProjectionKey: "w13g5-other", ClaimToken: "w13g5-token"}
	if acknowledged, err := repo.Ack(ctx, mismatch, 0); err != nil || acknowledged {
		t.Fatalf("projectionKey 不匹配必须返回 false: %v %v", acknowledged, err)
	}
	// 非 processing 状态 → false, nil。
	if _, err := db.Exec(`UPDATE account_circuit_outbox SET status = 'pending' WHERE event_id = 'w13g5-evt-key'`); err != nil {
		t.Fatal(err)
	}
	if acknowledged, err := repo.Ack(ctx, w13g5AckEvent("w13g5-evt-key", "w13g5-token"), 0); err != nil || acknowledged {
		t.Fatalf("非 processing 必须返回 false: %v %v", acknowledged, err)
	}
	// dispatched 幂等短路 → true。
	if _, err := db.Exec(`UPDATE account_circuit_outbox SET status = 'dispatched' WHERE event_id = 'w13g5-evt-key'`); err != nil {
		t.Fatal(err)
	}
	if acknowledged, err := repo.Ack(ctx, w13g5AckEvent("w13g5-evt-key", "w13g5-token"), 0); err != nil || !acknowledged {
		t.Fatalf("dispatched 幂等必须返回 true: %v %v", acknowledged, err)
	}
	// ack UPDATE 失败臂（processing + token 匹配）。
	if _, err := db.Exec(`UPDATE account_circuit_outbox SET status = 'processing' WHERE event_id = 'w13g5-evt-key'`); err != nil {
		t.Fatal(err)
	}
	spec.armOnce("SET status = 'dispatched'")
	if _, err := repo.Ack(ctx, w13g5AckEvent("w13g5-evt-key", "w13g5-token"), 0); err == nil {
		t.Fatal("ack UPDATE 失败必须传播")
	}
	spec.disarm()
	// dispatch_revision_changed → accounts 水位回写成功路径。
	acknowledged, err = repo.Ack(ctx, w13g5AckEvent("w13g5-evt-key", "w13g5-token"), 0)
	if err != nil || !acknowledged {
		t.Fatalf("合法 ack 必须成功: %v %v", acknowledged, err)
	}
}

func TestW13g5ControlAckIncidentBranchArms(t *testing.T) {
	ctx := context.Background()
	// incident 水位回写失败臂（独立 fixture：失败注入后 sql.Tx 会标记 done，
	// 驱动级事务只能随连接销毁释放，故每个注入阶段各自建库）。
	t.Run("incident 回写失败", func(t *testing.T) {
		_, repo, db, spec := w13g5CircuitFixture(t)
		w13g5SeedAccount(t, db, "w13g5-acc", "w13g5-viewer")
		seedIncident(t, db, "w13g5-scope", "w13g5-acc", "w13g5-acc", "OPEN", 1, 9, 5, 100)
		w13g5SeedOutbox(t, db, "w13g5-evt-inc", "incident_changed", "processing", "w13g5-token", 9, 5)
		spec.armOnce("UPDATE account_circuit_incidents")
		if _, err := repo.Ack(ctx, w13g5AckEvent("w13g5-evt-inc", "w13g5-token"), 0); err == nil {
			t.Fatal("incident 回写失败必须传播")
		}
	})
	t.Run("提交失败", func(t *testing.T) {
		_, repo, db, spec := w13g5CircuitFixture(t)
		w13g5SeedAccount(t, db, "w13g5-acc", "w13g5-viewer")
		seedIncident(t, db, "w13g5-scope", "w13g5-acc", "w13g5-acc", "OPEN", 1, 9, 5, 100)
		w13g5SeedOutbox(t, db, "w13g5-evt-inc", "incident_changed", "processing", "w13g5-token", 9, 5)
		spec.armOnce("w13g5-COMMIT")
		if _, err := repo.Ack(ctx, w13g5AckEvent("w13g5-evt-inc", "w13g5-token"), 0); err == nil {
			t.Fatal("提交失败必须传播")
		}
	})
	t.Run("成功路径", func(t *testing.T) {
		_, repo, db, _ := w13g5CircuitFixture(t)
		w13g5SeedAccount(t, db, "w13g5-acc", "w13g5-viewer")
		seedIncident(t, db, "w13g5-scope", "w13g5-acc", "w13g5-acc", "OPEN", 1, 9, 5, 100)
		// 对齐 outbox 行引用的 incident_id。
		if _, err := db.Exec(`UPDATE account_circuit_incidents SET incident_id = 'w13g5-incident' WHERE circuit_scope_key = 'w13g5-scope'`); err != nil {
			t.Fatal(err)
		}
		w13g5SeedOutbox(t, db, "w13g5-evt-inc", "incident_changed", "processing", "w13g5-token", 9, 5)
		acknowledged, err := repo.Ack(ctx, w13g5AckEvent("w13g5-evt-inc", "w13g5-token"), 0)
		if err != nil || !acknowledged {
			t.Fatalf("incident ack 必须成功: %v %v", acknowledged, err)
		}
		var projected int64
		if err := db.QueryRow(`SELECT projected_ledger_revision FROM account_circuit_incidents WHERE circuit_scope_key = 'w13g5-scope'`).Scan(&projected); err != nil {
			t.Fatal(err)
		}
		if projected != 5 {
			t.Fatalf("incident 投影水位必须推进: %d", projected)
		}
	})
}

func TestW13g5ControlAckAccountsUpdateErrorArm(t *testing.T) {
	_, repo, db, spec := w13g5CircuitFixture(t)
	ctx := context.Background()
	w13g5SeedAccount(t, db, "w13g5-acc", "w13g5-viewer")
	w13g5SeedOutbox(t, db, "w13g5-evt-acc", "dispatch_revision_changed", "processing", "w13g5-token", 9, 0)
	spec.armOnce("UPDATE accounts")
	if _, err := repo.Ack(ctx, w13g5AckEvent("w13g5-evt-acc", "w13g5-token"), 0); err == nil {
		t.Fatal("accounts 回写失败必须传播")
	}
}

func TestW13g5ControlReleaseArms(t *testing.T) {
	_, repo, db, spec := w13g5CircuitFixture(t)
	ctx := context.Background()
	event := opsjobs.OutboxEvent{EventID: "w13g5-evt-r", ClaimToken: "w13g5-token"}
	// 输入校验臂。
	if err := repo.ReleaseForReplay(ctx, opsjobs.OutboxEvent{ClaimToken: "t"}, "err", 0, 0); err == nil {
		t.Fatal("空 eventId 必须报错")
	}
	if err := repo.ReleaseForReplay(ctx, opsjobs.OutboxEvent{EventID: "w13g5-evt-r"}, "err", 0, 0); err == nil {
		t.Fatal("空 claimToken 必须报错")
	}
	if err := repo.ReleaseForReplay(ctx, event, "", 0, 0); err == nil {
		t.Fatal("空 errorClass 必须报错")
	}
	if err := repo.ReleaseForReplay(ctx, event, "err", 0, -1); err == nil {
		t.Fatal("负 retryDelay 必须报错")
	}
	// 未命中 claim → 明确错误。
	w13g5SeedAccount(t, db, "w13g5-acc", "w13g5-viewer")
	if err := repo.ReleaseForReplay(ctx, event, "err", 0, 0); err == nil {
		t.Fatal("未命中 claim 必须报错")
	}
	// UPDATE 失败臂。
	w13g5SeedOutbox(t, db, "w13g5-evt-r", "dispatch_revision_changed", "processing", "w13g5-token", 9, 0)
	spec.armOnce("SET status = 'pending'")
	if err := repo.ReleaseForReplay(ctx, event, "err", 0, 0); err == nil {
		t.Fatal("release UPDATE 失败必须传播")
	}
	spec.disarm()
	// 成功路径。
	if err := repo.ReleaseForReplay(ctx, event, "err", 1000, 500); err != nil {
		t.Fatal(err)
	}
}

func TestW13g5ReconcileCursorArms(t *testing.T) {
	_, repo, _, spec := w13g5CircuitFixture(t)
	ctx := context.Background()
	if err := repo.EnsureCursorSchema(ctx); err != nil {
		t.Fatal(err)
	}
	store, err := NewReconcileCursorStore(ControlPlaneConfig{DB: repo.db, Postgres: false})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, opsjobs.IncidentCursor{UpdatedAtMS: 42, CircuitScopeKey: "w13g5-scope"}); err != nil {
		t.Fatal(err)
	}
	cursor, err := store.Load(ctx)
	if err != nil || cursor == nil || cursor.UpdatedAtMS != 42 {
		t.Fatalf("游标回读失败: %+v %v", cursor, err)
	}
	// Save 失败臂。
	spec.armOnce("INSERT INTO account_circuit_reconcile_cursors")
	if err := store.Save(ctx, opsjobs.IncidentCursor{UpdatedAtMS: 43, CircuitScopeKey: "w13g5-scope"}); err == nil {
		t.Fatal("Save 失败必须传播")
	}
	spec.disarm()
	// EnsureSchema 失败臂。
	spec.armOnce("CREATE TABLE IF NOT EXISTS account_circuit_reconcile_cursors")
	if err := repo.EnsureCursorSchema(ctx); err == nil {
		t.Fatal("EnsureSchema 失败必须传播")
	}
	spec.disarm()
	// Load 查询失败臂。
	spec.armOnce("SELECT updated_at_ms, circuit_scope_key")
	if _, err := store.Load(ctx); err == nil {
		t.Fatal("Load 失败必须传播")
	}
}

func TestW13g5LedgerValidationAndScanArms(t *testing.T) {
	_, repo, db, _ := w13g5CircuitFixture(t)
	ctx := context.Background()
	w13g5SeedAccount(t, db, "w13g5-acc", "w13g5-viewer")
	// ListForRebuild 校验臂。
	if _, err := repo.ListForRebuild(ctx, opsjobs.RebuildPageQuery{Limit: 0}); err == nil {
		t.Fatal("limit<1 必须报错")
	}
	longScope := strings.Repeat("s", 2049)
	if _, err := repo.ListForRebuild(ctx, opsjobs.RebuildPageQuery{Limit: 1, AfterCircuitScopeKey: &longScope}); err == nil {
		t.Fatal("超长 afterScopeKey 必须报错")
	}
	// ListByRuntimeKeys 校验臂。
	if _, err := repo.ListByRuntimeKeys(ctx, []string{""}, false, 0); err == nil {
		t.Fatal("空运行态键必须报错")
	}
	if _, err := repo.ListByRuntimeKeys(ctx, []string{strings.Repeat("k", 1025)}, false, 0); err == nil {
		t.Fatal("超长运行态键必须报错")
	}
	if _, err := repo.ListByRuntimeKeys(ctx, nil, false, 0); err != nil || len(nil2Empty(err)) != 0 {
		t.Fatal("空键列表必须返回空切片")
	}
	many := make([]string, 101)
	for i := range many {
		many[i] = "w13g5-key-" + string(rune('a'+i%26)) + itoa2(i)
	}
	if _, err := repo.ListByRuntimeKeys(ctx, many, false, 0); err == nil {
		t.Fatal("超过 100 个键必须报错")
	}
	// GetByScopeKey 校验臂。
	if _, err := repo.GetByScopeKey(ctx, ""); err == nil {
		t.Fatal("空 scopeKey 必须报错")
	}
	// 数据驱动扫描错误：坏 childIncidentIds JSON。
	if _, err := db.Exec(`INSERT INTO account_circuit_incidents (
		circuit_scope_key, account_id, account_runtime_key, scope_kind,
		incident_id, child_incident_ids_json, state,
		generation, dispatch_revision, ledger_revision, transition_id,
		created_at_ms, updated_at_ms
	) VALUES ('w13g5-bad-json', 'w13g5-acc', 'w13g5-acc', 'account', 'i-bad', 'not-json', 'OPEN', 1, 1, 1, 'tr', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ListForRebuild(ctx, opsjobs.RebuildPageQuery{Limit: 10, NowMS: 1000}); err == nil {
		t.Fatal("坏 JSON 扫描必须报错")
	}
	if _, err := repo.ListByRuntimeKeys(ctx, []string{"w13g5-acc"}, false, 0); err == nil {
		t.Fatal("坏 JSON 扫描必须传播")
	}
	if _, err := repo.GetByScopeKey(ctx, "w13g5-bad-json"); err == nil {
		t.Fatal("坏 JSON 扫描必须传播")
	}
}

func nil2Empty(_ error) []opsjobs.CircuitIncidentRecord { return []opsjobs.CircuitIncidentRecord{} }

func itoa2(v int) string {
	if v == 0 {
		return "0"
	}
	digits := []byte{}
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	return string(digits)
}

func TestW13g5IncidentRowParseArms(t *testing.T) {
	if _, err := mapIncidentRow(incidentRow{childIncidentIDsJSON: "not-json"}); err == nil {
		t.Fatal("坏 child JSON 必须报错")
	}
	if _, err := mapIncidentRow(incidentRow{childIncidentIDsJSON: `["ok"]`, confirmationFailureEvidenceKeysJSON: "not-json"}); err == nil {
		t.Fatal("坏 evidence JSON 必须报错")
	}
	// parseBoundedIDArray 边界。
	if _, err := parseBoundedIDArray(`[` + strings.Repeat(`"x",`, 64) + `"y"]`); err == nil {
		t.Fatal("超过 64 项必须报错")
	}
	if _, err := parseBoundedIDArray(`["", "x"]`); err == nil {
		t.Fatal("空 ID 必须报错")
	}
	if _, err := parseBoundedIDArray(`["` + strings.Repeat("x", 257) + `"]`); err == nil {
		t.Fatal("超长 ID 必须报错")
	}
	dup, err := parseBoundedIDArray(`["a", "a", " b "]`)
	if err != nil || len(dup) != 2 {
		t.Fatalf("去重与 trim 后应为 2 项: %v %v", dup, err)
	}
	// parseEvidenceKeys 边界。
	if _, err := parseEvidenceKeys(`["not-sha"]`, 1); err == nil {
		t.Fatal("非 SHA256 evidence 必须报错")
	}
	valid := `["` + strings.Repeat("a", 64) + `", "` + strings.Repeat("b", 64) + `", "` + strings.Repeat("c", 64) + `"]`
	if _, err := parseEvidenceKeys(valid, 1); err == nil {
		t.Fatal("超过 required+1 项必须报错")
	}
	dupKeys, err := parseEvidenceKeys(`["`+strings.Repeat("a", 64)+`", " `+strings.Repeat("A", 63)+`a"]`, 1)
	if err != nil || len(dupKeys) != 1 {
		t.Fatalf("大小写归一后去重应为 1 项: %v %v", dupKeys, err)
	}
}
