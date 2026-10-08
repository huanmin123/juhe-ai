package main

// PLAN-20261008T113056000Z 本地单机形态恢复驱动：gateway 进程内的账户电路
// 后台恢复组件（memory 运行态形态专属装配）。
//
// 形态归属裁决「谁拥有运行态，谁负责恢复」：账户熔断运行态在 memory 驱动
// 下是 gateway 进程内事实（NewMemoryStore），jobs 的 account-circuit-recovery
// 任务族在该形态 fail-closed disabled（Redis store 无从读取进程内状态），
// 恢复驱动因此装配在本进程；redis 驱动下运行态共享，恢复仍归 jobs，本组件
// 不装配（composeChainRuntimeServices 按 cfg.RuntimeStateDriver 分叉）。
// 这是依赖门禁（运行态归属决定），不是功能开关。
//
// 状态机契约源：backend-go/projects/jobs/internal/opsjobs/circuitrecovery.go
// （gatewaycircuit.RecoveryService 逐段对照移植）；探针执行链契约源：
// backend-go/projects/jobs/cmd/juhe-ai-jobs/worker_circuit_jobs.go 的
// circuitRecoveryTargetResolver（identity 解析 → LoadAccountForTest →
// authorized 取 identity 的 group/system → LoadAccountForGroup →
// dispatch revision 围栏 → limited 诊断探针 → outcome 分类）。
//
// 投影契约：sweep 的每次 store mutation 以 MutationEvent 形状交给主链既有
// persist hook（bridge.Observe 包装，chain_circuit_controlplane.go），恢复
// 转换与请求热路径转换投影同一条 ledger 管道；hook 为 nil（owner gate 不齐）
// 时 sweep 照常推进运行态，仅 ledger 展示滞后（对齐主链同构行为）。
//
// 调度契约（对齐 jobs jobregistry/schedule.go 的 account-circuit-recovery
// 条目：Interval 5s / InitialDelay 5s / PassiveJitter）：节拍与抖动用
// shared/platform/schedulejitter（jobs scheduler 的 passiveIntervalDelay 同
// 源实现）；组件骨架对照 compose_authz_expiry_sync.go——先跑一次 + Ticker +
// 每轮 WithTimeout + 失败只 Warn 不中断，组件只在 ctx 取消时退出。

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountprobe"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountquality"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/proberepo"
	"github.com/huanminabc/juhe-ai/backend-go-platform/schedulejitter"
	"github.com/huanminabc/juhe-ai/backend-go-platform/supervisor"
)

const (
	// 节拍与初始延迟对齐 jobs account-circuit-recovery 任务族（5s/5s）。
	chainCircuitRecoveryInterval     = 5 * time.Second
	chainCircuitRecoveryInitialDelay = 5 * time.Second
	// 单轮超时覆盖最坏一轮：sweep 探针租约 deadline 为 180s
	//（gatewaycircuit.RecoveryLeaseDurationMS，探针耗时硬上界），加少量
	// 缓冲；超时截断的项按 unknown/release 语义落地（RecoveryService.Sweep
	// 聚合错误 → 仅 Warn，下一轮重试）。
	chainCircuitRecoveryPassTimeout = 185 * time.Second
)

// newChainAccountCircuitRecoveryComponent 装配 memory 形态的恢复驱动组件。
// 构造失败按组合根 fail-fast 契约向上传播（proberepo/probe service 打开失败
// 属于组合错误，不是运行期降级面）。
func newChainAccountCircuitRecoveryComponent(runtime chainAccountCircuitRuntime, composed *composition, cfg runtimeConfig) (supervisor.Component, error) {
	probeConcurrency, err := loadGatewayProbeConcurrency(os.Getenv)
	if err != nil {
		return supervisor.Component{}, err
	}
	savedStore, err := proberepo.NewStore(proberepo.Config{
		DB:       composed.db,
		Postgres: composed.pgDialect,
		Secret:   cfg.Secret,
	})
	if err != nil {
		return supervisor.Component{}, fmt.Errorf("create gateway circuit recovery probe store: %w", err)
	}
	probeService, err := accountprobe.NewService(accountprobe.Options{
		Source:      savedStore,
		Secret:      cfg.Secret,
		Concurrency: probeConcurrency,
	})
	if err != nil {
		return supervisor.Component{}, fmt.Errorf("create gateway circuit recovery probe service: %w", err)
	}
	resolver := chainCircuitRecoveryTargetResolver{store: savedStore, probe: probeService}
	recovery, err := gatewaycircuit.NewRecoveryService(runtime.Store, resolver.Resolve, gatewaycircuit.RecoveryServiceOptions{
		Concurrency: probeConcurrency,
		NowMS:       func() int64 { return time.Now().UnixMilli() },
		OnMutation:  runtime.MutationHook,
	})
	if err != nil {
		return supervisor.Component{}, fmt.Errorf("create gateway circuit recovery service: %w", err)
	}
	return supervisor.Component{
		Name: "account-circuit-recovery",
		Run: func(runCtx context.Context) error {
			// 首轮延迟带 PassiveJitter（对齐 jobs InitialDelay 5s + 抖动；
			// window 取 interval 档位，与 jobs passiveScheduleInitialDelayMS
			// 的半延迟钳制同族近似，避免多实例同拍起跑）。
			select {
			case <-runCtx.Done():
				return runCtx.Err()
			case <-time.After(chainCircuitRecoveryInitialDelay + schedulejitter.Offset(chainCircuitRecoveryInterval)):
			}
			ticker := time.NewTicker(chainCircuitRecoveryInterval + schedulejitter.Offset(chainCircuitRecoveryInterval))
			defer ticker.Stop()
			runPass := func() {
				ctx, cancel := context.WithTimeout(runCtx, chainCircuitRecoveryPassTimeout)
				defer cancel()
				result, err := recovery.Sweep(ctx)
				if err != nil {
					// 单轮失败仅告警并等下一轮（恢复推进是尽力而为的调度
					// 事实，租约互斥保证并发 sweep 无双 winner；不得因短暂
					// DB/上游抖动拖垮进程）。
					slog.Warn("账户电路后台恢复扫描失败，等待下一轮",
						"event", "gateway_account_circuit_recovery_failed",
						"due", result.DueCount,
						"error", err)
					return
				}
				if result.DueCount > 0 {
					slog.Info("账户电路后台恢复扫描完成",
						"event", "gateway_account_circuit_recovery_swept",
						"due", result.DueCount,
						"leased", result.LeasedCount,
						"framingComplete", result.FramingCompleteCount,
						"transportIncomplete", result.TransportIncompleteCount,
						"unknown", result.UnknownCount,
						"fenced", result.FencedCount,
						"skipped", result.SkippedCount,
						"credentialRejected", result.CredentialRejectedCount)
				}
			}
			runPass()
			for {
				select {
				case <-runCtx.Done():
					return runCtx.Err()
				case <-ticker.C:
					runPass()
					// 每轮重置抖动节拍（jobs PassiveJitter：相邻轮不收敛）。
					ticker.Reset(chainCircuitRecoveryInterval + schedulejitter.Offset(chainCircuitRecoveryInterval))
				}
			}
		},
	}, nil
}

// chainCircuitRecoveryTargetResolver 适配 proberepo 账户读取链 + accountprobe
// 为 gatewaycircuit.RecoveryTargetResolver（契约源
// worker_circuit_jobs.go circuitRecoveryTargetResolver，对照 Node
// createScheduledAccountCircuitRecoveryResolver：owner/authorized 身份 →
// find_account_for_test → find_openai_account_for_group（ignoreAvailability）
// → dispatch revision 围栏 → limited 诊断探针）。
//
// 与契约源的已知差异（登记为限制，不做伪装）：契约源按
// gatewayAccountRuntimeKey(candidate) 复核运行态键与 scope 一致；
// proberepo.CandidateAccount 未暴露 accessType/绑定上下文，无法重建授权键，
// 该复核退化为查询参数一致性（identity 派生自同一 runtime key），配合
// store 侧 dispatch revision CAS 围栏兜底（契约源同段注释）。
type chainCircuitRecoveryTargetResolver struct {
	store *proberepo.Store
	probe *accountprobe.Service
}

func (r chainCircuitRecoveryTargetResolver) Resolve(ctx context.Context, state gatewaycircuit.State) (gatewaycircuit.RecoveryProbeTarget, bool, error) {
	if err := ctx.Err(); err != nil {
		return gatewaycircuit.RecoveryProbeTarget{}, false, err
	}
	identity, ok := gatewaycircuit.ParseRecoveryRuntimeIdentity(state.Scope.AccountRuntimeKey)
	if !ok {
		return gatewaycircuit.RecoveryProbeTarget{}, false, nil
	}
	account, err := r.store.LoadAccountForTest(ctx, identity.AccountID)
	if err != nil {
		return gatewaycircuit.RecoveryProbeTarget{}, false, err
	}
	if account == nil {
		return gatewaycircuit.RecoveryProbeTarget{}, false, nil
	}
	groupID := account.BoundGroupID
	systemAccountID := account.SystemAccountID
	if identity.Kind == "authorized" {
		groupID = identity.GroupID
		systemAccountID = identity.SystemAccountID
	}
	if strings.TrimSpace(groupID) == "" || strings.TrimSpace(systemAccountID) == "" {
		return gatewaycircuit.RecoveryProbeTarget{}, false, nil
	}
	candidate, err := r.store.LoadAccountForGroup(ctx, groupID, identity.AccountID, systemAccountID)
	if err != nil {
		return gatewaycircuit.RecoveryProbeTarget{}, false, err
	}
	if candidate == nil {
		return gatewaycircuit.RecoveryProbeTarget{}, false, nil
	}
	if !candidate.HasDispatchRevision || candidate.DispatchRevision <= 0 {
		return gatewaycircuit.RecoveryProbeTarget{}, false, nil
	}
	target := gatewaycircuit.RecoveryProbeTarget{
		DispatchRevision: fmt.Sprintf("%d", candidate.DispatchRevision),
	}
	target.Probe = func(probeCtx context.Context) (gatewaycircuit.TransportProbeOutcome, error) {
		if probeCtx.Err() != nil {
			return gatewaycircuit.TransportProbeOutcome{Kind: gatewaycircuit.RecoveryProbeOutcomeUnknown, FailureKind: gatewaycircuit.RecoveryProbeFailureCanceled}, nil
		}
		return chainCircuitRecoveryTransportProbe(probeCtx, r.probe, chainCircuitRecoveryProbeRequest(identity, state, groupID, systemAccountID)), nil
	}
	return target, true, nil
}

// chainCircuitRecoveryProbeRequest 构造恢复探测请求（契约源
// circuitRecoveryProbeRequest）。protocol_model scope 钉住 modelBucket（真实
// 流量触发熔断时的模型，保证"探测模型 == 熔断模型"），modelBucket 解析不到
// （account/key scope，或 protocol_model scope 的 bucket 为空白/异常值）时
// 回退现状：不钉住，走账户健康检查模型默认逻辑。
func chainCircuitRecoveryProbeRequest(identity gatewaycircuit.RecoveryRuntimeIdentity, state gatewaycircuit.State, groupID, systemAccountID string) accountquality.ProbeRequest {
	pinnedModel := ""
	if state.Scope.Kind == gatewaycircuit.ScopeKindProtocolModel {
		pinnedModel = strings.TrimSpace(state.Scope.ModelBucket)
	}
	return accountquality.ProbeRequest{
		AccountID:       identity.AccountID,
		SystemAccountID: systemAccountID,
		GroupID:         groupID,
		TrafficSource:   "runtime_recovery_probe",
		Full:            false,
		ProbeModel:      pinnedModel,
	}
}

// chainCircuitRecoveryTransportProbe 对齐契约源 circuitRecoveryTransportProbe：
// limited 诊断 + TransportProbeOutcomeFromResult 分类；任务失败返回
// unknown/task_failure（不计入账户失败证据）。
func chainCircuitRecoveryTransportProbe(ctx context.Context, service *accountprobe.Service, req accountquality.ProbeRequest) gatewaycircuit.TransportProbeOutcome {
	observation, err := service.ProbeAccountView(ctx, req)
	if err != nil || observation == nil {
		return gatewaycircuit.TransportProbeOutcome{Kind: gatewaycircuit.RecoveryProbeOutcomeUnknown, FailureKind: gatewaycircuit.RecoveryProbeFailureTaskFailure}
	}
	result := observation.Result
	snapshot := gatewaycircuit.ProbeResultSnapshot{
		Success:    result.Success,
		ErrorCode:  result.ErrorCode,
		Message:    result.Message,
		StatusCode: result.StatusCode,
	}
	if result.FirstTokenMS > 0 {
		firstToken := result.FirstTokenMS
		snapshot.FirstTokenMS = &firstToken
	}
	evidence := observation.Evidence
	var upstream *gatewaycircuit.UpstreamAttemptSnapshot
	if evidence.HasRealUpstreamAttempt {
		upstream = &gatewaycircuit.UpstreamAttemptSnapshot{
			IsReal:               true,
			IsCompletedReal:      evidence.UpstreamCompleted,
			TransportFailureKind: evidence.TransportFailureKind,
		}
		if evidence.UpstreamCompleted {
			status := evidence.UpstreamStatus
			upstream.Status = &status
		}
	}
	exhausted := evidence.TimedOut && evidence.HasRealUpstreamAttempt
	return gatewaycircuit.TransportProbeOutcomeFromResult(snapshot, upstream, evidence.Canceled, evidence.TimedOut, &exhausted)
}
