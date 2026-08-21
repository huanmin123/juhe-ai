# J3a 代理延迟检测完整迁移契约

> 状态：L2 executor 与 Store/reader 基础域已完成本地验证，尚未接入 Go owner。基线为 `master@982468590`；本文件冻结 J3a 的实现契约，不启用 Go owner、不停止 Node owner，也不授权部署。

## 1. Owner 与范围

- Go `jobs` 将成为唯一的周期探测、单代理 lease、直接代理请求和 jobs outcome writer；不得调用 Node、gateway、IPC、Redis Stream、Node DB-service 或网关 HTTP。
- Node 保留管理路由、权限、手动命令 bridge、outcome projector 与业务 `proxy_profiles` writer；Go 不得写 Node business SQLite 或 `juhe_business.proxy_profiles`。
- 本批只迁移 `proxy-latency-refresh` 与 `POST /proxies/:id/test` 的已保存代理检测。账号复制、模型检测和质量检查不在范围内。
- owner gate 默认关闭。L2 可以拥有 SQLite 自有 Store 测试；真正 Go-owner 运行时只允许 PostgreSQL jobs Store + PostgreSQL 只读业务输入，SQLite business DB 不允许第二 writer。

## 2. 输入、资格与手动语义

### 2.1 周期输入

Go 在短生命周期、`REPEATABLE READ READ ONLY` 的 PostgreSQL 事务内读取冻结候选：启用的 `proxy_profiles`，按“未检测优先、`last_tested_at` 升序、`updated_at` 降序、`id` 升序”选取；同时读取启用 provider 的规范化 `base_url` 目标快照。业务库权限只允许该输入查询，使用明确 `statement_timeout`/`lock_timeout`，不依赖 PostgreSQL startup options。

每个输入至少包含 `proxy_id`、`input_version`、RFC3339 UTC `config_revision`（Node `updated_at`）、`trigger`、`issued_at`、`expires_at`、代理类型/主机/端口/用户名、Node 兼容的加密 password envelope、目标 ID/provider/URL 和 policy version。若只有 password 没有 username，Store 按 Node URL builder 语义丢弃该 envelope，不生成 `:password` 凭据；有 username 时才在 executor 内存中解密使用。不得在 input、outcome、日志或指标保存明文 password、拼接后的凭据 URL、响应 body、完整 header 或 cookie。

`input_version` 由 Go jobs Store 为每次不可变 request 生成；同一 request 的重放只按已存 identity 判断，不能用当前代理配置重写旧结果。

### 2.2 手动命令

保留当前管理员 `404`、诊断槽满 `503 + Retry-After`、成功 `200` 和执行异常 `502` 的外部语义。Node 只在显式 Go-owner gate 打开后向 jobs loopback HTTP 发送受限、加密的 v1 input，并同步等待最多 25 秒；Go 直接执行代理请求并写 outcome，不回调 Node。该 bridge 不是周期调度或持久队列。

## 3. 探测与结果语义

- 目标只允许 `http`/`https`，禁止重定向；响应 body 最大 512 KiB，CONNECT header 最大 64 KiB。
- `http`/`https` forward proxy、HTTPS CONNECT，以及持久化 Node `proxyUrlFromRow` 的 effective SOCKS 映射必须可单测：stored `socks5` 与 `socks5h` 均按 `socks5h` 远端 DNS 发起 CONNECT；配置无效或代理失败不得静默直连。HTTP 目标经 forward proxy 时，目标请求携带 `accept`、`user-agent`、`host`、`connection: close`、`proxy-connection: close` 和代理认证；HTTPS 目标经 HTTP(S) proxy 时，`host`/`proxy-connection`/代理认证只出现在 CONNECT 握手，隧道内目标请求只携带 `accept` 与 `user-agent`；SOCKS 隧道同样不得泄漏 forward-proxy 头。
- `observed_at` 固定为开始探测的 UTC 时刻，以兼容当前 Node 的 `last_tested_at <= testedAt` fence。探测完成时刻仅用于 outcome 存储排序和审计。
- 完整 framing 的任意 HTTP 状态码保持 item `passed`；2xx 归 `complete_success`，非 2xx 归 `framing_complete_neutral`。连接/DNS/TLS/超时/提前关闭/不完整 framing 归 `upstream_failure` 和 item `failed`。输入过期、取消、未发起请求的配置错误、lease 丢失和执行器错误归 `probe_task_failure` 和 item `unknown`。
- 整体状态保持 Node 规则：任一 failed 为 failed；无 failed 且 warning 或 passed/unknown 混合为 warning；仅 unknown 或空为 unknown；其余为 passed。
- 周期结果不包含出口 IP/地区；手动成功结果可以包含这两个字段。`probe_task_failure`、`stale`、`lease_busy` 默认只写可观察 receipt，不投影为代理 failed 或 unknown。

## 4. Jobs Store 与 fence

Go 自有 `juhe_jobs` / 独立 SQLite 文件包含：`proxy_latency_owner_leases`、`proxy_latency_proxy_leases`、`proxy_latency_input_versions`、`proxy_latency_inputs`、`proxy_latency_execution_claims`、`proxy_latency_outcomes`。只读 reader 只形成不可 JSON 序列化的 input draft；Store 在写入自己的 inputs 表时签发 `request_id + input_version`，并在 executor 首次上游请求前以 request 级 execution claim 原子交付持久化深拷贝；同一 request 的并发调用不得重复访问上游。owner lease 控制批次；proxy lease 控制同一代理的上游请求；每份 outcome 带 owner/proxy fence token、request/outcome ID、input/config revision、trigger、observed/storage time 与脱敏 items。

Store 签发时必须拒绝非 RFC3339 UTC revision、非 `j3a-proxy-latency-v1` policy、超出 1..15 分钟 TTL、无效 envelope、重复 provider target 等 draft；仅签发成功并持久化后才可请求上游。`AppendOutcome` 在同一 Store 事务中验证已签发 input 的 `request_id + proxy_id + input_version + config_revision + trigger`、有效期和 observed-time 区间，再验证 owner/proxy lease fence。相同完整 identity 与 payload digest 的 outcome 重放为幂等成功；同一 ID 但输入、fence 或 payload digest 不同为冲突/stale。SQLite 使用 WAL、`busy_timeout`、单连接和 `BEGIN IMMEDIATE`；PostgreSQL 使用短事务与 `FOR UPDATE`/`SET LOCAL`。PostgreSQL schema 由受控 migration/最小权限预置，jobs 不修改业务 schema。

## 5. Node 投影

Node projector 只读 `committed` jobs outcomes，按 `(stored_at, outcome_id)` 读取；`stored_at` 仅是 jobs Store 游标时间，业务 CAS 仍使用开始探测的 `observed_at`。它在同一个 Node business DB 事务内先处理 receipt，再校验 schema、proxy ID、trigger、config revision、开始观察时刻与允许的 outcome；只有匹配时才更新 `test_status`、`latency_ms`、允许的出口字段、`last_test_message` 和 `last_tested_at`。

业务 CAS 维持 `id + updated_at/config_revision + last_tested_at <= observed_at`。receipt 为 `applied`、`stale`、`ignored` 或 `rejected`；重复 outcome 不重复写，失败不推进 cursor。投影不得更新 `proxy_profiles.updated_at`，也不得触发代理配置缓存失效。

## 6. 已完成基础与剩余 L2

Header 模式边界：HTTP forward proxy 请求携带 `accept`、`user-agent`、目标 `host`、`connection: close`、`proxy-connection: close` 与代理认证。HTTPS 经 HTTP(S) proxy 时，`host`、`proxy-connection` 与代理认证只属于 CONNECT 握手；隧道内目标请求只携带 `accept` 与 `user-agent`。SOCKS 隧道同样不得收到 forward-proxy 头。

已完成且仅在本地 Go 测试（SQLite jobs Store + SQL contract）验证的基础域包括：proxy URL / stored `socks5` 与 `socks5h` 的 Node-effective 远端 DNS 映射、禁止重定向、Node probe request headers、512 KiB 收集上限并持续 drain 到完整 framing、CONNECT response header 64 KiB 上限、成功/neutral/upstream failure/probe task failure 分类、owner/proxy lease、request 级 execution claim、Store 签发的 durable `request_id/input_version`、持久化深拷贝 snapshot、issued-input identity/expiry fence、executor timeout 不越过 input expiry、payload SHA-256 digest 及 replay poison 校验，以及 outcome 的 trigger、RFC3339 UTC config revision 和 lease token 持久字段。最小 executor 只消费 Store 已签发且由 claim 交付的 input：先验证 live owner/proxy lease 与持久 payload，再逐 target 直接请求，且只把脱敏 item 写入 outcome；首次执行前的 input、解密、lease、取消或 claim 失败不伪造 committed outcome。已提交 request 只读回原 outcome，不再次请求上游；同一 request 并发执行返回明确 in-flight，不重复请求。password envelope 仅在 executor 内存中解密为 proxy URL，不进入 outcome、Store 错误或日志。PostgreSQL reader 已实现 `REPEATABLE READ READ ONLY` 与 transaction-local timeout；它使用 Node 兼容的候选排序、启用 profile 优先/无启用项回退、Gemini/GLM profile 优先和 username-only proxy 语义，但尚未在真实 PostgreSQL/PgBouncer 上运行。

这仍不构成生产 owner 切换。jobs supervisor/health 的 opt-in 本地 wiring 已加入，但 Node outcome reader/projector、手动 bridge、owner gate 和 Node 旧路径清零仍未实施。

## 7. L3 Go runtime readiness（本批）

本批新增但仍保持 opt-in 的 Go owner runtime：`JUHE_AI_PROXY_LATENCY_ENABLED=true` 且 `JUHE_AI_PROXY_LATENCY_JOBS_OWNER=go` 时，jobs 进程使用 PostgreSQL jobs Store 和 PostgreSQL `REPEATABLE READ READ ONLY` direct-input reader；启动阶段先完成 jobs schema/业务只读契约预检。runtime cycle 顺序固定为 owner lease → `LoadDue` → `IssueInput` → proxy lease → `ExecuteIssuedInput`。owner lease 在运行期间续租，续租/owner fence 丢失立即停止当前周期；proxy lease 返回持久化的 `lease_until`，proxy execution context 取实际剩余 lease window 与 issued input expiry 的较小值，不得按配置时长重新放大。单代理 issue/lease/executor 错误隔离但会记录失败并使 readiness 失效；context cancellation 终止周期并释放 owner/proxy 资源，释放错误保持可观测。

配置缺失、非法 duration/limit、非 PostgreSQL jobs Store 或缺少 owner gate/credential secret 均 fail-closed；默认关闭时不打开数据库、不创建 runner。jobs `/health` 增加 `proxyLatencyEnabled`、`proxyLatencyReady`、`proxyLatencyOwnerHeld`、最近周期/成功/错误、输入/执行/失败计数和 J3a readiness 汇总；启用后必须完成至少一轮无失败 cycle 才 ready，owner/lease 丢失或失败周期不得伪造成功状态。

本批不翻转 owner、不接 Node projector/manual bridge、不访问真实 dev PG/PgBouncer、不启动 Docker/Redis，也不修改 Node/accounthealth/tablemonitor。

## 8. L2 验收与非目标

L2 必须通过 Go unit/race/vet、Node typecheck 和新增回归：协议/DNS、取消/超时/response cap/full drain、CONNECT header、非 2xx、lease、execution claim 并发、持久 snapshot TOCTOU、重复 request、旧 revision/observed CAS、SQLite 单 writer 和 Node projector payload/metadata 双校验。当前本地命令已覆盖 proxylatency package/race/vet/jobs 全量、Node typecheck、三项 proxy regression 与 diff check；真实 dev PG/PgBouncer/Docker/Redis、Node -> Go -> Node 闭环、Jenkins 和 GitOps 属于后续 L3/L4，未完成前不得翻转 owner gate。
