package main

// B5：gateway 侧 system_settings 跨进程版本写入适配。版本实体/id 约定收口在
// shared/platform/crossprocessstate（version:settings:sys_admin），gateway
// settings Store 在 PG 事务提交后调用；jobs 读取同一键判断是否重载 60s 快照。

import (
	"context"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/settings"
	"github.com/huanminabc/juhe-ai/backend-go-platform/crossprocessstate"
)

type gatewaySettingsVersionBumper struct {
	client *crossprocessstate.Client
}

func (b gatewaySettingsVersionBumper) BumpSettingsVersion(ctx context.Context) error {
	_, err := b.client.BumpSettingsVersion(ctx, settings.SystemSettingsAccountID)
	return err
}
