package main

// B5：jobs 侧 system_settings 跨进程版本适配。gateway/jobs 写设置后递增
// version:settings:sys_admin；jobssettings.Source 读取同一版本并在变化时清空
// 60s 键缓存。未配置 Redis 时返回 nil，保持既有 60s 窗口。

import (
	"context"
	"log/slog"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobssettings"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/proberepo"
	"github.com/huanminabc/juhe-ai/backend-go-platform/crossprocessstate"
	"github.com/huanminabc/juhe-ai/backend-go-platform/rediscfg"
)

// settingsCrossProcessState 在已缓存的 OAuth 跨进程 Redis 客户端上派生独立
// settings 域客户端：版本键与 OAuth 锁/凭据/状态域隔离，但不额外开连接池。
func (a *workerAssembly) settingsCrossProcessState() (*crossprocessstate.Client, error) {
	_, redisClient, err := a.oauthCrossProcessState()
	if err != nil || redisClient == nil {
		return nil, err
	}
	return crossprocessstate.NewRedis(redisClient,
		rediscfg.DomainNamespace(a.config.RedisNamespace, rediscfg.DomainSettings))
}

// accountStateVersionBumper 返回 proberepo 可用的账户状态版本端口；未配置
// Redis 时 nil（保持既有窗口）。
func (a *workerAssembly) accountStateVersionBumper() proberepo.AccountStateVersionBumper {
	client, _, err := a.oauthCrossProcessState()
	if err != nil || client == nil {
		return nil
	}
	return oauthRuntimeStateBumper{client: client}
}

// settingsVersionReader 返回读取 system_settings 版本的函数；nil 表示未接线。
func (a *workerAssembly) settingsVersionReader() func(context.Context) (int64, error) {
	client, err := a.settingsCrossProcessState()
	if err != nil {
		if a.logger != nil {
			a.logger.Warn("settings 版本读取未接线，保持 60s 窗口",
				"event", "settings_version_reader_unavailable", "error", err)
		}
		return nil
	}
	if client == nil {
		return nil
	}
	return func(ctx context.Context) (int64, error) {
		readCtx, cancel := crossprocessstate.ReadContext(ctx)
		defer cancel()
		return client.SettingsVersion(readCtx, jobssettings.SystemSettingsAccountID)
	}
}

// bumpSettingsVersion 在 system_settings 提交后递增共享版本；失败只告警。
func (a *workerAssembly) bumpSettingsVersion(ctx context.Context) {
	client, err := a.settingsCrossProcessState()
	if err != nil || client == nil {
		return
	}
	bumpCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if _, err := client.BumpSettingsVersion(bumpCtx, jobssettings.SystemSettingsAccountID); err != nil {
		slog.Warn("settings 跨进程版本写入失败，gateway 按既有 60s 窗口回退",
			"event", "settings_version_bump_failed", "error", err)
	}
}
