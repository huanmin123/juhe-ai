# J3b 模型检测完整迁移契约

> 冻结日期：2026-08-26。
> 状态：L1 完整边界已按当前 `master` 重新冻结；Go jobs 已新增纯领域 `modelcheckprofile`、`modelcheckprobe`、单次 direct transport 和双模式 `modelcheckstore` 的 run/item/observation 基线。后者直接连接 SQLite 或 PostgreSQL dataset store，PostgreSQL 运行期只做 schema preflight、不执行 DDL；隔离 dev scratch 已完成 Node 六 schema 初始化后 Go run/item/observation/终态 writer smoke，并清理数据库、角色和 PgBouncer 临时认证。它仍未接入 Go J3b runtime、管理 API/SSE、retry/调度、质量投影、GitOps 或 Node 归档。本文件不是切换授权。历史 W7 单体 Go 实现和 Goose catalog 已由 `133cd4e48 (go del)` 删除，不能作为当前可复用实现或验收依据。

## 1. 完整功能边界

J3b 是“模型检测与其直接质量结果投影”，不是一个独立的 HTTP probe。一次完整接管必须由 Go `jobs` 直接承担以下所有部分：

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
| HTTP 与 SSE | `backend/src/modules/model-checks/model-checks.routes.ts` 提供 JSON、SSE、`connected`、10 秒 heartbeat、progress、complete/error、停止与 active-run 查询 | Go jobs 管理 listener 直接提供等价路径、管理员鉴权和 SSE；不经过 Node HTTP/IPC 转发 |
| 主编排 | `model-checks.service.ts` 解析 target、创建 run、执行 probe、完成 run、生成质量决策 | 以 Go 领域服务完整替代；不得逐行翻译或调用 Node service |
| 解析/协议与 probe | `model-checks.{profiles,payloads,probes,evaluation,parsing,response-parsing,provider-capabilities,gateway-probe,probe-retry}.ts` | 逐项固定请求、响应、重试、评分和包装 HTTP/上游 HTTP 的差异 |
| 高级 probe | `model-checks-{token-integrity,token-probes,identity-features,observation-security,gpt56-juice,trust-report}.ts` | 不能因 Go 首版省略 token/identity/trust 结果；每项需 golden 或 Node oracle |
| 手动生命周期 | `model-checks-active-runs.ts` 与 `diagnostic-task-limiter.ts` | Go 以请求/账号粒度的可取消活动 run 管理替代；不继承 Node 单事件循环低并发闸门 |
| 周期任务 | `background-jobs.ts`、`model-quality-scheduled-check.service.ts`、background registry | Go 独占三类 scheduler、lease、恢复和 health；Node scheduler/worker registry 路径清零 |
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

J3b 目标 owner 是 `juhe-ai-jobs`，并且必须在 PostgreSQL 与 SQLite 两种正式模式中分别形成唯一 writer。管理 listener、runner、scheduler、input/outcome store、业务 projector、运行 health 和 audit append 都在该进程内完成。管理读 API 也由 Go handler 直接提供，入口路由通过 GitOps 精确分流至 jobs；这是 Go 三项目基线定义的“完整后台功能受认证管理命令入口”例外，不保留 Node→Go manual adapter，也不把它扩展成通用业务代理。

Go 只可直接请求目标上游和 PostgreSQL/Redis 等明确依赖。严禁 Go 调用 Node、Node DB-service、Node IPC、Node queue、Node HTTP、或另一个 Go 服务以完成 J3b。没有明确定义 input identity、config revision、lease fence、outcome digest、CAS 和重放语义时，不得开始实现或发布 scheduler。

当前 SQLite business DB 仍由 Node 单 writer 持有，尚未存在可验证的 Go 直接业务写入 owner。按双模式完整迁移规则，J3b L2 必须让 Go 接管所有受影响 SQLite business writer 与 schema 生命周期，并在每个 SQLite 文件建立唯一 writer；只做 PostgreSQL 接管并保留 SQLite Node owner 只能作为显式的临时范围缩减，不能标记 J3b 已接管。当前 `backend-go/projects/jobs` 也没有历史 W7 对应的 worker/store 可直接接管；必须重新建立当前架构的输入、outcome、schema contract 和实现。该决策已冻结为 Go 的双模式完整 owner，未完成前不得将 J3b 标记为 L2/L3 已接管。

## 5. L2 前必须冻结的输入/结果协议

- 输入：request ID、trigger、目标/比较账号及其 immutable config revision、provider protocol profile、model 映射、credential envelope、probe profile、policy revision、observed time、deadline 和发起人授权快照。
- 结果：稳定 outcome ID/digest、run/item/observation、wrapped HTTP status 与 upstream status、评分/可信度、quality decision、health-sync 状态、error class、started/finished/observed time。
- 一致性：每个 input 只签发一次；相同 identity+digest 的 outcome 重放幂等；不一致重放、过期 lease、删除/禁用账号、config/policy revision 漂移和 CAS 冲突必须 fail-closed 或记录 stale，不得覆写新状态。
- PostgreSQL：jobs 自有表与业务结果表均使用最小权限、短事务、`statement_timeout`/`lock_timeout`；运行期不执行 DDL。Go schema/权限预检不通过时 listener/scheduler fail-closed。

## 6. 验收与 L4

L2/L3 至少覆盖：所有 Node profile/probe 的 golden 对照；JSON 与 SSE 成功、拒绝、取消、客户端断开和 EPIPE；manual/scheduled/recovery/health-sync；凭据不进入 jobs outcome、日志或审计；lease busy、重复 input/outcome、revision 漂移、CAS stale、上游/代理/timeout/partial failure；PostgreSQL/PgBouncer 真实闭环、并发/race/vet；以及直接 Go 管理入口的管理 API readback。

L4 还必须证明：Node route、SSE service、active-run、scheduler、worker/IPC/DB-service writer、retention/health retry、启动项、指标与专属测试已 active-path-zero；完整 Node 文件清单、SHA-256、接管/回滚提交和恢复顺序写入 backup manifest；Jenkins 与 GitOps 在同一 release-state 原子切换 Go jobs 镜像、能力开关和精确路由；生产 owner handoff、重启、回滚与 freshness 证据均已记录。未满足任一项时只能报告“L1/L2 实现中”，不能删除 Node 功能或宣称接管。

## 7. 当前结论

J3b 是下一项可开始 L2 设计的候选，但尚不具备删除 Node 的条件。架构已冻结为：`jobs` 进程内直接承受认证管理命令与 SSE，且 Go 在 PostgreSQL/SQLite 两种正式模式都成为唯一 J3b writer；不允许 Node bridge、Go→Node 调用或 SQLite 双 writer。下一步必须把现有模型检测的直接质量投影与 J3c 的统计/失败前置检查严格切开，再形成可执行的 Go input/outcome、schema 和 SQLite owner 契约。

## 8. 当前 L2 增量

`backend-go/projects/jobs/internal/modelcheckprofile` 已冻结九个现役协议 profile、十三个支持模型、默认模型/profile、配对模型优先级和来源 endpoint family；它只提供防御性拷贝的纯查询，不打开数据库、不发起网络请求，也不替代 Node 运行时。Go 单测直接读取 Node profile golden，因而 Node profile、模型或 endpoint family 的后续变更会先使 Go parity test 失败；本轮由此发现并修复 Anthropic 从旧 `claude-opus-4-8/4-7` 到当前 `claude-opus-5/4-8` 的偏移。

`backend-go/projects/jobs/internal/modelcheckinput` 已定义版本化、规范化的 `IssuedInput` 纯领域结构。它固定账号/config/profile/policy revision、endpoint fingerprint、credential envelope alias、model/profile/trigger/deadline 与 SHA-256 digest；payload 前会重算 digest，配置篡改会 fail-closed。该结构没有原始 API Key、token、cookie、代理密码或 response body 字段。`InputVersion` 由 durable Store 在同一 identity 的事务内分配并进入 digest，`IdentityKey` 仅由 system account、target、model/profile、trigger/schedule 和 comparison identity 派生，不包含凭据、时间或响应。

`backend-go/projects/jobs/internal/modelcheckdurable` 已实现这四张 `juhe_jobs` 表对应的 SQLite/PostgreSQL input、claim 和 outcome 事务边界：同一 `input_id` 只有字节等价的 immutable input 才能重放；同一 logical identity 的新快照分配下一个单调版本；执行 claim 以 owner/token/outcome 与到期时间维护单调 fence；outcome 必须同时匹配 input digest、claim token、owner、outcome ID 与 fence，旧 fence、过期 lease、不同 payload 重放一律拒绝。SQLite 回归已覆盖签发、重放、版本、busy/takeover、stale fence 和 outcome replay；一次性 dev PostgreSQL scratch 已通过真实表上的 schema preflight、Issue/Load、两个并发 owner 的一成功一 `ErrBusy` claim、Commit 和过期后的幂等 replay，随后数据库已删除并核验不存在。本包尚未做业务 revision re-read/stale、run/item/observation 写入衔接、质量投影或 runtime 接线，不能据此启用 J3b 或归档 Node。

`backend-go/shared/contracts` 与 `backend-go/projects/maintenance/internal/j3bmodelcheck` 已冻结 J3b 独立 `juhe_jobs` schema contract 及一次性 bootstrap 命令。表、列、主键/唯一约束和游标/target 索引均由共享 contract 描述；bootstrap 只在显式 `--check-j3b-model-check-postgres` 或 `--apply-j3b-model-check-postgres` 下运行，jobs runtime 后续只做只读 readiness。当前没有在开发主库或生产执行该 bootstrap；真实 scratch smoke 需另行完成并清理后才能作为环境证据。

`backend-go/projects/jobs/internal/modelcheckstore` 已建立 `model_check_runs`、`model_check_items`、`model_check_observations` 的 Go typed writer 基线：SQLite 显式建表，PostgreSQL 只做 `juhe_dataset` 三表读权限/列和必需索引契约预检；run 终态后拒绝追加和回退，JSON 字段仅接受有效 JSON。SQLite 建表索引与 Node dataset schema 的 run/item/observation 查询、retry 与 aggregation 索引同名对齐，缺任一必需索引时两种模式均 fail-closed。PostgreSQL 的追加和终结都先锁定同一 run 行，因而 terminal 与 item/observation 追加不能并发穿透；SQLite 保持单写者事务。`ProjectOutcome` 将同一 run 的全部 item 和终态摘要合并为单个事务，崩溃不会留下部分 item 的 terminal run；只有完整相同的终态 replay 才可幂等通过，任何 item 或摘要漂移均 fail-closed。它不调用 Node、Node DB-service、IPC 或 HTTP。隔离 dev scratch 已验证 PostgreSQL schema preflight 和完整 writer 生命周期，并在结束后清零临时数据库、角色与 PgBouncer 认证；本轮再以 dev PostgreSQL 验证严格索引 preflight、writer lifecycle、concurrent terminal fence，以及 Node 初始化的隔离 schema 上 Go atomic projector 的 idempotent/conflict 闭环，测试 run 会清理。该包仍未接入 runtime，不能据此启用 J3b、删除 Node 或宣称跨运行时完整等价。后续必须继续完成版本化 input/outcome digest、lease/fence/CAS、质量投影、管理 JSON/SSE 和调度恢复门禁。

`backend-go/projects/jobs/internal/modelcheckprobe` 已接管四种协议的基础 capability request 构造：OpenAI Responses、OpenAI Chat、Anthropic Messages 与 Gemini native。它生成不含凭据的不可变 JSON bytes，并保留 Node 的路径、短探针最低 token、Anthropic 不发送通用 `temperature`、Gemini `?alt=sse` 规则；它不发网。后续 executor 必须直接使用此包，不能重新在 Node 或另一个 Go 服务构造 payload。

同一包新增 jobs-owned direct transport 与 `RunBasicProbe` 组合：严格校验 endpoint（禁止 userinfo/query/fragment/redirect），按协议补齐 `/v1` 或 `/v1beta`，支持 JSON/SSE、取消、超时和响应大小上限，并只把解析后的 model/output/status/usage 交给 evaluator；原始 response body、认证头和 transport 原始错误不进入 durable evidence。transport 回归覆盖四协议路径、SSE、非 2xx、超大响应、userinfo、取消和超时，组合回归确认真实 HTTP 响应可直接得到 `responses_basic` 评分 item。该层仍是单次 probe，不包含 retry、input claim、持久化、质量投影或管理 API。

`backend-go/projects/jobs/internal/modelcheckexecutor` 已把 `LoadInput → resolver 预读 → Claim → resolver revision/profile/model/endpoint 二次复核 → RunSuite → CommitOutcome` 串成单输入 Go 闭环。resolver 预读失败不会留下租约；claim 后第二次快照必须与第一次及 immutable input 一致，否则以 stale 失败且不发网；retry 只重试 transport/HTTP 非 200，HTTP 200 的内容质量失败只评分一次；claim 后可恢复错误会释放租约且保留递增 fence。`modelcheckdurable` 同时提供按 `(stored_at,outcome_id)` 游标读取并重验 digest/identity 的 committed outcome API，供后续 Go projector crash recovery 使用。该 executor 尚未接入管理 listener、质量投影或 scheduler。

`modelcheckprobe.RunSuite` 已按 Node 的基础套件顺序执行 basic、可选 stream、structured、tool，并从同一批响应生成 usage-shape；最终非 200 触发与 Node 相同的后续探针终止栅栏，失败保留为 item 级证据，行为或 long-context 组提前终止时也不会错误继续 stability。四协议 structured/tool 请求 schema 已与 Node golden 对齐，并继承目标的 stream 模式。`modelcheckprobe` 另已落地 Node 同阈值的 token integrity 分析器、固定 0/512/2048 padding 构造契约、行为探针和 long-context 纯领域评分；当 runtime 没有经确认的 tokenizer 与模型窗口快照时，long-context 只产生显式 excluded/skipped 证据而不伪造执行结果。这些组件尚未接入 Go runtime、durable observation writer、管理 API/SSE、scheduler 或质量 projector，不能据此声称 J3b 已接管。

`backend-go/projects/jobs/internal/modelcheckruntime` 现已形成一个进程内的 L2 运行时闭环：它在同一 Go 进程中完成版本化 input 签发、dataset run 创建、resolver 双读与 durable claim、直接 probe suite、durable outcome commit，以及 run/item 的原子终态投影。成功、上游失败和客户端取消均会写入完整终态；调用方 context 已取消时，终态投影使用独立的短超时上下文，避免留下永久 `running` 记录；请求摘要通过结构化 JSON 编码，避免输入 ID 破坏持久化 JSON。该服务没有 Node、IPC 或跨进程依赖，并以 `modelcheckruntime` SQLite 集成测试覆盖成功投影、取消失败投影和 durable outcome readback；但它仍未接入管理 listener/SSE、trusted comparison、账号/凭据解析、observation/trust/quality projector、scheduler/recovery/health-sync 或 PostgreSQL 真实 runtime wiring，因此本增量不改变 Node owner 和归档条件。

`backend-go/projects/jobs/internal/modelcheckactive` 已补齐 Go 进程内活动任务生命周期：以 system-account 作用域做互斥，句柄绑定取消 context，`Stop` 只取消匹配作用域并标记 `stopRequested`，`Finish` 以句柄身份清理，旧句柄不会误删新任务；并发启动、跨作用域隔离和停止取消均有 `-race` 覆盖。runtime 可选接入该注册表，停止动作会沿同一 context 进入 executor，最终由终态投影记录 canceled。它只负责请求级协调，不替代 durable claim/fence 和跨实例恢复；管理 JSON/SSE 与持久化 active-run 查询仍未接线。

同一 `modelcheckprobe` 包还解析四协议 JSON/SSE 的 model、output、usage 与 error envelope，HTTP `200` 但包含协议错误时不会被标为成功。它只保留评分所需字段，不保存原始 response body；structured evidence 仅保留 `status/value`，usage evidence 仅保留八个数字 token 字段，不能把上游额外 JSON 写入 outcome。Node 的多行 `data:`/EOF frame、Anthropic delta、OpenAI stream failure、Gemini error 已由 Go 回归覆盖。该包现已实现并用 Node 的请求失败评分向量核对基础 `responses_basic`、stream、structured output、tool calling 与 usage-shape 的 item 状态、分数、分母、模型不匹配和证据不足语义；行为探针、token integrity 固定矩阵/统计判定、long-context marker 生成/needle 评分，以及七类 identity canary/八维特征向量也已形成可单测的 Go 组件。单次 direct transport 已由同包实现；stability、distribution、cross-model 与 trust/quality 汇总、durable observation/runtime 接线仍未迁入。
