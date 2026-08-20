# AI 健康监控设计

> 状态：已确认并实施。功能只监控账户能否正常使用，不判断失败责任归属。

## 1. 目标

以账户列表展示最近最多 31 天的健康检查结果，每小时一个状态槽位，支持账户名搜索、分页和自适应高度。

页面只回答一个问题：

> 这个账户在该次健康检查时能否正常使用？

页面不判断失败来自账户、供应商、中转平台还是网络，也不维护供应商错误码词典。

## 2. 三态口径

| 状态 | 颜色 | 页面文案 | 判定 |
| --- | --- | --- | --- |
| `success` | 绿色 | 可用 | 健康检查成功完成。 |
| `failure` | 红色 | 不可用 | 健康检查没有成功完成。余额、Key、权限、模型、服务器和网络问题均属于不可用。 |
| `unknown` | 灰色 | 无记录 | 该小时没有健康检查结果。 |

失败原因只用于详情展示，不参与颜色分类。能取得 `statusCode`、`errorCode` 或 `errorMessage` 时原样返回；没有具体原因时显示通用提示。

“不可用”不等于“供应商服务器故障”，只表示该账户当时不能完成健康检查。

## 3. 小时聚合

- 迁移前历史取 `traffic_source = account_health_check` 的使用记录，并按统计时区聚合到 `account_health_hourly`。
- J1 Go-owner 启用后，当前窗口内的健康事实从 jobs-owned outcome store 只读取得；页面按当前页账户和时间窗查询，不把 outcome 回写为 usage record 或 `account_health_hourly`。同一账户同一小时取最新的非 `stale` outcome；`complete_success` 为 `success`，其他已完成 outcome 为 `failure`。
- 同一账户同一小时有多次结果时，以 `created_at, id` 较新的记录为准。
- 成功写入 `success`，失败写入 `failure`，没有聚合行时由查询层补为 `unknown`。
- 页面请求不扫描使用记录分片；它读取已有小时预聚合历史，并在 J1 Go-owner 模式合并只读 outcome。

可用率口径：

```text
账户可用率 = 可用小时数 / (可用小时数 + 不可用小时数)
```

无记录不进入分母；没有有效检查时显示 `--`。

## 4. 数据结构

小时表 `account_health_hourly` 保留：

```text
account_id
system_account_id
provider_code
stat_hour
status
last_observed_at
last_record_id
status_code
error_code
error_message
updated_at
```

列表小时点只保留 Canvas 绘制所需字段：

```ts
interface AiHealthHourPoint {
  statHour: string
  status: 'success' | 'failure' | 'unknown'
}
```

用户点击非 `unknown` 小时槽后，详情接口才返回：

```ts
interface AiHealthHourDetail extends AiHealthHourPoint {
  lastObservedAt?: string
  statusCode?: number
  errorCode?: string
  errorMessage?: string
}
```

不新增失败分类、供应商来源、规则 ID、证据等级或第四种状态。

## 5. API

管理端：

```text
GET /api/stats/ai-health
```

用户端：

```text
GET /api/my-stats/ai-health
```

单点小时详情：

```text
GET /api/stats/ai-health/hour-detail?accountId=...&statHour=YYYY-MM-DDTHH
GET /api/my-stats/ai-health/hour-detail?accountId=...&statHour=YYYY-MM-DDTHH
```

列表响应只返回当前页面渲染字段以及 `items / hasMore / page / pageSize` 分页信息，不执行总数查询，也不以渐进下界伪装真实 `total`；页面只展示明确的上一页 / 下一页交互。不返回时区、范围重复元数据、所有者内部字段或小时错误正文。详情读取先按当前管理 / 自助作用域确认账户可见性；不可见统一返回 `404`。`unknown` 槽位直接由前端展示“无记录”，不发详情请求。

查询参数：

| 参数 | 默认值 | 边界 |
| --- | --- | --- |
| `hours` | `168` | `1..744` |
| `keyword` | 空 | 账户名搜索 |
| `page` | `1` | 正整数 |
| `pageSize` | `20` | `10..50` |

后端先按权限、账户名搜索和最近使用时间稳定分页，再批量读取当前页账户的小时结果。账户名包含搜索复用增量维护的 `account_name_search_terms / account_name_search_documents` 候选表，不扫描 `lower(accounts.name)`；默认按 `accounts.last_used_at DESC NULLS LAST` 的等价跨存储表达式排序，最后按 `name, id` 兜底，没有近期使用记录的账户自然排在后面。列表排序只读取业务库账户快照，并由管理 / 自助作用域各自的窄排序索引承接，不跨库读取质量统计，也不扫描使用记录明细。

## 6. 页面

菜单位于 AI 管理区域：

- 管理入口：`/ai-health`
- 用户入口：`/my-ai-health`

页面使用账户卡片列表，不使用表格：

- 工具栏提供账户名搜索、时间范围和刷新。
- 图例位于刷新按钮同一行最右侧，显示“可用、不可用、无记录”。
- 页面不显示内部统计时区。
- 中间账户列表占用剩余高度并独立滚动。
- 底部上一页 / 下一页保持可见，翻页、搜索和切换范围后列表回到顶部；不展示未经总数查询证明的伪总数。
- 每个账户展示当前状态、最近检查、下次检查、三态数量和账户可用率。
- 正常账户默认每 1 小时主动检查一次，并按账户 ID 在 0 到 10 分钟内稳定错峰；小时状态条是展示粒度，不要求所有账户在同一分钟执行。
- 小时状态使用单个 Canvas 绘制，避免 31 天产生 744 个 DOM 节点。
- 鼠标悬停只显示列表已有的小时与状态；点击非缺测状态槽后按需加载详情，展示检查时间、状态、HTTP 状态、错误码和错误原因。
- 页面隐藏时不发起首屏请求，并取消仍在途的列表读取；页面不建立自动轮询，仅用户主动刷新。

## 7. 性能边界

- 单次最多返回 50 个账户、每账户最多 744 个小时点。
- 查询只读取当前页账户，不加载全部账户后前端分页。
- 小时结果通过账户 ID 分块批量查询，禁止逐账户查询。
- 列表小时查询只投影 `account_id / stat_hour / status / source_order`；检查时间、状态码和错误正文只允许单点详情查询读取。
- 状态条使用 Canvas；页面不为小时点创建大量组件或监听器。
- 原始使用记录继续由 stats worker 增量聚合，接口请求不做临时统计。

## 8. Mockdata

Mockdata 默认覆盖：

- 超过一页的账户数量。
- 最近最多 31 天的逐小时记录。
- 可用、不可用和无记录三种状态。
- 带 HTTP 状态、错误码和错误消息的失败样本。
- 可按 `造数-` 搜索的账户名。

Mock 必须通过真实使用记录聚合器写入 `account_health_hourly`，不能在前端硬编码状态。

## 9. 明确不做

- 不判断失败是谁的责任。
- 不识别官网、中转平台或供应商来源。
- 不维护错误码白名单、黑名单或动态规则引擎。
- 不增加“账户受限”“已排除”等额外状态。
- 不根据单次监控结果自动停用账户。
- 不把健康监控变成供应商故障诊断系统。

## 10. 验收标准

- 成功显示绿色“可用”，失败显示红色“不可用”，缺测显示灰色“无记录”。
- 余额、Key、权限、模型、服务器或网络导致的失败均保持红色，但详情可以展示实际错误原因。
- 最近最多查看 31 天，账户名搜索和分页由后端执行。
- 列表自适应剩余高度，分页始终可操作。
- 图例与刷新按钮同行且不显示时区。
- 点击小时槽可以查看失败原因。
- Mockdata 至少产生两页账户及三态小时记录。
- 页面查询不扫描使用记录明细，31 天状态条不展开为 744 个 DOM 节点。
