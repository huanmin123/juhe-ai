# J3a 代理延迟检测完整迁移契约

> 状态：J3a 的周期执行、手动管理、结果投影、receipt/cursor 与 `proxy_profiles` CAS 写回均由 `jobs` 进程内 Go 实现独占；Node J3a scheduler、旧 executor、outcome reader/projector、business writer、手动 adapter、Node→Go 健康观测及其 Node 回归入口均已删除并归档至 `migration-backup/node/j3a-proxy-latency-manual-control-cutover-20260826/`。Go 管理入口保持原 `POST /__aisys__/api/proxies/:id/test` 资源路径、管理员鉴权、`404`/`503 + Retry-After`/`200`/`502` 响应契约，并直接追加 F4 兼容审计记录；不存在 Node fallback、双 owner、双写或 Go→Node/Go→Go HTTP。当前 Go/Node 定向测试与隔离 PostgreSQL schema/projector smoke 已通过，但本轮独立 Go jobs 管理 listener 的 dev 进程闭环尚未通过：Node 业务 schema 初始化成功，随后临时最小权限角色预检未完成，因此不能把 listener/F4 handoff 写成已通过。GitOps 已预置端口与精确 Go 入口定义，但默认关闭，Jenkins 只会在同一 release-state commit 写入 direct-Go 三镜像 digest 时同时启用。尚未 Argo 同步。生产部署、真实进程 handoff、active-path-zero、重启演练和生产/L4 仍需单独执行，未在本契约中伪称完成。

## 1. Owner 与范围

- Go `jobs` 将成为唯一的周期探测、单代理 lease、直接代理请求和 jobs outcome writer；不得调用 Node、gateway、IPC、Redis Stream、Node DB-service 或网关 HTTP。
- Go `jobs` 管理 listener 直接完成管理员 session / temporary token 鉴权、proxy/provider 快照、手动执行和 F4 兼容审计追加；入口层必须把原管理资源路径路由到该 listener。Node 不再拥有 J3a 管理路由、鉴权 adapter、operation-log writer、outcome reader/projector 或检测状态 writer。
- 本批只迁移 `proxy-latency-refresh` 与 `POST /proxies/:id/test` 的已保存代理检测。账号复制、模型检测和质量检查不在范围内。
- 运行时必须显式配置 `JUHE_AI_PROXY_LATENCY_JOBS_OWNER=go`；缺失或非法配置时 Go J3a fail-closed，不启动周期/手动路径，Node 也不回退。L2 可以拥有 SQLite 自有 Store 测试；真正 Go-owner 运行时只允许 PostgreSQL jobs Store + PostgreSQL 业务输入/结果写回，SQLite business DB 不允许第二 writer。

## 2. 输入、资格与手动语义

### 2.1 周期输入

Go 在短生命周期、`REPEATABLE READ READ ONLY` 的 PostgreSQL 事务内读取冻结候选：启用的 `proxy_profiles`，按“未检测优先、`last_tested_at` 升序、`updated_at` 降序、`id` 升序”选取；同时读取启用 provider 的规范化 `base_url` 目标快照。业务库权限只允许该输入查询，使用明确 `statement_timeout`/`lock_timeout`，不依赖 PostgreSQL startup options。Node 兼容边界要求保留每个启用 provider：空或非法 `base_url` 不形成网络请求，而冻结为 `target_url_invalid` 的 unknown item；其他 provider 继续执行，不能因单个 provider 无目标而丢弃整条 proxy 候选。

每个输入至少包含 `proxy_id`、`input_version`、RFC3339 UTC `config_revision`（Node `updated_at`）、`trigger`、`issued_at`、`expires_at`、代理类型/主机/端口/用户名、Node 兼容的加密 password envelope、目标 ID/provider/URL 和 policy version。目标允许携带受控的 `probe_error=target_url_invalid`，但此时 URL 必须为空且 executor 只能产生 unknown item；原始无效地址不得进入 input、outcome、日志或指标。若只有 password 没有 username，Store 按 Node URL builder 语义丢弃该 envelope，不生成 `:password` 凭据；有 username 时才在 executor 内存中解密使用。不得在 input、outcome、日志或指标保存明文 password、拼接后的凭据 URL、响应 body、完整 header 或 cookie。

`input_version` 由 Go jobs Store 为每次不可变 request 生成；同一 request 的重放只按已存 identity 判断，不能用当前代理配置重写旧结果。

### 2.2 手动命令

保留当前管理员 `404`、lease busy `503 + Retry-After`、成功 `200` 和执行异常 `502` 的外部语义。请求通过 ingress 到达 Go `jobs` 的显式管理 listener 后，在同一进程内读取受控 PostgreSQL 快照并同步等待最多 25 秒；Go 直接执行代理请求、写 outcome/投影并以最佳努力追加审计，不调用 Node 或另一 Go 进程。Node 的诊断槽是事件循环时期的实现限制，不迁入 Go；该管理入口不是周期调度或持久队列。

## 3. 探测与结果语义

- 目标只允许 `http`/`https`，禁止重定向；响应 body 最大 512 KiB，CONNECT header 最大 64 KiB。
- `http`/`https` forward proxy、HTTPS CONNECT，以及持久化 Node `proxyUrlFromRow` 的 effective SOCKS 映射必须可单测：stored `socks5` 与 `socks5h` 均按 `socks5h` 远端 DNS 发起 CONNECT；配置无效或代理失败不得静默直连。HTTP 目标经 forward proxy 时，目标请求携带 `accept`、`user-agent`、`host`、`connection: close`、`proxy-connection: close` 和代理认证；HTTPS 目标经 HTTP(S) proxy 时，`host`/`proxy-connection`/代理认证只出现在 CONNECT 握手，隧道内目标请求只携带 `accept` 与 `user-agent`；SOCKS 隧道同样不得泄漏 forward-proxy 头。
- `observed_at` 固定为开始探测的 UTC 时刻，以兼容当前 Node 的 `last_tested_at <= testedAt` fence。探测完成时刻仅用于 outcome 存储排序和审计。
- 完整 framing 的任意 HTTP 状态码保持 item `passed`；2xx 归 `complete_success`，非 2xx 归 `framing_complete_neutral`。连接/DNS/TLS/超时/提前关闭/不完整 framing 归 `upstream_failure` 和 item `failed`。输入过期、取消、未发起请求的配置错误、lease 丢失和执行器错误归 `probe_task_failure` 和 item `unknown`。
- 整体状态保持 Node 规则：任一 failed 为 failed；无 failed 且 warning 或 passed/unknown 混合为 warning；仅 unknown 或空为 unknown；其余为 passed。
- 周期结果不包含出口 IP/地区；手动成功结果可以包含这两个字段。`probe_task_failure`、`stale`、`lease_busy` 当前只产生 runtime error/`LastError`/失败计数，不提交 committed outcome，也不投影为代理 failed 或 unknown；Go result projector 对已提交 outcome 使用 durable receipt/cursor，处置为 `applied`、`stale`、`ignored` 或 `rejected`。

## 4. Jobs Store 与 fence

Go 自有 `juhe_jobs` / 独立 SQLite 文件包含：`proxy_latency_owner_leases`、`proxy_latency_proxy_leases`、`proxy_latency_input_versions`、`proxy_latency_inputs`、`proxy_latency_execution_claims`、`proxy_latency_outcomes`。只读 reader 只形成不可 JSON 序列化的 input draft；Store 在写入自己的 inputs 表时签发 `request_id + input_version`，并在 executor 首次上游请求前以 request 级 execution claim 原子交付持久化深拷贝；同一 request 的并发调用不得重复访问上游。owner lease 控制批次；proxy lease 控制同一代理的上游请求；每份 outcome 带 owner/proxy fence token、request/outcome ID、input/config revision、trigger、observed/storage time 与脱敏 items。

Store 签发时必须拒绝非 RFC3339 UTC revision、非 `j3a-proxy-latency-v1` policy、超出 1..15 分钟 TTL、无效 envelope、重复 provider target 等 draft；仅签发成功并持久化后才可请求上游。`AppendOutcome` 在同一 Store 事务中验证已签发 input 的 `request_id + proxy_id + input_version + config_revision + trigger`、有效期和 observed-time 区间，再验证 owner/proxy lease fence。相同完整 identity 与 payload digest 的 outcome 重放为幂等成功；同一 ID 但输入、fence 或 payload digest 不同为冲突/stale。SQLite 使用 WAL、`busy_timeout`、单连接和 `BEGIN IMMEDIATE`；PostgreSQL 使用短事务与 `FOR UPDATE`/`SET LOCAL`。PostgreSQL schema 由受控 migration/最小权限预置，jobs 不修改业务 schema。

## 5. Go 业务投影

Go result projector 只读 `committed` jobs outcomes，按 `(stored_at, outcome_id)` 读取；`stored_at` 仅是 jobs Store 游标时间，业务 CAS 仍使用开始探测的 `observed_at`。它在同一个 Go business-result 事务内先处理 receipt，再校验 schema、proxy ID、trigger、config revision、开始观察时刻与允许的 outcome；只有匹配时才更新 `test_status`、`latency_ms`、`last_test_message` 和 `last_tested_at`。`proxy_profiles` 的现役更新 trigger 会读取 `accounts`/availability projection，并对 `account_list_availability_dirty` 进行 upsert；因此 result role 必须拥有这条触发链所需的最小 `SELECT` 与 dirty 表 `INSERT`/`UPDATE`/冲突读取权限，`CheckContract` 在启动时以零行语句验证，缺失即 fail-closed。reader 对 PostgreSQL `observed_at` 保留微秒，并做行元数据与 payload 双校验。

业务 CAS 维持 `id + updated_at/config_revision + last_tested_at <= observed_at`。Go projector receipt 为 `applied`、`stale`、`ignored` 或 `rejected`；重复 outcome 不重复写，rejected 不推进 cursor，游标只在已处理结果后推进。投影不得更新 `proxy_profiles.updated_at`，也不得触发代理配置缓存失效。receipt/cursor 与业务状态均由 Go business-result 连接写入，Node 不再拥有任何 J3a 写回。

## 6. 已完成基础与剩余 L2

Header 模式边界：HTTP forward proxy 请求携带 `accept`、`user-agent`、目标 `host`、`connection: close`、`proxy-connection: close` 与代理认证。HTTPS 经 HTTP(S) proxy 时，`host`、`proxy-connection` 与代理认证只属于 CONNECT 握手；隧道内目标请求只携带 `accept` 与 `user-agent`。SOCKS 隧道同样不得收到 forward-proxy 头。

已完成且仅在本地 Go 测试（SQLite jobs Store + SQL contract）验证的基础域包括：proxy URL / stored `socks5` 与 `socks5h` 的 Node-effective 远端 DNS 映射、禁止重定向、Node probe request headers、512 KiB 收集上限并持续 drain 到完整 framing、CONNECT response header 64 KiB 上限、成功/neutral/upstream failure/probe task failure 分类、owner/proxy lease、request 级 execution claim、Store 签发的 durable `request_id/input_version`、持久化深拷贝 snapshot、issued-input identity/expiry fence、executor timeout 不越过 input expiry、payload SHA-256 digest 及 replay poison 校验，以及 outcome 的 trigger、RFC3339 UTC config revision 和 lease token 持久字段。最小 executor 只消费 Store 已签发且由 claim 交付的 input：先验证 live owner/proxy lease 与持久 payload，再逐 target 直接请求，且只把脱敏 item 写入 outcome；首次执行前的 input、解密、lease、取消或 claim 失败不伪造 committed outcome。已提交 request 只读回原 outcome，不再次请求上游；同一 request 并发执行返回明确 in-flight，不重复请求。password envelope 仅在 executor 内存中解密为 proxy URL，不进入 outcome、Store 错误或日志。Go result projector 已实现 `(stored_at,outcome_id)` reader、业务事务 receipt/cursor/CAS 与 `proxy_profiles` 写回；本轮 dev scratch 通过 PgBouncer `6432` required 验收时，applied/receipt/cursor/replay/outbound 属于 durable projector 覆盖，manual stale/deleted/no-target 属于无 receipt/cursor 的直接 CAS 边界，详见 [L3-PG 真实验收报告](../reports/J3a代理延迟检测L3-PG真实验收报告-2026-08-22.md)。该证据不代表生产发布。

当前代码已经完成一次性 Go owner 接线：Go jobs 独占周期执行、手动管理、结果投影、receipt/cursor 与 `proxy_profiles` CAS 写回；Node 的 J3a scheduler、旧 executor、outcome reader/projector、business writer、管理 adapter 和健康观测均已删除。生产部署、真实进程 handoff、active-path-zero、重启演练和生产/L4 仍需单独执行，不能用本地证据冒充已上线。

## 7. L3 Go runtime readiness（本批）

本批新增的 Go owner runtime（J3a 唯一执行路径）：`JUHE_AI_PROXY_LATENCY_ENABLED=true` 且 `JUHE_AI_PROXY_LATENCY_JOBS_OWNER=go` 时，jobs 进程使用 PostgreSQL jobs Store、Go result projector 和 PostgreSQL `REPEATABLE READ READ ONLY` direct-input reader；启动阶段只调用只读 `CheckSchema` 与业务读写契约预检。生产 PostgreSQL J3a 表、索引和 `juhe_jobs` schema owner 必须在受控 migration 中预置；运行期绝不调用 `EnsureSchema`、不做 DDL，缺失或不完整即 fail-closed。`EnsureSchema` 只允许隔离 SQLite 自动初始化或明确授权的一次性 scratch/bootstrap 维护，不能作为稳定 owner 的部署补救。runtime cycle 顺序固定为 owner lease → candidate pool → batch target → proxy lease → `IssueInput` → `ExecuteIssuedInput` → Go business projection。默认 `BatchSize=1000`、candidate pool factor=10、worker concurrency=1000、input limit=10000；input、batch、worker、direct reader、manual target 与 outcome projector 均统一支持到 `1000000`，candidate pool factor 支持到 `1000`。这些是 Go worker 的有效吞吐配置，不继承 Node 单事件循环时期的小并发阈值；Pod CPU/内存请求与上游承载能力才是实际容量边界。busy lease 会跳过并继续补位，selected/target/claimed/started/processed/skipped/deferred/partial 进入 health snapshot。owner lease 在运行期间续租，续租/owner fence 丢失立即停止当前周期；proxy lease 返回持久化的 `lease_until`，proxy execution context 取实际剩余 lease window 与 issued input expiry 的较小值，不得按配置时长重新放大。单代理 issue/lease/executor/projector 错误隔离但会记录失败并使 readiness 失效；context cancellation 终止周期并释放 owner/proxy 资源，释放错误保持可观测。

配置缺失、非法 duration/limit、非 PostgreSQL jobs Store 或缺少 owner gate/credential secret 均 fail-closed；默认关闭时不打开数据库、不创建 runner。jobs `/health` 增加 `proxyLatencyEnabled`、`proxyLatencyReady`、`proxyLatencyOwnerHeld`、最近周期/成功/错误、输入/执行/失败计数和 J3a readiness 汇总；启用后必须完成至少一轮无失败 cycle 才 ready，owner/lease 丢失或失败周期不得伪造成功状态。

J3a readiness 由 Go jobs `/health` 及 jobs metrics 独立观测；Node 不再读取或聚合 J3a Go health。Node 系统健康响应为兼容保留 `proxyLatency={enabled:false,ready:true}`，不能作为 J3a 发布证据。Jenkins 必须直接验证 Go jobs 的 `ready`、`proxyLatencyEnabled`、`proxyLatencyReady` 与 `proxyLatencyOwnerHeld`，不得只以 HTTP 状态码或 Node health 判定通过。

本批不保留 Node/Go 双 owner、双写或运行时 fallback。Go 配置、F4 审计表/权限、credential envelope alias 或 Go 服务缺失时管理 listener 必须拒绝启动或请求必须失败，不得回退到 Node。Go PgBouncer required jobs/projector smoke、现役 Node 代理管理 PG CRUD/CAS smoke 和管理 listener 单元测试已有证据；独立 Go jobs listener 的 dev 进程闭环仍未通过，原因是本轮 Node schema 初始化后未完成 F4 `operation_log_*` 表与最小权限角色预检，不能把它写成 listener/F4 handoff 通过。Go projector smoke 的 applied/receipt/cursor/replay/outbound 与 manual stale/deleted/no-target 分支分别记录，不能把无 receipt 的直接 CAS 分支写成 durable receipt 通过。该证据不替代真实 dev Pod、Argo 同步、独立 jobs 发布 handoff 或生产 handoff；active-path-zero、重启演练和生产/L4 仍需单独执行。

## 8. L2 验收与非目标

L2 必须通过 Go unit/race/vet、Node typecheck 和新增回归：协议/DNS、取消/超时/response cap/full drain、CONNECT header、非 2xx、lease、execution claim 并发、持久 snapshot TOCTOU、重复 request、旧 revision/observed CAS、SQLite 单 writer、Go 结果投影 payload/metadata 双校验、Go 管理鉴权/审计 DTO 和 Node active-path-zero 检查。当前本地命令已覆盖 proxylatency package、jobs command、Node typecheck、Go result projector、Go 管理 handler 与 Go-only 删除回归；本轮 dev PgBouncer `6432` required jobs/projector smoke、Node PG CRUD/CAS smoke 与 Go business-result projector 已实际通过并清理，projector durable 与 direct-CAS 分支按各自语义记录。独立 Go jobs 二进制的 dev 部署、F4 schema/权限、入口路由、Jenkins、GitOps、active-path-zero、owner handoff 和生产/L4 属于后续门禁。

## 9. Node↔Go 深度对照门

深度对照报告：[J3a 代理延迟检测 Node↔Go 深度对照报告（2026-08-22）](../reports/J3a代理延迟检测Node-Go深度对照报告-2026-08-22.md)。实现层面的三项阻断已落地修复：

1. Go result projector 独占 committed outcome reader、receipt、cursor、CAS 与 `proxy_profiles` 写回；Node outcome reader/projector 文件已删除。
2. Node J3a scheduler、旧 executor 和 refresh 回归入口已删除；Node 不再扫描候选、不再执行探测、不再写 J3a 状态。
3. Node 手动路由、loopback adapter 与 health observer 已删除；Go jobs 管理 listener 保留 `404`、`503 + Retry-After`、`200`、`502`、25 秒 deadline、session/temporary-token 鉴权、CAS stale 与审计外部边界；Go 不可用时直接失败，不走 Node fallback。

数值默认值、provider target 排序、IPv6 CONNECT authority、SOCKS wire、407/502/TLS/truncated/slow-drip 仍需同 fixture Node/Go golden 验证；不得以本地 Go 测试或一次 dev smoke 代替跨运行时闭环。

## 10. 完整迁移前的明确禁止事项

- 不通过修改用户可观察业务语义来掩盖 Go 缺口；J3a 管理 API 与操作日志由 Go jobs 直接处理，`proxy_profiles` 业务写回只允许 Go result projector。
- Go owner 是唯一 J3a owner；Go 不可用时 fail-closed，不做双写、双 consumer 或 Node fallback。生产部署、active-path-zero、owner handoff、重启演练和 L4 仍是发布门禁，不是第二套运行时实现。
- 不把 dev scratch PG/PgBouncer 结果扩展为生产/L4 结论；所有跨运行时和生产门禁必须另立计划、单独取证。

## 11. Node 机制差异冻结（深度对照补充）

### 11.1 失败写回不是同一语义

Node 周期候选旧实现遇到非取消执行异常时会把 `testStatus=unknown`、`latency=null`、错误消息、配置 revision 和 `testedAt` 写回业务读模型；该 Node J3a writer 已删除。Go 的输入、claim、凭据、取消、lease 或 envelope 失败属于 `probe_task_failure`：当前实现直接返回 runtime error、更新 runner 的 `LastError`/失败计数，不提交 committed outcome，也不得让 projector 把旧业务状态误改成 `unknown`。Go result projector 对已提交 outcome 使用 durable receipt/cursor 与幂等处置；不得把当前 runtime error 误称为已持久化 receipt。真实 transport failure 仍是代理 item `failed`，不能与调度执行失败混淆。Go projector 已冻结三类结果的映射和旧状态保留规则。

### 11.2 手动 ProxyTestReport 不是 Go Outcome 的同构对象

历史 Node 手动报告外部 schema 冻结为：报告字段 `proxyId`、`proxyName`、`score`、`grade`、`status`、`passedCount`、`warningCount`、`failedCount`、`outboundIp?`、`outboundRegion?`、`baseLatencyMs?`、`testedAt`、`items`、`message`；item 字段 `name`、`status`、`httpStatus?`、`latencyMs?`、`message`、`targetUrl?`。`?` 表示字段为 `undefined` 则省略，不得改写为 `null`；synthetic 基础连通性 item（无 provider 或 provider 聚合）可省略 `targetUrl`，provider item 无论 passed/failed/unknown/deadline/transport 都保留 `targetUrl`，其中 unknown/deadline/transport 可省略 `httpStatus`、`latencyMs`；无可用出口或基础延迟时也省略对应 report 字段。状态枚举冻结为 `passed|warning|failed|unknown`；聚合规则为 failed>0→failed，否则 warning>0 或 passed 与 unknown 混合→warning，否则 passed>0→passed，否则 unknown；`unknown` score=0，否则 `max(0, round(100-warningCount*10-failedCount*35))`，grade 为 A(≥90)/B(≥75)/C(≥60)/D。报告包含 synthetic 基础连通性 item；provider item 使用 display name/base URL，外部响应不携带 provider code。provider code 仅允许作为 Go 管理 handler 的内部查找元数据，code→display-name 映射不得改变外部 schema。出口探测顺序固定为 `ip-api`、`ipwho.is`、`api.ip.sb`、`ipinfo.io`、`ipify`、`httpbin`；仅 HTTP 200 进入解析，解析失败或非 200 才回退下一个目标，周期刷新不写 outbound。Go `Outcome` 不包含这些管理端聚合字段，Go 管理层必须逐字段适配，不能直接把 Go outcome 当成响应。

### 11.3 手动入口边界

Go 管理 handler 在读取快照前完成与 Node `requireAdmin` 等价的 session / temporary-token 鉴权，停用 proxy 仍可进入手动测试；owner/proxy lease busy 返回 `503 + Retry-After`，开始和结束存在性检查对应删除竞态返回 `404`；CAS stale (`updated=false`) 仍返回 `200` 报告且不启动/写入 outbound；异常才返回 `502`。无启用 provider 时 Go 仍生成 `unknown` 基础 item并返回 `200`；Go 周期 reader 在无有效 target 时返回零候选，不能以启动失败阻断手动 unknown 分支。F4 兼容 operation log 是最佳努力追加，25 秒 deadline、父取消和仅 applied 后执行的 outbound fallback 均由 Go 保留；Node 诊断槽/bridge 不再是运行边界。

### 11.4 调度器生命周期

以下 Node `proxy-latency-refresh` 数值仅是历史 oracle，用于解释迁移前语义；该 scheduler 已删除，不是当前运行配置或 owner 指令。当前只以 Go jobs runtime 配置和 Go owner handoff 门禁为准。

Node `proxy-latency-refresh` 的事实配置为 60 秒间隔、4 分钟初始延迟、30 秒 stable phase、`coalesceOne`、`external-account-maintenance` lane、60 秒 task timeout、30 秒至 10 分钟 failure backoff、ops-worker 角色；默认 `batchSize=20`、`candidatePoolFactor=4`，候选池为 `max(limit, limit*4)`，`targetCount=min(limit,candidates.length)`，`startedCount`/`deferredCount`/partial summary 可观察；busy lease 会跳过并继续补位，另受 global diagnostic concurrency、candidate deadline 与 lease grace 限制。scheduler timeout 只 abort，底层 task 结束前仍占用 running/lane；`stopAndDrain` 必须报告 active count。Go 当前默认 `BatchSize=1000`、`InputLimit=10000`、candidate pool factor=10、worker concurrency=1000，并暴露 selected/target/claimed/started/processed/skipped/deferred/partial 计数；这组提高后的容量是刻意差异，不作为 scheduler parity 缺口。Go 仍没有 Node 的 resource lane/global governor/统一 stopAndDrain runtime，因此这些剩余差异必须在 owner handoff manifest 中明确为 non-goal 或另立门禁，不能宣称完全 scheduler parity。

## 12. 证据与提交边界

- 报告中的“通过”统一解释为 Node 源码事实或 Go 本地验证；没有同一 fixture 的 Node/Go runtime golden 就标记为 `cross-runtime-unverified`。
- 本轮 dev PgBouncer smoke 与 Go business-result projector 的真实结果以本次受控验证记录为准；scratch 数据库、角色、临时 PgBouncer 认证和 outcome/receipt/cursor fixture 均已清理。历史 Node 子进程→Go handler 互操作仅证明已删除 bridge 的旧边界；新的 Go 管理 handler 仍须在独立 jobs 二进制、F4 schema/权限和入口路由就绪后完成 dev/生产闭环。
- 本轮受控范围覆盖 Go `proxylatency` runner/projector/direct reader/management handler、Node 管理 CRUD（不含已删除 J3a test route）和定向回归，以及 architecture/functions/migration/plans/reports 的 owner 说明；`accounthealth`、`accountbalance`、`migration-backup`、`docs/bug` 等并行 dirty worktree 路径不作为 J3a 通过证据，也不被本契约改写。

## 13. 生产发布前置快照（2026-08-26）

以下是从生产现役 Pod 的运行角色执行的只读关系/权限预检；未读取 Secret 值，未执行 `INSERT`、`UPDATE`、`DELETE` 或 DDL。它证明当前状态，**不**证明未来 J3a 专用 jobs/input/result 角色已正确配置。

- 现役 Go jobs `/health` 报告 `proxyLatencyEnabled=false`、`proxyLatencyOwnerHeld=false`；因此“J3a 已生产接管”不成立。后续发布应直接检查 Go jobs health，而不是依赖 Node 代理健康字段。
- 2026-08-26 从正式外部 Node health 的 `HTTP 200` / J2 状态只是一项历史只读事实，不能作为 J3a Go-owner、owner lease、Go jobs schema、Secret alias 或管理 listener 已启用的证据。仍须由具备受控 K3s/数据库只读权限的维护流程完成后续 gate。
- `juhe_business.proxy_latency_projection_receipts`、`juhe_business.proxy_latency_projection_cursors` 已存在；`juhe_jobs` schema 可用，但 `proxy_latency_owner_leases`、`proxy_latency_proxy_leases`、`proxy_latency_outcomes`、`proxy_latency_input_versions`、`proxy_latency_inputs`、`proxy_latency_execution_claims` 六张 J3a jobs 表及其两个 outcome 索引尚未存在。
- 现役应用角色有 `juhe_jobs` schema `USAGE`/`CREATE`，并可读取候选来源、读写现有 projection receipt/cursor；这只是 bootstrap 能力线索，不能替代按最小权限为 jobs、input、result 三条连接分别验收的 `GRANT`。
- 本批 GitOps 工作树已为 test/prod 预置 `JUHE_AI_PROXY_LATENCY_*` Go-owner 配置、每槽位独立 instance ID，以及稳定/A/B/preview Service 同构的 `j3a-management:3307` 端口；仅稳定 Service 的 Traefik `IngressRoute` 可匹配 `POST /__aisys__/api/proxies/{id}/test`。默认 `enabled=false` 且不引用路由资源。Jenkins 检测 direct-Go 源码后才会把开关、路由和三镜像 digest 写为同一 release-state 提交，并在标记 passed 前经受限 active-Pod port-forward 读取 Go `/health` 的 `ready`、`proxyLatencyEnabled`、`proxyLatencyReady`、`proxyLatencyOwnerHeld`，再以受控 temporary token 调用精确 POST 并回读 F4 管理端审计。缺少 verifier 凭据、环境 proxy ID、受限 observer 权限或任一闭环时必须失败；不能退回 Node health。反向候选另记录 `candidateJ3aManagementEnabled`，稳定切换必须把该能力值、三镜像 digest、两项 enabled flag、IngressRoute 引用和 stable selector 同一提交提升。旧 7 列 release history 记录在回滚时明确解释为 `J3a=false`。当前 `go-jobs` 容器 request 为 `100m CPU / 128Mi`、limit 为 `300m CPU / 256Mi`。这与 J3a 默认 `1000` worker 的容量模型不相称，切流前必须按实际 Pod CPU、内存、PostgreSQL/PgBouncer 与上游预算同步调整，不能以 Node 时代的小并发或当前容器 limit 静默压低 Go 功能。

为避免在稳定 Go owner 启动时以 runtime DDL 掩盖缺表，维护项目提供唯一的 J3a PostgreSQL 一次性 bootstrap：`juhe-ai-maintenance --check-j3a-proxy-latency-postgres` 默认只读检查，报告缺失或不兼容对象时退出 `3`；检查同时验证上述必需列的名称、类型、`NOT NULL` 与主键/唯一约束契约。maintenance 与 jobs runtime 复用同一 `shared/contracts` schema 事实，`Store.CheckSchema` 也在稳定 owner 启动前执行同样的结构 preflight，不能通过跳过 maintenance 让同名畸形表进入运行期。只有获得数据库变更授权后才可执行 `--apply-j3a-proxy-latency-postgres`。它只会在**已存在且当前连接角色拥有**的 `juhe_jobs` schema 中幂等创建上述 6 张表和 2 个索引，并在同一事务复查；对任何同名但结构不符的既有表 fail-closed，绝不以 `ALTER`、删除重建或隐式数据修复掩盖问题。它拒绝创建 schema、变更 schema owner、触碰 `juhe_business`、写入 `goose_db_version` 或进行任何 Node 初始化。连接仅从 `JUHE_AI_MAINTENANCE_J3A_POSTGRES_URL` 当前进程环境读取，JSON 报告只含数据库名、角色和对象标识，绝不含 URL 或 Secret。该一次性命令不是 jobs runtime，也不替代三条 runtime 最小权限连接或 GitOps release。

生产 handoff 必须按以下顺序由获授权的 GitOps/数据库维护流程执行并留存脱敏证据：

1. 在候选环境以向前兼容的独立 schema/权限契约预置并验证上述 6 张 jobs 表和索引；先以 `--check-j3a-proxy-latency-postgres` 留存缺项/owner 证据，获授权的受控数据库流程才可对预置且由目标 jobs role 拥有的 schema 执行 `--apply-j3a-proxy-latency-postgres`，再以只读检查返回 `0` 的 JSON 报告留证。jobs role 仅写 J3a jobs 表，input role 只读候选数据，result role 仅有 `proxy_profiles`、receipt/cursor 及 `proxy_profiles` update trigger 所需 `accounts`/availability projection 最小读取和 availability dirty 表 upsert 权限。不得首次在 stable owner 启动时把 `EnsureSchema` 当成 schema 验收。
2. 以 Secret alias 提供 `JUHE_AI_PROXY_LATENCY_POSTGRES_URL`、`INPUT_POSTGRES_URL`、`RESULT_POSTGRES_URL`、`CREDENTIAL_SECRET`，以及管理 listener 专用的 `JUHE_AI_PROXY_LATENCY_MANAGEMENT_POSTGRES_URL`；同一代理密码必须已经是与 `CREDENTIAL_SECRET` 匹配的 J3 v1 envelope。ConfigMap 明确提供 `ENABLED=true`、`JOBS_OWNER=go`、实例 ID、`STORE=postgres`、`JUHE_AI_PROXY_LATENCY_MANAGEMENT_ENABLED=true`、`JUHE_AI_PROXY_LATENCY_MANAGEMENT_LISTEN_ADDRESS`、`JUHE_AI_PROXY_LATENCY_MANAGEMENT_DEADLINE=25s` 和经过容量评审的参数。不得记录或输出任何 Secret 值。
3. 在受控 F4 migration 中先预置 `juhe_dataset.operation_logs`、`operation_log_targets`、`operation_log_viewers`、`operation_log_summary_search_terms` 及管理角色的 `INSERT` 权限；Go listener 会在 bind 前做只读 schema/权限预检，任一缺失即拒绝启动，绝不丢弃审计或回退 Node。
4. 候选 Pod 启动后，直接验证 Go `/health` 的 `proxyLatencyEnabled=true`、`proxyLatencyOwnerHeld=true`、至少一轮成功周期后的 `proxyLatencyReady=true`/`ready=true`；Jenkins 只对明确列名的 active Pod 建立短生命周期 `pods/portforward` 到 loopback jobs health，禁止用 Node health 代替。再经入口路由验证 `POST /__aisys__/api/proxies/:id/test` 到同一 jobs 进程的 Go handler，并由管理端回读 `operationKey=proxies.test` 的 F4 审计，覆盖管理员 temporary token、`404`、`503 + Retry-After`、`200` 报告、Go outcome/projector 与审计可见性。HTTP `200` 本身不构成通过。
5. 切换入口路由与删除 Node 管理路径必须作为同一 release 原子执行；随后记录 active-path-zero、单 owner/单 writer、owner lease、重启恢复、一次失败回退和回滚 release。J3a 路径不允许 Node→Go、Go→Node 或 Go→Go HTTP，配置缺失或 Go 不可用必须 fail-closed。
6. Jenkins 仅在上述 JSON health、manual 闭环、F4 audit readback 和 active-path-zero 全部通过后晋级同源 digest；生产记录必须包含 release/digest、schema contract、脱敏键名检查、health 摘要、owner epoch、观察窗口及回滚结果。任何一项缺失时 J3a 保持关闭，不启动 J3b/J3c 的 owner 切换。
