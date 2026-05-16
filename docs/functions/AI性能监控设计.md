# AI 性能监控设计

## 文档目标

本文定义 `AI性能监控` 菜单的功能边界、权限口径、默认展示规则、接口契约和性能约束。该页面用于用户查看自己 AI 账户在真实网关请求中的首 token 耗时和总耗时趋势；管理员可在管理侧按用户维度查看指定用户或全部用户的自有 AI 账户性能。不替代使用记录明细、用量统计、统计概览或主机级系统监控。

## 功能定位

- `AI性能监控` 面向 AI 账户维度，关注账号真实请求性能。
- 数据事实源仍是 `usage_records`，但页面接口不能实时回扫明细表。
- 后台 worker 继续按游标把使用记录增量聚合到 `usage_stats_hourly`，页面只读取小时缓存。
- 当前页面展示平均值和最大值趋势，不做 P95 / P99；分位数能力需要额外直方图或分位缓存，后续单独设计。

## 菜单与权限

菜单名称固定为 `AI性能监控`。

| 入口 | 当前路由 | 当前接口 | 可见范围 |
| --- | --- | --- | --- |
| 用户侧 | `/my-ai-performance` | `GET /api/my-stats/ai-performance` | 当前登录系统账户名下的自有 AI 账户 |
| 用户侧账户选项 | `/my-ai-performance` | `GET /api/my-stats/ai-performance/accounts` | 当前登录系统账户名下的自有 AI 账户基础选项 |
| 管理侧 | `/ai-performance` | `GET /api/stats/ai-performance` | 全部用户或指定系统账户名下的自有 AI 账户 |
| 管理侧账户选项 | `/ai-performance` | `GET /api/stats/ai-performance/accounts` | 全部用户或指定系统账户名下的自有 AI 账户基础选项 |

权限规则：

- 用户侧强制当前登录用户作用域，忽略 `systemAccountId` 查询参数。
- 用户侧只看自己名下的自有 AI 账户，不把别人授权给自己的 AI 账户纳入默认账户池、搜索结果或临时追加账户。
- 管理侧只允许管理员访问，支持 `systemAccountId` 筛选指定用户；未指定时读取全局 `account` 统计缓存和全局排行快照。
- 统计视图按账户拥有者真实账户维度读取，拥有者看到的是该 AI 账户整体性能；自己调用和被授权人调用都会进入同一账户曲线。
- 所有账户 ID 都必须在后端按权限重新校验，前端搜索添加只能改善体验，不能作为权限依据。

## 监控图

页面主体包含四个监控图：

1. `平均首token耗时监控图`
   - 每个 AI 账户一条折线。
   - 横轴为小时。
   - 纵轴为平均首 token 耗时。
   - 计算公式：`first_token_ms_sum / first_token_ms_count`。

2. `最大首 token 耗时监控图`
   - 每个 AI 账户一条折线。
   - 横轴为小时。
   - 纵轴为最大首 token 耗时。
   - 计算公式：小时桶中的 `first_token_ms_max`。

3. `平均总耗时监控图`
   - 每个 AI 账户一条折线。
   - 横轴为小时。
   - 纵轴为平均总耗时。
   - 计算公式：`duration_ms_sum / duration_ms_count`。

4. `最大总耗时监控图`
   - 每个 AI 账户一条折线。
   - 横轴为小时。
   - 纵轴为最大总耗时。
   - 计算公式：小时桶中的 `duration_ms_max`。

日期范围：

- 页面默认最近 3 天，最大最近 31 天。
- 时间粒度固定为小时；默认最近 3 天返回 72 个小时点，最近 31 天最多返回 744 个小时点。

空值规则：

- 某账户某小时没有请求时，对应点返回 `null` 或省略平均值，图上不应伪造成 `0ms`。
- 某账户某小时有请求但没有首 token 样本时，首 token 图返回空值；总耗时图仍可展示。
- 前端折线应跨过空小时保持连续，但空小时不显示采样点，tooltip 仍按无样本处理。
- tooltip 需要展示小时、账户名称、样本数和当前指标值。

## 默认账户池

为避免账户过多导致图表不可读，默认只展示最近 7 天活跃度最高的前 10 个 AI 账户。

默认账户池规则：

- 数据来源：`usage_rank_snapshots` 中 `account + last7d + request_count` 的最新快照。
- 作用域：`scope_type = 'account'`，即账户所有者的真实账户总量。
- 活跃窗口：最近 7 天。
- 活跃条件：快照 `metric_value > 0`。
- 活跃度排序：按快照 `rank` 升序。
- 快照缺失时默认账户池为空，接口不能临时聚合 `usage_stats_hourly` 降级。
- 默认数量：前 10 个。

默认账户池和图表窗口解耦：

- 即使页面日期范围不是最近 7 天，默认账户池仍按最近 7 天活跃度选。
- 这样可以避免账号因为当天没有请求而频繁从默认监控图里消失。

账户状态：

- 默认池只返回当前仍存在的 AI 账户。
- 账户已停用但最近 7 天有调用时可以展示，并在前端用状态标签标明。
- 已删除账户不进入默认池；历史性能排查回到使用记录或审计日志。

## 账户列表与筛选

页面提供“搜索并添加账户”和筛选区下方的账户列表，两者语义分离：

- “搜索并添加账户”只负责把不在默认前 10 内的可见账户临时加入当前账户列表；用户侧仅限自有账户，管理侧按筛选范围决定可见账户。
- 账户列表负责控制图表视图：未点选任何账户时，四个监控图展示当前账户列表内的全部账户；点选 A / B / C 后图表只展示 A / B / C；再次点击某个账户可取消该账户；取消全部选择或点击重置后恢复展示当前默认列表。页面顶部摘要读取后端窗口快照，不随前端点选账户重算。
- 搜索添加账户时，如果当前已经存在点选筛选，则新添加账户也进入点选集合，避免用户添加后看不到变化。

规则：

- 用户侧只能选择自己名下的自有 AI 账户。
- 搜索追加和 `accountIds` 参数都不能把别人授权给自己的账户带入页面。
- 可添加多个账户，但后端和前端限制临时追加最多 20 个账户，避免图例和 payload 过大。
- 当前账户列表集合为“默认前 10 + 搜索追加账户”，按账户 ID 去重。
- 点击账户列表产生的筛选只在前端当前页面生效，不回写后端，不作为查询参数。
- 搜索追加账户和点击筛选都不写数据库，不写系统设置，不写用户偏好，也不进入页面状态持久缓存。
- 离开页面、刷新页面或下次进入页面后，恢复默认最近 7 天活跃前 10。

## 接口契约

当前接口：

```text
GET /api/my-stats/ai-performance
GET /api/my-stats/ai-performance/accounts
GET /api/stats/ai-performance
GET /api/stats/ai-performance/accounts
```

查询参数：

| 参数 | 说明 |
| --- | --- |
| `startDate` | 日期范围开始，格式 `YYYY-MM-DD`；接口缺省为今天，页面首次进入显式传最近 3 天范围 |
| `endDate` | 日期范围结束，格式 `YYYY-MM-DD`；接口缺省为今天，最大最近 31 天 |
| `systemAccountId` | 仅管理侧有效；筛选指定系统账户，缺省为全部用户全局缓存 |
| `accountIds` | 搜索追加到账户列表的临时账户 ID，支持逗号分隔或重复参数；不持久化；不表示当前点击筛选 |

账户选项查询参数：

| 参数 | 说明 |
| --- | --- |
| `keyword` | 临时搜索关键词，匹配账户名称、账户 ID 或供应商编码 |
| `accountIds` | 已追加到账户列表的账户 ID，用于搜索词变化后仍保留选项标签 |
| `limit` | 返回选项数量，默认 30，最大 50 |

当前响应：

```ts
interface AiPerformanceOverview {
  range: { startDate: string; endDate: string; days: number; maxDays: number }
  defaultAccounts: AiPerformanceAccount[]
  selectedAccounts: AiPerformanceAccount[]
  accounts: AiPerformanceAccount[]
  hourlySeries: AiPerformanceAccountSeries[]
  summary: {
    requestCount: number
    firstTokenCount: number
    averageFirstTokenMs?: number
    maxFirstTokenMs?: number
    durationCount: number
    averageDurationMs?: number
    maxDurationMs?: number
  }
  statsLagSeconds: number
}

interface AiPerformanceAccount {
  id: string
  name: string
  status: string
  providerCode: string
  systemAccountId: string
  systemAccountName?: string
  requestCountLast7d: number
  selected: boolean
  defaultVisible: boolean
}

interface AiPerformanceAccountOption {
  id: string
  name: string
  status: string
  providerCode: string
  systemAccountId: string
  systemAccountName?: string
  requestCountLast7d: number
}

interface AiPerformanceAccountSeries {
  accountId: string
  accountName: string
  systemAccountId: string
  points: Array<{
    statHour: string
    requestCount: number
    firstTokenCount: number
    averageFirstTokenMs?: number
    maxFirstTokenMs?: number
    durationCount: number
    averageDurationMs?: number
    maxDurationMs?: number
  }>
}
```

返回要求：

- `defaultAccounts` 表示后端按最近 7 天活跃度选出的默认前 10。
- `selectedAccounts` 沿用接口字段名，表示本次请求里合法且可见的搜索追加账户，不表示前端点击筛选状态。
- `accounts` 是当前账户列表集合，等于默认账户与搜索追加账户去重后的结果。
- `hourlySeries` 必须按 `accounts` 顺序返回，便于前端颜色和图例稳定。
- 后端需要补齐窗口内小时桶；没有样本的小时返回 `requestCount = 0`，平均耗时为空。

## 查询策略

默认账户查询：

```sql
SELECT scope_id AS account_id, metric_value AS request_count_last_7d
FROM usage_rank_snapshots
WHERE system_account_id = :systemAccountId
  AND scope_type = 'account'
  AND window_key = 'last7d'
  AND metric = 'request_count'
  AND snapshot_at = :latestSnapshotAt
ORDER BY rank ASC
LIMIT 10
```

绘图数据查询：

- 先确定账户集合。
- 用户侧和管理侧指定用户时读取对应 owner `system_account_id` 下的 `usage_stats_hourly`；管理侧全部用户读取 `system_account_id = global` 的 `account` 缓存。
- 条件固定为 `scope_type = 'account'`、`scope_id IN (...)`、`stat_hour >= :windowSinceHour`。
- 查询按系统账户或全局缓存分块执行，避免构造过长 SQL 和跨 owner 权限混淆。
- 不从 `usage_records` 做实时 `GROUP BY`。
- 页面顶部摘要读取 `ai_performance_summary_windows`，不能由接口或前端把当前账户曲线再次相加 / 平均后生成摘要。

## 性能与边界

主要性能风险和处理方式：

| 风险 | 影响 | 设计约束 |
| --- | --- | --- |
| 账户过多导致图表不可读 | 图例拥挤、渲染变慢 | 默认只显示最近 7 天活跃前 10；搜索追加最多 20 个，用户可点击账户列表只看部分账户 |
| 前端请求实时汇总明细 | SQLite 高峰期被页面查询拖慢 | 接口禁止 `SUM usage_records`；趋势只读小时缓存，摘要只读窗口快照 |
| 统计缓存滞后 | 图表不是实时最新 | 返回并展示 `statsLagSeconds`，沿用现有统计 worker 滞后语义 |
| 搜索追加账户过多 | 返回体和 ECharts series 过大 | 后端限制 `accountIds` 数量，前端限制添加数量并给中文提示 |
| 账户归属校验缺失 | 越权查看他人账号趋势 | 用户侧 AI 账户性能按账户所有者 `system_account_id + account_id` 聚合且只接受当前登录用户自有账户；管理侧由管理员权限和 `systemAccountId` 筛选决定范围，授权给自己的账户不会进入用户侧 |
| 账户选项复用账户列表 | 可能暴露凭据字段且大用户预加载不完整 | 账户搜索添加使用轻量选项接口，只返回展示字段；前端远程搜索，不全量预加载 |

索引：

- 现有 `idx_usage_stats_hourly_scope_hour(system_account_id, scope_type, scope_id, stat_hour)` 适合指定系统账户和指定账户读取。
- 新增 `idx_usage_stats_hourly_scope_stat_hour(system_account_id, scope_type, stat_hour, scope_id)`，用于默认最近 7 天活跃前 10 先按小时窗口过滤再按账户聚合。
- 管理员全部用户视图复用 `system_account_id = global` 的 `account` 统计缓存和排行快照，不回扫明细。

## 前端交互要求

- 页面文案、空态、错误提示和筛选项必须保持中文。
- 图表颜色要在 10 条默认线下可区分；超过 10 条时仍保持图例可滚动或可横向换行。
- 移动端可以保留窗口筛选、刷新和账户搜索添加，但图表高度要固定，避免账户列表或 tooltip 挤压页面。
- “搜索并添加账户”使用远程搜索选项，避免拉取完整账户列表；选项接口不能返回 credentials、notes 或授权账户字段。
- 筛选区下方账户列表必须可点击；无点选时统计当前列表全部账户，有点选时只统计点选账户。
- 重置清空搜索追加账户和点击筛选状态；不会修改默认前 10 规则。

## 验收标准

- 用户侧进入 `AI性能监控`，默认日期范围为最近 3 天，默认账户池为自己最近 7 天活跃前 10 个自有 AI 账户。
- 管理员在管理模式进入 `AI性能监控`，可按用户筛选或查看全部用户全局账户性能，默认日期范围为最近 3 天。
- 别人授权给自己的 AI 账户不出现在默认账户池、搜索结果或 `accountIds` 临时追加结果中；自己名下账户的性能统计包含被授权人调用。
- 搜索添加多个账户后，这些账户进入筛选区下方账户列表；未点选时统计当前列表全部账户，点选后统计卡片和图表只展示点选账户；刷新或重新进入后恢复默认前 10。
- 接口只读取 `usage_stats_hourly` 和账户 / 系统账户元数据，不实时扫描 `usage_records`。
- 最近 31 天内的日期范围都按小时展示，并正确处理空小时。
- 统计滞后、空态、无权限和参数非法都返回或展示中文提示。
