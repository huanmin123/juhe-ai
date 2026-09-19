# BUG-0178 Responses 到 Chat 桥执行门控缺失致端到端断裂

## 基本信息

- 编号：BUG-0178
- 状态：已修复（2026-09-19 方案 A 落地；双侧裁决测试转绿、受影响面回归通过、独立复审"可入库"；组合根全包回归 2 项环境性失败经隔离复跑排除）
- 严重程度：P1
- 发现时间：2026-09-19
- 发现方式：GLM 三协议适配方案（PLAN-20260908T162203434Z）落地前复审 + 静态取证 + 运行时裁决测试
- 模块：Go 后端 / `shared/platform/openaicompatcore` / `internal/gatewayopenai` / `cmd/juhe-ai-gateway`（chain 响应变换）
- 关联计划：[PLAN-20260908T162203434Z GLM 三协议适配方案](../plans/计划-20260908T162203434Z-GLM三协议适配方案.md)（方案 L82"保留现有 bridge"的前提受本缺陷影响）
- 关联 bug：无

## 问题概述

`responses -> chat_completions` 桥（Codex Responses 等客户端经显式模型映射使用 GLM / DeepSeek / OpenAI 兼容 chat 账户）在 Go 网关中**许可层放行、执行层门控缺失**，端到端断裂：

- 许可/识别层认可该映射：`accounts/model_mapping_protocol_matrix.go` 允许 openai 协议账户与混合供应商配置该组合；`gatewayopenai/mapping.go`（`modelMappedUpstreamPathAndQuery`）把 `/responses` 请求改写到上游 `/chat/completions`；`gatewaycodex/chatbridgestate.go`（`codexResponsesCompactAccountKind`）把该映射账户识别为桥账户。
- 但两个转换执行门都依赖 `IsCrossProtocolBridgeRequired`（`shared/platform/openaicompatcore/config.go`），其组合矩阵不含 `responses -> chat_completions`：
  - 请求体转换（`gatewayopenai/driver.go` `BuildUpstreamRequest` 的 B-4 桥分支）不触发；
  - 响应回转（`cmd/juhe-ai-gateway/chain_bridge_response.go` `TransformUpstreamResponseForAccount` 前置门控）不触发，`bridgeStreamPump` 与缓冲分支中已完整移植的 `FamilyResponses -> FamilyChatCompletions` 回转实现成为不可达代码。
- 对照信号：同样走路径改写的 `anthropic -> chat`、`gemini -> chat` 都在矩阵内，唯独该组合缺席；且 `gatewayopenai/driver.go` B-4 注释自述覆盖 "codex-responses-chat"，与矩阵实际内容矛盾。
- Node 归档 `backend/src/domain/openai-endpoint-modes.ts` 的 `supportsCodexResponsesChatBridge` 白名单明确包含 GLM general/coding chat 档案（Node 中该桥为受支持的一等能力）；Go 侧未移植该判定。

## 运行时证据（2026-09-19 裁决测试）

- 请求侧：POST `/v1/responses` + openai 协议档案账户显式 `responses -> chat_completions` 映射，上游 URL 正确改写为 `/chat/completions`、模型覆写生效，但请求体仍为 Responses 格式原样发往 chat 上游：
  `{"input":"hi","instructions":"be brief","model":"glm-upstream","stream":true}`（无 `messages` 字段）。
- 响应侧：chat SSE（`data: {"choices":...}\n\ndata: [DONE]`）与 chat JSON 原样透传给 Responses 客户端，未回转成 Responses SSE/JSON。
- 裁决测试（已删除 `t.Skip`，现为常驻回归防护）：
  - `backend-go/projects/gateway/internal/gatewayopenai/glm_responses_chat_bridge_adjudication_test.go`
  - `backend-go/projects/gateway/cmd/juhe-ai-gateway/glm_responses_chat_bridge_adjudication_test.go`

## 复现步骤

1. 为 openai 协议 chat 账户（如 GLM coding chat 档案）配置显式模型映射 `responses -> chat_completions`。
2. 以 Responses 客户端 POST `/v1/responses`，观察上游收到 Responses 格式请求体（chat 端点报 400/语义错误）；直接观察响应侧则客户端收到 chat 格式响应。

## 环境信息

- 分支 / 版本：当前工作区（含 REFACTOR-0007/0008 结构重构改动）；HEAD（`e3be55c25`）与工作区矩阵一致，缺陷为迁移期遗留，非本批改动引入。
- 是否稳定复现：稳定（单元级裁决测试确定性复现）。

## 影响面

Codex Responses（或任意 `/v1/responses` 客户端）打到 openai 协议 chat 上游账户（GLM 两个 chat 档案、DeepSeek、hybrid chat）的显式映射链路：上游拒绝或语义错误，即使上游成功客户端也无法解析。网关直连/原生 Responses 路径（OpenAI 官方、Codex OAuth）不受影响。

## 根因分析（判断）

Node→Go 迁移时桥语义收口到 `IsCrossProtocolBridgeRequired` 矩阵，但该矩阵移植自跨协议对（OpenAI/Anthropic/Gemini 互转），遗漏了"OpenAI 族内 Responses->Chat"这一对；该桥无端到端测试（现有桥测试只覆盖矩阵内组合，映射与账户识别测试只覆盖配置许可层），故迁移后未暴露。

## 修复方案

### 已实施：方案 A（2026-09-19，两层）

第一层——执行门控：把 `FamilyResponses -> FamilyChatCompletions` 加入 `IsCrossProtocolBridgeRequired` 矩阵（`shared/platform/openaicompatcore/config.go`，矩阵 13 对扩为 14 对，doc 注释同步）。转换器、回转泵、桥账户识别均已就绪，为最小充分修复；端点模式闸经专项核查无回归——`requiredSupportedEndpointMode` 的映射分支本就按 `codexResponsesChatBridgeRequiredEndpointMode` 语义对 responses 源映射强制 `chat_sse`（GLM chat 档案持有，满足）。

第二层——链上 URL 路径改写（真机 E2E 验收暴露）：矩阵修复后请求体已转换，但链上 URL 构建器（`cmd/juhe-ai-gateway/chain_driver.go` `BuildGatewayUpstreamURLsForAccount` default 分支）使用客户端原始路径且丢弃 driver 计算的改写后 `PathAndQuery`，导致桥转换后的 chat 请求体仍打到上游原生 `/responses` 端点被拒。修复：`gatewayopenai.ModelMappedUpstreamPathAndQuery` 导出（driver.go 调用点同步改名），URL 构建器解析同一映射并应用同一改写函数，恢复"URL 与 body 转换同源"不变量。**附带修复**：hybrid 供应商 messages→chat、gemini→chat 组合此前在同一分支存在同类 URL/body 断裂，经同一 helper 一并修复（独立复审证据链确认）。

第三层——能力不变量守卫（第二层独立复审登记的矛盾配置窗口）：`responses -> chat_completions` 的 URL 改写与响应回转只服务 chat-only 账户。持有原生 Responses 端点模式的账户（codex OAuth、responses 模式 api_key）即使被配置了该映射，`/v1/responses` 仍按原生直通——URL 构建器与响应变换器（`chain_bridge_response.go`）经 `accountHasNativeResponsesModes` 成对守卫，恢复两类配置的既有语义：codex OAuth 账户原生 `/responses` 直通、codex_responses 画像客户端 compat body 原生直通。

同步改动：

- 四个裁决测试：请求体转换（gatewayopenai）、响应回转（cmd）、上游 URL 改写（cmd）、原生 Responses 账户守卫（cmd，URL 不改写 + 响应不回转两个子测试），删除 `t.Skip` 转为常驻回归防护。
- `cmd/juhe-ai-gateway/w14a_bridge_buffered_test.go` 两处过时注释修正：`FamilyResponses` 臂"不可达"声明改为"BUG-0178 修复起可达"；文件头不可达登记 6 处行号漂移对齐当前源码（HEAD 既有漂移，顺手修正）。
- `cmd/juhe-ai-gateway/acceptance/e2e_manual_real_test.go` 新增 `TestE2EManualRealResponsesBridge` 真机验收（B0 chat 基线 / B1 非流式桥 / B2 流式桥 / B3 无映射负对照；凭据全部走环境变量，不入源码）。

### 未采用：方案 B

移植 Node `supportsCodexResponsesChatBridge` profile 白名单作为执行门补充判定。归档中该桥执行层实现不全，语义需另行取证；方案 A 已达成同等效果，不采用。

### 已知边界（独立复审登记，非阻塞）

- ~~codex_responses 画像 / codex OAuth 账户的两个矛盾配置窗口~~：**已由第三层能力不变量守卫修复**——持有原生 Responses 端点模式的账户不参与桥改写与回转，`/v1/responses` 按原生直通（守卫钉子 `TestAdjudicateGLMResponsesToChatBridgeNativeResponsesAccount`）。
- **Node 对齐取证边界**：归档不含 `buildGatewayUpstreamUrlsForAccount` 原型，"URL 构建器是否应做映射改写"的 Node 语义无法核对；第二层修复的正确性依据是 Go driver 自身"URL/body 同源"不变量（driver 侧改写逻辑 HEAD 即存在）。
- **BUG-0179**：codex_responses 画像客户端在端点模式闸无法选中 chat-only 桥账户（预存在限制，Node 语义优先顺序待取证），Codex CLI 承接依赖原生 Responses 档案或该限制裁决。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 运行时裁决（修复前） | 请求侧桥断裂证据 | `go test ./internal/gatewayopenai/ -run TestAdjudicateGLMResponsesToChatBridgeRequestConversion -v`（修复前） | —— | FAIL：上游收到 Responses 格式 body（无 `messages`），路径改写与模型覆写正常 | 已完成 |
| 运行时裁决（修复前） | 响应侧桥断裂证据 | `go test ./cmd/juhe-ai-gateway/ -run TestAdjudicateGLMResponsesToChatBridgeResponseConversion -v`（修复前） | —— | FAIL：流式与缓冲均原样透传 chat 响应 | 已完成 |
| 修复后回归 | 请求侧桥转换 | 同上裁决测试（删除 Skip 后） | PASS，上游收到 chat completions 格式 | PASS：`{"messages":[{"content":"hi","role":"user"}],"model":"glm-upstream","stream":true}` | 已完成 |
| 修复后回归 | 响应侧回转 | 同上裁决测试（删除 Skip 后） | PASS，客户端收到 Responses SSE/JSON | PASS：Responses SSE 3366 字节 / Responses JSON 617 字节 | 已完成 |
| 包回归 | 桥相关受影响面 | `go test ./internal/gatewayopenai/ ./internal/gatewaycodex/ ./internal/openaicompat/... ./internal/gatewaydispatch/` | 全部 ok | 全部 ok（gatewaydispatch 52s） | 已完成 |
| 单元回归 | 矩阵真值 | `go test ./shared/platform/openaicompatcore/ -run TestIsCrossProtocolBridgeRequired` | ok | ok | 已完成 |
| 定向回归 | 组合根 Bridge 测试族 | `go test ./cmd/juhe-ai-gateway/ -run 'Bridge'` | 全部 PASS | 全部 PASS（18.9s） | 已完成 |
| 全包回归 | 组合根全量 | `go test -timeout 25m ./cmd/juhe-ai-gateway/`（约 19 分钟） | ok（或仅环境性失败） | 第一层后首跑：2 项 FAIL 均环境抖动（Windows TempDir 清理竞态、dev PG SQLSTATE 57P01），隔离复跑均 PASS；第二层后复跑：仅剩 TempDir 竞态 1 项（隔离 PASS），无桥相关失败 | 已完成 |
| 真机 E2E | 真实 chat-only 中转上游（OpenAI 兼容域名脱敏，实测原生 /v1/responses 被上游 503 拒绝）+ 显式映射 | `TestE2EManualRealResponsesBridge`（JUHE_AI_E2E_MANUAL=1，凭据走环境变量） | B0-B3 全 PASS | 第一层后 B1/B2 FAIL（暴露第二层：上游 URL 停在 /responses）；第二层后 **B0/B1/B2/B3 全 PASS**：客户端收到真实 Responses JSON（object=response、resp_openai_bridge_*）与 Responses SSE（event: response.created）；B3 负对照 endpoint_mode_unsupported；第三层守卫后复跑仍全 PASS | 已完成 |
| 守卫回归 | 原生 Responses 账户能力不变量（URL 不改写 + 响应不回转） | `TestAdjudicateGLMResponsesToChatBridgeNativeResponsesAccount` | 两子测试 PASS | PASS | 已完成 |
| 独立复审 | 第一层六点复审 + 第二层五点补充复审 | glm_5.3_high 只读复审 | 无 blocker | 两轮均"可入库"；第二层附 URL/body 同源证明、hybrid 顺带修复证据链、已知边界登记（见上节） | 已完成 |
| 独立复审 | 六点复审（矩阵/消费点/模式闸/codex 状态机/测试充分性/文档一致性） | glm_5.3_high 只读复审 | 无 blocker | 结论"可入库"，附 BUG-0179 登记与 w14a 行号修正两项跟进（均已处置） | 已完成 |
