# W9 网关 preflight 纯读基础设计

## 背景

W9 只为后续 Go 真实网关降低风险，不接管 Node 网关 owner。当前 Node `gateway-api-key.repository.ts` 会把系统账户和路由策略的 active 条件放进 JOIN，并在 API Key 配额共享快照未命中时回退 DB service 精确统计。该行为适合现有 Node 运行链路，但不适合作为 Go W9 的可解释、无副作用准备层基线。

## 目标

- 接受原始 API Key，仅在 `sk-` 前缀合法后使用现有 `apikeysecret.Hash` 计算 SHA-256 hash。
- 只读 PostgreSQL 中 API Key、系统账户、路由策略、最多 20 条 active 分组绑定和网关必要设置。
- 分别判定 API Key 不存在、禁用、过期、系统账户禁用、路由策略禁用和无可用绑定。
- 只读现有 Redis runtime state `gateway_quota_snapshot/current`；额度快照缺失、不完整、不可读或超额时返回明确 Decision，不扫描 `usage_records`，不执行精确 usage 回退。
- service 输出使用字段不导出的 Go value DTO；切片访问器返回副本，调用方不能修改缓存内对象。
- 允许注入 shared cache version reader 使结构缓存跨进程失效；未注入缓存时每次从事实源读取，语义仍正确。
- 不挂 HTTP、router、server，不接管 `/v1`，不读取账户候选，不申请 slot，不写 Redis route counter，不产生任何运行态副作用。

## 方案比较

### 方案一：复刻 Node JOIN 和 fallback

代码最接近 Node，但禁用原因会被压成“无效 Key”，额度快照缺失时还会进入精确统计，不符合 W9 的可解释纯读边界。

### 方案二：单条宽 SQL 一次拼完全部事实

往返少，但绑定、授权、设置会把结果扩成笛卡尔积，查询难以稳定限制 20 条，未来候选读取也容易误混入同一查询。

### 方案三：分层有界读取

先按唯一 hash 读取一条 key/account/strategy 状态，状态通过后再读取最多 20 条绑定，最后读取固定键集合设置。结构事实可进入可选版本缓存，配额快照独立读取和判定。本次采用该方案。

## 组件与数据流

1. `store/port/gatewaypreflight.go` 定义 PostgreSQL 结构事实和 quota current snapshot 端口。
2. `store/postgres/gatewaypreflight.go` 使用参数化、固定 schema SQL：唯一 hash 单行、绑定 `LIMIT $5`、设置固定键数组和上限。
3. `modules/gatewaypreflight/service.go` 按稳定优先级返回 Decision，并把 port record 转为不可变 DTO。
4. `modules/gatewaypreflight/cache.go` 提供可选结构缓存、shared version reader 和 quota runtime state reader。结构缓存不缓存 quota Decision。

数据流固定为：

`raw key -> prefix guard -> SHA-256 hash -> structural cache(optional) -> key/account/strategy -> bindings(max 20) -> settings -> quota current -> Decision`

## Decision 契约

- `ready`
- `invalid_api_key_format`
- `api_key_not_found`
- `api_key_disabled`
- `api_key_expired`
- `system_account_disabled`
- `route_strategy_disabled`
- `no_active_bindings`
- `quota_snapshot_missing`
- `quota_snapshot_incomplete`
- `quota_snapshot_unavailable`
- `quota_exceeded`

数据库结构错误、设置缺失或 JSON 无效属于内部 error，不伪装成业务 Decision。

## 查询与性能边界

- API Key 查询只按 `key_hash = $1`，`LIMIT 1`。
- 绑定查询只读取 route strategy、binding、group、group authorization/settings，按 `priority ASC, created_at ASC, id ASC`，最多 20 条。
- 设置查询只读取当前 Node 网关准备层需要的固定 13 个数值键，`streamCircuitBreakerEnabled` 维持当前常量 `true`。
- 所有动态值均通过参数传入；禁止字符串拼接用户输入。
- 本切片不读取 `accounts`、`group_accounts`、凭据、候选账户、统计明细或 `usage_records`。

## 缓存与并发

- 默认不创建缓存，service 直接读 store。
- 注入 cache 后，key 仅为 hash，不保存明文 API Key。
- 内置 version reader 分别注入 cache Redis 与 state Redis getter，组合 API Key validation、system settings shared version 和 `gateway_runtime_cache` topic version；任一版本变化即清空结构缓存。
- version 读取失败时绕过缓存并读取 PostgreSQL，不提供陈旧降级。
- loader 前后各读取一次版本；读取期间发生版本变化时返回本次只读结果但不回填缓存。
- cache 使用互斥锁保护 map，DTO 和切片在存取边界克隆；race 测试覆盖并发 Resolve。

## 配额语义

- API Key 未启用任何 quota limit 时不要求 quota snapshot。
- 启用 quota 后读取 `gateway_quota_snapshot/current`。
- snapshot 不存在或缺少 generatedAt：`quota_snapshot_missing`。
- 当前 scope entry 缺失且 `costEntriesComplete=false`：`quota_snapshot_incomplete`。
- 当前 scope entry 缺失但窗口声明完整：`quota_snapshot_missing`，避免把竞态误判为零成本。
- snapshot 读取/JSON 解码失败：`quota_snapshot_unavailable`。
- entry 存在时按 hourly/daily/weekly/monthly/total 任一上限判断；不回扫 usage，不调用 DB service。

## 测试与非目标

测试覆盖 Decision 表、hash、不可变 DTO、quota missing/incomplete/exceeded、默认无缓存、版本失效、并发 race、SQL 参数化/上限/禁读候选契约和 `go vet`。

真实 PostgreSQL `EXPLAIN`、HTTP 接线、候选账户读取、运行态过滤、slot acquire、Redis route counter、协议转换、上游转发、SSE、usage/audit 写入均后置到后续 W9/W10 切片。
