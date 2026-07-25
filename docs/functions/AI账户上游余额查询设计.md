# AI 账户上游余额查询设计

> 状态：既有核心实现完成；2026-07-16 完成余额查询身份比较、缺失快照自愈和列表最终状态收敛，待生产发布验证。
>
> 需求来源：Codex 会话 `019f50a8-4d4a-73c1-8506-0dca19b9a684`。

## 1. 目标

为 API Key 类型 AI 账户查询上游中转当前可用余额，并在“我的 AI 账户”列表的“用量（日）”单元格中展示。余额查询是辅助能力，不参与本地用量统计、额度判断、账户状态、健康检查或网关调度，也不能阻止多 Key 账户创建、编辑或参与网关轮换。用户可以主动开启；新建账户未主动开启时，系统在首次健康检查成功后异步探测一次，确认支持才自动开启。

首期保持轻量：

- 支持 Sub2API、New API、LiteLLM、通用 `/user/balance` 和受限自定义接口。
- 默认每 5 分钟刷新，可配置 1 至 10 分钟。
- 只保存当前查询结果，不保存余额历史，不做余额告警。
- 新账户只做一次内置适配器自动探测；不做管理员模板、脚本解析或任意 HTTP 请求配置。
- 仅单 API Key 账户可以开启查询；OAuth 不开放，账户保存为多 Key 池时自动关闭余额查询，但多 Key 业务本身必须正常保存和使用。

## 2. 页面口径

### 2.1 列表展示

余额显示在 `AccountUsageCell` 的本地日用量下方：

```text
剩余：$7.31  ⟳
```

- 刷新入口使用 `ReloadOutlined` 图标，不显示边框、背景或“刷新”文字。
- 图标悬浮提示“刷新余额”，查询期间旋转。
- 功能未开启时不显示余额行。
- 账户不可调用时保留当前结果，后台延后自动刷新；物理账户所有者仍可人工刷新，授权实例继续禁止。

列表只展示最终结果：

| 状态 | 展示 | 说明 |
| --- | --- | --- |
| `pending` | 仅展示人工刷新入口 | 已开启但尚无最终结果 |
| `refreshing` | 仅展示人工刷新入口 | 已有成功余额时继续展示旧余额 |
| `fresh` | `剩余：$7.31` | 当前查询成功 |
| `unlimited` | `剩余：不限额` | 上游明确报告无限额度 |
| `unsupported` | `余额查询失败` | 按用户周期继续查询，悬浮显示原因 |
| `failed` | `余额查询失败` | 当前查询失败，悬浮显示本次错误 |

第 3 次连续临时失败后不展示最后一次成功金额，只保留失败状态与错误摘要；前两次临时失败保留已有成功结果。确定性不支持只进入 `unsupported` 诊断状态，不取消用户配置的周期。下一次成功后恢复金额并清零失败序列。

完整账户表单提交余额字段时，后台必须比较规范化后的余额查询身份，不能按字段是否出现判断变更。身份仅包含启用状态、规范化余额配置、供应商、账户类型、有效 API Key 指纹、规范化 Base URL 和代理 ID。Base URL 比较忽略 query、hash 和尾斜杠，但保留路径。名称、状态、并发、备注和模型等无关字段不重置刷新代次，也不清理余额快照。

### 2.2 编辑配置

AI 账户新增/编辑弹窗增加“余额查询”开关，交互与时间计划一致：关闭时隐藏详细配置，开启时显示：

- 查询类型只展示“内置适配”和“自定义接口”。内置适配由系统维护具体中转规则和最近成功偏好，不要求用户识别中转项目。
- 刷新间隔：整数分钟，默认 5，范围 1 至 10。
- 自定义类型额外显示查询路径、认证方式、余额字段路径、可选总额/已用字段路径和换算除数。
- 配置区提供“测试查询”按钮，非持久化说明只放在标题帮助提示中。测试结果不在表单内保留，通过页面顶部消息显示成功金额或失败原因。

只有 `type=api_key`、单 Key、非授权实例账户可以开启。前端在单 Key 时展示配置入口；当最终有效 Key 数量大于 1 时，在 Key 配置区域提示“多 Key 账户不支持余额查询，保存后将自动关闭余额查询”，余额查询不能阻断保存。后端根据最终合并后的凭据执行同一规则，不能依赖前端隐藏或关闭开关。

## 3. 数据契约

### 3.1 账户配置

配置属于物理 AI 账户，直接进入 `accounts` 当前 schema：

| 字段 | 类型 | 默认值 | 用途 |
| --- | --- | --- | --- |
| `balance_query_enabled` | INTEGER / boolean | `false` | 是否启用；供到期任务索引筛选 |
| `balance_query_config_json` | TEXT / JSON | `{}` | 适配器、刷新间隔和自定义规则 |
| `balance_query_next_refresh_at` | TEXT / timestamptz NULL | `NULL` | 自动刷新到期时间；业务库中的唯一调度事实 |

```ts
type AccountBalanceBuiltinAdapter =
  | 'sub2api'
  | 'newapi'
  | 'litellm'
  | 'user_balance'

type AccountBalanceAdapter =
  | 'builtin'
  | 'custom'

interface AccountBalanceQueryConfig {
  adapter: AccountBalanceAdapter
  intervalMinutes: number
  preferredBuiltinAdapter?: AccountBalanceBuiltinAdapter
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
- 创建或更新后的最终有效 API Key 数量大于 1 时，强制保存 `balance_query_enabled=false` 并清空 `balance_query_next_refresh_at`；即使请求省略余额字段或显式提交 `true`，也不能因为余额查询拒绝多 Key 凭据。
- 多 Key 自动关闭时保留 `balance_query_config_json`，删除当前 `relay_balance` 快照；后续恢复为单 Key 时仍保持关闭，由用户明确重新开启，不能自动恢复查询。
- `intervalMinutes` 必须为 1 至 10 的整数。
- `adapter=builtin` 时可保存系统最近一次严格查询成功的 `preferredBuiltinAdapter`；该字段不在前端显示，也不能由普通表单任意填写。
- `adapter=custom` 时不允许保存 `preferredBuiltinAdapter`。
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

### 3.4 多 Key 状态收敛

多 Key 是账户凭据和网关轮换能力，余额查询只是附属功能。创建和更新账户时按以下顺序收敛状态：

1. 先完成凭据合并、空值清理、重复 Key 校验和最终有效 Key 列表归一化。
2. 最终有效 Key 数量大于 1 时，把余额启用状态归一化为关闭，并在同一次业务账户写入中清空下次刷新时间；该路径返回账户保存成功，不返回余额能力错误。
3. 业务库关闭成功后，统计侧幂等删除 `relay_balance` 快照。业务库和统计库不做跨库强事务；即使快照删除稍有延迟，列表也因 `balance_query_enabled=false` 不展示，worker 也因下次刷新时间为空而不再领取。
4. 查询期间账户被保存为多 Key 时，复用现有配置版本与条件写保护丢弃旧查询结果；旧请求不能重新写回快照、下次刷新时间或启用状态。
5. 多 Key 后再减少为单 Key 时只恢复“可配置”资格，余额查询保持关闭；用户重新开启后才立即到期并开始查询。

前端提示用于解释自动收敛，后端最终状态是唯一事实来源。普通编辑、管理端编辑、直接管理 API 和导入创建都必须遵守同一规则，不能只修一个页面入口。

## 4. 适配器

适配器接口保持最小：

```ts
interface AccountBalanceAdapterDriver {
  query(context: AccountBalanceQueryContext): Promise<AccountBalanceQueryResult>
}
```

通用请求约束：

- 使用物理账户现有 Base URL、当前单一 API Key 和已绑定代理。
- 账户绑定 `proxyProfileId` 时，手动查询、首次自动探测、上线全量扫描和周期刷新都必须先通过代理仓储解析并使用该代理；禁止自动探测绕过账户代理直连。
- 仅发送 `GET`，一次内置适配识别共享最长 15 秒总 deadline，禁止重定向，响应体上限 256 KiB；不能把每个候选适配器各自放大为 15 秒。
- 15 秒是手动刷新、草稿测试、首次自动探测、上线扫描和周期刷新共用的单账户总预算，不新增用户配置项；后台刷新仍保持单轮最多 100 个、并发 4、同账户 30 秒租约，避免慢上游把任务放大为无界等待。
- 自定义路径必须是以 `/` 开头的相对路径；解析后必须与账户 Base URL 同源。
- 只解析 JSON；状态码、鉴权失败、超时、超限和字段错误映射为中文错误摘要。
- 外部查询失败不修改账户状态、最近错误、冷却、Key 运行态或网关调度事实；仅允许按本设计更新余额快照和余额自身的下次刷新时间。
- 多 Key 自动关闭不是外部查询失败，也不进入 `failed`、`unsupported` 或连续失败状态机。

首批内置规则：

| 适配器 | 路径 | 归一化 |
| --- | --- | --- |
| `sub2api` | `GET /v1/usage` | Key 独立额度优先，其次订阅最紧窗口，最后钱包；上游 USD 直接使用 |
| `newapi` | `GET /api/usage/token/`，必要时 `GET /api/status` | `total_available / quota_per_unit` 转 USD；`unlimited_quota=true` 只表示令牌层不限额，不代表账户无限余额，按 `unsupported` 继续回退 |
| `litellm` | `GET /key/info` | 有 `max_budget` 时返回 `max_budget - spend`，否则 `unsupported` |
| `user_balance` | `GET /user/balance` | 读取顶层 `balance`，按 USD 钱包余额归一化；与 CC Switch 通用模板一致 |
| `custom` | 用户配置的同源相对路径 | JSON Pointer 取余额，或总额减已用，再除以 divisor |

所有余额只能来自上述明确查询接口的响应，不允许根据站点类型、本地 usage、账户额度配置或兼容特征推算。解析器必须以真实响应 fixture 做回归，不根据字段名猜测币种；New API 的旧 billing 字段可能实际承载 CNY、Token 或内部 quota，因此不作为余额来源。其他中转统一使用受限自定义接口。

内置适配查询顺序固定为：先尝试 `preferredBuiltinAdapter`，再按 `sub2api -> newapi -> litellm -> user_balance` 补齐尚未尝试的规则。任一规则得到 `fresh` 或真正的账户级 `unlimited` 即成功并条件更新偏好；原偏好失败而其他规则成功时，用新规则替换偏好。全部规则都形成确定性不匹配或只返回 `unsupported` 时返回能力不支持，但仍按账户刷新周期等待下一轮；只要本轮存在临时错误，就进入连续失败并使用同一周期。账户 Base URL、API Key、代理或上游实现变化后，下一次查询会自动完成回退和偏好修正。

## 5. 后台刷新

当前生产运行时由 Node `ops-worker` 承接一个简单定时任务，每分钟执行一次：

1. 按索引读取状态可用、启用余额查询、非授权实例、单 API Key 的到期物理账户，worker 单轮总容量为 12 个，其中包含自愈预留名额。
2. 每轮固定为自愈保留 4 个名额，再用剩余容量处理正常到期账户。自愈补充“活动、可调度、已开启但刷新时间为空”的账户，包括历史版本留下的 `unsupported` 停止计划；SQLite 和 PostgreSQL 都使用进程内游标按有界页轮转扫描。扫描页大于本轮返回容量时，游标只推进到本轮最后一个实际检查或消费的账户，不能跳到尚未消费的页末；只有整页消费完成后才允许页末 wrap。
3. 到期账户按 `next_refresh_after ASC, account_id ASC` 稳定排序。
4. 以小并发执行查询；自动和手动查询复用现有 `background_job_leases`，以 `account-balance:<accountId>` 为租约 key、30 秒为租期，保证跨进程同一账户同一时刻只有一个查询。
5. 成功、临时失败、确定性失败和 `unsupported` 都写当前快照并按用户配置周期安排下次刷新；余额结果永远不改变账户状态、可调度性或用户开关。
6. 自动刷新在业务状态活动且可调度的基础上读取运行态，只执行 `normal/degraded` 或无阻断记录的账户；短暂避让、待探针、半开和探针失败只延后本轮，不推进到期时间，恢复后立即补查。运行态存储不可用时按数据库状态 fail-open，避免辅助状态源故障饿死余额任务。

账户停用或时间计划当前不可用时不领取。重新启用后，若快照已到期则下一轮立即刷新。worker 重启不丢任务，因为到期时间持久化在快照表中。

首期不引入 Redis 队列、独立任务表、余额专用租约表或余额历史表，只复用现有后台任务租约。多实例部署不在本计划范围；后续真正启用多节点 worker 时再接入现有用户分片方案。

## 6. 手动刷新

接口：

```http
POST /__aisys__/api/accounts/:id/balance/refresh
```

- 仅物理账户所有者或管理员可调用。
- 正式刷新不接收临时配置，账户必须已经保存、启用余额查询且为自有物理单 Key；人工刷新不受账户状态、可调度性或运行态限制。
- 请求同步等待外部查询，服务器超时 15 秒；前端图标在请求期间旋转。
- 同一账户已有查询时返回当前 `refreshing` 状态，不重复发起上游请求。
- 成功、无限和失败都返回本次查询结果 DTO；只有本地权限或参数错误使用 4xx。
- 手动刷新无论成功、临时失败或明确不支持，都保存本次结果并按账户周期安排下一次自动刷新。
- 前端收到最新快照后必须替换当前页的目标账户对象和列表数组引用，只重渲染该行余额；禁止为了显示新金额重新请求整张账户列表。当前列表使用 `shallowRef`，不能只修改数组内部对象的 `balanceSnapshot`。
- 列表人工刷新拿到本次结果后先替换当前行；失败或不支持立即显示“余额查询失败”，不能继续展示旧金额。
- 人工刷新允许对当前不可调用账户主动检测；失败只保存本次诊断语义，不能取消自动周期。

新增/编辑弹窗使用独立草稿测试接口：

```http
POST /__aisys__/api/accounts/balance/test-draft
```

- 请求体包含当前未保存账户草稿和当前余额查询配置，新建与编辑使用同一契约。
- 服务端只构造内存账户快照并请求上游，复用当前 Base URL、单 API Key、代理和适配器；草稿最终有效 Key 数大于 1 时直接返回“多 Key 账户不支持余额查询，保存后将自动关闭余额查询”，不能选取其中一个 Key 请求上游。
- 不保存账户配置，不修改开关，不写调度时间，不领取正式刷新租约，不删除或写入余额快照。
- 查询结果只返回当前请求；前端通过顶部消息提示结果，不改变表单、列表金额或已保存账户状态。
- 用户点击账户“确定”后才保存余额配置；保存后的后台任务按调度时间查询，不要求保存动作同步等待余额接口。
- “测试查询”按钮与“刷新周期”输入框放在同一个表单项内：桌面端位于输入框右侧，窄屏空间不足时在该表单项内换行并右对齐；删除单独占行的测试区域，非持久化说明保留在标题帮助提示中。

账户列表接口按当前页账户 ID 批量加载 `relay_balance` 快照，不逐行查询。普通用户只能看到自有物理账户余额；管理员管理视图可以看到所有物理账户。授权实例和被授权用户响应中不返回余额配置或快照。

## 7. 新账户非阻塞自动探测

新建物理单 Key API Key 账户且用户没有开启余额查询时，在首次激活健康检查成功并完成账户状态写回后，把余额探测任务投入 ops-worker 内存队列。投递是常数时间操作，创建 HTTP 请求不等待健康检查或余额请求。

探测规则：

1. 队列低并发执行；同一账户按 ID 去重，不重试，不改变健康检查结果。
2. 领取时重新读取账户，只接受 `active`、可调度、未删除、非授权实例、单 API Key、`balance_query_enabled=false`、`balance_query_config_json='{}'` 且配置版本仍等于首次检查版本的账户。配置 JSON 非空表示用户关闭过余额或账户曾因多 Key 自动关闭，不能作为“从未配置”的新账户重新开启。
3. 以 `adapter=builtin` 调用统一内置适配解析器：优先已有偏好，再尝试其余全部规则；复用账户 Base URL、代理、15 秒总超时、256 KiB 限制和严格 JSON 解析。HTML 200、字段缺失、鉴权失败和网络错误都不算支持。
4. 只有得到 `fresh` 或 `unlimited` 快照才算命中。命中后以账户 ID、关闭状态和预期配置版本为条件写入 `{ adapter: 'builtin', preferredBuiltinAdapter, intervalMinutes }` 并开启功能，同时保存本次快照和下次刷新时间。
5. 全部不命中时不写配置、不写失败快照，账户继续保持关闭，表示当前无法确认支持。
6. 用户在探测期间编辑账户、主动开启或关闭余额查询时，条件更新失败，后台结果不得覆盖用户选择。

SQLite 严格 writer 边界下，ops-worker 不直接写业务库或统计库：业务配置和调度时间通过 DB service 条件提交，租约与余额快照通过 stats-writer 写入；手动刷新所在的 DB service 也通过 server IPC 把统计写操作转交 stats-writer。查询结束时先按 `configRevision + balanceQueryConfig` 提交业务状态，stats-writer 写快照前再次核对当前配置。配置在请求期间发生变化时丢弃旧结果，不恢复旧快照和旧调度时间。

自动探测只随新账户首次激活执行一次；周期健康检查、旧账户和普通编辑不触发全量探测，避免持续增加上游请求。导入创建账户沿用同一首次激活流程。

本功能首次上线后在 release 根目录执行一次维护命令 `pnpm --filter juhe-ai-backend maintenance:backfill-account-balance`，覆盖所有系统账户作用域下尚未开启余额查询的合格物理账户。发布包保留该命令和编译脚本。PostgreSQL 可在主服务运行时后台执行；SQLite 必须先停止主服务并设置 `JUHE_AI_SQLITE_OFFLINE_MAINTENANCE_CONFIRMED=1`，由专用离线维护启动器独占 business/stats 写入，禁止与在线 DB service/worker 并行。命令按账户 ID 游标每页 50 条读取、并发 2 探测，逐页输出 `scanned/enabled/unsupported/stale` 进度；不把全部账户载入内存，也不阻塞 PostgreSQL 主服务。上线后的常态只保留新账户首次激活探测，不把全量扫描注册为周期任务。

## 8. 后台连续失败判定

周期刷新只对后台自动请求启用连续失败判定，人工列表刷新和新增/编辑草稿测试不参与该序列。

- 临时失败只表示本地连接、绝对超时、读取中断或未完成 framing。连续第 1、2 次临时失败只更新 `consecutiveTransientFailures`、`lastTransientErrorMessage`、`lastTransientFailureAt`，不把余额状态改成 `failed`；第 1、2、3 次及后续失败都按账户 `intervalMinutes` 安排下一次查询，不另行覆盖用户配置周期。完整 HTTP（无论状态码）和完整业务正文只作为余额诊断，不产生账户健康或能力语义。
- 已有 `fresh` 或 `unlimited` 快照时，前两次临时失败保留原状态、金额、依据和 `lastSuccessAt`；列表继续显示上次成功结果，并在 tooltip 中说明“刷新暂时失败（1/3 或 2/3）”。
- 从未成功且没有可保留结果时，前两次临时失败保存 `status='pending'`，列表显示“待重试（1/3 或 2/3）”，而不是“查询失败”。
- 连续第 3 次本地 transport 失败才保存 `status='failed'`、清除旧金额并显示最近错误；后续本地 transport 失败把计数封顶在 3，并继续按账户配置周期查询。完整 framing 中性，不推进该序列；正式查询成功按余额快照自身代次清理临时诊断并继续用户配置周期。
- 确定性结果只包括账户缺少 Base URL 或单一 API Key、自定义查询缺少配置、跨 Origin 路径、全部内置适配器确定性不匹配以及明确的本地配置校验失败；完整上游 HTTP 状态码不属于确定性能力判断。后台保存 `status='unsupported'` 和可解释原因，并继续写入下一次刷新时间；错误类型只影响展示与诊断，不决定是否继续调度。
- 多 Key 不属于确定性查询错误。账户写入边界会先自动关闭余额查询，因此 worker 和人工刷新不应收到一个“已启用但包含多个 Key”的有效账户状态。
- 内置适配器会继续按偏好和全量回退顺序识别。只要本轮存在本地 transport 未完成且最终未命中，就按临时错误累计；完整 HTTP 结果保持中性，全部候选都形成确定性不匹配或只返回 `unsupported` 时保存诊断并等待下一周期。
- 失败次数随现有 `relay_balance` 快照 JSON 保存，不新增数据库列、不重建非业务 schema。配置关闭或更换后删除旧快照，自然清零失败序列。
- `balance_query_enabled=1` 表示用户意愿开启，启用期间 `balance_query_next_refresh_at` 必须保持下一次计划；`relay_balance.status='unsupported'` 仅保存可解释原因。只有用户关闭余额查询、账户变成不支持的多 Key/非 API Key、或账户删除时才允许清空计划。

## 9. 安全边界

- API Key 只在服务端内存中用于当前上游请求，不返回前端、不写日志、不写错误摘要。
- 自定义 URL 必须复用现有上游 URL 安全校验和账户代理，不允许访问另一 Origin。
- 禁止重定向，避免同源入口把 Authorization 转发到其他主机。
- JSON Pointer 只读取已解析、受大小限制的 JSON，不支持表达式、模板、JavaScript 或动态代码。
- 错误摘要区分超时、HTTP 状态、鉴权失败、JSON 非法和字段不可解析，不包含完整响应正文。
- 余额查询不能改变账户可调度性，也不能作为自动停用、切号或告警依据。

## 10. 状态与删除

- 创建账户时默认关闭；首次健康检查成功后可按第 7 节自动开启。
- 编辑配置后如果开启，清空旧适配器快照并立即到期；如果关闭，保留配置但删除 `relay_balance` 快照，列表立即隐藏。
- 创建多 Key 账户时无条件保持关闭；单 Key 已开启账户新增第二个有效 Key 时，账户保存成功并自动关闭查询、清空调度时间、删除快照。
- 多 Key 账户减少为单 Key 后不自动重新开启，用户必须显式开启；这样不会把历史余额配置或旧快照误用于新的凭据集合。
- 有效 Key 数量以凭据归一化后的去空白、去重结果为准。标准创建、完整编辑、基础编辑、管理端、自有账户接口、批量编辑和导入最终都进入同一写入规则；不能在路由层用局部凭据或仅靠前端隐藏判断。
- 自动关闭与 `config_revision` 递增在同一业务库事务完成；`balance_query_config_json` 原样保留，原来为空时写入当前内置默认配置作为“已配置关闭”标记。事务提交后再通过 stats-writer 幂等删除 `relay_balance`，不引入跨库事务。
- 删除物理账户时沿用现有账户清理流程删除对应快照。
- 授权实例不复制来源账户配置或快照。
- 不保留旧 schema、旧字段或旧 DTO 兼容分支；上线按当前 schema 单独同步数据库。

## 11. 验收

- 五类适配器都有成功、无限/未提供、鉴权失败、超时和字段异常回归。
- 开启、关闭、修改间隔和修改适配器时，SQLite/PostgreSQL 读写一致。
- 自动刷新只处理到期且可用的物理单 Key API Key 账户，单轮有界且稳定排序。
- 后台临时错误前两次保留上次成功余额或显示“待重试”，连续第 3 次才显示“查询失败”并清空金额；每次临时失败都按账户配置的刷新周期继续查询，任意成功清零失败次数。
- NewAPI `unlimited_quota=true` 不再显示“无限”；其他适配器仍未命中时显示“余额查询失败”，用户开关和后台周期保持开启。
- 确定性不支持不关闭用户开关、不取消余额调度，也不改变账户状态。
- 列表人工刷新不会重复并发请求；失败时立即提示并写入本次失败快照，但不累计后台失败次数，仍按用户周期安排下一次自动刷新。
- 授权实例和被授权用户看不到余额；管理员和物理账户所有者权限正确。
- 列表按页批量补齐快照，不出现 N+1 查询。
- 前端桌面和移动端不重叠，刷新图标无按钮外观，加载旋转和悬浮错误正常。
- 点击列表余额刷新图标后，当前行金额立即使用接口返回快照更新，分页、筛选、排序和其他行不重新加载。
- 新账户创建响应不等待余额探测；首次激活后只在严格命中时自动开启，探测失败或配置版本变化时保持关闭。
- 新增和编辑弹窗“测试查询”使用当前表单草稿，无持久化副作用；成功金额只通过顶部消息显示。
- 编辑弹窗的“测试查询”位于“刷新周期”输入框右侧，不再单独占据一行；桌面和移动端无重叠或横向溢出。
- 新建多 Key 账户即使请求携带余额开启状态也能正常保存，最终返回 `balanceQueryEnabled=false` 且无余额刷新时间。
- 多 Key 草稿点击“测试查询”时返回明确的自动关闭原因，余额上游请求次数必须为 0；单 Key 草稿仍能正常测试。
- 单 Key 已开启账户增加第二个有效 Key 时保存成功，不出现“余额查询仅支持单 API Key”错误；余额行立即隐藏，后台不再领取，旧查询结果不能写回。
- 多 Key 账户减少为单 Key 后保持关闭，用户重新开启并保存后才恢复余额查询。
- 前端隐藏、基础编辑、完整编辑、管理端和直接 API 写入都不能绕过后端的多 Key 自动关闭规则。

## 12. 非目标

- 余额历史、趋势、告警和自动停用。
- 多 API Key 池逐 Key 余额。
- OAuth 订阅额度。
- 管理员连接器模板市场。
- 对旧账户、周期健康检查或所有中转持续自动探测。
- 任意 Header、第二套密钥、POST 请求或脚本解析。
- 余额参与本地用量、额度、路由、质量评分或账户健康状态。
- 因余额接口不支持、鉴权失败或网络失败自动关闭用户开关；多 Key 保存时的自动关闭属于凭据能力收敛，不属于查询失败处理。
