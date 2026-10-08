package main

// upstream-client-version-refresh 组合根适配器（客户端版本自动跟版设计
// §5/§6/§8.3）。串行拉取五族官方发布源（含第二期收尾追加的 grokCLI），按
// 写者侧单调规则幂等 upsert 自动键 upstreamClientVersionAutoOverrides，并
// 即时注入进程内自动层。直连、无代理。单轮刷新入口同时存入装配字段
// upstreamClientVersionRefresh，供 Grok 426 版本门触发器即时调用
// （worker_xai_grok_usage.go）。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobsched"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobssettings"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/versiontracking"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamidentity"
)

const (
	upstreamClientVersionRefreshJob       = "upstream-client-version-refresh"
	upstreamClientVersionAutoOverridesKey = "upstreamClientVersionAutoOverrides"
	// systemSettingsAccountID 对齐 gateway/jobssettings 的 SYSTEM_SETTINGS_ACCOUNT_ID。
	systemSettingsAccountID = "sys_admin"
	// settingsUpdatedAtLayout 对齐 gateway settings/store.go upsert 的毫秒 UTC 文本。
	settingsUpdatedAtLayout = "2006-01-02T15:04:05.000Z07:00"
)

// wireVersionTrackingFamily 装配 upstream-client-version-refresh。业务库句柄
// 缺失时登记 disabled，不阻塞其他 job。
func (a *workerAssembly) wireVersionTrackingFamily(_ context.Context) error {
	name := upstreamClientVersionRefreshJob
	if a.config.Driver != "postgres" && a.config.BusinessSQLitePath == "" {
		a.registerDisabledJob(name, "缺业务库路径（自动版本键写入不可用）")
		return nil
	}
	business, err := openBusinessDB(a, "version-tracking-business")
	if err != nil {
		return err
	}
	a.addCloser(business.close)
	source := jobssettings.NewSource(jobssettings.Options{
		DB:   business.db,
		Mode: settingsMode(business.postgres),
		Warn: jobssettingsWarn(a.logger),
	})
	client := &http.Client{Timeout: versiontracking.SourceTimeout}
	refresh := func(ctx context.Context) error {
		_, runErr := versiontracking.Refresh(ctx, versiontracking.Deps{
			HTTP: client,
			Read: source.UpstreamClientVersionAutoOverrides,
			Write: func(ctx context.Context, next map[string]string) error {
				return upsertUpstreamClientVersionAutoOverrides(ctx, business, next)
			},
			Apply:   upstreamidentity.SetClientVersionAutoOverrides,
			BuiltIn: upstreamidentity.BuiltInClientVersion,
			Now:     func() time.Time { return time.Now().UTC() },
			Logger:  a.logger,
		})
		return runErr
	}
	// Grok 426 版本门触发器消费该入口（设计 §8.3）。nil 安全：本家族登记
	// disabled 时字段保持 nil，xai worker 侧跳过触发；两家族装配顺序无关
	//（xai worker 先于本家族装配，持有的是运行期延迟解引用本字段的闭包）。
	a.upstreamClientVersionRefresh = refresh
	a.scheduleWiredJob(name, func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		if runErr := refresh(taskCtx); runErr != nil {
			return jobsched.TaskResult{}, runErr
		}
		return jobsched.TaskResult{}, nil
	})
	return nil
}

// upsertUpstreamClientVersionAutoOverrides 幂等写入自动键全量。PG 用
// juhe_business. 前缀与 $n，SQLite 用裸表名与 ?；updated_at 为毫秒 UTC 文本，
// 与 gateway settings/store.go 一致。
func upsertUpstreamClientVersionAutoOverrides(ctx context.Context, business *businessDB, next map[string]string) error {
	if business == nil || business.db == nil {
		return fmt.Errorf("自动版本键写入缺少业务库句柄")
	}
	payload := map[string]string{}
	if next != nil {
		payload = next
	}
	valueJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("编码自动版本键失败: %w", err)
	}
	updatedAt := time.Now().UTC().Format(settingsUpdatedAtLayout)
	query := business.bind(`INSERT INTO ` + business.table("system_settings") + `
		(system_account_id, key, value_json, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(system_account_id, key) DO UPDATE SET
			value_json = excluded.value_json,
			updated_at = excluded.updated_at`)
	if _, err := business.db.ExecContext(ctx, query, systemSettingsAccountID, upstreamClientVersionAutoOverridesKey, string(valueJSON), updatedAt); err != nil {
		return fmt.Errorf("写入自动版本键失败: %w", err)
	}
	return nil
}
