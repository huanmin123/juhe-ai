# PLAN-0044 模型路由与跨供应商 API Key

## 基本信息

- 编号：PLAN-0044
- 状态：进行中
- 创建时间：2026-06-11
- 更新时间：2026-07-14
- 关联模块：模型目录 / API Key / 分组 / 网关 / 前端 / 验证

## 目标

- 一个 API Key 仍只绑定一个 `routeStrategyId`；允许多分组的路由模式可以绑定多个供应商分组，普通路由仍只能绑定一个分组。
- 网关先用客户端请求的 `model` 确定供应商，再进入现有路由策略和账号调度。
- 路由继续使用调用方可见模型目录和现有运行时缓存，不增加新的模型身份或路由表。

## 简化边界

- 不新增 public alias namespace、claim、`catalogModelId`、`catalogSurfaceId`、endpoint route 或 normalization version。
- 不要求真实上游模型名全系统唯一；不同系统账户的个人模型继续按现有作用域隔离。
- 不在供应商路由阶段执行账号模型映射；一跳模型映射仍在选中账号后处理。
- 不把跨协议转换写入 API Key；协议 bridge 继续使用现有 provider driver 和混合供应商账户边界。
- 不在请求热路径扫描全部模型表，也不增加新的数据库表。

## 路由规则

现有网关缓存已经能按 API Key 所属路由策略的供应商集合构建：

```ts
Map<modelKey, providerCode[]>
```

继续复用它，作用域固定为：

```text
systemAccountId + includeUnpriced + sorted(providerCodes)
```

其中 `providerCodes` 直接由 API Key 的路由策略 active 分组派生，沿用当前缓存 key，不把协议、endpoint 或 profile 再编码进模型索引。

处理顺序：

1. 从请求体读取 `model`，沿用现有 `modelKey` 规则，不增加第二套规范化算法。
2. 在调用方可见、启用且可请求的模型目录缓存中查找，并与路由策略绑定分组派生出的供应商集合取交集。
3. 只命中一个供应商时，保留路由策略中该供应商的分组，再按请求协议、endpoint mode 和现有路由模式调度。
4. 没有命中时返回本地“模型不存在或当前路由策略没有对应供应商”错误，不请求上游。
5. 命中多个供应商时返回本地“模型归属不明确”错误，要求管理员调整路由策略分组或模型名称；不能按顺序静默选择。
6. 选中账号后，现有一跳模型映射才可以改写实际上游 `model`。

模型目录、路由策略分组绑定或 API Key 的 `routeStrategyId` 变化时，继续失效现有模型路由缓存。缓存 miss 可以批量读取当前作用域目录构建快照；热路径只做 `Map.get()`。

## 价格关系

- 路由前只确定供应商，不复制价格。
- 最终计价按选中账号和一跳映射后的上游模型目录行执行。
- 模型价格仍只由管理员在模型目录维护，普通用户不能通过 API Key、分组、路由策略或个人模型普通编辑改价。
- 价格与 usage 规则以 [统一模型能力与计费抽象设计](../functions/统一模型能力与计费抽象设计.md) 为准，不为路由再建 price identity。

## 前端

- API Key 表单仍只选择一个路由策略，不读取完整分组或模型目录。
- 多供应商分组继续在策略路由页面维护；本计划不增加保存前模型冲突扫描。
- 运行时冲突响应使用中文，直接说明冲突模型和供应商，不展示内部索引结构。

## 实施项

- [x] API Key 绑定路由策略，路由策略可绑定多个供应商协议档案的分组。
- [x] 当前策略供应商集合已建立 `modelKey -> providerCode[]` 运行时缓存。
- [x] 热命中不重复读取模型目录。
- [ ] 多供应商命中从静默候选收敛为明确本地错误。
- [ ] 补多供应商歧义错误和后端回归。
- [ ] 补模型目录、策略分组和 Key 策略变化的缓存失效回归。

## 验证

| 场景 | 预期 |
| --- | --- |
| Key 的策略只绑定 GPT 分组，调用 GPT 模型 | 行为与当前单供应商策略一致 |
| Key 的策略绑定 GPT 与 DeepSeek 分组，模型只属于 DeepSeek | 只保留 DeepSeek 分组 |
| 模型存在，但策略没有绑定对应供应商 | 本地明确失败，不进入账号调度 |
| 同名模型同时命中策略中的两个供应商 | 本地返回归属不明确，不静默选第一个 |
| 两个用户各有同名个人模型 | 各自在自己的系统账户作用域解析，互不串目录或价格 |
| 第二次请求相同模型与供应商集合 | 热路径只读现有缓存 |

现有回归命令：

```powershell
pnpm --filter juhe-ai-backend test:api-key-multi-group-bindings
pnpm --filter juhe-ai-backend test:api-key-group-route-capability
pnpm --filter juhe-ai-backend test:api-key-route-validation
pnpm --filter juhe-ai-backend test:api-key-group-route-cache
pnpm --filter juhe-ai-frontend test:api-key-group-bindings
```

## 验收标准

- API Key 只绑定路由策略，路由策略可以绑定多个供应商分组。
- 唯一供应商命中时按现有路由策略调度。
- 零命中和多供应商命中都在本地明确失败。
- 请求热路径不新增数据库扫描。
- 不新增模型身份、alias claim、surface、route 或价格表。
