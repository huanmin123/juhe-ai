# GPT 请求服务等级与思考级别覆盖设计

> 状态：核心实现完成，待生产数据库同步、真实上游与 Codex Ultra 验证。
>
> 调研日期：2026-07-10。
>
> 本文定义 GPT 供应商 AI 账户高级配置中的请求级覆盖能力，并区分 OpenAI API 可发送参数与 Codex 客户端的 Ultra 多代理模式。

## 1. 结论摘要

- Codex 的 Fast 模式最终发送 JSON 请求体字段 `service_tier: "priority"`，不是通过请求头开启。
- `priority` 是 OpenAI API 的服务等级值，不是 GPT 模型名，也不是 juhe-ai 内部调度优先级。juhe-ai 第一版只在 `providerCode=gpt` 的原生 OpenAI 请求链路使用该语义。
- 账户高级配置新增两个可空覆盖项：
  - `service_tier_override`
  - `reasoning_effort_override`
- 两个字段默认都不存在。不存在表示不修改客户端请求；只要用户配置了值，就必须覆盖客户端原值。
- `service_tier_override=default` 是本地“强制标准档”哨兵，运行时删除客户端 `service_tier`；`priority`、`flex` 则写入对应值。
- 思考级别必须由模型目录能力驱动。账户所选多个支持模型只有共同支持的级别才允许显示和保存。
- OpenAI API 请求级 reasoning effort 不包含 `ultra`。GPT-5.6 API 当前可发送值为 `none`、`low`、`medium`、`high`、`xhigh`、`max`。
- Codex 源码中的 Ultra 不是 OAuth 专属能力。当前 `gpt-5.6-sol` 和 `gpt-5.6-terra` 都标记为 `supported_in_api: true` 并提供 Ultra；`gpt-5.6-luna` 不提供 Ultra。
- Codex 选择 Ultra 时，上游请求实际发送 `max`，同时 Codex 客户端启用主动多代理编排。因此 Ultra 不进入账户“思考级别覆盖”下拉，也不能靠向上游发送 `reasoning.effort=ultra` 实现。
- OpenAI 公共 Responses API 另有独立的 Multi-agent Beta：通过请求体 `multi_agent.enabled=true` 和请求头 `OpenAI-Beta: responses_multi_agent=v1` 开启，当前支持全部 GPT-5.6 模型。它与 Codex Ultra 类似，但不是 reasoning effort，也不纳入本次两个覆盖字段。

## 2. 调研依据

### 2.1 OpenAI 官方语义

- [Priority processing](https://developers.openai.com/api/docs/guides/priority-processing) 使用请求体 `service_tier: "priority"` 开启 Priority processing。该能力按支持模型和端点生效，价格高于标准档，响应中的实际 `service_tier` 可能与请求值不同。
- [Using the latest model](https://developers.openai.com/api/docs/guides/latest-model.md) 当前列出 GPT-5.6 的 reasoning effort 为 `none`、`low`、`medium`、`high`、`xhigh`、`max`。
- [Multi-agent](https://developers.openai.com/api/docs/guides/tools-multi-agent) 当前说明全部 GPT-5.6 模型可使用 Responses Multi-agent Beta；原始 HTTP 请求需要 `OpenAI-Beta: responses_multi_agent=v1`，请求体使用 `multi_agent.enabled`。
- [Codex models](https://learn.chatgpt.com/docs/models) 将 Max 解释为给当前模型更多单任务思考时间，将 Ultra 解释为使用子代理并行处理复杂任务。
- [Codex Fast mode](https://learn.chatgpt.com/docs/agent-configuration/speed#fast-mode) 说明 Fast 提升速度并增加消耗；ChatGPT 登录按 Codex credits 计量，API Key 登录按 API 定价计量。

官方 Fast 页面当前列出的模型与最新 Codex 模型目录存在时间差。本项目不在前端硬编码“哪些模型支持 Fast”，以当前模型能力目录和真实上游验证为准。

### 2.2 Codex 源码语义

调研源码：

- 仓库：`F:\temp-project\agent\openai-codex-main`
- commit：`6138909d6ec58b2fbe635ef973e02caecad5a5aa`
- commit 时间：`2026-07-10T12:16:14Z`

关键结论：

- `codex-rs/protocol/src/config_types.rs`
  - `ServiceTier::Fast.request_value()` 返回 `priority`。
  - `fast` 和 `priority` 都解析为 Fast。
  - `default` 是显式标准路由哨兵，不是模型目录中的服务等级 ID。
- `codex-rs/codex-api/src/common.rs`
  - Responses 请求在 JSON 请求体中携带 `reasoning` 和可选 `service_tier`。
- `codex-rs/core/src/client.rs`
  - `ReasoningEffortConfig::Ultra` 在发送请求前转换为 `Max`。
- `codex-rs/core/src/session/multi_agents.rs`
  - Ultra 令 `MultiAgentMode` 进入 `Proactive`。
- `codex-rs/models-manager/models.json`
  - `gpt-5.6-sol`：Codex 级别为 `low|medium|high|xhigh|max|ultra`，服务等级包含 `priority`，`supported_in_api=true`。
  - `gpt-5.6-terra`：Codex 级别为 `low|medium|high|xhigh|max|ultra`，服务等级包含 `priority`，`supported_in_api=true`。
  - `gpt-5.6-luna`：Codex 级别为 `low|medium|high|xhigh|max`，服务等级包含 `priority`，`supported_in_api=true`。

因此：

| 能力 | 上游请求值 | 是否能做账户覆盖 | 是否依赖 OAuth |
| --- | --- | --- | --- |
| Fast / Priority | `service_tier=priority` | 可以 | 不依赖；API Key 也可使用，按 API 计价 |
| Max | reasoning effort `max` | 可以 | 不依赖，取决于模型和账户链路 |
| Ultra | 上游发送 `max`，客户端同时主动创建子代理 | 不可以作为单一请求字段覆盖 | 不依赖 OAuth；取决于 Codex 客户端、模型目录和多代理兼容 |
| Responses Multi-agent Beta | `multi_agent.enabled=true`，并带 Beta 请求头 | 不属于本次两个覆盖字段；后续可设计独立开关 | 公共 API Key 能力，当前适用于全部 GPT-5.6 |

## 3. 目标与非目标

### 3.1 目标

- 允许 GPT AI 账户统一覆盖客户端请求的服务等级。
- 允许 GPT AI 账户统一覆盖客户端请求的思考级别。
- 默认不覆盖，保持客户端原始行为。
- 只展示账户所选上游模型共同支持的选项。
- 前端和后端使用同一模型能力事实，后端负责最终校验。
- API Key、OAuth、普通 JSON 和大 JSON 请求使用一致语义。
- Codex `/models` 返回真实模型能力，不再统一伪造 `minimal|low|medium|high` 和空服务等级。

### 3.2 非目标

- 不通过请求头开启 Fast。
- 不把 AI 账户 `priority` 调度字段与 OpenAI `service_tier=priority` 混用。
- 不把 `ultra` 发送到 OpenAI API。
- 不把账户 reasoning 覆盖扩展为服务端多代理编排。
- 不在本次实现中新增 Responses `multi_agent.enabled` 账户覆盖；如果立项，必须作为独立“多代理模式（Beta）”开关，而不是思考级别。
- 不根据模型名称前缀在前端临时猜测能力。
- 不为通用 `openai`、GLM、DeepSeek、Anthropic 或混合供应商账户自动套用 GPT 专属覆盖。
- 不在客户端请求路径实时查询模型目录数据库。

## 4. 字段契约

### 4.1 账户覆盖字段

字段存放在账户 provider 配置 JSON 中，与 `supported_endpoint_modes`、错误处理策略等账户运行配置一起进入 `credentials_encrypted`。它们不是认证秘密，但复用当前账户配置读写、授权只读展示、缓存和导入导出链路。

```ts
type GptServiceTierOverride =
  | 'default'
  | 'priority'
  | 'flex'

type GptReasoningEffortOverride =
  | 'none'
  | 'minimal'
  | 'low'
  | 'medium'
  | 'high'
  | 'xhigh'
  | 'max'

interface GptAccountRequestOverrides {
  service_tier_override?: GptServiceTierOverride
  reasoning_effort_override?: GptReasoningEffortOverride
}
```

存储规则：

- 空值不存空字符串，直接省略字段。
- 未配置字段不修改客户端请求。
- `none` 是有效思考级别，表示显式关闭 reasoning，不能与空值混为一谈。
- `default` 是有效服务等级覆盖，表示强制标准档，不能与空值混为一谈。
- 不接受 `fast` 作为持久化值；界面可显示“Priority（Fast）”，落库统一为 `priority`。
- 不接受 `ultra` 作为 `reasoning_effort_override`。

### 4.2 模型 API 请求能力

现有 `supportsServiceTier: boolean` 无法表达具体支持 `priority` 还是 `flex`，也无法表达 reasoning 级别。模型目录需要增加精确能力：

```ts
type GptWireReasoningEffort =
  | 'none'
  | 'minimal'
  | 'low'
  | 'medium'
  | 'high'
  | 'xhigh'
  | 'max'

type GptServiceTier = 'priority' | 'flex'

interface GptModelRequestCapabilities {
  supportedServiceTiers: GptServiceTier[]
  supportedReasoningEfforts: GptWireReasoningEffort[]
  defaultReasoningEffort?: GptWireReasoningEffort
}
```

规则：

- `supportedServiceTiers` 和 `supportedReasoningEfforts` 是账户覆盖功能的唯一能力来源。
- `supportsServiceTier` 如需保留给现有展示，应由 `supportedServiceTiers.length > 0` 派生，不允许两份数据独立维护。
- 空数组表示不支持或能力未知。能力未知时不显示覆盖入口，也不允许通过手工 payload 保存。
- 日期快照、稳定别名和 `pricingModel` 别名必须继承已确认的规范模型能力，不能在前端做字符串前缀猜测。
- 当前官方 `gpt-5.6` 别名指向 `gpt-5.6-sol`，能力解析应落到 Sol 的规范能力。

### 4.3 Codex 客户端能力

Codex picker 级别与 OpenAI API wire effort 必须分开建模：

```ts
type CodexReasoningLevel =
  | GptWireReasoningEffort
  | 'ultra'

interface CodexModelCapabilities {
  codexDefaultReasoningLevel?: CodexReasoningLevel
  codexSupportedReasoningLevels: CodexReasoningLevel[]
  codexMultiAgentVersion?: 'v2'
}
```

- 账户高级配置只消费 `supportedReasoningEfforts`。
- Codex `/models` 只消费 Codex 专用字段。
- `ultra` 只能出现在 `codexSupportedReasoningLevels`。
- 当前 Sol、Terra 可配置 `ultra`；Luna 不配置。
- Codex API 登录可见性按 `supported_in_api` 决定，不能再把 Ultra 理解为 OAuth 专属。

### 4.4 自定义模型

自定义 GPT 模型必须允许维护 wire 能力，否则选中自定义模型后只能按“能力未知”隐藏覆盖项。

建议给 `custom_provider_models` 增加当前 schema 字段：

| 字段 | 类型 | 默认值 | 用途 |
| --- | --- | --- | --- |
| `supported_service_tiers_json` | TEXT | `[]` | `priority|flex` |
| `supported_reasoning_efforts_json` | TEXT | `[]` | OpenAI API 可发送 reasoning effort |
| `default_reasoning_effort` | TEXT NULL | `NULL` | 模型 API 默认 effort，仅用于说明和 Codex 默认值推导前的参考 |

第一版不允许自定义模型直接声明 `ultra`。Codex Ultra 需要额外确认多代理版本、客户端能力和网关透传行为，不能由用户仅勾选一个字符串开启。

项目不保留旧 schema 兼容分支。实现时直接更新 SQLite / PostgreSQL 当前 schema、repository 和接口契约，部署数据同步由维护者按当前 schema 单独执行。

## 5. GPT-5.6 当前能力基线

账户覆盖使用 API wire 能力，Codex picker 使用 Codex 能力：

| 模型 | API reasoning effort | 服务等级 | Codex reasoning level | Ultra |
| --- | --- | --- | --- | --- |
| `gpt-5.6` / `gpt-5.6-sol` | `none, low, medium, high, xhigh, max` | `priority` | `low, medium, high, xhigh, max, ultra` | 是 |
| `gpt-5.6-terra` | `none, low, medium, high, xhigh, max` | `priority` | `low, medium, high, xhigh, max, ultra` | 是 |
| `gpt-5.6-luna` | `none, low, medium, high, xhigh, max` | `priority` | `low, medium, high, xhigh, max` | 否 |

注意：

- API 默认 reasoning 与 Codex 产品默认 reasoning 可以不同。当前 Codex 源码中 Sol 默认 `low`，Terra 和 Luna 默认 `medium`。
- 账户覆盖字段为空时，不应用本表默认值，继续保留客户端值或上游默认值。
- 模型能力是带日期的外部事实，后续更新必须走 [厂商模型目录更新与清洗指南](厂商模型目录更新与清洗指南.md)。

## 6. 前端交互

### 6.1 展示位置

在 AI 账户编辑弹窗“高级配置”中新增 `AccountGptRequestOverridesSection.vue`：

- 只在 `providerCode === 'gpt'` 时参与渲染。
- API Key 和 OAuth 账户都可以显示。
- 授权账户只读展示来源账户的有效覆盖值。
- 两个字段各自按能力决定是否显示，不要求同时显示。
- 配置任一字段后，“高级配置”已配置项计数增加。

### 6.2 服务等级

字段文案：

- 标签：`服务等级覆盖`
- 占位：`不覆盖客户端`
- 选项：
  - `标准（清除客户端 service_tier）` -> `default`
  - `Priority（Fast）` -> `priority`
  - `Flex` -> `flex`

显示规则：

1. 读取账户 `supportedModels` 对应模型能力。
2. 计算所有模型 `supportedServiceTiers` 的交集。
3. 再与账户类型和上游适配器支持集合求交集。
   - OAuth Codex 当前支持 `priority`。
   - API Key 按模型目录支持 `priority|flex`。
4. 交集为空时不显示服务等级覆盖。
5. 交集非空时显示 `default`，并只显示交集中的非标准档。

不提供 `auto`。空值已经承担“不覆盖客户端”的语义。

### 6.3 思考级别

字段文案：

- 标签：`思考级别覆盖`
- 占位：`不覆盖客户端`
- 选项按模型能力动态生成：
  - `none`：关闭
  - `minimal`：最小
  - `low`：低
  - `medium`：中
  - `high`：高
  - `xhigh`：超高
  - `max`：最大

显示规则：

1. 读取账户每个 `supportedModels` 的 `supportedReasoningEfforts`。
2. 计算全部模型的集合交集，不取并集。
3. 任一模型能力未知或为空，交集视为空。
4. 交集为空时不显示思考级别覆盖。
5. 只显示交集中的选项，不显示模型不支持的值。
6. 永远不在该下拉中显示 `ultra`。

选择多个模型的示例：

- Sol + Terra：显示 `none|low|medium|high|xhigh|max`。
- Sol + 一个只支持 `low|medium|high` 的模型：只显示 `low|medium|high`。
- Sol + 一个能力未知的自定义模型：不显示思考级别覆盖。

### 6.4 模型变化

- 用户修改 `supportedModels` 后重新计算交集。
- 如果当前覆盖值不再被所有模型共同支持，前端立即清空该字段，并提示：
  - `所选模型不再共同支持当前思考级别，已取消账户覆盖。`
  - `所选模型不再共同支持当前服务等级，已取消账户覆盖。`
- 编辑历史无效配置时，不把无效值塞回下拉选项；显示中文警告，表单值初始化为空，保存后清除无效配置。
- 前端隐藏不等于后端放行，后端仍需按最新模型目录校验。

### 6.5 风险提示

服务等级下方：

`Priority（Fast）可能提高请求价格；上游最终实际服务等级以响应 service_tier 为准。`

思考级别下方：

`配置后将覆盖客户端传入值。较高级别通常增加延迟和输出 token 消耗。Ultra 属于 Codex 多代理模式，不在此处配置。`

## 7. 后端保存校验

### 7.1 语法校验

账户凭据归一化允许键增加：

- API Key：
  - `service_tier_override`
  - `reasoning_effort_override`
- OAuth：
  - `service_tier_override`
  - `reasoning_effort_override`

归一化规则：

- `undefined|null|''` 均归一为省略字段。
- 非法枚举直接返回中文 `400`。
- `fast`、`auto`、`ultra` 不作为持久化合法值。

### 7.2 能力校验

保存账户时必须同时取得：

- `providerCode`
- `type`
- 归一化后的 `supportedModels`
- 账户所有者可见的 GPT 模型目录
- 覆盖字段

校验规则：

- 非 `gpt` 供应商拒绝保存两个字段。
- reasoning 覆盖值必须出现在全部 `supportedModels` 的 `supportedReasoningEfforts` 中。
- `priority|flex` 必须出现在全部 `supportedModels` 的 `supportedServiceTiers` 中，并被当前账户适配器支持。
- `default` 只有在全部模型至少共同支持一个可选服务等级时允许保存，避免给完全不支持服务等级的账户制造无意义配置。
- 任一模型能力未知时，拒绝保存对应覆盖字段。
- 校验使用实际上游模型，也就是账户 `supportedModels` 和模型映射的 `upstreamModel`，不使用客户端别名猜能力。

建议错误：

- `所选支持模型不共同支持服务等级 priority`
- `所选支持模型不共同支持思考级别 max`
- `模型 <model> 未声明思考级别能力，不能启用账户覆盖`
- `当前 OAuth 上游适配器不支持服务等级 flex`

### 7.3 OAuth 创建与重新授权

OAuth 创建流程的 `credentialsPatch` 必须允许携带两个覆盖字段。重新授权只刷新 token 和 OAuth 元数据，不清除既有覆盖配置。

## 8. 网关运行时覆盖

### 8.1 优先级

统一优先级：

1. 账户已配置的覆盖值。
2. 客户端请求值。
3. 上游模型默认值。

也就是：

- 账户字段不存在：完全保留客户端。
- 账户字段存在：无条件覆盖客户端。
- 不允许采用“客户端有值就不覆盖”的合并方式。

### 8.2 生效时机

覆盖发生在：

1. 已选中真实上游账户。
2. 已完成下游模型到上游模型映射。
3. 已确定实际上游 endpoint family。
4. 协议桥接已生成目标协议请求体，或即将生成最终上游请求体。
5. 上游适配器字段清洗和 JSON 序列化之前。

这样可以保证能力校验和字段形态都针对真实上游模型与协议。

### 8.3 字段改写

Responses：

```json
{
  "service_tier": "priority",
  "reasoning": {
    "effort": "max"
  }
}
```

- `reasoning_effort_override` 只覆盖 `reasoning.effort`，保留客户端 `reasoning.summary` 等其他合法字段。
- 删除冲突的顶层 `reasoning_effort`。

Chat Completions：

```json
{
  "service_tier": "priority",
  "reasoning_effort": "max"
}
```

- 覆盖顶层 `reasoning_effort`。
- 不把 Responses 的 `reasoning` 对象原样发送到 Chat 上游。

服务等级：

- `default`：删除客户端 `service_tier`，强制回到标准档。
- `priority`：设置 `service_tier="priority"`。
- `flex`：设置 `service_tier="flex"`。

### 8.4 OAuth Codex

当前 OAuth normalizer 只保留 `service_tier=priority`：

- 覆盖为 `priority`：在 `normalizeOpenAIOAuthCodexServiceTier()` 之前写入，后续保留。
- 覆盖为 `default`：在 normalizer 之前删除 `service_tier`。
- `flex`：当前 OAuth 适配器能力集合不包含，前端不显示，后端拒绝保存。
- 普通 `/responses`：reasoning 覆盖写入后保留。
- `/responses/compact`：可应用 service tier；reasoning 字段会按 compact 契约删除，因此 reasoning 覆盖不适用。

`/responses/compact` 的不适用状态应进入调试日志或审计元数据，不能伪装成已经成功覆盖。

### 8.5 API Key 大请求体

- 两个覆盖字段都为空时，API Key 原生透传继续沿用现有请求体路径，不增加 JSON 解析成本。
- 任一覆盖字段存在时才进入结构化 JSON 改写。
- 大于内联解析阈值的请求必须复用现有 JSON worker，不允许在主线程完整解析大请求体。
- worker 输入需要携带已校验的覆盖值、实际上游模型和 endpoint family。

### 8.6 失效配置

如果模型目录更新后，运行时发现已保存覆盖不再支持：

- 不静默忽略账户覆盖后继续请求上游。
- 该账户对当前模型视为本地不可用候选，并记录稳定诊断原因。
- 如果所有候选都因该原因不可用，返回稳定本地错误 `account_request_override_unsupported`。
- 管理端账户详情显示需要清理的无效覆盖。

## 9. Ultra、Multi-agent 与 Codex `/models`

### 9.1 账户覆盖边界

- `ultra` 不是 OpenAI API wire effort。
- 账户配置无法要求任意客户端自动创建子代理。
- 将 `ultra` 偷换为 `max` 会丢失多代理语义，因此不做自动降级。
- 如果客户端自己选择 Ultra，Codex 会发送 `max` 并在客户端侧主动创建子代理；网关只负责承接这些请求。

### 9.2 当前项目缺口

`buildCodexModelInfo()` 当前对所有模型硬编码：

- 默认 reasoning 为 `medium`
- 支持 `minimal|low|medium|high`
- `service_tiers=[]`
- `multi_agent_version=null`

该返回会导致：

- GPT-5.6 的 `xhigh|max` 丢失。
- Sol、Terra 的 Ultra 丢失。
- Fast / Priority 不出现在 Codex 模型能力中。
- 自定义模型被错误声明为支持并不存在的 reasoning 级别。

实现时必须改为模型能力驱动：

- `default_reasoning_level` 使用 Codex 专用默认值。
- `supported_reasoning_levels` 使用 Codex 专用级别。
- `service_tiers` 使用模型 `supportedServiceTiers`。
- `additional_speed_tiers` 在支持 `priority` 时包含 `fast`。
- `multi_agent_version` 只在已确认 Codex 多代理兼容时返回 `v2`。

### 9.3 Ultra 上线前验证

当前 CLI 本地网关回归显式禁用了 `multi_agent` 和 `multi_agent_v2`，因此现有测试不能证明 Ultra 全链路可用。启用 Codex `/models` Ultra 前必须验证：

- API Key 登录可以看到 Sol / Terra Ultra，证明不受 OAuth 过滤。
- Codex 选择 Ultra 后，上游请求 reasoning effort 为 `max`，不是 `ultra`。
- Codex 实际创建子代理任务。
- `x-openai-subagent` 等 Codex 子代理元数据在 GPT API Key 与 OAuth 适配器中的处理符合上游要求。
- 多代理请求继续经过 API Key、路由策略、分组、账户调度、额度、并发、审计和使用统计。
- Luna 不显示 Ultra。

Ultra 验证完成前，可以先落地账户 `max` 覆盖和 Fast / Priority，不把 Ultra 误放进账户高级配置。

### 9.4 OpenAI Responses Multi-agent Beta

OpenAI 公共 API 当前提供与 Ultra 类似但契约独立的托管多代理能力：

```http
OpenAI-Beta: responses_multi_agent=v1
```

```json
{
  "model": "gpt-5.6-sol",
  "multi_agent": {
    "enabled": true,
    "max_concurrent_subagents": 3
  }
}
```

关键边界：

- 当前官方说明全部 GPT-5.6 模型都支持，包括 Sol、Terra 和 Luna。
- 该能力由 Responses API 托管 root agent 和 subagent 协作，应用不需要执行 `multi_agent_call`。
- 它不是 `reasoning.effort=ultra`，也不要求 reasoning effort 必须是 `max`。
- `/responses/compact` 不支持 Multi-agent。
- 启用 Multi-agent 时不支持 `reasoning.summary` 和 `max_tool_calls`。
- API 可能返回 `multi_agent_call`、`multi_agent_call_output` 和 `agent_message` 等新 item / SSE 事件，网关响应语义、审计、流式中断和客户端兼容都需要同步扩展。

如果后续要在 GPT 账户高级配置中提供该能力，建议单独立项并使用：

```ts
type GptMultiAgentOverride = 'enabled' | 'disabled'
```

- 空值：不覆盖客户端。
- `enabled`：强制写入 `multi_agent.enabled=true` 并补 Beta 请求头。
- `disabled`：删除或关闭客户端 Multi-agent 配置，并移除只为该能力添加的 Beta 请求头。
- 只在全部支持模型都声明 `supportsResponsesMultiAgentBeta=true` 且实际上游为 Responses 时显示。
- 不与 `reasoning_effort_override` 合成一个枚举，也不使用“Ultra”作为 API 字段名。

## 10. 接口与类型变化

### 10.1 供应商模型接口

`ProviderModelPricing` 和 `/providers/:code/models` 增加：

```ts
supportedServiceTiers: GptServiceTier[]
supportedReasoningEfforts: GptWireReasoningEffort[]
defaultReasoningEffort?: GptWireReasoningEffort
```

`/providers/models/options` 和账户模型选项也必须返回前两个能力数组，避免账户表单为了能力再逐模型请求详情。

### 10.2 账户接口

账户创建、更新、OAuth 创建 patch、草稿测试、导入和导出使用同一凭据字段：

```json
{
  "credentials": {
    "service_tier_override": "priority",
    "reasoning_effort_override": "high"
  }
}
```

账户详情对有权限用户返回两个非秘密字段。授权账户只读详情也应通过 `publicAccountCredentialKeys` 返回它们，让被授权方能看见实际请求行为。

### 10.3 审计

请求审计建议记录非敏感结构化字段：

- `clientServiceTier`
- `accountServiceTierOverride`
- `upstreamRequestedServiceTier`
- `upstreamActualServiceTier`
- `clientReasoningEffort`
- `accountReasoningEffortOverride`
- `upstreamReasoningEffort`
- `overrideApplied`
- `overrideNotApplicableReason`

不能仅记录一个最终值，否则无法排查是客户端请求、账户覆盖还是上游降级造成。

## 11. 测试矩阵

### 11.1 模型能力

- GPT-5.6 Sol / Terra / Luna wire reasoning、Codex reasoning 和服务等级准确。
- Sol / Terra 有 Ultra，Luna 无 Ultra。
- `gpt-5.6` 别名继承 Sol 能力。
- 自定义模型空能力数组时不显示覆盖。
- 自定义模型声明能力后可进入交集。
- Codex `/models` 不再给所有模型伪造统一 reasoning。

### 11.2 前端

- 默认两个字段为空，高级配置计数不增加。
- 单模型按能力显示选项。
- 多模型取交集。
- 任一模型未知时隐藏对应字段。
- 模型变化导致当前值失效时清空并显示中文提示。
- Ultra 不出现在账户 reasoning 下拉。
- API Key 可显示 Priority；OAuth 可显示 Priority；OAuth 不显示 Flex。
- 授权账户只读展示来源覆盖值。

### 11.3 保存校验

- 空字段不落库。
- `none`、`default` 可正确保存，不能被当成空值。
- `fast`、`auto`、`ultra` 被拒绝。
- 非 GPT 供应商被拒绝。
- 任一支持模型不支持时被拒绝。
- 模型能力未知时被拒绝。
- OAuth `flex` 被拒绝。

### 11.4 网关

- API Key Responses 未配置时完整保留客户端值。
- API Key Responses 配置后覆盖 `service_tier` 和 `reasoning.effort`。
- API Key Chat 配置后覆盖 `service_tier` 和 `reasoning_effort`。
- `default` 删除客户端 `priority`。
- `none` 显式覆盖客户端 `high`。
- reasoning 覆盖保留 Responses `reasoning.summary`。
- OAuth `/responses` Priority 和 reasoning 覆盖生效。
- OAuth `/responses/compact` Priority 生效、reasoning 标记不适用。
- 模型映射后按实际上游模型和 endpoint family 应用。
- 大请求体走 JSON worker。
- 两个覆盖字段为空时不增加 API Key 请求体解析。
- 通用 OpenAI-compatible、Anthropic、GLM、DeepSeek、Gemini 和混合供应商链路不受影响。

### 11.5 Ultra

- Codex API Key 登录可见 Sol / Terra Ultra。
- Ultra 上游 wire effort 为 `max`。
- Proactive 多代理实际创建子任务。
- Luna 不显示 Ultra。
- 网关不接受账户 `reasoning_effort_override=ultra`。

### 11.6 Responses Multi-agent Beta 后续验证

- Sol、Terra、Luna 都可以通过公共 API Key 开启 `multi_agent.enabled`。
- 网关正确补充和过滤 `OpenAI-Beta: responses_multi_agent=v1`。
- `/responses/compact` 明确拒绝或关闭 Multi-agent。
- `reasoning.summary`、`max_tool_calls` 冲突在本地给出稳定中文指导。
- JSON 和 SSE 能保真承接 `multi_agent_call`、`multi_agent_call_output`、`agent_message` 和 agent attribution。

## 12. 发布与观测

- Priority 可能产生更高价格，发布前必须确认 GPT-5.6 当前价格数据是否包含 Priority 计价；不能继续按标准价格低估。
- 上游响应实际 `service_tier` 可能降级，统计和审计以响应实际值为事实，账户配置只表示请求意图。
- 首次发布建议观察：
  - 覆盖配置账户数。
  - Priority 请求数和实际 Priority 响应数。
  - 覆盖校验失败数。
  - 模型目录能力缺失数。
  - `account_request_override_unsupported` 数量。
- 发布异常时关闭或清空账户覆盖值即可恢复客户端原始参数行为；不需要引入旧字段兼容分支。

## 13. 关联文档

- [OpenAI 账号接入](OpenAI账号接入.md)
- [账户模型限制设计](账户模型限制设计.md)
- [厂商模型目录更新与清洗指南](厂商模型目录更新与清洗指南.md)
- [自定义模型与模型映射设计](自定义模型与模型映射设计.md)
- [请求处理分层设计](请求处理分层设计.md)
- [Codex Responses 转 Chat 协议转换设计](Codex%20Responses转Chat协议转换设计.md)
- [AI 账户导入协议](AI账户导入协议.md)
- [SQLite 存储说明](SQLite存储说明.md)
- [PLAN-0083 GPT 服务等级与思考级别覆盖](../plans/计划-0083-GPT服务等级与思考级别覆盖.md)
