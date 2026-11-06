# Linux 直跑（systemd / 裸进程）差异

> 生产形态是 [部署指南](../部署指南.md) 的 Docker Compose；本页只说明**不用 Docker、直接在 Linux 上跑三个 Go 二进制**时的差异与等价操作。通用步骤（构建、配置、验证、升级、备份）以部署指南为准。

## 1. 适用与边界

- 适用：单机 systemd / 裸进程部署，自备 PostgreSQL 与 Redis。
- 不适用：Windows/macOS 直跑（发布包支持，但本文只写 Linux systemd 形态）。
- 不再支持 SQLite / 内存模式：`--dsn` 是 maintenance 的唯一存储形态，`--driver` / `--paths` 已删除。

## 2. 进程与目录

三个二进制：

| 二进制 | 角色 |
| --- | --- |
| `juhe-ai-gateway` | 管理面 + `/v1` 网关 |
| `juhe-ai-jobs` | 后台任务 / 探针 / 统计 / retention |
| `juhe-ai-maintenance` | 一次性 CLI（schema、seed、upgrade、迁移、核验） |

**cwd 契约（必须）**：gateway/jobs 的 `data/`、`logs/` 等相对路径以进程 cwd 为基准，用量记录经 `usage-record-spool` 目录在 gateway↔jobs 间交接。systemd 必须设置 `WorkingDirectory=` 到同一含 `data/` 的目录，或显式配置 `JUHE_AI_DATA_DIR` 绝对路径；两侧必须解析到同一路径，否则统计恒空且容器/进程重建即丢数据。

## 3. systemd 单元示例

`/etc/systemd/system/juhe-ai-gateway.service`：

```ini
[Unit]
Description=juhe-ai gateway
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=app
Group=app
WorkingDirectory=/opt/juhe-ai/run
EnvironmentFile=/opt/juhe-ai/.env
ExecStart=/opt/juhe-ai/bin/juhe-ai-gateway
Restart=always
RestartSec=3
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
```

`/etc/systemd/system/juhe-ai-jobs.service` 同理（`ExecStart=/opt/juhe-ai/bin/juhe-ai-jobs`）。两个单元必须使用同一个 `WorkingDirectory` / `JUHE_AI_DATA_DIR`。

首次启用：

```sh
groupadd --gid 1000 app 2>/dev/null || true
useradd --uid 1000 --gid 1000 --home /opt/juhe-ai/run --shell /usr/sbin/nologin app 2>/dev/null || true
mkdir -p /opt/juhe-ai/{bin,run/data,run/logs,backup}
chown -R 1000:1000 /opt/juhe-ai/run
systemctl daemon-reload
systemctl enable --now juhe-ai-gateway juhe-ai-jobs
```

> 若机器上 UID/GID 1000 已被占用，改成实际服务用户并在 `.env`/单元中统一；关键是 gateway/jobs 对 `data/` 的读写权限一致。

## 4. 配置

- `.env` 变量与 Docker 形态同源（见[配置参考](../config/README.md)）；区别是连接串指向**真实 PG/Redis 地址**而不是容器名，三个进程启动时自动设置 `PGAPPNAME`（慢 SQL 归因用），不需要也不应在 URL 内写 `application_name`。
- `JUHE_AI_HOST` / `JUHE_AI_PORT` 默认 `127.0.0.1:3000`；对外暴露需在前面放 Caddy/Nginx（见 [https](../https/README.md)）。
- `JUHE_AI_FRONTEND_DIST_PATH` 指向前端 `dist` 绝对路径（不配则不挂管理 SPA）。
- `JUHE_AI_LOG_DIR` 显式指向与 jobs 索引器相同的日志目录。
- 直跑形态没有内置 Caddy：HSTS 必须由前置反代（Caddy/Nginx）提供，等价 `Strict-Transport-Security: max-age=31536000`（不要贸然加 `includeSubDomains`）；compose 的 `read_only`/`cap_drop` 容器基线不适用，需用 systemd 沙箱或主机加固替代。
- 应用层请求体上限与 `ReadTimeout`（主监听默认 10 分钟、J3b 管理别名 5 分钟、健康监听 30 秒；`/v1/files`/`/files` 与 64MiB 图片/音频车道路由在监听层分别延长到 60/30 分钟）在直跑形态同样生效，不依赖 Docker。

## 5. 首次初始化（直跑）

```sh
cd /opt/juhe-ai/run
set -a && . ./.env && set +a

# 1) 全新库：upgrade 会执行 ensure-schema + seed + 全部迁移，并推进账本
/opt/juhe-ai/bin/juhe-ai-maintenance upgrade \
  --dsn "$JUHE_AI_POSTGRES_URL" --secret "$JUHE_AI_SECRET" \
  --stop-write-confirmed --backup-confirmed </dev/null

# 2) J3b 账户电路运行态索引基线（gateway 启动硬性要求 ready）
/opt/juhe-ai/bin/juhe-ai-gateway -init-account-circuit-runtime-index

# 3) 启动
systemctl start juhe-ai-gateway juhe-ai-jobs
```

已有库升级：

```sh
systemctl stop juhe-ai-gateway juhe-ai-jobs
# 备份并验证可恢复（pg_dump -Fc + pg_restore -l）
/opt/juhe-ai/bin/juhe-ai-maintenance upgrade \
  --dsn "$JUHE_AI_POSTGRES_URL" --secret "$JUHE_AI_SECRET" \
  --stop-write-confirmed --backup-confirmed </dev/null
systemctl start juhe-ai-gateway juhe-ai-jobs
```

## 6. 端口与防火墙

| 端口 | 用途 | 暴露范围 |
| --- | --- | --- |
| 3000 | gateway 业务面（`/v1` + 管理面） | 仅本机/反代可达 |
| 3306 | gateway 健康 + metrics + pprof | 仅回环 |
| 3305 | jobs 健康 + metrics | 仅回环 |

- 只放行 22/80/443（及 443/udp 若用 HTTP/3）。
- 不要把 3306/3305 或 pprof 暴露到公网或加入反代。
- 若 health 监听需要绑 `0.0.0.0`（跨机抓取），gateway 必须设 `JUHE_AI_GATEWAY_HEALTH_ALLOW_NON_LOOPBACK=true`，公网地址仍会被拒绝。

## 7. 升级门禁与回滚

- gateway/jobs 启动时只读比对库 revision；落后于二进制认识的值会 fail-fast 并提示 `upgrade`。
- 回滚二进制时若库 revision 高于旧二进制认识的值，启动门禁会拒绝：恢复升级前备份（当前 revision 17；本次清退与 accounts CHECK 均不可逆，不支持只下调 revision 后启动旧二进制）。
- 直跑没有 compose 的健康等待：升级失败保持两个 unit 停止，修复后重跑 `upgrade`（账本断点续跑）。

## 8. 日志与排障

- `journalctl -u juhe-ai-gateway -f` / `journalctl -u juhe-ai-jobs -f`。
- 文件日志在 `JUHE_AI_LOG_DIR`；索引与轮转清理由 jobs 负责。
- 统计恒 0：先查两进程 cwd / `JUHE_AI_DATA_DIR` 是否同源，再看 `usage-record-spool` 是否有文件、jobs 是否出现 `usage_record_spool_drain_unwired` 告警。
