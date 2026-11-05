# 监控接入（Prometheus + Grafana）

> 面向自己部署 `juhe-ai` 的维护者，以及拿到同一套代码与环境、希望复用 `juhe-ai 应用总览` 面板的团队。
> 本文只描述**当前单机 Docker 生产形态**的监控接入；Redis Cluster 是未来可选能力，生产当前未启用，差异见 §9。
> 监控机地址、生产机地址、Grafana 账号、隧道端口等实例事实只在私有目录 `.local/project-resources/prod/` 留档，本文与仓库内所有监控配置均使用占位符。
> 应用指标契约（指标族、标签基数、端点安全边界）的权威是 [Prometheus 指标暴露设计](../../functions/Prometheus指标暴露设计.md)；部署配置契约的权威是 [`docker/single-server/README.md`](../../../docker/single-server/README.md)。

## 0. 结论与范围

- 需要的东西已经全部在仓库里，**不需要自己拼装**：应用机采集端在 [`docker/single-server/monitoring/collector/`](../../../docker/single-server/monitoring/collector/)，监控机栈在 [`docker/single-server/monitoring/stack/`](../../../docker/single-server/monitoring/)，Grafana 面板在 [`docker/single-server/monitoring/stack/dashboards/`](../../../docker/single-server/monitoring/stack/dashboards/)。
- 最小可用链路只有三段：应用暴露 `/__aisys__/metrics` → Prometheus 按 60s 抓取 → Grafana 加载面板。postgres_exporter、node_exporter、cadvisor、Loki、blackbox 都是可选增强，不装不影响“网关流量”和“Go Runtime”行。
- `juhe-ai 应用总览` 依赖本应用专属的 `juhe_ai_*`、`go_*`、`process_*` 指标；复用方必须是同一套 `juhe-ai` 后端。只有主机指标的环境无法直接复用该面板。
- 告警规则随 `stack/prometheus/rules/juhe-ai-alerts.yml` 交付，是**基线**而非固定值；阈值按各自机器规格与流量校准后再上线。

## 1. 监控分层与数据流

| 观测对象 | 数据来源 | 采集方式 | 仓库交付 |
| --- | --- | --- | --- |
| 应用进程（HTTP、上游失败、首字延迟、任务、PG 池、`go_*` / `process_*`） | gateway / jobs 的 `GET /__aisys__/metrics` | Prometheus pull，60s | 应用自带端点 |
| 任务停摆 / 统计游标 / 账户状态 / 库表容量 | postgres_exporter + `queries.yaml` | Prometheus pull，60s | `collector/` |
| 主机 CPU / 内存 / 磁盘 / 文件系统 | node_exporter | Prometheus pull，30s | `collector/` |
| 容器资源 | cadvisor | Prometheus pull，30s | `collector/` |
| Redis 内存 / 连接 / 慢命令 | redis_exporter + SLOWLOG 快照 cron | Prometheus pull / 宿主 cron | `collector/` |
| 应用日志 | Loki + Alloy | 应用容器日志推送 | `collector/` + `stack/` |
| 入口可用性与公网链路 | blackbox_exporter | Prometheus probe，60s | `stack/`（target 默认注释） |

```text
应用机（juhe-ai compose）
  gateway 127.0.0.1:3306 /__aisys__/metrics ─┐
  jobs    127.0.0.1:3305 /__aisys__/metrics ─┤
  node/cadvisor/postgres/redis exporter ─────┤ 全部只绑 127.0.0.1
                                             │
                                   SSH 隧道（监控机建立）
                                             │
监控机                              Prometheus（60s）──> Grafana
                                             └──> Alertmanager（可选）
  Alloy ── 反向隧道 ──> Loki ──> Grafana Explore
```

关键边界：应用指标端口只发布到**宿主回环**，不经过 Caddy、不加入公网反代、不做登录鉴权；安全完全依赖“回环发布 + SSH 隧道”。这与业务端口 `80/443` 的边界不同，不能混用。

### 1.1 参考部署形态

当前生产是单机 Docker，监控不在应用 compose 内，由独立监控机承载：

- 应用机：`juhe-ai` 的 PG/Redis/gateway/jobs/Caddy，以及 `collector/` 的 exporter（全部回环监听）。
- 监控机：`stack/` 的 Prometheus 抓取应用与 exporter，Grafana 提供 `juhe-ai 应用总览`、主机、PostgreSQL、Redis、日志面板，Loki 收应用容器日志。
- 隧道：指标用监控机 → 应用机的本地转发（`-L`），日志用监控机 → 应用机的反向转发（`-R`）。
- 告警：规则在 Prometheus 评估，Grafana Alerting 页看红绿灯；当前参考环境采用“规则 + 每日巡检”，未接主动通知。接不接通知由各部署方决定。
- 生产当前不启用 Redis Cluster；集群差异见 §9。

## 2. 快速开始（两个目录，直接复制）

### 2.1 应用机：`collector/`

```sh
# 上传 collector/ 到应用机 /opt/juhe-monitoring
cd /opt/juhe-monitoring
cp .env.example .env && chmod 600 .env    # 填 MONITORING_PG_PASSWORD / REDIS_PASSWORD
sh install.sh                             # 建只读角色 → 起采集容器 → 装 cron → 自检
```

`install.sh` 是幂等的：重复执行只会补齐缺失步骤（含 PG 只读角色与 `idx_bg_task_runs_job_finished` 索引——缺索引时任务停摆查询会退化为全表扫描）。它不会修改应用 compose、不会开防火墙、不会发布任何公网端口。

验证（全部应有输出）：

```sh
curl -fsS http://127.0.0.1:9100/metrics | head -n 2
curl -fsS http://127.0.0.1:18081/metrics | head -n 2
curl -fsS http://127.0.0.1:9187/metrics | grep -c '^juhe_postgres_'
curl -fsS http://127.0.0.1:9121/metrics | head -n 2
curl -fsS http://127.0.0.1:3306/__aisys__/metrics | head -n 2
curl -fsS http://127.0.0.1:3305/__aisys__/metrics | head -n 2
```

细节与文件清单见 [`collector/README.md`](../../../docker/single-server/monitoring/collector/README.md)。

### 2.2 监控机：`stack/`

```sh
# 上传 stack/ 到监控机 /opt/juhe-monitoring-stack（含 dashboards/ 面板）
cd /opt/juhe-monitoring-stack
cp .env.example .env && chmod 600 .env    # 填 GRAFANA_ADMIN_PASSWORD
sh install.sh                             # 起容器 → 等就绪 → 自检 → 打印应用 target 状态
```

验证：

```sh
curl -fsS http://127.0.0.1:19091/-/ready
curl -fsS http://127.0.0.1:13100/ready
curl -fsS http://127.0.0.1:19115/ | head -n 2
```

Grafana 打开 `http://127.0.0.1:3000`（或 `.env` 的 `GRAFANA_ROOT_URL`），Dashboards → `juhe-ai` 目录即可看到全部面板。细节见 [`stack/README.md`](../../../docker/single-server/monitoring/stack/README.md)。

### 2.3 建立 SSH 隧道（监控机 → 应用机）

指标六条本地转发：

```sh
ssh -N \
  -L 127.0.0.1:19107:127.0.0.1:9100 \
  -L 127.0.0.1:19108:127.0.0.1:18081 \
  -L 127.0.0.1:19109:127.0.0.1:9187 \
  -L 127.0.0.1:19110:127.0.0.1:9121 \
  -L 127.0.0.1:19111:127.0.0.1:3306 \
  -L 127.0.0.1:19112:127.0.0.1:3305 \
  <user>@<app-host>
```

日志一条反向转发：

```sh
ssh -N -R 127.0.0.1:13100:127.0.0.1:13100 <user>@<app-host>
```

长期运行用 `autossh` + systemd，配置 `ServerAliveInterval`、`ExitOnForwardFailure=yes`。隧道未建立时 Prometheus target 为 `down`，属预期。

### 2.4 已有监控栈、只接应用指标

只做两件事：

1. Prometheus 增加两个 job（job 名必须一致，路径 `/__aisys__/metrics`，60s）：

```yaml
scrape_configs:
  - job_name: juhe-ai-gateway
    scrape_interval: 60s
    metrics_path: /__aisys__/metrics
    static_configs:
      - targets: ["127.0.0.1:<gateway-tunnel-port>"]
  - job_name: juhe-ai-jobs
    scrape_interval: 60s
    metrics_path: /__aisys__/metrics
    static_configs:
      - targets: ["127.0.0.1:<jobs-tunnel-port>"]
```

2. 导入 `stack/dashboards/juhe-ai-app-overview.json`，绑定两个 Prometheus 数据源（`DS_PROMETHEUS` / `DS_POSTGRES_EXPORTER`）。

## 3. 应用指标端点

| 服务 | 应用机宿主地址 | 容器内监听 | 指标路径 | 同端口其他端点 |
| --- | --- | --- | --- | --- |
| gateway | `127.0.0.1:3306` | `0.0.0.0:3306`（compose 显式配置） | `/__aisys__/metrics` | `/health`、`/debug/pprof/` |
| jobs | `127.0.0.1:3305` | `0.0.0.0:3305`（compose 显式配置） | `/__aisys__/metrics` | `/health` |

容器内必须监听 `0.0.0.0` 才能被 compose 端口映射转发；宿主侧发布固定为 `127.0.0.1:<port>:<port>`，公网与容器网络都不能直接访问。不要为“方便抓取”改成全网卡发布，也不要在 Caddy 反代。

输出应包含 `go_goroutines`、`process_start_time_seconds` 等标准族；gateway 还应出现 `juhe_ai_http_requests_total`。`curl` 失败时先查容器是否 healthy、端口映射是否存在，不要先改 Prometheus。

## 4. Prometheus 抓取约定

- 应用 job 名固定 `juhe-ai-gateway` / `juhe-ai-jobs`，面板与告警都按 `job=~"juhe-ai-(gateway|jobs)"` 过滤；改名会让整块面板与告警失效。
- 应用抓取间隔 **60s**，`metrics_path: /__aisys__/metrics`（非默认路径，必须显式写）。
- 面板所有 `rate()` / 直方图查询用 `[5m]` 窗口（≥5 个采样点），默认刷新 `1m`。不要调密到 15s/30s：不会让 `[5m]` 更准，只会增加隧道与应用机负担。
- 主机 / 数据库 exporter 可 30-60s；exporter 多、机器弱时统一 60s 也够用。
- 校验：`promtool check config <file>`，再看 Prometheus → Status → Targets 两个应用 job 是否 `up=1`。

## 5. Grafana 面板

### 5.1 随仓库交付的面板

| 文件 | 标题 | 数据源 | 说明 |
| --- | --- | --- | --- |
| `stack/dashboards/juhe-ai-app-overview.json` | juhe-ai 应用总览（单机部署） | Prometheus + postgres_exporter 查询 | 核心面板，UID `juhe-ai-app-overview` |
| `stack/dashboards/host-overview.json` | 主机总览（juhe-ai 单机部署） | Prometheus（node_exporter + cadvisor） | 主机 CPU/内存/磁盘/网络/容器 |
| `stack/dashboards/postgres-overview.json` | PostgreSQL 总览（juhe-ai 单机部署） | Prometheus（postgres_exporter） | 连接、事务、缓存、锁、容量、WAL |
| `stack/dashboards/redis-overview.json` | Redis 总览（juhe-ai 单机部署） | Prometheus（redis_exporter） | 内存、命中、客户端、淘汰、慢命令 |
| `stack/dashboards/logs-overview.json` | 应用日志总览（juhe-ai 单机部署） | Loki | 按容器/级别/traceId/内容检索 |

所有 JSON 均不含服务器地址、账号、密钥或数据源 UID（`DS_PROMETHEUS` / `DS_POSTGRES_EXPORTER` 由导入变量选择）。

### 5.2 应用总览面板结构

| 行 | 面板 | 依赖 |
| --- | --- | --- |
| 概览 | 抓取目标 up、进程运行时长 | `up`、`process_start_time_seconds` |
| 网关流量 | 请求速率 by `route_group` × `status_class`、5xx 占比、时延 P95、在途请求、上游失败速率、首字延迟 P95 | gateway 的 `juhe_ai_http_*` / `juhe_ai_gateway_*`；选中 jobs 时该行为空属正常 |
| Go Runtime 与主业务池 | goroutines、堆分配、RSS、CPU、GC 暂停、文件描述符、OS 线程、PG 池连接数/等待 | `go_*`、`process_*`、`juhe_ai_gateway_db_pool_*`；池族只在 gateway |
| 后台任务 | 任务运行速率、失败率、耗时 P95、停滞时长 | `juhe_ai_jobs_task_*`；只在选中 jobs 时有数据 |
| 存储 | 库容量、表容量 Top 10、表行数 Top 10 | postgres_exporter 的 `juhe_postgres_*` |

### 5.3 导入与定制

1. Grafana → Dashboards → Import → 上传 JSON。
2. 选择数据源；变量默认值是名为 `Prometheus` 的数据源，名字不同则在顶部下拉框重选。
3. `DS_POSTGRES_EXPORTER` 类型仍是 **Prometheus**，不是 PostgreSQL 数据源；选错会让“存储”行为空。
4. 使用 `stack/` 时无需手工导入：`stack/dashboards/` 内的面板由 provisioning 自动加载（30s 扫描）。
5. 定制阈值、单位、颜色直接另存副本；仓库 JSON 是基线，不承载各部署方的定制。不要用 `metric_relabel_configs` 重命名指标或标签，否则面板与告警会一起失效。

### 5.4 空面板排查顺序

1. Prometheus Targets 两个应用 job 是否 `up=1`；`up=0` 先查隧道/端口。
2. 顶部 `job` 变量是否有值；没有值说明 `up{job=~"juhe-ai-(gateway|jobs)"}` 不存在。
3. 数据源是否选对；`DS_POSTGRES_EXPORTER` 必须是 Prometheus 类型。
4. 新部署至少等 3 个 60s 采样点再看。
5. “存储”行单独看：`curl <exporter>/metrics | grep '^juhe_postgres_'` 三族是否都有。
6. 仍为空时在 Explore 直接跑面板表达式，按报错定位标签、指标名或数据源。

## 6. postgres_exporter 查询

两种用法二选一：

- **新环境**：直接用 `collector/queries.yaml`（任务停摆、统计游标、账户状态、库表容量全部包含），由 `collector/docker-compose.yml` 挂载。
- **已有 exporter**：把 `postgres-exporter-queries-additive.yaml` 的五个顶层键**追加**到现有 `queries.yaml`，不覆盖既有查询；键冲突时以现网为准并停下确认。同时执行 `collector/sql/monitoring-role.sql`（只读角色）与 `collector/sql/query-indexes.sql`（任务停摆索引）。

| 顶层键 | 导出指标 | 含义 |
| --- | --- | --- |
| `juhe_app_job_stall` | `juhe_app_job_stall_minutes_since_success` | 任务距上次成功的分钟数 |
| `juhe_app_stats_cursor_lag` | `juhe_app_stats_cursor_lag_minutes_since_success` | 统计游标滞后分钟数 |
| `juhe_app_account_status` | `juhe_app_account_status_accounts` | 各状态账户数 |
| `juhe_postgres_db_size` | `juhe_postgres_db_size_bytes` | 各库磁盘大小 |
| `juhe_postgres_table` | `juhe_postgres_table_size_bytes`、`juhe_postgres_table_rows` | 表大小与估算行数 |

任务停摆查询依赖 `juhe_stats.background_task_runs(job_name, finished_at DESC)` 索引；缺失时会每 60s 全表扫描（历史 BUG-0313：2GB 表把磁盘打满）。`collector/sql/query-indexes.sql` 负责创建，`install.sh` 已包含该步骤。

命名规则：导出指标名 = 查询键名 + `_` + 列别名；键承担前缀，列别名补全后半段。两处 `juhe_postgres_*` 查询必须一致，改动后各跑一次 `curl <exporter>/metrics | grep '^juhe_postgres_'` 验证。

`n_live_tup` 是统计估算值，用于趋势观察，不用于精确对账；exporter 连接角色只需系统表与三张业务表的只读权限（见 `collector/sql/monitoring-role.sql`）。

## 7. 告警基线

`stack/prometheus/rules/juhe-ai-alerts.yml` 覆盖：

| 组 | 规则 |
| --- | --- |
| 抓取链路 | `AppScrapeDown`（`up == 0` / 5m）、`AppScrapeTargetMissing`（`absent(up)` / 5m） |
| 进程健康 | `AppGoroutinesHigh`、`AppGCPauseHigh`、`AppHeapFloorHigh`、`AppThreadsHigh`、`AppAuditPersistErrorRate` |
| 后台任务 | `AppJobStalled`（120m，排除 24h 级任务与已退役任务）、`AppStatsCursorStalled`（180m）、`AppAccountUnavailableSurge`（环比 +20） |
| 主机/容器 | `HostDown`、`HostDiskUsageHigh`、`HostMemoryHigh`、`AppContainerDown`、`AppContainerRestarted`、`AppContainerRestartLoop`、`AppContainerCpuHigh`、`AppContainerMemoryHigh`、`AppContainerOom` |
| 数据库 | `PostgresDown`、`PostgresDeadlocks`、`PostgresRollbacksHigh`、`RedisDown`、`RedisMemoryPressure`、`RedisSlowCommandsSurge` |
| 监控自身 | `Watchdog` |

两条容易踩的边界：

- `juhe_ai_jobs_task_last_success_timestamp_seconds` 在任务首次成功前不存在，且只覆盖“PostgreSQL + 经调度器注册”的任务；从未成功的任务要用 `absent(...)` 或 `juhe_ai_jobs_task_runs_total` 兜底。仓库基线用 postgres_exporter 的 `juhe_app_job_stall`，口径是全历史，不受此限，但必须维护 24h 任务与退役任务的排除清单。
- `AppJobStalled` 的排除清单里已有 `expired-deleted-account-cleanup`、`upstream-client-version-refresh`（24h 日频）与 `chat-retention-cleanup`（已退役）。新增 24h 任务或退役任务时必须同步，否则会每天约 22h 误报或永久误报。

落地流程：`promtool check rules <file>` → 同步到监控机 `prometheus/rules/` → `curl -X POST http://127.0.0.1:19091/-/reload` → 在 Prometheus/Grafana Alerting 页确认规则加载且无 pending 误报。是否接 Alertmanager、邮件、IM 通知由部署方决定。

## 8. 复用方环境适配

仓库交付的配置已脱敏，但**结构假设**来自参考环境；换环境时先确认这三项，否则会出现容器起不来或面板空：

| 项 | 默认值 | 何时需要改 |
| --- | --- | --- |
| 应用 compose 项目名 / 网络名 | `juhe-ai` / `juhe-ai_default` | 改过 compose 的 `name:` 时，同步改 `collector/docker-compose.yml` 的 `networks.app.name`（external 网络按名字找） |
| 日志 `host` 标签 | `juhe-ai-host` | `collector/alloy/config.alloy` 的 `host` replacement 与 `stack/dashboards/logs-overview.json` 的 `{host="juhe-ai-host"}` 必须一致；改名时两处同改 |
| 隧道本地端口 | `19107`–`19112` | `stack/prometheus/prometheus.yml` 的 targets 与 `ssh -L` 的本地端口必须一致；改了端口两处同改 |

另外，告警阈值取自参考环境基线（gateway ~100 / jobs ~110 goroutines、容器内存 2GB、Redis maxmemory 1gb、任务停摆 120 分钟）。复用方应先观察 1-2 天自己的基线，再调整阈值或 `for`，不要直接按参考值告警。

## 9. 集群 / 多实例形态（未来）

生产当前是**单机 Redis + 单机 PostgreSQL**，不启用集群；Redis Cluster 只是后续可选能力（部署与影响见[配置参考 §5](../config/README.md)）。对监控的影响：

- **应用指标端点不变**：gateway 仍是单进程 `3306`、jobs 仍是单进程 `3305`，Cluster 模式不改变路径或指标族。抓取配置与面板不需要改。
- **多实例**：未来 gateway/jobs 水平扩容时，每个实例暴露同样的 `job` 与不同 `instance`；保持 job 名不变，Prometheus 按 `instance` 区分，面板的 `sum by (route_group)` 会自然聚合。需要单实例视角时在 PromQL 加 `by (instance)`。
- **Redis Cluster**：应用面板不含 Redis 指标；Redis 监控需按 **每个 master 一个 target** 部署 `redis_exporter`，重点看每节点内存、evicted keys、connected clients、slowlog 与槽位归属。只抓一个 seed 不会覆盖整个集群。
- **PostgreSQL**：仍为单实例，postgres_exporter 与“存储”行不变。
- **采样预算**：集群化不改变 60s 抓取约定；节点变多时按“每节点一个 target”扩展，不要用缩短 interval 换灵敏度。
- **热点提示**：域级 hash tag 下 `keymodel`、`hotquality`、`circuit` 等热点域集中到固定 slot；集群模式要按节点观察内存与 CPU，不能只看集群总量。

## 10. 复用验收清单

1. 应用按[部署指南](../部署指南.md)完成单机部署，`docker compose ps` 全部 healthy。
2. 应用机宿主执行 §2.1 的 `curl`，两个 `/__aisys__/metrics` 都有输出。
3. `collector/` 容器全部 running，`queries.yaml` 的五个查询键全部有序列（`juhe_app_*` 与 `juhe_postgres_*`）。
4. 监控机 `stack/` 全部 running，Prometheus Targets 中 `juhe-ai-gateway` / `juhe-ai-jobs` 均 `up=1`。
5. Grafana `juhe-ai` 目录下五个面板可打开；应用总览“概览”显示两个 target，“网关流量”有曲线，“后台任务”有数据。
6. 发起一次真实 `/v1` 请求，`juhe_ai_http_requests_total` 增长，面板 1 分钟内可见。
7. 停掉 jobs 容器 5 分钟，`AppScrapeDown` 进入 pending/firing，恢复后转 inactive（验证告警链路，避开业务高峰）。
8. Loki 能看到 gateway/jobs 日志，`level=~"(?i)error"` 可过滤（启用日志时）。

## 11. 安全与运维边界

- 指标端口只绑回环；安全组不要放行 `9100/9187/9121/3305/3306`，Caddy 不要反代 `/__aisys__/metrics`。
- SSH 隧道用专用密钥/账号，监控机侧转发口只监听 `127.0.0.1`；隧道账号只需 SSH 权限。
- 面板 JSON 不含凭据；Grafana 数据源指向只读 Prometheus。postgres_exporter 用只读角色，禁止复用应用写账号。
- 采集端 `.env` 与监控栈 `.env` 都 600 权限、不入库；`MONITORING_PG_PASSWORD` 与 `REDIS_PASSWORD` 不要写进任何文档。
- Prometheus 数据保留 30 天、Loki 72 小时（`stack/` 默认值）；备份与卷清理按各环境规范执行。
- 面板与抓取配置变更后先在测试环境验证；告警规则变更按“`promtool check` → reload → 观察 10 分钟无 pending 误报”执行。

## 12. 关联文档

- [部署指南](../部署指南.md)：单机部署、升级、备份、排障与监控端口契约。
- [Prometheus 指标暴露设计](../../functions/Prometheus指标暴露设计.md)：指标族、标签基数、端点安全边界与已删除监控功能的映射。
- [`docker/single-server/README.md`](../../../docker/single-server/README.md)：compose / `.env` / 端口发布 / 健康检查的配置契约。
- [`docker/single-server/monitoring/README.md`](../../../docker/single-server/monitoring/README.md)：交付物总览与两条使用路径。
- [`collector/README.md`](../../../docker/single-server/monitoring/collector/README.md)：应用机采集端安装。
- [`stack/README.md`](../../../docker/single-server/monitoring/stack/README.md)：监控机栈安装。
