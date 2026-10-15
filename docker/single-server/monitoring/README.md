# 监控产物（单机部署 × infra 机 Prometheus/Grafana）

本目录承载应用侧 Prometheus 指标的部署配套产物：Grafana 面板、postgres_exporter 增补查询与接入说明。infra 机（192.168.1.203）已有 Prometheus / Grafana / Alertmanager / Loki，经既有 SSH 隧道抓取生产机宿主回环（127.0.0.1）上的端点，采样预算 60s。

背景：自维护「系统指标统计」与「表监控」功能已整体下线（DB 快照采样 + 管理页），运行时可观测改由应用暴露 Prometheus 指标承接。

## 指标来源与暴露边界

| job（Prometheus 必须用此名） | 生产机抓取地址 | 端点 | compose 依据 |
| --- | --- | --- | --- |
| `juhe-ai-gateway` | `127.0.0.1:3306` | `GET /__aisys__/metrics` | gateway 服务 env `JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS=0.0.0.0:3306`（代码默认 `127.0.0.1:3306`）+ ports `"127.0.0.1:3306:3306"` |
| `juhe-ai-jobs` | `127.0.0.1:3305` | jobs 健康监听（同端口提供健康与指标） | jobs 服务 env `JUHE_AI_JOBS_HEALTH_LISTEN_ADDRESS=0.0.0.0:3305`（既有）+ ports `"127.0.0.1:3305:3305"` |

- 两个端口只发布到**宿主回环**：容器网络与公网均不可直接访问；**部署层必须拒绝公网入口、只允许受限 Prometheus 抓取**（经 SSH 隧道认证）。不得把 ports 改成 `0.0.0.0` 或加防火墙放行公网。
- compose healthcheck 不受影响：容器内探测仍走 `127.0.0.1`（`http://127.0.0.1:3306/health` / `http://127.0.0.1:3305/health`）。
- 部署后冒核（生产机宿主执行）：`curl -sf http://127.0.0.1:3306/__aisys__/metrics | head -n 5` 与 `curl -sf http://127.0.0.1:3305/health`。

## SSH 隧道（需新增两条转发）

infra 机到生产机的既有 SSH 隧道（autossh/systemd 等承载形式，现状见 infra 侧既有 exporter 转发）需**追加两条本地转发**，把 infra 机本机端口转发到生产机 `127.0.0.1:3306` 与 `127.0.0.1:3305`：

```text
# 语义示意（具体 infra 侧监听端口沿用既有转发编号约定，避免与现有
# node-exporter / postgres-exporter 转发冲突；<infra-port-A/B> 由维护者定值）
<infra-port-A> -> 生产机 127.0.0.1:3306   # gateway metrics
<infra-port-B> -> 生产机 127.0.0.1:3305   # jobs metrics
```

转发生效后，先在 infra 机本机 `curl` 验证两个转发口有输出，再改 Prometheus。

## Prometheus 抓取配置片段

追加到 infra 机 Prometheus 配置（`job_name` 必须与下表一致，面板 job 变量按 `juhe-ai-(gateway|jobs)` 过滤）：

```yaml
scrape_configs:
  - job_name: juhe-ai-gateway
    scrape_interval: 60s
    metrics_path: /__aisys__/metrics   # 非默认路径，必须显式指定
    static_configs:
      - targets: ["127.0.0.1:<infra-port-A>"]   # 隧道转发 -> 生产机 127.0.0.1:3306
  - job_name: juhe-ai-jobs
    scrape_interval: 60s
    # jobs 3305 端口同时承载健康与指标；若实现侧指标不在默认 /metrics，
    # 在此补一行 metrics_path（以 `curl 127.0.0.1:3305/<路径>` 实测为准）。
    static_configs:
      - targets: ["127.0.0.1:<infra-port-B>"]   # 隧道转发 -> 生产机 127.0.0.1:3305
```

60s 采样预算与面板查询窗口匹配：全部 rate/直方图查询用 `[5m]` 窗口（≥5 个采样点），面板默认刷新 `1m`。不要把 scrape_interval 调得更密（隧道与生产机预算按 60s 评估）。

## Grafana 面板导入

1. Grafana（infra 机）→ Dashboards → Import → 上传或粘贴本目录 `juhe-ai-app-overview.json`。
2. 导入页选择两个数据源变量：
   - **Prometheus 数据源**（`DS_PROMETHEUS`）：infra 机 Prometheus（抓取 juhe-ai 两个 job 的实例）；
   - **postgres_exporter 数据源**（`DS_POSTGRES_EXPORTER`）：postgres_exporter 对应的 Prometheus 数据源（存储行走该 exporter 的指标）。
3. `job` 变量自动从 `up{job=~"juhe-ai-(gateway|jobs)"}` 发现，支持多选/All（All = 两个 job 一起看）；「后台任务」行只在选中 `juhe-ai-jobs` 时有数据，「网关流量」行只在 `juhe-ai-gateway` 有数据。
4. 面板行：概览（up、进程运行时长）→ 网关流量（QPS/5xx 占比/时延 P95/在途/上游失败/首字延迟）→ Go Runtime → 后台任务 → 存储（postgres_exporter）。

注意两个 label 取值以应用实际枚举为准，导入后如为空按实现枚举微调 PromQL（面板描述里已标注）：网关 `status_class="5xx"`、任务 `status="failed"`。

## postgres_exporter 增补查询合并

目标文件：生产机 `/opt/juhe-monitoring/queries.yaml`（postgres_exporter 经 `--extend.query-path` 加载）。

- 把本目录 `postgres-exporter-queries-additive.yaml` 的三个顶层键（`juhe_postgres_db_size_bytes` / `juhe_postgres_table_size_bytes` / `juhe_postgres_table_rows`）**追加**进该文件顶层映射。
- **增补不覆盖**：不得删除或改写现有 `juhe_app_*` 等任何既有查询；键冲突时以现网为准并停下确认。
- 合并后重载/重启 postgres_exporter，用 `curl 127.0.0.1:<exporter 端口>/metrics | grep juhe_postgres_` 验证三个指标出现。

## 告警规则迁移建议（不自动执行）

现有告警（维护侧规则文件，含 `AppGoroutinesHigh` / `AppJobStalled` / `AppStatsCursorStalled`）基于自维护系统指标统计/表监控的 DB 快照，随功能下线数据源消失。迁移建议：

| 现告警 | 建议 |
| --- | --- |
| `AppGoroutinesHigh` | 改用 `go_goroutines{job="juhe-ai-gateway"} > <阈值>`；阈值先用观察期数据（面板「Go Runtime」行）重定，不直接沿用旧口径数值 |
| 任务停滞（新增） | `time() - juhe_ai_jobs_task_last_success_timestamp_seconds{job="juhe-ai-jobs"} > <阈值>`；阈值按任务周期定（如 `max(3×周期, 1h)`），配 `for: 10m` 防抖 |
| 失联（新增） | `up{job=~"juhe-ai-(gateway|jobs)"} == 0`，配 `for: 5m`（60s 采样下 5 个点确认，兼顾隧道抖动不误报） |
| `AppJobStalled` / `AppStatsCursorStalled` | **口径核查后再迁移**：先核对现规则表达式与其消费的快照表（`*_storage_snapshots` / 统计游标）是否已无数据，再逐条映射到新指标（任务停滞用上式；统计游标类按新指标体系重新裁决口径，无对应指标时先降级为人工巡检并记录）。 |

本目录只提供建议；**不修改维护侧现有告警规则文件**（`F:\服务部署` 侧由维护者按上表另行迁移）。迁移完成前，旧告警将因数据源下线而失效/静默，属预期，需在迁移窗口内完成切换。
