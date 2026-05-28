# IP 统计与封禁设计

> 面向 `sub2api-lite` 后端、前端系统运维页面和后续 AI 维护者。
> 本文定义 `sub2api-lite` 自身的 IP 维度统计、IP 注册表、后台聚合 job、管理员运维页面和封禁 / 解封能力。公益站、公益榜和用户 IP 映射只消费这里产出的 IP 聚合事实，不属于本文实现范围。

## 设计目标

- 建立独立的 IP 统计事实层，支撑系统管理员在“系统运维 / IP统计”查看来源 IP 的请求次数、Token、成本、失败情况和活跃情况。
- 后台 job 按 `usage_records` 分片游标增量处理新记录，不能在页面、接口或普通请求路径按 IP 全量 `GROUP BY usage_records`。
- 通过 IP 注册表和内存分桶 Set 识别系统内已出现的 IP，避免每次聚合都从明细表扫描所有 IP。
- 提供管理员可控的 IP 封禁、临时封禁、解封和观察名单能力，作为 `sub2api-lite` 自身网关运维能力。
- 后续给 `juhe-ai-public-welfare` 等允许来源系统读取 IP 聚合数据时，只暴露受保护的 IP 聚合事实，不暴露后台封禁策略、用户映射或公益榜快照。

## 范围边界

### 本期包含

- IP 注册表：记录规范化 IP、聚合 key、hash、首次出现、最近出现和注册时间。
- IP 统计聚合：按 IP 聚合请求次数、成功 / 失败、Token、成本、活跃天数和最近使用时间。
- IP 运维页面：管理员在“系统运维 / IP统计”查看列表、筛选、排序、详情和策略状态。
- IP 策略：支持封禁、临时封禁、解封、观察名单和封禁命中记录。
- 后台 job：负责注册 IP、写入 IP 聚合、刷新范围窗口、刷新策略运行态缓存。

### 本期不包含

- 公益站用户与 IP 的映射：由 `juhe-ai-public-welfare` 自己维护。
- 公益榜快照、贡献榜、消耗榜和公开展示名称：由公益站自己快照汇总。
- 地区、国家、ASN、运营商、IP 段归属或代理标签封禁：容易误伤，不作为默认能力。
- 基于固定状态码 / 固定错误码的自动永久封禁：已有 IP 级错误熔断仍保持短 TTL 易失运行态。
- 浏览器或公网无鉴权读取 IP 统计：IP 统计属于管理员和受保护来源系统可读数据。

## 核心结论

- IP 统计是 `sub2api-lite` 的基础能力，应先于公益站外部接口落地。
- 页面和接口只读预聚合表或窗口表，不能为了 IP 列表临时扫描 `usage_records`。
- IP 注册 Set 是性能优化，不是事实源；最终正确性依赖 SQLite 唯一约束和 `INSERT OR IGNORE`。
- 首期只为 IP 写 `daily`、`totals` 和范围窗口，避免把 IP 维度直接接入全套 `minute / hourly / weekly / monthly` 造成写放大。
- 统计 scope 使用 `ip_hash`，列表接口再关联 IP 注册表返回可展示 IP。
- IPv6 默认按 `/64` 聚合作为统计 key，避免隐私 IPv6 地址导致 IP 基数爆炸；原始规范化 IP 可保留最近样本。
- IP 封禁是网关运行前置判断，必须读运行态缓存，不允许每次请求查库。

## 数据流

```text
网关请求
  -> usage_records 写入 client_ip
  -> 后台 IP 统计 job 按 usage shard cursor 增量读取新记录
  -> 规范化 IP，生成 aggregate_ip_key / ip_hash / bucket_no
  -> 查询内存 bucket Set 判断是否已注册
  -> 未命中则 INSERT OR IGNORE 到 client_ip_registry
  -> 写入 client_ip_stats_daily / client_ip_stats_totals
  -> 刷新 client_ip_usage_range_windows
  -> 管理页面和外部来源接口只读窗口表
```

封禁流程：

```text
管理员封禁 / 解封
  -> 写 client_ip_policies 和操作日志
  -> 通知网关运行态刷新策略缓存
  -> 网关请求进入时读内存策略缓存
  -> 命中 active blacklist 返回本地拒绝
  -> 异步记录封禁命中计数
```

## IP 规范化与聚合 key

IP 写入前必须先规范化：

- IPv4 去除端口、去除 `::ffff:` 前缀后保存标准点分十进制。
- IPv6 保存压缩后的标准形式。
- IPv6 统计默认使用 `/64` 聚合 key，例如同一 `/64` 下的隐私地址归为同一个 `aggregate_ip_key`。
- 如果请求无法识别 IP，统计为 `unknown` 不进入封禁策略。
- `client_ip` 表示最近一次原始规范化 IP，`aggregate_ip_key` 表示统计和策略匹配使用的 key。

建议 hash：

```text
ip_hash = sha256("client-ip:" + aggregate_ip_key)
bucket_no = stable_hash(ip_hash) % 4096
```

`ip_hash` 可以作为统计表 `scope_id` 或 IP 专用表主键，避免统计表索引和日志里到处出现明文 IP。

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
- request_count_seen INTEGER NOT NULL DEFAULT 0
- created_at TEXT NOT NULL
- updated_at TEXT NOT NULL
```

索引建议：

```text
idx_client_ip_registry_bucket ON client_ip_registry(bucket_no, ip_hash)
idx_client_ip_registry_last_seen ON client_ip_registry(last_seen_at DESC, ip_hash)
idx_client_ip_registry_ip ON client_ip_registry(aggregate_ip_key)
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
- first_token_ms_sum INTEGER NOT NULL DEFAULT 0
- first_token_ms_count INTEGER NOT NULL DEFAULT 0
- last_used_at TEXT
- last_error_at TEXT
- updated_at TEXT NOT NULL
- PRIMARY KEY (ip_hash, stat_date)
```

### client_ip_stats_totals

保存 IP 累计统计，列表累计视图和策略评估只读该表。

```text
client_ip_stats_totals
- ip_hash TEXT PRIMARY KEY
- request_count INTEGER NOT NULL DEFAULT 0
- success_count INTEGER NOT NULL DEFAULT 0
- error_count INTEGER NOT NULL DEFAULT 0
- input_tokens INTEGER NOT NULL DEFAULT 0
- output_tokens INTEGER NOT NULL DEFAULT 0
- cache_read_tokens INTEGER NOT NULL DEFAULT 0
- cache_read_cost_usd REAL NOT NULL DEFAULT 0
- total_cost_usd REAL NOT NULL DEFAULT 0
- active_days INTEGER NOT NULL DEFAULT 0
- first_used_at TEXT
- last_used_at TEXT
- last_error_at TEXT
- updated_at TEXT NOT NULL
```

`active_days` 不能在请求路径 `COUNT(DISTINCT stat_date)`；应由 worker 在刷新 totals 或范围窗口时维护。

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
- active_days INTEGER NOT NULL DEFAULT 0
- last_used_at TEXT
- last_error_at TEXT
- updated_at TEXT NOT NULL
- PRIMARY KEY (ip_hash, start_date, end_date)
```

排序索引按页面热点建立：

```text
idx_client_ip_range_cost ON client_ip_usage_range_windows(start_date, end_date, total_cost_usd DESC, ip_hash)
idx_client_ip_range_tokens ON client_ip_usage_range_windows(start_date, end_date, input_tokens DESC, output_tokens DESC, ip_hash)
idx_client_ip_range_requests ON client_ip_usage_range_windows(start_date, end_date, request_count DESC, ip_hash)
idx_client_ip_range_last_used ON client_ip_usage_range_windows(start_date, end_date, last_used_at DESC, ip_hash)
```

### client_ip_policies

保存 IP 运维策略。

```text
client_ip_policies
- id TEXT PRIMARY KEY
- ip_hash TEXT NOT NULL
- policy_type TEXT NOT NULL      -- blacklist | watch
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

- `blacklist + active` 表示封禁。
- `watch + active` 表示观察名单，不拦截请求。
- `expires_at` 为空表示长期生效；非空表示临时封禁或临时观察。
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

- worker 启动后分页读取 `client_ip_registry(bucket_no, ip_hash)` 预热 Set。
- 预热过程不能阻塞 worker 主循环太久，可以分批加载并记录进度。
- 预热未完成时，未命中 Set 仍执行 `INSERT OR IGNORE`，保证正确性。
- Set 只保存 hash，不保存明文 IP。
- Set 容量过高时可以启用 LRU 或只加载近期活跃 bucket，但唯一约束仍必须兜底。

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
- 写入 `client_ip_stats_daily` 和 `client_ip_stats_totals`。
- 推进 `stats_job_state` 或 IP 专用 job state。

禁止：

- 启动时全量扫描 `usage_records` 自动补历史。
- 页面请求时临时 `SUM/GROUP BY usage_records`。
- 前端拿明细自行 reduce。

### IP 范围窗口刷新 job

职责：

- 基于 `client_ip_stats_daily` 刷新最近 31 天内常用自然日范围。
- 只刷新受影响日期范围或固定窗口，不能每次扫全部历史。
- 写入 `client_ip_usage_range_windows`。

首期可支持：

- 今天
- 最近 7 天
- 最近 30 / 31 天
- 页面传入的最近 31 天内任意自然日范围

### IP 策略缓存刷新 job

职责：

- 加载 active 且未过期的 `blacklist` / `watch` 策略到网关运行态缓存。
- 管理员创建、解封或更新策略后主动触发刷新。
- 过期策略可由定时 job 标记为 disabled，也可以运行态读取时自然忽略，再由清理 job 归档。

## 管理端页面

菜单位置：

```text
系统管理
  -> 系统运维
    -> IP统计
```

页面只允许管理员访问。

### 列表字段

| 字段 | 说明 |
| --- | --- |
| IP | `aggregate_ip_key` 或规范化 IP |
| IP 版本 | IPv4 / IPv6 |
| 状态 | 正常 / 观察 / 已封禁 / 已过期 |
| 请求次数 | 范围内请求数 |
| 成功次数 | 范围内成功请求数 |
| 失败次数 | 范围内失败请求数 |
| 失败率 | `error_count / request_count` |
| 输入 Token | 范围内输入 token |
| 输出 Token | 范围内输出 token |
| 缓存读取 Token | 范围内缓存读取 token |
| 总 Token | `input_tokens + output_tokens` |
| 估算成本 | 范围内 `total_cost_usd` |
| 活跃天数 | 范围内有请求的自然日数量 |
| 首次出现 | 注册表 `first_seen_at` |
| 最近使用 | 窗口或注册表 `last_used_at / last_seen_at` |
| 最近错误 | `last_error_at` |
| 操作 | 查看详情、封禁、临时封禁、解封、加入观察 |

### 筛选与排序

筛选：

- 日期范围，最大最近 31 天。
- IP 关键词，精确或右侧前缀匹配。
- 状态：全部、正常、观察、已封禁。
- 高消耗：成本或 token 大于阈值。
- 高失败率：失败率大于阈值且请求数达到最小样本。
- 最近活跃：最近 N 小时 / N 天有请求。

排序：

- 成本降序
- 总 Token 降序
- 请求次数降序
- 失败率降序
- 最近使用时间降序

列表默认按成本降序，其次请求次数降序，再按 IP hash 稳定排序。

### 详情页

详情页建议展示：

- 基础信息：IP、版本、hash 前缀、首次出现、最近出现、当前策略。
- 统计摘要：请求数、成功数、失败数、失败率、Token、成本、活跃天数。
- 最近 31 天每日趋势：请求、Token、成本、失败。
- 错误摘要：最近错误时间、错误次数趋势。首期可只展示失败数，不新增错误码 Top。
- 相关操作历史：封禁、解封、观察名单变更。
- 封禁命中记录：命中次数和最近命中时间。

## 管理 API 草案

管理接口只允许管理员 Cookie 调用。

```http
GET /__aisys__/api/ip-stats
GET /__aisys__/api/ip-stats/:ipHash
POST /__aisys__/api/ip-stats/:ipHash/blacklist
POST /__aisys__/api/ip-stats/:ipHash/watch
POST /__aisys__/api/ip-stats/:ipHash/unblock
```

列表查询参数：

```text
startDate=YYYY-MM-DD
endDate=YYYY-MM-DD
page=1
pageSize=20
keyword=1.2.3
status=all | normal | watch | blacklisted
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
  total: number
  hasMore: boolean
  statsLagSeconds?: number | null
}

interface ClientIpStatsListItem {
  ipHash: string
  clientIp: string
  aggregateIpKey: string
  ipVersion: 4 | 6
  status: 'normal' | 'watch' | 'blacklisted'
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
  firstSeenAt: string
  lastSeenAt: string
  lastUsedAt?: string | null
  lastErrorAt?: string | null
}
```

封禁请求：

```ts
interface ClientIpBlacklistRequest {
  reason: string
  expiresAt?: string | null
}
```

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

- 每条有 IP 的业务使用记录会额外写入 IP 注册表、IP daily 和 IP totals。
- 使用 `daily + totals + range window` 控制写放大，首期不写全套六层通用统计桶。
- 批处理必须复用 prepared statement，避免每行反复 prepare。
- IP 注册表写入使用 Set 命中减少重复 `INSERT OR IGNORE`。

### 查询影响

- IP 列表按范围窗口表查询，配合排序索引。
- `total` 只作为分页上界，不执行精确全量 `COUNT(*)`。
- IP keyword 只支持精确或右侧前缀，不支持任意前导通配符全表扫描。
- 高失败率筛选需要有最小请求样本，例如 `request_count >= 10`，避免小样本误导。

### 启动影响

- Set 预热分页进行，不阻塞系统启动。
- 预热期间允许统计 job 正常运行，数据库唯一约束兜底。
- 如果 IP 注册量过大，可以只预热最近活跃 IP，冷 IP 靠 `INSERT OR IGNORE` 兜底。

## 安全与隐私

- IP 属于敏感运维数据，后台页面只允许管理员访问。
- 普通日志不输出完整 IP，可以输出 hash 前缀、策略状态和 traceId。
- 操作日志记录封禁、解封和观察名单变更，但不记录不必要的请求明细。
- 外部来源接口只返回 IP 聚合事实，不返回封禁原因、管理员 ID、内部策略历史或操作日志。
- 不做区域性封禁和外部标签自动定罪，避免误伤共享出口和移动网络用户。

## 与既有 IP 机制的关系

| 机制 | 存储形态 | 作用 | 本设计关系 |
| --- | --- | --- | --- |
| IP 级账号回避 | 进程内短 TTL | 同一 IP 短期避开刚失败账号 | 保持易失，不变成统计或封禁事实 |
| IP 级错误熔断 | 进程内短 TTL | 认证前 / 本地高置信错误短期止损 | 保持易失，不写永久黑名单 |
| IP 统计 | SQLite 预聚合 | 管理员观察和外部 IP 聚合事实 | 本文新增 |
| IP 封禁 | SQLite 策略 + 网关缓存 | 管理员明确操作后的拦截 | 本文新增 |

## 实施阶段

### 阶段 1：设计和计划

- 新增本文档。
- 新增 `PLAN-0031` 专项计划。
- 等待用户提交和部署当前项目后再开始实现。

### 阶段 2：后端事实层

- 新增 IP 注册表、IP daily、IP totals、IP range window、IP policy 表。
- 新增 IP 规范化、hash 和 bucket 工具。
- 新增后台 IP 注册与统计 job。
- 新增 IP 范围窗口刷新 job。

### 阶段 3：管理接口与页面

- 新增管理员 IP 统计列表、详情、封禁、解封接口。
- 在系统运维菜单新增 IP统计页面。
- 支持筛选、排序、分页、详情和策略操作。

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
- Set 未预热完成时，数据库唯一约束仍能保证注册正确。
- IPv6 默认按 `/64` 聚合。
- IP 列表可以按成本、Token、请求数、失败率和最近使用排序。
- IP 详情能展示范围摘要和每日趋势。
- 封禁后网关请求被本地拒绝，解封后恢复。
- 观察名单不拦截请求，只改变状态展示。
- 普通用户无法访问 IP 统计接口和页面。
- 外部来源接口不返回封禁策略和内部业务关系。
- 统计滞后通过 `statsLagSeconds` 明确展示。

