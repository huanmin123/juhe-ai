# Prometheus 指标暴露设计

> 面向后端、部署与 AI 维护者。
> 本文是应用监控指标暴露（`GET /__aisys__/metrics`）与容量观测分工的行为契约权威：监控分工、指标族清单、端点与安全边界、env 契约变化、抓取与面板交付物位置、已删除监控功能的映射与验收标准。接口权限与通用错误语义以 [接口契约与权限矩阵](接口契约与权限矩阵.md) 为准；生产部署细节以 `docker/single-server/README.md` 与 [部署指南](../deploy/部署指南.md) 为准。
> 2026-10-14 起：原管理后台「系统指标统计」页与「表监控」页整体下线，应用进程监控与容量观测统一收敛到 Prometheus pull 抓取 + Grafana 面板；本文替代原 [系统指标统计设计] 与 [表数据监控设计] 两篇文档（两篇已删除，git 历史可溯）。

## 目标

- gateway 与 jobs 两个 Go 进程经既有健康监听暴露 Prometheus text exposition 指标：gateway `3306`、jobs `3305`，路径统一为 `GET /__aisys__/metrics`（promhttp handler）。
- 采用 Prometheus pull 模型：infra Prometheus 经既有 SSH 隧道按 `60s` 间隔抓取，不引入 Pushgateway、不自建采样窗口表、不在应用内做趋势预聚合。
- 表/数据库容量观测由 postgres_exporter 承担；主机、容器、日志观测复用既有 infra（node_exporter、cadvisor、Loki + alloy），应用不重复实现。
- 监控指标不含 URL、账户 / 用户 ID、追踪号、请求内容或错误正文；所有自定义族保持低基数。

## 非目标

- 不恢复管理后台监控页面：系统指标统计页、表监控页及其全部读接口随本改造删除，不在前端重建等价页面。
- 不恢复 Node 兼容指标族与进程内自采样链路：不再有 `gometrics` 采样器、`go-runtime-trend` 读接口、`go_runtime_metrics_*` / `system_metrics_*` / `process_event_loop_*` 窗口表，也不恢复 `juhe_ai_process_*`、`juhe_ai_redis_stream_queue_enabled`、`juhe_ai_http_failure_scope_metrics_enabled` 等 Node 兼容族。
- 不做跨实例 / 多节点聚合：每个抓取目标只反映本进程事实；多实例由 Prometheus 按实例标签天然区分。
- 不改变业务统计：用量、成功率、模型质量仍属用量统计与 AI 性能监控的统计结果库口径，与 `/__aisys__/metrics` 无关。

## 监控分工

| 观测对象 | 承担方 | 说明 |
| --- | --- | --- |
| 应用进程指标（HTTP、上游失败、任务、主业务池、go_*/process_*） | gateway / jobs 的 `GET /__aisys__/metrics` | 本文契约；`github.com/prometheus/client_golang v1.23.2`，只允许 `backend-go/shared/platform/prommetrics` 包 import client_golang |
| 表 / 数据库容量 | postgres_exporter | `juhe_postgres_db_size_bytes`、`juhe_postgres_table_size_bytes`、`juhe_postgres_table_rows`（增补查询在 `docker/single-server/monitoring/`） |
| 主机资源（CPU / 内存 / 磁盘 / 文件系统） | node_exporter | 既有 infra；SQLite standalone 形态的文件容量观测也走主机层文件系统指标，应用不再自采样库文件大小 |
| 容器资源 | cadvisor | 既有 infra |
| 日志 | Loki + alloy | 既有 infra；运行日志 / 审计的应用内事实链路不变 |

## 端点与安全边界

- gateway 健康监听经 `JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS` 配置，同一监听同时承载 owner readiness 健康端点与 `GET /__aisys__/metrics`；jobs 的 `JUHE_AI_JOBS_HEALTH_LISTEN_ADDRESS`（jobs 侧保留）同理承载 jobs `/health` 与 `GET /__aisys__/metrics`。
- 部署边界（生产 compose 形态）：gateway 配置 `JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS=0.0.0.0:3306`（容器内监听全网卡才能被端口发布映射）且端口发布固定为 `127.0.0.1:3306:3306`；jobs 同理监听 `0.0.0.0:3305` 且端口发布固定为 `127.0.0.1:3305:3305`。宿主侧只有回环可达，部署层必须拒绝公网入口。
- 直跑 / 开发形态（`scripts/start-go-project.mjs`）缺省监听 `127.0.0.1:3306`（gateway）与 `127.0.0.1:3305`（jobs），天然仅本机可达。
- infra Prometheus 经既有 SSH 隧道访问宿主回环 `127.0.0.1:3306/3305`，抓取间隔 `60s`；隧道为用户自管外部资产，不在 compose 内复制。
- `/__aisys__/metrics` 不参与管理 API 限流，不需要登录态；安全完全依赖部署边界（回环发布 + 隧道），不得将该端口发布到非回环地址或加入反代。

## 指标族清单

### gateway 指标族

| 指标 | 类型 | 标签 | 含义 |
| --- | --- | --- | --- |
| `juhe_ai_http_requests_total` | counter | `route_group`、`method`、`status_class`、`outcome`、`failure_scope` | 管理面 / 公开面 / `/v1` 网关链 HTTP 请求计数。`status_class` 按状态码百位归类为 `1xx/2xx/3xx/4xx/5xx/unknown` 六类（无 `499` 等单码，状态码缺失或越界归 `unknown`）；`outcome` 只有 `completed`（正常返回）/ `aborted`（请求上下文取消）两值；`failure_scope` 只有 `none/gateway/upstream` 三值，仅 `completed` 且 `5xx` 时可取非 `none`（未给出时归 `gateway`），其余组合恒为 `none` |
| `juhe_ai_http_request_duration_seconds` | histogram | 同 `juhe_ai_http_requests_total` | 同一标签集的请求耗时直方图 |
| `juhe_ai_http_requests_in_flight` | gauge | `route_group`、`method` | 当前在途请求数 |
| `juhe_ai_gateway_upstream_failures_total` | counter | `failure_class`、`reason_class`、`status_class` | 网关上游失败分类计数，用于区分"尚无失败"与"实现未暴露"的恒定契约信号保留在该族暴露面内 |
| `juhe_ai_gateway_first_output_duration_seconds` | histogram | `method` | 首输出（首 token / 首响应）耗时直方图 |
| `juhe_ai_audit_log_captured_dropped_total` | counter | `service` | 审计日志捕获丢弃计数（活跃捕获超限等），只暴露丢弃事实，不承载正文 |
| `juhe_ai_operation_log_dropped_total` | counter | `service` | 操作日志投递丢弃计数 |
| `juhe_ai_gateway_db_pool_open_connections` | gauge | 无 | 主业务 PG 池当前已建立连接数（`sql.DBStats.OpenConnections`，抓取时读取） |
| `juhe_ai_gateway_db_pool_in_use_connections` | gauge | 无 | 主业务 PG 池使用中连接数（`InUse`） |
| `juhe_ai_gateway_db_pool_idle_connections` | gauge | 无 | 主业务 PG 池空闲连接数（`Idle`） |
| `juhe_ai_gateway_db_pool_max_open_connections` | gauge | 无 | 主业务 PG 池最大打开连接上限（`MaxOpenConnections`） |
| `juhe_ai_gateway_db_pool_wait_count_total` | counter | 无 | 等待空闲连接的累计次数（`WaitCount`；单调累计，必须按 counter 查询 `rate()/increase()`） |
| `juhe_ai_gateway_db_pool_wait_duration_seconds_total` | counter | 无 | 等待空闲连接的累计时长秒数（`WaitDuration`；单调累计，必须按 counter 查询） |

连接池族的暴露范围（2026-10-16 登记）：只暴露**主业务池**——组合根 role `gateway-system-api`（URL 来源 `JUHE_AI_BUSINESS_POSTGRES_URL`，唯一；codex context store 是同 role 同 URL 的引用共享，同属该池）。池去重键是 `{URL, role}`，因此 `gateway-chat`（同 URL、role `gateway-chat`）是**独立池实例**，其连接不体现在本族序列中；`gateway-store` role（F3 审计 / F4 操作日志池）在配置独立 URL 时同样会出现多个池实例。这些池不纳入暴露，避免同 role 多 URL 或同 URL 多 role 产生无法区分的重复序列；纳入新池前必须先在本文登记 role 与实例区分规则。抓取时读 `database/sql.DBStats`，无 provider 注入时整族缺席（与审计 / 操作日志丢弃计数同模式）。

标准运行时族由 client_golang 默认收集器提供（`go_goroutines`、`go_memstats_*` 等 `go_*` 与 `process_resident_memory_bytes`、`process_cpu_seconds_total`、`process_open_fds` 等 `process_*`），应用不自建等价指标。

### jobs 指标族

| 指标 | 类型 | 标签 | 含义 |
| --- | --- | --- | --- |
| `juhe_ai_jobs_task_runs_total` | counter | `job_name`、`job_type`、`status` | 按 jobregistry 任务名 / 类型 / 执行结局（成功、失败、跳过等）计数的任务执行累计 |
| `juhe_ai_jobs_task_run_duration_seconds` | histogram | `job_name`、`job_type` | 任务单轮执行耗时直方图 |
| `juhe_ai_jobs_task_last_success_timestamp_seconds` | gauge | `job_name`、`job_type` | 每个任务最近一次成功执行的 Unix 时间戳，用于"任务停摆"类告警（`time() - 指标值 > 预期间隔 × 容忍倍数`；适用边界见下） |

任务指标适用边界（2026-10-16 登记）：

- 埋点唯一入口是 taskruns `RunWithTaskRun` 的终态收口，只覆盖「PostgreSQL 驱动 + 经任务调度器（租约 + 运行历史包裹）注册的任务」。SQLite / 非 PostgreSQL 装配、租约未启用、`runWiredJobOnce` 单轮直调与 once/migration 短命进程均不产生任务指标。
- 驻留型组件（runtime-log-indexer、account-health 探活、outcome projection、business-result-projector、proxy-latency）不经过调度器收口，没有 `juhe_ai_jobs_task_*` 序列。
- `juhe_ai_jobs_task_last_success_timestamp_seconds` 子序列在任务首次 `completed` 时才创建：**从未成功的任务没有该序列**，纯 `time() - 指标值` 查询会漏掉"从未成功"。停摆告警需配合 `absent(...)` 或以 `juhe_ai_jobs_task_runs_total` 的存在性兜底。
- 生产当前为 PostgreSQL 单机形态，经调度器注册的定时任务覆盖基本可用；SQLite 形态下任务指标整体缺席属预期。

### 基数约束

- 自定义族标签只允许上表枚举值；禁止把路由路径、模型名、账户 / 用户 / API Key ID、上游地址、错误正文等高基数或敏感值放入标签。
- `route_group` 枚举为 `health` / `management` / `public_api` / `gateway` / `other` / `observability` 六值；`/__aisys__/metrics` 自身归 `observability` 且**不参与计量**（双重排除：分类器短路 + 端点挂健康监听不经指标中间件），因此有效 `route_group` 为五类。`method` 归一化为七个标准 HTTP 方法 + `OTHER`；`status_class` / `outcome` / `failure_scope` 取值见指标族表；`reason_class`（九值：`quota`/`rate_limit`/`authorization`/`protocol`/`timeout`/`transport`/`upstream_5xx`/`upstream_4xx`/`unknown`）与 `failure_class`（三值：`opaque_upstream_response`/`transport`/`unknown`）为固定分类词表。新增标签值前必须先在本文登记基数上界。
- 单实例序列上界（标签笛卡尔积理论值，实际受语义约束更小）：
  - `juhe_ai_http_requests_total`：5 类有效 `route_group` × 8 `method` × 13 个有效 (status_class, outcome, failure_scope) 组合（12 个 (status_class, outcome) 组合中 11 个恒配 `none`，`5xx`+`completed` 发射 `gateway/upstream` 两值——`none` 在该组合被归一为 `gateway`，不可达）= **520**。
  - `juhe_ai_http_request_duration_seconds`：同标签集，每组合 8 桶 + `+Inf` + `_sum` + `_count` = 11 序列，上界 **5720**。
  - `juhe_ai_http_requests_in_flight`：5 × 8 = **40**。
  - `juhe_ai_gateway_upstream_failures_total`：3 `failure_class` × 9 `reason_class` × 6 `status_class` = **162**。
  - `juhe_ai_gateway_first_output_duration_seconds`：8 `method` × 11 = **88**。
  - `juhe_ai_gateway_db_pool_*`：主业务池固定 **6** 序列（无标签）。
  - `juhe_ai_jobs_task_runs_total`：注册任务数 × 状态数（3 终态）；`juhe_ai_jobs_task_last_success_timestamp_seconds`：注册任务数（仅出现过的 job_name/job_type 组合）。
- 指标不含任何请求内容、追踪号或凭据；错误正文只允许进入分类词表，不允许进入标签或指标名。
- 队列深度类指标（2026-10-16 裁决）**暂缓**：队列对象、生命周期与既有原子计数 seam 未登记前不新增族；明确后按本节流程补登基数与语义再实现。

### 已删除的 Node 兼容族

`juhe_ai_process_*`、`juhe_ai_redis_stream_queue_enabled`、`juhe_ai_http_failure_scope_metrics_enabled` 随本改造删除，不得在 Go 侧恢复同名或同语义族；Redis Stream 队列、IPC pending 队列本就是 Node 历史机制，Go 无对应对象。

## 表 / DB 容量观测

- 容量事实由 postgres_exporter 抓取生产 PostgreSQL 承担：`juhe_postgres_db_size_bytes`（库 / schema 容量）、`juhe_postgres_table_size_bytes`（表级大小）、`juhe_postgres_table_rows`（估算行数）；增补查询（含索引体积与分区可见性所需的自定义 query）随 `docker/single-server/monitoring/` 交付。
- 增长趋势（原表监控页的 1 小时 / 24 小时增长列）由 Prometheus `increase()` / `rate()` 在查询期表达，不再预写快照表。
- SQLite standalone 形态的库文件容量走 node_exporter 文件系统指标；应用不再实现 SQLite 文件 / 表级采样。

## env 契约变化

本改造只做减法，无新增 env：

- 删除 `JUHE_AI_GO_RUNTIME_METRICS_*` 全家族（`STORE`、`POSTGRES_URL`、`ROLE`、`INTERVAL`、`RETENTION_DAYS`、`SERVICE`、`DATABASE_PATH`，以及历史 `ENABLED` / `URL`）：gometrics 采样器与统计库窗口表随功能删除，残留配置值不进入子进程（启动器拒转清单收敛）。
- 删除 `JUHE_AI_TABLE_MONITOR_*` 全家族（`STORE`、`POSTGRES_URL`、`DATABASE_PATH`、`INSTANCE_ID`、`INTERVAL`、`RUN_TIMEOUT`、`OWNER_LEASE`、`RETENTION_DAYS`、`MAX_TABLES`、`MAX_CONCURRENT_SOURCES`、`RETENTION_BATCH_SIZE`、`RETENTION_MAX_BATCHES`，以及 Node 时代 `OVERVIEW_FRESH_MS` / `OVERVIEW_STALE_MS` 与测试注入变量）：F2 表监控组件整体删除。
- 删除 gateway 侧 `JUHE_AI_JOBS_HEALTH_LISTEN_ADDRESS`（原用于系统指标页跨进程抓取 jobs 健康，链路已删）；jobs 侧该 env 保留，仍是 jobs 健康监听与 `/__aisys__/metrics` 的监听配置。
- 系统设置键删除：表监控的 `tableMonitorMaxTablesPerRun` 与系统指标采样 `systemMetricsSampleIntervalSeconds`，不得恢复。

## 抓取与面板交付物

监控交付物统一在 `docker/single-server/monitoring/`（部署域权威，随部署配置同步维护）：

- Prometheus 抓取配置片段：经 SSH 隧道指向宿主回环 `127.0.0.1:3306`（gateway）与 `127.0.0.1:3305`（jobs），`scrape_interval: 60s`；含 postgres_exporter target。
- postgres_exporter 增补查询：`juhe_postgres_db_size_bytes`、`juhe_postgres_table_size_bytes`、`juhe_postgres_table_rows` 及索引体积等自定义 query 定义。
- Grafana 面板 JSON：网关 HTTP / 上游失败 / 首输出面板、jobs 任务运行 / 停摆面板、go_/process_ 运行时面板、DB 容量面板。
- `docker/single-server/monitoring/README.md`：抓取接入、面板导入与验证步骤。

`.env` 清理清单：生产与开发环境删除上述两家族全部变量与 gateway 侧 `JUHE_AI_JOBS_HEALTH_LISTEN_ADDRESS`；无需新增任何变量。

端点抓取开销实测（2026-10-16，生产宿主 103.36.63.105 回环，命令 `for i in 1 2 3 4 5; do curl -sS -o /dev/null -w "%{time_total} %{size_download}\n" http://127.0.0.1:<port>/__aisys__/metrics; done`，5 次连续采样）：gateway `3306` 单次 0.0017–0.0032s、约 16KB、45 族 / 87 样本；jobs `3305` 单次 0.0030–0.271s（中位 0.0034s）、约 70KB、41 族 / 472 样本。抓取预算评估以此实测为准，不以其他未存档数字为准。

## 失联告警（2026-10-16 起执行）

进程失联（进程退出、隧道中断、端口失监听、target 从抓取配置消失）由 infra Prometheus 规则承载，权威文件 `F:\服务部署\alert-rules\juhe-app-alerts.yml`（与 `AppGoroutinesHigh` 等同组管理，修改后同步 infra rules 目录热加载；Grafana 侧仅做呈现，无 Grafana-managed 规则）：

| 规则 | 表达式 | for | 说明 |
| --- | --- | --- | --- |
| `AppScrapeDown` | `up{job=~"juhe-ai-(gateway\|jobs)"} == 0` | 5m | target 存在但抓取失败；60s 采样下 5 个点确认，兼顾隧道抖动不误报 |
| `AppScrapeTargetMissing` | `absent(up{job="juhe-ai-gateway"}) or absent(up{job="juhe-ai-jobs"})` | 5m | target 从抓取配置消失（`up` 序列不复存在，`== 0` 不会触发，需 `absent` 兜底） |

告警呈现在 Grafana Alerting；通知仍按既有裁定走巡检模式，不接入主动通知渠道。

## 已删除功能 → 新方案映射

| 已删除能力（页面 / 面板） | 新方案（Grafana 面板 / exporter 查询） |
| --- | --- |
| 系统指标页·进程状态双卡（gateway/jobs owner readiness 透传） | 进程存活用 Prometheus `up{...}`；jobs 就绪度用 `juhe_ai_jobs_task_last_success_timestamp_seconds` 停摆判断与 jobs `/health`（容器健康检查继续消费）；gateway F3/F4 owner 健康仍由 gateway 自身健康检查承载 |
| 系统指标页·Go Runtime 趋势（goroutine / heap / CPU 角色窗口） | Grafana 面板直读标准族：`go_goroutines`、`go_memstats_heap_alloc_bytes`、`process_resident_memory_bytes`、`rate(process_cpu_seconds_total)` 等；趋势范围由 Prometheus 本地保留期承担，不再自建窗口表 |
| 系统指标页·后台任务表（`background_task_runs` 读接口） | Grafana 面板读 `juhe_ai_jobs_task_runs_total` / `juhe_ai_jobs_task_run_duration_seconds` / `juhe_ai_jobs_task_last_success_timestamp_seconds`；`background_task_runs` 表与 taskruns 基础设施保留（任务历史、对账与告警的数据面），仅页面与 HTTP 读接口删除 |
| 表监控页·库摘要卡（库 / schema 大小、WAL、空闲空间、表数量） | `juhe_postgres_db_size_bytes` 按 schema 维度；WAL 与空闲空间类 SQLite 专属口径不再提供（SQLite 文件容量走 node_exporter 文件系统指标） |
| 表监控页·表级列表（行数、表 / 索引大小、1h / 24h 增长） | `juhe_postgres_table_size_bytes`、`juhe_postgres_table_rows`；索引体积由 monitoring/ 增补查询提供；增长用 `increase()` 查询期计算 |
| 表监控页·按库趋势图 | Grafana 面板按 schema / 表标签分组的时间序列 |
| 表监控告警判断（索引 / 表大小比、24h 增长超预期、未分区大表、shard 分布倾斜、archive 增长） | 迁移为 Prometheus 告警规则口径：基于上述指标与 `rate()` 表达同等判断；shard 分布与 archive 目录增长属 SQLite / 文件层，由主机层（node_exporter + Loki）表达，应用不再采样 |

## 已删除清单（减法边界）

- 页面与接口：管理后台「系统指标统计」页与 6 条 `/__aisys__/api/stats/system-metrics/*` 读接口；「表监控」页与其读接口。
- 代码：`backend-go/shared/platform/gometrics` 整包；jobs 的 `system-metrics-sample` / `system-metrics-trend-windows-refresh` 注册任务与 legacy 聚合；F2 表监控组件（jobs `internal/tablemonitor` 采样链与 gateway 侧表监控读面）。
- 存储表（12 张退役表）：统计库 9 张——`go_runtime_metrics_samples`、`go_runtime_metrics_hourly`、`go_runtime_metrics_trend_windows`、`system_metrics_samples`、`system_metrics_hourly`、`system_metrics_trend_windows`、`process_event_loop_samples`、`process_event_loop_hourly`、`process_event_loop_trend_windows`；表监控库 3 张——`database_storage_snapshots`、`table_storage_snapshots`、`table_monitor_owner_leases`。表清退经 maintenance 一次性命令 `--drop-retired-monitoring-tables` 执行（生产执行需用户另行授权）。
- 设置键：`systemMetricsSampleIntervalSeconds` 与表监控的 `tableMonitorMaxTablesPerRun`。
- 保留：`background_task_runs` 表与 taskruns 基础设施（任务历史 + 对账 + 告警数据面）；jobs `/health` readiness（删除其中 tableMonitor 就绪项）。

## 验收标准

1. **curl 冒核**：生产宿主机 `curl -fsS http://127.0.0.1:3306/__aisys__/metrics` 与 `curl -fsS http://127.0.0.1:3305/__aisys__/metrics` 返回 Prometheus text exposition，可见 `go_*` / `process_*` 与对应 `juhe_ai_*` 族；从宿主回环以外地址访问 3306/3305 被拒绝（端口仅回环发布）。
2. **抓取与面板**：infra Prometheus 两个 target UP（`scrape_interval=60s`，经 SSH 隧道）；Grafana 导入 monitoring/ 面板后可见网关请求 / 上游失败 / 首输出、jobs 任务运行与最近成功时间、`go_*` / `process_*` 运行时和 postgres_exporter 容量序列。
3. **告警口径迁移**：原表监控告警判断（索引放大、异常增长）与系统指标页人工巡检改为 Prometheus 告警规则 / Grafana 面板表达；运维文档不再指向已删除页面。失联告警（`AppScrapeDown` / `AppScrapeTargetMissing`，2026-10-16 起）随 `juhe-app-alerts.yml` 同步 infra 后热加载生效，Grafana Alerting 可见规则状态。
4. **减法回归**：启动器 / sidecar env 回归确认两家族 env 与 gateway 侧 `JUHE_AI_JOBS_HEALTH_LISTEN_ADDRESS` 不再转发（残留值被拒转清单剥离）；mockdata 任务清单不再引用 `system-metrics-sample` / `system-metrics-trend-windows-refresh`；`__aisys__` 前端路由不再包含已删除两页。
5. **主业务池指标**（2026-10-16 起）：PostgreSQL 形态的 gateway `/__aisys__/metrics` 暴露 `juhe_ai_gateway_db_pool_*` 六序列且值与真实池一致（`wait_*` 两项为 counter 类型）；SQLite 形态整族缺席。
