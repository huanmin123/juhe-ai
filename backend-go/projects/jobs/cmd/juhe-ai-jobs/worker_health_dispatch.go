package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/circuitstore"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/internalapi"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/proberepo"
)

// 账户健康检查派发（jobs internalapi 对网关桥
// gateway/cmd/juhe-ai-gateway/chain_request_failure_health.go 的对端装配）。
//
// 消费链（对齐归档 Node internal-api/account-health-check-dispatch.service.ts
// 的发布语义）：签名请求 → internalapi.DispatchAccountHealthCheckWithOutcome
// （Boundary 读账户 J1 冻结事实 → 范围内发布 signed request file 供 J1 Runner
// 消费；范围外静默跳过并结算 source fence = unknown）。派发线不写
// account_health_jobs_input_outbox（该表是授权 fanout / 账户删除清理线的
// snapshot 输入，Node 权威源发布的是 request 文件）。
//
// J1 未启用 / 配置缺失时派发能力保持未装配（Dispatch nil → 503 显式拒绝，
// 不伪装受理）；网关桥把非 202 一律按 rejected 显式降级并告警。

// healthDispatchProbeDeadlineFallback 对齐 Node
// integerConfig('JUHE_AI_BACKGROUND_ACCOUNT_HEALTH_CHECK_PROBE_DEADLINE_MS',
// 65_000, 1_000, 10 * 60_000)。
const (
	healthDispatchProbeDeadlineFallback = int64(65_000)
	healthDispatchProbeDeadlineMinimum  = int64(1_000)
	healthDispatchProbeDeadlineMaximum  = int64(10 * 60_000)
)

// healthDispatchRuntime 是派发闭包的已装配依赖；available=false 表示派发
// 能力不可用（503）。
type healthDispatchRuntime struct {
	available bool
	options   internalapi.HealthCheckDispatchOptions
}

// healthCheckDispatchOptions 组装健康检查派发 handler 配置。装配失败不阻塞
// worker 启动：派发是网关反向入口，失败保持 503 显式拒绝并告警（J1 Runner
// 侧对同一配置另行 fail-loud）。
func (a *workerAssembly) healthCheckDispatchOptions(getenv func(string) string) internalapi.HealthCheckDispatchRouterOptions {
	options := internalapi.HealthCheckDispatchRouterOptions{Secret: a.config.Secret}
	runtime, closer, err := a.buildHealthDispatchRuntime(getenv)
	if closer != nil {
		a.addCloser(closer)
	}
	if err != nil {
		a.logger.Warn("账户健康检查派发装配失败，派发请求将显式拒绝",
			"event", "account_health_check_dispatch_assembly_failed", "error", err.Error())
		return options
	}
	if !runtime.available {
		return options
	}
	options.Dispatch = func(ctx context.Context, accountID, reason, traceID string, sourceFence *internalapi.HealthCheckSourceFence) (internalapi.HealthCheckDispatchOutcome, error) {
		return internalapi.DispatchAccountHealthCheckWithOutcome(ctx, accountID, reason, traceID, sourceFence, nil, runtime.options)
	}
	return options
}

// buildHealthDispatchRuntime 解析派发依赖：J1 输入配置（目录/签名密钥）、
// 业务库账户事实读取（Boundary）、source fence 结算（Redis 电路运行态）。
func (a *workerAssembly) buildHealthDispatchRuntime(getenv func(string) string) (healthDispatchRuntime, func() error, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	config, err := accounthealth.LoadConfig(getenv)
	if err != nil {
		return healthDispatchRuntime{}, nil, err
	}
	if !config.Enabled {
		// jobs 未拥有 J1（合法状态）：派发显式 input_unavailable，不发布请求。
		return healthDispatchRuntime{}, nil, nil
	}
	deadlineMS, ok := healthDispatchProbeDeadlineMS(getenv)
	if !ok {
		return healthDispatchRuntime{}, nil, errors.New("JUHE_AI_BACKGROUND_ACCOUNT_HEALTH_CHECK_PROBE_DEADLINE_MS 必须是 [1000, 600000] 内的整数")
	}
	business, err := openBusinessDB(a, "health-dispatch-boundary")
	if err != nil {
		return healthDispatchRuntime{}, nil, err
	}
	runtime := healthDispatchRuntime{
		available: true,
		options: internalapi.HealthCheckDispatchOptions{
			InputRoot:       config.InputDirectory,
			SigningKey:      strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY")),
			ProbeDeadlineMS: deadlineMS,
			NowMS:           func() int64 { return time.Now().UnixMilli() },
			Boundary:        healthDispatchBoundary{business: business},
		},
	}
	settler, settlerCloser, settlerErr := a.healthDispatchSourceFenceSettler()
	if settlerErr != nil {
		_ = business.close()
		return healthDispatchRuntime{}, nil, settlerErr
	}
	runtime.options.SettleSourceFence = settler
	return runtime, func() error {
		var firstErr error
		if settlerCloser != nil {
			if err := settlerCloser(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		if err := business.close(); err != nil && firstErr == nil {
			firstErr = err
		}
		return firstErr
	}, nil
}

// healthDispatchSourceFenceSettler 装配 source fence 结算：复用账户电路运行
// 态的 Redis 键空间（与 worker_circuit_jobs.go 同 URL/namespace 约定）。Redis
// 未配置或 namespace 非法时返回 nil settler（fence 不结算，Node 中该失败
// 亦为 warn 语义），closer 为 nil。
func (a *workerAssembly) healthDispatchSourceFenceSettler() (internalapi.SourceFenceSettler, func() error, error) {
	if a.healthFenceSettler != nil {
		return a.healthFenceSettler, nil, nil
	}
	if strings.TrimSpace(a.config.RedisStateURL) == "" || !proberepo.ValidSpeedFirstNamespace(a.config.RedisNamespace) {
		a.logger.Warn("账户健康检查派发的 source fence 结算不可用（缺 JUHE_AI_REDIS_STATE_URL 或 namespace 非法）",
			"event", "account_health_check_dispatch_fence_settler_unavailable")
		return nil, nil, nil
	}
	store, err := circuitstore.NewProbeStateStore(a.config.RedisStateURL, a.config.RedisNamespace, nil)
	if err != nil {
		return nil, nil, err
	}
	settler := func(ctx context.Context, fence internalapi.HealthCheckSourceFence, state string) error {
		_, err := store.SettleDispatchedBySourceFence(ctx, fence.RuntimeKey, fence.ProbeGeneration, circuitstore.ProbeSourceFence{
			StateKey:         fence.StateKey,
			AccountID:        fence.AccountID,
			SourceGeneration: fence.SourceGeneration,
			SourceFenceID:    fence.SourceFenceID,
		}, state, nil)
		return err
	}
	return settler, store.Close, nil
}

// healthDispatchBoundary 是 internalapi.HealthCheckBoundary 的业务库实现：
// 对齐 Node account-health-jobs-input.repository.ts（config/dispatch revision
// 正整数断言）与 account-health-jobs-input-version.repository.ts（当前 input
// epoch）。任一事实缺失或非法即 ok=false（账户不在 J1 冻结范围）。完整账户
// 资格链（协议/状态/绑定）由消费端 runExplicitRequest 的 input_stale 判定
// 兜底，此处不复制网关域读取链。
type healthDispatchBoundary struct{ business *businessDB }

func (b healthDispatchBoundary) CurrentProbeInput(ctx context.Context, accountID string) (internalapi.HealthCheckAccountRef, int64, internalapi.HealthCheckRevisions, bool, error) {
	var configRevision, dispatchRevision sql.NullInt64
	err := b.business.db.QueryRowContext(ctx, b.bind("SELECT config_revision, dispatch_revision FROM "+b.business.table("accounts")+" WHERE id = ? AND deleted_at IS NULL"), accountID).Scan(&configRevision, &dispatchRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return internalapi.HealthCheckAccountRef{}, 0, internalapi.HealthCheckRevisions{}, false, nil
	}
	if err != nil {
		return internalapi.HealthCheckAccountRef{}, 0, internalapi.HealthCheckRevisions{}, false, err
	}
	if configRevision.Int64 < 1 || dispatchRevision.Int64 < 1 {
		return internalapi.HealthCheckAccountRef{}, 0, internalapi.HealthCheckRevisions{}, false, nil
	}
	var inputVersion sql.NullInt64
	err = b.business.db.QueryRowContext(ctx, b.bind("SELECT current_version FROM "+b.business.table("account_health_jobs_input_versions")+" WHERE account_id = ?"), accountID).Scan(&inputVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return internalapi.HealthCheckAccountRef{}, 0, internalapi.HealthCheckRevisions{}, false, nil
	}
	if err != nil {
		return internalapi.HealthCheckAccountRef{}, 0, internalapi.HealthCheckRevisions{}, false, err
	}
	if inputVersion.Int64 < 1 {
		return internalapi.HealthCheckAccountRef{}, 0, internalapi.HealthCheckRevisions{}, false, nil
	}
	return internalapi.HealthCheckAccountRef{
			ID:               accountID,
			ConfigRevision:   configRevision.Int64,
			DispatchRevision: dispatchRevision.Int64,
		}, inputVersion.Int64, internalapi.HealthCheckRevisions{
			ConfigRevision:   configRevision.Int64,
			DispatchRevision: dispatchRevision.Int64,
		}, true, nil
}

// bind 在 PostgreSQL 方言下把 ? 占位符改写为 $n（对齐 oauthrefresh.Store.bind）。
func (b healthDispatchBoundary) bind(query string) string {
	if !b.business.postgres {
		return query
	}
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + strconv.Itoa(index))
			index++
			continue
		}
		out.WriteByte(query[i])
	}
	return out.String()
}

// healthDispatchProbeDeadlineMS 解析 J1 探针 deadline 窗口（毫秒）。
func healthDispatchProbeDeadlineMS(getenv func(string) string) (int64, bool) {
	raw := strings.TrimSpace(getenv("JUHE_AI_BACKGROUND_ACCOUNT_HEALTH_CHECK_PROBE_DEADLINE_MS"))
	if raw == "" {
		return healthDispatchProbeDeadlineFallback, true
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < healthDispatchProbeDeadlineMinimum || value > healthDispatchProbeDeadlineMaximum {
		return 0, false
	}
	return value, true
}
