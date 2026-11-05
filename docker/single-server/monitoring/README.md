# 监控产物（单机部署 × Prometheus/Grafana）

本目录承载 `juhe-ai` 单机部署的**全套监控交付物**：Grafana 面板、采集端配置、监控机栈配置、postgres_exporter 查询与告警基线。拿到同一套代码与 Docker Compose 环境的团队可以直接复制到服务器使用，不需要自己从头拼装。

完整的接入、复用、验收与集群差异说明见公共文档 [docs/deploy/monitoring/README.md](../../../docs/deploy/monitoring/README.md)。

## 目录结构

```text
monitoring/
├── collector/                      # 应用机采集端：复制到 /opt/juhe-monitoring/
│   ├── docker-compose.yml          # node/cadvisor/postgres/redis exporter + alloy
│   ├── install.sh                  # 一键安装（建角色/起容器/装 cron/自检）
│   ├── .env.example                # 采集端密码模板
│   ├── queries.yaml                # postgres_exporter 全量自定义查询
│   ├── sql/monitoring-role.sql     # PG 只读监控角色
│   ├── sql/query-indexes.sql       # 任务停摆查询依赖的索引
│   ├── redis-slowlog-snapshot.py   # Redis 慢命令增量快照（cron 5 分钟）
│   ├── stall-sampler.sh            # 主机 PSI + PG 活动语句采样（cron 1 分钟）
│   ├── cron/                       # 两个 /etc/cron.d 文件（慢日志、僵死采样）
│   ├── alloy/config.alloy          # 应用容器日志 → Loki
│   └── README.md                   # 采集端安装步骤
├── stack/                          # 监控机：复制到 /opt/juhe-monitoring-stack/
│   ├── dashboards/                 # Grafana 面板 JSON（全部无实例地址/密钥）
│   │   ├── juhe-ai-app-overview.json   # juhe-ai 应用总览（核心面板）
│   │   ├── host-overview.json          # 主机总览
│   │   ├── postgres-overview.json      # PostgreSQL 总览
│   │   ├── redis-overview.json         # Redis 总览
│   │   └── logs-overview.json          # 应用日志总览（Loki）
│   ├── docker-compose.yml          # Prometheus/Grafana/Loki/blackbox（Alertmanager 可选）
│   ├── install.sh                  # 一键安装（起容器/等就绪/自检/target 状态）
│   ├── .env.example                # Grafana 管理员密码模板
│   ├── prometheus/prometheus.yml   # 抓取配置
│   ├── prometheus/rules/           # 告警规则基线
│   ├── grafana/provisioning/       # 数据源与面板加载
│   ├── loki/loki-config.yaml       # 单机 Loki（72h）
│   ├── blackbox/blackbox.yml       # 入口/链路探测模块
│   └── README.md                   # 监控机安装步骤
└── postgres-exporter-queries-additive.yaml   # 已有 exporter 的「追加式」查询片段
```

## 两条使用路径

**A. 新环境完整复用（推荐）**：应用机部署 `collector/`，监控机部署 `stack/`，面板 JSON 复制到 `stack/dashboards/`。按两个目录各自的 README 执行即可。

**B. 已有监控栈、只接应用指标**：只在 Prometheus 增加两个 job（`juhe-ai-gateway` / `juhe-ai-jobs`，路径 `/__aisys__/metrics`，60s），再导入 `stack/dashboards/juhe-ai-app-overview.json`。postgres_exporter 的 `juhe_app_*` 查询与主机/日志面板按需追加。

## 应用指标端点与安全边界

| job（Prometheus 必须用此名） | 应用机抓取地址 | 端点 | compose 依据 |
| --- | --- | --- | --- |
| `juhe-ai-gateway` | `127.0.0.1:3306` | `GET /__aisys__/metrics` | gateway 服务 env `JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS=0.0.0.0:3306`（代码默认 `127.0.0.1:3306`）+ ports `"127.0.0.1:3306:3306"` |
| `juhe-ai-jobs` | `127.0.0.1:3305` | `GET /__aisys__/metrics`（同端口另提供 `/health`） | jobs 服务 env `JUHE_AI_JOBS_HEALTH_LISTEN_ADDRESS=0.0.0.0:3305` + ports `"127.0.0.1:3305:3305"` |

- 两个端口只发布到**宿主回环**：容器网络与公网均不可直接访问；**部署层必须拒绝公网入口、只允许受限 Prometheus 抓取**（经 SSH 隧道认证）。不得把 ports 改成 `0.0.0.0` 或加防火墙放行公网。
- compose healthcheck 不受影响：容器内探测仍走 `127.0.0.1`（`http://127.0.0.1:3306/health` / `http://127.0.0.1:3305/health`）。
- 部署后冒核（应用机宿主执行）：`curl -sf http://127.0.0.1:3306/__aisys__/metrics | head -n 5` 与 `curl -sf http://127.0.0.1:3305/__aisys__/metrics | head -n 5`。

## 面板导入

1. Grafana → Dashboards → Import → 上传 `stack/dashboards/juhe-ai-app-overview.json`。
2. 导入页若出现数据源选择提示则选择；没有提示时，导入后在面板顶部确认数据源变量：
   - **Prometheus 数据源**（`DS_PROMETHEUS`）：抓取 juhe-ai 两个 job 的 Prometheus 实例；
   - **postgres_exporter 数据源**（`DS_POSTGRES_EXPORTER`）：postgres_exporter 对应的 Prometheus 数据源（存储行走该 exporter 的指标）。变量默认值是名为 `Prometheus` 的数据源，名字不同则在顶部下拉框重选。
3. `job` 变量自动从 `up{job=~"juhe-ai-(gateway|jobs)"}` 发现，支持多选/All；「后台任务」行只在选中 `juhe-ai-jobs` 时有数据，「网关流量」行只在 `juhe-ai-gateway` 有数据。
4. 面板行：概览（up、进程运行时长）→ 网关流量（QPS/5xx 占比/时延 P95/在途/上游失败/首字延迟）→ Go Runtime 与主业务池 → 后台任务 → 存储（postgres_exporter）。

使用 `stack/` 时不需要手工导入：`stack/dashboards/` 已包含全部面板，Grafana provisioning 自动加载到 `juhe-ai` 目录（30s 扫描）。

## postgres_exporter 查询

两种用法，二选一：

- **新环境**：直接用 `collector/queries.yaml`（含任务停摆、统计游标、账户状态与库表容量全部查询），由 `collector/docker-compose.yml` 挂载。
- **已有 exporter**：把 `postgres-exporter-queries-additive.yaml` 的五个顶层键**追加**到现有 `queries.yaml`，不覆盖既有查询；键冲突时以现网为准并停下确认。追加后还需执行 `collector/sql/monitoring-role.sql`（只读角色）与 `collector/sql/query-indexes.sql`（任务停摆索引）。
- 命名规则：导出指标名 = 查询键名 + `_` + 列别名；键承担前缀、列别名补全后半段（例如 `juhe_postgres_db_size` + `bytes` → `juhe_postgres_db_size_bytes`）。
- 两个文件中的 `juhe_postgres_*` 查询必须保持一致；改动时同步修改并各跑一次 `curl <exporter>/metrics | grep '^juhe_postgres_'` 验证三族都在。

## 告警规则

`stack/prometheus/rules/juhe-ai-alerts.yml` 是完整基线，含抓取失联、goroutine/GC/堆/线程、审计持久化失败、任务停摆、统计游标、账户异常、容器与主机、PG/Redis 与慢命令。阈值按本项目生产基线设定，复用方应先按自己的机器规格与流量校准再上线。

规则变更流程：`promtool check rules <file>` → 同步到监控机 `prometheus/rules/` → `curl -X POST http://127.0.0.1:19091/-/reload` → 在 Prometheus/Grafana Alerting 页确认加载且无 pending 误报。
