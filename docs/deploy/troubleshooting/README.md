# 排障速查

> 先按“入口 → 进程 → 存储 → 业务链路”顺序定位，不要一上来改配置。命令默认在服务器 `/opt/juhe-ai` 执行；涉及容器名以 compose 为准。需要读取 `.env` 变量的命令先执行 `set -a && . ./.env && set +a`。

## 1. 入口与反向代理

| 症状 | 先查 | 处理 |
| --- | --- | --- |
| 公网 502 | `docker compose ps`、gateway 是否 healthy；Caddy 日志；Nginx `error_log` | 恢复 gateway；确认上游仍是 `172.18.0.10:3000`（Caddy）或 `127.0.0.1:3000`（宿主 Nginx）；不要把 upstream 改成容器名/h2c |
| 公网 504 / 流式请求中途断开 | 反代 `proxy_read_timeout`；网关请求预算 | `/v1/` 读超时 ≥ 600s；SSE 必须关闭缓冲；Nginx 见 [Nginx 部署](../nginx/README.md) |
| SSE 一次性返回或卡住 | `proxy_buffering`、`gzip`、Caddy `flush_interval` | `/v1/` 关闭缓冲与 gzip；Caddy 保留 `flush_interval -1` |
| WebSocket `/v1/realtime` 400/无法升级 | `Upgrade` / `Connection` 头 | Nginx 透传两 header；Caddy 默认支持 |
| 413 Request Entity Too Large | 反代 `client_max_body_size` | 设为 `64m`，不要小于网关上限（text 16MB / image+audio 64MB） |
| 所有请求来源 IP 相同 | `X-Forwarded-For`、`JUHE_AI_TRUST_PROXY` | 反代追加 XFF；gateway `TRUST_PROXY=true`；不要把 gateway 直接暴露公网并信任转发头 |
| 证书未生效/续期后仍旧证书 | `openssl s_client`、certbot 日志、reload hook | `certbot renew --dry-run`；续期后 reload Caddy/Nginx |
| `upstream prematurely closed connection` | 网关客户端交接（锁死/预算耗尽/临时隔离）先 200 再断开 | **预期行为**，不要按该日志告警；未提交响应的崩溃才由反代合成 502 |
| 80/443 被占用 | `ss -lntp \| grep -E ':(80\|443)\b'` | 同一时间只保留一个入口：Caddy 或 Nginx，不要双开 |

## 2. 容器与主机

| 症状 | 先查 | 处理 |
| --- | --- | --- |
| 有容器 unhealthy | `docker compose logs <service> --tail=200` | 按日志根因；gateway 首次启动等租约约 1 分钟属正常 |
| gateway 反复重启 | `docker compose logs gateway`；启动门禁提示 | schema 未升级走 `maintenance upgrade`；租约冲突看 owner lease；内存看 `docker stats` |
| `docker compose up` 报 `Pool overlaps` / 网段冲突 | `docker network ls`、`ip route` | 修改 compose `ipam.subnet`，并同步 Caddyfile/反代的固定 IP |
| 内存压力/整机卡死 | `free -h`、`docker stats`、GOMEMLIMIT/mem_limit | 保持 compose 双限；新机确认 `host-init.sh` 已执行 |
| 磁盘满 | `df -h`、`du -sh /opt/juhe-ai/data/*` | 清理过期 dump/日志；审计 blob 按保留策略处理；不要直接删 PG 数据目录 |
| Caddy 容器 exec 全败/不健康但流量正常 | caddy zombie/子进程 | `docker compose force-recreate caddy`（入口闪断数秒） |
| Docker 拉镜像超时 | `docker pull` 输出 | 配置镜像加速或预热基础镜像；不要在生产机上编译 |

## 3. PostgreSQL / Redis

| 症状 | 先查 | 处理 |
| --- | --- | --- |
| health 返回 503 | `curl 127.0.0.1:3306/health`、PG/Redis 连接 | 连接串、密码、PG/Redis healthy；看 gateway 日志的具体探针 |
| PG 连接满 | `SELECT count(*) FROM pg_stat_activity;` | 查连接池配置与长事务；不要盲目调大 `max_connections` |
| PG 死锁/回滚高 | PG 日志 `deadlock detected`；`pg_stat_database` | 定位语句与应用侧事务边界；临时处置见 [监控接入](../monitoring/README.md) 告警说明 |
| Redis 写入失败 | `redis-cli -a "$REDIS_PASSWORD" INFO memory` | 本项目 `maxmemory 1gb` + `noeviction`；接近上限会拒绝写入，需扩容或清理 |
| Redis 慢命令多 | `SLOWLOG GET`；`/opt/juhe-monitoring/redis-slowlog/slowlog.jsonl` | 关注 Lua 脚本与大 key；采集端见 [collector](../monitoring/README.md) |
| PG 磁盘增长快 | `juhe_stats.background_task_runs`、审计/用量表 | 检查 retention 任务是否运行；按保留策略清理，不要手工 truncate 业务表 |

## 4. 业务链路

| 症状 | 先查 | 处理 |
| --- | --- | --- |
| 统计/用量恒 0 | 两容器 `data` 挂载是否同源、`usage-record-spool` 是否存在 | 恢复 `working_dir: /app/backend` 与 `./data/app/data` 挂载；`bash verify-release.sh` |
| 新版启动提示「尚未升级/升级未完成」 | `maintenance upgrade --dry-run` | 走[部署指南 §9](../部署指南.md) 升级流程 |
| 文件日志不增长 | `JUHE_AI_LOG_DIR` 与 `data/app/logs` 挂载 | 修正挂载；`disabled` 字面量会关闭写侧 |
| 后台登录 401/跨域失败 | `.env` 的 `JUHE_AI_ALLOWED_ORIGINS`、`COOKIE_SECURE`、`TRUST_PROXY` | 改成实际访问 Origin；HTTPS 下 cookie secure；反代后信任代理 |
| `/v1` 401 | 是否带 `Authorization: Bearer <key>` | 正常鉴权行为；用后台创建的 Key 重试 |
| `/v1` 429/503 | 路由策略、分组、账户可用性、限流 | 管理面看 API Key 绑定与账户状态；查 gateway 日志的失败分类 |
| 上游超时/失败 | 出海代理、账户、上游状态 | 按 [proxy/](../proxy/README.md) 检查代理；管理面测试账户；看 `juhe_ai_gateway_upstream_failures_total` |
| 账户列表搜索异常 | 名称搜索派生层是否已按发布窗口退役 | 见[部署指南 §9.6](../部署指南.md)；旧表与新二进制并存会放大误判 |

## 5. 监控与诊断

```sh
# 容器资源
docker stats --no-stream

# 应用指标（仅宿主回环）
curl -fsS http://127.0.0.1:3306/__aisys__/metrics | head
curl -fsS http://127.0.0.1:3305/__aisys__/metrics | head

# pprof（仅 gateway，只读）
curl -fsS http://127.0.0.1:3306/debug/pprof/ | head

# 最近错误
docker compose logs --since 30m gateway jobs 2>&1 | grep -E 'ERROR|panic|WARN' | tail -50
```

监控栈自身异常（target down、面板空、告警不触发）见 [监控接入](../monitoring/README.md) §4.5 与 §9。

## 6. 仍无法定位时

- 保留现场：`docker compose ps`、相关容器 `logs`、`df -h`、`free -h`、`docker stats`、最近一次发布记录。
- 不要在生产上试改 schema、手工删数据目录或重装 Docker；先备份并确认回滚路径。
- 升级窗口内的问题按[部署指南 §9](../部署指南.md) 的停止/重跑/恢复顺序处理，不要跳过停写直接重启旧二进制。
