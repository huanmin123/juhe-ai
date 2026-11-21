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
- `account_health_hourly` 是列表与详情的主读面。两个写入臂自 2026-11-21（T217）与 [模型价格与用量统计口径](模型价格与用量统计口径.md) §2.9 的健康率口径统一：
  - jobs 按 J1 outcome 滚动投影小时条：同一账户同一小时取最新的质量 outcome；`complete_success` 写 `success`，质量终态失败（`upstream_failure`、`probe_task_failure` 等）写 `failure`，非质量终态（`upstream_http_failure` 及历史别名 `framing_complete_neutral`、`opaque_upstream`、`gateway_capacity`、`gateway_policy`、`downstream_closed`）不写小时条，该小时由后续质量终态 outcome 决定或保持无条（读面显示 `unknown`）。写侧名单与 gateway 读面 `j1NonQualityOutcomes` 三处成对同步。
  - statsagg 聚合臂（`account_health_check` 使用记录）只消费成功行与真实账号失败行：失败行须带 `account_upstream` / `account_dependency` 归因才写 `failure`，`opaque_upstream` 等非质量归因与无归因失败（探针补记的 `upstream_http_failure`）不计入。
- T218 前置语义（2026-11-21）：jobs 小时条写入排在投影消费水位打标**之前**——写入失败停轮保留消费水位，下轮 drain 重放重试（条带 upsert newest-wins 幂等），消费过的 outcome 小时条必不丢失。取舍：stats 库故障期间 J1 投影消费暂停（current_state 滞后），恢复后从队头自动续跑；`account_health_hourly_record_failed` warn 事件即该停轮信号。
- J1 Go-owner 启用后，页面按三类读取 jobs-owned outcome store：①小时表未覆盖的缺失小时连续区间；②新鲜度区间（下界 `now-2h`，起点取该账户最新小时观测与该下界的较早者；即使该小时已有小时行，也按 latest-wins 合并，用于吸收投影滞后或迟到 outcome）；③区间未覆盖整窗的账户读取窗口内最新一条质量终态，用于表头，并同时参与其所在小时的条带合并。页面不把 outcome 回写为 usage record 或 `account_health_hourly`。J1 质量终态口径：`complete_success` 为 `success`；`upstream_http_failure`（历史别名 `framing_complete_neutral`）、`opaque_upstream`、`gateway_capacity`、`gateway_policy`、`downstream_closed` 与 `stale` 不计入健康率；其余质量终态（`upstream_failure`、`probe_task_failure` 等）为 `failure`。
- 同一账户同一小时有多次结果时，以 `created_at, id` 较新的记录为准；J1 行之间保持 latest-wins（`observed_at ASC, outcome_id ASC`，同 `observed_at` 取较大 `outcome_id`）。小时行与 J1 行同 `observed_at` 时当前实现由后追加的 J1 行胜出；小时行没有可比较的 outcome ID，跨源 tie 不按 `outcome_id` 判定。
- 成功写入 `success`，失败写入 `failure`，没有聚合行时由查询层补为 `unknown`。
- 页面请求不扫描使用记录分片；它先读取已有小时预聚合历史，再按账户读取 J1 质量终态补齐缺失小时与最新新鲜度空档，并单独读取每账户窗口内最新一条质量终态用于「最近检查 / 下次检查」；探针行同时参与条带合并。

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

列表读取不按整窗扫描 J1 outcome：先读取当前页账户在时间窗内的 `account_health_hourly` 行；对每个账户计算窗口内缺失小时的连续区间，并加上新鲜度区间（起点取该账户最新小时观测与 `now-2h` 的较早者、且不早于窗口起点，到窗口末端）。本地小时桶按 15 分钟采样构建：DST 过渡的局部小时可见，fall-back 重复的本地小时取最后一次出现去重（展示桶序可能非单调，缺失区间合并按 UTC min/max 扩展）。J1 只读取这些区间（`VALUES (account_id, observed_after, observed_before)` + `LATERAL` 分批查询，每批最多 2048 个区间，走 `(account_id, observed_at)` 覆盖索引）；没有小时行或小时时间戳非法的账户回退到完整窗口 `[now-(hours+2)h, now+2h)`。并对区间未覆盖整窗的账户，在同一连接上按账户读取窗口内最新一条质量终态用于表头；该行也参与其所在小时的 latest-wins 合并（较早空档行不证明表头已最新：更新的质量终态可能落在两个区间之间的已覆盖小时）。小时行已覆盖且不在新鲜度区间内的小时不做整小时回读，但可能由表头探针读取一条最新质量终态。表头探针失败时保留已读区间数据并记录 warn，表头回退账户业务投影字段（`accounts.last_health_check_at / last_health_success_at / next_health_check_at`）。详情只读取 `statHour` 对应的本地小时区间；DST fall-back 的重复小时覆盖全部 occurrence，spring-forward 跳过的小时直接返回无 J1 数据。

后端先按权限、账户名搜索和最近使用时间稳定分页，再批量读取当前页账户的小时结果。账户名包含搜索直读 `accounts.name`：`accounts.name COLLATE "C" ILIKE '%' || escaped(keyword) || '%' ESCAPE '\'`，先落 `system_account_id` 作用域；所属人视图由 `(system_account_id, name, id)` 索引承接，全局管理员视图由 `pg_trgm` GIN 索引 `idx_accounts_name_c_trgm_lookup` 兜底，不再维护派生词项表（2026-11-03 T119 去索引化）。默认按 `accounts.last_used_at DESC NULLS LAST` 的等价跨存储表达式排序，最后按 `name, id` 兜底，没有近期使用记录的账户自然排在后面。列表排序只读取业务库账户快照，并由管理 / 自助作用域各自的窄排序索引承接，不跨库读取质量统计，也不扫描使用记录明细。

## 6. 页面

从侧栏「统计与监控」进入「账户健康」。管理入口为 `/ai-health`，用户入口为 `/my-ai-health`；旧路径仍可直达。
- 标题旁注明「全部账户」或「我的账户」。筛选栏保留独立的最近检查窗口。

页面使用账户卡片列表，不使用表格：

- 工具栏提供账户名搜索、时间范围和刷新。
- 图例位于刷新按钮同一行最右侧，显示“可用、不可用、无记录”。
- 页面不显示内部统计时区。
- 中间账户列表占用剩余高度并独立滚动。
- 底部上一页 / 下一页保持可见，翻页、搜索和切换范围后列表回到顶部；不展示未经总数查询证明的伪总数。
- 每个账户展示当前状态、最近检查、下次检查、三态数量和账户可用率；J1 当前窗口有质量终态 outcome 时，最近/下次检查展示对应的最新质量终态（非质量终态不计入，见 §3），不因业务投影字段仅保留旧值而误导用户。
- 正常账户默认每 1 小时主动检查一次，并按全局被动调度策略每轮重新偏移；小时状态条是展示粒度，不要求所有账户在同一分钟执行。
- 手动模式账户（账户级「后台自动探测」总开关关闭，见 [AI 账户后台自动探测总开关设计](AI账户后台自动探测总开关设计.md)）显示「手动」标签；最近 / 下次独立检查显示「主动探测：已关闭」；历史小时条与可用率保留，并标注「关闭后无新检查」。
- 小时状态使用单个 Canvas 绘制，避免 31 天产生 744 个 DOM 节点。
- 鼠标悬停只显示列表已有的小时与状态；点击非缺测状态槽后按需加载详情，展示检查时间、状态、HTTP 状态、错误码和错误原因。
- 页面隐藏时不发起首屏请求，并取消仍在途的列表读取；页面不建立自动轮询，仅用户主动刷新。

## 7. 性能边界

- 单次最多返回 50 个账户、每账户最多 744 个小时点。
- 查询只读取当前页账户，不加载全部账户后前端分页。
- 小时结果通过账户 ID 分块批量查询，禁止逐账户查询。
- 列表小时查询只投影 `account_id / stat_hour / status / last_observed_at`；状态码和错误正文只允许单点详情查询读取。
- PostgreSQL J1 读取以小时表为前置：列表先读当前页账户的小时行，再按账户取「缺失小时的连续区间 + 新鲜度区间（起点取该账户最新小时观测与 `now-2h` 的较早者、且不早于窗口起点，到窗口末端）」；小时行已覆盖且不在新鲜度区间内的小时不做整小时回读。增量读取用 `VALUES (account_id, observed_after, observed_before)` + `LATERAL` 按区间分批查询（每批最多 2048 个区间），只投影合并所需实体列（`outcome_id / account_id / outcome / observed_at / next_due_at`），不回传探针 payload 正文；J1 行之间保持 latest-wins（同 `observed_at` 取 `outcome_id` 较大者）。仅对区间未覆盖整窗的账户回读窗口内最新一条质量终态用于表头，该行同时参与其所在小时的条带合并；详情只读目标本地小时区间。
- J1 读取总预算 5s 覆盖区间分批查询与表头探针；下界收敛后单页读取量从「整窗全部 outcome」降为「缺失小时 + 最新空档 + 每账户最新一条」，预期避免慢盘冷缓存首开再次打满预算（待发布后按 T216 验证）。表头探针失败时保留区间结果并记录 `ai_health_latest_probe_failed` warn，表头回退账户业务投影字段（`accounts.last_health_* / next_health_check_at`）；缺失区间分批查询在预算截止（`context.DeadlineExceeded`，客户端取消与非超时数据库错误仍按原错误上抛）时返回已读部分（含失败前当前批已扫描行）并记录 `ai_health_gap_read_degraded` warn，此时表头同样回退账户业务投影字段，未读小时按 `unknown` 展示，不转 500；表头探针失败一律降级不中断页面。
- 已知限制：①新鲜度区间起点为 `min(该账户最新小时行 last_observed_at, now-2h)`，起点之后可纠偏，早于起点且非账户最新质量终态的写失败小时不保证与原始 outcome 的 latest-wins 完全一致（投影整体停顿时最新小时行随之后退，区间会前移覆盖停顿期）；T218 前置重写实施后新写入不再丢条，存量（前置上线前已消费但小时条写失败/非质量误写的行）仍有 35 天滚动窗自愈，存量修复见 [待办与阻塞事项](../待办与阻塞事项.md) T218。②T217 统一后的新写入不再把非质量终态计入健康率；存量 statsagg 来源小时行（Node 时代探针 usage，无归因不可判别）在 35 天滚动窗内自然退场，历史窗口不重算（见 T217）。③极端稀疏页（大量孤立缺失小时跨 3 个以上 2048 区间批次）仍共享 5s 预算，冷缓存下可能截断为部分条带并记 `ai_health_gap_read_degraded`，页面显示未读小时 `unknown`；不因此回退整页整窗读取。
- 保留窗口（2026-10-31，W40 落地，用户裁决 35 天）：`juhe_jobs.account_health_outcomes` 是只增探针遥测——读面最大 31 天窗，超窗行无任何代码路径消费，生产备份亦把该表列为排除项（可重新探测累积）。jobs `accounthealth.CleanupOutcomesBefore` 按 `observed_at` 滚动 35 天窗口（读面窗 + 4 天排查余量）最旧优先批删（单批 5000 行、最多 200 批，ctid 批删压锁面；子查询走 `idx_account_health_outcomes_observed` 索引），随 `data-retention-cleanup` 节拍（10min fixedDelay、storage-maintenance lane）执行；稳态写入 ≈7000 行/天，收敛远快于节拍。清理 SQL 归 accounthealth 包（表归属 juhe_jobs schema 不动），窗口天数与批次在组合根常量。
- 读面载体（2026-10-31）：jobs 写侧把 `next_due_at` 与 payload 单键**双写**（payload 键保留供旧版 gateway 滚动窗口读取），`juhe_jobs.account_health_outcomes.next_due_at` 成为实体列；覆盖索引 `idx_account_health_outcomes_read_cover (account_id, observed_at) INCLUDE (全部读面投影列)` 使该读面走 Index Only Scan——此前 payload 单键投影迫使逐行回表，慢盘冷缓存首开曾 2×5s 读预算超时。PG 无法对表达式索引列做 IOS 输出（生产 EXPLAIN 实证），实体列是唯一可行形态。存量行该列为 NULL，读面由合并层既有 fallback 回退 `accounts.next_health_check_at`，探针每小时重写自愈，不做回填。旧普通范围索引与表达式键试验索引由 jobs schema 幂等退役。
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
