# AI 账户特供快速恢复通道设计

> **状态：已实施（2026-10-10），实现与本文档一致；后续行为变更先改本文。** 本文是"特供"能力的唯一设计契约；实现与本文冲突时按本文回正，或先修订本文。上位文档：[AI 账户运行态探针恢复设计](AI账户运行态探针恢复设计.md)（状态机与恢复出口矩阵权威，本文不重复其状态机定义，只叠加特供覆盖档）。修订记录见 §13。
>
> 实现归属（现行）：jobs 冷却复测在 `backend-go/projects/jobs/internal/accounthealth/`（调度决策 `scheduler.go`、PG/SQLite 双 reader `direct_input_reader*.go`、签名输入 `signed_input.go`）；共享探针契约在 `backend-go/shared/platform/accounttest/exactkeyprobe/types.go`；调度 jitter 在 `backend-go/shared/platform/schedulejitter/jitter.go`；账户补丁与管理面在 `backend-go/projects/gateway/internal/accounts/`；系统账户限制在 `backend-go/projects/gateway/internal/authsys/`；schema 在 `backend-go/projects/maintenance/internal/schema/`。

## 1. 背景与目标

部分 AI 账户处于"临期 + 极不稳定"形态：频繁被后台确认落入持久冷却态（`temporary_unavailable`），而现行恢复节奏是全局统一档——中性顺延基准 30 秒、封顶 15 分钟（jitter 后实际约 14.5–15.5 分钟）、持续失败 5 轮后进入 60 秒慢速道、超过最大恢复观察窗（默认 12 小时）降为 1 小时长期道。对存活窗口本来就有限的临期账户，这套节奏意味着"还活着的时间被恢复检测延迟吃掉"。

本文定义一个按账户显式标记的**恢复道加速档**，展示名"特供"：

- **目标**：特供账户在**具备合法冷却 fence 且被归类为冷却复测的持久冷却任务**中，复测更密、候补扫描更靠前（大积压下不被 LIMIT 饿死）、不被短观察终态提前打死；用户显式限流/停用等无 fence 状态不在目标范围内。
- **非目标**：不改变 `/v1` 请求调度、候选资格和路由结果；不降低复活证据标准；不改变普通账户的任何节奏；不加速 gateway 运行态道（`precheck_pending` / `runtime_degraded` / `latency_degraded`，二期另议）；不加速 Key 级恢复；不加速 `pending_test` 激活检查。

## 2. 术语与命名

| 术语 | 值 | 说明 |
| --- | --- | --- |
| 特供 | `expedited recovery` | 账户属性标记，非账户状态 |
| 账户列 | `accounts.expedited_recovery_enabled` | `integer NOT NULL DEFAULT 0 CHECK (IN (0, 1))`，与 `temporary_unavailable_continuous_probe_enabled` 同构 |
| 用户限制列 | `system_accounts.expedited_account_limit` | `integer NULL CHECK (BETWEEN 0 AND 100)`；NULL 语义为默认值 3（读侧 `COALESCE` 归一，不写回） |
| API 字段 | `expeditedRecoveryEnabled` / `expeditedAccountLimit` | 驼峰，与现有补丁字段一致 |
| 共享契约字段 | `Eligibility.ExpeditedRecovery`（JSON `expedited_recovery`） | 随签名输入冻结传播，见 §6 |

## 3. 关键决策与理由（为什么这样做）

每个决策同时给出"选择"和"理由"，复审时逐条对照：

1. **做属性标记，不做新状态。** 现行状态机已有 9 个状态和严格的恢复出口矩阵（《AI 账户运行态探针恢复设计》状态分层表）；新增状态会把状态组合按 2^N 膨胀，且每个状态都必须回答"谁触发、谁恢复"两个问题。特供不改变任何状态的进入/退出语义，只改恢复道参数——工程上与 `temporaryUnavailableContinuousProbeEnabled`（按账户改变恢复行为的既有先例）完全同构。
2. **不降低复活证据标准。** 复活仍然当且仅当匹配来源的 `complete_success`。加速的唯一安全形式是"更频繁地问"，而不是"更低门槛地判活"：降门槛会把真坏号标成 `active` 进入调度，直接伤害真实流量；而加密复测间隔只影响"复活被发现的延迟"，业务可接受。这是本设计的安全底线，不接受放宽。
3. **档位写死一档，不做用户可调参数。** 全局规范"功能不设开关"：暴露参数会制造不可测的参数组合；名额默认 3、可调上限 100（§9），配置上限本身就是爆炸半径控制。特供只有"设了/没设"两态。
4. **只加速 jobs 冷却复测道。** 临时不可调用恢复的延迟瓶颈全部在这条道：15 分钟中性顺延封顶、60 秒失败慢速道、12 小时后 1 小时长期道、未开持续探针的账户 10 分钟即写 `error` 终态。gateway 运行态探针本身分钟级，加速收益小而要动 `RuntimeProbeStateStore` 调度器，改动面与收益不成比例，列为二期候选。
5. **名额默认 3，按系统账户计。** 探针是带账户凭据的真实上游请求，密度必须受控；默认名额是成本基线，不是硬上限，管理员可在 0–100 内调整。上限入口放在"系统账户管理 → 编辑系统账户 → 用户限制"，与既有 `aiAccountLimit` 同区同模式。
6. **不加自动过期 TTL。** 最小改动；特供账户真死会被 7 天观察超时写 `error` 自然停损，账户过期有 `account_expires_at` 硬门，特供标记残留无害。"手动设、手动取消"语义清晰。
7. **特供视同持续探针，绕开有界 10 分钟 `error`。** 有界观察（`boundedCooldownRemaining`）的初衷是控制"未知状态账户"的探针预算；用户显式声明"这个账户值得密集探"后，该预算约束的前提不再成立。否则特供账户反而是最容易被 10 分钟终态打死的账户，与需求直接冲突。
8. **7 天观察超时终态保留。** `cooldown_retest_observation_timeout` 是防无限烧探针的兜底；特供不改变死亡事实，只保证存活窗口被用满。同理 `MaxPauseMinutes` 等其余全局参数不动。
9. **特供优先级只作用于恢复扫描，不作用于调度。** 扫描排序里特供层只插在冷却恢复类内部；`pending_test` 激活类仍然全局第一优先。理由：激活类是"新账户无法使用"的一等阻塞事实，恢复类只是"已知账户暂时不可用"，两者不可互相饿死。
10. **特供标记不进入克隆副本与导出/导入载荷。** 克隆（`clone.go`）不复制该列，导出（`accountstransfer/export.go`）不导出该字段（导出载荷不含 `expeditedRecoveryEnabled` 键）；**导入载荷若包含该键，按既有未知字段策略拒绝该条目**——`import.go:549-557` 对未知键追加"包含未知字段"错误并将该条目标记失败、不落库，不新增"允许但忽略"特例。理由：克隆/导入是批量创建通道，若携带标记将绕过名额校验；特供是工作区本地的运维意图而非账户资源事实，与凭据、模型能力这类必须随迁的资源事实不同类。用户需要时在目标工作区显式重新设置并经名额校验。
11. **名额计数口径有意偏离 `aiAccountLimit`。** 现行 `assertAiAccountCreationLimit` 计数排除授权实例（`authorization_instance_authorization_id IS NULL`），且 `0 = 不限制`。特供两者都不同：**授权实例计入**（实例行被 jobs reader 独立探针与复测，探针爆炸半径按行累积，排除实例会让名额失去意义）；**`0 = 禁止新增/重新启用`**（"不限制特供"违背名额存在的目的；存量标记遵循下调不回溯，不因改成 0 被静默清除）。两处偏离都是有意为之，实施与评审不得"对齐"回 aiAccountLimit 语义。

## 4. 边界契约

| 边界 | 契约 |
| --- | --- |
| 调度边界 | 不改变 `/v1` 请求调度、候选资格和路由结果；只改变 jobs 恢复扫描在冷却类内部的候选排序与恢复探针节奏。`accounts.status`、`schedulable`、账户排序、超级优先、降级备用、会话亲和的任何比特都不受特供影响。 |
| 证据边界 | 复活条件不变：匹配来源 `complete_success`；`framing_complete_neutral` / `unknown` 的语义与现行完全一致（中性只顺延、unknown 不计数不推进）。特供不新增任何"成功"形态。 |
| 状态覆盖边界 | 特供只作用于**已具备合法冷却 fence、且被归类为冷却复测任务的** `temporary_unavailable` / `rate_limited`（`nextDue` 对两类同判据，无合法五元 fence 即不进入复测）。用户显式限流/停用状态没有冷却 fence，本就不进入冷却复测：特供不得使其进入，也不得被无关 transport 复测接管（沿用上位设计的用户显式状态来源保护）。`pending_test` 激活检查（含 24 小时 `account_activation_check_timeout`）、`error` 终态的人工恢复、Key 级冷却复测不加速。 |
| 授权实例边界 | 特供标记打在授权实例自己的 `accounts` 行上，名额计入被授权方系统账户（见 §3.11）；来源账户的特供不传染实例，实例特供不回写来源（与"来源可用性参与 `effectiveAvailability`、运行态不回写"的既有授权边界一致）。 |
| 名额边界 | 默认 3；`expedited_account_limit = 0` 表示该用户**禁止新增/重新启用**特供（语义有意不同于 `aiAccountLimit` 的"0 = 不限制"，见 §3.11）。已有特供标记按"上限下调不回溯"保留，直至用户显式取消；管理员可调范围 0–100。设置特供走"事务 + 锁 + 计数"校验（§9）；取消特供无条件允许、不校验。 |
| 展示边界 | 特供徽章只表达"恢复道加速"，不表达账户健康、可用性或调度优先级；前端不得把特供渲染成"优先调度"或"状态正常"。管理员下调名额造成存量超限时，列表必须可见地展示"已超限"（§9）。 |
| 观察边界 | 特供改变的是调度节奏与优先级，全部参数可从代码与日志核对；审计日志必须能区分"用户开了持续探针"与"特供视同持续探针"两种来源。 |
| 时间语义边界 | 本文所有间隔均为**基准值**，实际生效值 = 基准值 + 全局被动 jitter（`schedulejitter.Delay`：<1 分钟间隔窗口 ±interval/2，1 分钟–1 小时窗口 ±30 秒，且恒不等于基准值）。本文不引入特供专属 jitter 策略；量化承诺一律按"基准值（jitter 后区间）"表述。 |

## 5. 行为契约：特供档位表

现状参数均见 `backend-go/projects/jobs/internal/accounthealth/scheduler.go` 与 `direct_input_reader.go`；特供在每个账户的探针输入装配点生效，契约见 §6。

| 行为点 | 现行（全部账户） | 特供档位 | 实现落点 | 理由 |
| --- | --- | --- | --- | --- |
| 中性顺延封顶 | 30s 基数倍增，封顶 15 分钟（jitter 后 14.5–15.5 分钟） | 基准封顶 **60s**（jitter 后 30–90s） | per-account 覆盖 `Schedule.CooldownNeutralMaxMS`（reader 装配点） | 主收益：复活检测最坏延迟 15 分钟 → 90 秒 |
| 失败慢速道 | 连续失败 >5 轮后固定 60s（jitter 后 30–90s） | 基准 **15s**（jitter 后 7.5–22.5s） | `cooldownSlowRetryDelay` 按 `Eligibility.ExpeditedRecovery` 分支 | 持续失败的特供号保持高压复测，不因失败次数而松弛 |
| 长期降频道 | 超过最大恢复观察窗（设置 `cooldownAccountRetestMaxBackoffHours`，默认 12h）降为 1 小时/轮（jitter 后 30–90 分钟） | **不降频**，保持特供节奏 | `applyCooldownDecision` 长期道分支按 `ExpeditedRecovery` 跳过 | 12 小时降频对临期账户无意义；探针密度已有名额约束兜底 |
| 有界观察 10 分钟 `error` | 未开持续探针的 `temporary_unavailable` 超过 10 分钟有界观察写 `cooldown_retest_limited_probe_timeout` | **视同持续探针**，不适用 | `boundedCooldownRemaining` 判定追加 `ExpeditedRecovery` | 见 §3.7 |
| 7 天观察超时 `error` | 写 `cooldown_retest_observation_timeout` | **保留** | 不改 | 防无限烧探针的停损（§3.8） |
| 失败初始退避 | 3s 基准（jitter 后 1.5–4.5s） | 不变 | 不改 | 首轮复测已经够快 |
| 失败指数退避 | 3s 起每轮 ×2 | 不变 | 不改 | 特供只改封顶与慢速道 |
| 中性顺延基数 | 30s 基准（jitter 后 15–45s） | 不变 | 不改 | 30s 起步已足够快，收益全在封顶 |
| 候补扫描排序 | 冷却类内按 `cooldown_until` ASC | 冷却类内特供账户**最优先层**，层内仍按 `cooldown_until` ASC | `directInputCandidatesSQL` ORDER BY（PG/SQLite 双 reader 同步） | 大积压 + LIMIT 下特供不被饿死；名额大小不改变排序规则，普通账户受影响的边界与实际候选数见 §10 第 5 项 |
| `pending_test` 激活 | 全局第一优先类，5 分钟失败重试，24h 终态 | 不变 | 不改 | §3.9 |
| active 常规巡检 | 全局设置（默认 1 小时） | 不变 | 不覆盖 `HealthIntervalMS` | 特供管"恢复快"，不管"巡检勤"，控制探针成本 |
| jitter | 全局被动 jitter | 不变 | 不改 | §4 时间语义边界；特供账户 due 由各自冷却时点天然错开，全局被动 jitter 已提供非零偏移——该决策与名额数无关，不因名额调大而改变 |

**量化效果**（含 jitter 上下界）：特供账户复活检测最坏延迟 ≈ **90 秒**（中性封顶基准 60s + 上界 jitter 30s），持续失败高压复测 7.5–22.5 秒/轮；普通账户维持现行全部节奏，逐字节不变。

## 6. jobs / shared 输入契约（新增字段与传播链）

现行 `exactkeyprobe.Input` 是签名序列化传播的任务契约（`signed_input.go` 序列化 → worker `VerifySignedInput` 反序列化）；`Schedule` 随输入版本冻结，`Eligibility` 是随输入冻结的业务状态快照。特供沿既有形态扩展，共两处 shared 字段变更：

1. **`Eligibility.ExpeditedRecovery bool`，JSON `expedited_recovery,omitempty`**（`exactkeyprobe/types.go`）。消费点三处，全在 `scheduler.go`：慢速道 15s 分支、长期道跳过、有界观察绕开。放在 `Eligibility` 而非 `Schedule` 的理由：它是"该账户是什么"的事实布尔（与 `TemporaryUnavailableContinuousProbeEnabled *bool` 同类），而行为数值继续走 `Schedule`。
2. **`Schedule.CooldownNeutralMaxMS` 按账户覆盖**，不新增字段：reader 装配输入时，特供账户写 `60_000`，普通账户维持现行 `900_000`。`Schedule` 随输入版本冻结的既有语义仍成立；对账户行配置发生变更的输入，先由 §6.3 新鲜度门拒绝旧输入，再由下一轮 reader 生成新档位，避免旧配置在门后继续发出探针。
3. **执行前新鲜度门（新增通用机制，本设计的动机案例是特供）。** 现行签名输入默认 TTL 为 24 小时（`JUHE_AI_ACCOUNT_HEALTH_INPUT_TTL_MS`，`config.go` 默认 24h、钳制 1 分钟–7 天），`validateScheduledInput` 只校验 `ExpiresAt`——特供开关变更后，已签发的旧输入在 TTL 内仍会通过校验并照常发出**旧档位**上游探针，只是结果在写回时被 fence 拒绝。

   - 门必须落在两条真实 dispatch 路径的 `ExecuteInputProbe` 之前：定时输入的 `prepareScheduledInput`，以及显式探活请求的 `runExplicitRequest`。两者都在签名验证与 `validateScheduledInput` 通过后，按 `input.AccountID` 重读 `accounts.config_revision` 单列，与 `input.ConfigRevision` 不一致即拒绝上游调用。
   - 对定时输入，拒绝结果不写回 `account_health_current_state`、不改变账户失败计数、不推进退避，记录 `input_stale_before_dispatch` 诊断事件；下一轮由数据库 reader 重新装配。对显式请求，允许沿用现有请求终态落库以消费该请求，但同样不得写回账户健康状态或失败计数。
   - **作用域**仅是 `accounts` 行配置（包括特供标记）；它不覆盖全局调度设置或其他未进入 `config_revision` 的配置。若将来要求全局设置变更也立即失效，必须把对应 settings revision 纳入签名输入并纳入同一门，不能把本门的账户行复核当作全局保证。
   - **线性化点与竞态边界**：门的线性化点是 freshness SELECT 成功返回后、构造上游请求前的最后一次判定。补丁事务在该点之前提交，必须拒绝旧输入；补丁在该点之后提交，可能与上游请求并发，允许请求已发出，但写回仍须由现有 fence 拒绝。若产品要求“任意补丁提交后绝不发出旧请求”的更强保证，需要账户级 dispatch lease/锁，超出本设计范围。
   - **读失败或账户不存在按失败关闭**：不能把数据库错误当作新鲜，也不能继续上游请求；记录 `input_freshness_unavailable`，不写回账户状态、不计失败。数据库 reader 模式下当前输入结束并由下一轮重新装配；文件输入源是只读的，jobs 不得擅自删除发布方文件，必须由发布方替换为新签名输入；在替换前每轮都应拒绝上游调用并对诊断限频。该结果是本任务已处理的 skip/terminal，不得作为本轮首个错误阻断其余账户，也不得把 freshness 拒绝当作可重试的上游失败。
   - 该门是所有账户行配置变更的通用保护，非特供专属；成本为每任务一次主键单列 SELECT，相对一次真实上游探针可忽略。
   - **实施落定（2026-10-10）**：① 新鲜度查询落在 PG/SQLite 两个直读 reader 的 `LoadAccountConfigRevision`（业务库连接面），由 `NewRunnerWithDirectInputReader` 注入 Runner——不落在 jobs Store（SQLite 模式下 jobs Store 与业务库是两个物理文件，PG 模式下 jobs Store 对业务库无读权限），不改变上文门的位置与拒绝语义；② files 文件输入后备通道的组合根不装配业务读面，**门未装配即跳过**——该通道保留探活能力，陈旧输入的上界由签名输入 24h TTL 兜底，"文件输入源不得删除发布方文件"的契约语义不变。

**传播链（实施必须逐环落位）**：

```text
PG reader    directInputCandidatesSQL 增列 a.expedited_recovery_enabled
SQLite reader 同名同列同步（direct_input_reader_sqlite.go）
  → scanDirectCandidate / directCandidate 增字段
  → 输入装配：Eligibility.ExpeditedRecovery = 列值；
              Schedule.CooldownNeutralMaxMS = 特供 ? 60_000 : 900_000
  → signed input JSON（expedited_recovery 随 Eligibility 自动序列化）
  → worker VerifySignedInput 反序列化
  → 执行前新鲜度门（config_revision 复核，§6.3）
  → scheduler 三处消费（慢速道 / 长期道 / 有界绕开）
```

单一 compose 形态 gateway/jobs 同包发布，无跨版本 skew 窗口；签名输入字段为加法变更，新旧输入可在同一发布窗口共存。排障字段（错误码、诊断、`account_health_cooldown_*` 日志）需携带 `expedited` 维度以便区分来源。

## 7. 存储与 schema 变更

1. **PG**（`pg_schema_business_tables.go`）：
   - `accounts` 建表 DDL 增 `expedited_recovery_enabled integer NOT NULL DEFAULT 0 CHECK (expedited_recovery_enabled IN (0, 1))`（照 `temporary_unavailable_continuous_probe_enabled` 同位置）；
   - **`new_accounts` / `old_accounts` 触发器 ROW 投影两处清单同步加列**（该 ROW `IS DISTINCT FROM` 比较是 accounts 变更检测投影，漏加任一侧会导致该列变更不触发投影）；
   - **`accounts` 既有库守卫（BUG-0305 发布事故修正；v3.1/v3.2 本条曾错误省略）**：`ALTER TABLE accounts ADD COLUMN IF NOT EXISTS expedited_recovery_enabled integer NOT NULL DEFAULT 0 CHECK (expedited_recovery_enabled IN (0, 1))`——CREATE TABLE 只救新库，存量 PG 库（生产与开发主库）缺它时新二进制启动契约校验缺列 fail-closed 崩溃循环；"probe 列无 ALTER"的先例不成立（该列先于存量库存在，新列没有这个前提）。
   - `system_accounts` 建表 DDL 同步增加 `expedited_account_limit integer CHECK (expedited_account_limit BETWEEN 0 AND 100)`，既有库再照 `ai_account_limit` 模式执行 `ALTER TABLE ... ADD COLUMN IF NOT EXISTS`。
2. **SQLite**（`sqlite_schema_business.go` + `sqlite_schema.go`）：
   - 新库：两表 CREATE TABLE DDL 同步加列；
   - **既有库：新增 ensure 守卫**（照 `ensureSystemAccountAiAccountLimitSchema` 的 PRAGMA table_info 早退模式）：`ensureAccountExpeditedRecoverySchema`（accounts 列）与 `ensureSystemAccountExpeditedAccountLimitSchema`（system_accounts 列），挂入 `EnsureSQLiteBusiness` 升级路径。只改 fresh DDL 不迁移既有库是阻断级错误。
3. 两列进 contracts 契约清单与 bootstrap 守卫测试（加列后守卫早退的幂等断言）。
4. maintenance bootstrap 加法幂等，随 `--ensure-schema` 生效，**无独立迁移命令、无新增环境变量**。

## 8. API 与交互

**账户侧（`PATCH /api/accounts/{id}`，照 `temporaryUnavailableContinuousProbeEnabled` 全链路）**：

1. 请求：`patchBody` 严格白名单增 `expeditedRecoveryEnabled`（bool）；`expectedConfigRevision` 乐观锁照旧必填。账户创建通道（`createBody` 白名单，routes.go:900 一族）同步接受该字段并执行同一名额校验。
2. 响应：列表与详情投影（`m11_reads.go` 一族）增 `expeditedRecoveryEnabled`；列表按归属人上限与当前计数派生"已超限"展示位（§9）。
3. 权限：沿用账户编辑权限面（归属人或 `CanAccessAll` 管理员），不新增权限点。
4. 写入语义：字段变更随补丁事务无条件 `config_revision = config_revision + 1` 与 `updated_at`（patch.go:1216 既有行为）；**不进入 `accountPatchGatewayRuntimeFields`**——特供的消费方是 jobs 恢复道，无需 gateway 运行态失效；进行中复测的迟到结果因账户 `ConfigRevision`（授权实例另含来源 `SourceConfigRevision`）CAS 校验按"未变更"返回，下一轮候选扫描从实时行值重建输入即按新档位执行（验证项 §11.4 锁定该断言，不要求 bump `account_health_jobs_input_versions`——该版本表是输入代次，由 create/delete 维护，与配置变更传播无关）。
5. 审计：变更进既有补丁审计与 `PatchChange` 记录；账户侧错误沿用 `ValidationError` 并由 accounts 路由返回 HTTP 400，至少区分“名额已满”和“值不在 0–100”的消息/日志原因（内部可使用 `expedited_account_limit_reached` / `expedited_account_limit_invalid`），不得返回成功或静默降级为不设置。
6. **克隆 / 导出 / 导入**：一律不携带该字段（§3.10）。克隆副本、导入创建的账户恒为未标记；导出载荷不含 `expeditedRecoveryEnabled` 键。
7. **批量编辑**：当前 `accountstransfer` batch-edit 字段白名单不加入该字段；载荷若提交该字段按既有“不支持字段”错误拒绝，不得把逐账户名额校验隐式扩展成批量绕过。若未来要开放批量设置，必须在同一系统账户锁内按整批启用增量一次性校验并定义部分成功/全量回滚契约。

**系统账户侧（编辑系统账户，`authsys` 一族，照 `aiAccountLimit`）**：

1. 请求/响应：`expeditedAccountLimit`（响应为 `int|null`；请求采用三态：字段缺省=不修改，显式 `null`=清除覆盖值并恢复 NULL/默认 3，整数=设置 0–100；值为 0 时禁止新增/重新启用，存量标记不回溯清除）；store `store_accounts.go` 读写列、owner_gate 投影列同步。
2. 校验：非空时必须为 0–100 整数；审计与 `updated_at` 沿用系统账户补丁既有链路。
3. 前端：系统账户编辑弹窗"用户限制"区增"特供账户上限"输入框，占位说明"默认 3，0 = 禁止新增/重新启用"。
4. 账户列表：行操作"设为特供 / 取消特供"；特供徽章；状态卡片在特供账户上展示加速节奏摘要（例如"特供恢复：中性封顶基准 1 分钟，实际 30–90 秒"），不得表述为调度优先级。

## 9. 名额校验与并发（精确契约）

- **计数口径**（有意偏离 `aiAccountLimit`，见 §3.11）：

```sql
SELECT COUNT(*) FROM accounts
WHERE system_account_id = ?        -- 归属系统账户（授权实例行的归属即被授权方）
  AND deleted_at IS NULL           -- 软删不计
  AND expedited_recovery_enabled = 1
-- 不过滤 status：停用/异常行仍占名额（标记是显式运维意图，
-- 按 status 过滤会被"先停用再新设"绕过）
```

- **校验时序**：设置 `expeditedRecoveryEnabled = true` 的补丁事务内，先读归属行并加写锁（PG `LIMIT 1 FOR UPDATE`，照 `assertAiAccountCreationLimit` 的 `s.forUpdate()` 双方言模式；SQLite 经单写者事务天然串行），再执行上述计数；`COALESCE(expedited_account_limit, 3)` 为上限，`0` 直接拒绝新增/重新启用；计数 ≥ 上限时整体回滚并返回上限错误。创建通道必须在同一事务内计入本次待插入行（或等价地使用 `当前计数 + 本次请求增量`），不得先校验后插入造成超卖；已是特供的补丁幂等更新不重复占用名额。
- **上限下调不回溯**：管理员下调后，已设特供的账户保留标记，只是不能再新增；当上限为 `0` 时也按此规则处理，表示禁止新增/重新启用，不强制清除存量标记。**列表必须可见地展示"已超限"状态**（归属人上限 < 当前特供计数时，账户列表对双方都展示超限标记），不得静默。
- 取消特供不校验、无条件允许。

## 10. 风险与取舍

1. **上游风控**：特供号中性顺延基准 60 秒；**持续失败时进入慢速道基准 15 秒（jitter 后 7.5–22.5 秒，单账户理论上界约 11,520 次探针/天）**。探针成本与风控压力必须按当前名额 N 评估（N 为该系统账户 `COALESCE(expedited_account_limit, 3)`，可配置到 100）：默认 N=3 时全池最坏约 3.5 万次/天（最小请求、走账户自有凭据）；N 调大则上界随 N 线性增长，调大名额即代表接受相应上游压力。文档与界面文案不承诺"特供能救活"，只承诺"复活会被最快发现"。
2. **临期终局**：特供不阻止死亡；7 天观察超时仍写 `error` 停损。特供的价值区间是"账户尚能复活的窗口期"。
3. **探针成本**：默认名额 3、中性路径下对探针池与 DB service 窗口可忽略；持续失败路径与更大名额按本节第 1 条的 N 上界评估，不由实现或文档宣称"可忽略"。约 11,520 次/天/账户是没有并发、限速、租约和上游拒绝等其他预算时的**策略路径理论最密上界**，实际吞吐只能更低；N=100 时单池理论上界约 1,152,000 次/天。
4. **误判不变性**：因证据标准未降，特供不会让坏号进入调度；加速的全部代价是探针密度。
5. **普通账户公平性**：扫描排序特供层只影响冷却类内部次序，类间槽位份额机制保留；普通账户的节奏参数逐字节不变（回归测试锁定）。大名额下（极端 N=100），普通账户可能因当轮实际入选的特供候选而延后出本轮扫描；设计上受影响幅度与该轮特供候选数同阶，不能把配置上限 N 直接当成每轮必后移的精确值，具体边界由 PG/SQLite 同 SQL 回归锁定。

## 11. 验证要求

实现时至少覆盖（Mock 优先，双方言）：

1. jobs `accounthealth` 单测：特供分支的中性封顶基准 60s、慢速道 15s、长期道跳过、有界 10 分钟 `error` 绕开；**普通账户节奏逐字节不变的回归**（中性 15 分钟封顶、60 秒慢速道、1 小时长期道仍生效）；jitter 上下界断言按 §4 换算（60s → [30s, 90s]，15s → [7.5s, 22.5s]，且恒不等于基准值）。
2. 7 天观察超时对特供同样生效的终态测试。
3. reader：PG 与 SQLite 双回归——新列读取、`Eligibility.ExpeditedRecovery` / `CooldownNeutralMaxMS` 装配正确；排序特供在冷却类内优先、激活类仍全局第一优先。
4. **特供变更传播**：补丁后下一轮扫描重建的输入携带新档位与新的账户 `ConfigRevision`；只有授权实例且存在来源账户时，相关来源版本才填入 `SourceConfigRevision`。旧代次输入的迟到结果按 fence 校验返回未变更。执行前新鲜度门（§6.3）必须覆盖“补丁在 freshness 判定前提交”这一时序（Mock 观测零上游请求、零状态写回、失败计数不变）；补丁在判定后提交的竞态允许请求已发出，但必须验证写回 fence 拒绝，不能把该时序写成绝对的零上游请求保证。
5. 签名输入回路：`expedited_recovery` 随 Eligibility 序列化 → `VerifySignedInput` 反序列化无损。
6. gateway 补丁：默认上限 3、管理员调整生效、显式 `null` 清除覆盖值、`0` 禁止新增/重新启用、超限文案与事务回滚、并发双写不超卖（PG `FOR UPDATE` 语义真实驱动验证，SQLite 单写者等价）、取消不校验、上限下调后列表"已超限"展示；审计与 `config_revision` 断言。
7. 克隆 / 导出 / 导入：克隆产物与导出载荷恒不含该标记；导入载荷含 `expeditedRecoveryEnabled` 键时按既有未知字段策略将**该条目标记失败且不落库**（`import.go` 未知键错误路径），不得新增"忽略未知键"特例。
8. schema：PG 建表 DDL + `new/old_accounts` 投影 + system_accounts `ADD COLUMN IF NOT EXISTS`；SQLite fresh DDL + 两个 ensure 守卫（既有库无列 → 加列，已有列 → 早退）；contracts 清单与守卫测试同步。
9. Mock AI 覆盖全链路：特供账户失败 → 持续不可用 → 复活，复活发现延迟显著小于普通账户；授权实例特供与来源账户隔离、实例计入被授权方名额。
10. 人工测试（账户测试按钮）对特供状态零副作用，与现行一致。
11. 新鲜度门专项：定时 `prepareScheduledInput` 与显式 `runExplicitRequest` 都在 `ExecuteInputProbe` 前执行门；补丁在门前提交时验证 `input_stale_before_dispatch`（零上游请求、零账户状态写回、失败计数不变；显式请求只允许写入自身终态以消费请求）；补丁在门后提交时验证允许请求竞态但 fence 拒绝账户状态写回；数据库读失败或账户不存在时按失败关闭，文件输入源不删除发布方文件、在替换前持续拒绝上游且诊断限频。
12. 批量编辑：提交 `expeditedRecoveryEnabled` 时按既有不支持字段错误拒绝；若将来开放，验证同一系统账户锁内按整批启用增量校验，且全量回滚/部分成功语义明确。

## 12. 文档同步清单（实现交付内完成）

| 文档 | 同步内容 |
| --- | --- |
| `docs/architecture/架构总览.md` | 账户运行态/恢复段落补特供一句（属性标记、只改变 jobs 恢复扫描排序与节奏，不改变 `/v1` 调度） |
| `docs/functions/AI账户运行态探针恢复设计.md` | 持久状态恢复/冷却复测段补特供覆盖档引用与来源区分 |
| `docs/functions/账号健康检测设计.md` | 探针节奏参数表补特供档 |
| `docs/functions/接口契约与权限矩阵.md` | 两个 API 字段、错误语义、权限面 |
| `docs/architecture/功能开发指导.md` | 字段与写入契约 |
| `docs/functions/SQLite存储说明.md` 与 schema 契约文档 | 双方言列与 ensure 守卫 |
| `docs/architecture/frontend/README.md` | 列表操作、徽章、已超限展示、系统账户限制输入 |
| `docs/functions/README.md` | 权威功能索引、当前设计稿版本与“未实施/待终审”状态 |
| `docker/single-server/README.md`、`docs/deploy/部署指南.md` | schema 加列随 ensure-schema 生效，无额外命令、无新环境变量 |
| `backend-go/README.md` 或 jobs 执行契约文档 | 执行前 `config_revision` 新鲜度门的作用域、竞态线性化点、失败关闭与租约结束语义 |

## 13. 修订记录

- **v3.3（2026-10-10，BUG-0305 发布事故修正）**：§7.1 补 `accounts.expedited_recovery_enabled` 的 PG 既有库 ALTER 守卫——v3.1/v3.2 契约错误省略该守卫（误引"probe 列无 ALTER"先例，该列先于存量库存在而新列不是），2026-10-10 发布窗口生产与 jobs 双双因缺列 fail-closed 崩溃循环约 11 分钟，手工 ALTER 恢复；代码守卫 + golden 706 + 守卫测试断言随本版入库。详 docs/bug/问题-0305。
- **v3.2（2026-10-10，实施落定）**：功能已全部实施并测试通过，状态头改为"已实施"。① contracts business SQLite schema 版本 v13→v14（`accounts.expedited_recovery_enabled`、`system_accounts.expedited_account_limit` 双方言加列，随 `--ensure-schema` 生效）；② 实施落定澄清两处（§6.3）：新鲜度门查询落在 PG/SQLite 直读 reader 的 `LoadAccountConfigRevision`（业务库连接面）并由 `NewRunnerWithDirectInputReader` 注入 Runner、不落 jobs Store；files 文件输入后备通道组合根不装配业务读面，门未装配即跳过，陈旧输入上界由签名输入 24h TTL 兜底；③ 验证结果摘要：jobs accounthealth 全量 ok（含 8 项 PG 门禁）、gateway accounts/authsys ok、contracts ok、maintenance schema ok、前端 vue-tsc/build/960 项单测 ok。
- **v3.1（2026-10-10，终审补充）**：① 明确执行前新鲜度门只覆盖 `accounts.config_revision`，补上全局 settings 不在覆盖范围内的边界；② 定义 freshness 判定的线性化点与补丁竞态，避免把“零旧请求”误写成无法实现的绝对保证；③ 补充数据库读失败/账户消失的失败关闭、旧签名输入不得原样重试与诊断语义；④ 补齐 PG `system_accounts` 建表 DDL、创建事务请求增量、幂等补丁不重复占额、系统账户限制字段的 `null` 三态；⑤ 修正 `SourceConfigRevision` 仅适用于授权实例的验收措辞；⑥ 将探针次数明确为无其他预算时的理论最密上界，并把普通账户后移改成需由双 SQL 回归锁定的实际候选数边界；⑦ 增加新鲜度门竞态与失败关闭验收项；⑧ 明确 batch-edit 不携带特供字段，未来批量开放必须按整批增量校验；README 索引同步。
- **v3（2026-10-10，按第二轮外部审核修正）**：① 导入语义统一为"未知字段策略拒绝该条目、不落库"（核实 `import.go:549-557` 未知键错误路径），删除"忽略"表述（§3.10、§11.7）；② 探针成本上界修正：持续失败慢速道基准 15s（jitter 7.5–22.5s，单账户理论上界 ≈11,520 次/天），成本按当前名额 N 评估，删除"每天 ≤1440 次 / 3 个特供"的错误表述（§10）；③ 名额表述统一为"默认 3、可调上限 100"（§3.3、§5、§10），如实写明大名额下冷却类内普通账户名次后移的预期代价，jitter 决策理由改为与名额数无关（§5）；④ 新增**执行前新鲜度门**（§6.3）：核实签名输入默认 TTL 24h（`validateScheduledInput` 只查 `ExpiresAt`），旧输入在 TTL 内可照常发出旧档位探针——新增执行前 `config_revision` 复核，不匹配即在发起上游请求前拒绝；§11.4 验收增强为"零上游请求、零写回"断言；⑤ §4 状态覆盖边界收窄：特供只适用于已具合法冷却 fence 的冷却复测任务，用户显式 `rate_limited` 沿用上位设计来源保护，特供不得使其进入复测。
- **v2（2026-10-10，按外部审核意见修订）**：① 新增 §6 shared 输入契约（`Eligibility.ExpeditedRecovery` 字段、`Schedule.CooldownNeutralMaxMS` 按账户覆盖、PG/SQLite reader → 签名序列化 → scheduler 三消费点的完整传播链）；② 全部时间承诺改为"基准值 + jitter 上下界"（§4 时间语义边界、§5 表、§5 量化效果：复活检测最坏 90 秒），不引入特供专属 jitter；③ §7 补 SQLite 既有库 ensure 守卫与 PG `new/old_accounts` 触发器 ROW 投影同源要求；④ §8 补完整 API 链路（请求/响应投影、权限、审计、`config_revision` 语义、`account_health_jobs_input_versions` 不参与的论证）与克隆/导出/导入不携带标记的契约（§3.10）；⑤ §9 名额计数精确 SQL 与口径裁决（授权实例计入、status 不过滤、`0 = 禁用`，显式偏离 `aiAccountLimit` 的论证见 §3.11）；⑥ §12 文档同步清单扩为 8 项（含 frontend README、账号健康检测、SQLite 存储说明）；⑦ 调度边界表述按审核建议收窄为"只改变 jobs 恢复扫描在冷却类内部的候选排序"；⑧ 上限下调后列表强制展示"已超限"。
- **v1（2026-10-10）**：初稿。
