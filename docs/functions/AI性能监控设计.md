# AI 性能监控设计

## 文档目标

本文定义 `AI性能监控` 菜单的功能边界、权限口径、默认展示规则、接口契约和性能约束。该页面用于账号所有者查看自己 AI 账户在真实网关请求中的首 token 耗时和总耗时趋势，不替代使用记录明细、用量统计、统计概览或主机级系统监控。

## 功能定位

- `AI性能监控` 面向 AI 账户维度，关注账号真实请求性能。
- 数据事实源仍是 `usage_records`，但页面接口不能实时回扫明细表。
- 后台 worker 继续按游标把使用记录增量聚合到 `usage_stats_hourly`，页面只读取小时缓存。
- 第一版只展示平均值趋势，不做 P95 / P99 / 最大值；分位数能力需要额外直方图或分位缓存，后续单独设计。

## 菜单与权限

菜单名称固定为 `AI性能监控`。

| 入口 | 建议路由 | 建议接口 | 可见范围 |
| --- | --- | --- | --- |
| 用户侧 | `/my-ai-performance` | `GET /api/my-stats/ai-performance` | 当前登录系统账户名下的自有 AI 账户 |
| 用户侧账户选项 | `/my-ai-performance` | `GET /api/my-stats/ai-performance/accounts` | 当前登录系统账户名下的自有 AI 账户基础选项 |

权限规则：

- 用户侧强制当前登录用户作用域，忽略 `systemAccountId` 查询参数。
- 用户侧只看自己名下的自有 AI 账户，不把别人授权给自己的 AI 账户纳入默认账户池。
- 不提供管理侧入口；管理员如需查看，也只能在用户模式下查看自己名下的自有 AI 账户。
- 所有账户 ID 都必须在后端按权限重新校验，前端多选只能改善体验，不能作为权限依据。

## 监控图

页面主体只保留两个监控图：

1. `首token 耗时监控图`
   - 每个 AI 账户一条折线。
   - 横轴为小时。
   - 纵轴为平均首 token 耗时。
   - 计算公式：`first_token_ms_sum / first_token_ms_count`。

2. `总耗时 监控图`
   - 每个 AI 账户一条折线。
   - 横轴为小时。
   - 纵轴为平均总耗时。
   - 计算公式：`duration_ms_sum / duration_ms_count`。

窗口选项：

- 默认：近 1 日。
- 可选：近 1 日、近 3 日、近 7 日。
- 最大：近 7 日。
- 时间粒度固定为小时；近 7 日最多返回 168 个小时点。

空值规则：

- 某账户某小时没有请求时，对应点返回 `null` 或省略平均值，图上不应伪造成 `0ms`。
- 某账户某小时有请求但没有首 token 样本时，首 token 图返回空值；总耗时图仍可展示。
- tooltip 需要展示小时、账户名称、样本数和平均耗时。

## 默认账户池

为避免账户过多导致图表不可读，默认只展示最近 7 天活跃度最高的前 10 个 AI 账户。

默认账户池规则：

- 数据来源：`usage_stats_hourly`。
- 作用域：`scope_type = 'account'`。
- 活跃窗口：最近 7 天。
- 活跃条件：`SUM(request_count) > 0`。
- 活跃度排序：`SUM(request_count) DESC`。
- 稳定兜底排序：最近有数据的小时倒序、账户名称升序、账户 ID 升序。
- 默认数量：前 10 个。

默认账户池和图表窗口解耦：

- 即使图表窗口选的是近 1 日，默认账户池仍按最近 7 天活跃度选。
- 这样可以避免账号因为当天没有请求而频繁从默认监控图里消失。

账户状态：

- 默认池只返回当前仍存在的 AI 账户。
- 账户已停用但最近 7 天有调用时可以展示，并在前端用状态标签标明。
- 已删除账户不进入默认池；历史性能排查回到使用记录或审计日志。

## 临时指定账户

页面提供“指定账户”多选，用于临时追加查看不在默认前 10 内的账户。

规则：

- 用户侧只能选择自己名下的自有 AI 账户。
- 可多选，但第一版建议接口限制临时指定最多 20 个账户，避免图例和 payload 过大。
- 最终展示账户集合为“默认前 10 + 临时指定账户”，按账户 ID 去重。
- 临时指定不写数据库，不写系统设置，不写用户偏好，也不进入页面状态持久缓存。
- 离开页面、刷新页面或下次进入页面后，恢复默认最近 7 天活跃前 10。

## 接口契约

建议新增：

```text
GET /api/my-stats/ai-performance
GET /api/my-stats/ai-performance/accounts
```

查询参数：

| 参数 | 说明 |
| --- | --- |
| `window` | `last1d`、`last3d`、`last7d`，默认 `last1d` |
| `accountIds` | 临时指定账户 ID，支持逗号分隔或重复参数；不持久化 |

账户选项查询参数：

| 参数 | 说明 |
| --- | --- |
| `keyword` | 临时搜索关键词，匹配账户名称、账户 ID 或供应商编码 |
| `accountIds` | 已选账户 ID，用于搜索词变化后仍保留已选标签 |
| `limit` | 返回选项数量，默认 30，最大 50 |

建议响应：

```ts
interface AiPerformanceOverview {
  window: { key: 'last1d' | 'last3d' | 'last7d'; label: string; hours: number }
  defaultAccounts: AiPerformanceAccount[]
  selectedAccounts: AiPerformanceAccount[]
  accounts: AiPerformanceAccount[]
  hourlySeries: AiPerformanceAccountSeries[]
  summary: {
    requestCount: number
    firstTokenCount: number
    averageFirstTokenMs?: number
    durationCount: number
    averageDurationMs?: number
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
    durationCount: number
    averageDurationMs?: number
  }>
}
```

返回要求：

- `defaultAccounts` 表示后端按最近 7 天活跃度选出的默认前 10。
- `selectedAccounts` 表示本次请求里合法且可见的临时指定账户。
- `accounts` 是实际绘图账户集合，等于默认账户与临时指定账户去重后的结果。
- `hourlySeries` 必须按 `accounts` 顺序返回，便于前端颜色和图例稳定。
- 后端需要补齐窗口内小时桶；没有样本的小时返回 `requestCount = 0`，平均耗时为空。

## 查询策略

默认账户查询：

```sql
SELECT scope_id AS account_id, SUM(request_count) AS request_count_last_7d
FROM usage_stats_hourly
WHERE scope_type = 'account'
  AND stat_hour >= :activeSinceHour
  AND system_account_id = :systemAccountId
GROUP BY scope_id
HAVING SUM(request_count) > 0
ORDER BY request_count_last_7d DESC, MAX(stat_hour) DESC, scope_id ASC
LIMIT 10
```

绘图数据查询：

- 先确定账户集合。
- 读取这些账户对应 owner `system_account_id` 下的 `usage_stats_hourly`。
- 条件固定为 `scope_type = 'account'`、`scope_id IN (...)`、`stat_hour >= :windowSinceHour`。
- 查询按系统账户分组或分块执行，避免构造过长 SQL 和跨 owner 权限混淆。
- 不从 `usage_records` 做实时 `GROUP BY`。

## 性能与边界

主要性能风险和处理方式：

| 风险 | 影响 | 设计约束 |
| --- | --- | --- |
| 账户过多导致图表不可读 | 图例拥挤、渲染变慢 | 默认只显示最近 7 天活跃前 10；临时指定建议最多 20 个 |
| 前端请求实时汇总明细 | SQLite 高峰期被页面查询拖慢 | 接口禁止 `SUM usage_records`；只读小时缓存 |
| 统计缓存滞后 | 图表不是实时最新 | 返回并展示 `statsLagSeconds`，沿用现有统计 worker 滞后语义 |
| 临时指定账户过多 | 返回体和 ECharts series 过大 | 后端限制 `accountIds` 数量，前端限制多选数量并给中文提示 |
| 账户归属校验缺失 | 越权查看他人账号趋势 | AI 账户性能按账户所有者 `system_account_id + account_id` 聚合；接口只接受当前登录用户自有账户 |
| 账户选项复用账户列表 | 可能暴露凭据字段且大用户预加载不完整 | 账户多选使用轻量选项接口，只返回展示字段；前端远程搜索，不全量预加载 |

索引建议：

- 现有 `idx_usage_stats_hourly_scope_hour(system_account_id, scope_type, scope_id, stat_hour)` 适合指定系统账户和指定账户读取。
- 新增 `idx_usage_stats_hourly_scope_stat_hour(system_account_id, scope_type, stat_hour, scope_id)`，用于默认最近 7 天活跃前 10 先按小时窗口过滤再按账户聚合。
- 本功能不提供管理员全局查询，不需要跨用户全局 top 10 索引。

## 前端交互要求

- 页面文案、空态、错误提示和筛选项必须保持中文。
- 图表颜色要在 10 条默认线下可区分；超过 10 条时仍保持图例可滚动或可横向换行。
- 移动端可以保留窗口筛选、刷新和账户多选，但图表高度要固定，避免 legend 或 tooltip 挤压页面。
- “指定账户”需要提示“仅本次查看生效，下次进入仍显示活跃前 10”。
- “指定账户”使用远程搜索选项，避免拉取完整账户列表；选项接口不能返回 credentials、notes 或授权账户字段。
- 重置筛选只清空临时指定账户；不会修改默认前 10 规则。

## 验收标准

- 用户侧进入 `AI性能监控`，默认看到自己最近 7 天活跃前 10 个自有 AI 账户的首 token 和总耗时趋势。
- 管理员在用户模式进入 `AI性能监控`，也只能看到自己名下的自有 AI 账户。
- 临时指定多个账户后，图表展示默认前 10 加指定账户；刷新或重新进入后恢复默认前 10。
- 接口只读取 `usage_stats_hourly` 和账户 / 系统账户元数据，不实时扫描 `usage_records`。
- 近 1 日、近 3 日、近 7 日窗口都按小时展示，并正确处理空小时。
- 统计滞后、空态、无权限和参数非法都返回或展示中文提示。
