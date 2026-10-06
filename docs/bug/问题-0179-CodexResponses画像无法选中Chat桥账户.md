# BUG-0179 Codex Responses 画像客户端在端点模式闸无法选中 Chat 桥账户

## 基本信息

- 编号：BUG-0179
- 状态：已修复（2026-10-06）
- 严重程度：P2
- 发现时间：2026-09-19（BUG-0178 修复独立复审中发现）
- 修复时间：2026-10-06
- 发现方式：`requiredSupportedEndpointMode` 专项核查
- 模块：Go 后端 / `cmd/juhe-ai-gateway`（chain_driver.go 端点模式闸）/ `internal/gatewaycodex`（客户端画像）
- 关联计划：[PLAN-20260908T162203434Z GLM 三协议适配方案](../plans/计划-20260908T162203434Z-GLM三协议适配方案.md)
- 关联 bug：[BUG-0178](问题-0178-Responses到Chat桥执行门控缺失端到端断裂.md)（桥执行门控已修复；本条是账户选择层的残留限制）

## 问题概述

`chain_driver.go` `requiredSupportedEndpointMode` 的第一个分支对 `requestClientCompatibility == "codex_responses"` 且 POST `/responses` 的请求**无条件要求账户持有 `responses_sse`**，且该分支先于跨协议映射分支返回（1140-1143 行 vs 1149 行起）。实参来源已查明为**请求级 Codex 客户端画像**（`internal/gatewaycodex/strategy.go` `ResolveOpenAIGatewayClientStrategy`：`x-codex-turn-metadata` 或显式 profile header 判定），非账户级派生。

结果：真实 Codex CLI 客户端发出的请求在账户选择阶段即被 `endpoint_mode_unsupported` 淘汰全部 chat-only 档案桥账户（GLM 两个 chat 档案、DeepSeek、hybrid chat——它们的 `SupportedEndpointModes` 只有 `chat_json/chat_sse`），无法到达 BUG-0178 修复后的桥转换路径。普通 OpenAI 兼容 Responses 客户端（`openai_standard` 画像）不受影响（走 mapping 分支要求 `chat_sse`，chat 档案满足）。

## 事实与取证结论（2026-10-06 定案）

- 事实：限制分支在修复前 HEAD（`e3be55c25`）已存在，预存在、非 BUG-0178 修复引入。
- 取证结论：Go 偏离 Node，映射分支应优先。Node 归档各 driver（glm / openai-compatible / hybrid / gpt）的账户能力闸中，`responses -> chat_completions` 显式映射分支一律先于通用分支：命中映射的 chat-only 账户对 `codex_responses` 客户端请求只要求 `chat_sse`（`codexResponsesChatBridgeRequiredEndpointMode()` 无条件返回 chat_sse）；codex 强制 `responses_sse` 只作用于**无映射**账户的通用分支。Node gpt driver 的映射分支另带 `account.type !== 'oauth'` 守卫——OAuth 账户不走映射分支，仍落入 codex 强制分支（保持原生 Responses 语义，避免"OAuth + 映射"新偏差）。
- 关键证据：Node 回归用例 `codex-cross-protocol-context-regression.ts:84-98` 直接证明"chat 桥账户承接 Codex CLI 请求"是既定设计。

## 复现步骤

1. Codex CLI（携带 `x-codex-turn-metadata`，画像判为 `codex_responses`）指向网关，路由到仅含 GLM chat 档案桥账户（显式 `responses -> chat_completions` 映射）的分组。
2. 观察 POST `/v1/responses` 在账户选择阶段被 `endpoint_mode_unsupported` 淘汰，桥转换从未执行。

## 修复记录（2026-10-06）

- `backend-go/projects/gateway/cmd/juhe-ai-gateway/chain_driver.go` `requiredSupportedEndpointMode`：跨族映射分支整体前移到 codex_responses 首分支之前（映射分支逻辑原样搬移），codex 强制 `responses_sse` 分支后移为仅无映射账户可达；映射分支新增 `account.Type != "oauth"` 守卫（移植 Node gpt driver 的 `account.type !== 'oauth'`）。函数签名未变（`account` 入参本就携带 `Type` 字段），调用点 `endpointModeMismatchReason` 与 `chain_switchtarget.go` `switchTargetForRequest` 行为随之对齐，无需改动。执行层构造（`shouldForceOpenAICodexResponsesSse` / `buildOpenAIClientCompatibilityBody`）已正确镜像 Node，未动。
- 新增回归：`chain_driver_endpointmodes_test.go` `TestEndpointModeCodexResponsesMappingBranchPriority` 四个子用例——codex_responses + 映射 chat-only 账户放行（主回归，闸 `chat_sse`）、codex_responses + 无映射 chat-only 账户仍淘汰（a3/a4 语义保留）、codex_responses + OAuth + 映射守卫生效（裁决 `responses_sse`）、openai_standard + 映射账户放行（既有语义不受顺序调整影响）。

## 验证记录（2026-10-06）

- `go build ./...` 与 `go vet ./cmd/juhe-ai-gateway/...` 通过。
- `go test ./cmd/juhe-ai-gateway/ -run 'EndpointMode|ClientStrategy|RequiredSupported' -count=1 -v` 全部 PASS（含 `TestEndpointModeCodexResponsesMappingBranchPriority` 四子用例与既有 `TestEndpointModeMismatchReasonConsumesSupportedEndpointModes`、`TestW1GRequiredSupportedEndpointModeArms`、`TestW1TDriverEndpointModeBridgeArms`）。
- 相关族：`-run 'SwitchTarget'`（切号冻结精确 mode 复用同函数）、`./internal/gatewaydispatch/ -run 'SwitchTarget|EndpointMode|Candidate|Bridge'`、`./internal/gatewaycodex/`、`./internal/gatewayopenai/` 全部 PASS。
- cmd 全包测试存在既有隔离问题（`TestMergeRouteResolverPerSegmentQuotaGate` 等并发跑超时、单跑通过），按分族方式验证。
