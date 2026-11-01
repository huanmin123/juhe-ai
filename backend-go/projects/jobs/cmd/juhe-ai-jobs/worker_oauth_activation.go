package main

// 账户可用性排期同步的激活 hook 装配（P0 修复）：定时窗口启用账号时，在
// 同一事务内推进账户 circuit dispatch revision 家族（Node 归档
// advanceAccountCircuitDispatchRevisionFamilyInTransaction 的 Go 装配），
// 解除网关 dispatch revision 门控；nil hook 只翻状态、门控不解除。
// worker_assembly.go 的 wireOAuthFamily 经本构造注入。

import (
	"context"
	"database/sql"
	"log/slog"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/oauthrefresh"
)

// newAccountScheduleActivationHook 装配排期激活 hook：复用 J1 投影的家族推进
// 实现（accounthealth.ScheduleActivationDispatch，与状态翻转同一事务——调用
// 方传入 oauthrefresh 同步事务本身，不新建连接池）。推进失败 best-effort：
// 只记 warn 不中断同步（状态翻转保持生效，与组合根既有 best-effort 失效
// 语义对齐；门控解除的缺口由下一次激活事件或手动 patch 补齐）。
func newAccountScheduleActivationHook(business *accounthealth.ProjectionBusinessDB, logger *slog.Logger) oauthrefresh.ActivationHook {
	if logger == nil {
		logger = slog.Default()
	}
	return oauthrefresh.ActivationHookFunc(func(ctx context.Context, tx *sql.Tx, accountID, _ string) error {
		if err := accounthealth.ScheduleActivationDispatch(ctx, business, tx, accountID, nil); err != nil {
			logger.Warn("账户排期激活的 circuit dispatch revision 家族推进失败（状态翻转保持生效，不中断同步）",
				"event", "account_schedule_activation_dispatch_failed",
				"error", err.Error(),
				"accountId", accountID)
		}
		return nil
	})
}

// newAPIKeyScheduleActivationHook 装配 API Key 排期激活 hook（B3-H11）：激活
// 翻转时在同一事务重建该 Key 的小时额度窗口作用域绑定，提交后给 statsagg
// 打脏。与账户 hook 的 best-effort 不同，绑定写失败必须上抛回滚整笔同步：
// 绑定表是小时窗口的唯一来源，置为 active 却缺少绑定会让小时额度静默
// fail-open（本缺陷本身）；任务失败可见并可下一轮重试。打脏仍按 gateway
// 语义在提交后 best-effort 执行（见 oauthrefresh.APIKeyScheduleActivationHook）。
func newAPIKeyScheduleActivationHook(store *oauthrefresh.Store, logger *slog.Logger) oauthrefresh.APIKeyActivationHook {
	return store.APIKeyScheduleActivationHook(logger)
}
