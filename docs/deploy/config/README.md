# 配置参考（`.env` / Redis Cluster / Caddyfile）

> 本文是部署时的配置参考，配合 [部署指南](../部署指南.md) 使用；配置契约的权威来源仍是 [`docker/single-server/README.md`](../../../docker/single-server/README.md)。生产实例的密钥、域名、IP 只在 `.local/project-resources/prod/` 留档，本文全部使用占位符。
>
> 修改任何变量前先确认它属于“应用运行时契约”还是“compose 发布变量”；前者以 `.env.example` 与 single-server README 为准，后者见本文 §1。

## 1. `.env` 必填项

`.env` 由 compose `env_file` 注入 gateway/jobs/maintenance，同时提供 compose 变量插值。文件权限 `600`，只放在服务器与本地发布机的 `docker/single-server/.env`（gitignore），不要提交。

缺失会在 `docker compose config` 或进程启动时失败：

| 变量 | 说明 |
| --- | --- |
| `POSTGRES_PASSWORD` | PG `juhe_ai` 用户密码；进程启动时映射为 `PGPASSWORD`，连接串可不再重复写密码 |
| `REDIS_PASSWORD` | Redis 密码；客户端自动注入，连接串可不再重复写密码 |
| `JUHE_AI_POSTGRES_URL` | `postgres://juhe_ai@postgres:5432/juhe_ai`；gateway/jobs/maintenance 及全部子系统共用这一个主库连接串 |
| `JUHE_AI_REDIS_URL` | 单节点 `redis://redis:6379`；Cluster 用 `redis://<node1>:6379?addr=<node2>:6379&addr=<node3>:6379` |
| `JUHE_AI_REDIS_MODE` | `single`（缺省）或 `cluster` |
| `JUHE_AI_REDIS_NAMESPACE` | 键空间根，生产固定 `prod`，必填非空 |
| `JUHE_AI_SECRET` | ≥32 位强随机；凭据/内建 Key 加密封套，有数据后不可更换 |
| `JUHE_AI_RUNTIME_LOG_POSTGRES_URL` | 运行日志索引连接串；**必须显式，不回退主库**（保留独立库隔离能力） |
| `JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL` | jobs 的 J1 账户健康 PG 输入；**必须显式，不回退主库** |
| `JUHE_AI_ALLOWED_ORIGINS` | 后台访问 Origin，逗号分隔，如 `https://<domain>`；生产环境为空或 `*` 都会启动失败 |
| `JUHE_AI_TRUST_PROXY` | `true`；缺失时默认 false，所有请求的来源 IP 都会变成反代容器地址，来源限流/封禁/调度避让全部失真 |
| `JUHE_AI_PROXY_IP_A` / `JUHE_AI_PROXY_IP_B` | 旧出海隧道域名的 IP 钉死；compose `extra_hosts` 的 `:?required` 引用（「域名 → IP 列表」映射形式，A/B 可相同），**过渡保留**，生产 `proxy_profiles.host` 核对完成后再删除 |

## 2. 建议显式配置

| 变量 | 说明 |
| --- | --- |
| `JUHE_AI_COOKIE_SECURE` | 生产默认即为 `true`；显式写出便于审计，也避免日后直跑时遗漏 |
| `JUHE_AI_ACCOUNT_BALANCE_JOBS_HTTP_SECRET` | J2 手动触发端点密钥，≥32 字符；`JUHE_AI_SECRET` 已 ≥32 时可省略 |
| `JUHE_AI_LOG_MAX_FILE_MB` / `JUHE_AI_LOG_MAX_FILES` / `JUHE_AI_LOG_RETENTION_DAYS` | 文件日志轮转（默认 100MB / 500 个 / 30 天） |
| `JUHE_AI_LOG_INSTANCE_ID` | 可选；gateway 运行日志文件名实例段（`juhe-ai.<id>.log`）与 `build_info.instance_id`，默认 hostname，多 gateway 共享日志目录时必须唯一 |
| `JUHE_AI_AUDIT_LOG_SUCCESS_HOT_RETENTION_HOURS` | 成功正文热保留窗口（默认 1 小时）；成功正文长期保留 3 天且恒全量采样 |

## 3. 语义与边界

- **一个 Redis 就够**：cache/state/J3b/限流/会话等域通过 `juhe-ai:{<namespace>:<域>}:...` 键前缀隔离，不再需要多逻辑库。单节点只支持 DB0；配置 DB1/DB2 会启动失败。
- **Cluster 已支持**：`JUHE_AI_REDIS_MODE=cluster` + `JUHE_AI_REDIS_URL` 用重复 `addr` 参数列节点。Cluster 只支持 DB0；每个域一个 hash tag，域内多键 Lua/批量读同 slot，域间分布到不同 slot。部署与迁移见 §5。
- **已知容量边界**：`circuit`（账户电路）、`keymodel`（Key 模型运行态）、`hotquality`（短窗口热质量）三个热点域内部有全局索引/多键原子脚本，只能落单个 slot，无法再拆；其他域按域分布。热点域压力靠垂直扩容，不是集群能解决的。
- PG 子系统 URL 已收口：审计/操作日志/J3b/J2/J3a/账户健康 store 等不再有独立连接串，统一主库。需要独立库隔离的只有 `RUNTIME_LOG` 与 `ACCOUNT_HEALTH_INPUT`，两者必须显式配置且不回退。
- `application_name` 不再写进连接串：三个进程启动时由 `pgcfg.PrepareProcessEnv` 设置 `PGAPPNAME`，慢 SQL 与 `pg_stat_activity` 仍可区分来源。
- maintenance 的 `--apply-j3a-proxy-latency-postgres` / `--apply-j3b-model-check-postgres` / `--apply-account-balance-postgres` / `--postgres-schema-snapshot` 全部改用 `--dsn "$JUHE_AI_POSTGRES_URL"`，不再读取 `JUHE_AI_MAINTENANCE_*` / `JUHE_AI_SCHEMA_SNAPSHOT_POSTGRES_URL`。
- SQLite/内存模式已退役：`JUHE_AI_RUNTIME_MODE`、`JUHE_AI_DATABASE_DRIVER`、`JUHE_AI_CACHE_DRIVER`、`JUHE_AI_RUNTIME_STATE_DRIVER`、全部 `*_DATABASE_PATH`、`JUHE_AI_USAGE_SHARD_*`、`JUHE_AI_CODEX_CONTEXT_STATE_SHARD_*`、`*_STORE` 等都不要再写；残留只会产生未知变量 WARN。
- 本次硬切删除的配置（不得回填）：`JUHE_AI_REDIS_CACHE_URL`、`JUHE_AI_REDIS_STATE_URL`、`JUHE_AI_J3B_CIRCUIT_REDIS_URL`、`JUHE_AI_J3B_CIRCUIT_REDIS_NAMESPACE`、`JUHE_AI_BUSINESS_POSTGRES_URL`、`JUHE_AI_AUDIT_LOG_POSTGRES_URL`、`JUHE_AI_OPERATION_LOG_POSTGRES_URL`、`JUHE_AI_J3B_POSTGRES_URL`、`JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL`、`JUHE_AI_ACCOUNT_BALANCE_POSTGRES_URL`、`JUHE_AI_PROXY_LATENCY_POSTGRES_URL`、`JUHE_AI_MAINTENANCE_*_POSTGRES_URL`、`JUHE_AI_SCHEMA_SNAPSHOT_POSTGRES_URL`、`JUHE_AI_ACCOUNT_HEALTH_JOBS_OUTCOME_POSTGRES_URL`、`JUHE_AI_USAGE_SPOOL_DIR`（旧名）、`JUHE_AI_COOKIE_SAME_SITE`、`JUHE_AI_BUSINESS_OWNER`、`JUHE_AI_PROCESS_ROLE`、`JUHE_AI_J3B_OWNER`、`JUHE_AI_MODEL_CHECK_ENABLED`、`JUHE_AI_ACCOUNT_BALANCE_STORE`、`JUHE_AI_PROXY_LATENCY_STORE`、`JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE`、`JUHE_AI_GATEWAY_TIMING_DETAIL_SAMPLE_PERMILLE`、`JUHE_AI_AUDIT_LOG_SUCCESS_SAMPLE_RATE`。
- 系统指标/表监控已下线：`JUHE_AI_GO_RUNTIME_METRICS_*`、`JUHE_AI_TABLE_MONITOR_*`、gateway 侧 `JUHE_AI_JOBS_HEALTH_LISTEN_ADDRESS` 不要再写。
- AI 问答已退役：`JUHE_AI_CHAT_*` 不要再写。

## 4. `.env` 模板

把 `<...>` 替换为真实值；完整模板见 [`docker/single-server/.env.example`](../../../docker/single-server/.env.example)。

```sh
# ---- compose / deploy ----
POSTGRES_PASSWORD=<PG强密码>
REDIS_PASSWORD=<Redis强密码>
# deploy.sh 发布时注入本次版本，成功后把 latest-<role> 指向同一镜像；回滚时改为目标版本
JUHE_AI_IMAGE_TAG=latest
# 旧隧道域名 IP 钉死（compose :?required 引用；删除 extra_hosts 前必须填写）
JUHE_AI_PROXY_IP_A=<代理IP或服务器IP>
JUHE_AI_PROXY_IP_B=<代理IP或服务器IP>

# ---- 应用 ----
JUHE_AI_SECRET=<至少32位随机串>
JUHE_AI_POSTGRES_URL=postgres://juhe_ai@postgres:5432/juhe_ai
JUHE_AI_RUNTIME_LOG_POSTGRES_URL=postgres://juhe_ai@postgres:5432/juhe_ai
JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL=postgres://juhe_ai@postgres:5432/juhe_ai
JUHE_AI_REDIS_URL=redis://redis:6379
JUHE_AI_REDIS_MODE=single
JUHE_AI_REDIS_NAMESPACE=prod
JUHE_AI_ALLOWED_ORIGINS=https://<domain>
JUHE_AI_COOKIE_SECURE=true
JUHE_AI_TRUST_PROXY=true
JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY=/app/backend/data/audit-payload-blobs
```

> `NODE_ENV=production` 与 `TZ=Asia/Shanghai` 由 compose 为三个服务注入，不需要写进 `.env`。
>
> 密码建议用字母数字（`openssl rand -hex 32`）；`.env` 值里不要出现 `$`，compose 会按变量插值解析。密码必须含 `@` `:` `/` `#` 等字符时，写进 URL 需 percent-encode；只写 `POSTGRES_PASSWORD`/`REDIS_PASSWORD` 则无需转义。

配置自检（发版前）：

```sh
cd /opt/juhe-ai
docker compose --profile tool run --rm maintenance check-config --process all
```

`.env` 已由 compose 注入容器，容器内没有 `.env` 文件，不要传 `--env-file`。

## 5. Redis Cluster 部署与迁移

### 5.1 部署要点

- 三个（或更多）master，建议 3 master + 3 replica；客户端经 `JUHE_AI_REDIS_URL` 的重复 `addr` 参数发现拓扑，故障转移由 Redis Cluster 负责。
- `JUHE_AI_REDIS_MODE=cluster`，URL 不能带 DB 路径（只支持 DB0）。
- 键形态固定 `juhe-ai:{<namespace>:<域>}:<key>`；同一域内多键操作同 slot。域清单：`circuit`、`j3b`、`keymodel`、`hotquality`、`clientip`、`session`、`runtimecache`、`ratelimit`、`callertier`、`quota`、`obs`、`routestate`、`proxyhealth`、`inval`、`realtime`、`speedfirst`、`oauth`、`authstate`、`settings`。
- 跨进程同键逐域对齐：延迟降级/恢复探针用 `proxyhealth`（jobs 不再读 `speedfirst`），policy avoidance 用 `keymodel`；详见《PostgreSQL与Redis高性能模式设计》键布局规则。
- 不支持把同一份数据同时跑在 single 与 cluster 之间热切：键布局与 DB0 假设不同，必须停写迁移。

### 5.2 从旧三 URL 配置迁移（一次性停写窗口）

1. 停 gateway/jobs；备份 PG 并确认可恢复。
2. 核对代理档案 `proxy_profiles.host` 是否引用旧隧道域名（只读）。
3. 清空旧 Redis（管理员会话、限流窗口、client-ip、会话亲和、Key 模型瞬态、电路运行态都会清零）：single 用 `FLUSHALL`；cluster 必须用 `redis-cli --cluster call <seed>:<port> FLUSHALL` 对每个 master 执行（单节点 FLUSHALL 只清当前节点），并逐个复核 `DBSIZE=0`。
4. 按新契约改 `.env`（删除 3 个 Redis URL、J3b namespace 与全部已删 PG URL）。
5. 发布新二进制；`docker compose --profile tool run --rm maintenance --ensure-schema --dsn "$JUHE_AI_POSTGRES_URL"`（本次无 schema 变更）。
6. 重建电路索引（gateway 一次性 flag）：`docker compose run --rm --entrypoint /app/backend/juhe-ai-gateway gateway -init-account-circuit-runtime-index`；由 PG 中的权威派发版本回填 Redis 索引，完成后进程自行退出。
7. 启动 gateway/jobs，验证 `/health`、`/v1` 流式请求与 `usage_records` 增长。

回滚只能使用旧二进制 + 旧配置并重新初始化旧键空间；不存在单/集群双读兼容路径。

本地/联调验证可用 Docker 起三主集群（无密码，仅供测试）：

```sh
for p in 7001 7002 7003; do
  docker run -d --name juhe-ai-redis-cluster-$p -p $p:$p -p 1$p:1$p \
    redis:7.4-alpine redis-server --port $p --cluster-enabled yes \
    --cluster-config-file nodes.conf --cluster-node-timeout 5000 \
    --cluster-announce-ip <宿主机IP> --cluster-announce-port $p --cluster-announce-bus-port 1$p \
    --appendonly no --save "" --dir /data
done
docker exec juhe-ai-redis-cluster-7001 redis-cli --cluster create <宿主机IP>:7001 <宿主机IP>:7002 <宿主机IP>:7003 --cluster-yes
```

真实集群回归：`JUHE_AI_TEST_REDIS_CLUSTER_ADDRS=<host>:7001,<host>:7002,<host>:7003 go test ./rediscfg -run TestRealClusterRoundTrip`；节点启用密码时另设 `JUHE_AI_TEST_REDIS_CLUSTER_PASSWORD`（显式 opt-in，未设置则 SKIP；SKIP 不作验证证据）。

### 5.3 Cluster 模式对业务的实际影响（2026-11 dev 实测）

功能面：

- 所有键都带 `{<namespace>:<域>}` hash tag，域内多键 Lua/MGET/SCAN 同 slot；dev 用 3 master + 3 replica、`requirepass` 集群跑通 gateway/jobs 全链路、管理面接口与 `/v1` SSE。
- 单节点与集群的客户端开销基本一致（本机到 dev 的 SET+GET 基准：single 4.7–5.0ms/op，cluster 4.8–4.9ms/op，差异在噪声内）；随机键吞吐集群可随 master 数横向扩展（dev 3 master `redis-benchmark SET`：43.6k → 79.7k ops/s）。
- **热点域不随节点数扩展**：`circuit`、`keymodel`、`hotquality` 必须单 slot，吞吐/内存上限就是单个 master。按生产 namespace `prod` + 3 master 默认区间计算：`keymodel` 落 master1，`hotquality` 与 `circuit` 同时落 master2，`ratelimit/authstate/quota` 等落 master3。master2 是明确的热点节点，需单独监控与垂直扩容；若要重新分布，只能在迁移前更换 namespace（键全部重建），不能在线改。
- 连接串只列一个可达 seed 也能发现拓扑；但把不可达节点排在第一位会让首次连接多等约 5s（dev 实测），生产按“先列当前存活节点”的顺序写 `addr`。

故障与运维：

- 有 replica 时，master 故障后该 master 负责的 slot 会短暂报连接错误，随后 replica 提升、客户端自动刷新拓扑恢复。工厂已把 Cluster 拨号收紧为 1s、读写 2s、最多 1 次重试（默认 go-redis 是 5s×3 次）：dev 实测单客户端首错从默认 12s 提前到 4s、总错误窗口从 22s 缩到 11s，应用层只出现 1 个 500 后恢复 200。没有 replica 时这些 slot 直到节点恢复都不可用，且无数据副本。
- Redis 复制是异步的：故障瞬间未复制的写入可能丢失，不能把 Redis 当持久事实库（与现有设计一致）。
- **`FLUSHALL` 在 Cluster 下只清当前节点**（dev 实测：本地 FLUSHALL 后仅该节点 DBSIZE 归零，其余 master 不变）。迁移清库必须用 `redis-cli --cluster call <seed>:<port> FLUSHALL`（带认证时加 `-a`），并对每个 master 复核 `DBSIZE=0`。
- 每个 master 独立 `maxmemory`：集群总容量是各节点之和，但热点域只能吃到单节点上限；`noeviction` 下热点节点先写失败。
- `/health` 的 Redis Ping 只证明“至少一个节点可达”，不代表所有 slot 可用；生产监控必须按节点抓取（redis_exporter cluster 模式）并告警单节点下线/槽位缺失。
- 新增多键命令/脚本必须继续使用 `rediscfg` 的域命名空间与 hash tag；跨域多键操作在 Cluster 下会直接 CROSSSLOT，代码评审需拦截。

## 6. Caddyfile（默认入口）

仓库 `docker/single-server/Caddyfile` 模板：

```caddy
<domain> {
	encode zstd gzip
	# HSTS（2026-11-06 安全基线）：只声明本站；不要贸然加 includeSubDomains
	header Strict-Transport-Security "max-age=31536000"
	redir / /__aisys__/ permanent

	reverse_proxy /v1/* 172.18.0.10:3000 {
		flush_interval -1
	}
	reverse_proxy 172.18.0.10:3000
}
```

- `<domain>` 替换为你的域名；Caddy 自动 ACME 签发/续期，80 常驻 308 升级 HTTPS。
- 默认返回 `Strict-Transport-Security: max-age=31536000`，刻意不加 `includeSubDomains`（避免影响同域其他未全程 HTTPS 的子域）；改用外部入口时由该入口补等价 header。
- `/v1/*` 必须保留 `flush_interval -1`（SSE 不缓冲）。
- 上游固定 `172.18.0.10:3000`（compose 固定 IP），不要改成容器名，避免 Docker DNS 偶发失败导致 502。
- 若同机还有其他站点（共享 Caddyfile），保留其 server 块；本项目生产 Caddyfile 含同机公益站块，部署纯本项目时可删除。
- **换域名时必须同步改两处**：`Caddyfile` 的站点地址，以及 `compose.yml` 中 caddy healthcheck 的 URL（模板里仍是当前实例域名）。healthcheck 不随 Caddyfile 变化，漏改会让 caddy 一直 unhealthy（流量仍正常）。
- **Caddy → gateway 必须保持 HTTP/1.1 upstream**（默认即是）：客户端交接依赖 Hijack 先提交 200 头再断开；不要给 gateway 配 `transport http { versions h2c }`，否则交接退化为 503。

需要 Nginx 入口时见 [Nginx 部署](../nginx/README.md)。

## 7. 权限

以下命令在[部署指南 §7.1](../部署指南.md) 上传完成后、启动容器前执行：

```sh
chmod 600 /opt/juhe-ai/.env
mkdir -p /opt/juhe-ai/data/app
chown -R 1000:1000 /opt/juhe-ai/data/app   # 容器以 UID/GID 1000 的 app 用户运行
```

`data/app` 的 chown 必须在旧容器停止后执行（首次部署无旧容器，直接执行即可）；否则 app 用户无权写 usage spool 和日志。

## 8. 关联文档

- [部署指南](../部署指南.md)：部署流程、首次部署、升级、验证。
- [`docker/single-server/README.md`](../../../docker/single-server/README.md)：`.env` 契约、compose、发布机制。
- [PostgreSQL 与 Redis 高性能模式设计](../../functions/PostgreSQL与Redis高性能模式设计.md)：存储与运行态设计契约。
- [Nginx 入口部署](../nginx/README.md)、[HTTPS / 反向代理](../https/README.md)。
