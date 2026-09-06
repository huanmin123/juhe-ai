package main

import (
	"context"
	"fmt"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/circuitstore"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobsched"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/proberepo"
)

// wireListProjectionFamily 把 account-list-availability-projection-maintenance
// 翻转为 GoWired：opsjobs.RunListAvailabilityMaintenance 的 LoadItems 物化
// 载荷由 circuitstore.ProjectionItemLoader 提供（同源 SQL 双模 + Redis
// 运行态读面，对照 Node loadProjectedAccountListItems）。
//
// 依赖门禁（逐项登记 disabled，不静默跳过）：
//   - JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_ENABLED：Node
//     runtimeConfig.background 默认 false，不启用不注册（Node background-jobs.ts:334）；
//   - databaseDriver=postgres：Node PostgreSQL-only 物化器，非 PG 返回空结果，
//     SQLite 分支不注册（account-list-availability-projection.service.ts）；
//   - JUHE_AI_REDIS_STATE_URL + 合法 namespace：concurrency/runtime
//     availability 为 Redis 单实现（同 Node 网关键空间）；
//   - JUHE_AI_SECRET：apiKeyRuntime 汇总需凭据解密与 Key 指纹；
//   - 探针族（proberepo 凭据解码）与 OAuth 族（schedule 边界同步）已装配。
//
// 登记差异（不构成静默降级）：payload 的
// isAccountBalanceSnapshotSuppressed 依赖网关进程内清理协调器内存态；jobs
// 进程无该组件，等价于 Node 协调器空状态（恒 false）。
func (a *workerAssembly) wireListProjectionFamily(ctx context.Context, business *businessDB, probeStore *proberepo.Store) error {
	const jobName = "account-list-availability-projection-maintenance"
	if !a.config.ListProjectionEnabled {
		return nil
	}
	if a.config.Driver != "postgres" {
		a.registerDisabledJob(jobName,
			"Node PostgreSQL-only 物化器：databaseDriver != postgres 时 runAccountListAvailabilityProjectionMaintenance 返回空结果，默认/SQLite 分支不注册（account-list-availability-projection.service.ts）")
		return nil
	}
	if a.config.RedisStateURL == "" {
		a.registerDisabledJob(jobName,
			"缺 JUHE_AI_REDIS_STATE_URL（账户并发与运行态可用性为 Redis 单实现，jobs 与 Node/Go 网关共用键空间，无 Redis 时不得落库复制）")
		return nil
	}
	if !proberepo.ValidSpeedFirstNamespace(a.config.RedisNamespace) {
		a.registerDisabledJob(jobName,
			"JUHE_AI_REDIS_NAMESPACE 非法（须匹配 ^[A-Za-z0-9_.:-]{1,64}$），运行态键空间不可定位")
		return nil
	}
	if a.config.Secret == "" {
		a.registerDisabledJob(jobName,
			"缺 JUHE_AI_SECRET（apiKeyRuntime 汇总需凭据解密与 Key 指纹，fail closed）")
		return nil
	}
	if probeStore == nil {
		a.registerDisabledJob(jobName,
			"探针族未装配（凭据解码器不可用），LoadItems 物化载荷缺 apiKeyRuntime 读面")
		return nil
	}
	if a.oauthStore == nil {
		a.registerDisabledJob(jobName,
			"OAuth 族未装配（account-availability-schedule 状态同步不可用），投影认领前无法应用调度边界转移")
		return nil
	}

	// Redis 运行态读面：overlay（concurrency-v2 同键 Lua）+ recovery probe
	// 状态机/policy avoidance 只读（与 Node 网关同键空间）。
	overlayStore, err := circuitstore.NewOverlayRedisStore(circuitstore.OverlayRedisConfig{
		URL:       a.config.RedisStateURL,
		Namespace: a.config.RedisNamespace,
	}, nil)
	if err != nil {
		return err
	}
	a.addCloser(overlayStore.Close)
	runtimeReader, err := circuitstore.NewRuntimeStateReader(a.config.RedisStateURL, a.config.RedisNamespace, nil)
	if err != nil {
		return err
	}
	a.addCloser(runtimeReader.Close)

	repo, err := circuitstore.NewListAvailabilityRepo(circuitstore.ListAvailabilityConfig{
		DB:       business.db,
		Postgres: business.postgres,
	})
	if err != nil {
		return err
	}
	loader, err := circuitstore.NewProjectionItemLoader(circuitstore.ProjectionLoadConfig{
		Business:            business.db,
		Stats:               business.db,
		Postgres:            business.postgres,
		StatsPostgres:       business.postgres,
		Secret:              a.config.Secret,
		Credentials:         projectionCredentials{store: probeStore},
		Concurrency:         circuitstore.NewOverlayConcurrencySource(overlayStore),
		RuntimeAvailability: runtimeReader,
		Timezone:            statsTimezoneSource{store: a.statsStore},
	})
	if err != nil {
		return err
	}
	maintenance := opsjobs.ListAvailabilityOptions{
		OwnerID:           fmt.Sprintf("list-projection:%s:%d", a.config.InstanceID, a.config.WorkerReplicaIdx),
		BatchSize:         a.config.ListProjectionBatchSize,
		MaxBatchesPerRun:  a.config.ListProjectionMaxBatchesPerRun,
		WorkerConcurrency: a.config.ListProjectionWorkerConcurrency,
		NowMS:             func() int64 { return time.Now().UnixMilli() },
		Repo:              repo,
		RuntimeProbe:      projectionRuntimeProbe{concurrency: circuitstore.NewOverlayConcurrencySource(overlayStore), runtime: runtimeReader},
		Overlays:          circuitstore.NewOverlayReconciler(overlayStore, repo),
		LoadItems:         loader.LoadItems,
		SyncSchedules: func(syncCtx context.Context, nowMS int64) error {
			// Node syncAccountAvailabilityScheduleStatusesAsync(now)：
			// 投影认领前应用全部到期调度边界转移。
			_, syncErr := a.oauthStore.SyncAccountScheduleStatuses(syncCtx, time.UnixMilli(nowMS).UTC(), 0, nil)
			return syncErr
		},
	}
	intervalOverride := time.Duration(a.config.ListProjectionIntervalMS) * time.Millisecond
	a.scheduleWiredJobWithSettings(jobName, func(name string) (time.Duration, bool) {
		if name == jobName {
			return intervalOverride, true
		}
		return 0, false
	}, func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		// driverPostgres 恒 true：非 PG 分支已在装配门禁登记 disabled。
		if _, err := opsjobs.RunListAvailabilityMaintenance(taskCtx, maintenance, true); err != nil {
			return jobsched.TaskResult{}, err
		}
		return jobsched.TaskResult{}, nil
	})
	return nil
}

// projectionRuntimeProbe 对齐 Node probeAccountRuntimeState（合成键探活，
// 任一运行态读失败即 fail closed）。
type projectionRuntimeProbe struct {
	concurrency circuitstore.ConcurrencySource
	runtime     circuitstore.RuntimeAvailabilitySource
}

func (p projectionRuntimeProbe) Probe(ctx context.Context) (runtimeAvailabilityAvailable, concurrencyAvailable bool, err error) {
	if _, err := p.runtime.LoadRuntimeAvailability(ctx, []string{"__account_list_projection_runtime_probe__"}); err != nil {
		return false, false, err
	}
	if _, err := p.concurrency.LoadConcurrency(ctx, []string{"__account_list_projection_concurrency_probe__"}); err != nil {
		return false, false, err
	}
	return true, true, nil
}

// projectionCredentials 适配 proberepo.Store 的凭据解码与 Key 池提取。
type projectionCredentials struct{ store *proberepo.Store }

func (p projectionCredentials) DecryptCredentials(envelope string) (map[string]any, error) {
	return p.store.DecryptCredentials(envelope)
}

func (p projectionCredentials) AccountAPIKeyEntries(credentials map[string]any) []circuitstore.APIKeyPoolEntry {
	entries := p.store.AccountAPIKeyEntries(credentials)
	output := make([]circuitstore.APIKeyPoolEntry, 0, len(entries))
	for _, entry := range entries {
		output = append(output, circuitstore.APIKeyPoolEntry{
			ID: entry.ID, Key: entry.Key, Fingerprint: entry.Fingerprint,
			Index: entry.Index, Weight: entry.Weight,
		})
	}
	return output
}
