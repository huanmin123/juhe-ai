package main

// 缺陷 E（熔断观测断链）装配测试：主链 OnMutation → control-plane 持久化
// 管道的字段映射、落行、幂等与热路径容错。

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	circuitcontrolplane "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/circuit_control_plane"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	_ "modernc.org/sqlite"
)

// w17eOpenBusinessSQLite 打开临时业务库并按 circuit control-plane 契约建表
// （与 circuitcontrolplane 包内测试同款 DDL），并预置账户行 acc(dispatch=1)。
func w17eOpenBusinessSQLite(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "business-circuit.sqlite3"))
	if err != nil {
		t.Fatalf("open business sqlite: %v", err)
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
			t.Fatalf("create circuit contract table: %v", err)
		}
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX idx_account_circuit_incidents_key_model_capability ON account_circuit_incidents(scope_kind, capability_hash) WHERE scope_kind = 'key_model' AND capability_hash IS NOT NULL`); err != nil {
		t.Fatalf("create key-model capability index: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO accounts(id,dispatch_revision) VALUES ('acc',1)`); err != nil {
		t.Fatalf("seed account row: %v", err)
	}
	return db
}

func w17eGateReadyConfig(db *sql.DB) chainAccountCircuitPersistConfig {
	return chainAccountCircuitPersistConfig{DB: db, Confirmed: true, SchemaReady: true, NodeWriterStopped: true}
}

func w17eWaitFor(t *testing.T, description string, probe func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if probe() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

// TestChainCircuitControlPlaneFieldMappings 校验三组纯映射（含 nil → 零值
// 与 FailureScope 空 → nil 的往返语义）。
func TestChainCircuitControlPlaneFieldMappings(t *testing.T) {
	scope := gatewaycircuit.Scope{
		Kind:              gatewaycircuit.ScopeKindProtocolModel,
		AccountRuntimeKey: "acc",
		ProtocolProfile:   "profile",
		RequestLane:       gatewaycircuit.LaneText,
		ModelBucket:       "unknown",
	}
	scopeKey := gatewaycircuit.MustScopeKey(scope)
	evidence := strings.Repeat("a", 64)
	one := int64(1)
	two := int64(2)
	three := int64(3)
	observed := true
	updatedAt := int64(1_000)
	input := gatewaycircuit.CompareAndSetIncidentInput{
		AccountID:                       "acc",
		AccountRuntimeKey:               "acc",
		CircuitScopeKey:                 scopeKey,
		ScopeKind:                       gatewaycircuit.ScopeKindProtocolModel,
		ProtocolCode:                    strPtr("profile"),
		RequestLane:                     strPtr("text"),
		ModelFamily:                     strPtr("unknown"),
		IncidentID:                      "transition-1",
		ParentIncidentID:                strPtr("parent-1"),
		ChildIncidentIDs:                []string{"child-1"},
		CausedByTerminalOutcomeID:       strPtr("outcome-1"),
		State:                           "SUSPECT",
		FailureScope:                    strPtr("protocol_model"),
		Generation:                      3,
		DispatchRevision:                1,
		ExpectedLedgerRevision:          &two,
		TransitionID:                    "transition-1",
		CooldownObservationGeneration:   &one,
		OpenUntilMs:                     &three,
		NextTransitionAtMs:              &three,
		LeaseID:                         strPtr("lease-1"),
		LeasePurpose:                    strPtr("confirmation"),
		LeaseOwnerRunID:                 strPtr("owner-1"),
		LeaseUntilMs:                    &three,
		UpstreamAttemptObserved:         &observed,
		BackoffLevel:                    &one,
		ConsecutiveFailures:             &one,
		ConfirmationFailuresRequired:    &two,
		ConfirmationFailureEvidenceKeys: []string{evidence},
		RecoveringSuccesses:             &one,
		LastFailureClass:                strPtr("connect_failed"),
		RetainedUntilMs:                 &three,
		StateUpdatedAtMs:                &updatedAt,
	}
	mutation := chainCircuitIncidentMutation(input)
	if mutation.ExpectedLedgerRevision == nil || *mutation.ExpectedLedgerRevision != 2 {
		t.Fatalf("ExpectedLedgerRevision = %v, want 2", mutation.ExpectedLedgerRevision)
	}
	incident := mutation.Incident
	if incident.CircuitScopeKey != scopeKey ||
		incident.IncidentID == nil || *incident.IncidentID != "transition-1" ||
		incident.ParentIncidentID == nil || *incident.ParentIncidentID != "parent-1" ||
		incident.FailureScope != "protocol_model" ||
		incident.CooldownObservationGeneration != 1 ||
		!incident.UpstreamAttemptObserved ||
		incident.ConfirmationFailuresRequired != 2 ||
		len(incident.ConfirmationFailureEvidenceKeys) != 1 || incident.ConfirmationFailureEvidenceKeys[0] != evidence ||
		incident.UpdatedAtMS != 1_000 {
		t.Fatalf("incident mapping mismatch: %+v", incident)
	}
	if incident.CreatedAtMS != 0 {
		t.Fatalf("CreatedAtMS = %d, want 0（gatewaycircuit 输入无 createdAt，落 store 侧契约）", incident.CreatedAtMS)
	}

	// Incident → IncidentRecord 往返：FailureScope 空 → nil。
	incident.CreatedAtMS = 900
	record := chainCircuitIncidentRecord(incident)
	if record.IncidentID != "transition-1" ||
		record.FailureScope == nil || *record.FailureScope != "protocol_model" ||
		record.CreatedAtMs != 900 || record.UpdatedAtMs != 1_000 ||
		len(record.ChildIncidentIDs) != 1 {
		t.Fatalf("record mapping mismatch: %+v", record)
	}
	incident.FailureScope = ""
	if record2 := chainCircuitIncidentRecord(incident); record2.FailureScope != nil {
		t.Fatalf("empty FailureScope must map to nil, got %q", *record2.FailureScope)
	}

	// Outbox 事件映射。
	scopePtr := strPtr(scopeKey)
	incidentIDPtr := strPtr("transition-1")
	generation := int64(3)
	ledger := int64(4)
	claim := strPtr("claim-1")
	outbox := chainCircuitOutboxEvent(circuitcontrolplane.Outbox{
		EventID:           "event-1",
		ProjectionKey:     circuitcontrolplane.ProjectionKey,
		EventType:         "incident_changed",
		AccountID:         "acc",
		AccountRuntimeKey: "acc",
		CircuitScopeKey:   scopePtr,
		IncidentID:        incidentIDPtr,
		TransitionID:      "transition-1",
		DispatchRevision:  1,
		Generation:        &generation,
		LedgerRevision:    &ledger,
		ClaimToken:        claim,
	})
	if outbox.EventID != "event-1" || outbox.EventType != "incident_changed" ||
		outbox.CircuitScopeKey == nil || *outbox.CircuitScopeKey != scopeKey ||
		outbox.IncidentID == nil || *outbox.IncidentID != "transition-1" ||
		outbox.Generation == nil || *outbox.Generation != 3 ||
		outbox.LedgerRevision == nil || *outbox.LedgerRevision != 4 ||
		outbox.ClaimToken == nil || *outbox.ClaimToken != "claim-1" {
		t.Fatalf("outbox mapping mismatch: %+v", outbox)
	}
}

// TestChainAccountCircuitPersistHookArms：gate 未就绪/业务库契约缺失时不
// fail-fast，保持既有无持久观测行为。
func TestChainAccountCircuitPersistHookArms(t *testing.T) {
	store, err := gatewaycircuit.NewMemoryStore(gatewaycircuit.MemoryStoreOptions{Capacity: 8})
	if err != nil {
		t.Fatalf("create memory store: %v", err)
	}
	// 零配置（无 DB）与 gate 未集齐。
	for name, config := range map[string]chainAccountCircuitPersistConfig{
		"noDB":           {},
		"gateIncomplete": {Confirmed: true, SchemaReady: true, NodeWriterStopped: false},
	} {
		hook, closeHook, hookErr := newChainAccountCircuitPersistHook(store, config)
		if hookErr != nil || hook != nil || closeHook != nil {
			t.Fatalf("%s: hook nil=%v close nil=%v err=%v, want all disabled", name, hook == nil, closeHook == nil, hookErr)
		}
	}
	// DB 就绪但库内无 circuit 契约表：只停用并告警，不报错。
	emptyDB, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "empty.sqlite3"))
	if err != nil {
		t.Fatalf("open empty sqlite: %v", err)
	}
	t.Cleanup(func() { _ = emptyDB.Close() })
	hook, closeHook, hookErr := newChainAccountCircuitPersistHook(store, w17eGateReadyConfig(emptyDB))
	if hookErr != nil || hook != nil || closeHook != nil {
		t.Fatalf("contract missing: hook nil=%v close nil=%v err=%v, want graceful disable", hook == nil, closeHook == nil, hookErr)
	}
}

// TestChainAccountCircuitMutationPersistsIncident：主链 SUSPECT 转换经
// OnMutation → bridge → CAS 落 incidents 行（管理页 circuitSummary 事实
// 源）；同一 mutation 重放（同 transitionID 的 CAS 输入）被 outbox dedupe
// 识别为 idempotent 回执，不产生重复行、ledger 不回退。
func TestChainAccountCircuitMutationPersistsIncident(t *testing.T) {
	db := w17eOpenBusinessSQLite(t)
	service, closeService, err := newChainAccountCircuitService("memory", "", "", w17eGateReadyConfig(db))
	if err != nil {
		t.Fatalf("create circuit service: %v", err)
	}
	t.Cleanup(closeService)

	result, err := service.PrepareAttempt(context.Background(), gatewaycircuit.PrepareAttemptInput{
		Account:                     gatewayruntimecache.OpenAIAccountSecret{ID: "acc", ProviderProtocolProfileID: "profile"},
		RequestLane:                 gatewaycircuit.LaneText,
		ConfirmationLeaseDurationMs: 30_000,
	})
	if err != nil {
		t.Fatalf("PrepareAttempt: %v", err)
	}
	if result.Outcome != gatewaycircuit.PrepareDispatchable || result.Attempt == nil {
		t.Fatalf("PrepareAttempt outcome = %s, want dispatchable attempt", result.Outcome)
	}
	decision, err := result.Attempt.ReportTransportFailure(context.Background(), gatewaycircuit.TransportFailure{
		Kind:   gatewaycircuit.TransportFailureKindTransport,
		Reason: "connect refused",
	})
	if err != nil {
		t.Fatalf("ReportTransportFailure: %v", err)
	}
	if decision.Outcome != gatewaycircuit.DecisionSuspected {
		t.Fatalf("failure decision outcome = %s, want suspected", decision.Outcome)
	}

	// 等待 incident 行出现（异步 worker 落库）。
	var (
		state            string
		ledgerRevision   int64
		scopeKey         string
		transitionID     string
		incidentID       string
		protocolCode     string
		requestLane      string
		modelFamily      string
		lastFailureClass sql.NullString
	)
	w17eWaitFor(t, "incident row persisted", func() bool {
		return db.QueryRow(`SELECT state, ledger_revision, circuit_scope_key, transition_id, incident_id,
			protocol_code, request_lane, model_family, last_failure_class
			FROM account_circuit_incidents WHERE account_id='acc'`).
			Scan(&state, &ledgerRevision, &scopeKey, &transitionID, &incidentID,
				&protocolCode, &requestLane, &modelFamily, &lastFailureClass) == nil
	})
	if state != "SUSPECT" {
		t.Fatalf("persisted state = %s, want SUSPECT", state)
	}
	if ledgerRevision != 1 {
		t.Fatalf("ledger revision = %d, want 1", ledgerRevision)
	}
	if protocolCode != "profile" || requestLane != "text" || modelFamily != "unknown" {
		t.Fatalf("scope projection = (%s,%s,%s), want (profile,text,unknown)", protocolCode, requestLane, modelFamily)
	}
	if !lastFailureClass.Valid || lastFailureClass.String == "" {
		t.Fatalf("last_failure_class must classify the failure reason, got %v", lastFailureClass)
	}

	// 幂等重放：与已落行同 scope/transition 的 CAS 输入再提交一次 →
	// idempotent 回执，行数与 ledger revision 不变。
	replayInput := gatewaycircuit.CompareAndSetIncidentInput{
		AccountID:                       "acc",
		AccountRuntimeKey:               "acc",
		CircuitScopeKey:                 scopeKey,
		ScopeKind:                       gatewaycircuit.ScopeKindProtocolModel,
		ProtocolCode:                    strPtr(protocolCode),
		RequestLane:                     strPtr(requestLane),
		ModelFamily:                     strPtr(modelFamily),
		IncidentID:                      incidentID,
		State:                           state,
		Generation:                      1,
		DispatchRevision:                1,
		ExpectedLedgerRevision:          int64PtrOf(1),
		TransitionID:                    transitionID,
		UpstreamAttemptObserved:         boolPtr(true),
		BackoffLevel:                    int64PtrOf(0),
		ConsecutiveFailures:             int64PtrOf(1),
		ConfirmationFailuresRequired:    int64PtrOf(2),
		ConfirmationFailureEvidenceKeys: []string{strings.Repeat("b", 64)},
		RecoveringSuccesses:             int64PtrOf(0),
		StateUpdatedAtMs:                int64PtrOf(2_000),
	}
	adapterStore, adapterErr := circuitcontrolplane.New(db, circuitcontrolplane.SQLite, businessSchema, circuitcontrolplane.OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if adapterErr != nil {
		t.Fatalf("create adapter store: %v", adapterErr)
	}
	adapter := chainCircuitControlPlaneDB{store: adapterStore}
	replay, err := adapter.CompareAndSetIncident(context.Background(), replayInput)
	if err != nil {
		t.Fatalf("replay apply: %v", err)
	}
	if replay.Status != "idempotent" {
		t.Fatalf("replay status = %s, want idempotent（outbox dedupe 回执）", replay.Status)
	}
	var (
		rows       int
		replayLedg int64
	)
	if err := db.QueryRow(`SELECT count(*), max(ledger_revision) FROM account_circuit_incidents`).Scan(&rows, &replayLedg); err != nil {
		t.Fatalf("count incidents: %v", err)
	}
	if rows != 1 || replayLedg != 1 {
		t.Fatalf("after replay rows=%d ledger=%d, want 1/1（重放不产生重复或回退）", rows, replayLedg)
	}
}

// TestChainAccountCircuitPersistFailureDoesNotBreakChain：业务库句柄失效后，
// 主链状态转换照常返回（无错误、无 panic），持久化失败只留在 bridge 重试
// 通道（热路径安全契约）。
func TestChainAccountCircuitPersistFailureDoesNotBreakChain(t *testing.T) {
	db := w17eOpenBusinessSQLite(t)
	service, closeService, err := newChainAccountCircuitService("memory", "", "", w17eGateReadyConfig(db))
	if err != nil {
		t.Fatalf("create circuit service: %v", err)
	}
	t.Cleanup(closeService)

	// 不同 lane = 独立熔断 scope（protocol_model scope 含 requestLane），
	// 每次调用各走一次完整 SUSPECT 转换。
	runSuspect := func(lane string) (gatewaycircuit.FailureDecision, error) {
		result, err := service.PrepareAttempt(context.Background(), gatewaycircuit.PrepareAttemptInput{
			Account:                     gatewayruntimecache.OpenAIAccountSecret{ID: "acc", ProviderProtocolProfileID: "profile"},
			RequestLane:                 lane,
			ConfirmationLeaseDurationMs: 30_000,
		})
		if err != nil {
			return gatewaycircuit.FailureDecision{}, err
		}
		if result.Attempt == nil {
			return gatewaycircuit.FailureDecision{}, errors.New("w17e: PrepareAttempt returned no attempt")
		}
		return result.Attempt.ReportTransportFailure(context.Background(), gatewaycircuit.TransportFailure{
			Kind:   gatewaycircuit.TransportFailureKindTimeout,
			Reason: "deadline exceeded",
		})
	}
	// 第一笔：落行成功，证明持久化通道已接通。
	if _, err := runSuspect(gatewaycircuit.LaneText); err != nil {
		t.Fatalf("first suspect: %v", err)
	}
	w17eWaitFor(t, "first incident row", func() bool {
		var rows int
		return db.QueryRow(`SELECT count(*) FROM account_circuit_incidents`).Scan(&rows) == nil && rows >= 1
	})
	// 关闭业务库句柄：后续持久化必然失败。
	if err := db.Close(); err != nil {
		t.Fatalf("close business db: %v", err)
	}
	// 第二笔：状态机行为不变（照常 suspected），持久化失败不向请求传播。
	decision, err := runSuspect(gatewaycircuit.LaneImage)
	if err != nil {
		t.Fatalf("suspect after persistence outage must not fail the request: %v", err)
	}
	if decision.Outcome != gatewaycircuit.DecisionSuspected {
		t.Fatalf("outcome after outage = %s, want suspected（状态机语义零改动）", decision.Outcome)
	}
}
