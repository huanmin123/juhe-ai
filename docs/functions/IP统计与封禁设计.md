# IP 统计与封禁设计

> 面向 `juhe-ai` 后端、前端系统运维页面和后续 AI 维护者。
> 当前实现已经落地持久 IP 注册表、IP 统计聚合表、IP 策略表、`/__aisys__/api/ip-stats` 管理接口、系统运维 / IP管理页面和网关封禁缓存。外部公益站读取接口仍是后续阶段，不属于当前已挂载接口。

## 当前状态

- 已实现：`client_ip_registry`、`client_ip_stats_daily`、`client_ip_usage_range_windows`、`client_ip_policies`、`client_ip_policy_hits`。
- 已实现：后台 `client-ip-stats-aggregation` job 按 usage shard 独立游标增量聚合 IP 统计，不在页面或网关请求路径扫描 `usage_records`。
- 已实现：管理员 `GET /__aisys__/api/ip-stats` 列表、封禁和解封接口。
- 已实现：系统运维菜单新增 `IP管理` 页面，展示请求、Token、成本、失败率、活跃天数、速度、最近使用和策略操作。
- 已实现：网关请求入口只读取 server 进程内 IP 封禁缓存，命中 active 封禁策略时本地返回 `403 client_ip_blacklisted`，封禁命中异步批量写入 `client_ip_policy_hits`；策略缓存由运行态快照和管理端封禁 / 解封变更刷新，认证前缺少 Bearer 或无效 Key 不会为了 IP 策略放大 DB service 读取。
- 未实现：受保护外部来源 IP 聚合接口。后续给 `juhe-ai-public-welfare` 接入时，只应暴露 IP 聚合事实，不暴露封禁策略、内部账号、API Key、模型或公益站业务关系。

## 设计目标

- 建立独立的 IP 统计事实层，支撑系统管理员在“系统运维 / IP管理”查看来源 IP 的请求次数、Token、成本、失败情况和活跃情况。
- 后台 job 按 `usage_records` 分片游标增量处理新记录，不能在页面、接口或普通请求路径按 IP 全量 `GROUP BY usage_records`。
- 通过 IP 注册表和本进程懒加载分桶 Set 识别当前进程已见过的 IP，避免启动时全量预热造成 worker 长时间阻塞；最终正确性仍由 SQLite 唯一约束兜底。
- 提供管理员可控的 IP 封禁、临时封禁和解封能力，作为 `juhe-ai` 自身网关运维能力。
- 后续给 `juhe-ai-public-welfare` 等允许来源系统读取 IP 聚合数据时，只暴露受保护的 IP 聚合事实，不暴露后台封禁策略、用户映射或公益榜快照。

## 范围边界

### 本期包含

- IP 注册表：记录规范化 IP、聚合 key、hash、首次出现、最近出现和注册时间。
- IP 统计聚合：按 IP 聚合请求次数、成功 / 失败、Token、成本、活跃天数、首 token / 总耗时样本和、最大总耗时和最近使用时间。
- IP 运维页面：管理员在“系统运维 / IP管理”查看列表、筛选、排序和策略状态。
- IP 策略：支持封禁、临时封禁、解封和封禁命中记录。
- 后台 job：负责注册 IP、写入 IP 聚合、刷新范围窗口、刷新策略运行态缓存。

### 本期不包含

- 公益站用户与 IP 的映射：由 `juhe-ai-public-welfare` 自己维护。
- 公益榜快照、贡献榜、消耗榜和公开展示名称：由公益站自己快照汇总。
- 地区、国家、ASN、运营商、IP 段归属或代理标签封禁：容易误伤，不作为默认能力。
- 基于固定状态码 / 固定错误码的自动永久封禁：已有 IP 级错误熔断仍保持短 TTL 易失运行态。
- 浏览器或公网无鉴权读取 IP 统计：IP 统计属于管理员和受保护来源系统可读数据。

## 核心结论

- IP 统计是 `juhe-ai` 的规划基础能力，如后续重启公益站外部接口，应先于外部接口落地。
- 页面和接口只读预聚合表或窗口表，不能为了 IP 列表临时扫描 `usage_records`。
- IP 注册 Set 是懒加载性能优化，不是事实源；启动时不全量读取注册表，最终正确性依赖 SQLite 唯一约束和 `INSERT OR IGNORE`。
- 首期只为 IP 写 `daily` 和范围窗口，避免把 IP 维度直接接入全套 `minute / hourly / weekly / monthly / totals` 造成写放大。
- 统计 scope 使用 `ip_hash`，列表接口再关联 IP 注册表返回可展示 IP。
- IPv6 默认按 `/64` 聚合作为统计 key，避免隐私 IPv6 地址导致 IP 基数爆炸；原始规范化 IP 可保留最近样本。
- IP 封禁是网关运行前置判断，必须读运行态缓存，不允许每次请求查库。

## 数据流

```text
网关请求
  -> usage_records 写入 client_ip
  -> 后台 IP 统计 job 按 usage shard cursor 增量读取新记录
  -> 规范化 IP，生成 aggregate_ip_key / ip_hash / bucket_no
  -> 查询本进程懒加载 bucket Set 判断是否已注册
  -> 未命中则 INSERT OR IGNORE 到 client_ip_registry
  -> 写入 client_ip_stats_daily
  -> 按 dirty IP 增量刷新 client_ip_usage_range_windows
  -> 管理页面和外部来源接口只读 IP 维度窗口表
```

封禁流程：

```text
管理员封禁 / 解封
  -> 写 client_ip_policies 和操作日志
  -> 通知网关运行态刷新策略缓存
  -> 网关请求进入时读内存策略缓存
  -> 命中 active 封禁策略返回本地拒绝
  -> 异步记录封禁命中计数
```

## IP 规范化与聚合 key

IP 写入前必须先规范化：

- IPv4 去除端口、去除 `::ffff:` 前缀后保存标准点分十进制。
- IPv6 保存压缩后的标准形式。
- IPv6 统计默认使用 `/64` 聚合 key，例如同一 `/64` 下的隐私地址归为同一个 `aggregate_ip_key`。
- 如果请求无法识别 IP，统计为 `unknown` 不进入封禁策略。
- `client_ip` 表示最近一次原始规范化 IP，`aggregate_ip_key` 表示统计和策略匹配使用的 key。

当前 hash：

```text
ip_hash = sha256("client-ip:" + aggregate_ip_key)
bucket_no = parseInt(ip_hash[0..8], 16) % 4096
```

`ip_hash` 作为 IP 专用表主键，避免统计表索引和日志里到处出现明文 IP。

## 存储设计

### client_ip_registry

保存 IP 注册事实，位于统计结果库。该表可从仍保留的 `usage_records` 离线重建。

```text
client_ip_registry
- ip_hash TEXT PRIMARY KEY
- bucket_no INTEGER NOT NULL
- aggregate_ip_key TEXT NOT NULL
- client_ip TEXT NOT NULL
- ip_version INTEGER NOT NULL
- first_seen_at TEXT NOT NULL
- last_seen_at TEXT NOT NULL
- created_at TEXT NOT NULL
- updated_at TEXT NOT NULL
```

索引建议：

```text
idx_client_ip_registry_bucket ON client_ip_registry(bucket_no, ip_hash)
idx_client_ip_registry_last_seen ON client_ip_registry(last_seen_at DESC, ip_hash)
idx_client_ip_registry_ip ON client_ip_registry(aggregate_ip_key)
idx_client_ip_registry_client_ip ON client_ip_registry(client_ip)
```

### client_ip_stats_daily

保存 IP 每日统计。首期专用表比把 IP 接入全套通用统计桶更可控。

```text
client_ip_stats_daily
- ip_hash TEXT NOT NULL
- stat_date TEXT NOT NULL
- request_count INTEGER NOT NULL DEFAULT 0
- success_count INTEGER NOT NULL DEFAULT 0
- error_count INTEGER NOT NULL DEFAULT 0
- input_tokens INTEGER NOT NULL DEFAULT 0
- output_tokens INTEGER NOT NULL DEFAULT 0
- cache_read_tokens INTEGER NOT NULL DEFAULT 0
- cache_read_cost_usd REAL NOT NULL DEFAULT 0
- total_cost_usd REAL NOT NULL DEFAULT 0
- duration_ms_sum INTEGER NOT NULL DEFAULT 0
- duration_ms_count INTEGER NOT NULL DEFAULT 0
- duration_ms_max INTEGER NOT NULL DEFAULT 0
- first_token_ms_sum INTEGER NOT NULL DEFAULT 0
- first_token_ms_count INTEGER NOT NULL DEFAULT 0
- last_used_at TEXT
- last_error_at TEXT
- updated_at TEXT NOT NULL
- PRIMARY KEY (ip_hash, stat_date)
```

### client_ip_usage_range_windows

保存常用日期范围窗口，管理页面和后续外部接口按完整范围 key 直读。

```text
client_ip_usage_range_windows
- ip_hash TEXT NOT NULL
- start_date TEXT NOT NULL
- end_date TEXT NOT NULL
- request_count INTEGER NOT NULL DEFAULT 0
- success_count INTEGER NOT NULL DEFAULT 0
- error_count INTEGER NOT NULL DEFAULT 0
- input_tokens INTEGER NOT NULL DEFAULT 0
- output_tokens INTEGER NOT NULL DEFAULT 0
- cache_read_tokens INTEGER NOT NULL DEFAULT 0
- cache_read_cost_usd REAL NOT NULL DEFAULT 0
- total_cost_usd REAL NOT NULL DEFAULT 0
- duration_ms_sum INTEGER NOT NULL DEFAULT 0
- duration_ms_count INTEGER NOT NULL DEFAULT 0
- duration_ms_max INTEGER NOT NULL DEFAULT 0
- average_duration_ms REAL
- first_token_ms_sum INTEGER NOT NULL DEFAULT 0
- first_token_ms_count INTEGER NOT NULL DEFAULT 0
- average_first_token_ms REAL
- active_days INTEGER NOT NULL DEFAULT 0
- last_used_at TEXT
- last_error_at TEXT
- updated_at TEXT NOT NULL
- PRIMARY KEY (ip_hash, start_date, end_date)
```

`active_days` 不能在请求路径 `COUNT(DISTINCT stat_date)`；应由 worker 在刷新范围窗口时基于 `client_ip_stats_daily` 维护。平均首 token 和平均总耗时由 worker 写入 `average_first_token_ms` / `average_duration_ms`，请求路径只读当前页窗口行，不为了平均值扫描明细或大范围汇总窗口。

排序索引按页面热点建立：

```text
idx_client_ip_range_cost ON client_ip_usage_range_windows(start_date, end_date, total_cost_usd DESC, ip_hash)
idx_client_ip_range_tokens ON client_ip_usage_range_windows(start_date, end_date, input_tokens DESC, output_tokens DESC, ip_hash)
idx_client_ip_range_total_tokens ON client_ip_usage_range_windows(start_date, end_date, (input_tokens + output_tokens) DESC, ip_hash)
idx_client_ip_range_requests ON client_ip_usage_range_windows(start_date, end_date, request_count DESC, ip_hash)
idx_client_ip_range_error_rate ON client_ip_usage_range_windows(start_date, end_date, (CASE WHEN request_count > 0 THEN CAST(error_count AS REAL) / request_count ELSE 0 END) DESC, ip_hash)
idx_client_ip_range_active_days ON client_ip_usage_range_windows(start_date, end_date, active_days DESC, ip_hash)
idx_client_ip_range_last_used ON client_ip_usage_range_windows(start_date, end_date, last_used_at DESC, ip_hash)
```

### 不设置范围总聚合表

IP 管理当前只需要 IP 维度行，不需要范围整体总统计。后端不创建、不维护、也不读取范围总聚合表；页面顶部不展示任何范围整体统计卡片。`duration_ms_sum/count` 和 `first_token_ms_sum/count` 只作为后台刷新单个 IP 窗口行平均值的输入；接口读取当前页 IP 行上的 `average_duration_ms`、`average_first_token_ms` 和 `duration_ms_max`，不在请求路径对窗口表重新 `SUM/COUNT/MAX`。

### client_ip_policies

保存 IP 运维策略。

```text
client_ip_policies
- id TEXT PRIMARY KEY
- ip_hash TEXT NOT NULL
- status TEXT NOT NULL           -- active | disabled
- reason TEXT
- expires_at TEXT
- created_by_system_account_id TEXT NOT NULL
- created_at TEXT NOT NULL
- updated_at TEXT NOT NULL
- disabled_at TEXT
- disabled_by_system_account_id TEXT
- disabled_reason TEXT
```

规则：

- `status = active` 表示当前处于封禁状态。
- `expires_at` 为空表示长期生效；非空表示临时封禁。
- 解封不删除记录，改为 `disabled` 并保留原因。

### client_ip_policy_hits

封禁命中不进入完整使用统计，但需要给管理员判断封禁效果。

```text
client_ip_policy_hits
- ip_hash TEXT NOT NULL
- stat_date TEXT NOT NULL
- policy_id TEXT NOT NULL
- hit_count INTEGER NOT NULL DEFAULT 0
- last_hit_at TEXT
- updated_at TEXT NOT NULL
- PRIMARY KEY (ip_hash, stat_date, policy_id)
```

命中计数应异步批量写入，网关请求不能等待 SQLite 写入。

## 内存分桶 Set

后台 worker 维护：

```text
Map<bucket_no, Set<ip_hash>>
```

启动策略：

- worker 启动后不全量预热 Set，避免高基数 IP 注册表把 worker 事件循环长时间占住。
- Set 只记录本进程运行期间已经见过的 IP hash；未命中 Set 时执行 `INSERT OR IGNORE`，保证正确性。
- Set 只保存 hash，不保存明文 IP。
- Set 容量过高时可以启用 LRU 或只保留近期活跃 bucket，但唯一约束仍必须兜底。

注册策略：

```text
if bucketSet.has(ip_hash):
  只更新统计
else:
  INSERT OR IGNORE client_ip_registry
  bucketSet.add(ip_hash)
  更新统计
```

## 后台 job

### IP 注册与统计增量 job

该 job 可以挂在现有 usage stats 聚合流程之后，也可以独立维护 per-shard cursor。首期建议独立 cursor，便于失败重跑和性能隔离。

任务职责：

- 按 usage shard 的 `(created_at, id)` 游标读取新增记录。
- 跳过无法识别 IP 的记录。
- 跳过 `traffic_source = cooldown_retest` 等不进入业务用量统计的记录。
- 注册 IP 到 `client_ip_registry`。
- 写入 `client_ip_stats_daily`。
- 推进 `stats_job_state` 或 IP 专用 job state。

禁止：

- 启动时全量扫描 `usage_records` 自动补历史。
- 页面请求时临时 `SUM/GROUP BY usage_records`。
- 前端拿明细自行 reduce。

### IP 范围窗口刷新 job

职责：

- 基于 `client_ip_stats_daily` 刷新最近 31 天内常用自然日范围。
- 常规刷新只处理本轮聚合产生的 dirty IP，不在每轮任务里全量重建所有 IP 窗口。
- dirty IP 会同时写入 `client_ip_range_window_dirty_ips` 持久小表，刷新成功后再删除；内存 Set 只做当前进程加速，worker 重启后仍能从 dirty 表继续增量刷新。
- 如果 dirty IP 超过单轮上限，窗口保持 stale；只有 dirty 表和内存 dirty Set 都清空后，才把当前固定窗口标记为 ready。
- 全量窗口重建只作为维护 / 离线重建动作使用，不能挂在系统 API 请求路径。
- 写入 `client_ip_usage_range_windows`。
- 不维护范围整体汇总；平均值和最大耗时只落在每个 IP 的窗口行上。
- 窗口 ready/stale 状态记录在 `stats_job_state(scope_type = client_ip_range_window)`，只保存刷新完成标记，不保存数量、成本或范围总量；有新 IP daily 写入时先把当前固定窗口标记为 stale，避免旧窗口行被继续当作新数据展示。

首期可支持：

- 今天
- 最近 7 天
- 最近 30 / 31 天
- 列表默认使用已经预生成的固定窗口；任意自然日范围若未预生成，接口返回 `rangeReady=false`，前端提示稍后刷新。

### IP 策略缓存刷新 job

职责：

- 加载 active 且未过期的 `blacklist` 策略到网关运行态缓存。
- 管理员创建、解封或更新策略后主动触发刷新。
- 过期策略可由定时 job 标记为 disabled，也可以运行态读取时自然忽略，再由清理 job 归档。

## 管理端页面

菜单位置：

```text
系统管理
  -> 系统运维
    -> IP管理
```

页面只允许管理员访问。

### 列表字段

| 字段 | 说明 |
| --- | --- |
| IP | `aggregate_ip_key` 或规范化 IP |
| 状态 | 正常 / 已封禁 |
| 请求次数 | 范围内请求数 |
| 成功次数 | 范围内成功请求数 |
| 失败次数 | 范围内失败请求数 |
| 失败率 | `error_count / request_count` |
| 输入 Token | 范围内输入 token |
| 输出 Token | 范围内输出 token |
| 缓存读取 Token | 范围内缓存读取 token |
| Token | `input_tokens + output_tokens` |
| 估算成本 | 范围内 `total_cost_usd` |
| 活跃天数 | 范围内有请求的自然日数量 |
| 速度 | 平均首 token、平均总耗时和最大总耗时，来自 IP 预聚合字段 |
| 首次出现 | 注册表 `first_seen_at` |
| 最近使用 | 窗口或注册表 `last_used_at / last_seen_at` |
| 最近错误 | `last_error_at` |
| 操作 | 封禁、临时封禁、解封 |

### 筛选与排序

筛选：

- 日期范围，最大最近 31 天。
- IP 关键词，精确或右侧前缀匹配。
- 状态：全部、正常、已封禁。
- 高消耗：成本或 token 大于阈值。
- 高失败率：失败率大于阈值且请求数达到最小样本。
- 最近活跃：最近 N 小时 / N 天有请求。

排序：

- 成本降序
- Token 降序
- 请求次数降序
- 失败率降序
- 最近使用时间降序

列表默认按成本降序，其次请求次数降序，再按 IP hash 稳定排序。

## 管理 API 草案

管理接口只允许管理员 Cookie 调用。

```http
GET /__aisys__/api/ip-stats
POST /__aisys__/api/ip-stats/:ipHash/blacklist
POST /__aisys__/api/ip-stats/:ipHash/unblock
```

列表查询参数：

```text
startDate=YYYY-MM-DD
endDate=YYYY-MM-DD
page=1
pageSize=20
keyword=1.2.3
status=all | normal | blacklisted
sortField=totalCost | totalTokens | requestCount | errorRate | lastUsedAt
sortOrder=desc | asc
```

列表响应：

```ts
interface ClientIpStatsListResponse {
  range: {
    startDate: string
    endDate: string
    maxDays: number
  }
  items: ClientIpStatsListItem[]
  page: number
  pageSize: number
  pageUpperBound: number
  hasMore: boolean
  rangeReady: boolean
}

interface ClientIpStatsListItem {
  ipHash: string
  aggregateIpKey: string
  status: 'normal' | 'blacklisted'
  rangeUsage: ClientIpUsageSummary
}

interface ClientIpUsageSummary {
  requestCount: number
  successCount: number
  errorCount: number
  errorRate: number
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  totalTokens: number
  totalCost: number
  activeDays: number
  averageFirstTokenMs?: number
  averageDurationMs?: number
  maxDurationMs?: number
  lastUsedAt?: string | null
  lastErrorAt?: string | null
}
```

封禁请求：

```ts
interface ClientIpBlacklistRequest {
  reason: string
  durationMinutes?: number
  durationDays?: number
  expiresAt?: string | null
}
```

封禁时长规则：

- `durationMinutes` 和 `durationDays` 用于表达“封禁多少分钟 / 多少天”，由后端按服务器时间换算为 `expiresAt`。
- 不传时表示永久封禁；旧版 `expiresAt` 继续兼容。
- `durationMinutes`、`durationDays` 和 `expiresAt` 只能传一种。
- 到期策略不再被列表和网关视为 active 封禁；网关缓存不会跨过最近的策略过期时间。
- 网关返回 `403 client_ip_blacklisted` 时，错误消息和 `error.client_ip` 包含当前来源 IP；IPv6 聚合封禁会同时提示封禁范围并返回 `error.aggregate_ip_key`，便于调用方反馈给管理员。

## 外部来源接口边界

后续 `juhe-ai-public-welfare` 只读取 IP 聚合事实：

```http
GET /__aisys__/api/external-integrations/juhe-ai/ip-usage
Authorization: Bearer <source_token>
X-Juhe-AI-Source: juhe-ai-public-welfare
```

该接口：

- 只校验来源系统是否允许调用。
- 不做公益站 IP 拦截、限频或用户权限判断。
- 不返回后台封禁策略和操作历史。
- 不返回 API Key、系统账户、AI 账户、分组、供应商或模型业务关系。
- 只读 `client_ip_usage_range_windows` 和 `client_ip_registry`。

## 性能影响与约束

### 写入影响

- 每条有 IP 的业务使用记录会额外写入 IP 注册表和 IP daily；范围窗口由后台 job 按 dirty IP 增量刷新。
- 使用 `daily + range window` 控制写放大，首期不写全套六层通用统计桶，也不维护 IP 累计总表。
- 批处理必须复用 prepared statement，避免每行反复 prepare。
- IP 注册表写入使用 Set 命中减少重复 `INSERT OR IGNORE`。

### 查询影响

- IP 列表按范围窗口表查询，配合排序索引。
- IP 管理不展示范围总统计卡片，也不在后端维护范围总聚合。
- IP 速度指标只读窗口表中已经落表的 `average_first_token_ms`、`average_duration_ms` 和 `duration_ms_max`；`sum/count` 只作为后台刷新单个 IP 窗口行派生字段的输入，不回扫 `usage_records`。
- 列表请求不触发窗口重建；未命中窗口或窗口已被新 daily 标记为 stale 时返回空列表和 `rangeReady=false`，等待后台 worker 生成。
- `pageUpperBound` 只作为分页器兼容上界，不能作为数量或范围总统计展示，不能为了精确总数额外执行大范围 `COUNT(*)` 或维护范围总聚合。
- 服务端限制 IP 列表最大页码，避免恶意或误操作的超大 `OFFSET` 在高基数窗口上长时间跳行。
- IP keyword 只按明文 IP / 聚合 IP 做精确或右侧前缀匹配，不支持 hash 搜索，也不支持任意前导通配符全表扫描。
- 高失败率筛选需要有最小请求样本，例如 `request_count >= 10`，避免小样本误导。

### 启动影响

- 不做启动全量 Set 预热，避免 IP 注册量过大时拖慢 worker 启动和后台心跳。
- 统计 job 正常运行，冷 IP 通过 `INSERT OR IGNORE` 和主键唯一约束兜底。
- 如后续确实需要预热，只允许独立低优先级分页任务并在批次之间让出事件循环。

## 安全与隐私

- IP 属于敏感运维数据，后台页面只允许管理员访问。
- 普通日志不输出完整 IP，可以输出 hash 前缀、策略状态和 traceId。
- 操作日志记录封禁和解封变更，但不记录不必要的请求明细。
- 外部来源接口只返回 IP 聚合事实，不返回封禁原因、管理员 ID、内部策略历史或操作日志。
- 不做区域性封禁和外部标签自动定罪，避免误伤共享出口和移动网络用户。

## 与既有 IP 机制的关系

| 机制 | 存储形态 | 作用 | 本设计关系 |
| --- | --- | --- | --- |
| IP 级账号回避 | 进程内短 TTL | 同一 IP 短期避开刚失败账号 | 保持易失，不变成统计或封禁事实 |
| IP 级错误熔断 | 进程内短 TTL | 认证前 / 本地高置信错误短期止损 | 保持易失，不写永久黑名单 |
| IP 统计 | SQLite 预聚合 | 管理员查看和外部 IP 聚合事实 | 本文新增 |
| IP 封禁 | SQLite 策略 + 网关缓存 | 管理员明确操作后的拦截 | 本文新增 |

## 实施阶段

### 阶段 1：设计和计划

- 新增本文档。
- 新增 `PLAN-0031` 专项计划。
- 等待用户提交和部署当前项目后再开始实现。

### 阶段 2：后端事实层

- 新增 IP 注册表、IP daily、IP range window、IP policy 表。
- 新增 IP 规范化、hash 和 bucket 工具。
- 新增后台 IP 注册与统计 job。
- 新增 IP 范围窗口刷新 job。

### 阶段 3：管理接口与页面

- 新增管理员 IP 统计列表、封禁、解封接口。
- 在系统运维菜单新增 IP管理页面。
- 支持筛选、排序、分页和策略操作。

### 阶段 4：网关封禁运行态

- 网关加载 active IP 策略缓存。
- 请求进入时读内存缓存判断封禁。
- 异步记录封禁命中。
- 管理操作后触发策略缓存刷新。

### 阶段 5：外部来源接口

- 新增受保护来源系统鉴权。
- 新增 IP 聚合只读接口，服务 `juhe-ai-public-welfare` 后端。
- 保持公益站业务快照和用户映射在公益站项目内完成。

## 验证清单

- 后台 IP 统计 job 只按 shard cursor 增量读取，不出现请求路径 `GROUP BY usage_records`。
- 新 IP 首次出现会写入注册表，重复 IP 不会重复注册。
- Set 为空或冷 IP 未命中时，数据库唯一约束仍能保证注册正确。
- IPv6 默认按 `/64` 聚合。
- IP 列表可以按成本、Token、请求数、失败率和最近使用排序。
- 封禁后网关请求被本地拒绝，解封后恢复。
- IP 管理策略只表达封禁和解封。
- 普通用户无法访问 IP 统计接口和页面。
- 外部来源接口不返回封禁策略和内部业务关系。
- IP 管理列表不返回范围总统计或统计滞后卡片，只验证 IP 行维度数据和分页状态。
