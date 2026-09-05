package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accountprobe"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accountquality"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/circuitstore"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobsched"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/proberepo"
)

// wireCircuitFamily 把账户电路族的两个任务翻转为 GoWired：
//   - account-circuit-control-plane-maintenance（opsjobs.ControlPlaneMaintenance：
//     CircuitStore 由 circuitstore 的 Redis 同键实现提供，Ledger/Outbox 由
//     circuitstore 的业务库双模适配器提供）；
//   - account-circuit-recovery（opsjobs.CircuitRecoveryService：同一 CircuitStore，
//     恢复探针目标解析走 proberepo 账户域读取链 + accountprobe limited 诊断）。
//
// 账户电路运行态是 Redis 单实现（与 Node/Go 网关同键空间、同一 Lua 状态机，
// 见 internal/circuitstore/store.go）：缺 JUHE_AI_REDIS_STATE_URL 或命名空间
// 非法时本族 fail closed 登记 disabled，不落库复制。
//
// account-list-availability-projection-maintenance 保持 disabled：投影仓储
// （ListAvailabilityRepo + overlay 对账）已迁移，但 LoadItems 物化载荷
// （AccountListItem 由网关域组装）仍无 jobs 侧等价实现（见
// worker_partial_jobs.go 缺口清单）。
func (a *workerAssembly) wireCircuitFamily(ctx context.Context, business *businessDB, registerDisabled func(name, reason string)) error {
	if a.config.RedisStateURL == "" {
		registerDisabled("account-circuit-control-plane-maintenance",
			"缺 JUHE_AI_REDIS_STATE_URL（账户电路运行态为 Redis 单实现，jobs 与 Node/Go 网关共用键空间，无 Redis 时不得落库复制）")
		registerDisabled("account-circuit-recovery", "同 account-circuit-control-plane-maintenance：缺 JUHE_AI_REDIS_STATE_URL")
		return nil
	}
	if !proberepo.ValidSpeedFirstNamespace(a.config.RedisNamespace) {
		registerDisabled("account-circuit-control-plane-maintenance",
			"JUHE_AI_REDIS_NAMESPACE 非法（须匹配 ^[A-Za-z0-9_.:-]{1,64}$），账户电路运行态键空间不可定位")
		registerDisabled("account-circuit-recovery", "同 account-circuit-control-plane-maintenance：JUHE_AI_REDIS_NAMESPACE 非法")
		return nil
	}
	redisStore, err := circuitstore.NewRedisStore(circuitstore.RedisStoreOptions{
		RedisURL:  a.config.RedisStateURL,
		Namespace: a.config.RedisNamespace,
		Capacity:  a.config.CircuitCapacity(),
	})
	if err != nil {
		return err
	}
	a.addCloser(redisStore.Close)
	store := circuitstore.NewOpsJobsStore(redisStore)

	// ---- account-circuit-control-plane-maintenance ----
	controlPlaneRepo, err := circuitstore.NewControlPlaneRepo(circuitstore.ControlPlaneConfig{
		DB:       business.db,
		Postgres: business.postgres,
	})
	if err != nil {
		return err
	}
	if err := controlPlaneRepo.EnsureCursorSchema(ctx); err != nil {
		registerDisabled("account-circuit-control-plane-maintenance", "控制面 schema 初始化失败："+err.Error())
		registerDisabled("account-circuit-recovery", "同 account-circuit-control-plane-maintenance：控制面 schema 初始化失败")
		_ = redisStore.Close()
		return nil
	}
	cursorStore, err := circuitstore.NewReconcileCursorStore(circuitstore.ControlPlaneConfig{
		DB:       business.db,
		Postgres: business.postgres,
	})
	if err != nil {
		return err
	}
	ownerID := fmt.Sprintf("circuit-bridge:%s:%d", a.config.InstanceID, a.config.WorkerReplicaIdx)
	maintenance, err := opsjobs.NewControlPlaneMaintenance(store, controlPlaneRepo, controlPlaneRepo, opsjobs.ControlPlaneOptions{
		OwnerID:     ownerID,
		NowMS:       func() int64 { return time.Now().UnixMilli() },
		CursorStore: cursorStore,
	})
	if err != nil {
		return err
	}
	a.scheduleWiredJob("account-circuit-control-plane-maintenance", func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		// Node runGatewayAccountCircuitControlPlaneMaintenance(limit=100)。
		if _, err := maintenance.RunMaintenance(taskCtx, 100); err != nil {
			return jobsched.TaskResult{}, err
		}
		return jobsched.TaskResult{}, nil
	})

	// ---- account-circuit-recovery ----
	resolver := circuitRecoveryTargetResolver{store: a.probeRepoStore, probe: a.circuitProbeService}
	recovery, err := opsjobs.NewCircuitRecoveryService(store, resolver.Resolve, opsjobs.CircuitRecoveryServiceOptions{
		Concurrency: a.config.ProbeConcurrency,
		NowMS:       func() int64 { return time.Now().UnixMilli() },
	})
	if err != nil {
		return err
	}
	a.scheduleWiredJob("account-circuit-recovery", func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		result, err := recovery.Sweep(taskCtx)
		if err != nil {
			return jobsched.TaskResult{Outcome: jobsched.OutcomePartial, Warning: fmt.Sprintf("due=%d framingComplete=%d transportIncomplete=%d unknown=%d fenced=%d skipped=%d",
				result.DueCount, result.FramingCompleteCount, result.TransportIncompleteCount, result.UnknownCount, result.FencedCount, result.SkippedCount)}, err
		}
		return jobsched.TaskResult{}, nil
	})
	return nil
}

// circuitRecoveryTargetResolver 适配 proberepo 账户读取链 + accountprobe 为
// opsjobs.CircuitRecoveryTargetResolver（对齐 Node
// createScheduledAccountCircuitRecoveryResolver：owner/authorized 身份 →
// find_account_for_test → find_openai_account_for_group（ignoreAvailability）
// → dispatch revision 围栏 → limited 诊断探针）。
//
// 与 Node 的已知差异（登记为限制，不做伪装）：
//  1. Node 按 gatewayAccountRuntimeKey(candidate) 复核运行态键与 scope 一致；
//     proberepo.CandidateAccount 未暴露 accessType/绑定上下文，无法重建授权键，
//     该复核退化为查询参数一致性（identity 派生自同一 runtime key），配合
//     store 侧 dispatch revision CAS 围栏兜底；
//  2. Node 探针针对 protocol_model scope 的 modelBucket 指定模型；
//     accountquality.ProbeRequest 无模型钉住参数，走账户健康检查模型。
type circuitRecoveryTargetResolver struct {
	store *proberepo.Store
	probe *accountprobe.Service
}

func (r circuitRecoveryTargetResolver) Resolve(ctx context.Context, state opsjobs.CircuitState) (opsjobs.CircuitRecoveryProbeTarget, bool, error) {
	if err := ctx.Err(); err != nil {
		return opsjobs.CircuitRecoveryProbeTarget{}, false, err
	}
	identity, ok := opsjobs.ParseRecoveryRuntimeIdentity(state.Scope.AccountRuntimeKey)
	if !ok {
		return opsjobs.CircuitRecoveryProbeTarget{}, false, nil
	}
	account, err := r.store.LoadAccountForTest(ctx, identity.AccountID)
	if err != nil {
		return opsjobs.CircuitRecoveryProbeTarget{}, false, err
	}
	if account == nil {
		return opsjobs.CircuitRecoveryProbeTarget{}, false, nil
	}
	groupID := account.BoundGroupID
	systemAccountID := account.SystemAccountID
	if identity.Kind == "authorized" {
		groupID = identity.GroupID
		systemAccountID = identity.SystemAccountID
	}
	if strings.TrimSpace(groupID) == "" || strings.TrimSpace(systemAccountID) == "" {
		return opsjobs.CircuitRecoveryProbeTarget{}, false, nil
	}
	candidate, err := r.store.LoadAccountForGroup(ctx, groupID, identity.AccountID, systemAccountID)
	if err != nil {
		return opsjobs.CircuitRecoveryProbeTarget{}, false, err
	}
	if candidate == nil {
		return opsjobs.CircuitRecoveryProbeTarget{}, false, nil
	}
	if !candidate.HasDispatchRevision || candidate.DispatchRevision <= 0 {
		return opsjobs.CircuitRecoveryProbeTarget{}, false, nil
	}
	target := opsjobs.CircuitRecoveryProbeTarget{
		DispatchRevision: fmt.Sprintf("%d", candidate.DispatchRevision),
	}
	target.Probe = func(probeCtx context.Context) (opsjobs.TransportProbeOutcome, error) {
		if probeCtx.Err() != nil {
			return opsjobs.TransportProbeOutcome{Kind: opsjobs.ProbeOutcomeUnknown, FailureKind: opsjobs.ProbeFailureCanceled}, nil
		}
		return circuitRecoveryTransportProbe(probeCtx, r.probe, identity, state, groupID, systemAccountID), nil
	}
	return target, true, nil
}

// circuitRecoveryTransportProbe 对齐 Node runAccountCircuitRecoveryTransportProbe：
// limited 诊断 + transportProbeOutcomeFromAccountTestResult 分类；任务失败
// 返回 unknown/task_failure（不计入账户失败证据）。
func circuitRecoveryTransportProbe(ctx context.Context, service *accountprobe.Service, identity opsjobs.RecoveryRuntimeIdentity, state opsjobs.CircuitState, groupID, systemAccountID string) opsjobs.TransportProbeOutcome {
	observation, err := service.ProbeAccountView(ctx, accountquality.ProbeRequest{
		AccountID:       identity.AccountID,
		SystemAccountID: systemAccountID,
		GroupID:         groupID,
		TrafficSource:   "runtime_recovery_probe",
		Full:            false,
	})
	if err != nil || observation == nil {
		return opsjobs.TransportProbeOutcome{Kind: opsjobs.ProbeOutcomeUnknown, FailureKind: opsjobs.ProbeFailureTaskFailure}
	}
	result := observation.Result
	snapshot := opsjobs.ProbeResultSnapshot{
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
	var upstream *opsjobs.UpstreamAttemptSnapshot
	if evidence.HasRealUpstreamAttempt {
		upstream = &opsjobs.UpstreamAttemptSnapshot{
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
	return opsjobs.TransportProbeOutcomeFromResult(snapshot, upstream, evidence.Canceled, evidence.TimedOut, &exhausted)
}
