# AI 健康监控设计

> 状态：账户哨兵 v1 已实施；多模型能力 v2 是完整目标契约，只有与多模型能力健康方案一起交付后才启用。

## 1. 监控回答的两个问题

AI 健康监控必须明确区分：

1. **账户哨兵观察 v1**：这个账户在某次 healthCheckModel 检查时是否完成。
2. **模型执行能力 v2**：这个账户当前配置的模型、endpoint、转换路径和凭据作用域中，哪些已确认可用、未知、正在确认、已阻断或恢复中。

v1 不能代表全部模型，v2 也不能改写 v1 历史。页面并列展示两层信息，不把一个哨兵成功渲染成“全部能力可用”。

页面不判断失败责任归属，不维护供应商错误码词典。HTTP 状态、错误码和原始错误详情可以留在现有受权限保护的 v1 usage / audit 详情中，但不参与 v2 能力状态决策。

## 2. v1 账户哨兵三态

| 状态 | 颜色 | 页面文案 | 判定 |
| --- | --- | --- | --- |
| success | 绿色 | 哨兵可用 | 哨兵检查成功完成 |
| failure | 红色 | 哨兵不可用 | 哨兵检查没有成功完成 |
| unknown | 灰色 | 哨兵无记录 | 该小时没有哨兵检查结果 |

v1 失败原因只用于诊断展示，不参与颜色分类。“哨兵不可用”不等于账户所有模型或供应商服务器不可用。

正常 active 账户默认每 1 小时检查一次，并按账户 ID 在 0 到 10 分钟内稳定错峰。只有与保存的哨兵 model + endpoint + lane + adapter route 完全相同的真实成功可以满足本轮水位；其他模型成功不能顺延哨兵。

## 3. v1 小时聚合

- 历史事实只取 traffic_source = account_health_check 的使用记录。
- 按统计时区聚合到 account_health_hourly。
- 同一账户同一小时有多次结果时，以 created_at、id 较新的记录为准。
- 没有聚合行时由查询层补为 unknown。
- 页面请求只读取预聚合表，不扫描使用记录。

v1 哨兵可用率：

~~~text
哨兵可用率 = success 小时数 / (success 小时数 + failure 小时数)
~~~

unknown 不进入分母；没有有效检查时显示 --。该百分比必须标为“哨兵检查可用率”，不得简称“账户所有模型可用率”。

account_health_hourly 保留：

~~~text
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
~~~

## 4. v2 能力状态

v2 使用 [AI 账户多模型能力健康与精确隔离设计](AI账户多模型能力健康与精确隔离设计.md) 的 Catalog 和 circuit ledger。

scope 状态：

- unknown
- available
- suspect
- temporarily_blocked
- half_open
- recovering

账户小时 aggregate：

- unknown
- healthy
- no_confirmed_unavailability
- confirming
- partially_unavailable
- recovering
- all_configured_capabilities_blocked

unknown 允许调度，但不能显示绿色 healthy。全部能力阻断只是派生调度门禁，不表示 accounts.status 已改变。

## 5. v2 数据结构

### 5.1 事件

account_capability_health_events 保存：

~~~text
event_id
system_account_id
runtime_account_id
account_runtime_key
route_scope_id
scope_id
capability_universe_revision
effective_dispatch_revision
generation
event_type
outcome
phase_before
phase_after
observed_at
trace_id
created_at
~~~

事件不保存 API Key、OAuth token、用户 payload 或上游正文，默认保留 7 天。HTTP / error code 不进入 outcome。

### 5.2 每 Route 小时表

account_capability_health_hourly 保存：

~~~text
system_account_id
runtime_account_id
account_runtime_key
route_scope_id
capability_universe_revision
stat_hour
last_state
last_outcome
available_observation_count
unavailable_observation_count
task_failure_count
last_observed_at
last_event_id
updated_at
semantics_version = 2
~~~

主键 / 唯一约束覆盖 system_account_id + account_runtime_key + route_scope_id + stat_hour。默认保留 31 天。

### 5.3 账户小时摘要

account_capability_health_hourly_summary 保存：

~~~text
system_account_id
runtime_account_id
account_runtime_key
capability_universe_revision
stat_hour
routable_route_count
schedulable_route_count
available_route_count
unknown_route_count
suspect_route_count
soft_blocked_route_count
blocked_route_count
half_open_route_count
recovering_route_count
aggregate
last_observed_at
computed_at
semantics_version = 2
~~~

摘要必须把同一 universe revision 的 CapabilityScopeCatalog 作为全集。只扫描 incident 行无法计算 unknown，也不得宣称 healthy。

## 6. v2 聚合 owner 与优先级

- gateway / control worker 只写 circuit ledger 和有界事件。
- ingest owner 持久化 account_capability_health_events。
- stats owner 按游标增量写 route 小时表和账户小时摘要。
- 管理 API 只读预聚合窗口，不扫描 usage、audit、运行日志或完整 ledger 临时 GROUP BY。

同一小时多次状态变化时：

- route 小时表保存最后状态和各 outcome 计数。
- 账户摘要以当前小时最后完成的 Catalog revision 计算。
- revision 中途变化时，旧 revision 事件保留诊断，但不进入新 revision 的 Route 总数。
- stats 聚合滞后时返回 stale / generatedAt，不回退用 v1 冒充 v2。

v2 不计算“能力成功率”。available / unavailable observation 的比例只表示探针结果分布，不能代表请求成功率或供应商 SLA。

## 7. API

### 7.1 列表

管理端：

~~~text
GET /__aisys__/api/stats/ai-health
~~~

用户端：

~~~text
GET /__aisys__/api/my-stats/ai-health
~~~

查询参数：

| 参数 | 默认值 | 边界 |
| --- | --- | --- |
| hours | 168 | 1..744 |
| keyword | 空 | 账户名搜索 |
| page | 1 | 正整数 |
| pageSize | 20 | 10..50 |

每个账户返回：

~~~text
sentinel
  semanticsVersion = 1
  successCount
  failureCount
  unknownCount
  availabilityRate
  hourly[]

capability
  semanticsVersion = 2
  capabilityUniverseRevision
  currentSummary
  hourly[]
  generatedAt
  stale
~~~

v2 尚未启用或切换日前，capability 为 null，不补造全绿小时点。

### 7.2 小时能力明细

~~~text
GET /__aisys__/api/stats/ai-health/accounts/:accountId/capability-hours/:statHour
GET /__aisys__/api/my-stats/ai-health/accounts/:accountId/capability-hours/:statHour
~~~

参数 limit 默认 50、范围 1 到 100，使用签名 cursor 和稳定 Route 排序。响应从 route 小时表读取；事件仍在保留期时可附带分页事件，过期后明确返回 eventsExpired=true。

管理路径仅管理员。my-* 只使用当前 session；授权实例用户只能看自己的聚合和脱敏 Route，不能看到来源 Key、其他实例或 trace。不可见资源返回 404。

### 7.3 分页和排序

后端先按权限、账户名搜索和使用热度稳定分页，再批量读取当前页账户的 v1 / v2 小时结果。默认按 account_quality_scores.recent_request_count、accounts.last_used_at、name、id 排序。禁止逐账户查询。

## 8. 页面

菜单保留：

- 管理入口：/ai-health
- 用户入口：/my-ai-health

页面使用账户卡片列表：

- 工具栏提供账户名搜索、时间范围和刷新。
- v1 图例显示“哨兵可用、哨兵不可用、无记录”。
- v2 图例显示“能力可用、未知、确认中、部分不可用、恢复中、全部阻断”。
- 每个账户先展示统一 effectiveAvailability / availabilityPresentation，再展示哨兵和能力两条独立摘要。
- active + partially_unavailable 显示“可调度 · 部分能力不可用”。
- active + all_configured_capabilities_blocked 显示“当前无可调度模型能力”，不能继续只显示绿色“可调度”。
- quality_isolated、disabled、error、用户显式 temporary_unavailable 等硬状态优先。
- 小时状态使用 Canvas，31 天不创建 744 个 DOM 节点。
- 点击 v1 槽查看哨兵诊断；点击 v2 槽分页查看模型、endpoint、转换路径和通用 outcome。
- v2 事件过期后仍展示小时摘要，并注明明细已过保留期。

页面不显示内部时区、generation、lease、原始 fingerprint 或来源账户秘密。

## 9. 历史切换

- capabilityHealthSemanticsStartedAt 记录 v2 启用时间。
- 启用前的 account_health_hourly 只按 v1 展示，不迁移成 v2。
- 启用后的 v1 仍继续记录哨兵，不被 v2 覆盖。
- v2 聚合不存在时显示“能力数据待聚合”或“尚无能力观察”，不能用 v1 success 填充。
- 回滚 v2 时保留 v1；v2 表可只读保留或按迁移契约移除。

## 10. 性能与容量

- 单次最多返回 50 个账户、每账户最多 744 个 v1 / v2 小时摘要。
- 当前页账户 ID 分块批量查询；禁止 N+1。
- Route 明细按需加载，默认 50、最大 100。
- stats owner 使用 staged / window 写入，不在 API 请求中聚合 raw 事件。
- Canvas 绘制必须有固定尺寸和命中索引，不为每个小时点创建组件或监听器。
- capability event 7 天、小时摘要 31 天；清理任务独立有界运行。

## 11. Mockdata

Mockdata 必须通过真实事件 / 聚合 owner 生成：

- 超过一页的账户。
- v1 success / failure / unknown。
- v2 unknown、healthy、confirming、partially_unavailable、recovering、all blocked。
- A 可用、B 阻断的多模型账户。
- Key-1 + B 阻断、Key-2 + B 可用的多 Key 账户。
- 切换日前只有 v1、切换日后 v2 聚合滞后、事件已过期的样本。

禁止前端硬编码状态。

## 12. 明确不做

- 不判断失败是谁的责任。
- 不维护错误码白名单、黑名单或动态错误分类。
- 不把 v1 哨兵结果扩散为全部模型结论。
- 不根据页面查询触发全模型扫描。
- 不把健康监控变成生产重检入口。
- 不把 unknown 计为健康或失败。
- 不用模型能力结果修改 accounts.status。

## 13. 验收标准

- v1 三态、可用率和历史行为保持兼容，文案明确是哨兵。
- A 成功、B 失败时 v1 与 v2 可以同时呈现不同结论，页面不互相覆盖。
- A 成功、B/C 未观察时 v2 不是 healthy。
- 全部能力阻断时统一状态显示无可调度能力，但 accounts.status 未变化。
- v1 / v2 切换日前后、聚合滞后和事件过期均有稳定显示。
- 管理 / my-* 权限、授权实例脱敏和 404 边界正确。
- 31 天列表只读预聚合，当前页批量查询，无 N+1、raw scan 和 744 DOM 节点。
- Browser 回归覆盖全部 aggregate、硬状态优先和小时明细交互。
