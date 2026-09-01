# J3b 模型检测完整迁移契约

> 冻结日期：2026-08-26。
> 状态：L1 完整边界已按当前工作树重新冻结；Gateway 已形成 J3b 专属 Store、run/item/observation、JSON/SSE、durable scheduler、quality health retry 和 fail-closed Host 基线，jobs 对 J3b 启用配置硬失败。当前本地/dev 复核显示核心 Node 运行入口已拆离、J3b 归档清单已可校验、active-path 生产源码扫描为零；但本文件不是切换授权，且详细 token/identity observation、独立 stream probe、Juice 请求随机化/协议 gate、真实三库回填与 GitOps/owner handoff 仍未形成完整语义或外部证据。历史 W7 单体 Go 实现和 Goose catalog 已由 `133cd4e48 (go del)` 删除，不能作为当前可复用实现或验收依据。文中早期“尚未接入主进程”的段落属于阶段性记录，以本状态行和后文实际装配说明为准。

Node active-path 证据由 `juhe-ai-maintenance -scan-node-j3b-active-path` 生成。历史候选复扫曾报告 `scannedFiles=968`、`blockedFindings=161`（见 9.7.35），该数字不能与归档后的当前结果混用；当前生产源码扫描为 `scannedFiles=912`、`blockedFindings=0`，但 scanner 明确跳过 `scripts/regression` 与 `maintenance/mockdata` evidence-only 目录，因此不等价于所有脚本、fixture、DDL 和配置残留均已清除。

## 1. 完整功能边界

J3b 是“模型检测与其直接质量结果投影”，不是一个独立的 HTTP probe。一次完整接管必须由单一 Go J3b owner 直接承担以下所有部分：

- 管理端 `POST /__aisys__/api/model-checks/run` 与 `POST /__aisys__/api/model-checks/run/stream` 的鉴权、参数验证、JSON/SSE 响应、客户端取消、活动任务冲突和进度事件；
- `manual`、`scheduled`、`quality_recovery` 三种 trigger 的输入快照、资格判定、账号/协议 profile/模型映射解析、凭据解密、直接上游请求、取消和错误分类；
- quick/full probe suite、trusted comparison、token integrity、identity observation、structured/tool/long-context/distribution probe，以及现有评分、可信度和 `modelCheckUnverified` 语义；
- `model_check_runs`、`model_check_items`、model trust observation、trust aggregation cursor/receipt/latest result 及质量决策的幂等持久化、管理读 API 与依赖该 receipt 的 retention；
- 与本次检测直接相关的账户质量处罚、质量隔离恢复、health-hour failure sync 和 health-sync retry；
- `model-quality-scheduled-check`、`model-quality-recovery`、`model-quality-health-sync-retry` 的周期发现、lease、取消、恢复和运行态观测。

以下不属于 J3b，不能借本批改变：`account-quality-refresh` 的 usage 统计重算、`account-quality-failure-precheck-queue` 的账户失败前置确认、普通账号探活、网关请求调度、账户 CRUD、provider/OAuth 业务功能，以及 F1-F6 日志/usage owner。J3b 只处理由模型检测产生的 trust/quality 事实及其直接投影；J3c 仍拥有 usage 统计刷新和失败前置确认。它们与 J3b 共用部分数据表或配置，不构成可保留 Node 写回的理由。

## 2. Node L1 事实与归档范围

当前 Node 功能由下列活跃路径共同实现；L4 前必须全部从活跃目录移除并按原路径归档到 `migration-backup/node/j3b-model-check/`。共享文件若仍承载未迁功能，必须先完成无行为变化的职责拆分，不能归档其中一段。

| 域 | 当前 Node 事实 | Go L2/L4 要求 |
| --- | --- | --- |
| HTTP 与 SSE | `backend/src/modules/model-checks/model-checks.routes.ts` 提供 JSON、SSE、`connected`、10 秒 heartbeat、progress、complete/error、停止与 active-run 查询 | Go J3b owner 的管理 listener 直接提供等价路径、管理员鉴权和 SSE；不经过 Node HTTP/IPC 转发 |
| 主编排 | `model-checks.service.ts` 解析 target、创建 run、执行 probe、完成 run、生成质量决策 | 以 Go 领域服务完整替代；不得逐行翻译或调用 Node service |
| 解析/协议与 probe | `model-checks.{profiles,payloads,probes,evaluation,parsing,response-parsing,provider-capabilities,gateway-probe,probe-retry}.ts` | 逐项固定请求、响应、重试、评分和包装 HTTP/上游 HTTP 的差异 |
| 高级 probe | `model-checks-{token-integrity,token-probes,identity-features,observation-security,gpt56-juice,trust-report}.ts` | 不能因 Go 首版省略 token/identity/trust 结果；每项需 golden 或 Node oracle |
| 手动生命周期 | `model-checks-active-runs.ts` 与 `diagnostic-task-limiter.ts` | Go 以请求/账号粒度的可取消活动 run 管理替代；不继承 Node 单事件循环低并发闸门 |
| 周期任务 | `background-jobs.ts`、`model-quality-scheduled-check.service.ts`、background registry | Go J3b owner 独占三类 scheduler、lease、恢复和 health；Node scheduler/worker registry 路径清零 |
| 业务写入 | `model-checks.repository.ts`、`model-trust.repository.ts`、`model-quality*.repository.ts`、`background-dataset-writer.ts`、DB-service 命令 | Go 直接在 PostgreSQL 事务中完成 version/fence/CAS；Go 不调用 Node DB-service、IPC 或 HTTP |
| 配置与读 API | `model-checks.routes.ts` 的 quality policy/schedule/run/read 路由及其调用的 repository | Go 直接管理同一 API 资源；Node 不保留 adapter、fallback reader 或 writer |
| 启停、指标与测试 | `server.ts`、`worker.ts`、background registry、相关 model-check regression | Go health/metrics/Graceful shutdown 接管；Node 专属 runtime 与回归入口归档 |

现有周期注册事实：`model-quality-scheduled-check` 和 `model-quality-recovery` 均为 1 分钟周期、20 分钟 task timeout；`model-quality-health-sync-retry` 为 1 分钟周期、2 分钟 task timeout。它们是 Node oracle，不是 Go 并发或调度参数的直接复制依据。Go 必须保留 lease、取消、重放、fence 和可观察失败，但不得为复制 Node scheduler lane 或小并发而引入人为吞吐限制。

## 3. 外部行为冻结

1. 手动请求只接受账户目标、完整模型 ID、`quick|full` profile 和可选 trusted comparison；缺 target、模型、profile、权限、账号状态或比较账号不合法时保持当前 4xx/409/503 语义。
2. JSON 手动 run 与 SSE run 都必须支持客户端断开/显式 stop。SSE 必须先发送 `: connected`，每 10 秒发送 `: heartbeat`，并保持 progress/complete/error 的事件结构。下游不可写时取消上游工作，不得继续执行未被客户端消费的诊断。
3. 不设置短的总 HTTP deadline。现有 15 分钟 `chatTimeoutMs` 是上游 socket idle timeout；Go 仍须区分超时、代理失败、包装 HTTP 状态和上游非 200，不得把它们折叠为一个“检测失败”。
4. token integrity 两次非 200 会停止该 token probe 的后续 padding/round，但不能提前终止其他非 token probe。任何一个 probe 的失败、skipped、warning、unavailable、canceled 都必须保留到 item/run 结果而非静默吞掉。
5. 质量处罚只在完整、已验证的证据满足阈值或硬失败规则时执行；手动检查的 enforcement 开关和物理账号限制、`quality_recovery` 的隔离恢复、health sync 的失败重试语义必须保持。业务状态和质量结果不能由 Go 成功探测后再请求 Node 写回。

## 4. Go 目标结构与禁止边界

方案 A 下，J3b 目标 owner 是 `juhe-ai-gateway`。无论 PostgreSQL 或 SQLite，管理 listener、runner、scheduler、input/outcome store、业务 projector、运行 health 和 audit append 都由该进程内的同一 J3b 组件完成。管理读 API 也由其 Go handler 直接提供，入口路由通过 GitOps 精确分流至 gateway；这是完整后台功能的受认证管理命令入口，不把 gateway 扩展成 Node 的通用代理。

J3b owner 只可直接请求目标上游和 PostgreSQL/Redis 等明确依赖。严禁 J3b 调用 Node、Node DB-service、Node IPC、Node queue、Node HTTP 或另一个 Go 服务。`jobs` 可以复用无副作用的 J3b Go 包，但不得作为 J3b runtime、scheduler、Business SQLite writer 或 projector；也不得以 HTTP、IPC、queue、RPC 或 typed command 调用 gateway。没有明确定义 input identity、config revision、lease fence、outcome digest、CAS 和重放语义时，不得开始实现或发布 scheduler。

当前 SQLite business DB 仍由 Node 单 writer 持有，尚未存在可验证的 Go 直接业务写入 owner。按双模式完整迁移规则，J3b L2 必须先由 gateway 接管所有受影响 SQLite business writer 与 schema 生命周期，并在每个 SQLite 文件建立唯一 writer；只做 PostgreSQL 接管并保留 SQLite Node owner 只能作为显式的临时范围缩减，不能标记 J3b 已接管。当前 `backend-go/projects/jobs` 的 J3b 基线只是待复用的实现材料，不是方案 A 的运行时 owner；必须以当前架构重新建立输入、outcome、schema contract 和 gateway 内进程实现。未完成前不得将 J3b 标记为 L2/L3 已接管。

## 5. L2 前必须冻结的输入/结果协议

- 输入：request ID、trigger、目标/比较账号及其 immutable config revision、provider protocol profile、请求模型与已解析的上游模型映射、credential envelope、probe profile、policy revision、observed time、deadline 和发起人授权快照。上游模型必须在 input 签发前解析并冻结；重放、审计或 retry 不得以运行时重新读取映射规则替代当次实际请求。
- 结果：稳定 outcome ID/digest、run/item/observation、wrapped HTTP status 与 upstream status、评分/可信度、quality decision、health-sync 状态、error class、started/finished/observed time。
- 一致性：每个 input 只签发一次；相同 identity+digest 的 outcome 重放幂等；不一致重放、过期 lease、删除/禁用账号、config/policy revision 漂移和 CAS 冲突必须 fail-closed 或记录 stale，不得覆写新状态。
- PostgreSQL：J3b owner 自有表与业务结果表均使用最小权限、短事务、`statement_timeout`/`lock_timeout`；运行期不执行 DDL。Go schema/权限预检不通过时 listener/scheduler fail-closed。

## 6. 验收与 L4

L2/L3 至少覆盖：所有 Node profile/probe 的 golden 对照；JSON 与 SSE 成功、拒绝、取消、客户端断开和 EPIPE；manual/scheduled/recovery/health-sync；凭据不进入 J3b outcome、日志或审计；lease busy、重复 input/outcome、revision 漂移、CAS stale、上游/代理/timeout/partial failure；PostgreSQL/PgBouncer 真实闭环、并发/race/vet；以及直接 Go 管理入口的管理 API readback。

L4 还必须证明：Node route、SSE service、active-run、scheduler、worker/IPC/DB-service writer、retention/health retry、启动项、指标与专属测试已 active-path-zero；完整 Node 文件清单、SHA-256、接管/回滚提交和恢复顺序写入 backup manifest；Jenkins 与 GitOps 在同一 release-state 原子切换 Go gateway 镜像、能力开关和精确路由；生产 owner handoff、重启、回滚与 freshness 证据均已记录。未满足任一项时只能报告“L1/L2 实现中”，不能删除 Node 功能或宣称接管。

## 7. 当前结论

J3b 仍不具备删除 Node 的条件。方案 A 架构已冻结为：`gateway` 进程内直接承受认证管理命令、SSE、scheduler 与业务投影，且 Go 在 PostgreSQL/SQLite 两种正式模式都成为唯一 J3b writer；不允许 Node bridge、Go→Node 调用、Go→Go J3b 调用或 SQLite 双 writer。Gateway 已形成可测试的 input/outcome、schema、health retry 和 owner fail-closed 基线；在真实 source/auth/resolver、scheduler executor、三库 backfill 和 Business SQLite handoff 完成前，不得打开 listener 或切换 owner。

## 8. 当前 L2 增量

`backend-go/projects/jobs/internal/modelcheckprofile` 已冻结九个现役协议 profile、十三个支持模型、默认模型/profile、配对模型优先级和来源 endpoint family；它只提供防御性拷贝的纯查询，不打开数据库、不发起网络请求，也不替代 Node 运行时。Go 单测直接读取 Node profile golden，因而 Node profile、模型或 endpoint family 的后续变更会先使 Go parity test 失败；本轮由此发现并修复 Anthropic 从旧 `claude-opus-4-8/4-7` 到当前 `claude-opus-5/4-8` 的偏移。

`backend-go/projects/jobs/internal/modelcheckinput` 已定义版本化、规范化的 `IssuedInput` 纯领域结构。它固定账号/config/profile/policy revision、endpoint fingerprint、credential envelope alias、model/profile/trigger/deadline 与 SHA-256 digest；payload 前会重算 digest，配置篡改会 fail-closed。该结构没有原始 API Key、token、cookie、代理密码或 response body 字段。`InputVersion` 由 durable Store 在同一 identity 的事务内分配并进入 digest，`IdentityKey` 仅由 system account、target、model/profile、trigger/schedule 和 comparison identity 派生，不包含凭据、时间或响应。

`backend-go/projects/jobs/internal/modelcheckdurable` 已实现 input、claim 和 outcome 事务边界，作为待迁移的纯实现材料；Gateway `internal/modelcheckowner.Store` 已新增只读 schema preflight，固定 J3b SQLite 单文件/PostgreSQL `juhe_j3b` 的物理入口，运行期不执行 DDL。相同 `input_id` 只有字节等价 immutable input 才能重放；claim/outcome 仍必须匹配 input digest、owner、outcome ID 和 fence。现有 jobs 包尚未迁入 Gateway 的业务 revision re-read、run/item/observation writer、质量投影或 runtime 接线，不能据此启用 J3b 或归档 Node。

Gateway `internal/modelcheckowner` 现已增加不可变 input 记录边界：payload 先规范化并计算 SHA-256，`input_id` 重放要求摘要和内容一致，不同内容复用 ID 会 fail-closed；写入使用单事务并保留 identity/version/config/policy/trigger/expiry 字段。该层仍是库级能力，未接入 listener、scheduler 或 Node 路径。

本轮复核还校正了 Gateway Business `circuit-control-plane` 的双模式数据契约：Go owner 现在完整读写 `key_model` 的 `client_model`、`capability_hash`、`credential_source_account_id`、`client_endpoint_family`、`final_upstream_model`、`upstream_endpoint_mode` 六个身份字段，按 Node 的作用域组合执行 fail-closed 校验，并在 SQLite/PostgreSQL 预检中要求 `idx_account_circuit_incidents_key_model_capability` 唯一索引；运行时文本输入遵循 Node 的 trim、有界文本和证据哈希规范，不再额外限制 `keyFingerprint` 或 `accountRuntimeKey` 的字符集，公开入口的 ID、owner 和游标也执行同等长度边界。Go 回归覆盖完整/部分 key_model 身份、重复 capability hash、字段回读、缺索引、文本规范化、输入长度和 `failure_scope` 枚举漂移；运维批次上限提升为 100000，仅表示事务恢复窗口，不作为产品并发限制。Gateway 主进程现在会在 J3b owner 启动时用同一 Business 连接预检该控制面契约，但仍未接入 circuit runtime dispatch、Node active-path-zero、Business handoff、真实 SQLite/PostgreSQL 切换和回滚证据。

Gateway `internal/modelcheckowner` 现已增加 run/item/observation 持久化边界：只有 running run 可追加检测项和 observation，终态 projection 在同一事务内写入全部 item 与终态摘要；相同终态可幂等重放，状态、分数、摘要或 item 集合漂移会拒绝。运行时会把请求模型和解析后的上游模型同时冻结到 durable input，并以该上游模型执行所有 probe；observation 保留请求/上游模型及 `mapped|unmapped` 状态。隔离回归覆盖六个 core probe 的实际上游请求、冻结 input 和 observation 三者一致。该事实不解除完整证据、Business SQLite handoff、三库 backfill、Node active-path-zero 或切换门禁。

Gateway `internal/modelcheckowner` 现已增加 claim/outcome durable fence：input 过期或活动租约竞争会拒绝，租约接管时 fence 严格递增；旧 owner 的 release/commit 会被拒绝；相同 outcome digest 可重放，内容漂移会冲突。该边界仍未由 Gateway runtime 调用，不能替代完整 scheduler、probe 和恢复验证。

Gateway 已建立独立的 `modelcheckactive` 与 `modelcheckowner.HTTPHandler` 边界：进程内按 system-account 防重复运行、停止和释放；HTTP 路径覆盖 `/run`、`/run/stream`、`/run/active`、`/run/stop`、`/runs` 及详情读取，SSE 发送 connected/progress/complete/error，活动冲突返回 409 和 `Retry-After`。handler 只接受注入的认证、构建器和 runtime service，未接线时返回 503；它不调用 jobs、Node 或其他进程。Gateway 主进程已有条件式挂载代码，但只有四项 readiness gate 全部通过才注册 listener。

Gateway 已开始迁入纯领域 profile catalog，`internal/modelcheckprofile` 与 Node golden 的默认模型、协议 profile、paired model 和 endpoint family 规则保持独立副本，未引入 `jobs/internal` 依赖。该包目前只作为后续 probe/runtime relocation 的值对象基础，尚未意味着 J3b runtime 已切换。

Gateway 已将 `Runtime` 接入 `RunService` 契约：在注入 target resolver 后可执行 `IssueInput → CreateRun → Claim → direct probe → CommitOutcome → ProjectOutcome`，并提供按 system-account 的 run 列表/详情读取。隔离 SQLite 的成功闭环和失败终态均已回归；主进程现已装配 Business source/auth/resolver，但在完整 probe/trust 聚合、schema/handoff 与其它 readiness gate 未满足时仍 fail-closed，不注册 J3b listener。

`backend-go/shared/contracts` 与 `backend-go/projects/maintenance/internal/j3bmodelcheck` 已冻结 J3b 独立 `juhe_j3b` schema contract 及一次性 bootstrap 命令。表、列、主键/唯一约束和游标/target 索引均由共享 contract 描述；其中 `model_token_intercept_baseline_versions` 及其 active 索引也必须由同一 maintenance 流程预置并验收，不能只在 Gateway 运行时临时创建。bootstrap 可通过显式 PostgreSQL 检查/应用命令或 `JUHE_AI_MAINTENANCE_J3B_SQLITE_PATH` 对专属 SQLite 文件检查/应用。SQLite readiness 除表/列外还会校验每张表的主键形状和关键索引，任何结构漂移均在回填前 fail-closed。SQLite schema 已覆盖 Gateway runtime 的终态字段（`account_id`、`level/score/max_score/message`、health-sync 状态）及 item 证据字段，避免只通过 readiness 却在实际投影时缺列。SQLite apply 使用单事务，任一 DDL 失败会整组回滚，且强制要求 `--node-stopped --go-stopped --backup-confirmed`；Gateway runtime 后续只做只读 readiness。另新增 `--backfill-j3b-model-check-sqlite`：它从 `JUHE_AI_MAINTENANCE_J3B_SOURCE_DATASET_PATH` 与 `JUHE_AI_MAINTENANCE_J3B_SOURCE_STATS_PATH` 只读复制 Node legacy 的 run/item/observation/health 事实到专属文件，并对 Go 新增的 input/version、claim、outcome、scheduler 表明确允许“源表不存在”。所有目标表仍先做完整 readiness 预检；缺少的 legacy 源表按 `source=0, inserted=0, target=<实际行数>` 报告，不能把已有 Go 目标事实误报为空。复制按目标列交集、单事务、幂等不覆盖已有行；投影列按名称规范排序用于稳定摘要，复合主键始终按 SQLite 声明顺序排序；同主键已有行会逐列比较，内容冲突直接回滚并失败，并输出源/新增/目标行数及每张表确定性 SHA-256 摘要，供切换前逐表核对。执行同样要求停机和备份确认，三库 digest/cleanup/rollback 审核仍是 L2 必需门禁。当前没有在开发主库或生产执行 bootstrap/backfill。

回填回归还覆盖 Node legacy 常见的扩展列与多行反序插入：源表只投影与 Go 目标共享的列，复制按主键声明顺序读取，源/目标公共列 digest 与只读 readback 保持一致；这项测试不改变真实数据库，也不构成切换授权。

`backend-go/projects/jobs/internal/modelcheckstore` 仍保留 `model_check_runs`、`model_check_items`、`model_check_observations` 的 Go typed writer 作为迁移材料：SQLite 显式建表，PostgreSQL 仅对旧 `juhe_dataset` 三表做契约预检；按方案 A，jobs 配置已对 SQLite/PostgreSQL J3b 一律 fail-closed，该包不得接入 jobs 常驻 runtime 或形成第二 writer。方案 A 的实际目标是 Gateway 的 `juhe_j3b` 专属存储，维护命令现在对 input/outcome、run/item/observation 和 health 表统一执行 schema/索引预检与显式 bootstrap；Gateway `modelcheckowner.Store` 已提供 runtime writer、health latest-wins 和 scheduler schema preflight。原有 run 终态 fence、原子 item/observation 投影和 replay 语义继续作为迁移参考；Gateway 的 listener/source/auth 生产接线、旧三库 backfill 和 Node active-path-zero 仍未完成，不能据此启用 J3b、删除 Node 或宣称跨运行时完整等价。

`backend-go/projects/jobs/internal/modelcheckprobe` 已接管四种协议的基础 capability request 构造：OpenAI Responses、OpenAI Chat、Anthropic Messages 与 Gemini native。它生成不含凭据的不可变 JSON bytes，并保留 Node 的路径、短探针最低 token、Anthropic 不发送通用 `temperature`、Gemini `?alt=sse` 规则；它不发网。后续 executor 必须直接使用此包，不能重新在 Node 或另一个 Go 服务构造 payload。

同一包新增 jobs-owned direct transport 与 `RunBasicProbe` 组合：严格校验 endpoint（禁止 userinfo/query/fragment/redirect），按协议补齐 `/v1` 或 `/v1beta`，支持 JSON/SSE、取消、超时和响应大小上限，并只把解析后的 model/output/status/usage 交给 evaluator；原始 response body、认证头和 transport 原始错误不进入 durable evidence。transport 回归覆盖四协议路径、SSE、非 2xx、超大响应、userinfo、取消和超时，组合回归确认真实 HTTP 响应可直接得到 `responses_basic` 评分 item。该层仍是单次 probe，不包含 retry、input claim、持久化、质量投影或管理 API。

`modelcheckexecutor` 已把 `LoadInput → resolver 预读/Business fence → Claim → RunSuite → CommitOutcome` 串成单输入 Go 闭环。resolver 预读失败不会留下租约；Business source 在构建阶段完成 revision/profile/model/endpoint 复核并冻结 immutable input；retry 只重试 transport/HTTP 非 200，HTTP 200 的内容质量失败只评分一次；claim 后可恢复错误会释放租约且保留递增 fence。当前 runtime 尚未实现独立的 claim 后 resolver 二次读取与 lease renewal，故这两项仍是运行时门禁，不得按已完成表述。`modelcheckdurable` 同时提供按 `(stored_at,outcome_id)` 游标读取并重验 digest/identity 的 committed outcome API，供 Gateway projector crash recovery 使用。该闭环现已由 Gateway runtime、管理 listener 与 scheduler 调用，但完整 quality/trust 投影仍受形成证据与 handoff 门禁约束。

`modelcheckprobe.RunSuite` 已按 Node 的基础套件顺序执行 basic、可选 stream、structured、tool，并从同一批响应生成 usage-shape；最终非 200 触发与 Node 相同的后续探针终止栅栏，失败保留为 item 级证据，行为或 long-context 组提前终止时也不会错误继续 stability。四协议 structured/tool 请求 schema 已与 Node golden 对齐，并继承目标的 stream 模式。`modelcheckprobe` 另已落地 Node 同阈值的 token integrity 分析器、固定 0/512/2048 padding 构造契约、行为探针和 long-context 纯领域评分；Gateway runtime 通过版本化 tokenizer/model-limit snapshot 接入这些组件，快照缺失时只产生显式 excluded/skipped 证据而不伪造执行结果。durable observation、管理 API/SSE、scheduler 已有 Gateway 接线，但完整 trust/quality 形成和 handoff 仍未通过，不能据此声称 J3b 已接管。

Gateway `modelcheckowner.Runtime` 现已形成一个进程内的 L2 运行时闭环：它在同一 Go 进程中完成版本化 input 签发、run 创建、resolver 双读与 durable claim、直接 probe suite、durable outcome commit，以及 run/item 的原子终态投影。成功、上游失败和客户端取消均会写入完整终态；调用方 context 已取消时，终态投影使用独立的短超时上下文，避免留下永久 `running` 记录。该服务没有 Node、IPC 或跨进程依赖，并以 Gateway SQLite 集成测试覆盖成功投影、取消失败投影和 durable outcome readback。immutable comparison snapshot 存在时可由 Go 独立执行第二套 suite 并生成比较摘要；但完整 observation/trust/quality 形成、Business handoff、三库回填和 PostgreSQL 端到端 wiring 仍是归档前门禁，因此本实现不改变 Node owner。

Gateway 现将 Go 可形成的 trust 摘要固化为 J3b 事实：每个 scope observation 先写去重 receipt，再标记 `aggregation_completed_at`，随后以 `(created_at,id)` 单调 cursor 写入 `model_trust_aggregation_state`，并更新 `model_account_trust_results`、清理相同 scope 的 dirty entry；同 cursor 不同事实会 fail-closed。`juhe_j3b` schema、SQLite bootstrap、inventory、PostgreSQL/SQLite readback manifest 均已纳入 `model_account_trust_results`、`model_trust_latest_dirty_accounts`、`model_trust_observation_receipts` 与 `model_trust_aggregation_state`。隔离 dev PostgreSQL 经 PgBouncer 的 Gateway runtime smoke 已覆盖 `Run → observation → receipt → latest/cursor`，且临时库已删除；另有 9 张 legacy fact（run/item/observation/health/baseline/trust latest/dirty/receipt/cursor state）零行结构 readback 通过。零行/夹具结果只证明契约可执行，不是历史数据回填或 owner handoff 证据；token/identity window、aggregation lease 的历史 handoff、真实非空 source freeze/readback 及最终 retention 仍是未闭合门禁。

`backend-go/projects/jobs/internal/modelcheckquality` 现已冻结 Node 质量 gate 的纯事实层，并由 runtime 在终态投影完成后以 CAS 方式追加版本化 `quality_decision_json`（包含 outcome/policy/evidence 摘要身份）。证据未由 trust/identity/Juice projector 完整形成时，Go 显式写入 `quality_evidence_not_formed` / `not_triggered`，不会处罚、隔离、降级或写健康失败；缺失、空值或未知 item status 同样按 partial evidence 处理，不得借 score 推断形成；相同事实可幂等重放，漂移会 fail-closed。追加会在同一行锁事务内结构化比对终态 status、result summary 与 policy snapshot，不以 JSON 文本等值作为 SQL 条件，避免 PostgreSQL JSONB 与 SQLite TEXT 的序列化差异；隔离 dev PostgreSQL writer smoke 已覆盖首次追加与幂等重放，并清理临时 run。该增量仍不是业务质量 projector：`account_quality_enforcements`、恢复、health-hour 写入与失败重试尚未接线，因此 Node 质量 writer、scheduler 和 SQLite business writer 仍不可删除或归档。

首个 Go 直接业务写入原语 `modelcheckquality.ApplyEnforcement` 已实现为单事务的 PostgreSQL/SQLite business CAS：先逐字段复核冻结的 manual policy 或 schedule，再复核自有物理账户、删除状态、授权实例、来源状态与 `config_revision`；账户更新成功后才 upsert generation 单调递增的 active enforcement。相同旧快照在账户 revision 已递增后按 Node 源码顺序返回 `stale`，而不是把旧 run 误判为成功重放。该原语尚未从 runtime 调用，也未形成 SQLite writer handoff，故不会改变现有 Node owner；后续必须接入 formed evidence、Go cache invalidation、recovery/health projector 与 scheduler 后，才能启用。

同包新增 `ClaimDueRecoveries` 与 `CompleteRecovery`，作为尚未挂载的 Go 质量隔离恢复事务原语：前者只 lease 到期的 active isolate enforcement，并把账户当前 `config_revision` 固化回 enforcement；后者必须同时匹配 owner、account、enforcement ID、generation 和恢复 lease。失败检查、policy/config 漂移只会清租约并重排下次检查，绝不解除隔离；成功检查才按账户可用时段恢复为 `active` 或 `disabled`，然后清除 enforcement。账户时间计划按 Node 的时区、例外和跨日 window 计算；坏的持久化计划不会默认为允许，而是回滚并报错。该原语同样没有 runtime/scheduler/health/cache 接线，不能启用、不能成为 SQLite 业务 writer handoff，也不能作为删除 Node 的依据。

同一层另有 `ClaimDueSchedules` 与 `CompleteScheduledRun`：它们对 enabled schedule、当前 active 的自有账户、schedule revision 和 schedule lease 做直接 PostgreSQL/SQLite CAS。Node 的低 `limit=3`/单 worker 参数没有被复刻为 Go 吞吐限制；未来 Go scheduler 可以按资源配置更大的批量和并行度，但仍必须保持每条 schedule 的 lease/revision fence。当前这也只是未挂载原语，尚未请求上游、没有写 run，不能与 Node scheduler 并行启用。

`backend-go/projects/jobs/internal/modelcheckactive` 已补齐 Go 进程内活动任务生命周期：以 system-account 作用域做互斥，句柄绑定取消 context，`Stop` 只取消匹配作用域并标记 `stopRequested`，`Finish` 以句柄身份清理，旧句柄不会误删新任务；并发启动、跨作用域隔离和停止取消均有 `-race` 覆盖。runtime 可选接入该注册表，停止动作会沿同一 context 进入 executor，最终由终态投影记录 canceled。它只负责请求级协调，不替代 durable claim/fence 和跨实例恢复；管理 JSON/SSE 与持久化 active-run 查询仍未接线。

Gateway `internal/modelcheckowner` 已建立 Go-owned JSON/SSE 管理边界：`/run`、`/run/stream`、`/run/active`、`/run/stop` 使用统一 `{data: ...}` envelope，严格拒绝未知字段和客户端伪造的 provider/threshold，默认 profile 与 trusted comparison 参数校验对齐 Node；handler 还会复核 Build 返回的 system/actor scope、目标和 profile 快照，防止装配层发生授权漂移。JSON 与 SSE 均在写响应前取得以认证 actor 为 key 的活动租约，冲突返回 `409`/`Retry-After`，并在 run started 后回填真实 `runId`；SSE 下游写失败或客户端取消会取消同一 Go run context，suite item 进度按顺序发送且不静默丢弃。公开 listener 分别挂载 `/__aisys__/api/model-checks/`（管理员）和 `/__aisys__/api/my-model-checks/`（登录账户自身作用域）；self 路径忽略调用方提供的 `systemAccountId`，并拒绝管理员专属的 token 截距基线激活；未修改初始密码的请求返回 `403` 与 `must_change_password`。管理员入口现以 `actor + selected tenant | all` 显式 scope 传递：未传或传 `systemAccountId=all` 时历史/详情/自有账户候选可读取全局，显式 tenant 时才合并可证实的授权实例；发起检测时 Go 读取主目标和可信对照账户各自 owner，run 保留目标 tenant 与 actor metadata，可信对照只作为同一 run 的检查输入，长期 observation/trust projection 始终归主目标，不能把管理员、主目标或对照账户租户伪写为另一方；quality policy/schedule 在全局 scope 仍明确返回 `400` 要求选择具体租户。Gateway 写入 run/item 摘要时现复用 Node 的字段名和字符串形态脱敏，并限制 JSON 深度、对象键、数组和字符串长度；因此凭据、代理 URL 密码、原始 body/完整 response 等不会进入 durable J3b projection。该局部对齐不证明授权账户可见性、完整详情 DTO、错误/事务 golden 与 self 脱敏已经完成，owner manifest 仍为 `partial`。Host 的 `Mount` 只在 owner ready 后把完整 handler 以显式前缀挂入 Gateway mux，避免路由已曝光但 owner 未装配。主进程已有真实 auth、账号 source/build、enforcement、scheduler 与 listener 装配路径；缺少 handoff/backfill/active-path-zero 时仍不得启用或删除 Node 管理路由。

Gateway full suite 现已补齐 Node 的非 trusted comparison 行为：即使未选择独立可信账户，也会在同一 resolved endpoint 上执行 profile 配置的 paired-model cross-model probe；只有显式 trusted comparison 才执行跨账户 distribution similarity。该探针只持久化比较结果摘要和模型身份，不保留响应原文；质量 projector 仍以完整 evidence family 和 trust 形成作为前置条件。

`backend-go/projects/gateway/internal/modelcheckauth` 已建立 Gateway 自有的 PostgreSQL/SQLite 管理会话鉴权模块：cookie 会话、`juhe_tmp_` bearer 优先级、SHA-256 token hash、到期、账户状态、强制改密、管理员角色与每分钟 Node ISO 格式的 session touch 都由 Go 直接校验；authenticated/temporary session 创建、撤销和清理也已在 owner transaction 内实现；`modelcheckowner.NewAdminAuthorize` 将其以进程内 adapter 注入 J3b handler。Gateway 管理 listener 现已接入 `/auth/login`、`/auth/logout`、`/auth/me`、`PATCH /auth/me`、`/auth/change-password`、`/auth/captcha` 及受显式 `JUHE_AI_TEMPORARY_ACCESS_IP_ALLOWLIST` 保护的临时令牌签发/撤销路径，覆盖 PBKDF2 revision fence、must-change、保留当前会话撤销其它会话、Node 10 次/10 分钟失败后 15 分钟锁定的进程内登录限流，以及 5 分钟一次性验证码和每 IP 每分钟 60 次生成限频。验证码和登录限流当前均为进程内实现，共享 runtime-state、多实例一致性、完整 system-account/team 管理 API 与 production schema handoff 仍未完成；jobs 的同名模块只作为历史迁移材料保留，不能被 Gateway import，也不能成为 J3b owner。

`backend-go/projects/jobs/internal/modelcheckapp` 的 host 仅作为历史迁移材料保留，`juhe-ai-jobs` 现对任何 J3b 启用配置硬失败，不打开 J3b 资源。Gateway `internal/modelcheckowner.Host` 已将 Store、HTTP handler、Runtime、QualityProjector 和三类 scheduler 组合为单进程 owner；Host 构造阶段现在强制要求 durable scheduler source/executor 与 enforcement，缺失即 fail-closed，不会把只有 HTTP 的半装配实例标记为 ready。真实主进程装配已完成；管理配置 API、schema/data backfill、GitOps readiness/回滚证据和 Node active-path-zero 尚未完成，因此默认 listener 保持关闭，Node 仍不能删除或归档。

SQLite 管理 listener 当前必须 fail-closed：Node 仍是共享 business SQLite 文件的唯一 writer，而 Go 管理鉴权与 Node 一致，会在会话超过一分钟未见时更新 `system_sessions.last_seen_at`；模型检测 `POST` 还是 Node system API 的 write/touch 会话路径。将 Go 改成只读验证并取消 session touch 不与该可观察会话契约等价，也不能使后续质量处罚、恢复 lease 或 health-sync 写入安全。故 `JUHE_AI_MODEL_CHECK_ENABLED=true` 且 `JUHE_AI_MODEL_CHECK_STORE=sqlite` 当前被 Go 配置拒绝；这是共存期保护，不是 SQLite 双模式接管完成或永久能力删除。共享 `system_sessions` 和 business SQLite file writer 的完整 owner handoff 超出 J3b，必须在独立范围中完成并有 Node active-path-zero 证据后，才能重新设计 SQLite owner。

J3b health-sync 的 Gateway adapter 已接入 durable failed/pending_retry 重发现、formed/trusted 校验和 latest-wins upsert；`QualityProjector` 现在还要求一个显式的 Business-owner `EnforcementApplier` 才能处理低于冻结阈值的 formed/trusted 失败，否则标记 retry 并 fail-closed，不会仅凭 score 修改账号。Gateway 已提供受 config-revision CAS 保护的 `BusinessEnforcementApplier`：账号状态、错误信息、revision 和 `account_quality_enforcements` upsert 在同一 Business 事务提交，重复/过期 revision 会拒绝；该实现只有在完整 Business writer handoff 后才能注入。它仍不具备生产启用条件。Node 当前把已形成的模型质量失败写入 `account_quality_health_hourly`，并以失败 run 的 dataset fact 作重试来源；该统计写入不能借用、替换或提前接管 J3c 的 usage 统计刷新和失败前置确认 writer。Go runtime 仍缺生产级 Node 等价的 observation、trust aggregation、trusted comparison 与 tokenizer/model-limit source；三类 scheduler executor 已有主进程装配，但未有隔离环境的完整行为验证。`Evidence.Formed=false` 仍是未形成证据时的 fail-closed 状态。没有这些事实，按 score 推断处罚、恢复 passed 或 health-sync 都会改变可观察业务结果；在真实 handoff 和物理存储验证完成前，listener/scheduler 必须保持关闭。

专属 J3b storage 的 maintenance bootstrap/readiness 已与 Gateway `CreateRun` 对齐：`model_check_runs` 必须包含 `schedule_id`、`probe_set_version`、`started_at`、`trace_id`，旧专属 SQLite 文件仅能在显式 maintenance transaction 中前向补列，并以旧 `created_at` 回填缺失 `started_at`；runtime 不执行 DDL。SQLite legacy backfill 将 Node 的 `run/item/observation` 与 J3b health facts 视为必需来源并逐主键比较冲突，而 `input/version`、claim、outcome、scheduler task 是 Go owner 的新事实，Node source 缺表时只记录零行，不得以此阻断切换或伪造旧事实。该行为已有新建库、旧列升级、真实 legacy 缺少 Go-only 表、幂等重放和冲突回滚测试；未在任何开发主库或生产库执行。

Gateway `internal/modelcheckowner.BusinessTargetSource` 已开始承担真实 Business 直接读取：只读事务按 `system_account_id + account_id` 查询逻辑账户、启用的 `group_accounts/groups` binding、provider protocol profile、状态/可调度性和 config revision，校验旧快照漂移后在进程内解封 v1 credential envelope 并构造协议认证头；`Resolver()` 提供给 Runtime 的仅进程内适配，不引入跨进程 transport。`OpenBusinessTargetConnection` 返回经 contract 校验的共享 `DB + Source + Close`，供 Gateway auth/source 复用同一 DSN、权限与生命周期；未完成 Business handoff 时 SQLite 使用 `mode=ro + query_only`，确认 handoff 后才切换为 owner 的 `mode=rw`（Source 仍只用 read-only transaction），以便同一 Gateway owner 正确执行 session touch/enforcement；PostgreSQL 先执行只读 contract 检查。它不创建 schema，也不依赖 jobs/Node/IPC/HTTP。J3b 启用配置现在还强制声明 Business SQLite/ PostgreSQL 数据源、credential secret、identity secret，并拒绝 J3b 专属 SQLite 与 Business 文件复用。当前组件已有 SQLite scope/redaction/credential/group binding 回归，并已在 Gateway 主进程装配 auth/build/scheduler 工厂；但 readiness gate 和真实 handoff 未完成，因此仍不得打开 listener。jobs 中的 `modelchecksource` 与 `modelcheckresolver` 继续作为迁移参考，不能被 Gateway import。

Gateway 的手动 run resolver 与 quality schedule command 现共同复用账户有效模型判定：若 `account_supported_models` 为空，采用冻结的 provider/profile catalog；一旦该账户存在显式支持模型，只有该集合中的 source model，或启用的 `account_model_mappings` 把该 source endpoint 映射到集合内 upstream model 时才可运行或建立计划。映射不仅用于放行：Gateway 将解析出的 upstream model 固化在 execution target，实际 probe 发送该模型，并在 observation 中分别持久 requested/mapped model 与 `mapped|unmapped` 状态；不能因静态 catalog 命中绕过账户限制或向上游发送错误 source model。`intervalMinutes` 也必须在 10 至 10080 分钟，创建与更新都遵循同一边界。Gateway 现已接入只读 `/options`、`/account-options` 与 `/options/accounts` 兼容路径，严格复用 `purpose/accountId/keyword/selectedIds/limit` 参数校验，并从同一 Business source 返回过滤后的账户与静态模型目录；当前仍仅覆盖 Gateway admin scope，完整授权实例/跨 system-account access scope 以及 Business handoff 前的 Node owner 退出尚未完成，不能把该只读接口视为完整管理 API 迁移。

Gateway `internal/modelcheckauth` 现已补齐 owner 内的 session lifecycle 基线：authenticated session 与 temporary token 创建会在同一事务内校验密码 revision、写入 `system_sessions` 并更新 `last_login_at`；token/session 撤销、撤销其它会话直接作用于 owner 连接，保留 Node 的 `juhe_tmp_` token 形状。过期清理已拆为 `internal/business/session_retention` owner port，由主进程在相同 Business 连接上完成 contract 检查后由 supervisor 周期执行，严格保持 Node 的 `expires_at < expiredBefore` 与有界删除语义；组件首次成功清理前不会进入 Gateway health ready。主进程管理 listener 已接入基础 login/logout/me/profile/password/captcha 路径并保持四项 readiness gate；验证码和登录限流仍是单进程实现，临时访问令牌的完整策略、system-account/team 管理和 Business writer handoff 未完成前，不能把 auth 视为完整迁移，也不得切换或归档 Node。

`BusinessTargetSource.BuildRequest` 已将管理请求构建收口到 Gateway：先按 system/account scope 冻结目标、provider 和 `config_revision`，再在同一只读边界读取 `model_quality_policies` 的 profile、revision、threshold 和 `penalty_action`；缺少策略、可信对比 source、非法处置动作或 revision 漂移时拒绝构建，不回读可变全局策略。该 builder 已由 HTTP 与三类 scheduler 工厂复用，处置动作会随 policy snapshot 进入 health retry/enforcement CAS；真实 handoff 和 formed evidence 验证未完成前仍保持 fail-closed。

Gateway `cmd/juhe-ai-gateway` 已具备方案 A 的主进程装配路径：启用 J3b 时先打开经 handoff 门控的 Business connection、auth、source/build、enforcement 和专属 J3b Store，再由 `Host` 在同一进程创建三类 scheduler（含 owner/fence claim 与 failed retry），挂载 `/model-checks/` 管理 listener 并交给 supervisor 生命周期管理；任何 schema、权限、source、enforcement 或 scheduler factory 缺失都会在监听前 fail-closed。`quality_recovery` 在 Business generation/CAS 完成器未接入前会在执行入口直接失败并留下重试，不会启动探测或伪造恢复结果。该路径仍受真实 schema/data backfill、完整 Business API handoff、部署 readiness 和 Node active-path-zero 门禁约束，当前环境未授权启用。

Gateway 现已将 `quality-policy` 与 `quality-schedules` 的读取、revision CAS、创建、更新和删除装配为同一进程内的 Business-owner 管理 API；`/model-checks/quality-policy`、`/model-checks/quality-schedules` 与原有 run/SSE 共用 Gateway admin session scope，不再调用 Node DB-service。schedule/recovery scheduler 也已直接在 Business `model_quality_schedules` 与 `account_quality_enforcements` 上 claim owner lease：scheduled run 终态以 schedule revision/owner CAS 写回下次运行时间，quality recovery 以 enforcement ID/generation/recovery lease 完成恢复或重排，health-sync retry 则从 J3b durable failed run 幂等物化到专属 task 表后再 claim。处罚事实现在冻结 schedule ID、policy action、threshold 与 recovery interval；health retry 会从 durable run 重放同一组字段，避免把 schedule 处罚改写成 manual/10-minute 默认值。当前已覆盖 SQLite lease、stale fence、失败重排、跨日 availability、policy/schedule CAS 与 retry task materialization；隔离 PostgreSQL/PgBouncer、真实 schema/backfill、全量管理 API handoff、Node active-path-zero 与发布回滚仍未完成，不能启用或归档 Node。

Gateway `modelcheckprobe` 现提供显式版本化的 `Tokenizer` 与 `ModelLimitSnapshot` ports：token integrity 固定三轮 `0/512/2048` 差分 padding，long-context 根据已冻结的模型窗口 snapshot 生成 low/medium/high 窗口；缺少任一 snapshot、usage 不完整或模型窗口不合法时只产生 `excluded/skipped` 证据，绝不以 rune/byte 计数或默认窗口推断完整质量。Gateway 已接入嵌入式纯 Go `o200k_base` tokenizer（版本保持 `js-tiktoken@1.0.21:o200k_base`），并从 Business `provider_model_catalog` 读取版本化 `max_input_tokens/context_window_tokens` 快照；`HostDependencies -> Runtime -> Suite` 已贯通这两个注入点并有成功/缺失回归。其余 full-profile 证据族、真实上游 golden 和 Business handoff 仍未完成，quality projector、recovery 和 health-sync 继续受 formed/trusted 门禁保护。

处罚提交前还会在相同 Business transaction 内重新读取 schedule 或 manual policy，逐字段比较 revision、profile、threshold、action、recovery interval（schedule 另比较 model）；配置漂移只保留 J3b 质量事实并拒绝修改账户。该检查与 account `config_revision`、enforcement generation、recovery lease 是不同层的 fence，任何一层不匹配都不得降级为“尽力处罚”。

`modelchecksource.PostgresReader` 已开始承担真实 Go 业务读取：在一个 `REPEATABLE READ` / `READ ONLY` 事务中，以 `systemAccountID + accountID` 作为 SQL scope 读取逻辑账户、授权实例的有效物理源、账户/分组授权、启用 binding、protocol profile、模型限制/mapping 与有效 proxy；凭据和 proxy password 只在本进程解封装为内存 execution snapshot，durable input 只保存 HMAC/摘要身份。它不会调用 Node DB-service、IPC 或 HTTP；2026-08-27 已使用 dev PostgreSQL 的应用连接完成零行 schema/grant 合同预检。相同语义的 `SQLiteReader` 使用 `mode=ro + query_only` 读取当前 Node business SQLite，并已有 owner-account fixture 的 snapshot/redaction/replay 回归；它仍是 reader，不触碰 Node 当前 SQLite writer。两者已由 Gateway 主进程的 source/build 工厂持有，但跨 endpoint-family 的 model mapping 也尚未具备 Go 直接协议转换，不能静默按错误协议请求上游。真实 handoff、质量投影和全量回归未完成前，Node 仍是 J3b active owner，禁止归档其 route/scheduler/writer。

本轮补入 `modelcheckcommand` 与 `modelcheckpolicy`：管理命令构建器先以 Go source reader 冻结目标和可选可信对比账户，再读取并冻结同一 system-account 的有效质量策略，才生成 runtime request；目标名称、资源所有者、分组、profile、策略版本和探针版本均进入 run/input。`PolicySnapshot` 已升级为包含 profile、手动处置开关、阈值、动作和恢复周期的完整值对象，digest 由 Go 对规范 JSON 计算且在 input 签发/重放时复核，不能以任意外部 digest 绕过。`modelcheckpolicy.Reader` 对缺省 policy 使用与 Node 相同的 quick/70/fallback/10-minute 默认值，并可从 PostgreSQL 或 SQLite 业务表以事务级只读方式冻结显式 policy。普通检测的账号可用性已收紧为 Node `includeUnavailable` 域：仅 `active`、`temporary_unavailable`、`rate_limited` 且可调度、未过期；`pending_test` 不得执行，`quality_isolated` 仅可由 `quality_recovery` 请求使用。PostgreSQL 候选 SQL 也在只读 readiness 时经 `EXPLAIN` 规划，避免首个管理请求才发现 schema/type drift。以上已由 Gateway runtime、管理 API/SSE 与 scheduler 工厂持有，但完整 Business handoff、生产 schema/认证与质量 writer 验证仍未完成，不能改变 Node owner。

同一 `modelcheckprobe` 包还解析四协议 JSON/SSE 的 model、output、usage 与 error envelope，HTTP `200` 但包含协议错误时不会被标为成功。它只保留评分所需字段，不保存原始 response body；structured evidence 仅保留 `status/value`，usage evidence 仅保留八个数字 token 字段，不能把上游额外 JSON 写入 outcome。Node 的多行 `data:`/EOF frame、Anthropic delta、OpenAI stream failure、Gemini error 已由 Go 回归覆盖。Gateway 现已迁入 structured/tool/usage/stability 的纯评估与 suite 执行，并将 core-suite item 与 `partial` observation receipt 写入 Gateway 专属 J3b store；full profile 现执行行为探针和七类 identity canary，并只保留计数/脱敏摘要。token integrity 与 long-context 也已有 Gateway 纯评估器；Gateway 已接入版本化 `o200k_base` tokenizer 与 Business `provider_model_catalog` 窗口 snapshot，快照缺失或不合法时 suite 显式写入 `skipped` evidence，不伪造成功。distribution、cross-model 和 GPT-5.6 Responses full-only Juice 也已在 Gateway 纯函数层实现，Juice 按强异常 25、弱异常 8、coverage mismatch 12 的既有分级保留摘要；缺少 comparison snapshot 时明确 `skipped`。`EvidenceAggregate` 严格要求 identity、token、stability、distribution、cross-model、Juice、usage、behavior、long-context 全部形成，缺失或 partial 时输出 `evidenceFormed=false`；Gateway `TrustReport` 现将 identity/Juice/cross-model 风险和缺失族写入脱敏 quality decision 摘要，但不触发 enforcement/recovery/health 写入。Gateway 还新增统一三类 scheduler coordinator（`scheduled`、`quality_recovery`、`health_sync_retry`），其 SQL source 现在以 owner/fence claim、`completed`/`failed` 状态和延迟重试事实闭合。`SchedulerRunExecutor` 对 scheduled/recovery 只接受含 system/actor/target、profile/provider、config/policy/probe revision、identity 和阈值的持久 payload，并在 Gateway 进程内调用注入的 builder 与 Runtime；`SchedulerExecutorMux` 将其与 formed/trusted health retry executor 严格分开。缺任何字段、未注册执行器或未知 kind 一律留下失败重试事实，绝不 reload 全局 policy 或伪造请求；jobs 的 J3b 配置现已对启用请求硬失败，旧 host/listener 代码仅作为迁移材料，未启动或成为 owner。Gateway 新增 `modelcheckowner.Host`，把 Store、HTTP handler、Runtime、QualityProjector 和必需的 scheduler 组合成单进程 owner，并在依赖缺失或 schema 未预置时 fail-closed；Runtime 将 policy revision/threshold 固化到 durable policy snapshot，仅在 evidence formed、trust formed、provider 和 threshold 已冻结时调用 QualityProjector，失败保留 retry；SSE 已通过有界事件通道传递 progress、周期性发送 heartbeat，并在客户端取消/断开时取消 runtime。主程序已完成 source/auth/resolver、tokenizer/model-limit 与 scheduler 的装配代码，但仍受 readiness gate 保护，未授权打开 listener。Health retry executor 已实现 durable failed/pending_retry 重发现与 formed/trusted 校验；trusted comparison 已具备进程内双目标执行边界，但完整 observation/trust 形成、Business handoff、三库回填与生产 Gateway wiring 仍未接入。

trusted comparison 当前已具备 Gateway 进程内的双目标执行边界：请求构建阶段冻结独立比较账号及其 `config_revision`，runtime 分别解析两端的 upstream model、protocol、endpoint fingerprint 并直接执行 comparison probe；这些字段进入 durable input，缺少比较快照时仍保持 skipped。该实现已由 Gateway 端到端隔离回归覆盖，但不解除 tokenizer/model-limit、完整质量证据、Business SQLite handoff、三库回填或 Node active-path-zero 门禁。

本节前文阶段性记录中“可信对比未接线”的表述以本段为准：当前仅说明双目标执行边界已存在，不能解释为完整 trust evidence 或质量处罚条件已经满足。

## 9. 方案 A：Business SQLite 单 owner 前置契约（L1）

> 本节是 J3b 进入 L2 前必须完成的独立前置契约，不是 J3b 的 L2 实现、发布或切换授权。当前 Node `db-service` 仍是共享 Business SQLite 物理文件的唯一 writer；本节没有改变任何运行时 owner，也没有授权启动服务、执行数据库变更或归档 Node。

### 9.1 前置目标与范围

方案 A 的目标是先完成共享 Business SQLite 文件的完整 owner handoff，令 Go `gateway` 成为该文件的唯一运行时 writer，再让 J3b 在该既成边界内取得自己的完整 SQLite owner。不能把 `system_sessions` 的局部 touch、J3b 质量写入、单个 HTTP 路由或一张表的 Go 基线当作 handoff 完成。

该前置范围包括 schema/seed、管理 mutation、认证与 session lifecycle、DB-service command、后台 cleanup/lease/outbox 及其跨库顺序。`dataset`、stats、chat usage 等不因位于其他物理文件而成为本文件 writer；但涉及 Business SQLite 删除、lease、outbox 或投影的跨库事务顺序仍须在对应批次中验证。J3b 和 J3c 仍是两个独立功能 owner，不能以“后台任务”名义合成一个通用 writer。

### 9.1.1 可追溯写者与能力清单

下表是进入 B1 实施前的最小追踪索引；它不是“类别承诺”，每一行都必须在实施阶段补齐逐命令、逐路径、逐表的 manifest，并以源码扫描和运行时审计闭环。当前列出的目标 owner 是方案 A 的目标，不代表已经完成接管。

| 当前写者/能力 | 可核验路径或命令域 | 主要物理表/文件 | 当前 owner | 目标 owner | 处理方式与门禁 |
| --- | --- | --- | --- | --- | --- |
| Business schema/seed | `backend/src/storage/database.ts`、`business-schema.ts`、`seed-defaults.ts` | Business SQLite 全部业务表 | Node `db-service` | `maintenance`（离线）+ `gateway`（运行） | 先做 schema/seed manifest、幂等与回滚；runtime 禁止 DDL |
| 管理 mutation | `backend/src/modules/system-api/**`、`storage/*repository.ts` | accounts、groups、route strategies、API keys、providers、settings、authorization、proxy、announcements | Node `db-service` | Go `gateway` | 按事务组迁移完整 API；Node 路由与 DB-service command active-path-zero |
| 认证与 session | `auth.routes.ts`、`auth.middleware.ts`、`system-accounts.repository.ts` | system_accounts、system_sessions | Node `db-service` | Go `gateway` | 保留 login/touch/logout/revoke/password-change 语义；禁止只迁 touch |
| DB-service runtime mutation | `db-service-types.ts`、`db-service-handlers.ts`、`db-service-operation-access-mode.ts` | accounts 状态、OAuth、key runtime、circuit、health cursor/outbox | Node `db-service` | Go `gateway` | 逐 operation 建 command/表/事务映射；不可用 jobs 代写 |
| Business cleanup/lease/outbox | `background-jobs.ts`、maintenance cleanup、circuit control-plane | 删除清理 targets、sessions、availability、authorization、circuit outbox | Node worker/`db-service` | Go `gateway` | 固化 drain、lease、跨库顺序和恢复；不得双 consumer |
| J3b 专属事实 | `backend/src/modules/model-checks/**`、质量 repository/worker | 专属 J3b SQLite 文件或 PostgreSQL schema：run/item/observation/trust/quality/health | Node dataset/stats/db-service | Go `gateway` 进程内 | 从 `jobs` 现有基线迁移为 gateway 内实现；不写 Node dataset/stats 文件 |
| J3c 统计/前置确认 | quality refresh、failure-precheck queue | J3c usage/stat 及其独立状态 | Node `stats-worker`/`ops-worker` | J3c 独立 owner | 不与 J3b 合并；J3b 只读已发布健康结果 |
| 当前 Go 基线 | `backend-go/projects/jobs/internal/modelcheck*` | jobs-owned test/store 与 durable facts | Go `jobs`（未启用 owner） | Go `gateway` 内包/重写 | 仅复用无副作用领域代码；禁止 gateway import jobs/internal 或跨进程调用 |

该表明确能力缺口：gateway 当前缺少完整管理 API、session lifecycle、Business SQLite writer、schema owner 和 J3b runtime 接线；maintenance 只有一次性 schema/诊断能力；jobs 现有 `modelcheck*` 不能直接成为 gateway 依赖。缺口必须拆成可验收的迁移/重写任务，未补齐前不得以目标 owner 作为已实现能力。

### 9.2 B0-B4 owner 批次

| 批次 | 唯一 owner 与工作 | 完成条件 | 明确未完成时的限制 |
| --- | --- | --- | --- |
| B0 | `maintenance` 负责可重复、显式触发的 schema/seed/preflight；运行期不得 DDL。 | 新旧 schema 版本、权限、seed 幂等性和失败恢复均有 SQLite/PG 证据。 | 不得由 jobs、gateway 或 Node runtime 隐式补 schema。 |
| B1 | `gateway` 承接完整 Business SQLite 管理 API、认证/session lifecycle 和既有 DB-service mutation 的直接 Go 实现。 | 每条 mutation 已归类、具备请求/事务/错误语义对照，Node 同一路径清零。 | 仍由 Node owner 写入；Go 只能按已存在的只读共存门运行。 |
| B2 | `gateway` 在同一进程内承接所有会影响 Business SQLite 的后台 lease、input/outcome、scheduler、投影和恢复；`jobs` 不参与 J3b runtime。 | 每个任务的触发、取消、重试、lease/fence、跨库顺序和恢复均有边界测试。 | jobs 不得直接或间接写 Business SQLite，也不得通过跨进程命令参与 J3b。 |
| B3 | J3b 以独立输入/结果事实、scheduler 和直接质量投影接入 gateway 已完成的 owner 边界。 | 满足本文件 9.6 的 J3b L2 准入矩阵。 | 不得以 jobs 中的 J3b runtime、handler 或 reader 基线宣称 SQLite handoff 完成。 |
| B4 | J3c 另立契约迁移 usage 统计刷新和失败前置确认。 | J3c 的表、worker、重试来源、投影和回滚均由其自身证据覆盖。 | J3b 不得写入、借用或替代 J3c 的统计/前置确认 owner。 |

### 9.3 三项目职责与单物理文件不变量

`gateway` 是 Business SQLite 与 J3b 的唯一进程 owner：它负责管理请求、认证/session 写入、业务 mutation、J3b 的直接上游工作、input/outcome、scheduler、最终 business projector 及该文件的事务/恢复语义。`jobs` 不运行 J3b；它只可复用无副作用的共享 Go 领域包，不持有该文件的连接写权限，也不向 gateway 发送 J3b command。`maintenance` 只负责显式离线 schema、seed、backfill、诊断和预检；它不是常驻业务 writer，完成后必须退出。

任一时刻、每一个 Business SQLite **物理文件**只能有一个 writer 进程。该不变量包含 `system_sessions.last_seen_at`、管理 mutation、quality enforcement/recovery、scheduler lease、cleanup、outbox 和 schema lifecycle，不能按表、路由或功能拆分规避。非 owner 进程必须以只读连接和 `query_only` 运行；任何需要该文件 mutation 的路径均必须在 owner 内完成其完整事务。

J3b 不建立 `gateway`/`jobs` typed command、HTTP、IPC、queue、RPC 或任何补偿链路。需要跨项目复用时，只能抽取无 I/O、无连接、无调度副作用的共享 Go 包，并由 gateway 在同一进程内调用；Business SQLite mutation、J3b lease、scheduler 和 projector 的 command ID、revision、owner epoch/fence、payload digest、超时/取消与幂等重放均在该进程内完成和验证，旧 fence、过期 lease、冲突 payload 或重复副作用一律 fail-closed。

### 9.4 禁止的共存方式

在 B0-B4 期间，禁止 Node↔Go HTTP/IPC/DB-service/queue bridge、手动 adapter、silent fallback、双 consumer 和双 writer。也禁止让 Node 写一部分表、Go 写另一部分表，或让 jobs 先写 Business SQLite 再由 gateway 补偿。新 owner 未达到 readiness 时必须拒绝该 mutation；不能回落到旧 Node writer 并把结果描述为 Go 完成。

切换只能在已 drain 的单 owner epoch 内进行：旧 owner 停止接收 mutation，完成或显式中止在途事务与 consumer，确认该物理文件无旧 writer 后，新 owner 才可通过同一 epoch readiness 接管。Node active-path-zero 是切换后验收条件，不是允许并行写入的替代条件。

### 9.5 SQLite/PG 切换与回滚

J3b SQLite 回填完成后，必须执行 maintenance 的只读 `--verify-j3b-model-check-sqlite-backfill` readback。该命令对 dataset、stats 与专属 target 做 regular-file/物理路径隔离检查，验证三方 `query_only`、mandatory/optional 表、行数和按源列投影的 SHA-256 digest；报告同时输出每张表的 `ignoredSourceColumns`，明确记录 legacy 源端存在但专属目标未映射的列，不能将公共投影摘要当成完整字段迁移。回填写入端遇到 mandatory 表的 source-only 列会在打开/提交部分投影前直接 fail-closed 并回滚，避免先写入丢列数据再由 readback 拦截。漂移、共享文件、只读边界或非完整投影均以退出码 3 fail-closed。readback 报告作为切换及 rollback epoch 证据，不能替代 Node active-path-zero 或完整三库回填验收。

SQLite 和 PostgreSQL 都是正式目标模式，必须分别保持唯一 writer、schema 生命周期、权限、事务语义和恢复证据；通过 PostgreSQL 不能替代 SQLite handoff，反之亦然。PostgreSQL runtime 只做权限/schema/readiness 预检，DDL 归 B0 `maintenance` 的显式流程；SQLite 同样不得由 runtime 隐式建表或修复 schema。

每次 owner 切换前必须固化 owner manifest、epoch、版本兼容性、drain 状态、数据库备份/恢复点、健康/readiness、精确路由和回滚责任人。回滚只能整组恢复到先前已验证的单 owner：先停止新 owner 的 mutation/consumer，处理或隔离在途 command，再恢复旧 owner、路由与相同物理文件的写权限，并验证 revision/fence、session touch、lease、outbox、J3b/J3c 相关投影和 freshness。仅回滚镜像、开关或 Git 提交而未恢复 writer epoch，不构成有效回滚。维护命令的 owner-manifest verifier 还会强制解析 `operation_source_contract`，要求 `cutover_epoch_required`、`drain_required` 与 `rollback_requires_stop_and_replay` 明确为 `true`；这只是静态契约完整性校验，不是实际 handoff 证据。

### 9.6 J3b/J3c 边界、L2 准入与验证矩阵

J3b 只拥有模型检测 input/outcome、run/item/observation、trust/quality 事实及已形成证据后的直接质量投影。物理存储固定为 `JUHE_AI_J3B_DATABASE_PATH` 指向的单一 SQLite 文件，或 PostgreSQL `juhe_j3b` schema；`account_quality_health_hourly` 与 health-sync retry source 同属该 J3b 存储，不再写 Node stats/dataset。J3c 独占 usage 统计刷新和账户失败前置确认，只能通过只读 `J3bHealthReader` 消费 J3b 发布事实，不得打开 J3b writer。J3b 不得消费未形成的 score 推断处罚、恢复或 health-sync，也不得把 J3c worker 当作 J3b 的兼容出口。旧三库数据迁移、回放、cleanup consumer 下线和 rollback epoch 必须在 L2 证据中逐项完成。

OpenAI OAuth refresh 与 Codex usage snapshot 也不属于 J3b：refresh 必须由 Gateway 在同一进程内完成 lock、凭据解密、直接上游交换、CAS、失败处置和缓存失效，禁止 opaque handle 或任何 IPC/bridge；真实上游响应头只能先经固定白名单解析为 typed usage observation，再由独立 stats owner 完整持久化。J3b 不得接收原始 headers、代写 stats observation，或以该路径宣称 OAuth/J3b owner 已完成。

| L2 准入项 | 必须证明的结果 | 最低验证 |
| --- | --- | --- |
| 写路径归类 | Business SQLite 全量 Node writer、DB-service command、启动/worker/scheduler/maintenance 写路径均有唯一归属；切换后 Node active path 为零。 | 源码、启动配置和路由精确扫描；运行时 writer/connection 审计。 |
| 单 owner 切换 | SQLite 与 PG 均只出现一个物理 writer，旧 owner drain、epoch/fence 和恢复顺序可观察。 | 隔离 SQLite 与 PostgreSQL/PgBouncer 并发 handoff、重启和整组回滚演练。 |
| 进程内调用边界 | J3b 不存在 gateway/jobs transport；共享 Go 包不能自行打开 Business SQLite、发起调度或制造第二个 owner。 | 静态依赖扫描、进程内 command ID/revision/fence/digest/CAS/replay 与故障注入测试。 |
| J3b 完整功能 | JSON/SSE、三类 trigger、直接上游 probe、input/outcome、lease/fence、quality projector、recovery/health 的语义均完成。 | Node golden、成功/拒绝/取消/断连、busy/replay/stale、上游/代理/timeout/partial failure 及双模式集成。 |
| J3b/J3c 隔离 | J3b 质量投影与 J3c usage/失败前置确认无双写、无借用重试来源、无交叉回滚。 | 表/worker/路由 ownership 审计，跨功能故障与回滚回归。 |
| 发布与回退 | GitOps 精确路由、readiness、owner manifest、Node active-path-zero、备份和恢复顺序一致。 | candidate、生产 handoff、重启、回滚和 freshness 证据；未取得环境证据不得称为完成。 |

以下范围仍未被本契约覆盖，不能随 B0-B4 顺带接管：chat 业务写入、`codex-context` 的存储/命令语义，以及任何未另行授权的跨项目 typed command。它们需要各自的 owner 清单、数据/外部副作用边界、兼容性与回滚契约；在此之前保持现有 owner，且不得作为 J3b SQLite L2 的隐式依赖或 fallback。J3b 不因这些未覆盖范围获得 gateway/jobs 跨进程调用的例外。

### 9.7 当前实现校正（2026-08-28）

后文阶段性记录中出现的“尚未接入主进程”仅表示当时的中间状态；当前源码已在 Gateway 主进程完成 Business source、auth、enforcement、Host 和三类 scheduler 的装配代码，但仍受 readiness gate 保护，未授权开启 listener。新增 `/auth/captcha` 已对齐 Node 的 5 分钟一次性 challenge、每 IP 每分钟 60 次生成限频和登录必填校验；验证码与登录失败限流均为进程内实现，尚未达到共享 runtime-state 的多实例生产条件。Gateway Host 现在还会在打开 J3b Store 前强制要求版本化 `Tokenizer` 与 `ModelLimitSnapshot`，缺失时直接 fail-closed，避免 full profile 将 token integrity/long-context 静默记为 skipped 后误进入可运行 owner。

该校正不改变 L1/L2 结论：Business SQLite 全量 writer handoff、J3b 三物理库 backfill、完整 observation/trust 形成、J3c 只读审计、Node active-path-zero、隔离切换/回滚与归档仍未完成，任何一项未通过都必须保持 fail-closed。Gateway 打开 Business SQLite owner 连接时还会显式启用连接级 `foreign_keys(1)`，与 schema 中的级联/关系约束保持一致；该设置只强化运行时约束，不代表 handoff 已完成。

当前 `juhe-ai-jobs` 配置加载在任意 `JUHE_AI_MODEL_CHECK_ENABLED=true` 情况下均直接 fail-closed，不再接受 PostgreSQL 或 SQLite 的 J3b owner 配置；jobs 中保留的 model-check 包仅供迁移参考和单元测试，不能通过环境变量重新成为第二运行时 owner。

### 9.7.1 固定截距基线激活管理 API（2026-08-28）

Gateway 已补齐 `POST /model-checks/token-intercept-baselines/activate` 的进程内管理入口。请求仅接受 Node 契约中的 `cohortKeyHmac`、`requestedModel`、`tokenizerVersion`、`probeSetVersion`、正整数 `baselineVersion`、非负有限 `strongThresholdIntercept` 和 1--500 字符 `calibrationNote`，未知字段、类型错误或超限统一返回 400；入口仍沿用 Gateway admin session，未认证/非管理员分别返回 401/403。激活只允许专属 J3b 表中 `calibration_pending`、`evidence_status=stable` 且独立来源数不少于 10 的候选，且阈值不得低于 q90；旧 active 版本退役和候选激活在同一事务内 CAS，竞争/过期候选返回 409，存储或 schema 不可用返回 503。该表由 B0 maintenance schema 流程显式预置，Gateway runtime 不建表、不调用 Node/IPC/bridge；表未预置时仍 fail-closed，不能把 API 通过视为迁移完成。

证据完整性例外：只有探针明确声明 scope 不适用且 reason 属于 `juice_scope_not_applicable` 或 `trusted_comparison_not_attached` 时，Gateway 才将该 family 记录为可审计的 neutral/skipped receipt，并从形成度分母剔除；`tokenizer`、`model-limit`、usage、identity 或其他未列入白名单的 `excluded` 仍视为 evidence insufficient，禁止形成 quality/trust fact、处罚、恢复或 health-sync。该规则用于保持 Node 的合法排除语义，不放宽缺失或 partial probe 的 fail-closed 门禁。

### 9.7.2 J3c 只读边界审计（2026-08-28）

Gateway 新增 `internal/j3creadonly.Reader`，将 `modelcheckowner.HealthReader` 投影为 J3c 专用的 `PublishedHealth` 值对象。该包不持有数据库连接、不暴露 `ApplyHealthFact`、`MarkHealthSync` 或 baseline/enforcement 写方法；每次读取都必须带 `accountID + statHour`，缺失事实、范围漂移或不完整字段均 fail-closed。定向 `go test -race ./internal/j3creadonly ./internal/modelcheckowner` 覆盖单次限定读取、缺失事实、非法事实、空 scope 和 nil source。maintenance 的 `--verify-j3c-readonly-boundary` 还会用 AST 检查该适配包的方法面，并列出仍存在的 Node J3c owner 文件；当前 Go boundary 通过，但命令按预期以退出码 3 报告 Node owner 尚存。

这只是 J3b -> J3c 的进程内只读契约和静态审计证据，不是 J3c owner 接管。`account-quality-refresh`、`account-quality-failure-precheck-queue` 及其 Node writer/worker 仍未迁移；在 J3c 自身契约、双模式实现、回滚和 active-path-zero 完成前，不能启用该 reader 作为生产消费者，也不能删除或归档 Node J3c 路径。

### 9.7.3 Account circuit Redis owner 接线（2026-08-28）

Gateway 新增 `internal/business/circuit_runtime`，通过 `go-redis/v9` 直连 Node 兼容的 `juhe-ai:<namespace>:account-circuit:gateway-account-circuit:*` 键空间，并移植完整的 suspect/open/recovering/half-open、lease、父子升级、due、revision、incident restore、outbox revision projector 与 runtime-index backfill Lua/typed contract。J3b owner 启动时在 handoff/schema/Node-writer 三道门满足后执行 Redis ping 与 `runtime-index-meta(version=1,status=ready,ownerMode=go-runtime-state-v1)` 只读校验；健康端点新增 `accountCircuitRuntimeReady`。Owner facade 对所有 runtime mutation 先执行该 fence，无 Node bridge、IPC 或跨进程补偿。

同批新增 `internal/business/key_model_runtime`，按 Node 的 `CapabilityKey` canonical identity 实现 `OPEN/RECOVERING/HALF_OPEN/CLOSED` 状态、三次成功恢复、固定退避、foreground admission、lease、main-probe fence 和 J1 限频；Memory 版本只用于单元测试，Redis 版本使用 `gateway-account-circuit-key-model:*` 兼容键和 Lua 原子操作，并要求同一 owner gate。`key_model` durable incident 的六个身份字段也已接入 circuit runtime scope/wire/restore，避免恢复时丢失能力身份。

该接线仍未闭合真实上游 dispatch、`key_model` foreground/recovery 调用者接入、Business SQL outbox reader/projector 的当前模块适配、双模式集成和 Node active-path-zero；当前已补充 `circuit_control_plane.ListDispatchRevisions` 与 runtime-index reader seam，但尚未执行 backfill/publish。Node circuit/key-model runtime 仍保持 active，不能删除或归档，`migration-backup` 也暂不新增该路径备份。

### 9.7.4 Key-model Redis 互操作修正（2026-08-29）

复核 Node `redisNamespacedKey` 与 Go adapter 后修正两项确定偏差：Go `RedisStore` 现在同时接受 `dev-*` 短 namespace 与 `juhe-ai:dev-*` 完整 namespace，并统一生成 `juhe-ai:<namespace>:gateway-account-circuit-key-model:*`；`RecordMainProbeFence` 改为与 Node 相同的 Lua 原子操作，在写 fence 的同时释放实际 attempt lease、移除 admission zset、递增 wake 并发布唤醒事件。`RecordFailureIntent` 将 receipt identity 与实际 foreground permit identity 分开，失败记录会按 permit 的真实 attempt ID 恰好释放，并发布同一 wake 事件；恢复 Lua 保留 `retryAtMs`、generation、dispatchRevision 和 distributed lease fence。

Go 定向测试覆盖短 namespace、main-probe permit 释放、failure intent permit 释放和 wake sequence；Node `test:key-model-runtime`、`test:key-model-redis-store`、`test:key-model-j1-fence` 继续通过。上述修正只证明状态原语和 Node/Go Redis wire 兼容，不改变本节未完成项；没有真实 gateway request caller、双模式生产 smoke、Node active-path-zero 和可恢复备份前，不得删除 Node。

2026-08-29 复核证据：`go test -count=1 -p 1 ./...`、定向 `go test -race` 与 `go vet ./...` 均通过；`-verify-gateway-route-owner-manifest` 仍报告 22 个业务族中 21 个缺失、1 个 partial；`-scan-node-j3b-active-path` 仍报告 143 个 blocked findings。审计结果确认本轮没有误删 Node 活跃路径，`migration-backup` 继续保持为空。

### 9.7.5 真实 dispatch 调用链与 active-path 复核（2026-08-29）

独立只读审计重新核对了实际调用者，而不是只检查 Go 类型或启动装配。Node `backend/src/modules/gateway/dispatch/upstream-dispatch.ts` 在约 989--1071 行为每次上游尝试调用 `prepareGatewayKeyModelAttempt`，并在 transport、响应、取消及异常路径调用 `reportUnknown`、`reportUpstreamNotComplete`、permit renew/release；同一文件约 466--508 行调用 account circuit `prepareAttempt`，随后将 confirmation/half-open lease 与 `reportTransportFailure`、`reportFramingComplete` 等终态交给响应 owner。该文件仍是 key_model/account circuit 包住实际上游请求的生产调用链。

同次扫描维护命令 `-scan-node-j3b-active-path` 覆盖 966 个 Node TS/TSX 文件，返回 `findings=143`、`blockedFindings=143`、退出码 3；分类为 `dataset-writer=80`、`health-writer=23`、`management-route=20`、`business-command=14`、`management-proxy=3`、`scheduler=3`。Go `backend-go/projects/gateway` 只存在 runtime/store/projector/runner 与主进程 readiness 装配，未发现生产 `gatewaydispatch`、`gatewayupstream`、请求 listener 或调用 `AdmitForeground`、`RecordFailureIntent`、account circuit transition 的真实 caller。故 Go runtime 仍是已验证能力而非生产 owner，active-path-zero、Business handoff、Node 归档和 `migration-backup` 创建继续禁止。

本次结论不以增加空壳 caller 或静态 allowlist 解决；下一阶段必须在同一 Gateway 进程内实现真实 ingress、候选/凭据解析、上游 dispatch、响应/usage/audit 终态，并把 key_model 与 account circuit 的所有 admission、lease、failure、success、unknown、取消和重试边界接入后，再以 Node golden、SQLite/PG 双模式和 owner manifest 证据复核切换。

本轮在 `backend-go/projects/gateway/internal/business/gateway_dispatch` 增加了同进程 typed upstream-attempt owner：真实 `http.Client` 调用前执行 key-model foreground admission，transport 或缺失 body 进入 unknown 终态，响应 body 由调用方显式消费并释放 permit，并提供 renew/release 接口。该包不创建 Node/IPC/跨进程依赖，也不持有候选、凭据或 HTTP listener；它是可测试的接线基础，不等价于生产 ingress 已注册。对应契约测试已通过，但在真实候选解析、account circuit transition、response/usage/audit owner 和 listener 接入完成前，仍不得宣称 Go 唯一 owner。

### 9.7.6 本轮 Gateway 收敛与未通过门禁（2026-08-29）

本轮新增的分组可见性规则：同 owner 分组直接允许；跨 owner 分组必须具备有效的 group `resource_authorizations(scope='use', status='active', 未过期)`，否则保持 fail-closed。该规则已有 Go SQLite 定向回归，但真实 PostgreSQL 与完整代理/type 语义仍未验证。

本节只记录本轮本地源码及命令验证证据，**不构成切换授权，也不表示 Gateway 已成为生产唯一 owner**。Gateway 侧本轮已收敛以下可独立核对的边界：quality health 投影只接受 `failed`、`suspicious` 或 `unavailable` 结果，成功质量结果不能写 health；`suspicious` 属于硬质量失败，即使分数高于阈值也必须沿健康/处罚路径处理；调度恢复必须同时满足 completed、evidence/trust 已形成、score 达到阈值且 level 非 `unavailable`；单个 scheduler 完成处理错误被隔离，不能直接终止 owner 调度循环。请求构建继续冻结 account scope，并沿用既有 query contract；stat-hour 取自 Business 设置而非进程本地时区推断。新增的 Gateway ingress boundary 仅提供进程内接线边界，尚未注册为真实业务入口。maintenance 还补充了 PostgreSQL readback CLI 和显式确认保护的 PostgreSQL backfill CLI；两者都只提供可审计的一次性工具入口，未连接生产数据库、未执行真实回填，也不能替代三库 readback、唯一 writer handoff 或 Node active-path-zero。

本轮进一步收敛 Business target/options 的授权实例路径：`run/history` 已按 `resource_authorizations`、source account、viewer 授权分组绑定和 active/expiry 条件执行只读 UNION，目标解析从 source account 读取 provider/profile/凭据并将 `OwnPhysicalAccount=false`；`schedule` 仍保持 owner-only。账户模型选项和目标模型解析均以 source account 的 `account_supported_models`/`account_model_mappings` 为事实来源。缺失授权记录、source 或绑定时统一按 404 隐私语义关闭。新增的 `account_expires_at`、`cooldown_until`、`last_error_code` 已纳入 Go 只读 contract 和 run/target 的 fail-closed 可用性判断；授权实例的 CAS/dispatch revision 保留实例行版本，物理 source 的 config/dispatch revision 也进入 target/request 复核；熔断/key-model 的 credential source 使用物理 source account。授权过期判断按 SQLite `datetime(TEXT)` 与 PostgreSQL `::timestamptz` 分方言生成。options 查询现在在 SQL 层过滤固定 model-check profile catalog，并在授权多分组绑定时 `DISTINCT` 去重。该实现仍未覆盖 API-key pool、quota project、单一一致性快照和真实 PostgreSQL 双模式 smoke，因此不代表 authorized-account 能力已完成或可切换。

Gateway target 现已接入受限的进程内 proxy client：读取并校验 `proxy_profiles` 的 enabled、host、port、协议，`socks5` 归一为 `socks5h`，可选密码只在内存中解封装；代理 profile 缺失、停用、协议非法或密码不可解封时 fail-closed，禁止静默退回直连。授权 target 按 Node 语义优先使用 source proxy；source 未配置 proxy 时回退到 virtual instance proxy。当前仅覆盖 J3b target 的单代理 profile，不覆盖 API-key pool、OAuth refresh、quota project，完整代理/type 语义仍是上线前门禁。

本轮进一步纳入 source `type` 到 target snapshot，并按协议生成认证头：Anthropic API key 使用 `x-api-key`、OAuth 使用 Bearer；OAuth 请求同时带固定 Claude CLI identity 与 OAuth beta headers；GLM Coding Anthropic API key 使用 Bearer；Gemini native API key 使用 `x-goog-api-key`、仅 `google_oauth` 使用 Bearer，并在凭据包含 `quota_project_id` 时发送 `x-goog-user-project`；OpenAI-compatible profile 仅接受 `api_key`/`oauth` 并拒绝 `google_oauth`，Anthropic 也拒绝 `google_oauth`，Gemini native 拒绝普通 `oauth`。未知 credential type 或未知 JSON 字段 fail-closed。Go 当前没有 OAuth refresh owner：OAuth/Google OAuth 的结构化凭据缺少 `access_token` 时（即使存在 `refresh_token`）直接拒绝，不把 refresh token 当 Bearer；凭据中的合法 `base_url` 优先于 profile base URL。仍未覆盖 API-key pool、OAuth refresh/Codex adapter、完整 quota project 与所有 provider/type 接管。

Account options 与 target 解析现在共享同一类型边界：列表查询仅返回 `api_key`、`oauth`、`google_oauth`，未知账户类型不会先出现在可运行选项中再于提交阶段失败。该边界仍不代表完整 OAuth refresh、API-key pool 或所有 provider/type 组合已迁移。

本轮追加的本地契约修复：model-check probe 的 Dispatcher 现在接受每次 target 解析得到的 `http.Client` 覆盖值，代理 client 会沿着 Dispatcher/ProbeAdapter 实际请求路径生效；trusted comparison 按显式 Suite owner 选择请求，不再用共享 endpoint URL 推断 owner，避免同 endpoint 账户的凭据、代理和 header 串用。Store、shared schema contract 与 SQLite bootstrap readiness 同步要求 `model_check_items` 的详情列（score/max_score/duration_ms/trace_id/error_code/error_message）以及 health 的 `error_code/error_message`，缺列会在 readiness 阶段 fail-closed。PostgreSQL readback 对 source-only 未映射列拒绝 Ready；SQLite readback 保留既有公共投影兼容语义（legacy extra columns 仍需由现行契约确认），并显式输出 `sourceReadOnly`、`statsReadOnly`、`targetReadOnly` 三方状态，任一未满足则保持未就绪。上述均为本地代码和定向回归证据，不代表真实代理、数据库或生产环境已验证。

本轮已记录的本地验证包括 Gateway 的 `go test -count=1 ./...`、`GOMAXPROCS=2 go test -race -p 1 -count=1 ./...` 与 `go vet ./...`，以及 maintenance 的等价 `go test`、受限并发 `go test -race` 和 `go vet`。首次使用默认并行度的 Gateway 全量 race 构建因 Windows `VirtualAlloc errno=1455` 资源耗尽而中止，受限并发复跑通过；这不构成生产并发或资源容量证据。这些结果只覆盖当前源码和相应测试，不覆盖真实候选、凭据、上游响应或生产切换。维护扫描的最新结果仍为未通过：`juhe-ai-maintenance -scan-node-j3b-active-path` 返回 `blockedFindings=161`；`-verify-gateway-route-owner-manifest` 仍将 `model-checks` 列为 pending。它们与本节前述的历史 `143` 项扫描共同说明 Node 活跃路径没有清零，不能以任一局部 Go 测试替代 owner 交接证据。

以下门禁仍必须逐项通过：真实 ingress 的 candidate/凭据解析与响应、usage、audit 终态使用审计；`/model-checks` 及相关管理路由的完整 scope/错误/事务语义对照；dataset、stats、专属 J3b 三物理库的回填与 readback（包括 trust/token facts）；Business SQLite 的唯一 writer handoff；外部 GitOps 的 candidate、Node drain、owner epoch 与整组 rollback 演练；以及 active-path-zero 的最终扫描。上述任一项缺失时，严禁删除或移动 Node 活跃路径，`migration-backup` 也不得创建为提前归档或被当作已完成证据。

### 9.7.7 BuildRequest 显式 target/source/mapping fence（2026-08-30）

`BuildRequest` 现对首次解析与复核解析之间增加事务内的 canonical fence digest。摘要覆盖目标账户和物理 source account 的配置、传输、可用性、凭据密文、provider profile、proxy profile、授权/分组绑定、授权记录、supported models 与 model mappings；摘要只保留 SHA-256，不把凭据写入 request。复核阶段同时比较完整 `Target` 语义（endpoint、provider、upstream model、credential type/source、dispatch/config revision、协议和认证头），trusted comparison 也执行同样的双读 fence。这样可拒绝不会推进 virtual instance `config_revision` 的 mapping、source credential、proxy 或 grant drift。

该 fence 是请求构建阶段的显式漂移检测，不等价于把后续上游调用与 Business writer 串行化；最终 dispatch/enforcement 仍必须依赖 owner epoch、CAS 和 source revision，单一 repeatable-read/生产 handoff 仍是上线门禁。当前 `Target`/`RunRequest` 还会显式携带授权物理 source 的 `SourceConfigRevision` 与 `SourceDispatchRevision`，并在复核解析和 Runtime 入口校验；virtual instance revision 仍是对外目标 CAS，不能被 source revision 替代。当前定向回归已验证 mapping、credential drift 及 source revision mismatch 会 fail-closed；未执行真实 PostgreSQL 或生产切换验证。

### 9.7.8 结构化 cutover evidence 只读门禁（2026-08-30）

maintenance 新增 `-verify-j3b-cutover-evidence <json>`，只读取外部生成的结构化证据，不连接业务数据库、不停止进程、不修改 owner 状态。校验器要求旧/新 owner 与 epoch、drain 完成且 `inFlight=0`、Node `activePathZero` 且 `blockedFindings=0`、备份文件及 SHA-256、rollback replay cursor 和带最大年龄的 RFC3339 freshness；此外必须引用一个版本化 J3b readback manifest。manifest 以文件 SHA-256 绑定到 evidence，包含 source snapshot identity、source/target schema、固定 legacy-facts scope、producer、完整投影、验证时间及逐表行数/source-target digest，并自行保存 canonical manifest hash。旧 `sourceDigest`/`targetDigest` 字段仍可读取，但不再能单独使授权 `Ready=true`；缺 manifest、缺表、行数或摘要漂移、lossy projection、格式/scope/aggregate hash 不符或过期均以退出码 3 fail-closed，证据文件不可读以退出码 2 返回。该文件校验只防止本地用相等标量 digest 伪造 readback 通过，不证明真实数据库快照、drain、epoch、备份可恢复性或 rollback 环境；这些仍须由真实 Business SQLite 唯一 writer、三库回填/readback、Node drain、owner epoch、GitOps rollback 和生产恢复演练提供证据。当前仍未取得任何真实环境证据。

Gateway 在启用 J3b 且确认 Business handoff 时，必须提供 `JUHE_AI_J3B_CUTOVER_EVIDENCE_PATH`；启动会在打开 Business 数据库和 management listener 之前读取并验证该文件，要求 `newOwner=go-gateway` 且 `ownerEpoch` 与 `JUHE_AI_J3B_OWNER_EPOCH` 完全一致。缺失、不可读、过期或任一证据门失败均保持 fail-closed；这项绑定只证明启动前消费了外部证据，不代表当前环境已有真实 handoff，也不允许绕过后续 Node active-path-zero、三库 readback 和 rollback 门禁。

J3b PostgreSQL/SQLite backfill 另要求显式 `-j3b-backfill-evidence <json>`。该前置校验复用同一 owner/drain/active-path/备份/freshness 结构，并同样要求已核验的 readback manifest binding；旧 scalar digest（即使相等、即使 target digest 为空）不会授权 backfill/cutover。缺失、不可读或未就绪时，在打开目标数据库前以退出码 2/3 fail-closed。回填完成后仍必须运行上述 `-verify-j3b-cutover-evidence`，且不得把文件一致性误当作真实 drain、epoch 或可恢复 rollback 证据。
Gateway 的 J3b 配置在 `BUSINESS_HANDOFF_CONFIRMED=true` 时还必须显式提供 `JUHE_AI_J3B_OWNER_EPOCH`；该值只是启动配置中的不透明标识，不能替代上述证据文件对 epoch、drain 和 freshness 的真实性校验。

SQLite readback 报告现在分别输出 `sourceReadOnly`、`statsReadOnly` 与 `targetReadOnly`，三者任一缺失或未处于 `query_only` 均保持 `ready=false`；这只是机器可核验的读回状态，不代表三库真实回填或唯一 writer handoff 已完成。

账户 `availability_schedule_json` 的时区、跨日窗口、日期范围和例外解析已在 Go recovery 原语中与 Node 规则对齐；Gateway owner/authorized Resolve 及 run options 现在也会读取并校验实例与 source 的计划，普通运行在计划窗口外 fail-closed，quality recovery 保留既有隔离账户例外。列表查询仍使用有限候选窗口并依赖 Business 状态同步提供排序/分页基础，因此 schedule worker 的边界执行、source/实例双侧一致性和真实运行证据仍需在 handoff 前核验，不能把本地定向回归当作生产 schedule 接管证明。

`-verify-business-sqlite-handoff` 的既有 `ready` 字段仅保留为路径隔离预检的兼容别名；报告现同时输出 `pathIsolationReady` 与始终为 `false` 的 `handoffReady`。它不打开用户数据库，因而不能观察真实 writer、drain、epoch、备份或回滚，任何切换自动化都必须使用结构化 cutover evidence，而不得以路径预检的 `ready=true` 作为 owner handoff 结论。

### 9.7.9 当前工作区定向验证（2026-08-30）

本轮在不访问 `.local`、真实数据库或生产服务的前提下，并行完成了迁移相关 Go 包的定向验证：Gateway `modelcheckowner`、`modelcheckprobe`、`gateway_dispatch`、`gateway_ingress`、Gateway command，maintenance `businesshandoff`、`j3bmodelcheck`、`ownermanifest`、maintenance command，shared contracts 以及 jobs `runtimelog` 的 `go test -count=1` 均通过；对应包的 `go vet` 与 `git diff --check` 也通过。该验证只证明当前源码和测试夹具的一致性，不替代真实三库 readback、唯一 writer、上游/凭据、Node drain、active-path-zero、owner epoch 或 rollback 证据；按迁移策略，统一全量 test/race/vet 仍待所有外部门禁闭合后执行。

PostgreSQL readback 现复用 backfill 的完整列投影校验，同时检查 source-only/target-only 列、数据类型、底层 UDT 和 NULL 约束；任何结构漂移在摘要比较前即保持 `ready=false`。因此 readback 不再仅凭同名公共列的 digest 生成完整迁移证据；仍不代表真实 source 冻结、writer 停止或恢复点有效。

### 9.7.10 Gateway 运行时 schema gate 与 circuit 索引语义收敛（2026-08-30）

复核发现 Gateway 启动路径的 `CheckBusinessSQLiteSchema` 仍只按索引名称检查，可能绕过 shared v11 contract 对 circuit key-model capability 索引及关键 PK/UNIQUE 约束的结构要求。本轮已将运行时 gate 与 maintenance verifier 统一：`account_circuit_incidents` 的 `idx_account_circuit_incidents_key_model_capability` 必须是 `UNIQUE(scope_kind, capability_hash) WHERE scope_kind='key_model' AND capability_hash IS NOT NULL`，同时核对 SQLite `PRAGMA index_list/index_info`、列顺序和 `sqlite_master.sql` predicate；`system_accounts.username`、`system_settings(system_account_id,key)`、`model_quality_schedules(system_account_id,account_id)`、`account_quality_enforcements(account_id)`、`account_circuit_incidents(circuit_scope_key)` 的 PK/UNIQUE 结构也按列顺序 fail-closed 检查。SQLite 单连接池场景在关闭游标后再执行后续查询，避免 schemaReady 阻塞。PostgreSQL circuit contract 同时限定目标 schema/table，并要求 key 列数量与索引总列数量精确一致，拒绝错误表、额外 included/expression 列和 predicate 漂移；关键 PK/UNIQUE 通过 `pg_catalog.pg_constraint` 核对。

新增 Gateway 运行时 schema fixture 覆盖正确唯一 partial index、错列、非唯一和缺 predicate 四类 fail-closed 情形；`modelcheckowner`、`circuit_control_plane`、maintenance handoff 与相关 `go vet` 定向验证通过，`git diff --check` 通过。本节仍只是启动前结构证据，不代表 Business SQLite 已完成唯一 writer handoff、真实 PostgreSQL 验证、Node drain 或生产切换；统一全量测试继续留到全部外部门禁闭合之后。

### 9.7.11 Gateway API-key 授权过期时间方言收敛（2026-08-30）

`account_runtime.ValidateGatewayAPIKey` 的跨租户分组授权查询现按 Business schema 的 ISO-8601 文本字段生成方言条件：SQLite 使用 `datetime(column) > datetime(?)`，PostgreSQL 使用 `column::timestamptz > ?::timestamptz`。这样带时区偏移的授权不会按字符串顺序误判；新增定向回归覆盖有效授权与过期授权的入口行为。该修复仅收敛本地查询语义，仍不提供真实授权数据、唯一 owner 或生产切换证据。

### 9.7.12 Gateway API-key 分组授权 scope 与 schema 前门（2026-08-30）

`account_runtime` 的跨租户分组绑定现要求 `resource_authorizations.scope='use'`，与模型检测候选及 Node 授权语义一致；`CheckContract` 同时显式检查 `resource_authorizations` 和 `group_authorization_settings` 两张直接查询依赖。定向回归覆盖非 `use` 授权被拒绝，避免只拥有读取权限的授权进入 Gateway 路由。该前门仍只是代码级 fail-closed 检查，不替代真实 owner handoff 或授权数据审计。

### 9.7.13 Gateway health timezone schema dependency（2026-08-30）

Gateway 启动时的 `LoadBusinessHealthStatHour` 会读取 `system_settings(system_account_id,key,value_json,updated_at)`。该表及其指向 `system_accounts` 的级联外键、复合主键已纳入 Business SQLite `v11` shared contract，并由 Gateway/maintenance 两侧 schema verifier 共同检查；缺表、缺列、缺外键或错误约束均保持 `schemaReady=false`。这仍是结构门禁，不代表真实 Business handoff 或统计配置已在目标环境验证。

### 9.7.14 Scheduler source revision payload fence（2026-08-30）

周期调度与质量恢复任务的不可变 `ScheduledPayload` 现同时保存目标 `configRevision/dispatchRevision` 对应的物理 source revision。claim 阶段从账户（授权实例则从 `authorization_instance_source_account_id`）读取 source config/dispatch revision；Gateway scheduler builder 将其作为 `RunRequest.SourceConfigRevision/SourceDispatchRevision` 传入 `Resolve`，Runtime 在执行入口再次校验，排队期间 source 凭据或 dispatch 配置漂移时 fail-closed。旧 payload 缺少 source 字段时由 scheduler executor 直接拒绝，不伪造版本，需重新 claim 生成新 payload。该修复仅覆盖进程内调度快照，不能替代 durable owner epoch、writer 串行化或生产 handoff 证据。

### 9.7.15 SQLite backfill canonical value encoding（2026-08-30）

SQLite 回填与冲突比较的 canonical 编码现明确区分 `NULL`、文本/驱动返回的字节文本、整数、浮点和布尔值；`NULL` 不再与字面量 `<nil>` 混淆，整数 `1` 也不再与浮点 `1` 混淆，同时保留 PostgreSQL/SQLite 驱动间 bytes/text 的既有等价语义。新增定向回归覆盖这些碰撞边界。该修复只提高摘要与冲突检测的本地准确性，不能替代 source 冻结、唯一 writer 或真实 readback 证据。

### 9.7.16 J3b Business endpoint mode source contract（2026-08-30）

Gateway target Resolve 现把 `accounts.health_check_endpoint_mode` 作为虚拟目标账户选择的探测形态；授权实例的 provider、凭据和 `credentials.supported_endpoint_modes` 仍取物理 source account，与 Node/Jobs 的 effective-source 规则一致。选择值必须是 Go 已实现、与 source protocol profile 匹配且出现在物理凭据支持列表中的文本 mode；缺失、空值、畸形列表、`images_*`/`interactions_*` 等 Go 未实现选择值或不一致配置均 fail-closed。支持列表中的其他 Node-only mode 不会被重写，只要已选 mode 可执行即可保留为 target capability。shared Business SQLite schema 已升级为 `v11`，启动 schema gate 与 `CheckContract` 均要求 `accounts.health_check_endpoint_mode`；凭据 JSON 的结构仍由 resolver 在解密后严格校验，而不是交给 schema gate 推断。

### 9.7.17 OpenAI OAuth Codex probe adapter（2026-08-30）

J3b 模型检测现已在 Go 探针内支持 `provider_code=gpt`、`profile_gpt_openai_v1` 且 `type=oauth` 的 Codex upstream 适配。该形态只接受 `responses_json`/`responses_sse` 两种已实现的 health-check mode，固定请求到 Node oracle 对齐的 `https://chatgpt.com/backend-api/codex/responses`，并在请求尝试内规范 Responses `input`、`instructions`、`store=false`、`stream=true`、Codex client identity、session/thread metadata、`openai-beta=responses=experimental` 与 `chatgpt-account-id`（如凭据提供）。响应 JSON 与 `data:` SSE 的 bounded 终态均可被探针解析；API key 账户仍使用原有 profile base URL 与 `/v1/responses`/`/v1/chat/completions` 构造。

该适配只存在于 J3b probe path，不注册或改变 public ingress，也不实现 OAuth refresh、refresh-token exchange、Codex usage snapshot、浏览器/session 生命周期、Responses history sanitizer 的完整业务语义或任何公网 owner handoff。GPT OAuth 的 `chat_*`、images/interactions 及其他没有 Node Codex oracle 的 mode 会 fail-closed，不回退为普通 OpenAI 请求；凭据中的 `base_url` 不能绕过 Codex 固定 host/path。缺少 `access_token`（即使存在 `refresh_token`）继续 fail-closed。上述仅为本地源码与定向测试证据，不构成上游、生产数据库、Node active-path-zero 或生产切换完成证明。

### 9.7.18 PostgreSQL Business schema structural gate（2026-08-30）

Gateway 的 `CheckBusinessPostgresSchema` 现对 `juhe_business` 只读核验使用 `information_schema` 与 `pg_catalog`：要求对象为真实 `BASE TABLE`（普通表或分区表），逐列检查 v11 contract，按列顺序核对 PK/UNIQUE，按引用 schema/表/列顺序及 `ON DELETE`/`ON UPDATE` action 核对外键，并对登记的索引检查目标表、唯一性、key 列顺序、表达式/included 列和 partial predicate。错误表同名对象、未知 referential action 或结构漂移均 fail-closed；该 gate 不执行 DDL。尚无真实 PostgreSQL 集成环境证据，不能替代候选库 schema/readback 验收。

### 9.7.19 SQLite backfill lossless write gate（2026-08-30）

`BackfillSQLite` 在复制任一 J3b 事实表前比较 source/target 列集合；发现 source-only 列时立即返回错误并回滚整个目标事务，不再先提交公共列投影再依赖 readback 报告拦截。`VerifySQLiteBackfill` 仍输出 `ignoredSourceColumns` 与 `ProjectionComplete/Complete`，用于诊断历史或外部报告，但任何 source-only projection 都不能产生完整回填证据。该行为会要求目标 schema 先覆盖 Node 事实列，避免把列丢失隐藏在“回填成功”状态中；未连接真实三库，仍需在停写窗口验证 source 冻结、唯一 writer、逐表 digest 和恢复点。

### 9.7.20 Trust 聚合游标无损映射（2026-08-31）

J3b maintenance 现将 Node `juhe_stats.stats_job_state` 中唯一的 `scope_type='global'`、空 `scope_id`、`job_name='model-trust-observation-aggregation'` 游标映射到 Go `model_trust_aggregation_state`，保留 `cursor_created_at`、`cursor_id`、`last_success_at`、`last_error_message`、`lag_seconds` 和 `updated_at` 全部字段，并以固定 `scope_key` 做插入式冲突校验和 PG/SQLite readback 摘要。其他 Node `stats_job_state` 作业不会被误拷贝；`background_job_leases` 仍是 Node 调度所有权状态，必须作为独立 drain/epoch/唯一 writer handoff 证据处理，不能用历史数据回填代替。该映射已通过本地定向测试以及开发主库 PG 直连/PgBouncer 空状态 readback，但尚无真实停写窗口、三库数据快照或切换证据。

### 9.7.21 Dev PgBouncer Gateway runtime smoke（2026-08-31）

本轮在远端 dev Docker PostgreSQL 的一次性隔离库 `juhe_ai_sub2api_dev_j3b_dataset_projector_20260826` 中完成了真实 Go Gateway runtime smoke。测试通过 PgBouncer `6432` 连接，在目标 `juhe_j3b` schema 已预置后执行完整 probe，并核验 durable run、五类 observations、trust observation receipts、aggregation cursor 与 latest trust result 均已写入；测试结束后先定向终止该 disposable 库的 2 个空闲连接，再删除数据库并确认目标不存在。过程中未写入主库或 Redis，也未触碰生产。

同一 dev Docker 环境还在另一一次性 scratch 库执行了 PostgreSQL 非空 backfill/readback smoke：合成 `juhe_dataset` 的 run/item/observation、`juhe_stats` 的 health/baseline/trust/dirty/receipt 以及唯一 trust aggregation cursor 各 1 行，`BackfillPostgres` 在 serializable 事务内均插入 `1→1`，只读 repeatable-read readback 对全部 9 项报告 `match`，第二次 backfill 仅对同值主键行计为 skip。测试会删除其三个临时 schema，外层随后删除整个 scratch 库；它验证真实 PostgreSQL/PgBouncer 上的非空工具行为和幂等性。

### 9.7.22 管理员跨租户 scope 的 actor/target 分离（2026-08-31）

复核 Node 的 `getRequestAccessScope` 后确认，管理员请求的 `systemAccountId=all` 或无筛选代表未过滤的全局 scope，而不是字面量租户；质量策略/调度写入仍要求先选择具体租户。Go 现以 `ManagementScope{ActorSystemAccountID, SelectedSystemAccountID, AllSystemAccounts}` 显式传递该三态：管理员全局 scope 可读取全部 run/history 与自有账户候选，但不把授权实例提升为全局候选；显式 tenant 可收窄并合并可证实的授权实例；self 路径仍固定 actor 自身并忽略伪造参数。管理员发起 run 时 `BuildScopedRequest` 按主目标账户读取其 owner，run 持久化该 target tenant 并保留认证 actor；可信对照账户在 global scope 另行读取自己的 owner、fence 和 revision，仅作为同一 run 的检查输入，不产生对照账户 observation 或 trust projection，长期事实仍归主目标。active/stop 互斥键继续按 actor，避免一次管理员操作跨选择 scope 遗失自身活动生命周期；quality policy/schedule 对 global scope 明确返回 `400` 要求具体 tenant。定向 Gateway/Business/runtime 回归已覆盖 actor-target 分离、global account options/list、global run、comparison owner/fence，以及 comparison 不生成长期跨租户事实。

这只完成管理 scope 的局部行为对齐，不证明授权账户可见性、全量 DTO 脱敏、错误/事务 golden、真实认证、外部上游 usage/audit 或 Node/Go 双路径行为均已验收；Gateway route owner manifest 的 `model-checks` 仍为 `partial`，也不解除 Business handoff、三库 backfill/readback、GitOps rollback 或 active-path-zero 门禁。

对应的 SQLite 三物理文件回填回归也已对相同 9 项非空事实覆盖首次 `1→1` 写入、幂等重跑、只读 readback、源列漂移和共享路径拒绝。该回归使用 `t.TempDir()` 生成的 dataset/stats/target 文件，只证明 SQLite 工具和 fail-closed 边界，不是运行中 Node 数据库的停写窗口或正式三库迁移证据。

同期对 cache/state/queue 三个 dev Redis 实例的 DB `9` 鉴权 PING 均返回 `PONG`；J3b runtime/backfill smoke 没有写入 Redis，故该结果只证明依赖环境可用，不代表 Redis 状态迁移或 owner handoff 已完成。上述均不证明 Node drain、Business SQLite 唯一 writer、真实 Node dataset/stats/J3b 三库正式 backfill、真实上游凭据/usage/audit、GitOps rollback 或 active-path-zero。scratch 库最初因 PgBouncer 对尚不存在数据库保留缓存错误，数据库创建并预置 schema 后通过发送 HUP 刷新 PgBouncer，再次运行成功；该处理仅针对 dev PgBouncer，不应作为生产切换步骤。

### 9.7.23 Go run projection metadata 与 Dev PgBouncer 回归（2026-08-31）

Gateway `modelcheckowner` 现从 Business source 冻结并持久化 `targetName`、`targetOwnerSystemAccountId`、`groupId` 和独立 `traceId`；`trustedComparisonAvailable` 仅在比较目标完成独立 resolver、revision 和 fence 校验后写为可用，不再简单镜像请求开关。管理 run 列表/详情现读取 Node 对齐的 actor、目标/租户、schedule、trusted comparison、probe/set、started/finished/duration、错误和 policy/quality JSON 字段；durable JSON 仍经过对象校验与敏感信息脱敏。`go test ./projects/gateway/internal/modelcheckowner`、`go vet` 通过，并在一次性 dev PostgreSQL/PgBouncer scratch 库完成新增字段的 Gateway runtime/trust smoke，随后删除 scratch 库。

该增量只闭合 Go 内部的 run projection/DTO 缺口，不证明完整详情授权可见性、Node active-path-zero、Business SQLite 唯一 writer、真实三库停写回填、GitOps rollback 或 Node 归档条件。

### 9.7.24 Go full detail latest trust readback（2026-08-31）

Go `GetRun` 现与 Node `getModelCheckRun` 的 full-profile 详情行为对齐：在目标账户和模型匹配、run 未标记 `modelCheckUnverified` 且 profile 不是 `quick` 时，按 `(system_account_id, account_id, requested_model)` 读取 `model_account_trust_results` 的最新 durable projection，并将 `identityStatus`、`mappingStatus`、`usageIntegrityStatus`、`protocolStatus`、`evidenceStatus`、coverage/count、reason codes 与最后观测时间合并到 `resultSummary.trustReport`。若当前 run 已标记 `model_response_evidence_unavailable`，或为 `unavailable` 且没有本次 `observedModel`，则保持本次 run 摘要、不读取 latest；latest 行缺失、表暂时不可读或 JSON reason codes 畸形时同样保留已落盘的基础详情，不把读取失败伪装成新的可信结果；comparison tenant 仍不会被查询或投影。新增 SQLite durable-detail 回归、race 与 vet 通过。

这只闭合 Go 管理读路径的一项 Node parity，不证明 latest trust 的历史窗口/token/identity 事实已经从 Node stats 完整回填，也不解除真实 source freeze、唯一 writer、Node active-path-zero、owner manifest、GitOps rollback 或归档门禁。

### 9.7.25 Node-to-Go PostgreSQL non-empty backfill fixture（2026-08-31）

PostgreSQL `BackfillPostgres` 已修复一项会阻断真实 Node source 的过度门禁：legacy 表必须存在、列/类型/NULL/主键投影完整，且所有非空行仍须在 serializable transaction 中逐主键 insert-or-equal；但已验证的零行表不再被当作 fail-open 错误。Node 正常完成 trust aggregation 后，baseline、dirty queue 或 transient receipt 表可以合法为零，readback 仍要求 source/target 行数和 digest 均为 `0`，不会跳过缺表、结构漂移、超限、非空 digest 漂移或已有行冲突。

本轮在一次性 dev PostgreSQL/PgBouncer 库中以当前 Node storage repository 写入带唯一 marker 的 legacy `run/item/observation/health/latest trust/aggregation cursor` 事实，Node 进程退出后由 Go maintenance core 执行 backfill；前置只读 readback 为上述六项 `1→0` drift，首次 backfill 为 `1→1`，第二次为 equal-row skip，最终只读 readback 对九项均 `match`，其中 baseline/dirty/receipt 为 `0→0`。该流程有显式 opt-in、数据库名前缀限制的 Go fixture 覆盖，scratch 库和临时脚本均已删除。它证明同一隔离 PG/PgBouncer 中 Node writer 的数据形状可被 Go 工具无损读取和投影；不等于 Node active-path-zero、真实业务停写窗口、Business SQLite 唯一 writer、GitOps cutover、备份恢复或 rollback 证据。

### 9.7.26 Node 模型检测 golden 回归快照（2026-08-31）

本轮重新执行 Node 模型检测回归：可信对照、存储脱敏、不可用边界、full profile、严格模型匹配、多协议 profile、Token integrity 均通过。`model-check-user-authorized-resource-regression.ts` 连续两次在同一隔离临时 SQLite 夹具失败于固定断言 `detail.level === 'high_confidence'`，实际结果为 `likely`；失败发生在测试断言阶段，且本轮没有修改该 Node 脚本或 `backend/src/modules/model-checks/**`。因此这不是 Go 回填通过的证据，也不能被忽略为“全量 golden 已通过”；在 owner 复核评分输入（实际 score/探针状态）并决定修正 Node 行为或测试预期前，该项保持未验收。该失败不改变 Go 侧定向 test/race/vet 已通过的结论，也不解除 Node active-path-zero、唯一 writer handoff、真实三库停写回填、GitOps rollback 或归档门禁。

### 9.7.27 Readback manifest 扩展后的 maintenance 全量复验（2026-08-31）

将 readback required table 从五张扩展为九张后，首次 maintenance 全量测试发现 `businesshandoff` 的“complete”证据 fixture 仍缺少 `model_account_trust_results`、`model_trust_aggregation_state`、`model_trust_latest_dirty_accounts` 和 `model_trust_observation_receipts`；这是测试数据与现行契约的漂移，不是验证器放宽问题。本轮已只更新 `cutover_evidence_test.go` 的合法 fixture，未改变生产校验规则；maintenance、Gateway、jobs、shared contracts 和 platform 各 module 的全量 `go test` 均通过，各 module `go vet` 也通过。该修复只证明源码测试夹具与九表契约一致，仍不构成真实 readback manifest、writer handoff 或 rollback 证据。

### 9.7.28 Readback manifest 固定 scope fail-closed（2026-08-31）

`j3b-readback-manifest/v2` 现不仅要求九张固定 legacy fact 全部存在，还拒绝任何未列入 scope 的额外表；未知表不会被静默纳入 `manifestHash` 后当作可接受证据。新增 contracts 回归覆盖额外事实表，contracts、maintenance 与 Gateway module 全量 `go test`/`go vet` 通过。该校验仍只证明 manifest 自洽和 scope 完整，不证明数据库快照、Node drain、唯一 writer、备份恢复或 GitOps rollback。

### 9.7.29 Dev Redis 依赖连通性探针（2026-08-31）

使用 `.local/project-resources/dev/env/shared.env` 中的隔离连接配置，对 cache/state/queue 三个 dev Redis 实例的 DB `9` 执行只读 `PING`，结果均为 `PONG`。探针未执行写命令，也未改变 namespace、队列或租约状态；该证据只证明 Gateway/runtime 所需 Redis 依赖可达，不代表 Redis 状态迁移、租约 drain、唯一 writer handoff、GitOps rollback 或 Node 归档门禁已完成。

### 9.7.30 Dev 主 PostgreSQL 空状态 schema/readback（2026-08-31）

使用 dev `shared.env` 的显式维护连接执行 `-check-j3b-model-check-postgres` 与 `-verify-j3b-model-check-postgres-backfill`。结果显示数据库 `juhe_ai_sub2api_dev` 的 `juhe_j3b` schema/table/index/owner 均符合 contract，当前角色为 `juhe_ai_sub2api_dev_app`；readback 事务为 read-only，九张固定事实表均为 source `0`、target `0`、digest `match`。由于 source 为空，这只是候选目标结构和空状态一致性证据，不是 Node 事实已回填的证据，也不替代 source freeze、writer drain、三库非空快照、恢复点或 rollback 验收。

### 9.7.31 Business/J3c 只读 manifest 复核（2026-08-31）

维护命令复核显示 Business capability manifest 当前 `capabilities=15`、`operations=92`，但 status coverage 仍为 `missing=9`、`partial=4`，命令保持非零退出；Business owner manifest 的 92 个操作可映射到 52 个 writer、40 个 read、15 个 transaction group，但该清单只是源码装配覆盖，不证明运行中唯一 writer 或 handoff。J3c readonly boundary 的 Go reader 与 forbidden-findings 检查已就绪，但 `j3cOwnerReady=false`，Node 仍是实际 owner。上述结果进一步确认 J3b 不能以局部清单通过替代 Node drain、owner epoch、三库回填和 active-path-zero。

### 9.7.32 Dev Docker/数据库运行态复核（2026-08-31）

按 dev runbook 使用远端 `huanmin@192.168.1.203` 只读检查 Docker：Server `29.1.3`，PostgreSQL、PgBouncer 和 cache/state/queue Redis 均为 healthy；当前没有运行中的 `juhe-ai` Node 应用或 Go Gateway 容器，只有基础设施容器。对主 dev 库读取 `pg_stat_activity` 仅见一个管理员 idle 连接和一个 `juhe-ai:server` idle 连接，没有活动事务。该结果证明当前没有可供演练的 Node writer 进程，但“没有进程”不等于完成 drain/handoff，也不产生 owner epoch、备份恢复或 rollback 证据；因此不能据此把 Node 标记为已归档。

### 9.7.33 远端 Go Gateway 镜像 J3b 启动门探针（2026-08-31）

远端标签 `192.168.1.203:31080/platform/juhe-ai-go-gateway:4073250e407e` 与当前 Git `HEAD=4073250e407ea25d37a9d33d47a99bff1d8ffded` 对齐，镜像创建时间为 `2026-08-31T10:37:56+08:00`；工作区未提交改动不包含在该镜像内。其 `--help` 只显示通用 F3/F4 CLI，不能单凭 CLI 判断 J3b（J3b 通过环境变量配置）。在不连接数据库、不开 listener 的前提下，以 `JUHE_AI_J3B_ENABLED=true` 和其余最小 owner 配置运行一次性容器探针：镜像先拒绝缺失 `JUHE_AI_J3B_OWNER`，补齐 owner/instance/store/secret/epoch 后继续拒绝缺失的 cutover evidence 文件。这证明该镜像对应的提交包含 J3b fail-closed 启动门；但尚未用当前工作区构建物和真实 handoff evidence 启动隔离 DB/Redis runtime，也未完成 GitOps candidate、rollback 或 Node handoff，因此仍不能作为已接管证据或触发 Node 归档。

### 9.7.34 当前工作树 Gateway candidate 容器打包（2026-08-31）

为验证未提交 J3b 修复可以形成 Linux runtime，在 Windows 工作区以 `CGO_ENABLED=0 GOOS=linux` 交叉编译当前 `projects/gateway/cmd/juhe-ai-gateway`，传入 dev Docker 后以基础镜像 `platform/juhe-ai-go-gateway:4073250e407e` 构建临时标签 `juhe-ai-go-gateway:j3b-worktree-20260831`。Dockerfile 显式 `chmod 0755`，修复 Windows 传输不保留 Unix executable bit 的首轮构建失败；最终镜像 digest 为 `sha256:fd95fe420942581523ba4d2e83941f03eb01836786e5fd4719c9ee7647e7f166`，`-check-boundary` 返回 0。用完整最小 J3b owner 配置启动一次性容器时，在读取不存在的 cutover evidence `/missing` 处 fail-closed，返回非零且未打开 DB/listener；远端 `/tmp` 临时二进制、Dockerfile 和 build context 均已清理。该 candidate 只证明当前工作树可打包并保留启动安全门，不证明真实 DB/Redis runtime、三库 backfill、Node drain、owner epoch、rollback 或 GitOps 切换。

### 9.7.35 Candidate 后源码回归与硬门禁复扫（2026-08-31）

candidate 打包后，从当前工作树分别运行 Gateway、maintenance、shared contracts 的全量 `go test -count=1 ./...` 与 `go vet ./...`，均通过；`git diff --check` 通过。随后重跑维护扫描：`-scan-node-j3b-active-path` 仍为 `scannedFiles=968`、`blockedFindings=161`，`-verify-gateway-route-owner-manifest` 仍为 `families=22`、`mutationRoutes=98`、`missing=21`、`partial=1`、`pending=22`，两者均以未就绪状态退出。源码和 candidate 回归不能覆盖或覆盖掉这些 Node owner、route scope、drain、backfill、rollback 硬门禁；Node 归档继续禁止。

### 9.7.36 本轮本地/dev 接管进度（2026-08-31）

本轮按“本地与隔离 dev 为验收边界”完成 J3b Node owner 拆离：Go Gateway 的模型检测运行时已接入质量摘要决策（包含 high-confidence/likely/uncertain/suspicious 梯级、模型不匹配与 Juice hard anomaly 的 fail-closed 分支），并补充对应单元回归；Go Gateway、maintenance、modelcheckprobe 定向测试通过。Node 侧已移除 `/model-checks` 与 `/my-model-checks` 的 server/system-api 挂载、HTTP proxy、token worker 停止入口及质量/信任后台调度；dataset/stats/db-service IPC writer 链也已拆除，兼容监控字段仅保留 deprecated 的零值读取。

Node J3b 实现、专属存储文件及历史 golden/smoke 夹具已移入 `migration-backup/node/j3b-model-check/`；当前 manifest 为 62 条（核心实现加 supplemental comparison-only 夹具），记录原路径、归档路径和逐文件 SHA-256。清单以 `a63d03f0c46ad43f4ed30be81faa8ff4182c94f9` 记录来源/rollback 锚点，并以 `16a01b51d81d393e1ef765f7447365e822cf1299` 记录 cutover 锚点；supplemental 夹具不能作为 runtime rollback 输入。当前 active-path 生产源码扫描为 `ruleVersion=j3b-active-path-v2`、`scannedFiles=912`、`blockedFindings=0`，但 regression/mockdata 目录被跳过，故仍需单独验证脚本入口。Node mockdata CLI 与公开 J3b package scripts 已去除归档模块运行时加载；此前记录的 `entries=58` 与当前 62 条 manifest 是不同阶段证据，不能混称为当前计数。完整 Go/Node 回归和真实数据库证据仍以实际复跑结果为准，不在此处提前宣称全绿。

Gateway route owner manifest 已将 `model-checks` 标为 `implemented`，并记录 admin/self 两个 scoped mount 与 16 条 HTTP/JSON/SSE 方法矩阵；定向 ownership 测试通过。全局 manifest 仍报告其它 21 个未迁移 route families，故全局 verifier 继续以退出码 3 保持关闭，这是其它迁移批次的门禁，不是 J3b 残留。

dev Docker 侧已确认 PostgreSQL 18、PgBouncer、cache/state/queue Redis 容器 healthy；当前工作树 candidate 镜像 `juhe-ai-go-gateway:j3b-worktree-20260831` digest 为 `sha256:fd95fe420942581523ba4d2e83941f03eb01836786e5fd4719c9ee7647e7f166`，J3b 缺失 evidence 时按预期 fail-closed。既有隔离 dev 证据显示九表 Node→Go backfill/readback、PgBouncer runtime smoke 和 Redis DB9 鉴权 PING 通过；本轮未触碰主库数据或生产资源。J3b 本地代码与归档门禁已闭合，但生产 GitOps canary、真实停写窗口和全局其它 route families 仍不在本轮完成范围。

当前判断：Node J3b 物理归档、active-path-zero（限生产源码扫描）、model-checks route proof 与归档哈希复核已经通过；一审已完成 trusted comparison 的 provider/protocol/profile 一致性、comparison full-suite 证据、summary 高可信条件、SSE 前端 envelope、质量计划 DTO/删除账户过滤、持久化行锁/CAS、claim 续租/过期完成门、claim 后二次 resolver 快照、HTTP 200 质量失败/不可用 health 与 `suspicious` 硬失败语义修复。Go 探针现已补齐三次重试、terminal non-200 终止、非适用 Juice 标记、随机 coverage/nonce、高 reasoning/instructions 与独立 stream probe。仍未完成或未形成 Node golden 对照的门禁包括：详细 token/identity observation 字段与 HMAC/feature/trace 持久化、Juice contract hash/repeat 历史证据、identity 题面随机化、完整跨协议请求体和行为 family 终止语义。因此不能把“Node 文件已归档”扩大表述为“所有 J3b 语义已验收”。全局迁移仍未完成：route owner manifest 还有 21 个非 J3b family 待迁移；生产 GitOps canary、真实停写窗口、owner epoch 和生产 rollback 也不属于本轮证据。因此不能宣称整个 Node 后端已完成迁移或生产已切换。

### 9.7.37 一审复查与补漏（2026-08-31）

一审修复的范围是 trusted comparison 和归档可操作性。Gateway 在构建阶段解析主目标与 comparison 目标后，以 HTTP `400` 拒绝 provider、protocol 或 `providerProtocolProfileId` 不一致的组合；runtime 仍保留相同的防御性 gate，并将 profile ID 写入主目标和对照目标的 durable snapshot。comparison full suite 现在保留为 `trusted_comparison.*` run item，传入与主目标同一版本的 tokenizer/model-limit 依赖；其 item 不会被投影成 comparison tenant 的长期 observation 或 trust。高可信要求 comparison 汇总项自身通过，不能仅以 distribution similarity 通过替代。定向 Go 回归覆盖 revision、provider、protocol、profile 不匹配和 comparison item 投影边界。

这轮修复尚不替代完整 Node golden 对照。详细 token/identity observation 字段、Juice contract hash/repeat、identity 题面随机化和完整协议请求体仍需以 archived Node oracle 的 fixture 逐项对照；未完成前不应将“Node 文件已归档”扩大表述为“所有 J3b 语义已验收”。
