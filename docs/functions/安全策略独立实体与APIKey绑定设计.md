# 安全策略独立实体与 API Key 绑定设计

> 状态：设计稿（2026-11-06），未实施。
> 定位：新增独立实体「安全策略」，管理面/个人面各一个菜单；API Key 可绑定一个安全策略。未绑定 = 内置「默认安全策略」 = **与当前行为逐字节一致**。
> 关联：[接口契约与权限矩阵](接口契约与权限矩阵.md)（安全策略章）、[安全与日志策略](安全与日志策略.md)、[原始审计日志设计](原始审计日志设计.md)、[审计日志保全策略设计](审计日志保全策略设计.md)、[调度偏好独立实体与 APIKey 绑定计划](../plans/计划-20261012T033000000Z-调度偏好独立实体与APIKey绑定.md)（实体/绑定/菜单的既有范式）。

## 1. 目标与非目标

### 1.1 目标

1. 新增 `security_policies` 实体与「安全策略」菜单（管理面 `/security-policies`、个人面 `/my-security-policies`），CRUD、owner 隔离、内置行只读、删除自动解绑，全部沿用调度偏好的既有契约范式。
2. `api_keys` 新增可空绑定列 `security_policy_id`；API Key 创建/编辑/公开面写入均可绑定；未绑定解析为内置行。
3. **一期只控制「用户内容是否落库」**：安全策略提供 `capture_mode`，决定该 Key 的成功/失败请求是否写入原始审计 payload、使用记录请求/响应快照。
4. 二期在同一实体上扩展：客户端 IP 存储方式、内容指纹持久化、按 Key 保留期、上游数据政策约束（禁止训练 / ZDR / 禁止第三方中转）。
5. 所有新增能力只在「绑定非内置策略」时生效；未绑定的存量 Key 行为不变。

### 1.2 非目标

- 不改现有原始审计、使用记录、运行日志、Codex context 的默认捕获与保留。
- 不做字段名/正则脱敏，不改变「日志型落点不做递归脱敏」的既有规范；本期做的是「不采集」，不是「采集后打码」。
- 不承诺端到端加密（中转必须看到明文）。
- 不改调度、路由、计费、配额、健康检查的语义。
- 一期不动 `juhe_codex_context` 段文件、审计 blob 加密、公开接口日志。

## 2. 实体与默认

### 2.1 内置行

| 字段 | 值 |
| --- | --- |
| `id` | `secpol-builtin-inherit`（固定不透明标识，三处常量同源） |
| `name` | `默认安全策略` |
| `system_account_id` | `NULL`（全局只读例外，所有 owner 可见可绑定） |
| `built_in` | `1` |
| `capture_mode` | `inherit` |
| `description` | `跟随系统当前的审计与使用记录捕获策略` |

- 由 maintenance `--seed` 幂等 upsert（只恢复内置行权威值，不触碰用户行）。
- 内置行任何业务写入口（PATCH/DELETE）返回 `403 { message: "系统内置安全策略不可修改或删除" }`。
- 内置行缺失或结构错误属于初始化失败，启动校验报错，不在热路径补种。

### 2.2 自定义行

- owner 为创建者 `system_account_id`；管理面可按 owner 查看/创建，个人面只能操作自己的行。
- 名称在 owner 内唯一（大小写不敏感），trim 后必填。
- 无启用/停用态（与调度偏好 v2 收口一致）：创建即生效。
- 删除为硬删除，同一事务内把绑定该策略的 API Key 重置为内置默认 ID，返回 `resetBindings`；数据库 FK `RESTRICT` 保留为兜底（先重置再删行）。

### 2.3 一期字段

| 字段 | 类型 | 默认 | 语义 |
| --- | --- | --- | --- |
| `id` | text PK | — | 策略 ID |
| `system_account_id` | text NULL | — | owner；内置行为 NULL |
| `name` | text | — | 名称 |
| `description` | text | NULL | 说明，仅后台识别 |
| `capture_mode` | text | `inherit` | `inherit` / `metadata_only` / `errors_only` / `full` |
| `built_in` | integer | 0 | 内置只读标记 |
| `created_at` / `updated_at` | text | — | 版本与 CAS |

`capture_mode` 语义：

| 值 | 成功请求 | 失败请求 | 说明 |
| --- | --- | --- | --- |
| `inherit` | 跟随系统 | 跟随系统 | 内置行默认；等价当前行为 |
| `metadata_only` | 不写 payload | 不写 payload | 只保留审计主行、上游尝试元数据、usage 结构化字段 |
| `errors_only` | 不写 payload | 按系统上限写 payload | 成功零正文，失败保留排障能力 |
| `full` | 跟随系统 | 跟随系统 | 显式声明「按系统策略完整捕获」，当前与 `inherit` 等价 |

- `metadata_only` / `errors_only` 下，原始审计仍写主行（路由、状态码、耗时、token、错误分类、账号/分组身份），只抑制 payload blob 与热搜索镜像正文。
- 使用记录仍写行与计费字段；仅把 `request_snapshot_json` / `response_snapshot_json` 置空。
- 被抑制的 payload 不写 blob、不写正文 hash；如需在审计详情解释原因，写一条 tombstone 引用：`capture_status='policy_suppressed'`、`drop_reason='security_policy'`、`hasBody=false`、只保留 `sizeBytes` 与 `partType`。
- `errors_only` 的「失败」口径与原始审计问题链路一致：`success_after_retry`、`gateway_failed`、`upstream_failed`、`stream_failed`、`downstream_closed`。

## 3. 数据模型

### 3.1 新表

```sql
CREATE TABLE IF NOT EXISTS security_policies (
  id text PRIMARY KEY,
  system_account_id text,
  name text NOT NULL,
  description text,
  capture_mode text NOT NULL DEFAULT 'inherit'
    CHECK (capture_mode IN ('inherit', 'metadata_only', 'errors_only', 'full')),
  built_in integer NOT NULL DEFAULT 0 CHECK (built_in IN (0, 1)),
  created_at text NOT NULL,
  updated_at text NOT NULL
)
```

索引（幂等）：

```sql
CREATE UNIQUE INDEX IF NOT EXISTS idx_security_policies_owner_name_unique
  ON security_policies(system_account_id, name) WHERE built_in = 0;
CREATE UNIQUE INDEX IF NOT EXISTS idx_security_policies_owner_name_unique_lower
  ON security_policies(system_account_id, lower(name)) WHERE built_in = 0;
CREATE UNIQUE INDEX IF NOT EXISTS idx_security_policies_builtin_id_unique
  ON security_policies(id) WHERE built_in = 1;
CREATE INDEX IF NOT EXISTS idx_security_policies_owner
  ON security_policies(system_account_id);
```

### 3.2 API Key 绑定列

```sql
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS security_policy_id text
  REFERENCES security_policies(id);
CREATE INDEX IF NOT EXISTS idx_api_keys_security_policy
  ON api_keys(security_policy_id);
```

- 纯加法：新表 + 可空列 + 索引；不改既有列、不改既有行。
- schema 走既有 `--ensure-schema` 加法相；revision 由升级账本分配，不占用既有空号。

## 4. API Key 绑定契约（五写入口矩阵）

绑定字段 JSON 名 `securityPolicyId`，列 `security_policy_id`。

| 写入口 | 提交 | 结果 |
| --- | --- | --- |
| 管理/个人创建 `POST /__aisys__/api/api-keys`、`POST /__aisys__/api/my-api-keys` | 缺省或 `null` | 持久化内置 ID `secpol-builtin-inherit` |
| 同上 | 非空 ID | 同 owner 自定义策略或全局内置行；否则 `400 { message: "安全策略不存在或不可用" }` |
| 管理/个人 `PATCH .../api-keys/{id}` | 缺失 | 保持原值（含存量 `NULL`） |
| 同上 | `null` | 持久化内置 ID（显式回默认） |
| 同上 | 非空 ID | 同创建校验；同值提交是 no-op（不 bump revision、不失效、不写操作日志） |
| 公开 `POST /__aipublic__/api-key/add` | 缺省或 `null` | 内置 ID |
| 同上 | 非空 ID | 按目标 Key owner 校验 |
| 公开 `POST /__aipublic__/api-key/update` | 缺失 | 保持原值 |
| 同上 | `null` | 内置 ID |
| 同上 | 非空 ID | 同规则校验；既有「至少一个变更字段」约束不变 |

- 绑定校验失败（非法类型、空串、不存在、跨 owner）统一 `400`，不区分提示，避免策略枚举。
- 绑定真实变更计入 `expectedRevision` CAS 与 `changedFields`；`patchChangeLabels` 新增 `{ field: "securityPolicyId", label: "安全策略" }`。
- 列表/详情/公开摘要新增 `securityPolicyId`、`securityPolicyName`、`securityPolicyBuiltIn`；`securityPolicyId=null` 表示存量未绑定，前端显示「默认安全策略（跟随系统）」，不得回显为某种捕获模式。
- 绑定变更提交后执行既有 `InvalidateValidation`（reason 沿用 `ReasonAPIKeyUpdated`）；失败语义与调度偏好一致：已提交写入不回滚，返回既有错误契约。

## 5. 生效链路（一期）

### 5.1 Key 运行时解析

- `loadGatewayAPIKeyRow` 增选 `api_keys.security_policy_id`，LEFT JOIN `security_policies`：
  - `security_policy_id IS NULL` → 内置 ID 常量，`capture_mode=inherit`；
  - 绑定行存在 → 使用该行 `capture_mode`；
  - 绑定引用损坏（行缺失 / 跨 owner / `capture_mode` 非法）→ 显式错误上抛，禁止静默回落 `inherit`（与调度偏好「绑定引用损坏显式错误」一致）。
- 解析结果作为附加字段进入既有 Key 运行时快照（内存 + Redis 序列化），字段名 `SecurityCaptureMode`；其余快照字段与指纹语义不变。
- 请求内固定同一快照，不在一次请求内二次读取策略。

### 5.2 原始审计捕获

- 网关链仍按现有逻辑生成终态审计对象与 payload 计划；在 F3 `Persist` 入队前按 `SecurityCaptureMode` 过滤：
  - `inherit` / `full`：完全走现有路径，零分支差异；
  - `metadata_only`：丢弃全部 `client_request` / `upstream_request` / `upstream_response` / `gateway_response` 的 headers 与 body，改写成 tombstone；
  - `errors_only`：成功链路按 `metadata_only`；问题链路（含 `success_after_retry`）按现有路径。
- `gateway_error` / `gateway_metadata` 保留元数据；`gateway_error` 正文同样受模式约束（`metadata_only` 不写 body）。
- 热搜索镜像：被抑制的 payload 不写入；不影响其他 Key 的镜像内容。
- 审计主行仍写：`trace_id`、账号/Key/分组/账户、模型、状态码、耗时、token、错误分类、`capture_status`。`capture_status` 置为 `metadata_only`；被抑制的 payload 引用以 `drop_reason='security_policy'` 区分于容量裁剪，不新增 `sample_reason` 取值。
- 不新增「关闭审计」能力：安全策略只改变正文捕获范围，审计主行始终保留。

### 5.3 使用记录快照

- `gatewayusage` 构建请求/响应快照前读取同一 `SecurityCaptureMode`：
  - `metadata_only` / `errors_only` 的成功链路：`RequestSnapshot=nil`、`ResponseSnapshot=nil`；
  - `errors_only` 的失败链路：按现有路径写快照；
  - `inherit` / `full`：不变。
- 使用记录主行、计费、统计、账号归属字段完全不变；`usage_record_spool` 契约不变（快照字段本就可选）。

### 5.4 不改的链路

- 调度、路由、候选、健康检查、冷却、配额、计费、限流、会话亲和、内容指纹运行态。
- 运行日志：安全策略不改变普通运行日志的捕获范围；现有「业务代码写了什么就输出什么」的规范不变。
- Codex context：一期不改；`metadata_only` 用户的 Codex 多账号上下文承接能力保持现状（见 §10 待决）。
- 公开接口日志：一期不改。

## 6. API

管理面（`RequireAdmin`）与个人面（`RequireSession(true)` + selfScope）镜像：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/__aisys__/api/security-policies` | 分页列表，支持 `keyword`、`systemAccountId`（管理面）、`page/pageSize`；返回 `boundApiKeyCount` |
| GET | `/__aisys__/api/security-policies/options` | 选项，支持 `ids` / `ids[]`、`keyword`、`limit`（1..100，默认 50） |
| GET | `/__aisys__/api/security-policies/{id}` | 详情 |
| POST | `/__aisys__/api/security-policies` | 创建；管理面必须显式 `systemAccountId` owner |
| PATCH | `/__aisys__/api/security-policies/{id}` | `expectedUpdatedAt` CAS；严格白名单 `name`/`description`/`captureMode`/`expectedUpdatedAt` |
| DELETE | `/__aisys__/api/security-policies/{id}` | 总是成功；同事务重置绑定，返回 `{ resetBindings }` |

个人面路径前缀 `/__aisys__/api/my-security-policies`，语义相同，owner 恒为当前登录用户。

- 创建/更新/删除写操作日志：`Module=security_policies`，`OperationKey=security_policies.create|update|delete`，`ResourceType=security_policy`，查看范围=资源 owner。
- PATCH 同值 no-op：零 DML、不推进 `updatedAt`、不写操作日志、不失效。
- 版本冲突：`409 { message: "安全策略已被其他操作更新，请刷新后重试", currentUpdatedAt }`。
- 公开面不提供安全策略 CRUD；`/__aipublic__/api-key/add|update` 只接受绑定字段。

## 7. 前端

- 路由与菜单：
  - 个人面 `/my-security-policies`，标题「我的安全策略」，菜单组「密钥与调度」；
  - 管理面 `/security-policies`，标题「安全策略」，菜单组「密钥与调度」，`managementRoles`。
- 列表列：名称、捕获模式（中文标签）、绑定 API Key 数、更新时间、操作。
- 编辑弹窗：名称、说明、捕获模式单选（四项，附一句影响说明）；内置行只读。
- API Key 编辑弹窗：新增「安全策略」选择器，默认为「默认安全策略（跟随系统）」；不改变现有「策略路由」「调度偏好」的布局与校验。
- API Key 列表新增「安全策略」列，显示策略名；未绑定显示默认策略名。
- 捕获模式文案：
  - `inherit`：跟随系统；
  - `metadata_only`：不留存正文（仅元数据）；
  - `errors_only`：成功不留正文，失败保留；
  - `full`：按系统策略完整捕获。

## 8. 不影响现有功能的兼容性保证

1. **存量 Key**：`security_policy_id` 为 `NULL` → 解析为内置 `inherit` → 审计、使用记录、热搜索、运行日志路径与当前一致；不新增必填校验、不回填存量行。
2. **新建 Key**：未显式选择时写入内置 ID；`inherit` 与 `full` 走现有分支，残留差异为零。
3. **数据库**：只加表、加可空列、加索引；不改既有列、不删行、不改约束语义。`--ensure-schema` 幂等。
4. **接口**：只新增路由与响应字段；既有请求体白名单新增可选字段，缺省不改变行为。
5. **前端**：既有页面只多一个选择器/一列；默认值渲染为默认策略，不改变原有表单必填与保存逻辑。
6. **调度与计费**：安全策略不进入候选筛选、排序、配额、计费；`SecurityCaptureMode` 只被审计/usage 捕获层消费。
7. **回归证据**：未绑定 Key 的审计 payload 计划、usage JSON、热搜索镜像写入需有「与改动前逐字节一致」的测试断言；绑定 `metadata_only` 的 Key 需断言「主行存在、payload 为零、usage 快照为零」。

## 9. 分期

| 期 | 内容 | 是否改变现有行为 |
| --- | --- | --- |
| P1 | 实体 + 菜单 + API Key 绑定 + `capture_mode` + 审计/usage 抑制 | 否（仅绑定非内置策略时生效） |
| P2 | `client_ip_mode`、`store_content_fingerprint`、按 Key 保留期覆盖、上游数据政策约束 | 否（新增可选字段） |
| P3 | 数据导出/删除、payload 查看访问审计、透明度页面 | 否（新增能力） |

P2 的上游约束依赖供应商/连接的上游数据政策元数据（`training_policy` / `retention_days` / `zdr` / `region` / `channel_type`），需单独设计；本设计只预留字段位置与消费点，不在 P1 实现。

## 10. 待决

1. 个人用户是否允许创建 `full` 策略（管理员可建）；若不允许，个人面只开放 `inherit` / `metadata_only` / `errors_only`。
2. `capture_mode=metadata_only` 时 Codex context 段文件是否也要按 Key 关闭（会牺牲多账号上下文承接）；建议 P1 不动，P2 单独裁决。
3. 菜单命名用「安全策略」还是「数据安全策略」；建议「安全策略」。
4. P2 上游数据政策的元数据来源与维护责任（管理员手填 vs 供应商目录内置），需单独取证后定。

## 11. 验收

1. 未绑定 Key 的全链路回归：审计 payload 计划、usage 记录、热搜索镜像与改动前一致（测试断言）。
2. 绑定 `metadata_only`：审计主行存在、payload 只有 tombstone、usage 快照为空、计费/统计字段不变。
3. 绑定 `errors_only`：成功链路无 payload；失败链路 payload 与现有上限/保留一致。
4. 五写入口矩阵全部覆盖：创建/编辑/公开 add/update 的缺省、null、非空、跨 owner、同值 no-op。
5. 删除策略：绑定 Key 同事务重置为内置 ID，`resetBindings` 正确，运行态失效生效。
6. 内置行写 403；个人面不能读写他人策略；管理面 owner 过滤正确。
7. 前端 typecheck、单测与既有回归全绿；新增菜单与选择器不改变既有 API Key 表单默认行为。
