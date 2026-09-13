package circuitprojector

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	control "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/circuit_control_plane"
	runtime "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/circuit_runtime"

	"github.com/alicebob/miniredis/v2"
	_ "modernc.org/sqlite"
)

// wkControlDB 构造与控制面契约一致的内存 SQLite：accounts/incidents/outbox 三张
// 关系加 key_model capability 唯一索引，保证 CompareAndSetIncident/Advance 等写路径可用。
func wkControlDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:circuit-projector-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ddls := []string{
		`CREATE TABLE accounts (id TEXT PRIMARY KEY, dispatch_revision INTEGER NOT NULL DEFAULT 1, circuit_projection_revision INTEGER NOT NULL DEFAULT 0, deleted_at TEXT)`,
		`CREATE TABLE account_circuit_incidents (
 circuit_scope_key TEXT PRIMARY KEY, account_id TEXT NOT NULL, account_runtime_key TEXT NOT NULL, scope_kind TEXT NOT NULL,
 key_fingerprint TEXT, protocol_code TEXT, request_lane TEXT, model_family TEXT, client_model TEXT, capability_hash TEXT,
 credential_source_account_id TEXT, client_endpoint_family TEXT, final_upstream_model TEXT, upstream_endpoint_mode TEXT, incident_id TEXT NOT NULL,
 parent_incident_id TEXT, child_incident_ids_json TEXT NOT NULL, caused_by_terminal_outcome_id TEXT, state TEXT NOT NULL,
 failure_scope TEXT, generation INTEGER NOT NULL, dispatch_revision INTEGER NOT NULL, ledger_revision INTEGER NOT NULL,
 projected_ledger_revision INTEGER NOT NULL, transition_id TEXT NOT NULL, cooldown_observation_generation INTEGER NOT NULL,
 open_until_ms INTEGER, next_transition_at_ms INTEGER, lease_id TEXT, lease_purpose TEXT, lease_owner_run_id TEXT,
 lease_until_ms INTEGER, attempt_started_at_ms INTEGER, attempt_hard_deadline_ms INTEGER, upstream_attempt_observed INTEGER NOT NULL,
 backoff_level INTEGER NOT NULL, consecutive_failures INTEGER NOT NULL, confirmation_failures_required INTEGER NOT NULL,
 confirmation_failure_evidence_keys_json TEXT NOT NULL, recovering_successes INTEGER NOT NULL, last_failure_class TEXT,
 retained_until_ms INTEGER, created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL)`,
		`CREATE TABLE account_circuit_outbox (
 event_id TEXT PRIMARY KEY, projection_key TEXT NOT NULL, dedupe_key TEXT NOT NULL UNIQUE, event_type TEXT NOT NULL,
 account_id TEXT NOT NULL, account_runtime_key TEXT NOT NULL, circuit_scope_key TEXT, incident_id TEXT, transition_id TEXT NOT NULL,
 dispatch_revision INTEGER NOT NULL, generation INTEGER, ledger_revision INTEGER, status TEXT NOT NULL, available_at_ms INTEGER NOT NULL,
 claim_token TEXT, claimed_by TEXT, claim_until_ms INTEGER, attempt_count INTEGER NOT NULL, last_error_class TEXT,
 acknowledged_at_ms INTEGER, created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL)`,
	}
	for _, ddl := range ddls {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX idx_account_circuit_incidents_key_model_capability ON account_circuit_incidents(scope_kind, capability_hash) WHERE scope_kind = 'key_model' AND capability_hash IS NOT NULL`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts(id,dispatch_revision,circuit_projection_revision) VALUES ('a1',1,0)`); err != nil {
		t.Fatal(err)
	}
	return db
}

// wkProjector 组装控制面（SQLite）+ 运行态（miniredis）双存储的投影服务，
// 并通过 BackfillRuntimeIndex 把运行态索引置为 ready，使 CheckReady 放行。
func wkProjector(t *testing.T) (*Service, *control.Store, *sql.DB, *miniredis.Miniredis) {
	t.Helper()
	db := wkControlDB(t)
	controlStore, err := control.New(db, control.SQLite, "", control.OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	server := miniredis.RunT(t)
	runtimeStore, err := runtime.New(runtime.Config{URL: "redis://" + server.Addr(), Namespace: "wk-proj"}, runtime.OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtimeStore.Close() })
	svc, err := New(controlStore, runtimeStore, "wk-owner")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	result, err := svc.BackfillRuntimeIndex(ctx, runtime.GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: "wk-owner"})
	if err != nil {
		t.Fatalf("backfill runtime index: %v", err)
	}
	if result.Epoch == "" {
		t.Fatal("backfill result epoch is empty")
	}
	if err := runtimeStore.CheckReady(ctx); err != nil {
		t.Fatalf("runtime index is not ready after backfill: %v", err)
	}
	return svc, controlStore, db, server
}

func wkInsertOutbox(t *testing.T, db *sql.DB, eventID, eventType, scope, incidentID string, dispatchRevision int64) {
	t.Helper()
	// 手工注入一个绕过控制面写路径的 outbox 事件，用于构造投影失败分支。
	var scopeArg, incidentArg any
	if scope != "" {
		scopeArg = scope
	}
	if incidentID != "" {
		incidentArg = incidentID
	}
	_, err := db.Exec(`INSERT INTO account_circuit_outbox (event_id,projection_key,dedupe_key,event_type,account_id,account_runtime_key,circuit_scope_key,incident_id,transition_id,dispatch_revision,generation,ledger_revision,status,available_at_ms,attempt_count,created_at_ms,updated_at_ms) VALUES (?, 'account_circuit_runtime_v1', ?, ?, 'a1', 'a1', ?, ?, 'tr-'+?, ?, 1, 1, 'pending', 100, 0, 100, 100)`,
		eventID, "dedupe-"+eventID, eventType, scopeArg, incidentArg, eventID, dispatchRevision)
	if err != nil {
		t.Fatal(err)
	}
}

func TestNewRequiresStoresAndOwner(t *testing.T) {
	server := miniredis.RunT(t)
	runtimeStore, err := runtime.New(runtime.Config{URL: "redis://" + server.Addr(), Namespace: "wk-new"}, runtime.OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtimeStore.Close() })
	db := wkControlDB(t)
	controlStore, err := control.New(db, control.SQLite, "", control.OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(nil, runtimeStore, "owner"); err == nil {
		t.Fatal("nil 控制面存储必须被拒绝")
	}
	if _, err := New(controlStore, nil, "owner"); err == nil {
		t.Fatal("nil 运行态存储必须被拒绝")
	}
	if _, err := New(controlStore, runtimeStore, "  "); err == nil {
		t.Fatal("空 owner id 必须被拒绝")
	}
	svc, err := New(controlStore, runtimeStore, " owner ")
	if err != nil {
		t.Fatalf("合法构造失败: %v", err)
	}
	if svc == nil || svc.ownerID != "owner" {
		t.Fatalf("owner id 未做 trim: %+v", svc)
	}
}

func TestNilServiceAndUninitializedServiceFailClosed(t *testing.T) {
	var nilService *Service
	ctx := context.Background()
	if _, err := nilService.RunOnce(ctx, time.Time{}, 1); err == nil {
		t.Fatal("nil 服务 RunOnce 必须失败")
	}
	if _, err := nilService.BackfillRuntimeIndex(ctx, runtime.GatewayAccountCircuitRuntimeIndexBackfillInput{}); err == nil {
		t.Fatal("nil 服务 backfill 必须失败")
	}
	if _, err := (&Service{}).RunOnce(ctx, time.Time{}, 1); err == nil {
		t.Fatal("未初始化服务 RunOnce 必须失败")
	}
	if _, err := (&Service{}).BackfillRuntimeIndex(ctx, runtime.GatewayAccountCircuitRuntimeIndexBackfillInput{}); err == nil {
		t.Fatal("未初始化服务 backfill 必须失败")
	}
}

func TestRunOnceFailsWhenRuntimeNotReady(t *testing.T) {
	db := wkControlDB(t)
	controlStore, err := control.New(db, control.SQLite, "", control.OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	server := miniredis.RunT(t)
	// gate 不满足（未确认节点写入停止）→ CheckReady 必须失败。
	runtimeStore, err := runtime.New(runtime.Config{URL: "redis://" + server.Addr(), Namespace: "wk-notready"}, runtime.OwnerGate{Confirmed: true, SchemaReady: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtimeStore.Close() })
	svc, err := New(controlStore, runtimeStore, "wk-owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RunOnce(context.Background(), time.UnixMilli(100), 10); err == nil {
		t.Fatal("运行态 gate 未就绪时 RunOnce 必须失败")
	}
}

func TestDispatchRevisionReaderAdaptsControlPage(t *testing.T) {
	_, controlStore, _, _ := wkProjector(t)
	ctx := context.Background()
	if _, err := controlStore.AdvanceDispatchRevision(ctx, control.DispatchRevision{AccountID: "a1", AccountRuntimeKey: "a1", TransitionID: "t-reader", NowMS: 100}); err != nil {
		t.Fatal(err)
	}
	reader := DispatchRevisionReader{Store: controlStore}
	page, err := reader.ListGatewayAccountCircuitDispatchRevisions(ctx, runtime.GatewayAccountCircuitDispatchRevisionPageInput{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].AccountID != "a1" || page.Items[0].DispatchRevision != 2 {
		t.Fatalf("reader 页内容不符: %+v", page)
	}
	if page.NextAfterAccountID != "" {
		t.Fatalf("未满页不应返回续游标: %+v", page)
	}
	if _, err := reader.ListGatewayAccountCircuitDispatchRevisions(ctx, runtime.GatewayAccountCircuitDispatchRevisionPageInput{Limit: 1, AfterAccountID: "a1"}); err != nil {
		t.Fatalf("续页读取失败: %v", err)
	}
	if _, err := (DispatchRevisionReader{Store: controlStore}).ListGatewayAccountCircuitDispatchRevisions(ctx, runtime.GatewayAccountCircuitDispatchRevisionPageInput{}); err == nil {
		t.Fatal("零值 limit 必须被底层边界校验拒绝")
	}
	if _, err := (DispatchRevisionReader{}).ListGatewayAccountCircuitDispatchRevisions(ctx, runtime.GatewayAccountCircuitDispatchRevisionPageInput{Limit: 1}); err == nil {
		t.Fatal("nil store 的 reader 必须失败")
	}
}

func TestConvertIncidentRejectsMissingIncidentID(t *testing.T) {
	// 数据库层 incident_id 为 NOT NULL，但转换函数自身必须对缺失身份失败关闭。
	if _, err := convertIncident(control.Incident{CircuitScopeKey: "scope", AccountID: "a1", AccountRuntimeKey: "a1", ScopeKind: "account", State: "OPEN"}); err == nil {
		t.Fatal("缺少 incident id 的转换必须失败")
	}
	// timePointer/value 的 nil 分支：全空可选字段时转换仍成功。
	got, err := convertIncident(control.Incident{CircuitScopeKey: "scope", AccountID: "a1", AccountRuntimeKey: "a1", ScopeKind: "account", IncidentID: strptr("inc-1"), State: "OPEN", UpdatedAtMS: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got.OpenUntil != nil || got.LeaseUntil != nil || got.RetainedUntil != nil || got.LeaseID != "" {
		t.Fatalf("nil 可选字段未映射为零值: %+v", got)
	}
	if got.UpdatedAt != time.UnixMilli(10).UTC() {
		t.Fatalf("updated_at 毫秒转换错误: %+v", got)
	}
}

func TestRunOnceProjectsDispatchAndStaleIncident(t *testing.T) {
	svc, controlStore, _, _ := wkProjector(t)
	ctx := context.Background()
	// 先在 revision 1 上产生 incident_changed 事件，再推进 dispatch revision 制造 stale。
	incidentID := "inc-stale"
	if _, err := controlStore.CompareAndSetIncident(ctx, control.IncidentMutation{Incident: control.Incident{
		CircuitScopeKey: "scope-stale", AccountID: "a1", AccountRuntimeKey: "a1", ScopeKind: "account",
		IncidentID: &incidentID, State: "OPEN", Generation: 1, DispatchRevision: 1, TransitionID: "tr-stale",
		ConfirmationFailuresRequired: 1, ChildIncidentIDs: []string{}, ConfirmationFailureEvidenceKeys: []string{},
		CreatedAtMS: 100, UpdatedAtMS: 100,
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := controlStore.AdvanceDispatchRevision(ctx, control.DispatchRevision{AccountID: "a1", AccountRuntimeKey: "a1", TransitionID: "tr-dispatch", NowMS: 150}); err != nil {
		t.Fatal(err)
	}
	result, err := svc.RunOnce(ctx, time.UnixMilli(200), 10)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if result.Claimed != 2 || result.Projected != 2 || result.Acknowledged != 2 || result.Failed != 0 || result.Released != 0 {
		t.Fatalf("投影统计不符: %+v", result)
	}
	// incident 快照仍在控制面（restore 是运行态侧行为），dispatch 记账推进。
	if _, _, err := controlStore.GetIncident(ctx, "scope-stale"); err != nil {
		t.Fatalf("incident 读取失败: %v", err)
	}
}

func TestRunOnceReleasesFailedProjections(t *testing.T) {
	svc, _, db, _ := wkProjector(t)
	ctx := context.Background()
	// 分支一：不支持的 outbox 事件类型 → 投影失败并释放重放。
	wkInsertOutbox(t, db, "evt-bogus", "bogus_event_type", "", "", 1)
	// 分支二：incident_changed 指向不存在的 scope → 快照缺失 → 投影失败。
	wkInsertOutbox(t, db, "evt-missing", runtime.GatewayAccountCircuitIncidentChanged, "scope-unknown", "inc-unknown", 1)
	result, err := svc.RunOnce(ctx, time.UnixMilli(200), 10)
	if err != nil {
		t.Fatalf("释放路径不应返回错误: %v", err)
	}
	if result.Claimed != 2 || result.Projected != 0 || result.Failed != 2 || result.Released != 2 {
		t.Fatalf("失败投影统计不符: %+v", result)
	}
	var lastError sql.NullString
	if err := db.QueryRow(`SELECT last_error_class FROM account_circuit_outbox WHERE event_id='evt-bogus'`).Scan(&lastError); err != nil {
		t.Fatal(err)
	}
	if lastError.String != "projection_failed" {
		t.Fatalf("释放时未记录错误类别: %q", lastError.String)
	}
}

func TestRunOnceStaleRevisionIncidentIsAcknowledged(t *testing.T) {
	// 行为契约：incident 快照 revision 落后于账户当前 revision 时，
	// 投影必须得到 stale 结果并确认事件，而不是失败重放。
	svc, _, db, _ := wkProjector(t)
	ctx := context.Background()
	// 账户当前 revision 已是 2（backfill 读取），手工注入 revision=99 的迟到事件。
	wkInsertOutbox(t, db, "evt-stale-rev", runtime.GatewayAccountCircuitIncidentChanged, "scope-ghost", "inc-ghost", 99)
	result, err := svc.RunOnce(ctx, time.UnixMilli(200), 10)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if result.Claimed != 1 || result.Projected != 1 || result.Acknowledged != 1 || result.Failed != 0 {
		t.Fatalf("stale 事件必须被确认而非释放: %+v", result)
	}
}

func TestRunOnceRequiresZeroTimeDefaultsToUTCNow(t *testing.T) {
	// 零时钟输入必须可用：ClaimOutbox 的 now 取当前 UTC，事件 available_at=100，
	// 只要时钟单调前进就能认领；这里只断言不报错且无事件被错误释放。
	svc, _, _, _ := wkProjector(t)
	result, err := svc.RunOnce(context.Background(), time.Time{}, 10)
	if err != nil {
		t.Fatalf("零时间 RunOnce: %v", err)
	}
	if result.Claimed != 0 || result.Failed != 0 {
		t.Fatalf("空 outbox 统计应为零: %+v", result)
	}
}

func TestRunOnceProjectsCurrentIncident(t *testing.T) {
	// 行为契约：incident 快照与账户当前 revision 一致时，投影必须走
	// LoadIncidentForProjection(current) → RestoreIncident → 确认的完整链路。
	svc, controlStore, _, _ := wkProjector(t)
	ctx := context.Background()
	incidentID := "inc-current"
	if _, err := controlStore.CompareAndSetIncident(ctx, control.IncidentMutation{Incident: control.Incident{
		CircuitScopeKey: wkAccountScope("a1"), AccountID: "a1", AccountRuntimeKey: "a1", ScopeKind: "account",
		IncidentID: &incidentID, State: "OPEN", Generation: 1, DispatchRevision: 1, TransitionID: "tr-current",
		ConfirmationFailuresRequired: 1, ChildIncidentIDs: []string{}, ConfirmationFailureEvidenceKeys: []string{},
		CreatedAtMS: 100, UpdatedAtMS: 100,
	}}); err != nil {
		t.Fatal(err)
	}
	result, err := svc.RunOnce(ctx, time.UnixMilli(200), 10)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if result.Claimed != 1 || result.Projected != 1 || result.Acknowledged != 1 || result.Failed != 0 || result.Released != 0 {
		t.Fatalf("current incident 投影统计不符: %+v", result)
	}
}

func TestProjectDirectSemantics(t *testing.T) {
	// 直接驱动 project()：分别覆盖 incident 身份非法、重复恢复幂等两条语义。
	svc, controlStore, _, _ := wkProjector(t)
	ctx := context.Background()
	incidentID := "inc-direct"
	scope := wkAccountScope("a1")
	if _, err := controlStore.CompareAndSetIncident(ctx, control.IncidentMutation{Incident: control.Incident{
		CircuitScopeKey: scope, AccountID: "a1", AccountRuntimeKey: "a1", ScopeKind: "account",
		IncidentID: &incidentID, State: "OPEN", Generation: 1, DispatchRevision: 1, TransitionID: "tr-direct",
		ConfirmationFailuresRequired: 1, ChildIncidentIDs: []string{}, ConfirmationFailureEvidenceKeys: []string{},
		CreatedAtMS: 100, UpdatedAtMS: 100,
	}}); err != nil {
		t.Fatal(err)
	}
	// 身份缺失的事件必须被拒绝。
	if _, err := svc.project(ctx, control.Outbox{EventID: "e-bad", EventType: runtime.GatewayAccountCircuitIncidentChanged, AccountID: "", DispatchRevision: 1}); err == nil {
		t.Fatal("缺 account id 的 incident 事件必须投影失败")
	}
	event := control.Outbox{EventID: "e-1", ProjectionKey: runtime.GatewayAccountCircuitProjectionKey, EventType: runtime.GatewayAccountCircuitIncidentChanged, AccountID: "a1", AccountRuntimeKey: "a1", CircuitScopeKey: &scope, IncidentID: &incidentID, TransitionID: "tr-direct", DispatchRevision: 1, Generation: int64ptr(1), LedgerRevision: int64ptr(1)}
	first, err := svc.project(ctx, event)
	if err != nil {
		t.Fatalf("首次 incident 投影: %v", err)
	}
	if first.Status == runtime.GatewayAccountCircuitRevisionStale {
		t.Fatalf("当前 revision 的 incident 不应被判 stale: %+v", first)
	}
	second, err := svc.project(ctx, event)
	if err != nil {
		t.Fatalf("重复 incident 投影应幂等: %v", err)
	}
	_ = second
}

func int64ptr(v int64) *int64 { return &v }

// wkAccountScope 返回 account 作用域的规范 scope key，与运行态 restore 的
// ScopeKey 校验一致（生产中该列由 GatewayAccountCircuitScopeKey 生成）。
func wkAccountScope(accountID string) string {
	kind := "account"
	return fmt.Sprintf("%d:%s|%d:%s", len(kind), kind, len(accountID), accountID)
}

func TestRunOnceReleasesWhenAcknowledgeMisses(t *testing.T) {
	// 契约：ack 未命中（投影键不匹配）必须计为 Failed 且不报错。
	// incident_changed 投影不校验投影键，篡改后投影仍成功、ack 落空。
	svc, controlStore, db, _ := wkProjector(t)
	ctx := context.Background()
	incidentID := "inc-ack-miss"
	if _, err := controlStore.CompareAndSetIncident(ctx, control.IncidentMutation{Incident: control.Incident{
		CircuitScopeKey: wkAccountScope("a1"), AccountID: "a1", AccountRuntimeKey: "a1", ScopeKind: "account",
		IncidentID: &incidentID, State: "OPEN", Generation: 1, DispatchRevision: 1, TransitionID: "tr-ack-miss",
		ConfirmationFailuresRequired: 1, ChildIncidentIDs: []string{}, ConfirmationFailureEvidenceKeys: []string{},
		CreatedAtMS: 100, UpdatedAtMS: 100,
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE account_circuit_outbox SET projection_key='tampered' WHERE event_type='incident_changed'`); err != nil {
		t.Fatal(err)
	}
	result, err := svc.RunOnce(ctx, time.UnixMilli(200), 10)
	if err != nil {
		t.Fatalf("ack 未命中必须被吞掉: %v", err)
	}
	if result.Projected != 1 || result.Acknowledged != 0 || result.Failed != 1 {
		t.Fatalf("ack 未命中统计不符: %+v", result)
	}
}

func TestRunOnceFailsWhenClaimErrors(t *testing.T) {
	// CheckReady 只探测 Redis；控制面连接断开时 claim 必须把错误向上抛。
	svc, _, db, _ := wkProjector(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RunOnce(context.Background(), time.UnixMilli(200), 10); err == nil {
		t.Fatal("claim 失败必须返回错误")
	}
}

func TestRunOnceFailsWhenAcknowledgeErrors(t *testing.T) {
	// AcknowledgeOutbox 在投影键不匹配时返回 (false,nil)：RunOnce 必须记为 Failed
	// 且继续不报错——这是幂等护栏语义而不是故障。
	svc, controlStore, db, _ := wkProjector(t)
	ctx := context.Background()
	if _, err := controlStore.AdvanceDispatchRevision(ctx, control.DispatchRevision{AccountID: "a1", AccountRuntimeKey: "a1", TransitionID: "tr-ack", NowMS: 100}); err != nil {
		t.Fatal(err)
	}
	// 预先破坏投影键，dispatch 事件在投影层即失败并释放重放。
	if _, err := db.Exec(`UPDATE account_circuit_outbox SET projection_key='tampered'`); err != nil {
		t.Fatal(err)
	}
	result, err := svc.RunOnce(ctx, time.UnixMilli(200), 10)
	if err != nil {
		t.Fatalf("投影失败必须走释放路径: %v", err)
	}
	if result.Failed != 1 || result.Released != 1 || result.Projected != 0 {
		t.Fatalf("释放统计不符: %+v", result)
	}
}

func TestBackfillRuntimeIndexFailsWhenGateNotReady(t *testing.T) {
	db := wkControlDB(t)
	controlStore, err := control.New(db, control.SQLite, "", control.OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	server := miniredis.RunT(t)
	blocked, err := runtime.New(runtime.Config{URL: "redis://" + server.Addr(), Namespace: "wk-blocked"}, runtime.OwnerGate{Confirmed: true, SchemaReady: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = blocked.Close() })
	blockedSvc, err := New(controlStore, blocked, "wk-owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blockedSvc.BackfillRuntimeIndex(context.Background(), runtime.GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: "wk-owner"}); err == nil {
		t.Fatal("gate 未就绪的 backfill 必须失败")
	}
}
