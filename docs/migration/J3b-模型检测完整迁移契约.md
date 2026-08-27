# J3b 模型检测完整迁移契约

> 冻结日期：2026-08-26。
> 状态：L1 完整边界已按当前 `master` 重新冻结；Go jobs 已新增纯领域 `modelcheckprofile`、`modelcheckprobe`、单次 direct transport 和双模式 `modelcheckstore` 的 run/item/observation 基线。后者直接连接 SQLite 或 PostgreSQL dataset store，PostgreSQL 运行期只做 schema preflight、不执行 DDL；隔离 dev scratch 已完成 Node 六 schema 初始化后 Go run/item/observation/终态 writer smoke，并清理数据库、角色和 PgBouncer 临时认证。它仍未接入 Go J3b runtime、管理 API/SSE、retry/调度、质量投影、GitOps 或 Node 归档。本文件不是切换授权。历史 W7 单体 Go 实现和 Goose catalog 已由 `133cd4e48 (go del)` 删除，不能作为当前可复用实现或验收依据。

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

- 输入：request ID、trigger、目标/比较账号及其 immutable config revision、provider protocol profile、model 映射、credential envelope、probe profile、policy revision、observed time、deadline 和发起人授权快照。
- 结果：稳定 outcome ID/digest、run/item/observation、wrapped HTTP status 与 upstream status、评分/可信度、quality decision、health-sync 状态、error class、started/finished/observed time。
- 一致性：每个 input 只签发一次；相同 identity+digest 的 outcome 重放幂等；不一致重放、过期 lease、删除/禁用账号、config/policy revision 漂移和 CAS 冲突必须 fail-closed 或记录 stale，不得覆写新状态。
- PostgreSQL：J3b owner 自有表与业务结果表均使用最小权限、短事务、`statement_timeout`/`lock_timeout`；运行期不执行 DDL。Go schema/权限预检不通过时 listener/scheduler fail-closed。

## 6. 验收与 L4

L2/L3 至少覆盖：所有 Node profile/probe 的 golden 对照；JSON 与 SSE 成功、拒绝、取消、客户端断开和 EPIPE；manual/scheduled/recovery/health-sync；凭据不进入 J3b outcome、日志或审计；lease busy、重复 input/outcome、revision 漂移、CAS stale、上游/代理/timeout/partial failure；PostgreSQL/PgBouncer 真实闭环、并发/race/vet；以及直接 Go 管理入口的管理 API readback。

L4 还必须证明：Node route、SSE service、active-run、scheduler、worker/IPC/DB-service writer、retention/health retry、启动项、指标与专属测试已 active-path-zero；完整 Node 文件清单、SHA-256、接管/回滚提交和恢复顺序写入 backup manifest；Jenkins 与 GitOps 在同一 release-state 原子切换 Go gateway 镜像、能力开关和精确路由；生产 owner handoff、重启、回滚与 freshness 证据均已记录。未满足任一项时只能报告“L1/L2 实现中”，不能删除 Node 功能或宣称接管。

## 7. 当前结论

J3b 是下一项可开始 L2 设计的候选，但尚不具备删除 Node 的条件。方案 A 架构已冻结为：`gateway` 进程内直接承受认证管理命令、SSE、scheduler 与业务投影，且 Go 在 PostgreSQL/SQLite 两种正式模式都成为唯一 J3b writer；不允许 Node bridge、Go→Node 调用、Go→Go J3b 调用或 SQLite 双 writer。下一步必须把现有模型检测的直接质量投影与 J3c 的统计/失败前置检查严格切开，再形成可执行的 Go input/outcome、schema 和 SQLite owner 契约。

## 8. 当前 L2 增量

`backend-go/projects/jobs/internal/modelcheckprofile` 已冻结九个现役协议 profile、十三个支持模型、默认模型/profile、配对模型优先级和来源 endpoint family；它只提供防御性拷贝的纯查询，不打开数据库、不发起网络请求，也不替代 Node 运行时。Go 单测直接读取 Node profile golden，因而 Node profile、模型或 endpoint family 的后续变更会先使 Go parity test 失败；本轮由此发现并修复 Anthropic 从旧 `claude-opus-4-8/4-7` 到当前 `claude-opus-5/4-8` 的偏移。

`backend-go/projects/jobs/internal/modelcheckinput` 已定义版本化、规范化的 `IssuedInput` 纯领域结构。它固定账号/config/profile/policy revision、endpoint fingerprint、credential envelope alias、model/profile/trigger/deadline 与 SHA-256 digest；payload 前会重算 digest，配置篡改会 fail-closed。该结构没有原始 API Key、token、cookie、代理密码或 response body 字段。`InputVersion` 由 durable Store 在同一 identity 的事务内分配并进入 digest，`IdentityKey` 仅由 system account、target、model/profile、trigger/schedule 和 comparison identity 派生，不包含凭据、时间或响应。

`backend-go/projects/jobs/internal/modelcheckdurable` 已实现 input、claim 和 outcome 事务边界，作为待迁移的纯实现材料；Gateway `internal/modelcheckowner.Store` 已新增只读 schema preflight，固定 J3b SQLite 单文件/PostgreSQL `juhe_j3b` 的物理入口，运行期不执行 DDL。相同 `input_id` 只有字节等价 immutable input 才能重放；claim/outcome 仍必须匹配 input digest、owner、outcome ID 和 fence。现有 jobs 包尚未迁入 Gateway 的业务 revision re-read、run/item/observation writer、质量投影或 runtime 接线，不能据此启用 J3b 或归档 Node。

Gateway `internal/modelcheckowner` 现已增加不可变 input 记录边界：payload 先规范化并计算 SHA-256，`input_id` 重放要求摘要和内容一致，不同内容复用 ID 会 fail-closed；写入使用单事务并保留 identity/version/config/policy/trigger/expiry 字段。该层仍是库级能力，未接入 listener、scheduler 或 Node 路径。

Gateway `internal/modelcheckowner` 现已增加 run/item/observation 持久化边界：只有 running run 可追加检测项和 observation，终态 projection 在同一事务内写入全部 item 与终态摘要；相同终态可幂等重放，状态、分数、摘要或 item 集合漂移会拒绝。该实现仍未接入管理 listener、真实 probe runtime、quality decision、scheduler 或 health retry，不能据此启用 J3b。

Gateway `internal/modelcheckowner` 现已增加 claim/outcome durable fence：input 过期或活动租约竞争会拒绝，租约接管时 fence 严格递增；旧 owner 的 release/commit 会被拒绝；相同 outcome digest 可重放，内容漂移会冲突。该边界仍未由 Gateway runtime 调用，不能替代完整 scheduler、probe 和恢复验证。

Gateway 已建立独立的 `modelcheckactive` 与 `modelcheckowner.HTTPHandler` 边界：进程内按 system-account 防重复运行、停止和释放；HTTP 路径覆盖 `/run`、`/run/stream`、`/run/active`、`/run/stop`、`/runs` 及详情读取，SSE 发送 connected/progress/complete/error，活动冲突返回 409 和 `Retry-After`。handler 只接受注入的认证、构建器和 runtime service，未接线时返回 503；它不调用 jobs、Node 或其他进程，当前也未注册到 Gateway 主 listener。

Gateway 已开始迁入纯领域 profile catalog，`internal/modelcheckprofile` 与 Node golden 的默认模型、协议 profile、paired model 和 endpoint family 规则保持独立副本，未引入 `jobs/internal` 依赖。该包目前只作为后续 probe/runtime relocation 的值对象基础，尚未意味着 J3b runtime 已切换。

Gateway 已将 `Runtime` 接入 `RunService` 契约：在注入完整 target resolver 后可执行 `IssueInput → CreateRun → Claim → direct probe → CommitOutcome → ProjectOutcome`，并提供按 system-account 的 run 列表/详情读取。隔离 SQLite 的成功闭环和失败终态均已回归；主进程仍在缺少 Business source、真实认证和完整 probe/trust 聚合时 fail-closed，不注册 J3b listener。

`backend-go/shared/contracts` 与 `backend-go/projects/maintenance/internal/j3bmodelcheck` 已冻结 J3b 独立 `juhe_j3b` schema contract 及一次性 bootstrap 命令。表、列、主键/唯一约束和游标/target 索引均由共享 contract 描述；bootstrap 只在显式 `--check-j3b-model-check-postgres` 或 `--apply-j3b-model-check-postgres` 下运行，Gateway runtime 后续只做只读 readiness。当前没有在开发主库或生产执行该 bootstrap；真实 scratch smoke 需另行完成并清理后才能作为环境证据。

`backend-go/projects/jobs/internal/modelcheckstore` 仍保留 `model_check_runs`、`model_check_items`、`model_check_observations` 的 Go typed writer 作为迁移材料：SQLite 显式建表，PostgreSQL 仅对旧 `juhe_dataset` 三表做契约预检；按方案 A，jobs 配置已对 SQLite/PostgreSQL J3b 一律 fail-closed，该包不得接入 jobs 常驻 runtime 或形成第二 writer。方案 A 的实际目标是 Gateway 的 `juhe_j3b` 专属存储，维护命令现在对 input/outcome、run/item/observation 和 health 表统一执行 schema/索引预检与显式 bootstrap；Gateway runtime 仍只做只读 readiness。原有 run 终态 fence、原子 item/observation 投影和 replay 语义继续作为迁移参考，但尚未接入 Gateway runtime、管理 API/SSE、retry/调度或质量 projector，不能据此启用 J3b、删除 Node 或宣称跨运行时完整等价。

`backend-go/projects/jobs/internal/modelcheckprobe` 已接管四种协议的基础 capability request 构造：OpenAI Responses、OpenAI Chat、Anthropic Messages 与 Gemini native。它生成不含凭据的不可变 JSON bytes，并保留 Node 的路径、短探针最低 token、Anthropic 不发送通用 `temperature`、Gemini `?alt=sse` 规则；它不发网。后续 executor 必须直接使用此包，不能重新在 Node 或另一个 Go 服务构造 payload。

同一包新增 jobs-owned direct transport 与 `RunBasicProbe` 组合：严格校验 endpoint（禁止 userinfo/query/fragment/redirect），按协议补齐 `/v1` 或 `/v1beta`，支持 JSON/SSE、取消、超时和响应大小上限，并只把解析后的 model/output/status/usage 交给 evaluator；原始 response body、认证头和 transport 原始错误不进入 durable evidence。transport 回归覆盖四协议路径、SSE、非 2xx、超大响应、userinfo、取消和超时，组合回归确认真实 HTTP 响应可直接得到 `responses_basic` 评分 item。该层仍是单次 probe，不包含 retry、input claim、持久化、质量投影或管理 API。

`backend-go/projects/jobs/internal/modelcheckexecutor` 已把 `LoadInput → resolver 预读 → Claim → resolver revision/profile/model/endpoint 二次复核 → RunSuite → CommitOutcome` 串成单输入 Go 闭环。resolver 预读失败不会留下租约；claim 后第二次快照必须与第一次及 immutable input 一致，否则以 stale 失败且不发网；retry 只重试 transport/HTTP 非 200，HTTP 200 的内容质量失败只评分一次；claim 后可恢复错误会释放租约且保留递增 fence。`modelcheckdurable` 同时提供按 `(stored_at,outcome_id)` 游标读取并重验 digest/identity 的 committed outcome API，供后续 Go projector crash recovery 使用。该 executor 尚未接入管理 listener、质量投影或 scheduler。

`modelcheckprobe.RunSuite` 已按 Node 的基础套件顺序执行 basic、可选 stream、structured、tool，并从同一批响应生成 usage-shape；最终非 200 触发与 Node 相同的后续探针终止栅栏，失败保留为 item 级证据，行为或 long-context 组提前终止时也不会错误继续 stability。四协议 structured/tool 请求 schema 已与 Node golden 对齐，并继承目标的 stream 模式。`modelcheckprobe` 另已落地 Node 同阈值的 token integrity 分析器、固定 0/512/2048 padding 构造契约、行为探针和 long-context 纯领域评分；当 runtime 没有经确认的 tokenizer 与模型窗口快照时，long-context 只产生显式 excluded/skipped 证据而不伪造执行结果。这些组件尚未接入 Go runtime、durable observation writer、管理 API/SSE、scheduler 或质量 projector，不能据此声称 J3b 已接管。

`backend-go/projects/jobs/internal/modelcheckruntime` 现已形成一个进程内的 L2 运行时闭环：它在同一 Go 进程中完成版本化 input 签发、dataset run 创建、resolver 双读与 durable claim、直接 probe suite、durable outcome commit，以及 run/item 的原子终态投影。成功、上游失败和客户端取消均会写入完整终态；调用方 context 已取消时，终态投影使用独立的短超时上下文，避免留下永久 `running` 记录；请求摘要通过结构化 JSON 编码，避免输入 ID 破坏持久化 JSON。该服务没有 Node、IPC 或跨进程依赖，并以 `modelcheckruntime` SQLite 集成测试覆盖成功投影、取消失败投影和 durable outcome readback。当前 executor 还可在 immutable comparison snapshot 存在时由 Go 独立执行第二套 suite，并生成 `trusted_comparison.comparison`；该能力已有 revision/profile 二次校验和回归测试，但仍未接入管理 listener 的真实账号/凭据解析、distribution/cross-model/Juice、observation/trust/quality projector、scheduler/recovery/health-sync 或 PostgreSQL 真实 runtime wiring，因此本增量不改变 Node owner 和归档条件。

`backend-go/projects/jobs/internal/modelcheckquality` 现已冻结 Node 质量 gate 的纯事实层，并由 runtime 在终态投影完成后以 CAS 方式追加版本化 `quality_decision_json`（包含 outcome/policy/evidence 摘要身份）。证据未由 trust/identity/Juice projector 完整形成时，Go 显式写入 `quality_evidence_not_formed` / `not_triggered`，不会处罚、隔离、降级或写健康失败；相同事实可幂等重放，漂移会 fail-closed。追加会在同一行锁事务内结构化比对终态 status、result summary 与 policy snapshot，不以 JSON 文本等值作为 SQL 条件，避免 PostgreSQL JSONB 与 SQLite TEXT 的序列化差异；隔离 dev PostgreSQL writer smoke 已覆盖首次追加与幂等重放，并清理临时 run。该增量仍不是业务质量 projector：`account_quality_enforcements`、恢复、health-hour 写入与失败重试尚未接线，因此 Node 质量 writer、scheduler 和 SQLite business writer 仍不可删除或归档。

首个 Go 直接业务写入原语 `modelcheckquality.ApplyEnforcement` 已实现为单事务的 PostgreSQL/SQLite business CAS：先逐字段复核冻结的 manual policy 或 schedule，再复核自有物理账户、删除状态、授权实例、来源状态与 `config_revision`；账户更新成功后才 upsert generation 单调递增的 active enforcement。相同旧快照在账户 revision 已递增后按 Node 源码顺序返回 `stale`，而不是把旧 run 误判为成功重放。该原语尚未从 runtime 调用，也未形成 SQLite writer handoff，故不会改变现有 Node owner；后续必须接入 formed evidence、Go cache invalidation、recovery/health projector 与 scheduler 后，才能启用。

同包新增 `ClaimDueRecoveries` 与 `CompleteRecovery`，作为尚未挂载的 Go 质量隔离恢复事务原语：前者只 lease 到期的 active isolate enforcement，并把账户当前 `config_revision` 固化回 enforcement；后者必须同时匹配 owner、account、enforcement ID、generation 和恢复 lease。失败检查、policy/config 漂移只会清租约并重排下次检查，绝不解除隔离；成功检查才按账户可用时段恢复为 `active` 或 `disabled`，然后清除 enforcement。账户时间计划按 Node 的时区、例外和跨日 window 计算；坏的持久化计划不会默认为允许，而是回滚并报错。该原语同样没有 runtime/scheduler/health/cache 接线，不能启用、不能成为 SQLite 业务 writer handoff，也不能作为删除 Node 的依据。

同一层另有 `ClaimDueSchedules` 与 `CompleteScheduledRun`：它们对 enabled schedule、当前 active 的自有账户、schedule revision 和 schedule lease 做直接 PostgreSQL/SQLite CAS。Node 的低 `limit=3`/单 worker 参数没有被复刻为 Go 吞吐限制；未来 Go scheduler 可以按资源配置更大的批量和并行度，但仍必须保持每条 schedule 的 lease/revision fence。当前这也只是未挂载原语，尚未请求上游、没有写 run，不能与 Node scheduler 并行启用。

`backend-go/projects/jobs/internal/modelcheckactive` 已补齐 Go 进程内活动任务生命周期：以 system-account 作用域做互斥，句柄绑定取消 context，`Stop` 只取消匹配作用域并标记 `stopRequested`，`Finish` 以句柄身份清理，旧句柄不会误删新任务；并发启动、跨作用域隔离和停止取消均有 `-race` 覆盖。runtime 可选接入该注册表，停止动作会沿同一 context 进入 executor，最终由终态投影记录 canceled。它只负责请求级协调，不替代 durable claim/fence 和跨实例恢复；管理 JSON/SSE 与持久化 active-run 查询仍未接线。

`backend-go/projects/jobs/internal/modelcheckhttp` 已建立 Go-owned JSON/SSE 管理边界：`/run`、`/run/stream`、`/run/active`、`/run/stop` 使用统一 `{data: ...}` envelope，默认 profile 与 trusted comparison 参数校验对齐 Node；活动租约在写响应前取得，冲突返回 `409`/`Retry-After`，SSE 下游写失败或客户端取消会取消同一 Go run context，suite item 进度按顺序发送且不静默丢弃。`NewBuildRequestFunc` 已固定为到 `modelcheckcommand.Builder` 的唯一生产适配器，HTTP JSON 不能再自行拼出绕过 Go scope/策略冻结的 runtime request。handler 仍未挂载到 `juhe-ai-jobs` 的真实认证、账号解析和部署路由，不能据此删除 Node 管理路由。

`backend-go/projects/jobs/internal/modelcheckauth` 已把现有 `system_sessions` 的直接鉴权抽为 PostgreSQL/SQLite 双模式模块：cookie 会话、`juhe_tmp_` bearer 优先级、SHA-256 token hash、到期、账户状态、强制改密、管理员角色与每分钟 Node ISO 格式的 session touch 都由 Go 直接校验。J3a 管理入口已复用该实现，防止两套 Go 管理鉴权漂移；J3b `NewAdminAuthorizeFunc` 可直接把认证 actor 带入 handler，并区分 actor 与操作 scope；`NewAdminTargetScopeResolver` 已补齐无显式 `systemAccountId` 时按目标账号解析所属系统账户的行为。

`backend-go/projects/jobs/internal/modelcheckapp` 已将 J3b 的 Go runtime、`/run` JSON/SSE、active/stop、`/runs` 读接口、鉴权、目标/策略冻结组装到 `juhe-ai-jobs`。配置关闭时不打开 J3b 资源；启用时启动独立 management listener，并把 `modelCheckEnabled/modelCheckReady` 纳入 jobs health。该接线仍不代表完整接管：质量 policy/schedule/options 管理接口、三类 scheduler、质量/health projector、SQLite 唯一业务 writer、完整 observation/trust aggregation、GitOps readiness/回滚证据和 Node active-path-zero 尚未完成，因此 Node 仍不能删除或归档。

SQLite 管理 listener 当前必须 fail-closed：Node 仍是共享 business SQLite 文件的唯一 writer，而 Go 管理鉴权与 Node 一致，会在会话超过一分钟未见时更新 `system_sessions.last_seen_at`；模型检测 `POST` 还是 Node system API 的 write/touch 会话路径。将 Go 改成只读验证并取消 session touch 不与该可观察会话契约等价，也不能使后续质量处罚、恢复 lease 或 health-sync 写入安全。故 `JUHE_AI_MODEL_CHECK_ENABLED=true` 且 `JUHE_AI_MODEL_CHECK_STORE=sqlite` 当前被 Go 配置拒绝；这是共存期保护，不是 SQLite 双模式接管完成或永久能力删除。共享 `system_sessions` 和 business SQLite file writer 的完整 owner handoff 超出 J3b，必须在独立范围中完成并有 Node active-path-zero 证据后，才能重新设计 SQLite owner。

J3b health-sync 也暂不接线：Node 当前把已形成的模型质量失败写入 `account_quality_health_hourly`，并以失败 run 的 dataset fact 作重试来源；该统计写入不能借用、替换或提前接管 J3c 的 usage 统计刷新和失败前置确认 writer。Go runtime 目前仍缺 Node 等价的 observation、trust aggregation、distribution/cross-model 与 GPT-5.6 Juice evidence，`Evidence.Formed=false` 是有意的 fail-closed 状态。没有这些事实，按 score 推断处罚、恢复 passed 或 health-sync 都会改变可观察业务结果；后续必须由用户明确 J3b health output 的数据 owner、与 J3c 的表/worker 边界及验证范围，才可形成新的 projector/scheduler 契约。

`backend-go/projects/jobs/internal/modelchecksource` 与 `modelcheckresolver` 已把 J3b 的业务候选冻结和执行解析拆开：前者复核账号状态、协议 profile、endpoint mode、账户模型限制与启用 mapping，并生成不含 endpoint、密文或代理值的 durable account snapshot；后者只消费该 snapshot 对应的内存加密执行材料，在 Go 进程内按协议构造认证头和显式代理 client。executor 的 resolver 现在接收完整 `IssuedInput + AccountSnapshot`，而非仅 account ID/config revision；因此重启重放仍能复核 system-account scope、原始请求模型、映射模型、profile revision、endpoint fingerprint、credential reference 与 proxy version，不能依赖前一次 HTTP 请求的进程内缓存。API Key、OAuth、Gemini quota project、profile/config revision 都有回归；quick 只执行核心 suite，full 的 token/identity 仅属于目标账户且发生在 target core suite 后、trusted comparison suite 前。

`modelchecksource.PostgresReader` 已开始承担真实 Go 业务读取：在一个 `REPEATABLE READ` / `READ ONLY` 事务中，以 `systemAccountID + accountID` 作为 SQL scope 读取逻辑账户、授权实例的有效物理源、账户/分组授权、启用 binding、protocol profile、模型限制/mapping 与有效 proxy；凭据和 proxy password 只在本进程解封装为内存 execution snapshot，durable input 只保存 HMAC/摘要身份。它不会调用 Node DB-service、IPC 或 HTTP；2026-08-27 已使用 dev PostgreSQL 的应用连接完成零行 schema/grant 合同预检。相同语义的 `SQLiteReader` 使用 `mode=ro + query_only` 读取当前 Node business SQLite，并已有 owner-account fixture 的 snapshot/redaction/replay 回归；它仍是 reader，不触碰 Node 当前 SQLite writer。两者都尚未挂入 jobs main 或管理 handler，跨 endpoint-family 的 model mapping 也尚未具备 Go 直接协议转换，不能静默按错误协议请求上游。以上缺口连同真实认证、质量投影和 scheduler 未完成前，Node 仍是 J3b owner，禁止归档其 route/scheduler/writer。

本轮补入 `modelcheckcommand` 与 `modelcheckpolicy`：管理命令构建器先以 Go source reader 冻结目标和可选可信对比账户，再读取并冻结同一 system-account 的有效质量策略，才生成 runtime request；目标名称、资源所有者、分组、profile、策略版本和探针版本均进入 run/input。`PolicySnapshot` 已升级为包含 profile、手动处置开关、阈值、动作和恢复周期的完整值对象，digest 由 Go 对规范 JSON 计算且在 input 签发/重放时复核，不能以任意外部 digest 绕过。`modelcheckpolicy.Reader` 对缺省 policy 使用与 Node 相同的 quick/70/fallback/10-minute 默认值，并可从 PostgreSQL 或 SQLite 业务表以事务级只读方式冻结显式 policy。普通检测的账号可用性已收紧为 Node `includeUnavailable` 域：仅 `active`、`temporary_unavailable`、`rate_limited` 且可调度、未过期；`pending_test` 不得执行，`quality_isolated` 仅可由 `quality_recovery` 请求使用。PostgreSQL 候选 SQL 也在只读 readiness 时经 `EXPLAIN` 规划，避免首个管理请求才发现 schema/type drift。以上仍未挂入 runtime listener、真实认证、scheduler 或质量 writer，不能改变 Node owner。

同一 `modelcheckprobe` 包还解析四协议 JSON/SSE 的 model、output、usage 与 error envelope，HTTP `200` 但包含协议错误时不会被标为成功。它只保留评分所需字段，不保存原始 response body；structured evidence 仅保留 `status/value`，usage evidence 仅保留八个数字 token 字段，不能把上游额外 JSON 写入 outcome。Node 的多行 `data:`/EOF frame、Anthropic delta、OpenAI stream failure、Gemini error 已由 Go 回归覆盖。Gateway 现已迁入 structured/tool/usage/stability 的纯评估与 suite 执行，并将 core-suite item 与 `partial` observation receipt 写入 Gateway 专属 J3b store；full profile 现执行行为探针和七类 identity canary，并只保留计数/脱敏摘要。token integrity 与 long-context 也已有 Gateway 纯评估器；在 tokenizer/model-limit snapshot 尚未接线时，suite 显式写入 `skipped` evidence，不伪造成功。`EvidenceAggregate` 严格要求 identity、token、stability、distribution、cross-model、Juice、usage、behavior、long-context 全部形成，缺失或 partial 时输出 `evidenceFormed=false`，禁止 enforcement/recovery/health 写入。distribution/cross-model/Juice 与 trust projector 仍未接入 Gateway runtime。

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

SQLite 和 PostgreSQL 都是正式目标模式，必须分别保持唯一 writer、schema 生命周期、权限、事务语义和恢复证据；通过 PostgreSQL 不能替代 SQLite handoff，反之亦然。PostgreSQL runtime 只做权限/schema/readiness 预检，DDL 归 B0 `maintenance` 的显式流程；SQLite 同样不得由 runtime 隐式建表或修复 schema。

每次 owner 切换前必须固化 owner manifest、epoch、版本兼容性、drain 状态、数据库备份/恢复点、健康/readiness、精确路由和回滚责任人。回滚只能整组恢复到先前已验证的单 owner：先停止新 owner 的 mutation/consumer，处理或隔离在途 command，再恢复旧 owner、路由与相同物理文件的写权限，并验证 revision/fence、session touch、lease、outbox、J3b/J3c 相关投影和 freshness。仅回滚镜像、开关或 Git 提交而未恢复 writer epoch，不构成有效回滚。

### 9.6 J3b/J3c 边界、L2 准入与验证矩阵

J3b 只拥有模型检测 input/outcome、run/item/observation、trust/quality 事实及已形成证据后的直接质量投影。物理存储固定为 `JUHE_AI_J3B_DATABASE_PATH` 指向的单一 SQLite 文件，或 PostgreSQL `juhe_j3b` schema；`account_quality_health_hourly` 与 health-sync retry source 同属该 J3b 存储，不再写 Node stats/dataset。J3c 独占 usage 统计刷新和账户失败前置确认，只能通过只读 `J3bHealthReader` 消费 J3b 发布事实，不得打开 J3b writer。J3b 不得消费未形成的 score 推断处罚、恢复或 health-sync，也不得把 J3c worker 当作 J3b 的兼容出口。旧三库数据迁移、回放、cleanup consumer 下线和 rollback epoch 必须在 L2 证据中逐项完成。

| L2 准入项 | 必须证明的结果 | 最低验证 |
| --- | --- | --- |
| 写路径归类 | Business SQLite 全量 Node writer、DB-service command、启动/worker/scheduler/maintenance 写路径均有唯一归属；切换后 Node active path 为零。 | 源码、启动配置和路由精确扫描；运行时 writer/connection 审计。 |
| 单 owner 切换 | SQLite 与 PG 均只出现一个物理 writer，旧 owner drain、epoch/fence 和恢复顺序可观察。 | 隔离 SQLite 与 PostgreSQL/PgBouncer 并发 handoff、重启和整组回滚演练。 |
| 进程内调用边界 | J3b 不存在 gateway/jobs transport；共享 Go 包不能自行打开 Business SQLite、发起调度或制造第二个 owner。 | 静态依赖扫描、进程内 command ID/revision/fence/digest/CAS/replay 与故障注入测试。 |
| J3b 完整功能 | JSON/SSE、三类 trigger、直接上游 probe、input/outcome、lease/fence、quality projector、recovery/health 的语义均完成。 | Node golden、成功/拒绝/取消/断连、busy/replay/stale、上游/代理/timeout/partial failure 及双模式集成。 |
| J3b/J3c 隔离 | J3b 质量投影与 J3c usage/失败前置确认无双写、无借用重试来源、无交叉回滚。 | 表/worker/路由 ownership 审计，跨功能故障与回滚回归。 |
| 发布与回退 | GitOps 精确路由、readiness、owner manifest、Node active-path-zero、备份和恢复顺序一致。 | candidate、生产 handoff、重启、回滚和 freshness 证据；未取得环境证据不得称为完成。 |

以下范围仍未被本契约覆盖，不能随 B0-B4 顺带接管：chat 业务写入、`codex-context` 的存储/命令语义，以及任何未另行授权的跨项目 typed command。它们需要各自的 owner 清单、数据/外部副作用边界、兼容性与回滚契约；在此之前保持现有 owner，且不得作为 J3b SQLite L2 的隐式依赖或 fallback。J3b 不因这些未覆盖范围获得 gateway/jobs 跨进程调用的例外。
