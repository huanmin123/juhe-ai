# 监控机栈安装（Prometheus + Grafana + Loki + blackbox）

前置：本目录已上传到监控机 `/opt/juhe-monitoring-stack/`；应用机采集端已按 `../collector/README.md` 部署；SSH 隧道已建立（或 `prometheus/prometheus.yml` 的 targets 已改成实际可达地址）。

## 一键安装

```sh
cd /opt/juhe-monitoring-stack
cp .env.example .env && chmod 600 .env   # 填写 GRAFANA_ADMIN_PASSWORD
sh install.sh
```

`install.sh` 幂等：启动 compose → 等 Prometheus/Loki 就绪 → 自检四个服务 → 查询并打印 `juhe-ai-gateway` / `juhe-ai-jobs` 两个 target 的 `up` 值。脚本不改防火墙、不开放公网端口。

## 验证

```sh
curl -fsS http://127.0.0.1:19091/-/ready
curl -fsS http://127.0.0.1:13100/ready
curl -fsS http://127.0.0.1:3000/api/health
curl -fsS 'http://127.0.0.1:19091/api/v1/query?query=up%7Bjob%3D~%22juhe-ai-%28gateway%7Cjobs%29%22%7D'
```

Grafana 打开 `http://127.0.0.1:3000`（或 `.env` 的 `GRAFANA_ROOT_URL`），Dashboards → `juhe-ai` 目录可看到五个面板。

## SSH 隧道（监控机 → 应用机）

六条本地转发对应采集端六个端口：

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

日志走反向隧道（在监控机执行，把本机 Loki 暴露到应用机回环）：

```sh
ssh -N -R 127.0.0.1:13100:127.0.0.1:13100 <user>@<app-host>
```

长期运行用 autossh + systemd 托管，配置 `ServerAliveInterval` 与 `ExitOnForwardFailure=yes`。隧道未建立时 target 为 `down`，属预期。

## 关键约定

- 应用 job 名固定 `juhe-ai-gateway` / `juhe-ai-jobs`，抓取路径 `/__aisys__/metrics`，间隔 60s；改名会让面板与告警整块失效。
- `prometheus/rules/juhe-ai-alerts.yml` 是基线，阈值按自己的基线校准；改后用 `promtool check rules` 校验，再 `curl -X POST http://127.0.0.1:19091/-/reload`。
- Prometheus 保留 30 天、Loki 72 小时；数据在 named volume，`docker compose down -v` 会清空。
- 需要主动通知时启用 compose 中被注释的 Alertmanager 服务，补 `alertmanager/alertmanager.yml`，并在 `prometheus/prometheus.yml` 打开 `alerting` 段。
- 生产当前是单机 Redis，未启用 Cluster；集群形态下每个 master 单独一个 redis_exporter target。
