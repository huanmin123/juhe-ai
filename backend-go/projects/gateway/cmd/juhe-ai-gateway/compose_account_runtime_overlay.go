package main

// compose_account_runtime_overlay.go — 账户列表运行态 overlay 的读伴随装配
// （缺陷修复批次一，与 SetConcurrencyReader 同位：仅 cfg.ChainEnabled 分支）：
//
//	RuntimeAvailabilitySource ← chainRuntimeServices.SuppressionStore
//	                            （D-134 进程内屏蔽快照，suppression.go
//	                            SnapshotAvailability）
//	                            + chainRuntimeServices.ConfiguredPolicyAvoidance
//	                            （D-132 配置策略避让状态；jobs 只读面
//	                            RuntimeStateReader.LoadRuntimeAvailability 的
//	                            合并语义：避让条目覆盖同键本地屏蔽条目）
//	CircuitSummarySource      ← circuitcontrolplane.Store（业务库
//	                            account_circuit_incidents 持久账本）经
//	                            gatewaycircuit.LoadPublicAccountCircuitSummaries
//	                            公共摘要 reducer（与熔断持久观测同一 owner
//	                            gate；未就绪/契约缺失时不装配，字段缺席）
//	APIKeyRuntimeSummarySource ← accountkeystates.Store（summary.go
//	                            LoadSummariesByAccountIds；专用 Store 实例与
//	                            compose_account_reads.go 同款，共享表与 CAS 围栏）
//
// 失败语义：三个端口读失败在 accounts.hydrateRuntimeOverlay 内逐源降级为字段
// 缺席（warn 留痕、不阻断页面）；装配期构造失败按组合根约定 fail-fast。
// 探针臂（probePresentation / 恢复探针 schedule）属第二批，本批不产出。

import (
	"context"
	"log/slog"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accountkeystates"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
	circuitcontrolplane "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/circuit_control_plane"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
)

// circuitSummaryReadChunk 是控制面摘要读的运行态键分块上限（
// gatewaycircuit.boundedRuntimeKeys / circuitcontrolplane.ListByRuntimeKeys
// 单次最多 100 键；分页上限 200 行时分块保证页尾不被静默丢弃）。
const circuitSummaryReadChunk = 100

// wireAccountRuntimeOverlaySources 装配账户列表三路运行态读端口。services 为
// nil（组合测试形态）时 runtime 端口保持 nil；其余端口按各自 gate 装配。
func wireAccountRuntimeOverlaySources(composed *composition, cfg runtimeConfig, accountStore *accounts.Store, services *chainRuntimeServices) error {
	if services != nil && services.SuppressionStore != nil {
		accountStore.SetRuntimeAvailabilitySource(chainRuntimeAvailabilitySource{
			suppression: services.SuppressionStore,
			avoidance:   services.ConfiguredPolicyAvoidance,
		})
	}
	// circuit 摘要读面与熔断持久观测共用 owner gate 三元（Business* 配置）。
	// gate 未就绪时不装配（Node 仍是账本写者的混合运行态下列表不出摘要，
	// 与 newChainAccountCircuitPersistHook 的 ErrOwnerGate 纪律一致）；表未建
	// 时 warn 停用，maintenance ensure-schema 后重启恢复。
	if composed.db != nil && cfg.BusinessHandoffConfirmed && cfg.BusinessSchemaReady && cfg.BusinessNodeWriterStopped {
		mode := circuitcontrolplane.SQLite
		if composed.pgDialect {
			mode = circuitcontrolplane.Postgres
		}
		controlStore, err := circuitcontrolplane.New(composed.db, mode, businessSchema, circuitcontrolplane.OwnerGate{
			Confirmed:         cfg.BusinessHandoffConfirmed,
			SchemaReady:       cfg.BusinessSchemaReady,
			NodeWriterStopped: cfg.BusinessNodeWriterStopped,
		})
		if err != nil {
			return err
		}
		if contractErr := controlStore.CheckContract(context.Background()); contractErr != nil {
			slog.Warn("账户列表 circuitSummary 读面停用：业务库 circuit control-plane 契约校验失败（maintenance ensure-schema 后重启恢复）",
				"event", "gateway_account_circuit_summary_source_disabled",
				"error", contractErr.Error())
		} else {
			accountStore.SetCircuitSummarySource(chainCircuitSummarySource{store: controlStore})
		}
	}
	keyStates, err := accountkeystates.NewStore(accountkeystates.Config{
		DB:       composed.db,
		Postgres: composed.pgDialect,
		Secret:   cfg.Secret,
		Now:      time.Now,
	})
	if err != nil {
		return err
	}
	accountStore.SetAPIKeyRuntimeSummarySource(chainAPIKeyRuntimeSummarySource{store: keyStates})
	return nil
}

// chainRuntimeAvailabilitySource merges the D-134 process-local suppression
// snapshot with the D-132 configured policy avoidance states into the list
// runtimeAvailability projection.
type chainRuntimeAvailabilitySource struct {
	suppression *gatewaycircuit.LocalSuppressionStore
	avoidance   *gatewayaccounteffects.ConfiguredPolicyAvoidanceService
}

// LoadRuntimeAvailabilityByRuntimeKeys implements accounts.RuntimeAvailabilitySource.
func (s chainRuntimeAvailabilitySource) LoadRuntimeAvailabilityByRuntimeKeys(ctx context.Context, runtimeKeys []string) (map[string]accounts.AccountRuntimeAvailabilityPublic, error) {
	out := map[string]accounts.AccountRuntimeAvailabilityPublic{}
	// 列表读面不感知 precheck 调度预算：dispatch 过滤面把 precheck 判定留在
	// store 锁内（FilterSuppressions nil 谓词语义），快照谓词传 false 等价
	// jobs 只读面——本地屏蔽条目按窗口可见性展示，不因 precheck 预算额外隐藏。
	for runtimeKey, availability := range s.suppression.SnapshotAvailability(func(string) bool { return false }) {
		out[runtimeKey] = accounts.AccountRuntimeAvailabilityPublic{
			Status: availability.Status,
			Reason: availability.Reason,
			Since:  availability.Since,
		}
	}
	if s.avoidance != nil {
		states, err := s.avoidance.LoadConfiguredPolicyAvoidanceStates(ctx, runtimeKeys)
		if err != nil {
			return nil, err
		}
		for index, runtimeKey := range runtimeKeys {
			state := states[index]
			if state == nil {
				continue
			}
			// 避让条目覆盖同键本地屏蔽（jobs LoadRuntimeAvailability 同序）：
			// local_suppressed + 避让原因/起始时刻。
			out[runtimeKey] = accounts.AccountRuntimeAvailabilityPublic{
				Status: gatewaycircuit.AvailabilityStatusLocalSuppressed,
				Reason: state.Reason,
				Since:  accountscore.IsoMillis(time.UnixMilli(state.StartedAtMs)),
			}
		}
	}
	return out, nil
}

// chainCircuitSummarySource adapts the durable account circuit incident ledger
// onto the list circuitSummary overlay（gatewaycircuit 公共摘要 reducer 复用，
// 与 jobs listavailability_sources.loadCircuitSummaries 同一投影）。
type chainCircuitSummarySource struct {
	store *circuitcontrolplane.Store
}

// LoadCircuitSummariesByRuntimeKeys implements accounts.CircuitSummarySource.
func (s chainCircuitSummarySource) LoadCircuitSummariesByRuntimeKeys(ctx context.Context, runtimeKeys []string) (map[string]accounts.AccountCircuitSummaryPublic, error) {
	out := make(map[string]accounts.AccountCircuitSummaryPublic, len(runtimeKeys))
	db := chainCircuitControlPlaneDB{store: s.store}
	for start := 0; start < len(runtimeKeys); start += circuitSummaryReadChunk {
		end := start + circuitSummaryReadChunk
		if end > len(runtimeKeys) {
			end = len(runtimeKeys)
		}
		summaries, err := gatewaycircuit.LoadPublicAccountCircuitSummaries(ctx, db, runtimeKeys[start:end])
		if err != nil {
			return nil, err
		}
		for runtimeKey, summary := range summaries {
			out[runtimeKey] = accounts.AccountCircuitSummaryPublic{
				Status:      summary.Status,
				Reason:      summary.Reason,
				Since:       summary.Since,
				NextCheckAt: summary.NextCheckAt,
			}
		}
	}
	return out, nil
}

// chainAPIKeyRuntimeSummarySource adapts the accountkeystates pool summary
// onto the list apiKeyRuntime overlay（public 聚合形状：无诊断、无单 Key
// 信息，与前端 AccountApiKeyRuntimePublicSummary 一一对应）。
type chainAPIKeyRuntimeSummarySource struct {
	store *accountkeystates.Store
}

// LoadAPIKeyRuntimeSummariesByAccountIds implements
// accounts.APIKeyRuntimeSummarySource.
func (s chainAPIKeyRuntimeSummarySource) LoadAPIKeyRuntimeSummariesByAccountIds(ctx context.Context, accountIDs []string) (map[string]accounts.AccountApiKeyRuntimeSummaryPublic, error) {
	loaded, err := s.store.LoadSummariesByAccountIds(ctx, accountIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[string]accounts.AccountApiKeyRuntimeSummaryPublic, len(loaded))
	for accountID, summary := range loaded {
		out[accountID] = accounts.AccountApiKeyRuntimeSummaryPublic{
			Total:                summary.Total,
			Active:               summary.Active,
			TemporaryUnavailable: summary.TemporaryUnavailable,
			RateLimited:          summary.RateLimited,
			Error:                summary.Error,
			Disabled:             summary.Disabled,
			Unavailable:          summary.Unavailable,
			AllUnavailable:       summary.AllUnavailable,
			NextProbeAt:          summary.NextProbeAt,
		}
	}
	return out, nil
}
