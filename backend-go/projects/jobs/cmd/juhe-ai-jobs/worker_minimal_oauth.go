package main

// minimal assembly 的 OAuth token 保活装配（D 任务①）。
//
// 背景：OAuth token 保活（openai-oauth-access-token-refresh）此前只装配在
// worker assembly（JUHE_AI_JOBS_WORKER_ENABLED 门控）；J1 账户健康有独立的
// minimal assembly 路径（ACCOUNT_HEALTH_ENABLED=1 且 worker 关闭，承载
// outbox 消费与 outcome 投影）。结果：开了 J1 但没开 worker 的部署里，
// OAuth 账户 token 过期后无自动续期。本文件把保活任务接进该路径。
//
// 单 owner/租约语义：
//   - 进程级单 owner 由 ownermode 承担（passive 副本根本不装配，见 main 的
//     runPassiveJobs 分支），本装配只在 owner 进程发生；
//   - 注册进 assembly 既有 scheduler（main 对 minimal assembly 同样追加
//     worker.components()，"worker scheduler" 组件承担运行与停机排空，
//     Close → closeStores 承担业务库句柄关闭），注册表条目的
//     OverlapCoalesceOne 防进程内重叠；
//   - worker 路径的 taskruns PG scheduled-lease 包裹层（withLease）在
//     minimal assembly 不装配（task-runs 家族属 worker 组合根）：防重叠
//     退化为 ownermode 单 owner + OverlapCoalesce，调度参数（间隔/抖动/
//     超时/退避）仍来自 jobregistry 冻结条目，与 worker 路径一致。

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobsched"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/oauthrefresh"
)

// wireMinimalOAuthRefresh 在 minimal assembly 下装配 OpenAI OAuth token
// 保活：复用 worker 路径 wireOAuthFamily 的同一构造（oauthrefresh.OpenStore +
// NewRefreshJob + HTTPTokenExchanger）与同一 env（JUHE_AI_SECRET、
// JUHE_AI_DATABASE_DRIVER、JUHE_AI_POSTGRES_URL / JUHE_AI_DATABASE_PATH、
// JUHE_AI_JOBS_OAUTH_ENABLED 家族开关），经 scheduleWiredJob 注册进既有
// scheduler。
//
// 依赖缺失不静默：家族关闭（OAuthEnabled=false）是显式配置，安静返回；
// 缺 Secret / 缺业务库路径按跳过并 warn（保活缺席但 J1 主体继续）；存储
// 打不开等硬失败返回 error，由 main 降级 warn（对齐 outbox 消费面装配失败
// 的降级语义，不阻塞启动）。
func (a *workerAssembly) wireMinimalOAuthRefresh() error {
	if !a.config.OAuthEnabled {
		return nil
	}
	if strings.TrimSpace(a.config.Secret) == "" {
		a.logger.Warn("minimal assembly OAuth token 保活未接线：缺少 JUHE_AI_SECRET（凭据解封不可用）",
			"event", "jobs_minimal_oauth_refresh_unwired", "missing", "JUHE_AI_SECRET")
		return nil
	}
	postgres := a.config.Driver == "postgres"
	var db *sql.DB
	if postgres {
		if strings.TrimSpace(a.config.PostgresURL) == "" {
			a.logger.Warn("minimal assembly OAuth token 保活未接线：PG 模式缺少 JUHE_AI_POSTGRES_URL",
				"event", "jobs_minimal_oauth_refresh_unwired", "missing", "JUHE_AI_POSTGRES_URL")
			return nil
		}
		handle, err := a.acquirePool(a.config.PostgresURL, "oauth-refresh-minimal")
		if err != nil {
			return err
		}
		db = handle.DB()
	} else {
		if strings.TrimSpace(a.config.BusinessSQLitePath) == "" {
			a.logger.Warn("minimal assembly OAuth token 保活未接线：SQLite 模式缺少 JUHE_AI_DATABASE_PATH",
				"event", "jobs_minimal_oauth_refresh_unwired", "missing", "JUHE_AI_DATABASE_PATH")
			return nil
		}
		var err error
		if db, err = a.openSQLite(a.config.BusinessSQLitePath, "oauth-refresh-minimal"); err != nil {
			return err
		}
	}
	mode := oauthrefresh.StoreSQLite
	if postgres {
		mode = oauthrefresh.StorePostgres
	}
	store, err := oauthrefresh.OpenStore(db, mode, a.config.Secret)
	if err != nil {
		return fmt.Errorf("open minimal oauth-refresh store: %w", err)
	}
	a.oauthStore = store
	// 任务闭包与 worker 路径 wireOAuthFamily 逐字一致（RefreshOptions 零值
	// 全量轮询）；调度参数经 jobregistry 冻结条目解析（minimal 无系统设置
	// 间隔源，取注册表默认）。
	refreshJob := oauthrefresh.NewRefreshJob(store, oauthrefresh.NewHTTPTokenExchanger(), oauthrefresh.WithLogger(a.logger))
	a.scheduleWiredJob("openai-oauth-access-token-refresh", func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		if _, err := refreshJob.RunOnce(taskCtx, oauthrefresh.RefreshOptions{}); err != nil {
			return jobsched.TaskResult{}, err
		}
		return jobsched.TaskResult{}, nil
	})
	a.logger.Info("minimal assembly 已接线 OAuth token 保活",
		"event", "jobs_minimal_oauth_refresh_wired",
		"job", "openai-oauth-access-token-refresh", "driver", a.config.Driver)
	return nil
}
