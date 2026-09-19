# BUG-0179 Codex Responses 画像客户端在端点模式闸无法选中 Chat 桥账户

## 基本信息

- 编号：BUG-0179
- 状态：待取证（限制本身已确认存在；是否偏离 Node 语义未定）
- 严重程度：P2
- 发现时间：2026-09-19（BUG-0178 修复独立复审中发现）
- 发现方式：`requiredSupportedEndpointMode` 专项核查
- 模块：Go 后端 / `cmd/juhe-ai-gateway`（chain_driver.go 端点模式闸）/ `internal/gatewaycodex`（客户端画像）
- 关联计划：[PLAN-20260908T162203434Z GLM 三协议适配方案](../plans/计划-20260908T162203434Z-GLM三协议适配方案.md)
- 关联 bug：[BUG-0178](问题-0178-Responses到Chat桥执行门控缺失端到端断裂.md)（桥执行门控已修复；本条是账户选择层的残留限制）

## 问题概述

`chain_driver.go` `requiredSupportedEndpointMode` 的第一个分支对 `requestClientCompatibility == "codex_responses"` 且 POST `/responses` 的请求**无条件要求账户持有 `responses_sse`**，且该分支先于跨协议映射分支返回（1140-1143 行 vs 1149 行起）。实参来源已查明为**请求级 Codex 客户端画像**（`internal/gatewaycodex/strategy.go` `ResolveOpenAIGatewayClientStrategy`：`x-codex-turn-metadata` 或显式 profile header 判定），非账户级派生。

结果：真实 Codex CLI 客户端发出的请求在账户选择阶段即被 `endpoint_mode_unsupported` 淘汰全部 chat-only 档案桥账户（GLM 两个 chat 档案、DeepSeek、hybrid chat——它们的 `SupportedEndpointModes` 只有 `chat_json/chat_sse`），无法到达 BUG-0178 修复后的桥转换路径。普通 OpenAI 兼容 Responses 客户端（`openai_standard` 画像）不受影响（走 mapping 分支要求 `chat_sse`，chat 档案满足）。

## 事实与未决

- 事实：限制分支在修复前 HEAD（`e3be55c25`）已存在，预存在、非 BUG-0178 修复引入。
- 未决：Node 归档存在歧义——`domain/openai-endpoint-modes.ts:180-182` 显示 Node 对 `codex_responses` 配置列同样强制 `responses_sse` 能力，但同文件 `supportsCodexResponsesChatBridge`（185-193 行）白名单包含 GLM 两个 chat 档案；Node `codexResponsesChatBridgeRequiredEndpointMode` 与 api-key-client-compatibility 的判定优先顺序未取证。**不能断言 Go 与 Node 相悖**，需先取证 Node required-mode 判定顺序再定性（缺陷 or 设计）。

## 复现步骤

1. Codex CLI（携带 `x-codex-turn-metadata`，画像判为 `codex_responses`）指向网关，路由到仅含 GLM chat 档案桥账户（显式 `responses -> chat_completions` 映射）的分组。
2. 观察 POST `/v1/responses` 在账户选择阶段被 `endpoint_mode_unsupported` 淘汰，桥转换从未执行。

## 处置方向（待取证后定）

- 若 Node 对桥账户在 codex_responses 画像下同样要求 `responses_sse`（即 chat 桥账户须显式开启 responses 模式才承接 Codex CLI）：则 Go 与 Node 等价，登记为设计边界并回写 GLM 方案文档（Codex CLI 承接依赖原生 Responses 档案）。
- 若 Node 对桥账户走 `codexResponsesChatBridgeRequiredEndpointMode`（要求 `chat_sse`）：则 Go 分支顺序偏离，需调整 `requiredSupportedEndpointMode` 判定顺序（mapping 分支前移或为映射账户跳过首分支），并补 Codex CLI → GLM chat 桥账户的端到端测试。

## 验证记录

待取证后补记。
