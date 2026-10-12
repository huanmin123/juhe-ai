package main

// schedulingpreferences 组合根接线（调度偏好独立实体 PLAN-20261012T033000000Z
// P1，接口契约「调度偏好」章）：内部切片无外部端口适配器——全局运行态失效直
// 接复用策略路由侧的同一 K5 总线端口（routestrategies.RuntimeInvalidator，
// *inval.Bus 天然满足），本文件固化装配顺序与失效注入理由，并提供可测试的
// 挂载入口。
//
// 失效契约：偏好 create/update/delete（含启停）提交成功后发布
// topic:gateway_runtime_cache 全局失效（reason
// scheduling_preference_created/updated/deleted）。原因：绑定 Key 的合成配置
// 依赖偏好内容/状态——speed 参数变更产生新指纹（v2 运行态键）、停用回退内置
// 行、删除使绑定失效——必须让全部绑定 Key 下次加载重建快照。订阅方
// gatewayruntimecache 对该主题任意 reason 推进全局代际并强制重算（reason 只
// 区分 settings / model-catalog），与策略路由侧 route_strategy_* reason 同一
// 机制、不做逐 Key N+1 失效（接口契约「失效契约」章）。

import (
	"log/slog"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/schedulingpreferences"
)

// mountSchedulingPreferences 装配调度偏好双面 CRUD（admin
// /__aisys__/api/scheduling-preferences + self /__aisys__/api/
// my-scheduling-preferences）；Store 构造、日志 Sink 与 auth 依赖照
// routestrategies 的接线模式（compose.go 内构造 Store 时传入同一 bus）。
func mountSchedulingPreferences(kern *kernel.Kernel, store *schedulingpreferences.Store, auth *authsys.Deps, sink authsys.OperationLogSink) {
	(&schedulingpreferences.Deps{Store: store, Auth: auth, Sink: sink, Log: slog.Default()}).Mount(kern)
}
