# J3b 模型检测完整迁移契约

> 冻结日期：2026-08-26。
> 状态：L1 完整边界已按当前 `master` 重新冻结；Go jobs 已新增纯领域 `modelcheckprofile`、`modelcheckprobe` 和双模式 `modelcheckstore` 的 run/item/observation 基线。后者直接连接 SQLite 或 PostgreSQL dataset store，PostgreSQL 运行期只做 schema preflight、不执行 DDL；隔离 dev scratch 已完成 Node 六 schema 初始化后 Go run/item/observation/终态 writer smoke，并清理数据库、角色和 PgBouncer 临时认证。它仍未接入 Go J3b runtime、管理 API/SSE、直接上游 transport、质量投影、GitOps 或 Node 归档。本文件不是切换授权。历史 W7 单体 Go 实现和 Goose catalog 已由 `133cd4e48 (go del)` 删除，不能作为当前可复用实现或验收依据。

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

`backend-go/projects/jobs/internal/modelcheckdurable` 已实现这四张 `juhe_jobs` 表对应的 SQLite/PostgreSQL input、claim 和 outcome 事务边界：同一 `input_id` 只有字节等价的 immutable input 才能重放；同一 logical identity 的新快照分配下一个单调版本；执行 claim 以 owner/token/outcome 与到期时间维护单调 fence；outcome 必须同时匹配 input digest、claim token、owner、outcome ID 与 fence，旧 fence、过期 lease、不同 payload 重放一律拒绝。SQLite 回归已覆盖签发、重放、版本、busy/takeover、stale fence 和 outcome replay；PostgreSQL runtime 仍只会使用 maintenance 预置并通过 readiness 的 schema，尚无真实 PostgreSQL durable Store smoke。本包尚未做业务 revision re-read/stale、run/item/observation 写入衔接、质量投影或 runtime 接线，不能据此启用 J3b 或归档 Node。

`backend-go/shared/contracts` 与 `backend-go/projects/maintenance/internal/j3bmodelcheck` 已冻结 J3b 独立 `juhe_jobs` schema contract 及一次性 bootstrap 命令。表、列、主键/唯一约束和游标/target 索引均由共享 contract 描述；bootstrap 只在显式 `--check-j3b-model-check-postgres` 或 `--apply-j3b-model-check-postgres` 下运行，jobs runtime 后续只做只读 readiness。当前没有在开发主库或生产执行该 bootstrap；真实 scratch smoke 需另行完成并清理后才能作为环境证据。

`backend-go/projects/jobs/internal/modelcheckstore` 已建立 `model_check_runs`、`model_check_items`、`model_check_observations` 的 Go typed writer 基线：SQLite 显式建表，PostgreSQL 只做 `juhe_dataset` 三表读权限/列契约预检；run 终态后拒绝追加和回退，JSON 字段仅接受有效 JSON。PostgreSQL 的追加和终结都先锁定同一 run 行，因而 terminal 与 item/observation 追加不能并发穿透；SQLite 保持单写者事务。它不调用 Node、Node DB-service、IPC 或 HTTP。隔离 dev scratch 已验证 PostgreSQL schema preflight 和完整 writer 生命周期，并在结束后清零临时数据库、角色与 PgBouncer 认证；该包仍未接入 runtime，不能据此启用 J3b、删除 Node 或宣称跨运行时完整等价。后续必须继续完成版本化 input/outcome digest、lease/fence/CAS、质量投影、管理 JSON/SSE 和调度恢复门禁。

`backend-go/projects/jobs/internal/modelcheckprobe` 已接管四种协议的基础 capability request 构造：OpenAI Responses、OpenAI Chat、Anthropic Messages 与 Gemini native。它生成不含凭据的不可变 JSON bytes，并保留 Node 的路径、短探针最低 token、Anthropic 不发送通用 `temperature`、Gemini `?alt=sse` 规则；它不发网。后续 executor 必须直接使用此包，不能重新在 Node 或另一个 Go 服务构造 payload。

同一 `modelcheckprobe` 包还解析四协议 JSON/SSE 的 model、output、usage 与 error envelope，HTTP `200` 但包含协议错误时不会被标为成功。它只保留评分所需字段，不保存原始 response body；structured evidence 仅保留 `status/value`，usage evidence 仅保留八个数字 token 字段，不能把上游额外 JSON 写入 outcome。Node 的多行 `data:`/EOF frame、Anthropic delta、OpenAI stream failure、Gemini error 已由 Go 回归覆盖。该包现已实现并用 Node 的请求失败评分向量核对基础 `responses_basic`、stream、structured output、tool calling 与 usage-shape 的 item 状态、分数、分母、模型不匹配和证据不足语义。它仍没有 direct transport；behavior、long-context、stability、token integrity、identity、distribution、cross-model 与 trust/quality 汇总尚未迁入。
