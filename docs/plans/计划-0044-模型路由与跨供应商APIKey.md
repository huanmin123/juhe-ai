# PLAN-0044 模型路由与跨供应商 API Key

## 基本信息

- 编号：PLAN-0044
- 状态：进行中
- 创建时间：2026-06-11
- 更新时间：2026-06-22
- 需求来源：用户对话
- 执行者：AI / 维护者
- 关联模块：前端 / 后端 / 存储 / 网关 / 模型目录 / API Key / 文档 / 验证

## 需求目标

- 背景：未来一个本地 API Key 需要同时使用 GPT、GLM、CC 等多个供应商号池，不能再依赖“同一个 Key 只绑定同一供应商协议档案”的限制。
- 目标：以客户端请求里的 `model` 作为供应商路由键，先解析模型归属，再从当前 API Key 绑定的同供应商协议档案分组中选择号池。
- 交付物：模型名全局唯一规则、模型路由索引、API Key 跨供应商分组绑定、网关模型路由、前端配置体验、回归验证和文档同步。

## 范围边界

### 本次包含

- [ ] 明确全系统客户端模型名唯一：不按供应商、scope 或用户维度放宽，同名直接拒绝。
- [ ] 调整全局 / 个人自定义模型保存校验，管理员新增全局模型时也要拒绝撞已有个人模型。
- [ ] 建立模型路由事实：`model -> providerCode + providerProtocolProfileId + upstreamModel`。
- [x] 在网关 runtime cache 为当前 API Key 绑定供应商集合构建 `modelKey -> providerCode[]` 索引，同一供应商集合热命中按模型名 O(1) 查询。
- [ ] 构建全系统模型路由事实索引，禁止请求链路扫描全部供应商目录。
- [x] API Key 允许绑定多个供应商协议档案的分组，但请求调度必须先通过模型路由筛出目标档案。
- [x] 更新 API Key 前端绑定号池体验，不再按第一个分组的协议档案隐藏其他供应商分组。
- [ ] 补充回归测试覆盖模型重名拒绝、模型路由命中、无对应号池、跨供应商 Key 调度和旧单供应商 Key 回归。

### 本次不包含

- 不做智能模型选择、成本路由、质量路由或自动降级；请求模型就是用户显式路由意图。
- 不做模糊匹配、前缀匹配或按客户端特征猜供应商。
- 不做跨协议转换；只有同一客户端协议入口下可被本地 adapter 支持的供应商档案才能参与。
- 不自动从上游 `/models` 同步模型；模型目录仍由内置目录和自定义模型维护。
- 不把账号级模型映射提前到供应商路由阶段；模型映射仍在选中账号后改写上游 `model`。
- 智谱 GLM 第一版接入不依赖全量 O(1) 模型路由索引完成；当前 API Key 已允许跨供应商或跨 GLM profile 混绑，但模型名全系统唯一和紧凑路由索引仍按本计划继续推进。

## 关联文档

- 架构文档：`docs/architecture/架构总览.md`
- 功能文档：`docs/functions/自定义模型与模型映射设计.md`
- 功能文档：`docs/functions/APIKey多分组路由设计.md`
- 功能文档：`docs/functions/账户模型限制设计.md`
- 功能文档：`docs/functions/请求处理分层设计.md`
- 功能文档：`docs/functions/模型价格与用量统计口径.md`
- 功能文档：`docs/functions/接口契约与权限矩阵.md`
- 存储说明：`docs/functions/SQLite存储说明.md`
- 验证手册：`docs/develop/测试与验证说明.md`

## 方案概述

### 模型名唯一性

客户端可请求模型名是网关路由键，必须在全系统唯一。唯一性不按供应商、scope 或用户维度放宽：

- 内置模型之间不能重名。
- 全局自定义模型不能和内置模型、已有全局模型、任何个人模型重名。
- 个人自定义模型不能和内置模型、全局模型、任何用户的个人模型重名。
- 大小写、首尾空白归一后判断重复。
- 重名不能通过目录合并去重解决；保存时拒绝，脏数据由离线清洗或重建处理。

这样可以避免“用户先添加个人模型，管理员后添加同名全局模型，最终全局模型把用户模型去重挤掉”的风险。

### 模型路由索引

目标状态下，网关运行时维护可按模型名直接命中的全系统索引：

```ts
type ModelRouteIndex = {
  exact: Map<string, ModelRouteTarget>
}

type ModelRouteTarget = {
  providerCode: string
  providerProtocolProfileId: string
  upstreamModel: string
}
```

热路径只做：

```text
normalize(model) -> exact.get(model) -> providerProtocolProfileId
```

索引构建或失效来源：

- 内置供应商模型目录变化。
- 全局自定义模型创建、更新、停用或删除。
- 个人自定义模型创建、更新、停用或删除。
- 供应商协议档案启停。

索引构建可以在 DB service 或网关 runtime cache 层完成，请求链路只能读取已经构建好的紧凑快照，不能每次请求遍历全部供应商模型。

当前已落地的中间态是 API Key 供应商集合索引：网关 runtime cache 按 `systemAccountId + includeUnpriced + sorted(providerCodes)` 构建 `modelKey -> providerCode[]`。同一供应商集合首次请求会读取各供应商模型目录构建索引；后续相同供应商集合和模型只做 `Map.get(modelKey)`，不再读取模型目录。它解决当前普通跨供应商 Key 的热路径 O(1) 查询，但不替代后续全系统唯一模型名和 `providerProtocolProfileId` 级全局路由事实。

### API Key 跨供应商号池

API Key 绑定分组时不再要求所有分组 `provider_protocol_profile_id` 一致。当前运行时先使用有界模型路由：在该 Key 已绑定供应商范围内用供应商集合索引唯一命中目标档案，或用账号支持模型 / 模型映射显式命中跨档案目标；后续再把这一步替换为全系统模型路由索引。

1. 校验 API Key 和调用方。
2. 解析请求体 `model`。
3. 用模型路由索引得到目标 `providerProtocolProfileId`。
4. 从 API Key active 绑定中筛出同 `providerProtocolProfileId` 的分组。
5. 在目标分组集合内按 API Key 路由策略生成候选顺序。
6. 进入现有分组内账号调度、账号模型限制和账号级模型映射。

如果模型不存在、模型停用、模型不可请求或当前 API Key 没有绑定目标供应商分组，直接返回本地可读错误，不猜测供应商。

### 数据变化

- 可优先通过后端写入校验保证 `custom_provider_models.model` 全系统唯一。
- 后续可增加表达式唯一索引或规范化模型名字段来兜底，例如 `model_key = lower(trim(model))`。
- 不在运行路径做旧数据兼容；已有重名数据属于数据异常，按当前 schema 离线清洗。

### 前端变化

- 模型目录页保存前提示“模型 ID 全系统唯一”，保存失败展示后端返回的中文冲突原因。
- API Key 表单的分组选项不再在选择第一个分组后隐藏其他供应商分组。
- API Key 表单需要展示每个绑定分组的供应商、协议档案和分组类型。
- API Key 详情或提示中说明：跨供应商 Key 由请求 `model` 决定实际号池。

## 执行拆解

- [x] 记录方案计划和长期文档初稿。
- [ ] 梳理当前模型目录合并和自定义模型保存校验。
- [ ] 调整模型名唯一性校验和必要索引。
- [x] 新增当前 API Key 供应商集合模型路由 runtime index。
- [ ] 新增全系统模型路由事实索引。
- [x] 修改 API Key 绑定校验，允许跨供应商协议档案分组。
- [x] 修改网关分组选路顺序：先做有界模型路由，再分组策略。
- [x] 修改前端 API Key 绑定号池筛选和提示。
- [ ] 补充后端回归、前端表单验证和文档验证记录。

## 测试项

| 测试类型 | 测试项 | 验证方式 / 命令 | 预期结果 | 状态 | 实际结果或备注 |
| --- | --- | --- | --- | --- | --- |
| 命令类验证 | 后端类型检查 | `pnpm --filter juhe-ai-backend typecheck` | 后端类型检查通过 | 已通过 | 2026-06-22 通过 |
| 命令类验证 | 前端类型检查 | `pnpm --filter juhe-ai-frontend typecheck` | 前端类型检查通过 | 已通过 | 2026-06-22 通过 |
| 命令类验证 | 构建验证 | `pnpm build` | 前后端构建通过 | 未执行 | 待实现后执行 |
| 功能主流程 | 跨供应商 API Key 按模型路由 | 绑定 GPT 与 DeepSeek 分组，请求 DeepSeek 模型 | 请求命中模型所属供应商分组 | 已通过 | `pnpm --filter juhe-ai-backend test:api-key-group-route-capability` |
| 性能边界 | 跨供应商模型路由热命中 O(1) | 连续两次用同一 Key 请求 DeepSeek 模型 | 第二次不再读取模型目录，DB service 操作次数不增加 | 已通过 | `pnpm --filter juhe-ai-backend test:api-key-group-route-cache` |
| 异常与边界 | 全局模型撞个人模型 | 先创建个人模型，再由管理员创建同名全局模型 | 管理员保存被拒绝，个人模型不被覆盖 | 未执行 | 待实现 |
| 异常与边界 | 用户模型撞全局模型 | 用户创建同名个人模型 | 保存被拒绝 | 未执行 | 待实现 |
| 异常与边界 | 模型存在但 Key 未绑定目标供应商 | 请求未绑定供应商模型 | 返回本地可读错误，不进入账号调度 | 未执行 | 待实现 |
| 回归场景 | 单供应商 API Key | 只绑定 GPT 分组并请求 GPT 模型 | 行为保持现有语义 | 已通过 | `pnpm --filter juhe-ai-backend test:api-key-group-route-cache`、`pnpm --filter juhe-ai-backend test:api-key-route-validation` |
| 前端体验 | API Key 绑定多个供应商分组 | 表单分组绑定回归 | 分组选择、错误提示和展示文案均为中文 | 已通过 | `pnpm --filter juhe-ai-frontend test:api-key-group-bindings` |

## 进度记录

| 日期 | 状态 | 记录人 | 进展 / 决策 / 阻塞 |
| --- | --- | --- | --- |
| 2026-06-11 | 草稿 | AI | 根据对话创建计划，先落地模型路由和模型名唯一性方案文档，不改运行代码。 |
| 2026-06-22 | 部分落地 | AI | 放开普通 API Key 跨供应商协议档案分组绑定；前端分组选项不再按首个档案过滤；网关普通路由按请求 `model` 在当前 Key 绑定范围内做有界目标档案收窄；当前 Key 绑定供应商集合已支持热命中 O(1)。全系统唯一模型名和全局模型路由事实仍待实现。 |

## 决策记录

| 日期 | 决策 | 原因 | 影响 |
| --- | --- | --- | --- |
| 2026-06-11 | 客户端模型名全系统唯一 | 模型名将作为供应商路由键，覆盖式去重会导致路由不确定 | 自定义模型保存必须做全系统冲突校验 |
| 2026-06-11 | 模型路由先于 API Key 分组策略 | 先选分组再做模型过滤会在跨供应商 Key 下误选供应商 | 网关调度顺序需要调整 |
| 2026-06-11 | 账号级模型映射不参与供应商路由 | 映射依赖已选账号，提前参与会形成循环依赖 | 路由只看目录模型名，选中账号后再做上游模型改写 |

## 验收标准

- [ ] 模型名在全系统维度唯一，新增和编辑都能拒绝冲突。
- [x] 网关在当前 API Key 绑定供应商集合内按请求 `model` 热命中 O(1) 解析目标供应商。
- [ ] 网关按全系统模型路由事实 O(1) 解析目标供应商协议档案。
- [x] API Key 能绑定多个供应商分组，并按模型只调度目标供应商分组。
- [ ] 模型缺失、模型不可请求、未绑定目标供应商分组时返回清晰错误。
- [ ] 单供应商 API Key、多分组主备、轮询和权重策略回归不退化。
- [ ] 文档、接口契约、存储说明和验证记录同步完成。

## 验证记录

- 类型检查：已执行，命令：`pnpm --filter juhe-ai-backend typecheck`、`pnpm --filter juhe-ai-frontend typecheck`
- 构建：未执行，命令：`pnpm build`
- 单项验证：已执行，命令：`pnpm --filter juhe-ai-backend test:api-key-multi-group-bindings`、`pnpm --filter juhe-ai-backend test:api-key-group-route-capability`、`pnpm --filter juhe-ai-backend test:api-key-route-validation`、`pnpm --filter juhe-ai-backend test:api-key-group-route-cache`、`pnpm --filter juhe-ai-frontend test:api-key-group-bindings`
- 手动验证：未执行。
- 测试项结果：已通过，说明：跨供应商 API Key 调度、当前 Key 供应商集合模型路由热命中 O(1)、普通 Key 保存层、前端表单体验和单供应商路由回归已覆盖；模型名唯一和全系统模型路由事实仍待实现。
- 未验证项：全系统模型路由事实索引、模型名全系统唯一约束、无对应号池本地错误。

## 风险与注意事项

- 已有数据库如果存在重名模型，需要上线前离线清洗；运行时代码不保留重名兼容分支。
- 模型名全系统唯一会限制不同用户使用同名私有 deployment；如确需表达，应让用户用不同客户端模型名，再通过账号级模型映射改写到各自上游 deployment。
- `/v1/models` 返回的模型目录应和模型路由事实保持一致，不能返回无法路由的公开模型。
- 缓存失效必须覆盖模型目录和 API Key 绑定两类变化；模型索引失效只由模型事实变化触发，API Key 绑定变化只影响候选分组，不需要重建模型索引。

## 完成总结

- 完成时间：待补充
- 实际完成内容：待补充
- 主要改动位置：待补充
- 验证结果：待补充
- 后续建议：待补充
