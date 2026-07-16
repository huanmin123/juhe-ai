# PLAN-0102 Responses Lite 请求契约修复

## 基本信息

- 编号：`PLAN-0102`
- 状态：设计已确认，待实现
- 创建时间：2026-07-16
- 需求来源：生产 2chat API Key 账户人工测试返回 `X-OpenAI-Internal-Codex-Responses-Lite requires reasoning.context to be all_turns`
- 执行分支：`codex/responses-lite-contract-hotfix`

## 问题与证据

- 生产成功样本 `traceId=e93a52c1-8c6e-45f7-90c9-390061ac5ecd` 使用 `gpt-5.6-sol`，客户端画像为 `codex / codex_responses`，HTTP 状态为 `200`。
- 该样本的客户端请求与实际上游请求均包含 `reasoning.context="all_turns"` 和 `parallel_tool_calls=false`，说明网关能够保留正确的 Lite 契约。
- 软件账户测试会构造 Codex Responses 请求，但当前 payload 没有 `reasoning.context`；API Key 兼容层又会为 Lite 模型添加内部 Lite header，并把缺省 `parallel_tool_calls` 设为 `true`。
- 因此严格校验 Lite 契约的上游会拒绝账户测试；这不是 API Key 失效，也不是模型目录需要过滤。

## 目标与边界

### 本次完成

- 只要网关决定发送 Responses Lite header，上游请求体必须同步满足 Lite 契约。
- `reasoning.context` 固定为 `all_turns`，同时保留已有 `reasoning.effort` 和 `reasoning.summary`。
- `parallel_tool_calls` 固定为 `false`。
- API Key 兼容路径、OAuth Codex 路径和软件账户测试共用同一契约判断。
- 非 Lite 模型保持当前行为，不新增模型过滤、版本拦截或其他特殊校验。

### 本次不做

- 不改变 `/models` 返回的模型集合。
- 不修改账户状态、调度、模型映射、计费或审计结构。
- 不为未知模型推断 Lite 能力。
- 本计划不直接授权提交到远程或生产发布。

## 方案比较与决策

1. 在共享 Codex Lite 契约层同步 header 与 body，API Key 和 OAuth 都调用该逻辑。优点是从根因保证不变量，影响面明确；采用此方案。
2. 只修改账户测试 payload。改动更小，但其他旧客户端或内部调用仍可能产生 header/body 不一致；不采用。
3. 账户测试删除 Lite header。可以绕过严格校验，但测试内容不再等价于真实 Lite 请求；不采用。

## 设计

- 以最终上游模型是否属于已登记 Lite 模型作为唯一判断，与现有 Lite header 判断保持同源。
- Lite 请求归一化时，把非对象 `reasoning` 视为缺省对象；对象值则浅复制后覆盖 `context="all_turns"`，不删除其他合法 reasoning 字段。
- Lite 请求无条件写入 `parallel_tool_calls=false`，不接受调用方的相反值继续上游。
- 非 Lite 请求不主动增加 `reasoning.context`，并保持现有并行工具默认行为。
- 账户测试仍走真实网关链路，不在测试服务里绕过 header 或上游校验。

## 执行拆解

- [ ] 先补 API Key Lite body 回归并确认按当前实现失败。
- [ ] 补 OAuth Lite body 回归并确认按当前实现失败。
- [ ] 补账户测试 payload / 真实网关边界回归并确认按当前实现失败。
- [ ] 实现共享 Lite body 契约并接入两条上游路径。
- [ ] 执行专项回归、后端类型检查和构建。
- [ ] 复核非 Lite 模型与既有 Codex 请求行为。

## 测试项

| 测试类型 | 测试项 | 预期结果 | 状态 |
| --- | --- | --- | --- |
| 回归 | API Key `gpt-5.6-sol` Codex Responses | Lite header、`context=all_turns`、`parallel_tool_calls=false` 同时存在 | 未执行 |
| 回归 | OAuth `gpt-5.6-sol` Codex Responses | 与 API Key 保持相同 Lite body 契约 | 未执行 |
| 回归 | 已有 reasoning 字段 | 保留 effort / summary，只收口 context | 未执行 |
| 回归 | 非 Lite 模型 | 不新增 context，不改变现有并行工具语义 | 未执行 |
| 回归 | 软件账户人工测试 | 严格 Lite 上游不再返回缺少 all_turns 错误 | 未执行 |
| 静态 | 后端 typecheck / build | 全部通过 | 未执行 |

## 验收标准

- 任一发往上游且包含 Responses Lite header 的请求，都同时包含 `reasoning.context="all_turns"` 和 `parallel_tool_calls=false`。
- API Key、OAuth、软件账户测试三条路径具有自动化回归。
- 非 Lite 模型请求行为不变。
- 不引入模型过滤、客户端版本限制或与本问题无关的协议改造。

## 风险与回退

- 风险集中在 Codex Responses 请求体归一化；通过 Lite 模型门禁和非 Lite 回归限制影响范围。
- 若上游出现不兼容，可回退本计划对应代码提交；无数据库、缓存或数据迁移回退要求。

## 进度记录

- 2026-07-16：完成生产成功 trace、当前账户测试构造器和最新 Codex 源码对比；用户确认采用共享契约根因修复方案。
