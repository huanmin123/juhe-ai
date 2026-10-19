# 问题-0274：Codex Responses↔Chat 桥状态在生产从未装配（含 retention segments 根错位）

- 编号：BUG-0274
- 状态：已修复（2026-10-03），待发布
- 发现：BUG-0269 后的全量排查（接线断线审计）。本缺陷代码注释自证："2026-09-28 哲学裁定补齐（功能默认开启）……迁移漏装配，与终局波 TrafficRuntimeMigrator 同类断线"——补齐只写了消费侧，组合根装配从未落地。

## 现象

`chainRuntimeDeps.CodexBridge` / `CodexContextRoot` / `CodexContextStateStore`（`chain_compose.go:135-148`）全仓无任何赋值点，gateway 不读 `JUHE_AI_CODEX_CONTEXT_ROOT`（jobs/retention 读，gateway 只把 `codex-context` 当目录名常量）→ `newChainCodexBridgePreflight` 永不构造，`codexPreflightAdapter` 恒 no-op。后果：chat-only 桥账户上 Codex `/responses` 多轮请求的 `previous_response_id` 上下文还原与 compact 前置校验全部缺席。既有 `TestW1CodexBridge*` 手工注入 deps，对生产断线不敏感（装配前全绿）。

## 修复

- `runtimeConfig` 新增 `CodexContextRoot`，派生 `datadir.Path(getenv, dataDir, "JUHE_AI_CODEX_CONTEXT_ROOT", datadir.CodexContextRoot)`（显式 env 优先、缺省 `<DATA_DIR>/codex-context`，与 jobs/文档默认一致）。
- 新文件 `codex_context_store.go`：`newChainCodexContextStateStore`——空根 (nil,nil) 保持既有 no-op 退出；postgres 分支 `Acquire` 业务池（同 role/参数复用）+ `NewPostgresContextStateStore` + `SELECT 1 FROM juhe_codex_context.codex_context_sessions LIMIT 0` 探针，**失败启动 fail-fast**（生产 schema 由 maintenance ensure-schema 预置，缺失属部署错误必须启动期暴露）；sqlite 分支 `NewSQLiteShardContextStateStore`（分片 schema 由组合根前置预检）。
- `compose.go`：deps 注入两键；sqlite 行存储注册 shutdown（LIFO 防句柄锁）。
- **配套缺陷（同批）**：jobs retention 删除根错位——`worker_retention.go` 用 `store.ShardRoot`（state-shards 根）解析 segments 相对路径，永远 miss 且"文件不存在=成功"把删除按成功键落账 → segment 文件永不清理（存储泄漏）。修复：jobs `CodexContextRoot` 同款 datadir 派生，`codexStorageProcessor` 删除根改用 segments 根，"不存在=成功"语义保持。
- **残余补齐（2026-10-19 compact 派发装配批）**：compact 合成摘要 dispatcher 装配——`CompactSummaryDispatchInput` 补齐引擎透传字段（usage/audit/avoidance/groupPolicy/lane/compat/priority/coordination）；新增 `cmd/juhe-ai-gateway/chain_codex_compact_dispatch.go` 复用 `Engine.FetchFirstAvailableUpstream` + `ReadUpstreamBodyLimited`，组合根晚绑定回填 `Dispatcher`（`chain_compose.go`），`chain_ports.go` 缺席注释改写。**同期根因修复**：`ReadUpstreamBodyLimited` 在 `(n>0, io.EOF)` 合并返回时先 break、末批数据静默丢弃（io.Reader 契约违背，Node 原版无此形态，属移植缺陷），改为数据先入捕获再处理 EOF，新增回归三用例 `gatewaydispatch/bodyread_coalesce_test.go`。

## 部署契约同步

`docker/single-server/README.md`（env 契约：gateway 新增读取 `JUHE_AI_CODEX_CONTEXT_ROOT`，可选、缺省 `<数据根>/codex-context`、双进程必须同值；PG 模式要求 juhe_codex_context schema）、`docs/deploy/部署指南.md`、`docs/functions/SQLite存储说明.md`、`docs/develop/测试与验证说明.md` 隔离 env 块。

## 测试

`TestComposeSystemAPIWiresCodexBridgeState`（生产组合根 sqlite 装配断言 bridge/compact/registry 非 nil，装配前必红）；`TestNewChainCodexContextStateStoreArms`（空根降级、PG nil 池 fail-fast）；jobs `TestCodexStorageProcessorDeletesUnderSegmentsRoot`（异源 shardRoot 下修复前必红，"不存在=成功"保持）；compact 派发装配批新增 `TestCompactSummaryDispatchUpstreamSuccess/UpstreamFailurePropagates/EmptyChoicesPassesThrough`（适配器三路径，httptest 账户 BaseURL 走真实 HTTP）与 `TestReadUpstreamBodyLimitedCoalescedEOF*` 三用例。

## 残余项

- PG 探针 fail-fast 的真实缺 schema 场景未做运行断言（生产前置为 ensure-schema）。

## 桥接七组合阶段级行为验收记录（T27，2026-10-19）

`调度内核通用化设计` §5.4 / D3 要求的七组合阶段级验收已执行（测试 `backend-go/projects/gateway/cmd/juhe-ai-gateway/w27_bridge_combos_test.go`，`TestW27Combo1..7` + `TestW27PostStageSkipIfProcessedMarkDedup`）。oracle 为真实 bridge preflight/restore（组合根真实装配的 `gatewaycodex.ChatBridgeStateService` / `CompactPreflightService` / registry，segments + 双模行存储全真实）；阶段级观察点为 `Engine.BuildPreparedUpstreamRequestParts` 三阶段（Front 内联清理 / OAuth 适配器出站构造 / Post 序列化 body 清理），清理调用以注入 `gatewayoauthcodex.SanitizeCodexHistory` 的探针 hook 计数，并以 hook 恒 nil 的生产形态做 no-op 对照。

**两个如实断言的结构现状（结构性等价的组成部分，不是「无需清理」）**：

1. **清理算法 hook 生产恒 nil**：各组合均以 hook 注入（阶段命中与调用次数取证）+ hook nil（清理 no-op、出站字节不受清理维度影响）双形态驱动；OAuth 路径的已处理标记写入与 hook 无关、无条件生效（nil 下依旧写入，测试钉住）。
2. **引擎侧 per-account 桥准备端口无生产装配**：`Engine.CodexBridge`（`accountpreparation.go:457` 的 `PrepareContextForAccount` 消费点）全仓唯一赋值在 `gatewaydispatch` w17a 测试；BUG-0274 装配的是 preauth 层桥（preflight/compact restore/registry）。因此 bridge restore 的 registry 产物**不落入 dispatch 请求体**（组合 2/4 断言 dispatch body 仍携带原始 `previous_response_id`）；per-account 桥准备行为（合并 input 写回、删内部 previous_response_id、拒绝外部 previous）以真实桥服务 `PrepareCodexResponsesContextForAccount` 直接取证。§5.4 接线状态更新中「#2 已由 BUG-0274 装配」的表述仅覆盖 preauth 层，引擎端口仍未接线——接线属 §5.4 语义下的后续立项（行为变更，需单独评估），本验收不改生产代码。

**逐组合结论**：

| 组合 | 结论 | 阶段命中 / 标记（oracle 与断言点） |
| --- | --- | --- |
| 1. Responses 新会话 → Chat bridge → API Key | 可达，通过 | preflight oracle：registry `responses/none`、审计 `new_session`；桥准备接管 `ActiveBridgeAccountID`。清理调用 Front 1 + Post 1、Adapter 0（探针标记双落点取证：请求上下文 + 序列化出站）；API Key 出站**不写标记**；hook nil 下 input 原样 |
| 2. Responses 内部 previous（restore 成功）→ Chat bridge → API Key | 可达，通过（dispatch body 不消费恢复产物——引擎端口未接线的现状） | restore oracle：真实快照链恢复（`restored` 审计、合并 input ≥2 轮）；桥准备删 `previous_response_id`、合并 input 写回；清理 Front 1 + Post 1、不写标记；dispatch body 保留原始 `previous_response_id`（现状证据） |
| 3. Responses 新会话 → Chat bridge → OAuth | 可达，通过 | 桥准备 oracle：responses→chat 映射使 OAuth 账户成 bridge-kind；清理调用恰 1 次（Adapter；Front/Post preserve），请求上下文未被改写；出站**无条件写标记** + Codex 归一化形态（`store=false`/`stream=true`）；hook nil 下标记照写、input 原样 |
| 4. Responses 内部 previous（restore 成功）→ Chat bridge → OAuth | 可达，通过 | 组合 2 的恢复事实 + 组合 3 的清理形态（Adapter 1 次 + 标记）；dispatch body 保留原始 `previous_response_id` |
| 5. `/responses/compact` 恢复已有 bridge snapshot → API Key | 可达，真实派发成功 | 晚绑定 `chainCompactSummaryDispatcher` → 真实引擎 → httptest 上游恰好 1 次（POST `*/v1/chat/completions`、Bearer 凭据）；restore/审计 `gateway_summary_compact` + `restoredResponseCount≥1`；200 `response.compaction` + 审计 finalize；合成 chat 请求 + API Key 目标 = 策略全 preserve：0 次清理调用、出站原始字节**不写标记**；终态/释放闭包语义由 #T5 批既有断言钉住，本组合锁定其在完整 preflight 路径中被服务消费 |
| 6. `/responses/compact` 恢复已有 bridge snapshot → OAuth | 过滤层可达、**适配器层语义不可达**（如实登记，非「无需清理」） | OAuth 账户按映射归 bridge-kind 并进入 bridge 派发窗（`CompactDispatchMode=bridge`）；但 OAuth codex 适配器要求 Codex Responses 形态（必须含 `input` 字段），合成 chat body 在出站构造校验处被拒 → 502 `codex_bridge_compact_summary_failed`，上游 0 请求；校验先于清理调用点故 0 次调用。OAuth 分支恒产出 Codex Responses 形态出站，chat-bridge 合成摘要在该形态下不可达——与 §5.4「OAuth 恒走 codex adapter，部分矩阵组合静态不可达」一致，现以运行证据落档 |
| 7. 恢复失败/无快照后继续普通请求 | 通过（含一处与组合标题的源码事实差异登记） | 外部 previous（无桥快照）→ preflight 继续（`external_previous_response`），按普通请求进入派发准备：API Key 原生 = Front+Post 2 次、不写标记、`previous_response_id` 原样出站；OAuth 原生 = Adapter 1 次 + 无条件标记。**内部 previous 快照缺失**：preflight 以 404 `codex_bridge_previous_response_not_found`（`restore_failed`）终结，**不**继续普通派发——「继续普通请求」语义仅覆盖外部 previous/无桥状态形态，差异如实登记。另钉：外部 previous × 桥账户在真实桥准备服务处拒绝（「外部 previous_response_id 只能发送给原生 Responses 账号」；引擎端口未接线，该拒绝当前不在生产派发生效） |

**已处理标记去重（SkipIfProcessed）行为钉**（`TestW27PostStageSkipIfProcessedMarkDedup`）：Post 阶段（`policy.Post=sanitize && SkipIfProcessed`）带标记 body 跳过清理（0 次调用、字节原样），无标记 body 恰 1 次调用——§5.4「后置遇到标记必须跳过」以调用次数取证。

**验证口径**：`go test ./cmd/juhe-ai-gateway/ -run 'TestW27' -count=1`（定向）与 `go test ./internal/gatewaycodex/ -count=1`（桥服务回归）。本验收为纯测试 + 文档交付，生产代码零改动；验收未暴露需修复的清理维度缺陷，暴露的接线现状（引擎端口未装配）已在上文登记并属 §5.4 语义下的后续立项决策。
