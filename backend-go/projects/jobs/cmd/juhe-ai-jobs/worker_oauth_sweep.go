package main

// worker_oauth_sweep.go：resource-authorization-expiry-sweep 的组合根副作用
// 装配（T6d）。
//
// Node 过期授权 sweep（expireDueResourceAuthorizationsAsync）的下游副作用分两段：
//  1. 事务内（每个翻转的 grant）：syncResourceAuthorizationGrantRuntimeAsync =
//    runtime sync（resource_authorizations 投影 + effective source 重算）+
//    quota 窗口 scope bindings + account-health-jobs 输入 fanout。其中仅
//    fanout 是 jobs 侧可移植的同库写入（oauthrefresh 包移植）；runtime sync
//    与 scope bindings 属 gateway authz sync 域（gateway internal/authz，
//    跨 module 不可 import）。
//  2. 事务后（expired>0 一次）：refreshAfterResourceAuthorizationBusinessWrite
//    Async('authorization_expired') = markAllGroupAccountStatsDirty(reason) +
//    网关缓存族失效。脏标记由本组合根直写（statsverify store）；网关缓存族
//    （gateway api-key validation cache / group account ids cache / runtime
//    cache / quota cache 失效）归 gateway 进程，Node 经 worker→server IPC
//    与进程内钩子触达，该通道按 Go 总设计消灭，Go 侧由 gateway 读路径的
//    expires_at 门禁兜底（chain_accounts activeResourceAuthorization* 均带
//    expires_at > now 判定）。
//
// 选型（Node 等价优先，登记不静默）：不新建 jobs→gateway 任务表——gateway
// 当前没有 authz 交接表的消费端，先建表会留下无人消费的死数据；改为
// 「fanout 直写 + 脏标记直写 + 每轮 sweep 显式 warn 交接缺口」，并在
// jobregistry 的 resource-authorization-expiry-sweep GoBinding 冻结登记，
// 待 gateway 侧接入 authz 交接消费时收敛。

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/oauthrefresh"
)

// authorizationGrantHealthFanout 把 oauthrefresh.Store 适配为
// oauthrefresh.GrantFinalizer：事务内执行 Node 可移植的健康任务输入 fanout
// （kind='snapshot'，reason='authorization_grant_changed'）。
type authorizationGrantHealthFanout struct {
	store *oauthrefresh.Store
}

// FinalizeExpiredGrant 实现 oauthrefresh.GrantFinalizer（sweep 事务内调用）。
func (f authorizationGrantHealthFanout) FinalizeExpiredGrant(ctx context.Context, tx *sql.Tx, grant oauthrefresh.ResourceAuthorizationGrant, _ string) error {
	_, err := f.store.EnqueueAccountHealthInputsForAuthorizationSourceTx(
		ctx, tx, grant.ResourceType, grant.ResourceID, oauthrefresh.AuthorizationGrantHealthFanoutReason)
	return err
}

// groupStatsDirtyMarker 是 statsverify.Store 全量脏标记的窄 port（可 Mock）。
type groupStatsDirtyMarker interface {
	MarkAllGroupAccountStatsDirty(ctx context.Context, reason string, now time.Time) error
}

// markAllGroupAccountStatsAfterAuthzWrite 对齐 Node
// refreshGroupAccountStatsAfterWriteAsync({all:true, reason})：marker 为 nil
// （stats 家族未装配，statsverify store 缺席）时显式 warn——Node 由
// db-service 单进程承担该写入，Go jobs 只在 StatsEnabled 时持有写入目标——
// 不静默跳过。
func markAllGroupAccountStatsAfterAuthzWrite(ctx context.Context, logger *slog.Logger, marker groupStatsDirtyMarker, reason string) error {
	if marker == nil {
		logger.Warn("background_resource_authorization_stats_dirty_handoff_skipped",
			"reason", reason,
			"missing", "stats 家族未装配（statsverify store 缺席），全量脏标记无写入目标")
		return nil
	}
	if err := marker.MarkAllGroupAccountStatsDirty(ctx, reason, time.Now()); err != nil {
		return err
	}
	logger.Info("background_resource_authorization_stats_dirty_marked", "reason", reason, "scope", "all")
	return nil
}
