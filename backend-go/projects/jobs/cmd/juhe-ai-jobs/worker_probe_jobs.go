package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accountprobe"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accountquality"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobsched"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/proberepo"
)

// wireProbeFamily 把探针依赖族的三个任务翻转为 GoWired：
//   - account-quality-refresh（RefreshRunner：统计刷新 + 失败前置确认入队，
//     队列执行 PrecheckRunner：Prober/AccountReader/PrecheckMutation 由
//     proberepo + accountprobe 提供）；
//   - account-api-key-cooldown-retest（CooldownRetestRunner 扫描 + 异步队列，
//     CooldownCandidateSource/CooldownMutation 由 proberepo 提供）；
//   - normal-route-speed-first-recovery-probe（opsjobs.SpeedFirstProbeRunner，
//     SpeedFirstClaimStore 由 proberepo 的 Redis 降级运行态实现提供，与 Node
//     网关共用同一键空间，不引入第二 writer）。
//
// 探针上游调用在 jobs 侧直接发起（accountprobe 移植 Node 协议诊断栈的
// 后台探针窄路径），不经任何跨进程回调。
func (a *workerAssembly) wireProbeFamily(ctx context.Context) error {
	if !a.config.ProbeEnabled {
		return nil
	}
	business, err := openBusinessDB(a, "probe-family-business")
	if err != nil {
		return err
	}
	store, err := proberepo.NewStore(proberepo.Config{
		DB:       business.db,
		Postgres: business.postgres,
		Secret:   a.config.Secret,
	})
	if err != nil {
		_ = business.close()
		return err
	}
	a.addCloser(business.close)

	// 探针族辅助表幂等建表；核心业务表缺失时 fail closed 登记本族三个任务
	// disabled（对齐 balance-detect 的契约校验模式，不阻塞其他 job）。
	if err := store.EnsureSchema(ctx); err != nil {
		a.registerProbeFamilyDisabled("探针族 schema 初始化失败：" + err.Error())
		_ = business.close()
		return nil
	}
	if err := store.ValidateCoreTables(ctx); err != nil {
		a.registerProbeFamilyDisabled("业务库契约校验失败：" + err.Error())
		_ = business.close()
		return nil
	}

	probeService, err := accountprobe.NewService(accountprobe.Options{
		Source:      store,
		Client:      &http.Client{},
		Secret:      a.config.Secret,
		Concurrency: a.config.ProbeConcurrency,
	})
	if err != nil {
		return err
	}
	logger := probeFamilyLogger{logger: a.logger}
	settings := a.probeSettingsSource(business)
	concurrency := accountquality.QueueConcurrency(func() int { return a.config.ProbeConcurrency })

	// 质量统计存储（RefreshRunner 的 account_quality_* 读写）。
	statsConfig := accountquality.StatsStoreConfig{
		Business: probeBusinessMetaLookup{store: store},
		Clock:    accountquality.SystemClock{},
	}
	if a.config.Driver == "postgres" {
		handle, err := a.acquirePool(a.config.PostgresURL, "account-quality")
		if err != nil {
			return err
		}
		statsConfig.Mode = accountquality.StatsPostgres
		statsConfig.PostgresPool = handle
	} else {
		statsConfig.Mode = accountquality.StatsSQLite
		statsConfig.DatabasePath = a.config.StatsSQLitePath
	}
	statsStore, err := accountquality.OpenStatsStore(statsConfig)
	if err != nil {
		return err
	}
	// 与 stats 家族一致：EnsureSchema 幂等建表（CREATE TABLE IF NOT EXISTS）。
	if err := statsStore.EnsureSchema(ctx); err != nil {
		return err
	}

	precheck := accountquality.NewPrecheckRunner(accountquality.PrecheckDeps{
		Reader:      store,
		Prober:      probeService,
		Mutation:    store,
		Concurrency: a.config.ProbeConcurrency,
	})
	cooldown := accountquality.NewCooldownRetestRunner(accountquality.CooldownDeps{
		Reader:       store,
		Prober:       probeService,
		Candidates:   store,
		Mutation:     store,
		Settings:     settings,
		Concurrency:  concurrency,
		QueueWorkers: a.config.ProbeConcurrency,
	})
	refresh := accountquality.NewRefreshRunner(accountquality.RefreshDeps{
		Store:       statsStore,
		Settings:    settings,
		Logger:      logger,
		Precheck:    precheck,
		Concurrency: concurrency,
	})
	a.addCloser(func() error {
		precheck.StopAndDrain(a.config.DrainTimeout)
		cooldown.StopAndDrain(a.config.DrainTimeout)
		_ = statsStore.Close()
		return nil
	})

	a.scheduleWiredJob("account-quality-refresh", func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		return jobsched.TaskResult{}, refresh.Run(taskCtx)
	})
	a.scheduleWiredJob("account-api-key-cooldown-retest", func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		return jobsched.TaskResult{}, cooldown.Scan(taskCtx)
	})

	// 保存探针族句柄供账户电路族恢复解析复用，并接线账户电路族
	// （control-plane maintenance + circuit recovery）。
	a.probeRepoStore = store
	a.circuitProbeService = probeService
	if err := a.wireCircuitFamily(ctx, business, func(name, reason string) {
		a.registerDisabledJob(name, reason)
	}); err != nil {
		return err
	}

	// ---- normal-route-speed-first-recovery-probe ----
	redisConfig := proberepo.SpeedFirstRedisConfig{
		URL:       a.config.RedisStateURL,
		Namespace: a.config.RedisNamespace,
		Enabled:   a.config.RedisStateURL != "",
	}
	if !redisConfig.Enabled {
		a.registerDisabledJob("normal-route-speed-first-recovery-probe",
			"缺 JUHE_AI_REDIS_STATE_URL（速度优先降级运行态为 Redis 单实现，jobs 与 Node 网关共用键空间，无 Redis 时不得落库复制）")
		return nil
	}
	if !proberepo.ValidSpeedFirstNamespace(redisConfig.Namespace) {
		a.registerDisabledJob("normal-route-speed-first-recovery-probe",
			"JUHE_AI_REDIS_NAMESPACE 非法（须匹配 ^[A-Za-z0-9_.:-]{1,64}$），速度优先降级运行态键空间不可定位")
		return nil
	}
	speedFirstStore, err := proberepo.OpenSpeedFirstStore(redisConfig, nil)
	if err != nil {
		return err
	}
	a.addCloser(speedFirstStore.Close)
	speedFirstRunner, err := opsjobs.NewSpeedFirstProbeRunner(
		speedFirstStore,
		speedFirstCandidateSource{store: store},
		speedFirstProbeFunc(probeService),
		opsjobs.SpeedFirstProbeRunnerOptions{
			ClaimRenewInterval: 30 * time.Second,
			NowMS:              func() int64 { return time.Now().UnixMilli() },
		},
	)
	if err != nil {
		return err
	}
	queue := accountquality.NewRetryQueue[opsjobs.ProbeCandidate](
		"normal-route-speed-first-recovery-probe",
		a.config.ProbeConcurrency,
		accountquality.SystemClock{},
		logger,
		func(ctx context.Context, run accountquality.QueueRunContext, candidate opsjobs.ProbeCandidate) (bool, error) {
			return speedFirstRunner.Run(ctx, candidate)
		},
		func(event accountquality.RetryQueueEvent[opsjobs.ProbeCandidate]) {
			logger.Warn("background_normal_route_speed_first_recovery_probe_exhausted", map[string]any{
				"accountId":       event.Item.AccountID,
				"accountName":     event.Item.AccountName,
				"routeStrategyId": event.Item.Scope.RouteStrategyID,
				"groupId":         event.Item.Scope.GroupID,
				"attemptCount":    event.AttemptIndex + 1,
			}, "普通路由速度优先恢复探针已用尽，本轮保留降级状态等待下次探针")
		},
	)
	a.addCloser(func() error {
		queue.StopAndDrain(a.config.DrainTimeout)
		return nil
	})
	a.scheduleWiredJob("normal-route-speed-first-recovery-probe", func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		candidates, err := speedFirstStore.ListProbeCandidates(taskCtx, probeFamilyBatchSize)
		if err != nil {
			return jobsched.TaskResult{}, err
		}
		enqueuedCount := 0
		skippedQueuedCount := 0
		for _, candidate := range candidates {
			if queue.Enqueue(candidate.StateKey, candidate) {
				enqueuedCount++
			} else {
				skippedQueuedCount++
			}
		}
		if len(candidates) > 0 {
			snapshot := queue.Snapshot()
			logger.Info("background_normal_route_speed_first_recovery_probe_completed", map[string]any{
				"candidateCount":                 len(candidates),
				"enqueuedCount":                  enqueuedCount,
				"skippedQueuedCount":             skippedQueuedCount,
				"recoveryProbeQueueConcurrency":  a.config.ProbeConcurrency,
				"recoveryProbeQueuePendingCount": snapshot.PendingCount,
				"recoveryProbeQueueRunningCount": snapshot.RunningCount,
			}, "普通路由速度优先恢复探针候选已加入异步队列")
		}
		return jobsched.TaskResult{}, nil
	})
	return nil
}

// registerProbeFamilyDisabled 一次性登记探针族三个任务与账户电路族两个任务
// disabled（电路族复用探针族业务库/探针服务，schema/契约失败时一并 fail closed）。
func (a *workerAssembly) registerProbeFamilyDisabled(reason string) {
	a.registerDisabledJob("account-quality-refresh", reason)
	a.registerDisabledJob("account-api-key-cooldown-retest", reason)
	a.registerDisabledJob("normal-route-speed-first-recovery-probe", reason)
	a.registerDisabledJob("account-circuit-control-plane-maintenance", "同探针族失败："+reason)
	a.registerDisabledJob("account-circuit-recovery", "同探针族失败："+reason)
}

// probeFamilyBatchSize 对齐 Node normalRouteSpeedFirstRecoveryProbeBatchSize
// （JUHE_AI_BACKGROUND_NORMAL_ROUTE_SPEED_FIRST_RECOVERY_PROBE_BATCH_SIZE 默认 10）。
const probeFamilyBatchSize = 10

// parseRFC3339Millis 等价 rfc3339InstantMilliseconds（错误文案与 Node 一致）。
func parseRFC3339Millis(value string) (int64, error) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("速度优先恢复探针 accountExpiresAt 必须是带 Z 或数值 offset 的 RFC3339 时间：%s", value)
	}
	return parsed.UnixMilli(), nil
}

// probeSettingsNumber 已由 a.probeSettingsSource（worker_settings.go）替换：
// 经 internal/jobssettings 读取 system_settings（DEFAULT_SYSTEM_SETTINGS：
// accountQualityWindowMinutes=10、cooldownAccountRetestMaxBackoffHours=12），
// 读取失败按 accountquality.SettingsNumber 的无错误签名回落默认值并 warn。

// probeBusinessMetaLookup 适配 proberepo.Store 为 accountquality.BusinessLookup。
type probeBusinessMetaLookup struct{ store *proberepo.Store }

func (l probeBusinessMetaLookup) LoadAccountMetadataByIds(ctx context.Context, ids []string) (map[string]accountquality.AccountMetadata, error) {
	return l.store.LoadAccountMetadataByIds(ctx, ids)
}

// speedFirstCandidateSource 适配 proberepo.Store 为 opsjobs.SpeedFirstCandidateSource。
type speedFirstCandidateSource struct{ store *proberepo.Store }

func (s speedFirstCandidateSource) FindAccountForTest(ctx context.Context, accountID, systemAccountID string) (*opsjobs.SpeedFirstAccountSummary, error) {
	view, err := s.store.LoadAccountForTest(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if view == nil {
		return nil, nil
	}
	summary := &opsjobs.SpeedFirstAccountSummary{
		Status:           view.Status,
		Schedulable:      view.Schedulable,
		AccountExpiresAt: view.AccountExpiresAt,
	}
	if view.AccountExpiresAt != "" {
		expiresAtMS, err := parseRFC3339Millis(view.AccountExpiresAt)
		if err != nil {
			return nil, err
		}
		summary.ExpiresAtMS = &expiresAtMS
	}
	if view.HasEffectiveAvail {
		available := view.EffectiveAvailable
		summary.EffectiveAvailable = &available
	}
	_ = systemAccountID
	return summary, nil
}

func (s speedFirstCandidateSource) FindCandidateAccount(ctx context.Context, groupID, accountID, systemAccountID string) (*opsjobs.ProbeAccountRef, error) {
	candidate, err := s.store.LoadAccountForGroup(ctx, groupID, accountID, systemAccountID)
	if err != nil {
		return nil, err
	}
	if candidate == nil {
		return nil, nil
	}
	return &opsjobs.ProbeAccountRef{AccountID: candidate.ID, GroupID: groupID}, nil
}

// speedFirstProbeFunc 组装速度优先探针闭包：完整分级诊断 + 传输证据分类。
func speedFirstProbeFunc(service *accountprobe.Service) func(ctx context.Context, account *opsjobs.SpeedFirstAccountSummary, candidate opsjobs.ProbeCandidate, candidateAccount *opsjobs.ProbeAccountRef) (opsjobs.ProbeResultSnapshot, opsjobs.TransportProbeOutcome) {
	return func(ctx context.Context, _ *opsjobs.SpeedFirstAccountSummary, candidate opsjobs.ProbeCandidate, _ *opsjobs.ProbeAccountRef) (opsjobs.ProbeResultSnapshot, opsjobs.TransportProbeOutcome) {
		observation, err := service.ProbeAccountView(ctx, accountquality.ProbeRequest{
			AccountID:       candidate.AccountID,
			SystemAccountID: candidate.Scope.SystemAccountID,
			GroupID:         candidate.Scope.GroupID,
			TrafficSource:   "runtime_recovery_probe",
			Full:            true,
		})
		if err != nil || observation == nil {
			message := "探针任务失败"
			if err != nil {
				message = err.Error()
			}
			return opsjobs.ProbeResultSnapshot{Success: false, Message: message},
				opsjobs.TransportProbeOutcome{Kind: opsjobs.ProbeOutcomeUnknown, FailureKind: opsjobs.ProbeFailureTaskFailure}
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
		outcome := opsjobs.TransportProbeOutcomeFromResult(snapshot, upstream, evidence.Canceled, evidence.TimedOut, &exhausted)
		return snapshot, outcome
	}
}

// probeFamilyLogger 适配 slog 为 accountquality.Logger。
type probeFamilyLogger struct {
	logger interface {
		Debug(msg string, args ...any)
		Info(msg string, args ...any)
		Warn(msg string, args ...any)
		Error(msg string, args ...any)
	}
}

func (l probeFamilyLogger) Debug(event string, fields map[string]any, message string) {
	l.logger.Debug(message, slogFields(event, fields)...)
}

func (l probeFamilyLogger) Info(event string, fields map[string]any, message string) {
	l.logger.Info(message, slogFields(event, fields)...)
}

func (l probeFamilyLogger) Warn(event string, fields map[string]any, message string) {
	l.logger.Warn(message, slogFields(event, fields)...)
}

func (l probeFamilyLogger) Error(event string, fields map[string]any, message string) {
	l.logger.Error(message, slogFields(event, fields)...)
}

func slogFields(event string, fields map[string]any) []any {
	args := make([]any, 0, len(fields)*2+2)
	args = append(args, "event", event)
	for key, value := range fields {
		args = append(args, key, value)
	}
	return args
}
