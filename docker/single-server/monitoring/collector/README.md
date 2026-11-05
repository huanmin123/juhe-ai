# 应用机采集端（juhe-ai 单机 Docker）

本目录是应用机（跑 `juhe-ai` compose 的那台机器）的监控采集配置，直接复制到服务器 `/opt/juhe-monitoring/` 即可使用。所有监听只绑 `127.0.0.1`，由监控机经 SSH 隧道拉取。

## 文件清单

| 文件 | 服务器位置 | 作用 |
| --- | --- | --- |
| `docker-compose.yml` | `/opt/juhe-monitoring/docker-compose.yml` | node_exporter / cadvisor / postgres_exporter / redis_exporter / alloy |
| `install.sh` | 执行一次 | 幂等安装：建目录/角色、起容器、装 cron、自检六个端点 |
| `.env.example` | `/opt/juhe-monitoring/.env` | 采集栈密码（PG 只读角色、Redis） |
| `queries.yaml` | `/opt/juhe-monitoring/queries.yaml` | postgres_exporter 自定义查询（任务停摆/游标/账户状态/库表容量） |
| `sql/monitoring-role.sql` | 执行一次 | 创建 PG 只读监控角色 `juhe_monitor` 并授权 |
| `sql/query-indexes.sql` | 执行一次 | 建 `idx_bg_task_runs_job_finished`；缺失时任务停摆查询会全表扫描 |
| `redis-slowlog-snapshot.py` | `/opt/juhe-monitoring/redis-slowlog-snapshot.py` | 每 5 分钟增量导出 Redis SLOWLOG + textfile 指标 |
| `stall-sampler.sh` | `/opt/juhe-monitoring/stall-sampler.sh` | 每分钟采样主机 load/PSI 与 PG 活动语句（僵死取证） |
| `cron/juhe-slowlog-snapshot` | `/etc/cron.d/juhe-slowlog-snapshot` | 慢日志快照定时任务 |
| `cron/juhe-stall-sampler` | `/etc/cron.d/juhe-stall-sampler` | 僵死采样定时任务 |
| `alloy/config.alloy` | `/opt/juhe-monitoring/alloy/config.alloy` | 应用容器日志 → 监控机 Loki |

## 安装步骤（应用机，root）

前置：应用 compose 已运行（`/opt/juhe-ai`，项目名 `juhe-ai`，网络 `juhe-ai_default`），Docker 可用。

```sh
# 1) 上传本目录到应用机 /opt/juhe-monitoring（保持相对结构）
cd /opt/juhe-monitoring
cp .env.example .env && chmod 600 .env
# 填写 .env 的 MONITORING_PG_PASSWORD 与 REDIS_PASSWORD

# 2) 一键安装（幂等）：建目录 → 建 PG 只读角色 → 起容器 → 装 cron → 自检
sh install.sh
```

脚本会自动用 `MONITORING_PG_PASSWORD` 创建 `juhe_monitor` 角色（密码通过 psql 变量传入，
`sql/monitoring-role.sql` 里没有明文密码），用应用账号补建监控查询索引，并逐个 `curl` 六个端点。

手动核对（全部应有输出）：

```sh
curl -fsS http://127.0.0.1:9100/metrics | head -n 2
curl -fsS http://127.0.0.1:18081/metrics | head -n 2
curl -fsS http://127.0.0.1:9187/metrics | grep -c '^juhe_postgres_'
curl -fsS http://127.0.0.1:9121/metrics | head -n 2
curl -fsS http://127.0.0.1:3306/__aisys__/metrics | head -n 2   # gateway
curl -fsS http://127.0.0.1:3305/__aisys__/metrics | head -n 2   # jobs

# 手动跑一次定时脚本，确认产物
./redis-slowlog-snapshot.py
./stall-sampler.sh
ls -l ./textfile/
```

## 暴露端口（全部仅回环）

| 端点 | 端口 | 消费者 |
| --- | --- | --- |
| node_exporter | `127.0.0.1:9100` | 监控机 Prometheus（隧道） |
| cadvisor | `127.0.0.1:18081` | 监控机 Prometheus（隧道） |
| postgres_exporter | `127.0.0.1:9187` | 监控机 Prometheus（隧道） |
| redis_exporter | `127.0.0.1:9121` | 监控机 Prometheus（隧道） |
| gateway metrics | `127.0.0.1:3306` | 监控机 Prometheus（隧道） |
| jobs metrics | `127.0.0.1:3305` | 监控机 Prometheus（隧道） |
| alloy UI | `127.0.0.1:12346` | 本机排障 |

不要把这些端口发布到 `0.0.0.0`，也不要在 Caddy 里反代。

## 与应用 compose 的关系

- postgres/redis exporter 通过外部网络 `juhe-ai_default` 用容器名连接 `juhe-ai-postgres` / `juhe-ai-redis`；应用 compose 未启动时这两个容器会启动失败，属预期。
- `docker-compose.yml` 的 `name: juhe-ai-monitoring` 与本目录分离，不会覆盖应用 compose 项目。
- 应用容器名变更（`juhe-ai-go-gateway` / `juhe-ai-go-jobs` / `juhe-ai-postgres` / `juhe-ai-redis`）时，同步修改本目录 compose 与 alloy 配置。

## 慢日志与僵死采样的产物

- `redis-slowlog/slowlog.jsonl`：每行一条 >10ms 命令；超过 10MB gzip 轮转，保留 14 天。
- `redis-slowlog/cron.log`：定时任务输出。
- `stall-sampler/host.log`：每分钟主机 load/PSI/内存。
- `stall-sampler/pg-activity.log`：每分钟运行超过 2s 的 PG 语句与阻塞关系；超 40MB 截尾。
- `textfile/*.prom`：node_exporter textfile 指标（`juhe_redis_slowlog_*`、`juhe_host_psi_*`），告警数据源。
