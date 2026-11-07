package main

// securitypolicies 组合根接线（API Key 安全策略与出站内容脱敏设计 §4.3，批A
// 实体 CRUD + 批B2 运行链接入）：镜像 compose_scheduling_preferences_wiring.go
// 先例——内部切片无外部端口适配器，全局运行态失效直接复用策略路由侧的同一
// K5 总线端口（routestrategies.RuntimeInvalidator，*inval.Bus 天然满足），本
// 文件固化装配顺序与失效注入理由，并提供可测试的挂载入口。
//
// 失效契约：策略 create/update/delete（含 rules 变更与删除解绑回内置）提交成
// 功后发布 topic:gateway_runtime_cache 全局失效。原因：绑定 Key 的出站脱敏行
// 为依赖策略 rules_json 内容——规则变更改变替换结果与占位符编号、删除解绑使
// Key 回落内置默认——必须让全部绑定 Key 下次加载重建快照（设计 §4.3：全局
// topic，不做逐 Key N+1 失效）。订阅方 gatewayruntimecache 对该主题任意
// reason 推进全局代际并强制重算，与调度偏好、策略路由侧同一机制。运行态快
//照的编译缓存（gatewayruntimecache/securitypolicy.go）以 rules_json 内容摘
// 要为键，重建快照后新内容自然命中新键，无需额外失效协调。
//
// 启动门禁裁决（任务契约核查项）：调度偏好无启动期内置行缺失校验先例
// （compose.go 无 EnsureBuiltin/启动校验臂，内置行缺失由读取链六态解析暴露
// ——偏好 NULL 绑定显式报错），安全策略同构跳过启动门禁；内置行缺失时 Key
// 绑定列 NULL/指向缺失行的行为已由 loadGatewayAPIKeyRow 的引用损坏显式上抛
// 覆盖（对齐调度偏好 P2 语义），不在组合根另加启动期检测。

import (
	"log/slog"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/securitypolicies"
)

// mountSecurityPolicies 装配安全策略双面 CRUD（admin
// /__aisys__/api/security-policies + self /__aisys__/api/my-security-policies）；
// Store 构造、日志 Sink 与 auth 依赖照 schedulingpreferences 的接线模式
// （compose.go 内构造 Store 时传入同一 bus）。protection 是删除清键侧面
// （写后缓存设计 §4.3.1；读侧走 PG 列不依赖 Redis；链条关闭时 nil，nil 安
// 全降级）。
func mountSecurityPolicies(kern *kernel.Kernel, store *securitypolicies.Store, auth *authsys.Deps, sink authsys.OperationLogSink, protection *securitypolicies.ProtectionCountStore) {
	(&securitypolicies.Deps{Store: store, Auth: auth, Sink: sink, Log: slog.Default(), Protection: protection}).Mount(kern)
}
