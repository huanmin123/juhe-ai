# BUG-0083 GPT 覆盖与 Bridge 字段保护偏离

## 基本信息

- 编号：BUG-0083
- 状态：已修复（待生产验证）
- 严重程度：P1
- 发现时间：2026-07-14
- 发现方式：会话设计对照
- 模块：GPT / 账户覆盖 / 模型目录 / 协议 bridge / 网关
- 关联计划：PLAN-0096
- 关联 bug：BUG-0071

## 问题概述

- GPT 多模型账户按能力并集 / 任一模型校验，运行时还会向下选择或静默清空覆盖，与“全部模型共同支持且精确生效”冲突。
- 新能力错误发生在网关归一化之外时可能成为通用 500。
- 重建型 Gemini bridge 会静默丢服务等级 / 思考控制，初版保护又误拒显式 null。
- 外部公开账号更新只校验请求中直接提交的字段，没有用最终凭据和最终模型重新校验后台已保存的 GPT 覆盖；仅改名称、Base URL 或模型列表时，可能保留与新模型不兼容的覆盖。Node 异步入口若只在写前读取并校验，还存在并发更新在校验后改变凭据或模型的窗口；SQLite 直接忽略 expected revision 会发生陈旧覆盖，PostgreSQL 只保护最终 UPDATE 又会让基于旧 revision 的校验错误直接返回。

## 根因与修复

- 前后端统一使用完整目录能力交集；缺目录或目标模型不精确支持时返回 account-scoped `account_request_override_unsupported`，不降级。
- API Key 和 OAuth driver 共用同一错误归一化入口。
- 无法保真映射的非 null 控制字段明确返回 400；null 作为 no-op 放行，不静默删除有效值。
- Node 和 Go 的公开账号更新均改为校验 `existing + payload` 的最终凭据与最终模型全集；公开接口仍不能设置或清除覆盖。Go 在账号行锁事务内完成校验和写入；Node SQLite 在同一同步事务内比较 expected revision，PostgreSQL 使用 `config_revision` 条件更新，陈旧校验或陈旧写入均最多三次完整重读、重建和重校验，耗尽后以专用错误返回 HTTP `409`。校验失败时凭据、模型和其他账号字段保持原子不变。

## 验证记录

- `test:account-gpt-request-overrides`（前后端）
- `test:openai-api-key-passthrough`
- `test:protected-request-control-bridges`
- `test:service-tier-billing`
- `test:gemini-gateway-mock-ai`
- `test:external-public-account-push-async-boundary`
- `test:external-source-auth`
- `go test ./internal/modules/publicaccounts ./internal/httpapi ./internal/app -count=1`
- `go test -race ./internal/modules/publicaccounts -count=1`

真实 PostgreSQL stale-revision smoke 已补测试代码，仍待具备 PostgreSQL / Docker 的环境执行；本机无 Docker 且 `127.0.0.1:5432` 不可达，不能把源码门禁和 SQLite 行为回归记录为真实 PostgreSQL 通过。

## 下次遇到

- 账户覆盖必须按全部支持模型取交集并精确应用，不能用排序等级向下选择。
- Bridge 重建 body 时，受保护字段只能明确转换、明确拒绝或无操作放行 null，不能静默丢失。
- 任何可改变账号最终凭据或模型集合的入口都必须重跑已保存覆盖校验；异步存储路径必须把校验依据绑定到同一 revision 或同一事务，不能只做无条件的“先读后写”。
