package main

// 缺陷 E（熔断观测断链）装配：把 Go 主链的熔断状态转换接到既有 control-plane
// 持久化管道上。链路：
//
//	CircuitService OnMutation → gatewaycircuit.Bridge.Observe（按 scope 异步
//	合并、worker 内有界重试）→ circuitcontrolplane.CompareAndSetIncident
//	（juhe_business.account_circuit_incidents 的 ledger CAS 围栏 upsert +
//	account_circuit_outbox 幂等回执，同一事务）。
//
// jobs 侧（opsjobs.ControlPlaneMaintenance 与管理页 circuitSummary 读面
// listavailability_sources）消费同一 incidents/outbox 面，本接线完成后零
// 改动即可见。写侧归属依据：jobs/internal/circuitstore/controlplane.go 头注
// 「写侧归 gateway 模块 backend-go/projects/gateway/internal/business/
// circuit_control_plane 的 CAS 写路径」；Node 归档热修的 account_not_found
// 终态语义也在该写侧实现。
//
// 热路径安全契约：notifyMutation 会把 OnMutation 错误向上传播（gatewaycircuit/
// service.go），因此本文件构造的回调恒返回 nil；Observe 只做互斥锁下的合并
// 入队并启动 worker goroutine，DB 写入全部发生在 worker 上，DB 不可用/超时
// 只触发 bridge 内部重试与 OnPersistFailure 日志，绝不阻塞主链、绝不向请求
// 返回错误。
//
// 幂等契约：同一 transition 的重放在 CAS 事务内被 outbox dedupe_key
// （"incident:"+transitionID）识别为 idempotent 回执，不产生新 ledger
// revision；generation 单调围栏（current.Generation > input.Generation 即
// cas_conflict）阻止迟到的旧状态回退 incident 行。

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	circuitcontrolplane "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/circuit_control_plane"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
)

// chainAccountCircuitPersistConfig 是主链熔断持久观测的装配输入。DB 为业务库
// 句柄（compose.db：SQLite 业务文件句柄或共享 PostgreSQL 池连接）；三个
// owner-gate 布尔与 businesssettings/businessauth/groupdirtycursor 同源
// （cfg.Business*，零配置自动认领时已置真）。
type chainAccountCircuitPersistConfig struct {
	DB                *sql.DB
	Postgres          bool
	Confirmed         bool
	SchemaReady       bool
	NodeWriterStopped bool
}

// chainCircuitControlPlaneDB 把 circuitcontrolplane.Store 适配到
// gatewaycircuit.ControlPlaneDB 端口（纯字段映射，无语义增减）。
type chainCircuitControlPlaneDB struct {
	store *circuitcontrolplane.Store
}

var _ gatewaycircuit.ControlPlaneDB = chainCircuitControlPlaneDB{}

// CompareAndSetIncident 映射 CAS 输入/结果（camelCase Ms → 大写 MS，标量
// 指针 → 具体零值）。
func (d chainCircuitControlPlaneDB) CompareAndSetIncident(ctx context.Context, input gatewaycircuit.CompareAndSetIncidentInput) (gatewaycircuit.CompareAndSetIncidentResult, error) {
	result, err := d.store.CompareAndSetIncident(ctx, chainCircuitIncidentMutation(input))
	if err != nil {
		return gatewaycircuit.CompareAndSetIncidentResult{}, err
	}
	out := gatewaycircuit.CompareAndSetIncidentResult{
		Status:                  result.Status,
		CurrentDispatchRevision: result.CurrentDispatchRevision,
	}
	if result.Incident != nil {
		record := chainCircuitIncidentRecord(*result.Incident)
		out.Incident = &record
	}
	return out, nil
}

// ListIncidentsForRebuild 映射 ledger 分页读（bridge 重建/对账用）。
func (d chainCircuitControlPlaneDB) ListIncidentsForRebuild(ctx context.Context, input gatewaycircuit.RebuildPageInput) (gatewaycircuit.RebuildPage, error) {
	afterUpdatedMs := int64(0)
	if input.AfterUpdatedAtMs != nil {
		afterUpdatedMs = *input.AfterUpdatedAtMs
	}
	afterScopeKey := ""
	if input.AfterCircuitScopeKey != nil {
		afterScopeKey = *input.AfterCircuitScopeKey
	}
	page, err := d.store.ListForRebuild(ctx, input.NowMs, afterUpdatedMs, afterScopeKey, input.Limit)
	if err != nil {
		return gatewaycircuit.RebuildPage{}, err
	}
	out := gatewaycircuit.RebuildPage{Items: make([]gatewaycircuit.IncidentRecord, 0, len(page.Items))}
	for _, incident := range page.Items {
		out.Items = append(out.Items, chainCircuitIncidentRecord(incident))
	}
	if page.NextCursor != nil {
		out.NextCursor = &gatewaycircuit.RebuildCursor{
			UpdatedAtMs:     page.NextCursor.UpdatedAtMS,
			CircuitScopeKey: page.NextCursor.CircuitScopeKey,
		}
	}
	return out, nil
}

// ListIncidentsByRuntimeKeys 映射摘要读（bridge account load 用）。
func (d chainCircuitControlPlaneDB) ListIncidentsByRuntimeKeys(ctx context.Context, input gatewaycircuit.ListIncidentsByRuntimeKeysInput) ([]gatewaycircuit.IncidentRecord, error) {
	nowMs := int64(0)
	if input.NowMs != nil {
		nowMs = *input.NowMs
	}
	incidents, err := d.store.ListByRuntimeKeys(ctx, input.AccountRuntimeKeys, input.IncludeRetainedClosed, nowMs)
	if err != nil {
		return nil, err
	}
	out := make([]gatewaycircuit.IncidentRecord, 0, len(incidents))
	for _, incident := range incidents {
		out = append(out, chainCircuitIncidentRecord(incident))
	}
	return out, nil
}

// GetIncidentByScopeKey 映射单键读（bridge outbox 投影用）；未命中返回 nil。
func (d chainCircuitControlPlaneDB) GetIncidentByScopeKey(ctx context.Context, circuitScopeKey string) (*gatewaycircuit.IncidentRecord, error) {
	incident, found, err := d.store.GetIncident(ctx, circuitScopeKey)
	if err != nil || !found {
		return nil, err
	}
	record := chainCircuitIncidentRecord(incident)
	return &record, nil
}

// ClaimOutbox 映射 outbox 认领。
func (d chainCircuitControlPlaneDB) ClaimOutbox(ctx context.Context, input gatewaycircuit.ClaimOutboxInput) ([]gatewaycircuit.OutboxEvent, error) {
	events, err := d.store.ClaimOutbox(ctx, input.OwnerID, input.NowMs, input.LeaseMs, input.Limit)
	if err != nil {
		return nil, err
	}
	out := make([]gatewaycircuit.OutboxEvent, 0, len(events))
	for _, event := range events {
		out = append(out, chainCircuitOutboxEvent(event))
	}
	return out, nil
}

// AckOutbox 映射 outbox 确认（含投影水位回写）。
func (d chainCircuitControlPlaneDB) AckOutbox(ctx context.Context, input gatewaycircuit.AckOutboxInput) (gatewaycircuit.AckOutboxResult, error) {
	acknowledged, err := d.store.AcknowledgeOutbox(ctx, input.EventID, input.ProjectionKey, input.ClaimToken, input.AcknowledgedAtMs)
	return gatewaycircuit.AckOutboxResult{Acknowledged: acknowledged}, err
}

// ReleaseOutboxForReplay 映射 outbox 失败重放释放。
func (d chainCircuitControlPlaneDB) ReleaseOutboxForReplay(ctx context.Context, input gatewaycircuit.ReleaseOutboxInput) error {
	_, err := d.store.ReleaseOutboxForReplay(ctx, input.EventID, input.ClaimToken, input.ErrorClass, input.NowMs, input.RetryDelayMs)
	return err
}

// chainCircuitIncidentMutation 把 CAS 输入映射为业务库 IncidentMutation。
// gatewaycircuit 侧无 CreatedAtMs（新行由 store 侧契约落 0，更新行保留
// current.CreatedAtMS），StateUpdatedAtMs 承担 updatedAt 语义。
func chainCircuitIncidentMutation(input gatewaycircuit.CompareAndSetIncidentInput) circuitcontrolplane.IncidentMutation {
	return circuitcontrolplane.IncidentMutation{
		ExpectedLedgerRevision: input.ExpectedLedgerRevision,
		Incident: circuitcontrolplane.Incident{
			CircuitScopeKey:                 input.CircuitScopeKey,
			AccountID:                       input.AccountID,
			AccountRuntimeKey:               input.AccountRuntimeKey,
			ScopeKind:                       input.ScopeKind,
			KeyFingerprint:                  input.KeyFingerprint,
			ProtocolCode:                    input.ProtocolCode,
			RequestLane:                     input.RequestLane,
			ModelFamily:                     input.ModelFamily,
			ClientModel:                     input.ClientModel,
			CapabilityHash:                  input.CapabilityHash,
			CredentialSourceAccountID:       input.CredentialSourceAccountID,
			ClientEndpointFamily:            input.ClientEndpointFamily,
			FinalUpstreamModel:              input.FinalUpstreamModel,
			UpstreamEndpointMode:            input.UpstreamEndpointMode,
			IncidentID:                      chainStringPtr(input.IncidentID),
			ParentIncidentID:                input.ParentIncidentID,
			ChildIncidentIDs:                input.ChildIncidentIDs,
			CausedByTerminalOutcomeID:       input.CausedByTerminalOutcomeID,
			State:                           input.State,
			FailureScope:                    chainDerefString(input.FailureScope),
			Generation:                      input.Generation,
			DispatchRevision:                input.DispatchRevision,
			TransitionID:                    input.TransitionID,
			CooldownObservationGeneration:   chainCircuitDerefInt64(input.CooldownObservationGeneration),
			OpenUntilMS:                     input.OpenUntilMs,
			NextTransitionAtMS:              input.NextTransitionAtMs,
			LeaseID:                         input.LeaseID,
			LeasePurpose:                    input.LeasePurpose,
			LeaseOwnerRunID:                 input.LeaseOwnerRunID,
			LeaseUntilMS:                    input.LeaseUntilMs,
			AttemptStartedAtMS:              input.AttemptStartedAtMs,
			AttemptHardDeadlineMS:           input.AttemptHardDeadlineMs,
			UpstreamAttemptObserved:         chainCircuitDerefBool(input.UpstreamAttemptObserved),
			BackoffLevel:                    chainCircuitDerefInt64(input.BackoffLevel),
			ConsecutiveFailures:             chainCircuitDerefInt64(input.ConsecutiveFailures),
			ConfirmationFailuresRequired:    chainCircuitDerefInt64(input.ConfirmationFailuresRequired),
			ConfirmationFailureEvidenceKeys: input.ConfirmationFailureEvidenceKeys,
			RecoveringSuccesses:             chainCircuitDerefInt64(input.RecoveringSuccesses),
			LastFailureClass:                input.LastFailureClass,
			RetainedUntilMS:                 input.RetainedUntilMs,
			UpdatedAtMS:                     chainCircuitDerefInt64(input.StateUpdatedAtMs),
		},
	}
}

// chainCircuitIncidentRecord 把业务库 Incident 行映射回 gatewaycircuit
// IncidentRecord（CAS 结果/重建页复用）。
func chainCircuitIncidentRecord(incident circuitcontrolplane.Incident) gatewaycircuit.IncidentRecord {
	record := gatewaycircuit.IncidentRecord{
		CircuitScopeKey:                 incident.CircuitScopeKey,
		AccountID:                       incident.AccountID,
		AccountRuntimeKey:               incident.AccountRuntimeKey,
		ScopeKind:                       incident.ScopeKind,
		KeyFingerprint:                  incident.KeyFingerprint,
		ProtocolCode:                    incident.ProtocolCode,
		RequestLane:                     incident.RequestLane,
		ModelFamily:                     incident.ModelFamily,
		ClientModel:                     incident.ClientModel,
		CapabilityHash:                  incident.CapabilityHash,
		CredentialSourceAccountID:       incident.CredentialSourceAccountID,
		ClientEndpointFamily:            incident.ClientEndpointFamily,
		FinalUpstreamModel:              incident.FinalUpstreamModel,
		UpstreamEndpointMode:            incident.UpstreamEndpointMode,
		IncidentID:                      chainDerefString(incident.IncidentID),
		ParentIncidentID:                incident.ParentIncidentID,
		ChildIncidentIDs:                incident.ChildIncidentIDs,
		CausedByTerminalOutcomeID:       incident.CausedByTerminalOutcomeID,
		State:                           incident.State,
		Generation:                      incident.Generation,
		DispatchRevision:                incident.DispatchRevision,
		LedgerRevision:                  incident.LedgerRevision,
		ProjectedLedgerRevision:         incident.ProjectedLedgerRevision,
		TransitionID:                    incident.TransitionID,
		CooldownObservationGeneration:   incident.CooldownObservationGeneration,
		OpenUntilMs:                     incident.OpenUntilMS,
		NextTransitionAtMs:              incident.NextTransitionAtMS,
		LeaseID:                         incident.LeaseID,
		LeasePurpose:                    incident.LeasePurpose,
		LeaseOwnerRunID:                 incident.LeaseOwnerRunID,
		LeaseUntilMs:                    incident.LeaseUntilMS,
		AttemptStartedAtMs:              incident.AttemptStartedAtMS,
		AttemptHardDeadlineMs:           incident.AttemptHardDeadlineMS,
		UpstreamAttemptObserved:         incident.UpstreamAttemptObserved,
		BackoffLevel:                    incident.BackoffLevel,
		ConsecutiveFailures:             incident.ConsecutiveFailures,
		ConfirmationFailuresRequired:    incident.ConfirmationFailuresRequired,
		ConfirmationFailureEvidenceKeys: incident.ConfirmationFailureEvidenceKeys,
		RecoveringSuccesses:             incident.RecoveringSuccesses,
		LastFailureClass:                incident.LastFailureClass,
		RetainedUntilMs:                 incident.RetainedUntilMS,
		CreatedAtMs:                     incident.CreatedAtMS,
		UpdatedAtMs:                     incident.UpdatedAtMS,
	}
	if incident.FailureScope != "" {
		failureScope := incident.FailureScope
		record.FailureScope = &failureScope
	}
	return record
}

// chainCircuitOutboxEvent 把业务库 outbox 行映射为 bridge 消费事件。
func chainCircuitOutboxEvent(event circuitcontrolplane.Outbox) gatewaycircuit.OutboxEvent {
	return gatewaycircuit.OutboxEvent{
		EventID:           event.EventID,
		ProjectionKey:     event.ProjectionKey,
		EventType:         event.EventType,
		AccountID:         event.AccountID,
		AccountRuntimeKey: event.AccountRuntimeKey,
		CircuitScopeKey:   event.CircuitScopeKey,
		IncidentID:        event.IncidentID,
		TransitionID:      event.TransitionID,
		DispatchRevision:  event.DispatchRevision,
		Generation:        event.Generation,
		LedgerRevision:    event.LedgerRevision,
		ClaimToken:        event.ClaimToken,
	}
}

func chainStringPtr(value string) *string { return &value }

func chainCircuitDerefInt64(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func chainCircuitDerefBool(value *bool) bool {
	if value == nil {
		return false
	}
	return *value
}

// newChainAccountCircuitPersistHook 装配主链熔断持久观测回调。返回
// (hook, close, error)：
//   - 配置不齐（无业务库句柄或 owner gate 未就绪）时返回 (nil, nil, nil)，
//     保持既有"无持久观测"行为——Node 仍是业务库写者的混合运行态下，
//     Go 主链不越权写 incidents（ErrOwnerGate 纪律）；
//   - 业务库契约缺失（表未建）时不 fail-fast 启动，只记警告并停用持久化，
//     由运维执行 maintenance ensure-schema 后重启恢复；
//   - 其余构造错误按组合根 fail-fast 契约返回 error。
func newChainAccountCircuitPersistHook(runtimeStore gatewaycircuit.Store, config chainAccountCircuitPersistConfig) (func(context.Context, gatewaycircuit.MutationEvent) error, func(), error) {
	if config.DB == nil || !config.Confirmed || !config.SchemaReady || !config.NodeWriterStopped {
		return nil, nil, nil
	}
	mode := circuitcontrolplane.SQLite
	if config.Postgres {
		mode = circuitcontrolplane.Postgres
	}
	gate := circuitcontrolplane.OwnerGate{
		Confirmed:         config.Confirmed,
		SchemaReady:       config.SchemaReady,
		NodeWriterStopped: config.NodeWriterStopped,
	}
	controlStore, err := circuitcontrolplane.New(config.DB, mode, businessSchema, gate)
	if err != nil {
		return nil, nil, fmt.Errorf("create gateway account circuit control-plane store: %w", err)
	}
	if contractErr := controlStore.CheckContract(context.Background()); contractErr != nil {
		slog.Warn("gateway 账户电路持久观测停用：业务库 circuit control-plane 契约校验失败（maintenance ensure-schema 后重启恢复）",
			"event", "gateway_account_circuit_persistence_disabled",
			"error", contractErr.Error())
		return nil, nil, nil
	}
	bridge, err := gatewaycircuit.NewBridge(gatewaycircuit.BridgeOptions{
		Store:            runtimeStore,
		DB:               chainCircuitControlPlaneDB{store: controlStore},
		OnPersistFailure: chainCircuitPersistFailureLogger,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("create gateway account circuit control-plane bridge: %w", err)
	}
	hook := func(_ context.Context, event gatewaycircuit.MutationEvent) error {
		// 热路径安全契约：Observe 只做按 scope 的合并入队（互斥锁 + worker
		// goroutine 启动），DB 写入/重试全部发生在 worker 上，本回调恒返回
		// nil——notifyMutation 的错误传播路径（gatewaycircuit/service.go）对
		// 请求不可见。
		bridge.Observe(event.Scope, event.State)
		return nil
	}
	return hook, bridge.Close, nil
}

// chainCircuitPersistFailureLogger 把 bridge 持久化失败诊断记入进程日志
// （持久化失败的唯一可见出口；不含凭据与上游响应）。
func chainCircuitPersistFailureLogger(failure gatewaycircuit.PersistFailure) {
	slog.Warn("gateway 账户电路状态转换持久化失败（不阻塞主链，稍后自动重试）",
		"event", "gateway_account_circuit_persist_failed",
		"scopeKey", failure.ScopeKey,
		"accountRuntimeKey", failure.AccountRuntimeKey,
		"error", failure.Err.Error())
}
