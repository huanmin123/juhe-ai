# AI 账户上游余额查询设计

> 状态：设计完成，待实现。
>
> 需求来源：Codex 会话 `019f50a8-4d4a-73c1-8506-0dca19b9a684`。

## 1. 目标

为 API Key 类型 AI 账户查询上游中转当前可用余额，并在“我的 AI 账户”列表的“用量（日）”单元格中展示。余额查询是账户所有者主动开启的辅助能力，不参与本地用量统计、额度判断、账户状态、健康检查或网关调度。

首期保持轻量：

- 支持 Sub2API、New API、LiteLLM 和受限自定义接口。
- 默认每 5 分钟刷新，可配置 1 至 10 分钟。
- 只保存当前查询结果，不保存余额历史，不做余额告警。
- 不做自动识别、管理员模板、脚本解析或任意 HTTP 请求配置。
- 仅支持单 API Key 账户；OAuth 和账户内多 Key 池不开放。

## 2. 页面口径

### 2.1 列表展示

余额显示在 `AccountUsageCell` 的本地日用量下方：

```text
剩余：$7.31  ⟳
```

- 刷新入口使用 `ReloadOutlined` 图标，不显示边框、背景或“刷新”文字。
- 图标悬浮提示“刷新余额”，查询期间旋转。
- 功能未开启时不显示余额行。
- 账户停用时保留当前结果，但刷新图标禁用，后台停止刷新。

状态文案固定为：

| 状态 | 展示 | 说明 |
| --- | --- | --- |
| `pending` | `剩余：待查询` | 已开启但尚无结果 |
| `refreshing` | `剩余：查询中` | 手动或自动查询进行中 |
| `fresh` | `剩余：$7.31` | 当前查询成功 |
| `unlimited` | `剩余：无限` | 上游明确报告无限额度 |
| `unsupported` | `剩余：未提供` | 接口成功但没有可换算金额口径 |
| `failed` | `剩余：查询失败` | 当前查询失败，悬浮显示本次脱敏错误 |

失败状态不展示最后一次成功金额。查询开始时进入 `refreshing`；查询失败后立即清空快照中的金额字段，只保留失败状态与错误摘要。下一次成功后再恢复金额。

### 2.2 编辑配置

AI 账户新增/编辑弹窗增加“余额查询”开关，交互与时间计划一致：关闭时隐藏详细配置，开启时显示：

- 中转类型：Sub2API、New API、LiteLLM、自定义。
- 刷新间隔：整数分钟，默认 5，范围 1 至 10。
- 自定义类型额外显示查询路径、认证方式、余额字段路径、可选总额/已用字段路径和换算除数。

只有 `type=api_key`、单 Key、非授权实例账户可以开启。前端隐藏不适用入口，后端仍执行完整校验。

## 3. 数据契约

### 3.1 账户配置

配置属于物理 AI 账户，直接进入 `accounts` 当前 schema：

| 字段 | 类型 | 默认值 | 用途 |
| --- | --- | --- | --- |
| `balance_query_enabled` | INTEGER / boolean | `false` | 是否启用；供到期任务索引筛选 |
| `balance_query_config_json` | TEXT / JSON | `{}` | 适配器、刷新间隔和自定义规则 |
| `balance_query_next_refresh_at` | TEXT / timestamptz NULL | `NULL` | 自动刷新到期时间；业务库中的唯一调度事实 |

```ts
type AccountBalanceAdapter =
  | 'sub2api'
  | 'newapi'
  | 'litellm'
  | 'custom'

interface AccountBalanceQueryConfig {
  adapter: AccountBalanceAdapter
  intervalMinutes: number
  custom?: {
    path: string
    auth: 'bearer' | 'x-api-key' | 'x-goog-api-key' | 'none'
    remainingPointer?: string
    totalPointer?: string
    usedPointer?: string
    divisor: number
  }
}
```

规则：

- 关闭功能时 `balance_query_enabled=false`，配置可保留，重新开启时恢复表单。
- 开启或修改适配器时把 `balance_query_next_refresh_at` 设为当前时间；关闭时清空。
- `intervalMinutes` 必须为 1 至 10 的整数。
- 自定义配置必须提供 `remainingPointer`，或同时提供 `totalPointer` 与 `usedPointer`。
- `divisor` 必须为有限正数，默认 `1`。
- 不在配置中保存第二套密钥、任意 Header、请求体或脚本。

### 3.2 当前快照

复用 `account_usage_snapshots`，新增 `kind='relay_balance'`。主键继续是 `(system_account_id, account_id, kind)`，每个物理账户只保留一行。

```ts
interface RelayBalanceSnapshot {
  status: 'pending' | 'refreshing' | 'fresh' | 'unlimited' | 'unsupported' | 'failed'
  remainingUsd?: string
  rawRemaining?: string
  rawUnit?: 'usd' | 'quota' | 'budget'
  basis?: 'api_key_quota' | 'subscription' | 'wallet' | 'key_budget'
}
```

- 金额使用 decimal string，禁止用浮点数作为持久化事实。
- `refresh_status` 与 `snapshot_json.status` 使用同一状态值。
- `last_attempt_at` 记录本次尝试，成功时写 `last_success_at`。
- `next_refresh_after` 只用于向前端展示下次刷新时间，不参与 worker 候选查询；调度事实固定读取账户表的 `balance_query_next_refresh_at`。
- `last_error_message` 只保存适合展示的错误摘要，不保存响应正文或密钥。
- `failed` 时 `snapshot_json` 不得保留 `remainingUsd`、`rawRemaining` 或旧成功金额。

### 3.3 索引

账户表增加启用账户候选索引：

```sql
CREATE INDEX idx_accounts_balance_query_due
ON accounts(balance_query_next_refresh_at, id)
WHERE status = 'active'
  AND balance_query_enabled = 1
  AND deleted_at IS NULL
  AND authorization_instance_authorization_id IS NULL;
```

业务库和统计库相互独立，禁止跨库 JOIN 判断到期。worker 直接按上述账户索引读取 `balance_query_next_refresh_at <= now` 的候选；列表展示再按当前页账户 ID 批量读取统计快照。SQLite 与 PostgreSQL 使用同一稳定排序：`balance_query_next_refresh_at ASC, id ASC`，单轮固定上限，禁止无界扫描。

## 4. 适配器

适配器接口保持最小：

```ts
interface AccountBalanceAdapterDriver {
  query(context: AccountBalanceQueryContext): Promise<AccountBalanceQueryResult>
}
```

通用请求约束：

- 使用物理账户现有 Base URL、当前单一 API Key 和已绑定代理。
- 仅发送 `GET`，最长 8 秒，禁止重定向，响应体上限 256 KiB。
- 自定义路径必须是以 `/` 开头的相对路径；解析后必须与账户 Base URL 同源。
- 只解析 JSON；状态码、鉴权失败、超时、超限和字段错误映射为中文错误摘要。
- 外部查询失败不修改账户状态、最近错误、冷却、Key 运行态或调度事实。

首批内置规则：

| 适配器 | 路径 | 归一化 |
| --- | --- | --- |
| `sub2api` | `GET /v1/usage` | Key 独立额度优先，其次订阅最紧窗口，最后钱包；上游 USD 直接使用 |
| `newapi` | `GET /api/usage/token/`，必要时 `GET /api/status` | 无限额度单独返回；`total_available / quota_per_unit` 转 USD |
| `litellm` | `GET /key/info` | 有 `max_budget` 时返回 `max_budget - spend`，否则 `unsupported` |
| `custom` | 用户配置的同源相对路径 | JSON Pointer 取余额，或总额减已用，再除以 divisor |

所有余额只能来自上述明确查询接口的响应，不允许根据站点类型、本地 usage、账户额度配置或兼容特征推算。解析器必须以真实响应 fixture 做回归，不根据字段名猜测币种；New API 的旧 billing 字段可能实际承载 CNY、Token 或内部 quota，因此不作为余额来源。其他中转统一使用受限自定义接口。

## 5. 后台刷新

当前生产运行时由 Node `ops-worker` 承接一个简单定时任务，每分钟执行一次：

1. 按索引读取状态可用、启用余额查询、非授权实例、单 API Key 的到期物理账户，单轮最多 100 个。
2. 按 `next_refresh_after ASC, account_id ASC` 稳定排序。
3. 以小并发执行查询；自动和手动查询复用现有 `background_job_leases`，以 `account-balance:<accountId>` 为租约 key、15 秒为租期，保证跨进程同一账户同一时刻只有一个查询。
4. 查询开始写 `refreshing`，成功或失败后写最终状态与下次刷新时间。
5. 下次刷新时间固定写入账户的 `balance_query_next_refresh_at`，值为本次完成时间加配置分钟数；快照可复制该值供展示，首期不做指数退避。

账户停用或时间计划当前不可用时不领取。重新启用后，若快照已到期则下一轮立即刷新。worker 重启不丢任务，因为到期时间持久化在快照表中。

首期不引入 Redis 队列、独立任务表、余额专用租约表或余额历史表，只复用现有后台任务租约。多实例部署不在本计划范围；后续真正启用多节点 worker 时再接入现有用户分片方案。

## 6. 手动刷新

接口：

```http
POST /__aisys__/api/accounts/:id/balance/refresh
```

- 仅物理账户所有者或管理员可调用。
- 账户必须启用余额查询且当前可用。
- 请求同步等待外部查询，服务器超时 8 秒；前端图标在请求期间旋转。
- 同一账户已有查询时返回当前 `refreshing` 状态，不重复发起上游请求。
- 成功、无限、未提供和失败都返回最新快照 DTO；只有本地权限或参数错误使用 4xx。
- 手动刷新同样更新 `next_refresh_after`，避免随后被自动任务重复查询。

账户列表接口按当前页账户 ID 批量加载 `relay_balance` 快照，不逐行查询。普通用户只能看到自有物理账户余额；管理员管理视图可以看到所有物理账户。授权实例和被授权用户响应中不返回余额配置或快照。

## 7. 安全边界

- API Key 只在服务端内存中用于当前上游请求，不返回前端、不写日志、不写错误摘要。
- 自定义 URL 必须复用现有上游 URL 安全校验和账户代理，不允许访问另一 Origin。
- 禁止重定向，避免同源入口把 Authorization 转发到其他主机。
- JSON Pointer 只读取已解析、受大小限制的 JSON，不支持表达式、模板、JavaScript 或动态代码。
- 错误摘要区分超时、HTTP 状态、鉴权失败、JSON 非法和字段不可解析，不包含完整响应正文。
- 余额查询不能改变账户可调度性，也不能作为自动停用、切号或告警依据。

## 8. 状态与删除

- 创建账户时默认关闭余额查询。
- 编辑配置后如果开启，清空旧适配器快照并立即到期；如果关闭，保留配置但删除 `relay_balance` 快照，列表立即隐藏。
- 删除物理账户时沿用现有账户清理流程删除对应快照。
- 授权实例不复制来源账户配置或快照。
- 不保留旧 schema、旧字段或旧 DTO 兼容分支；上线按当前 schema 单独同步数据库。

## 9. 验收

- 四类适配器都有成功、无限/未提供、鉴权失败、超时和字段异常回归。
- 开启、关闭、修改间隔和修改适配器时，SQLite/PostgreSQL 读写一致。
- 自动刷新只处理到期且可用的物理单 Key API Key 账户，单轮有界且稳定排序。
- 手动刷新不会重复并发请求，失败后页面只显示“查询失败”且金额字段为空。
- 授权实例和被授权用户看不到余额；管理员和物理账户所有者权限正确。
- 列表按页批量补齐快照，不出现 N+1 查询。
- 前端桌面和移动端不重叠，刷新图标无按钮外观，加载旋转和悬浮错误正常。

## 10. 非目标

- 余额历史、趋势、告警和自动停用。
- 多 API Key 池逐 Key 余额。
- OAuth 订阅额度。
- 管理员连接器模板市场。
- 自动探测中转类型。
- 任意 Header、第二套密钥、POST 请求或脚本解析。
- 余额参与本地用量、额度、路由、质量评分或账户健康状态。
