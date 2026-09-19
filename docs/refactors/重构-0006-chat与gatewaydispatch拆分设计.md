# REFACTOR-0006 chat 与 gatewaydispatch 包子域拆分设计

## 基本信息

- 编号：REFACTOR-0006
- 状态：草稿（设计完成；实施排在覆盖率收尾 M1 与 REFACTOR-0005 阶段 A 之后）
- 创建时间：2026-09-18
- 更新时间：2026-09-18
- 关联计划：PLAN-20260918T064119294Z（后端架构与性能优化改革，波次 R3）
- 关联模块：后端（`backend-go/projects/gateway/internal/chat`、`backend-go/projects/gateway/internal/gatewaydispatch`）
- 框架复用：[REFACTOR-0005 accounts 包子域拆分设计](重构-0005-accounts包子域拆分设计.md)（目标形态 / 子域边界表 / 共享类型归属 / 循环依赖检查 / 分阶段实施）
- 组合根侧联动：[REFACTOR-0007 组合根cmd拆分设计](重构-0007-组合根cmd拆分设计.md)（本包的 6 个 chain_chat_* 消费文件与 16 个 chain_* 消费文件的归属见该文档）

## 重构目标

- `internal/chat` 单包 27 个生产文件 / 16,085 行 / `Store` 上 88 个方法（2026-09-18 实测），是评估文档 2.3 节第三大 god 包；`internal/gatewaydispatch` 单包 38 个生产文件 / 13,364 行（同日实测，较评估报告 13,287 漂移 +77，为工作区演进），是 /v1 调度链的核心引擎与扇入重灾区（评估文档 2.5 节：80 处引用）。
- 按子域拆分包级边界，缩小改动半径；与 REFACTOR-0005 一样保持"门面包名不变 + 类型 alias + 子域包"的已验证形态。
- 期望保留的边界：外部消费面零破坏（chat 6 个生产消费文件、gatewaydispatch 16 个生产消费文件，全部经 `chat.XXX` / `gatewaydispatch.XXX` 前缀引用）、/v1 链与 my-chat 流式行为不变、覆盖率不降（chat 当前在 <95% 缺口清单内，沿 M1 缺口清单收口不因重构回退）。
- 特殊约束（本设计最高优先级）：两包都处 /v1 热路径。chat 的 SSE 泵（`stream_execute.go` 内循环调用 `CollectOpenAIChatSse` / `CollectChatResponsesSse`、客户端写泵 `sse_write.go`、事件泵 `generation_runner.go`/`generation_registry.go`）与 gatewaydispatch 的上游传输（`transport.RequestUpstream`、`ReadStreamChunkWith*`、`body.go` 管道）必须保持直接函数调用，**拆分不得引入接口间接层**。包边界迁移（同签名自由函数跨包直调）允许；为拆分新增 interface/wrapper 一律禁止。

## 重构前问题

### chat

- 单一 `Store` 类型承载 88 个方法，分布在 7 个文件：会话/消息 CRUD（conversations 9 + turns 21）、上下文预算（context 15）、资产（assets 16 + assets_store 10）、窗口（windows 8）、核心（store 7）、分区（postgres_partitions 1）——与 accounts 的 god Store 同构，包内无编译期隔离。
- 生成子域（generation_* 8 文件 4,567 行）与流式子域（stream/sse 5 文件 2,672 行）互相交织：`stream_execute.go` 是 runner 的 execute 闭包（每轮模型调用经内部 /v1 链 → SSE 收集 → 工具编排）。
- 反向依赖交织点：`routes.go` 的 `Deps` 同时持有 `Store`、`Hub *GenerationHub`、`Compactions *CompactionService`（routes.go:85 起），而 `CompactionService` 又持有 `*Store`（compaction_service.go:70）、`loadImageEditReferences` 是定义在 generation_images.go:565 的 `Store` 方法（生成家族反向查询 assets 表）。
- 外部扇入极小但全是装配面：生产消费仅 6 个文件（全部在 `cmd/juhe-ai-gateway/` 的 chain_chat_* 家族），消费 4 个构造器（`NewStore`/`NewGenerationHub`/`NewCompactionService`/`Deps`）+ 约 25 个类型词汇（头部 `ChatTransportProtocol`×9、`ProtocolResponses`×8、`Deps`×8）。
- 文件级健康（与 accounts 不同）：无任何文件超 2000 行预警线（最大 turns.go 1,450）——chat 是纯包级 god，不是文件级问题。

### gatewaydispatch

- 词汇层与引擎层耦合在同一包：`ports.go`（926 行）承载全部共享词汇（约 90 个类型/端口），`Engine` 方法散布 12 个文件（4,813 行）；`FetchFirstAvailableUpstream` 单函数 750+ 行。
- 外部扇入以类型词汇为主：`gatewaydispatch.AccountCandidate`×108（该类型本身已是 `= gatewayruntimecache.OpenAIAccountSecret` 别名）、`SuppressionFilterResult`×21、`PreparedRequestParts`×17、`GatewayUpstreamResponse`×15（定义在 transport.go:35）；生产消费 16 文件（14 个 cmd chain_* 文件 + `gatewayresponse/finalize.go`、`nonstream.go`）+ 测试消费 64 文件 = 80，与评估文档扇入表吻合。
- 反向边唯一且清晰：`engine.go:144` 的 `Engine.CandidatePipelineOf()` 返回 `*CandidatePipeline`（pipeline 包住 engine 的反向构造边），全工作区唯一调用点是 `chain_compose.go:564`。
- 候选/准备纯函数族（candfilters/capacity/fallbackcandidate/serialized/preparation/bodypreparation 共 1,937 行）实测零 `Engine` 引用、零 pipeline 引用，天然是叶子；传输族（3,177 行）的类型自含（`UpstreamHeaderAccount`/`UpstreamRequestOptions`/`TransportDeps` 均定义在 transport.go 自身）；oauth/codex 族（1,336 行）仅被 upstreamdispatch 单向消费（10 处调用点）。
- R5 正在本包建 bench harness：现有 `dispatch_bench_test.go` + `benchfixtures_bench_test.go` 两个 `*_bench_test.go` 文件，基准对象是调度引擎（根包），设计要求 bench 文件随其基准对象归属。

## 拆分设计

### chat 目标形态

```text
internal/chat/                     # 兼容门面（保持包名不变）
├── store.go / conversations.go / turns.go / context.go / windows.go
│                                  # Store 核心：会话/消息/上下文（Store + 61 方法）
├── routes.go                      # my-chat 路由层 + Deps（组合根唯一装配入口）
├── transport.go                   # ChatTransportProtocol 等共享词汇 + toolDefinition
├── errors.go / generation_errors.go
├── generation_*.go                # 生成子域：runner/hub/tools/images/context（热路径事件泵，留守）
├── stream_route.go / stream_execute.go / sse_write.go / gateway_sse.go / responses_sse.go
│                                  # 流式子域：SSE 泵（热路径，留守，直调不间接）
├── compaction_service.go          # 压缩服务（评估项：Store 强耦合，默认留守）
├── postgres_partitions.go / hex_encode.go
└── chatassets/                    # 阶段 A：资产子域（Store 26 方法 → AssetStore 服务化）
```

热路径文件清单（留守根包，保持直调，禁止接口化）：`stream_execute.go`（SSE 泵消费端，:92/:108 直调两个收集器）、`gateway_sse.go`（上游 Chat Completions SSE 收集）、`responses_sse.go`（上游 Responses SSE 收集）、`sse_write.go`（客户端写泵 + 心跳）、`generation_runner.go`（runner 事件分发）、`generation_registry.go`（Hub 订阅泵）、`transport.go`（请求构建，每轮模型调用经过）。

说明：`gateway_sse.go`/`responses_sse.go` 是自含叶子解析器（仅依赖 encoding/json、io、regexp、sort、strings、unicode，无包内其他类型依赖，仅导出 `ChatToolCall` 被生成家族消费）。若后续需要进一步缩根，可作为阶段 C 评估整体平移为 `chatssecollect` 子包——前提是函数签名逐字节不变（仍是自由函数直调），并在 R5 bench 上核对无回归；默认方案留守。

### gatewaydispatch 目标形态

```text
internal/gatewaydispatch/          # 兼容门面（保持包名不变）
├── doc.go / shims.go
├── errors.go / errorhelpers.go
├── engine.go / enginehelpers.go   # 引擎核心（热路径编排，留守直调）
├── upstreamdispatch.go / dispatchsingle.go / upstreamattempts.go
├── attemptoutcomes.go / attempt.go / circuitfacade.go
├── apikeyrotation.go / keymodelcapability.go
├── pipeline.go / candidatefilter.go   # CandidatePipeline 门面（实现 gatewaypreauth.CandidatePipeline 冻结端口）
├── accountpreparation.go          # Engine 方法族（账户/密钥准备）
├── gatewaydispatchports/          # 阶段 B：词汇层（ports.go + util.go 下沉为叶子包）
├── gatewaycandidates/             # 阶段 B：候选过滤/容量/回退/准备纯函数族
├── gatewayupstream/               # 阶段 A：上游传输族（HTTP/SSE 泵/首字节死线/头策略）
└── gatewayoauthcodex/             # 阶段 A：OAuth normalizer/adapter + codex 规避
```

R5 bench 文件归属：`dispatch_bench_test.go` / `benchfixtures_bench_test.go` 基准对象是调度引擎，随根包保留；若阶段 A/B 后基准输入改经子包构造，bench 文件随被测对象同包迁移（保持 `go test -bench` 可重放）。

### 子域边界（按实测文件/行数）

#### chat（27 文件 16,085 行；另有 49 个测试文件 21,186 行随迁）

| 子域 | 文件（行数） | 生产行数 | 阶段 |
| --- | --- | --- | --- |
| 留守：Store 核心 | store.go 279 / conversations.go 927 / turns.go 1,450 / context.go 1,154 / windows.go 210 / postgres_partitions.go 134 / hex_encode.go 7 | 4,161 | — |
| 留守：生成 + 流式（热路径） | generation_runner.go 1,219 / generation_tools.go 680 / generation_images.go 615 / generation_deps.go 627 / generation_context.go 569 / generation_parameters.go 469 / generation_registry.go 263 / generation_errors.go 125 / stream_route.go 860 / stream_execute.go 645 / responses_sse.go 656 / gateway_sse.go 288 / sse_write.go 223 | 7,239 | —（其中 responses_sse + gateway_sse 944 行为 C-选评估项，见下） |
| 留守：路由/词汇/错误/压缩 | routes.go 1,308 / transport.go 735 / compaction_service.go 920 / errors.go 140 | 3,103 | — |
| `chatassets` | assets.go 610 / assets_store.go 554 / asset_routes.go 418 | 1,582 | A |
| `chatssecollect`（可选评估项） | gateway_sse.go 288 / responses_sse.go 656（已含在上行留守行数内） | (944) | C-选（可留守） |

合计核对：4,161 + 7,239 + 3,103 + 1,582 = 16,085（`chatssecollect` 为留守行数的子集，不重复计入）。

注：生成家族中 `loadImageEditReferences`（generation_images.go:565，`Store` 方法）是唯一寄生在生成家族里的 assets 查询，阶段 A 随 assets 子域迁入 `chatassets`，生成侧改经窄接口消费（见循环依赖检查）。

#### gatewaydispatch（38 文件 13,364 行；另有 44 个测试文件 18,738 行 + 2 个 bench 文件）

| 子域 | 文件（行数） | 生产行数 | 阶段 |
| --- | --- | --- | --- |
| 留守：引擎核心 + pipeline 门面 + 准备方法 | upstreamdispatch.go 1,473 / dispatchsingle.go 981 / attemptoutcomes.go 527 / enginehelpers.go 421 / errors.go 317 / apikeyrotation.go 227 / errorhelpers.go 191 / upstreamattempts.go 187 / engine.go 168 / keymodelcapability.go 135 / circuitfacade.go 134 / attempt.go 52 / pipeline.go 144 / candidatefilter.go 234 / accountpreparation.go 668 | 5,859 | — |
| 留守：词汇层（阶段 B 前先原地） | ports.go 926 / util.go 62 / doc.go 27 / shims.go 40 | 1,055 | B（下沉叶子） |
| `gatewayupstream` | transport.go 906 / body.go 1,056 / usageheaders.go 296 / clientheaders.go 263 / headerpolicy.go 254 / firstbytedeadline.go 249 / transport_urlpolicy.go 153 | 3,177 | A |
| `gatewayoauthcodex` | oauthnormalizer.go 569 / oauthnormalizer_overrides.go 366 / oauthadapter.go 209 / builtintools.go 81 / codexturnavoidance.go 68 / providerprotocol.go 43 | 1,336 | A |
| `gatewaycandidates` | preparation.go 768 / candfilters.go 370 / bodypreparation.go 268 / capacity.go 257 / fallbackcandidate.go 201 / serialized.go 73 | 1,937 | B |
| 留守（bench） | dispatch_bench_test.go + benchfixtures_bench_test.go（测试文件，不计生产行） | 0 | 随根包 |

### 共享类型归属

#### chat

- **core（门面根保留）**：`Store` 及其 61 个核心方法、SQL 原语（`postgres_partitions.go`/`hex_encode.go` 的方言与编码设施）、`Conversation`/`Message`/`ContentBlock`/`ChatMessageRole` 等领域类型、`errors.go` 全部错误类型、`Deps` 及 4 个构造器（`NewStore`/`NewGenerationHub`/`NewCompactionService` + `Deps.Register`）、生成与流式全部类型（`GenerationHub`/`ChatGenerationRunner`/`ChatGenerationEvent`/`ChatGenerationSubscriber`/`GenerationIdentity`/`AttachStreamHandler`）、`ChatTransportProtocol`/`ProtocolChatCompletions`/`ProtocolResponses`/`ChatTransportAccount`/`ChatTransportModelMapping`/`toolDefinition`（transport.go，生成家族与 cmd 共同消费的词汇）。
- **chatassets 子域**：`Asset`/`AssetDeletionClaim`/`AssetAPIMetadata`、`AssetQuotaExceededError`/`AssetCountExceededError`、`CreateChatAssetInput`/`CompleteAssetProcessingInput`/`GeneratedAssetCommitInput`/`GeneratedImageGenerationRecord`/`ImageGenerationRecord`/`CompactionSourcePage` 随文件迁入并按消费面定导出（包外当前无引用的转私有）；原 26 个 `Store` 方法改为 `AssetStore` 服务类型方法（持有 db 句柄 + 方言 + 时钟，REFACTOR-0005 "Store 方法改子域服务"同一模式），门面根保留转发方法供 `compaction_service.go` 的 `CompactionSourcePage` 查询等包内消费（转发体一行直调，非接口间接层）。
- **跨域消费类型**：`ObjectStore` 接口与 `LocalObjectStore`（定义在 generation_images.go）留根——assets 与生成双侧消费；`chatssecollect` 若拆出，`ChatToolCall`/`OpenAIChatSseResult`/`ChatResponsesEvent`/`ChatResponsesCollectionResult`/`ImageResultSink` 随迁并在门面根留 alias。

#### gatewaydispatch

- **gatewaydispatchports（阶段 B 叶子）**：`ports.go` 全部内容原样下沉——约 90 个类型/端口（`AccountCandidate`/`ModelPriority`/`HotQualityReservation` 别名三件套、`SuppressionPort`/`DegradationPort`/`AccountLocks`/`RuntimeCachePort` 等端口接口、`AuditCapture`/`FailedAttemptRecord` 等结构）+ `util.go`。门面根保留同名 alias 转发（`type AccountCandidate = gatewaydispatchports.AccountCandidate` 等），16 个生产消费文件 + 64 个测试消费文件的 `gatewaydispatch.XXX` 引用零改动——与 R1 波次 Clock 别名迁移同一模式。
- **gatewayupstream 子域**：`GatewayUpstreamResponse`/`UpstreamHeaderAccount`/`UpstreamRequestOptions`/`TransportDeps`/`LimitedBodyReadInput`/`FirstByteDeadline*` 族随文件迁入（类型自含已实测：三个人口类型均定义在 transport.go 自身）；门面根 alias 转发（`gatewayresponse` 消费的 `GatewayUpstreamResponse`×15、FirstByteDeadline 族×10+ 必须保持前缀合法）。
- **gatewayoauthcodex 子域**：oauth normalizer/overrides 的输入输出类型随迁（当前包外无引用，转私有优先）；`codexturnavoidance`/`builtintools`/`providerprotocol` 随迁。
- **gatewaycandidates 子域**：`CandidateFilterOutput`/`CandidateFilterArgs` 等随迁；`CandidatePipeline` 类型与其 `FilterOpenAIGatewayRequestCandidateAccounts` 方法留守根包（持有 `*Engine`，实现冻结端口 `gatewaypreauth.CandidatePipeline`）。

### 循环依赖检查（关键约束）

#### chat

两个反向依赖点，均已有收敛方案：

1. **compaction ↔ 根**：`CompactionService` 持有 `*Store`（compaction_service.go:70），根包 `Deps.Compactions` 又持有 `*CompactionService`（routes.go）——若 compaction 拆出子包即成环。**默认解法：不拆**，CompactionService 留守门面根（它消费 turns/context 的 Store 方法面太宽，窄接口化成本高于收益）。
2. **generation ↔ assets**：`loadImageEditReferences`（generation_images.go:565，`Store` 方法）查 assets 表；阶段 A assets 迁出后，该方法的宿主改为 `chatassets.AssetStore`，生成侧经门面装配注入的窄接口 `AssetEditReferenceReader`（生成家族定义、`chatassets` 实现、`Deps` 装配注入）消费——方向收敛为 root → chatassets 单向，无环。assets 拆出后 `chatassets` 需要的 `Asset` 领域类型与错误类型随迁自带，不回引根包；若实现中发现 `chatassets` 反向需要根类型（如 `Conversation`），回退方案与 REFACTOR-0005 transfer 相同：该文件/方法留守根包，阶段 A 只迁 assets_store.go + asset_routes.go（1,197 行）。

其余留守子域同包不产生新边；`chatssecollect`（若拆）是纯叶子解析器，仅被根包单向 import，无环风险。

#### gatewaydispatch

唯一反向边：`engine.go:144` `func (e *Engine) CandidatePipelineOf() *CandidatePipeline { return NewCandidatePipeline(e) }`。若 `gatewaycandidates` 拆出且候选族经根包类型消费词汇，则"根 → candidates（调用纯函数）"与"candidates → 根（Engine/词汇）"成环。解法按序：

1. **阶段 B（ports 下沉后天然无环）**：`gatewaydispatchports` 成为叶子，根与全部子包单向依赖它；`gatewaycandidates` 只依赖 ports 叶子（已实测候选/准备族零 `Engine` 引用）；根调用候选纯函数单向。`CandidatePipelineOf` 方法此时仍引用 `*CandidatePipeline`（根内类型）无需删除——pipeline.go/candidatefilter.go 留守根包。
2. **阶段 A 过渡期（ports 尚未下沉）**：`gatewayupstream`/`gatewayoauthcodex` 已实测不引用根内词汇类型（传输族类型自含；oauth 族若实现中发现引用 `AccountCandidate` 等词汇，则将该函数留守根包或改注源头类型 `gatewayruntimecache.OpenAIAccountSecret`——优先前者）。阶段 A 不动 `Engine.CandidatePipelineOf`。
3. 验收断言：每阶段 `go vet ./...` + `go list -deps` 断言 `internal/gatewaydispatch` 及其子包无 import cycle。

## 变更范围

- 受影响：`internal/chat/` 27 个生产文件（阶段 A 动 3 个）+ 49 个测试文件随迁；`internal/gatewaydispatch/` 38 个生产文件（阶段 A 动 13 个）+ 44 个测试文件与 2 个 bench 文件按被测对象随迁；新增 1（阶段 A chat）~4（阶段 B 后累计）个子域包目录。
- 不动：chat 的 6 个生产消费文件与 gatewaydispatch 的 16 个生产消费文件（`chat.`/`gatewaydispatch.` 前缀引用经门面 alias 保持合法）、`cmd/juhe-ai-gateway` 的 `Deps`/`NewStore`/`NewGenerationHub`/`NewCompactionService`/`Engine`/`CandidatePipeline` 装配调用、路由路径、SQLite/PG 双模语义、`gatewaypreauth.CandidatePipeline` 冻结端口实现关系。
- 文档联动：本文件 + `docs/plans/` 计划状态 + `docs/refactors/README.md` 索引 + 评估文档 god 包表复测。
- 与覆盖率攻坚协调：chat 在 M1 六包缺口清单内（w16c 测试文件会出现），本设计以 2026-09-18 生产文件集为准，忽略测试文件增删；chat 阶段 A 实施须与 w16c 收尾写入错峰（同包写入冲突），排在 M1 chat 收口之后。

## 行为边界

- 保持不变：my-chat 全部 HTTP 路由与方法面、SSE 事件字节布局（`event: <type>\ndata: <json>\n\n`、5s 心跳）、/v1 调度链行为（候选过滤顺序、重试分类、退避契约、首字节死线、suppression/降级语义）、错误文案、SQLite/PG 双模 SQL、审计事件与 metadata 字段、R5 bench 基准可重放性。
- 明确变化：仅包结构（新子域包）+ assets 26 个 Store 方法的宿主改为 `AssetStore`（阶段内一次性完成，不保留双入口；门面根保留一行直调转发）。
- 热路径保证：SSE 泵与上游传输全程自由函数直调；拆分不引入任何 interface/wrapper/闭包转发；阶段 A 交付后在 R5 bench 上复测 dispatch 与 chat 流式基准，无回归才验收。

## 分阶段实施与验收

| 阶段 | 内容 | 验收 |
| --- | --- | --- |
| A-1 | `gatewayupstream` 拆出（3,177 行，类型自含叶子） | 该包测试全绿 + 门面 alias 下 16 个消费文件零改动 + `go vet` 无环 + R5 dispatch bench 无回归 |
| A-2 | `gatewayoauthcodex` 拆出（1,336 行） | 同上；若发现 oauth 函数引用根词汇，按循环依赖检查 §3 回退 |
| B-1 | `gatewaydispatchports` 下沉 + 根 alias 转发（80 个消费文件零改动断言） | 全部消费文件前缀引用不变 + 全绿 |
| B-2 | `gatewaycandidates` 拆出（1,937 行） | 同上 + `go list -deps` 无环 |
| C | `chatassets` 拆出（1,582 行，AssetStore 服务化 + 窄接口收敛） | 该包测试全绿 + my-chat 手动/验收路径回归 + 覆盖率不降 |
| C-选 | `chatssecollect` 评估（944 行，仅当根包仍超目标时） | 签名逐字节不变 + R5 chat 流式 bench 无回归，否则放弃留守 |
| 收尾 | chat 门面根目标：阶段 C 后 ≤14.6k（16,085 − assets 1,582）、C-选后 ≤13.6k；gatewaydispatch 门面根目标 ≤6.1k（留守 5,859 + ports alias 转发文件）；复盘更新本文件与评估文档 god 包表 | 两包 `go build ./... && go test ./internal/chat/... ./internal/gatewaydispatch/... -count=1` 全绿 + `-cover` 覆盖率不降 |

每阶段验证命令：`cd backend-go/projects/gateway && go build ./... && go vet ./... && go test ./internal/<包>/... -count=1`，热路径阶段加 R5 bench 对比。

## 风险与后续

- **热路径回归风险（最高）**：SSE 泵与传输族的任何间接化都会进入每请求路径。缓解：留守清单硬编码进评审单；阶段 A-1/C 必须附 R5 bench 前后对比；发现间接化即回退。
- **测试随迁断裂**：chat 21,186 测试行 / gatewaydispatch 18,738 测试行与生产文件同包强绑定，且 chat 正被覆盖率 agent 持续写入（w16c）。缓解：chat 阶段 C 排 M1 之后；gatewaydispatch 测试按被测文件前缀成批 `git mv` 等价改名 + 包名调整（REFACTOR-0005 阶段 A 已验证的随迁模式先行小批量试点）。
- **bench 文件漂移**：R5 与本设计并行期 bench 文件持续演进；阶段 A-1 实施前与 R5 owner 对齐 bench 文件归属快照，避免同文件双写。
- **oauth 族词汇回流**：oauthadapter/normalizer 若实测引用 `AccountCandidate` 等根词汇，阶段 A-2 按回退方案执行（留守），不影响 A-1 与阶段 B 收益（4,513 行先行出包）。
- **与 REFACTOR-0005/0007 的协调**：accounts 拆分（0005）与本设计的实施窗口互斥共享覆盖率收尾配额；本包的 cmd 侧消费文件（chain_chat_* 家族、chain_dispatch/chain_error_policy 等）的归属与下沉见 REFACTOR-0007，两侧不交叉写入同一文件。
- 后续同类：`gatewaycircuit`（11,190）/ `gatewayresponse`（10,169）等 god 包可在本框架验证后复用。
