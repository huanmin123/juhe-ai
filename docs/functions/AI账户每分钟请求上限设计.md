# AI 账户每分钟请求上限（RPM）设计

> 状态：已实施 2026-11-03，待随全量发布上线（发布与生产验证见 `docs/待办与阻塞事项.md` T104）。
> 定位：在现有账户硬并发上限之外，为 AI 账户增加账户级「每分钟向上游派发请求次数」上限（RPM），两层限制正交叠加；本文是字段契约、计数口径、调度语义、展示和升级的唯一权威。
> 关联：[高并发分组调度设计](高并发分组调度设计.md)、[核心功能设计](核心功能设计.md)、[接口契约与权限矩阵](接口契约与权限矩阵.md)、[AI账户批量编辑设计](AI账户批量编辑设计.md)、[AI账户导入协议](AI账户导入协议.md)、[升级兼容与迁移账本设计](升级兼容与迁移账本设计.md)。

## 1. 背景与目标

部分上游供应商不限制同时并发数，而是限制每分钟请求数（RPM）。现有 `concurrency_limit` 只能约束「同一时刻在途请求」，无法表达「一分钟内累计派发次数」：并发上限很低时仍可能在一分钟内对上游发出远超其配额的大量请求。

目标：

1. 账户可配置 `rpm_limit`，`0` 表示不限制；与 `concurrency_limit` 同时存在、同时生效。
2. 两个限制在同一个原子临界区判定，任何一次派发不会出现「扣了 RPM 却没占并发」或「占了并发却没扣 RPM」。
3. RPM 耗尽时优先切换其他账户；全池 RPM 耗尽时有界等待到最近重置点，仍不可用返回 `429 + Retry-After`。
4. 管理列表、编辑表单、批量编辑、导入导出、公开 API 和授权实例全部一致。

非目标：

- 不做 60 秒精确滑动窗口或令牌桶平滑（v1 固定自然分钟窗口；理由见 §5.1）。
- 不做分组级 RPM 默认值/上限（仅账户级）。
- 不根据上游 `429` 自动学习或回写 `rpm_limit`。
- 不改变现有 `concurrency_limit` 的字段语义和 503/429 终态分流。

## 2. 现状事实（取证）

| 事实 | 位置 |
| --- | --- |
| 账户并发字段 `accounts.concurrency_limit integer NOT NULL DEFAULT 5000` | `backend-go/projects/maintenance/internal/schema/pg_schema_business_tables.go:610` |
| 进程内并发 tracker：`map[accountID] → {total, text, image}` + 单互斥锁，`TryAcquire` 同锁校验并自增 | `backend-go/projects/gateway/internal/gatewayclientip/accountconcurrency.go:42-95` |
| Redis 并发实现有 acquire/release Lua，但生产组合根未装配（仅 memory tracker 生效） | `backend-go/projects/gateway/internal/gatewayclientip/accountconcurrency_store_redis.go:160-238`、`cmd/juhe-ai-gateway/chain_runtime.go:343-348` |
| 派发首取与 Key 轮换重取都走 `TryAcquireAsync`；失败记入 `capacityLimitFailures` 并跳过账户 | `backend-go/projects/gateway/internal/gatewaydispatch/dispatchsingle.go:137-260` |
| 候选运行时上限来源：授权实例取来源账户 `source_accounts.concurrency_limit` | `backend-go/projects/gateway/cmd/juhe-ai-gateway/chain_accounts.go:239/:270`、`:1466-1471` |
| 高并发分组容量等待：`WaitForCapacity` 只认并发计数，ready 判定按账户 total/lane | `backend-go/projects/gateway/internal/gatewaydispatch/upstreamdispatch.go:1119-1208`、`internal/gatewayclientip/queue.go:424-458/:656-682/:859-874` |
| 个人分组并发耗尽终态是 503；超级分组目标容量耗尽才是 429 | `cmd/juhe-ai-gateway/chain_v1_loop.go:1408-1479`、`:1306-1400` |
| 列表 `currentConcurrency` 由进程内 tracker 读时叠加 | `backend-go/projects/gateway/internal/accounts/list.go:330-368` |
| 账户列表并发列 `current/limit` 内联编辑 | `frontend/src/views/accounts/accountTableColumns.ts:17`、`AccountConcurrencyEditor.vue` |

结论：RPM 必须挂在同一个 `TryAcquire` 原子临界区和同一个账户资源 ID 上；不能做成请求级前置中间件，否则无法与并发出让顺序一致。

## 3. 字段契约

```text
accounts.rpm_limit integer NOT NULL DEFAULT 0
```

- `0`：不限制（与现有 `concurrency_limit <= 0 视为不设限` 的既有语义一致）。
- 正整数：该账户在一个自然分钟内最多成功派发的上游请求次数。
- 账户级总量：跨分组、跨 `text`/`image` lane、跨 API Key 统一计数；授权实例与来源账户共享同一计数。
- 授权实例：配置继承来源账户，被授权用户只读；管理面 PATCH 对授权实例提交 `rpmLimit` 一律 400（与 `concurrencyLimit` 同守卫）。
- 克隆：克隆上下文与创建载荷携带 `rpmLimit`；克隆是配置复制，不复制运行态计数。
- 默认值：新账户 `0`（不限制）。不把 `concurrency_limit` 的 `20` 默认值套用到 RPM，避免对未配置上游配额的账户产生无依据限流。

## 4. 计数口径

### 4.1 计数单位

一次「成功占用账户并发槽并进入上游派发准备」计 1 次。具体包含：

- 首次账户尝试；
- 同账户 API Key 轮换后的重新尝试（每次重取计 1 次，因为每次都会真实出站）；
- 上游 4xx/5xx/传输失败的尝试（上游配额按收到的请求计费，不按成功计费）。

不计：

- 并发已满被拒绝的尝试（未占槽）；
- RPM 已满被拒绝的尝试（未占槽）；
- 网关本地预检/请求体/负缓存等未进入账户派发的拒绝。

边界（刻意保守）：占槽后但在真正出站前被跳过的少见路径（如上游 URL 构造为空）仍消耗 1 次 RPM。该口径保证不超上游配额，代价是极小概率少用一次配额，不引入跨路径退款复杂度。

### 4.2 窗口

自然分钟固定窗口：`bucket = floor(nowMs / 60000)`，窗口结束时刻 `(bucket+1)*60000`。

- 账户计数器保存 `rpmBucket` 与 `rpmCount`；读取或判定时发现 `rpmBucket != 当前 bucket`，视为 `count = 0` 并就地重置。
- Redis 实现使用同一 HASH 的 `rpm_bucket` / `rpm_count` 字段，`PEXPIRE` 沿用并发 acquire 脚本的 TTL（120000ms），窗口滚动后旧字段自然过期。
- 不采用 60 秒滑动窗口：精确滑动窗口需要保存窗口内每次请求时间戳，1000 账户 × 600 RPM 约 60 万条记录（约 10–15MB 及持续清理开销），Redis ZSET 成本更高；固定窗口内存 O(1)，且与多数上游「每分钟」口径一致。

### 4.3 原子判定

`TryAcquire` 扩展为三项判定，全部通过才同时自增：

```text
TryAcquire(accountID, totalLimit, lane, laneLimit, rpmLimit, nowMs):
  lock
  if totalLimit > 0 && total >= totalLimit: reject(total, laneCurrent, rpmCount)
  if laneLimit  > 0 && laneCurrent >= laneLimit: reject(...)
  if rpmLimit   > 0:
      bucket = nowMs / 60000
      if rpmBucket != bucket: rpmBucket, rpmCount = bucket, 0
      if rpmCount >= rpmLimit: reject(..., rpmCurrent=rpmCount, rpmResetAtMs=(bucket+1)*60000)
  total++, laneCurrent++, rpmCount++
  unlock
```

- 拒绝时**不得**自增任何计数。
- `Release` 只回退 `total` 与 lane 计数，**不回退** `rpmCount`（请求已经出站或已占用配额）。
- 返回结构新增 `RPMCurrent`、`RPMLimit`、`RPMResetAtMs`，供错误文案、审计和列表展示使用。
- `MemoryAccountConcurrency` 是生产唯一装配实现，必须先改；`RedisAccountConcurrency` 同步扩展同一 HASH 的 acquire Lua（多两个参数与两个字段），保持契约完整，虽然当前组合根未装配。

### 4.4 预留槽

speed-first 切号预留槽在预留（`TryAcquireAsync`）时消耗 1 次 RPM；未使用的预留槽释放时只退还并发，不退还 RPM。理由：预留本身已经为潜在出站占用配额，且实现保持简单；该路径低频，保守口径不影响正确性。

## 5. 调度语义

### 5.1 候选级

RPM 耗尽与并发耗尽同属「本地容量不可用」：

- `AccountCapacityLimitFailure` 增加 `Kind`（`concurrency` / `rpm`）与 `RetryAfterMs`。
- 失败 attempt 的 `UpstreamURL` 分别为 `concurrency:limit` / `rpm:limit`；错误文案：「账户每分钟请求数已达到上限 X/Y（约 N 秒后重置）」。
- 纯 RPM 拒绝**不进入并发短等重试**（`acquireAccountConcurrencyWithShortRetry` 立即返回，不消耗请求级短等预算）：RPM 计数在窗口内不会因并发释放而下降，逐账户短等只会把延迟累加到请求上；重置等待统一由整池等待（§5.2/§5.3）承担。并发拒绝与「并发 + RPM 双满」仍保留既有短等，因为并发槽可能随时释放。
- 当前候选跳过，继续下一个候选；不写账户数据库状态、冷却或错误策略。

### 5.2 高并发分组

`WaitForCapacity` 的容量模型加入 RPM：

- `highConcurrencyAccountCapacity` 增加 `rpmLimit`；
- ready 判定追加 `rpmLimit <= 0 || rpmUsed < rpmLimit`；
- 输入增加 `AccountRPMLimits map[string]int`，RPM 已用次数/重置时刻由 tracker 实时读（memory）或 HASH 读（Redis）；
- 若本轮全部容量失败均为 RPM 且预算充足，`MaxWaitMs = min(剩余预算, 最近重置时刻 - now)`，把队列等待封顶到最近重置点；到点后由外层 `wholePoolRound` 重新选号，不依赖 release 事件（RPM 重置没有槽释放事件）。

### 5.3 个人分组

个人分组没有分组队列。若本轮容量失败集合**全部**为 RPM：

- 单次等待 `min(最近重置时刻 - now, 剩余请求预算)`，然后重跑整池轮次；
- 预算不足以覆盖到重置点 → 走既有 handoff，返回 429（§5.4）；
- 若失败集合混合了并发与 RPM，保持现有 500ms 级退避重试，因为并发槽可能随时释放，不能盲目长等。

### 5.4 终态

- 个人分组：本轮耗尽且最后一个容量失败为 RPM → `429`，`Retry-After: ceil(RetryAfterMs/1000)`（最小 1、最大 60）；payload `message="上游账户每分钟请求数已达上限，请稍后重试"`、`type/code="rate_limit_exceeded"`；审计 `errorCode="account_rpm_limit"`、`outcome=gateway_failed`、`failureScope="capacity"`。
- 超级分组：RPM 归入 `capacity_exhausted` 聚合分类，沿用现有 429 渲染，不改契约。
- 非 RPM 的并发耗尽：保持现状（个人 503、超级分组 429）。

### 5.5 审计与观测

- 审计 attempt：`errorPhase="dispatch"`、`errorCode="account_rpm_limit"`、`upstreamURL="rpm:limit"`；
- usage attempt：`failureAttribution="gateway_capacity"`、`upstreamURL="rpm:limit"`；
- Prometheus 计数器：`juhe_ai_gateway_account_rpm_limited_total`（无标签，命中一次 RPM 拒绝加 1）；
- `classifyGatewayDispatchExhaustion` 新分类 `account_rpm_exhausted`。

## 6. 读面与前端展示

### 6.1 列表与详情投影

`AccountListItem` / `AccountSummary` / 编辑基础详情 / 克隆上下文增加：

- `rpmLimit`：配置值；
- `rpmCurrent`：本分钟已用次数（授权实例按来源账户 ID 读）；
- `rpmResetAt`：窗口结束时刻（RFC3339，可空）。

列表已有的 `hydrateCurrentConcurrency` 扩展为同时叠加 RPM 运行态；读失败按 0 降级并保留一次 warn。授权实例行的 `rpmLimit` 若 joined 来源账户存在，取来源值（与 `concurrencyLimit` 同规则）。

### 6.2 列表列

「并发数」列改名为「限频」，列宽 100 → 150：

```text
0/5000, 0/2
```

- 单行两个数对：前者是并发 `当前/上限`，后者是每分钟请求 `已用/上限`；未配置每分钟（`rpmLimit=0`）时只显示 `0/5000`。
- 列头「限频」+ 原生 title 说明两个数对：配置了每分钟时显示「并发 0/5000，本分钟已派发 0 次，上限 2 次/分钟，约 22 秒后重置」；未配置时不挂 title（保留 2026-10-23 并发列不挂悬浮气泡的裁决）。
- 点击标签原地切换为一体化控件 `[ 5000 | 2 ] ✓`：两个输入（并发上限 `min=1`、每分钟上限 `min=0`，两者 `max=9999` 即 4 位上限）共用一条边框、竖线分隔、右侧确认按钮；回车或确认一起保存，Esc 或点击外部取消。超过 9999 时保存提示「并发上限最大为 9999」/「每分钟请求上限最大为 9999」。
- 保存走普通账户 PATCH，只提交实际变化的 `concurrencyLimit` / `rpmLimit`；授权账户只读，不提供行内编辑。
- 移动端卡片行标签「限频」，同一编辑器单行展示与编辑。
- 同步更新 `accountTableScrollX` 与列宽 pinned 测试。

### 6.3 表单

- 创建向导/创建基础信息、编辑弹窗调度卡、媒体域段 3：在「并发上限」下方新增「每分钟请求上限」`a-input-number`，`min=0`、`max=9999`（4 位上限）、`precision=0`、placeholder「0 = 不限制」，tooltip 说明「按真实派发到上游的请求计数，每分钟重置；0 表示不限制（0–9999）。达到上限后会先尝试其他账户，全池耗尽时返回 429。」并发上限同步 `min=1`、`max=9999`。
- 授权实例编辑：字段禁用/只读，提交载荷不带该键。
- 表单默认值 `rpmLimit: 0`；基础编辑快照键集、创建/更新/OAuth 公共载荷、克隆装载、详情回填、测试载荷链全部同步。
- PATCH 校验：整数且 `>= 0`，否则 400；授权实例提交即 400（与并发同守卫）。

### 6.4 批量编辑与导入导出

- 批量编辑：新增 `rpmLimit` 三态字段（启用开关 + 数值，`min=0`、`max=9999`），标签「每分钟请求上限」；校验整数 `0..9999`，并发上限校验 `1..9999`。
- 导入协议：`rpmLimit` 可选、非负整数、缺省 `0`；模板 9 个示例账户统一补 `rpmLimit: 0`；字段表和规则说明同步 `docs/functions/AI账户导入协议.md` 与 `frontend/src/views/accounts/accountImportProtocol.ts`。
- 导出：`rpmLimit` 仅在 `>0` 时输出（与 `concurrencyLimit` 同省略规则）；导入缺省落 0。
- 公开 API（`/__aipublic__`）：add/update 白名单、解析（`0..1000000`）与响应 DTO 投影同步；与 `concurrencyLimit` 的暴露面保持一致。

## 7. schema 与升级

### 7.1 DDL

- `pg_schema_business_tables.go` 的 `CREATE TABLE accounts` 在 `concurrency_limit` 后增加 `rpm_limit integer NOT NULL DEFAULT 0`；
- 同文件存量库幂等 ALTER 组增加 `ALTER TABLE accounts ADD COLUMN IF NOT EXISTS rpm_limit integer NOT NULL DEFAULT 0`；
- 账户列表投影脏触发器的 `new_accounts` / `old_accounts` ROW() 比较加入 `rpm_limit`；
- mockdata 账户种子与授权实例种子写 `rpm_limit`（授权实例取来源值）；
- legacybusinessimport 旧库映射读取并写入 `rpm_limit`（旧库无该列时按 0）。

### 7.2 golden 与版本

- schema golden：语句总数 +1、`juhe_business` +1、`alterColumns` +1、digest 变化；按实际断言更新。
- `shared/contracts/business_schema.go`：`accounts` 契约列加入 `rpm_limit`；`BusinessSchemaVersion` `v20` → `v21`（gateway 运行时读写该列，旧库缺列必须被 schemaReady 门拦截）。
- `shared/contracts/project.go`：`SchemaRevision` `13` → `14`。本批为纯 DDL 增量（无新数据步骤），按 [升级兼容与迁移账本设计](升级兼容与迁移账本设计.md) §6.4 占用 revision 14 空号、不入账本；`upgrade` 的加法相自动补列。
- `upgrade_registry_golden_test.go` 的「最高注册 revision == SchemaRevision」断言按 §6.4 修正为「所有注册 revision ≤ SchemaRevision，且末位不超过 SchemaRevision」；纯 DDL 空号不再视为注册表违规。该测试注释同步说明空号规则。
- `docker/single-server/README.md` 与 `docs/deploy/部署指南.md` 增加 revision 14 说明：部署新版前 `upgrade --dry-run` 会因 `dbRevision < 14` 拒绝，必须走 `./deploy.sh upgrade`；回滚旧二进制时新列对旧代码无害（旧代码不读）。

### 7.3 回滚

新列为纯加法、带默认值，旧二进制不读；回滚只需换回旧二进制，不需要删列。运行态 RPM 计数在进程内存，重启清零，不存在数据迁移。

## 8. 实施范围（最小非重叠）

1. **存储与账户读写面**：schema、contracts、mockdata、legacy import、accounts 创建/PATCH/批量/导入导出/克隆、aipublic、authz provisioning、列表/详情投影与测试。
2. **调度热路径**：memory + Redis 并发 store、dispatchsingle、enginehelpers、capacity/queue、upstreamdispatch 等待与终态、审计/usage/metrics 与测试。
3. **前端**：类型、列表双行、编辑器、创建/编辑/媒体表单、批量、导入协议/模板、移动端、回归脚本与单测。
4. **文档**：核心功能设计字段表、高并发分组调度设计配置表与等待语义、接口契约与权限矩阵、批量编辑设计、导入协议、前端 README/样式规范、部署文档、升级账本设计 §6.4 实施注记。

## 9. 验收

1. 字段：新建/编辑/批量/导入/导出/公开 API/克隆/授权实例全部可读可写，`0` 表示不限制，授权实例只读且继承来源值。
2. 原子性：并发满 + RPM 有余时不消耗 RPM；RPM 满 + 并发有余时不占并发；两者都通过时同时 +1，`Release` 只退并发。
3. 调度：单账户 RPM 满时切换下一账户；全池 RPM 满时高并发分组等待到最近重置点、个人分组有界等待后返回 429 + `Retry-After`；非 RPM 并发耗尽终态保持现状。
4. 展示：列表双行（未配置不显示第二行）、编辑/批量/导入表单、移动端和授权实例只读均正确。
5. 升级：fresh 库与存量库 `upgrade` 后 `schema_state.schema_revision=14`、列存在；旧库缺列时 gateway/jobs 启动门禁 fail-closed；旧二进制回滚可用。
6. 回归：schema golden、accounts/aipublic/authz/accountstransfer、gatewaydispatch/gatewayclientip、frontend typecheck + 相关回归脚本全绿。

## 10. 已知边界

- 固定窗口在分钟边界可能出现短时双倍突发（例如 10:59:59 用满 60 次、11:00:00 再用 60 次）；上游仍可能在边界返回 429，走既有错误策略。
- 占槽后未出站的少见跳过路径会消耗 1 次 RPM（保守，不超配额）。
- 生产当前只有 memory tracker；Redis 实现同步扩展但未装配，多副本部署前需另行接线并验证跨节点 RPM 一致性。
- 授权实例的列表 `rpmCurrent` 按来源账户读；现有 `currentConcurrency` 叠加仍按行 ID，二者口径差异在本批不改动 `concurrency` 历史行为。
