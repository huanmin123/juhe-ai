# BUG-0159 K6 legacybridge 前缀翻转缺少并发保护

## 基本信息

- 编号：BUG-0159
- 状态：待修复
- 严重程度：P1
- 发现时间：2026-09-04
- 发现方式：自查（已提交 Git 历史审计）
- 模块：后端 / 网关 / 迁移
- 关联计划：`docs/migration/Node全量清零迁移总计划-20260904.md`
- 关联 bug：无
- 责任人：待定

## 问题概述

- 现象：Go `legacybridge.Bridge` 的 `RegisterPrefix`、`RemovePrefix` 与 `ServeHTTP` 读写 prefix slice 没有互斥保护。
- 期望：入口翻转和并发请求期间路由表一致，不发生 data race、越界或请求落到半更新状态。
- 实际：运行时翻转与请求并发时会并发读写 slice，`go test -race` 若覆盖该时序可报竞态；现有测试未做并发 flip。
- 进一步事实：`ReverseProxy` 使用 `Rewrite` 仅 `SetURL`，没有调用 `SetXForwarded`；Rewrite 会先清除出站 `X-Forwarded-*`，Node 收不到原客户端 IP 链，`trust proxy` 下 `req.ip` 可能退化为 bridge 连接地址。
- 影响范围：切片 G4 翻转窗口可能出现请求选择错误、竞态崩溃或 Node/Go owner 短暂重叠。

## 根因与证据

- 文件：`backend-go/projects/gateway/internal/legacybridge/bridge.go`。
- `RegisterPrefix`/`RemovePrefix` 修改共享列表，`ServeHTTP` 同时遍历该列表，未使用 mutex 或不可变快照；代理重写也未恢复 X-Forwarded 链。
- 提交测试只覆盖顺序注册、删除和代理，不覆盖并发翻转。

## 修复与验证

- 修改点：使用读写锁或原子不可变路由快照，显式恢复受信任的 X-Forwarded 链，并补并发 register/remove/serve 的 race 与真实 client-IP 测试。
- 当前验证：未执行并发 race；目标 gateway 入口尚未完成正式切换。
- 结论：K6 翻转组件存在已确认的并发安全缺口，待修复。
