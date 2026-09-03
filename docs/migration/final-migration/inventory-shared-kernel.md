# 内核横切设施清单（2026-09-04 子代理盘点）

用途：W1 内核工作包（K1-K8）的实现规格源；中间件链顺序、env 变量面、Redis key 面、Prometheus 指标面都必须等价移植。

## 1. shared/ 文件清单（48 文件，核心摘录）

| 文件（行数） | 职责 | Go 去向 |
| --- | --- | --- |
| logger.ts (1110) | pino 双通道日志、轮转保留 | K1（zap/slog 等价，文件格式契约保持） |
| request-context.ts (760) | AsyncLocalStorage 请求上下文/traceId/IP 判定/HTTP 指标埋点 | K1（Go context） |
| gateway-cache-invalidation.ts (494) | 4 topic 跨进程失效：本地 handler + Redis 版本号 + 1s 节流 + 重试 | K5 |
| cache.ts (487) | 进程内 + Redis 共享 JSON 缓存（版本化 clear、zset 索引、lease） | K5 |
| redis-client.ts (312) | Redis 客户端、离线排队、deadline 包装 | K5（go-redis） |
| redis-stream-queue.ts (881) | XADD+Lua 原子入队、消费组、backlog 记账 | **P05/J-F 消灭队列时对照语义** |
| redis-stream-drain.ts (186) + redis-stream-metrics.ts (129) | 3 条 Stream 排空契约与指标 | X02 删除面 |
| redis-queue-fence.ts (140) | 队列维护围栏租约 `juhe-ai:queue:fence` | X01 消除 |
| prometheus-metrics.ts (331) | 自研 /metrics 文本渲染 + 队列指标 | K1 |
| performance-process-metrics-registry.ts (424) + process-event-loop-monitor.ts (113) | 进程事件循环样本发布（TTL 20s/60s） | **X02 Node 指标删除面** |
| runtime-readiness.ts (34) | /health 200/503 就绪快照 | K1 |
| runtime-state-store.ts (355) | 运行态 KV（memory/Redis 双 driver：JSON/CAS/incr/锁） | K5/G 运行态 |
| runtime-probe-state-store.ts (1054) | 探测状态机（generation、原子替换、围栏） | J-A/G11 |
| http-security.ts (56) + system-error-message.ts (99) + http-compression.ts (30) | 安全头/中文错误文案映射/gzip≥1024B | K1 |
| account-concurrency.ts (1106) + concurrency-governor.ts (223) | 账号并发槽位租约（text/image lane）、全局槽位 | G13 |
| keyed-batch-buffer.ts (140) + bounded-buffer.ts (52) + retry-policy/queue (562) + queue-size.ts (107) | 批量缓冲/有界缓冲/重试 | 平台包等价（已有 schedulejitter 等） |
| worker-owner.ts (337) | worker 所有权判定（node/go）与 owner 锁 | X01 退役 |
| upstream-base-url-validator.ts (259) + upstream-url-policy.ts (263) | SSRF/私网/重定向校验 | G15（对照 platform/upstreamhttp 已有语义） |
| loopback-http.ts (24) | 仅 loopback 的 Node↔Go 交接边界 | X01 消除 |
| process-fatal*.ts + supervisor-*.ts (369) | fatal 诊断、子进程输出/重启策略 | X01 消除（Go supervisor 已有） |
| http.ts/query-values/rfc3339/release-schema-version(94)/runtime-log-file-name 等 | 工具 | 各包内等价 |

注意：**shared/ 下没有限流**；限流在 `modules/system-api/system-api-rate-limit.middleware.ts`（476 行，K3 规格）与 `modules/gateway/runtime/user-request-limit-coordinator.ts`（173 行，G13）。

## 2. 配置面（config/runtime.ts 1,728 行，约 330 个 JUHE_AI_* 读取）

分组与代表变量（完整逐变量清单在 K1 实施时以 rg 提取核对）：

- 进程/网络：HOST、PORT、TRUST_PROXY、RUNTIME_MODE、PROCESS_ROLE、INSTANCE_ID、WORKER_ROLE、*_REPLICAS、OWNER_LOCK_*、OWNER_MANIFEST_PATH
- 数据库：DATABASE_DRIVER/PATH、POSTGRES_URL、POSTGRES_*_TIMEOUT、DB_POOL_MAX、DB_SERVICE_*（HTTP 面板/队列/并发）、SQLITE_READ_WORKER_POOL_SIZE、USAGE_SHARD_COUNT/ROOT、*_DATABASE_PATH（USAGE_CATALOG/STATS/DATASET/CHAT/AUDIT_LOG/RUNTIME_LOG/TABLE_MONITOR）、ACCOUNT_BALANCE_POSTGRES_URL、ACCOUNT_HEALTH_JOBS_*
- Redis：REDIS_CACHE_URL / REDIS_QUEUE_URL / REDIS_STATE_URL、REDIS_NAMESPACE、REDIS_STREAM_*、QUEUE_DRIVER、CACHE_DRIVER、RUNTIME_STATE_DRIVER
- 网关：GATEWAY_UPSTREAM_AGENT_*、GATEWAY_BODY_IN_FLIGHT_MAX_MB、GATEWAY_ACCOUNT_CIRCUIT_*（~9）、GATEWAY_PROXY_HEALTH_*（~6）、GATEWAY_AUTOMATIC_PROBE_*（10）、GATEWAY_ACCOUNT_SIDE_EFFECT_*（6）、SPEED_FIRST_*（9）、GATEWAY_DISPATCH_ACCOUNT_CANDIDATE_LIMIT、GATEWAY_USAGE_FINALIZATION_MAX_ITEMS、ALLOW_PRIVATE_UPSTREAM_BASE_URLS、UPSTREAM_BASE_URL_PRIVATE_ALLOWLIST、ACCOUNT_DEFAULT_CONCURRENCY_LIMIT、ACCOUNT_HEALTH_*、OAUTH_PROXY_URL
- 认证/安全：SECRET、COOKIE_SECURE/SAME_SITE、OIDC_ENABLED/ISSUER/KEY_ENCRYPTION_SECRET、AUTH_CAPTCHA_DISABLED、DEV_AUTO_LOGIN_USERNAME、ALLOWED_ORIGINS、CONTROL_READ_REPLICA_ORIGINS、TEMPORARY_ACCESS_IP_ALLOWLIST、ACCOUNT_HEALTH_INPUT_SIGNING_KEY、J1_CROSS_LANGUAGE_*
- 日志/观测：LOG_*（8）、AUDIT_LOG_*（~12）、OPERATION_LOG_INPUT_*、RUNTIME_LOG_*（6）、GO_RUNTIME_METRICS_URL、TABLE_MONITOR_*（~8）、GATEWAY_TIMING_DETAIL_SAMPLE_PERMILLE
- 业务：CHAT_*（~10）、CODEX_CONTEXT_*/CODEX_WEB_SEARCH_*、CODE_INTERPRETER_*、COMPUTER_BROWSER_ADAPTER_*、HOSTED_TOOL_*_MODE、OPENAI_COMPATIBLE_FILES_ROOT、BACKGROUND_*（约 45 个批量/延迟/队列上限）、DATA_DIR、USAGE_SPOOL_*

约束：Go 侧同名兼容；`PROCESS_ROLE`/worker 拓扑类变量随 X01 退役。

## 3. 中间件链确切顺序

### server.ts（前置进程）
trust proxy → 关 x-powered-by → 1.requestContext → 2.systemErrorMessageLocalization → 3./__aisys__ 安全头 → 4-5./internal/account-test-dispatch（读副本守卫 + secret 路由）→ 6.GET /health、/__aisys__/health、/__aisys__/metrics → 7./my-chat chatHttpProxy → 8-12./.well-known、/oauth、/__aidelegated__、/__aisys__/api、/__aipublic__ → dbServiceHttpProxy → 13.help + 静态托管 + SPA fallback → 14./__aisys__ 404 → 15.网关栈：rejectGatewayTrafficOnControlNode → rejectUnrecognizedGatewayProtocolRequest → preResolveGatewayRuntime → handleGatewayDbServiceUnavailable → filesRouter → vectorStoresRouter → rawBody 限额 → speedFirstAdmission → parseGatewayRawBody → captureGatewayRawBody → openAIGatewayRouter → 16.全局 404 → 17.错误 handler

### system-api-app.ts（db-service 进程内）
etag=false → 1.requestContext → 2.errorLocalization → 3.compression → 4./api no-store → 5.dbAccessMode → 6.readReplicaGuard → 7.IP 限流 → 8./my-chat 链（requireAuth→用户限流→admission→json 24mb→selfScope→chatRouter）→ 9./api json 256kb → 10-12./__aipublic__（守卫→capturePublicApiLog→json→dbAccessMode→admission）→ 13-14./__aidelegated__/v1 → 15./api/health → 16./.well-known、/oauth 守卫 → 17.oauthPublicRouter → 18./auth authRouter → 19./settings/public → 20./__aipublic__ externalIntegrations → 21.requireAuth → 22.用户限流 → 23.admission → 24.readReplicaProxy → 25.业务路由批（my-* 强制 self；管理面 requireAdmin）→ 26.404 → 27.handleSystemApiError

## 4. Redis key 面（前缀 `juhe-ai:<NS>:`，三实例 cache/queue/state）

| key/前缀 | 用途 | 去向 |
| --- | --- | --- |
| cache:<name>:<key> / cache-index / cache-version / cache-lease | 共享 JSON 缓存（name：settings:system、settings:global、gateway:runtime、gateway:runtime-api-key-identity、gateway:settings、gateway:group-usage-access、gateway:openai-accounts、gateway:provider-model-catalog） | K5/G10 |
| state:<store>:<key> | 运行态 KV（auth_login_guard、auth_captcha、四 oauth sessions、*refresh-locks、gateway_quota_snapshot、gateway-hybrid-route-affinity、gateway-codex-turn-retry、gateway-gemini-interaction-affinity、gateway-client-ip-account-avoidance、gateway-configured-account-policy-avoidance、gateway-upstream-bucket-health、gateway-normal-route-latency-degradation、gateway-client-ip-error-circuit 等） | K2/G 各包（G11-G13 需做 key 清单文档） |
| state gateway_cache_invalidation topic:*（4 topic） | 网关缓存失效版本 | K5 |
| queue:usage-records / public-api-logs / record-maintenance（+group、:backlog-created-at） | 3 条 Stream | P05/J-F/X01（消灭） |
| queue:fence | 队列维护围栏 | X01 消除 |
| runtime:process-event-loop:*（+index:v2） | Node 事件循环指标 | X02 删除 |

## 5. Prometheus 指标面

- Counter：juhe_ai_http_requests_total{route_group,method,status_class,outcome,failure_scope,service}；juhe_ai_gateway_upstream_failures_total{failure_class,reason_class,status_class,service}
- Histogram：juhe_ai_http_request_duration_seconds（buckets 0.05..10）；juhe_ai_gateway_first_output_duration_seconds（0.25..60，label method）
- Gauge：juhe_ai_http_requests_in_flight；能力位×3；juhe_ai_process_resident_memory_bytes / heap_used_bytes / uptime_seconds；Redis Stream 队列族（10 项 × 3 队列）
- 暴露点 GET /__aisys__/metrics；Go runtime 指标由 JUHE_AI_GO_RUNTIME_METRICS_URL 拉取（gometrics 已有）
- 约束：指标名/label/buckets 等价保持；队列族与 process_* 指标随 X01/X02 删除，Go 侧按《Go系统指标字段迁移清单》口径
