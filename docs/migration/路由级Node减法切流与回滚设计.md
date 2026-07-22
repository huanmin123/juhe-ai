# 路由级 Node 减法切流与回滚设计

> 状态：书面设计已批准，待实施。基线：`origin/master=13b7dbd160b408ecb14451a45d803b3628c8f329`（2026-07-22）。关联计划：[PLAN-0081](../plans/计划-0081-Node转Go渐进减法迁移.md)、[PLAN-0153](../plans/计划-0153-路由级Node减法切流.md)。

## 1. 目标

在 Node 与 Go 共存期间，把“整个 management/public 域一次切换”改成可审计的路由级减法：每次只把已经完成 Go 实现和真实验证的 `HTTP method + canonical path` 切给 Go，未列出的请求继续由 Node 处理。稳定后删除该精确 Node 路由；不能通过保留长期双实现来换取表面回滚能力。

迁移保留业务原理和公开契约，不逐行翻译 Node。Node 已确认的缺陷在 Go 修复并记录原因；Go 能用不可变 DTO、有界查询、显式上下文和原生并发模型简化的地方采用 Go 方式实现。

## 2. 当前事实与阻断

当前不具备生产路由级切流能力，不能直接开始删 Node：

- `deploy/owner-manifest.json` 只有 `management/public/gateway/worker` 四个粗粒度 owner，且全部为 `node`；它无法表达单条 GET 路由。
- `JUHE_AI_MANAGEMENT_API_ENABLED=true` 会注册整套管理路由，不是单路由 owner 开关。`JUHE_AI_PUBLIC_API_ENABLED` 同样以整个 `/__aipublic__` 前缀为粒度。
- 标准 release、启动脚本和 Docker/Compose 仍是 Node 单应用；仓库内 Caddy/Nginx 示例也是整站单 upstream，没有 Node/Go method+path 分流。
- Node/Go `server` owner lock 是进程级共享锁，会阻止两个 server 以不同路由 owner 共存；它不能替代路由 owner 校验。
- 已提交代码的 Goose authority 是 `69`，但 owner manifest 为 `67`，`deploy/start.ps1` / `start.sh` 仍要求 `63`，部署迁移文档仍引用 `57`。主工作区另有尚未合入的 `000070` 候选；它在合入前不属于 authority。正式 owner 门禁前必须以届时最新已提交 catalog 原子同步全部声明。
- Go `health` 在依赖降级时仍可能返回 HTTP 200；切流必须解析 health JSON 并同时检查 `readyz`，不能复用只执行 `curl -f` 的旧整实例脚本。

因此，本设计不把“Go handler 已存在”“单元测试通过”或“全局 opt-in 能启动”视为接管证据。

## 3. Owner 模型

### 3.1 路由键

每个 HTTP owner 记录使用以下稳定键：

```text
routeKey = METHOD + " " + canonicalPathPattern
```

规则：

- method 必须显式；`GET` 不自动包含 `HEAD` 或 `OPTIONS`。
- 首批只允许 exact path。带资源 ID 的 pattern 必须等 exact pilot 稳定后再启用，并使用受约束的单段参数，不接受任意正则或 catch-all。
- 默认 owner 永远是 Node；只有清单中显式标记且通过门禁的 routeKey 才进入 Go。
- 不允许 exact、parameter、prefix 规则重叠后命中不同 owner；配置生成阶段必须拒绝歧义。
- `/__aipublic__/*`、`/v1/*`、gateway 通配入口在具备独立 owner 设计前不得拆成看似只读的单条路径。

建议 manifest 后续升级为版本化声明，至少包含 `id/method/path/owner/capabilities/rollbackOwner`。代理配置、Go route allowlist、发布预检和 smoke 必须从同一声明生成或校验，禁止维护四份手工清单。

### 3.2 进程 owner 与数据 owner 分离

HTTP 路由切到 Go 不等于 Node 进程可以退出。首批期间继续保留：

- Node HTTP server：承载 gateway、未迁移路由、内部 bridge、模型目录 reconcile/prewarm 和请求侧 writer 投递。
- Node DB service：承载仍未迁移的 System API、Node writer 和 worker 存储访问。
- Node ingest-worker：usage/audit/operation/public/runtime index 与记录维护等尚未逐任务切换的 writer/consumer。
- Node stats-worker：系统指标、usage/IP/分组/排名/窗口/表空间等聚合与新鲜度 owner。
- Node ops-worker：账户测试、健康检查、冷却恢复、OAuth token、余额、代理及清理任务。
- Node temporary maintenance worker 与 supervisor：保留 lease、heartbeat、排空和重启责任。

Go worker 只有在某个任务具有独立 owner 记录、Node 对应任务已停止且真实恢复/重试门禁通过后才能启用。不能因为已有同名 Go 命令就与 Node 重叠消费或双写。

## 4. 首批候选

### 4.1 Pilot：匿名公共设置

第一条试点固定为：

```text
GET /__aisys__/api/settings/public
```

理由：Node 和 Go 都在登录鉴权前提供该路由；Go 只读取 `global_settings` 的 `appName/appIcon`，不创建或 touch session，不触发 worker、缓存重建或业务写。两端仍会写 Redis IP read limiter 计数，这是允许的基础设施副作用，必须在真实 smoke 中核对 200/429、`Retry-After`、`no-store` 和可信代理 IP 语义。

它目前只是条件候选：真实 PG/Redis `w1a-public-settings-smoke`、真实 Go listener、精确 ingress owner 证明和回切演练未完成前，不删除 Node 入口。

### 4.2 第一批管理纯读

完成 route registry、双 listener 发布和 Pilot 后，按以下顺序逐个切换，不整批同时开：

| 顺序 | routeKey | 选择理由 | 额外门禁 |
| --- | --- | --- | --- |
| 1 | `GET /__aisys__/api/external-integration-sources/scopes` | 静态 scope catalog，无业务写 | admin/self 权限与 Node catalog 快照相同 |
| 2 | `GET /__aisys__/api/external-integration-sources/api-docs` | 嵌入式文档 catalog，无 DB 写 | 响应体 hash、敏感字段与缓存头对照 |
| 3 | `GET /__aisys__/api/stats/usage-window` | 只读统计时区，不扫 usage 明细 | admin、非法/缺失时区 500、31 日边界 |
| 4 | `GET /__aisys__/api/my-stats/usage-window` | 同上，强制当前用户 scope | query 越权参数必须被忽略 |
| 5 | `GET /__aisys__/api/settings/global` | 只读品牌设置，read auth 不 touch | 不能连带切同 URI 的 PATCH |
| 6 | `GET /__aisys__/api/settings` | 有界白名单读取系统设置 | 53 key、错误脱敏、时区语义对照 |

完成上述静态/设置组后，再切实际页面高频消费且当前 DTO 已对齐的用户流量组：

- `GET /__aisys__/api/proxies/options`
- `GET /__aisys__/api/system-accounts/options`
- `GET /__aisys__/api/authorization-options/grantee-accounts`
- `GET /__aisys__/api/my-authorization-options/grantee-accounts`
- `GET /__aisys__/api/authorization-options/grantee-teams`
- `GET /__aisys__/api/my-authorization-options/grantee-teams`
- `GET /__aisys__/api/route-strategies/options`
- `GET /__aisys__/api/my-route-strategies/options`

前端实际依赖 proxy 的 `id/name/type/enabled`、账户的 `id/username/displayName/status`、团队的 `id/name/status`，以及策略的 `id/name/mode/status/isDefault` 和可选 owner 字段。切流必须保留前端缓存依赖的 `/page-data/confirm` 为 Node，不能把 `/options` 扩成资源前缀。

operation log 管理/个人列表和详情四条 GET 作为第一批后半段候选。它们只读且已有 Go smoke，但 viewer 权限、分页、changes 脱敏与渐进 total 更复杂，必须逐角色 fixture 对照后再切。

### 4.3 先迁实现、后进入切流清单

以下纯读路由适合并行迁移，但在代码尚未合入、真实 listener 未验证时不是切流候选：

- `GET /__aisys__/api/model-checks/options` 与 `GET /__aisys__/api/my-model-checks/options`：Node 当前为静态常量，不能连带切 `/run/active`。
- `GET /__aisys__/api/announcements/public/{id}`：只读详情，不能连带切 `POST /announcements/public/read`。
- provider definitions 与单模型 capabilities：只读 catalog，需精确 scope、enabled 和 404 契约。
- audit error groups 与 runtime grep：优先整合已有并行实现；grep 还需 `rg` 路径、进程超时、文件权限和结果上限门禁。

### 4.4 明确暂缓

- `/__aipublic__/*/list`：统一 Bearer 鉴权会更新 `last_used_at`、Redis penalty-window 并入队 public API log，仍按整个前缀单 owner。
- public API logs：Node 旧表和 Goose 类型未完成离线同步与 writer 单 owner。
- providers/options 与 providers/models/options：当前 master 的 Go DTO/ID 语义仍落后最新前端和 Node；先整合 provider capabilities 分支并重新对照后再放行。
- usage records：需要真实多分区查询计划和 Node writer -> Go reader 证据。
- accounts/groups 动态列表：业务漂移频繁，仍依赖运行态/预聚合/授权 union，不作为首批试点。
- `auth/captcha`：GET 但会创建 challenge；所有 auth/session 写路径均不属于纯读。
- table monitor、audit runtime、system metrics runtime：必须改成 Go 的 PG/Redis/worker/runtime 口径，不能复制 Node/SQLite/supervisor 字段。
- gateway、Chat、OAuth writer 和任何 worker：按各自单 owner 设计推进，不混入本批。

## 5. 放行门禁

每条 routeKey 必须逐项有证据，不能以包级测试代替生产范围证明：

1. **契约门禁**：Node/Go 同 fixture 比较 status、JSON、headers、权限、非法参数、空数据、分页/排序和错误脱敏；确认最新 Node 业务调整已同步或记录 Go 修复差异。
2. **纯读门禁**：除 read limiter 计数外，无 session touch、DB mutation、enqueue、游标推进、惰性 rebuild、cache version publish 或本地文件状态更新。
3. **数据门禁**：schema catalog、Node gate、owner manifest、start scripts 和部署文档使用同一已提交版本；真实 PG/Redis/Asynq 测试在 `JUHE_AI_REQUIRE_INTEGRATION=1` 下不能 `SKIP`。
4. **双 listener 门禁**：Node/Go 使用不同 loopback 端口；分别通过 health JSON `success=true,status=ok`、两个 `readyz` 入口和路由专属真实 smoke。Go 端口不得直接公网暴露。
5. **owner 门禁**：method+path manifest 无重叠；生成的代理 dry-run 只改变目标 routeKey，默认 catch-all、gateway、其他 methods 仍指向 Node。
6. **切流门禁**：保存 previous proxy config/hash；原子 reload；通过 ingress 响应标记或受控诊断证明请求实际到 Go，而不是只看业务 200。
7. **观察门禁**：在预定窗口核对 route 级请求量、4xx/5xx/429、p95/p99、Go readiness、PG pool、Redis limiter 和 Node writer/worker lag；无自动 fallback 掩盖故障。
8. **删除门禁**：稳定窗口结束后，删除 Node 精确 route 注册、对应仅供该路由使用的 service/test/命令；`rg` 证明无入口残留。共享 service 仍被其他 Node 路由使用时不得误删。

## 6. 切流步骤

1. 获取最新 `origin/master`，确认工作树干净，记录 Node/Go commit、schema version、manifest hash 和 proxy config hash。
2. 启动 Node 原 owner；启动 Go 候选 listener。两者仅绑定 loopback，Node bridge 保持直连且不经过公网代理。
3. 直接端口执行 health/readyz、route smoke 和 Node/Go fixture 对照。
4. 生成只改变一个 routeKey 的候选代理配置，执行静态校验和 dry-run；验证 catch-all 仍为 Node。
5. 保存 previous config，原子 reload 候选配置。
6. 从 ingress 执行成功、权限、429、非法输入和 adjacent-route probes，证明 owner 与未切路径均正确。
7. 观察并记录指标；首批不得同时切下一条，避免归因不清。
8. 满足稳定窗口后再进入 Node 精确路由删除提交；删除提交仍保留上一发布包用于回滚。

## 7. 回滚

切流失败不做运行时双读或自动 Node fallback：

1. 立即停止继续放量。
2. 原子恢复 previous proxy config 并 reload。
3. 通过 ingress owner 证明和路由 smoke 确认已回到 Node。
4. Go listener 保留用于取证，不影响 Node writer；必要时停止 Go listener，但不停止 Node server/DB service/worker。
5. 若 Node 路由已经从当前发布包删除，部署上一份同 schema 契约的 Node release；schema 变化只按已批准的离线恢复方案处理，不由运行时代码降级。

回滚成功的证据必须包括 owner、业务响应和 worker 新鲜度，不能只记录“代理 reload 成功”。

## 8. 并行实施边界

可并行：各路由契约、fixture、独立 Go handler/store、真实 smoke、指标查询和文档复核。必须串行：route manifest schema、代理生成器、`router.go/server.go/config.go` 中央接线、schema catalog、最终切流和 Node 删除。所有实现分支先同步最新 master；Node 新业务提交必须重新触发对应 route 的漂移审计。
