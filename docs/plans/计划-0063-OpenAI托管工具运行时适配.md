# PLAN-0063 OpenAI 托管工具运行时适配

## 基本信息

- 编号：PLAN-0063
- 状态：进行中
- 创建时间：2026-06-24
- 更新时间：2026-06-25
- 需求来源：用户对话
- 执行者：AI
- 关联模块：后端 / 网关 / Anthropic bridge / OpenAI hosted tools / MCP / code execution / computer use / 审计 / 文档 / 验证

## 需求目标

- 背景：OpenAI 到 Anthropic 高兼容桥接已经对 `web_search`、`file_search`、`image_generation`、thinking、文件输入和 compact 建立了可验证路径。剩余 `code_interpreter`、`computer`、`mcp` 和 Codex 本地工具目前只能返回 guidance。用户要求长期完整方案，不能把不可执行工具静默删除，也不能伪造执行成功。
- 目标：建立 hosted runtime 统一设计和分阶段计划；先保留当前 OpenAI 形态 guidance，再逐项实现具备权限、沙箱、allowlist、审计和 mock 回归的真实运行时。
- 交付物：运行时设计文档、计划记录、能力矩阵链接、后续执行拆解、mock 优先测试矩阵和真实联调门槛。

## 范围边界

### 本次包含

- [x] 新增运行时设计文档，明确 `code_interpreter` / `computer` / `mcp` / Codex 本地工具不能靠字段映射解决。
- [x] 记录当前已完成的 guidance mock 覆盖：Responses `computer` / `code_interpreter` / `mcp`、Chat `code_interpreter` 均不请求 Anthropic。
- [x] 定义后续真实运行时的权限、沙箱、allowlist、审计和 mock 优先验收门槛。
- [x] 设计 Runtime Registry 和配置开关；首批允许 `guidance` / `reject`，并为 mockai 回归开放受控 `mock`；MCP 额外允许 `local_runtime` 作为 real proxy 闸门，但未配置执行器时必须本地 503。
- [x] 实现 Responses `tool_search` + `namespace` function 本地展开，并补 JSON / SSE mock 回归。
- [x] 实现 Responses `code_interpreter` mock runtime 首批 JSON / SSE 回归。
- [x] 实现 MCP proxy 首批 allowlist + mock server。
- [x] 补齐 MCP real proxy 长期设计：远程 server 首批、connector adapter 后置、allowlist、auth 引用、approval 状态、transport、输出限制和审计。
- [ ] 实现 code interpreter 受限沙箱首批 mock runtime。
- [ ] 设计 computer adapter 的受控浏览器 / 屏幕状态协议。

### 本次不包含

- 不在主 Web 进程内执行任意代码。
- 不默认接入任意远程 MCP server。
- 不把宿主桌面暴露给模型。
- 不把 Anthropic 原生 beta 能力写死为协议规则；必须由账号 profile 显式声明。

## 关联文档

- 运行时设计：`docs/functions/OpenAI托管工具运行时设计.md`
- 高兼容矩阵：`docs/functions/OpenAI到Anthropic高兼容能力矩阵.md`
- OpenAI 到 Anthropic 桥接：`docs/functions/OpenAI到Anthropic协议桥接设计.md`
- 安全与日志：`docs/functions/安全与日志策略.md`
- 审计保全：`docs/functions/审计日志保全策略设计.md`
- 验证手册：`docs/develop/测试与验证说明.md`

## 方案概述

- 方案原则：先统一 registry 和边界，再逐个工具落地；默认 L4 guidance，只有执行器、权限、审计和 mock 回归齐全后才升级为 L2 / L3。
- 数据变化：首批计划阶段不新增 schema；后续如需要 allowlist、runtime policy 或 tool execution records，再按当前 schema 直接新增表。
- 接口变化：不新增下游必填字段；继续承接 OpenAI Chat / Responses 的 hosted tool 声明。
- 前端变化：首批无 UI；后续如需要 runtime policy 管理页再单独扩展。
- 后端变化：后续新增 Runtime Registry、MCP proxy、code sandbox worker、computer adapter 接口和审计 metadata。
- 数据处理策略：工具大输出、截图、文件产物和二进制 payload 默认不写普通 payload body，只保留摘要、引用和 omission metadata。

## 执行拆解

- [x] 创建 PLAN-0063。
- [x] 新增 `docs/functions/OpenAI托管工具运行时设计.md`。
- [x] 更新文档索引和高兼容矩阵。
- [x] Runtime Registry 设计落地到后端类型和配置。
- [x] Responses `tool_search` + `namespace` 本地展开：展开请求内 function、恢复 Responses `namespace`、不伪造 hosted search item。
- [x] Code interpreter mock runtime：`mock` 模式下不请求 Anthropic、不执行代码，返回 OpenAI Responses `code_interpreter_call` JSON / SSE。
- [x] MCP proxy mock：固定 allowlist、approval request、allowed_tools 过滤、tool result 映射和敏感字段不回显。
- [x] MCP proxy real 设计：远程 MCP server 首批，OpenAI `connector_id` 独立 adapter 后置；明确 server allowlist、auth 引用、tool cache、approval request、execution record、transport 和拒绝边界。
- [x] MCP proxy real 入口定义校验：重复 `server_label`、`server_url` / `connector_id` 冲突、缺少 server 标识、非 HTTPS `server_url`、`allowed_tools` / `require_approval` 形态错误、`authorization` 与 `headers.Authorization` 冲突均本地拒绝且不上游。
- [x] MCP proxy real allowlist / executor 第一段：server allowlist 读取、真实 executor 接口落地、重复 label 和 server 标识冲突继续本地拒绝。
- [x] MCP proxy real transport 第一段：Streamable HTTP JSON-RPC `initialize` / `notifications/initialized` / `tools/list` / `tools/call`、超时、输出上限和错误映射。
- [x] MCP proxy JSON 免批模型驱动工具循环：`require_approval=never` 或请求 `allowed_tools` 全部命中 `require_approval.never.tool_names` 时，`tools/list` 导入 Anthropic client tools，Anthropic `tool_use` 转 Responses `mcp_call` 并执行 MCP `tools/call`，再把 MCP 输出作为 Anthropic `tool_result` 回灌给同一上游生成最终 assistant answer；默认 approval 返回本地 `mcp_approval_request` 且不上游、不调用 `tools/call`。
- [ ] MCP proxy JSON 多轮工具循环：第二轮 Anthropic 如果继续要求工具调用，按上限继续执行或受控返回需要后续调用的工具轨迹。
- [ ] MCP proxy real transport 完整化：HTTP-SSE、重试、重定向审计和更完整的错误省略。
- [ ] MCP proxy real approval：实现 approval request 持久化、跨 API Key / 分组边界校验、approve / reject / expired 状态流转。
- [ ] Code interpreter：受限 worker、临时目录、超时、输出上限、stderr 和文件产物策略。
- [ ] Computer adapter：受控浏览器状态、动作协议、截图省略和拒绝策略。
- [ ] 对每个 runtime 补 JSON / SSE mock 回归，再做真实联调。

## 测试项

| 测试类型 | 测试项 | 验证方式 / 命令 | 预期结果 | 状态 | 实际结果或备注 |
| --- | --- | --- | --- | --- | --- |
| Mock 回归 | 当前 guidance | `pnpm --dir backend test:openai-anthropic-bridge-mock` | 无执行器时返回 OpenAI 形态 guidance，且不请求 Anthropic | 已通过 | 已覆盖 Responses `computer` / `code_interpreter` / `mcp` 和 Chat `code_interpreter` |
| Mock 回归 | Runtime Registry reject | `pnpm --dir backend test:openai-anthropic-bridge-mock` | 显式配置 `code_interpreter=reject` 时返回本地 400，且不请求 Anthropic | 已通过 | 已覆盖临时切换 `runtimeConfig.hostedToolRuntimes.codeInterpreter='reject'` 后 Responses 本地拒绝 |
| 设计检查 | 运行时边界 | 文档审查 | 不在主 Web 进程执行代码；MCP 需要 allowlist；computer 不能文本伪造 | 已完成 | 已写入运行时设计 |
| Mock 回归 | Responses tool_search namespace 本地展开 | `pnpm --dir backend test:openai-anthropic-bridge-mock` | JSON / SSE 都能把 namespace function 展开给 Anthropic，并把 tool_use 恢复为 Responses `function_call.namespace` | 已通过 | 2026-06-25 已覆盖 JSON 强制 namespace function、SSE added / done namespace 恢复、Anthropic 展开工具名 `namespace__function` |
| Mock 回归 | Responses code_interpreter mock runtime | `pnpm --dir backend test:openai-anthropic-bridge-mock` | `code_interpreter=mock` 时 JSON / SSE 返回 `code_interpreter_call`，不上游，不执行真实代码 | 已通过 | 2026-06-25 已覆盖 JSON、SSE、`include=code_interpreter_call.outputs`、固定 logs marker、用户 prompt 不进入工具输出、零 Anthropic 上游命中 |
| Mock 回归 | MCP proxy mock | `pnpm --dir backend test:openai-anthropic-bridge-mock` | `mcp=mock` 时固定 allowlist server 返回 `mcp_list_tools` / `mcp_call`，approval required 返回 `mcp_approval_request`，未授权 server 本地拒绝，authorization 不回显 | 已通过 | 2026-06-25 已覆盖 JSON、SSE、`allowed_tools` 过滤、approval request、未授权 server 本地拒绝、authorization 不回显、用户 prompt 不进入工具输出、零 Anthropic 上游命中 |
| 设计检查 | MCP real proxy | 文档审查 | 真实 proxy 不混淆 remote MCP 和 OpenAI connector，明确 allowlist、auth、approval、transport、输出限制和审计边界 | 已完成 | 2026-06-25 已写入运行时设计；首批只承接 `server_url` 远程 MCP，`connector_id` 后置 |
| Mock 回归 | MCP definition validation | `pnpm --dir backend test:openai-anthropic-bridge-mock` | MCP 工具定义错误本地 400，不进入 guidance、不请求 Anthropic | 已通过 | 2026-06-25 已覆盖重复 `server_label`、`server_url` / `connector_id` 冲突、缺少 server 标识、`authorization` 与 `headers.Authorization` 冲突 |
| Mock 回归 | MCP local_runtime gate | `pnpm --dir backend test:openai-anthropic-bridge-mock` | `mcp=local_runtime` 但未配置 executor 时本地 503；`connector_id` 本地 400；均不请求 Anthropic | 已通过 | 2026-06-25 已覆盖未配置 executor 本地 503、`connector_id` 本地 400、authorization / prompt 不泄漏、零 Anthropic 上游命中 |
| Mock 回归 | MCP real proxy mock server | `pnpm --dir backend test:openai-anthropic-bridge-mock` | 本机 mock MCP server 覆盖 tools/list、tools/call、auth、approval、超时、输出超限和 JSON-RPC transport | 已通过 | 2026-06-25 已覆盖 allowlist 读取、authorization 仅入 allowlist server、JSON 免批路径 Anthropic 选择工具 + MCP `tools/call` + Anthropic `tool_result` 回灌最终回答、默认 approval 本地 `mcp_approval_request` 且不上游不调用 `tools/call`、SSE 直返路径、输出截断 |
| Mock 回归 | Code interpreter | 待实现 | 成功、stderr、超时、输出超限、网络禁止均可诊断 | 未开始 | 待沙箱 worker |
| Mock 回归 | Computer adapter | 待实现 | 动作成功、动作拒绝、截图省略、超时均可诊断 | 未开始 | 待 adapter 设计 |
| 安全检查 | 凭据与敏感输出 | 固定 key 前缀和工具输出扫描 | 不落真实 key、截图或大输出正文 | 待执行 | 每次实现后执行 |

## 进度记录

| 日期 | 状态 | 记录人 | 进展 / 决策 / 阻塞 |
| --- | --- | --- | --- |
| 2026-06-24 | 进行中 | AI | 已确认剩余 hosted runtime 不能靠协议字段映射解决；创建 PLAN-0063，并新增运行时设计文档。 |
| 2026-06-24 | 进行中 | AI | 当前 guidance mock 已覆盖 Responses `computer` / `code_interpreter` / `mcp` 和 Chat `code_interpreter`，真实运行时仍需 Runtime Registry、权限、沙箱、allowlist 和审计后才能启用。 |
| 2026-06-24 | 进行中 | AI | 已落地 Runtime Registry 首批骨架和环境配置：`code_interpreter`、`computer`、`mcp`、`shell`、`skills`、`tool_search` 默认 `guidance`，可显式切到 `reject`；mock 覆盖 reject 不上游。 |
| 2026-06-25 | 进行中 | AI | 依据 OpenAI Responses tool_search 官方形态，确定本轮只做请求内 `namespace` / function 本地展开：不伪造 `tool_search_call` / `tool_search_output`，回包恢复 `function_call.namespace`。 |
| 2026-06-25 | 进行中 | AI | 已实现 Responses `tool_search` + `namespace` 本地展开：请求内 namespace function 展开为 Anthropic client tool，`tool_choice` 映射为展开名，Responses JSON / SSE 回包恢复 `namespace`。 |
| 2026-06-25 | 进行中 | AI | 用户允许用 mockai；本轮推进 Responses `code_interpreter` mock runtime，用于固定协议外形和不请求上游边界，不执行 Python / shell。 |
| 2026-06-25 | 进行中 | AI | 已实现 Responses `code_interpreter` mock runtime：仅 `codeInterpreter=mock` 时启用，JSON / SSE 本地返回 `code_interpreter_call` 和 assistant message，`include=code_interpreter_call.outputs` 返回固定 logs，不请求 Anthropic、不执行代码、不泄漏用户 prompt。 |
| 2026-06-25 | 进行中 | AI | 依据 OpenAI MCP 官方形态，MCP 输出需要覆盖 `mcp_list_tools`、`mcp_call`、`mcp_approval_request`；本轮先做固定 allowlist 的 mock proxy，不连接真实远程 MCP server。 |
| 2026-06-25 | 进行中 | AI | 已实现 Responses MCP mock proxy：仅 `mcp=mock` 且命中 `mock-mcp` allowlist 时启用，JSON / SSE 返回 `mcp_list_tools`、approval request 或 `mcp_call`，不请求 Anthropic、不连接远程 MCP、不回显 authorization。 |
| 2026-06-25 | 进行中 | AI | 已补真实 MCP proxy 长期设计：首批只承接 `server_url` 远程 MCP，`connector_id` 必须等独立 connector adapter；真实执行需 server allowlist、auth 引用、tool cache、approval request、execution record、transport 限制、输出省略和审计。 |
| 2026-06-25 | 进行中 | AI | 已补 MCP 入口定义校验骨架和 mock 回归：重复 `server_label`、`server_url` / `connector_id` 冲突、缺少 server 标识、凭据 header 冲突等错误本地 400，避免被 guidance 或 mock runtime 吞掉。 |
| 2026-06-25 | 进行中 | AI | 已实现 MCP `local_runtime` 首段 gate：只用于 Responses MCP real proxy；`connector_id` 本地拒绝，未配置 executor 本地 503，避免误打 Anthropic 或远程 MCP。 |
| 2026-06-25 | 进行中 | AI | 已实现 MCP real proxy allowlist / executor 第一段：本地 allowlist server、JSON-RPC `initialize` / `tools/list` / `tools/call`、authorization 白名单透传、`allowed_tools` 过滤、输出截断。 |
| 2026-06-25 | 进行中 | AI | 已实现 MCP JSON 免批模型驱动工具循环：非流式 Responses 在免批策略下先把 MCP 工具导入 Anthropic client tools，Anthropic `tool_use` 触发网关执行 `tools/call`，再把 `tool_result` 回灌给同一 Anthropic 上游生成最终回答；默认 approval 仍本地返回 `mcp_approval_request`。 |

## 决策记录

| 日期 | 决策 | 原因 | 影响 |
| --- | --- | --- | --- |
| 2026-06-24 | 默认保持 L4 guidance | 任意代码、远程 MCP 和 computer use 都有安全边界，不能静默伪造执行 | 未实现 runtime 时不请求 Anthropic、不执行本地工具 |
| 2026-06-24 | code execution 不进主 Web 进程 | 防止请求路径执行任意代码拖垮或污染网关进程 | 后续必须使用 worker / 子进程 / 容器类隔离 |
| 2026-06-24 | MCP 必须 allowlist + approval | remote MCP 可访问外部系统和敏感数据 | 未配置 allowlist 或 approval 时 guidance / 拒绝 |
| 2026-06-24 | computer 首批只考虑受控 adapter | 宿主桌面暴露风险过高，纯文本模拟没有语义价值 | 首批优先受控浏览器或上游原生 computer use |
| 2026-06-24 | Registry 首批真实执行模式只开放 `guidance` / `reject` | 真实 local/native 执行器尚未实现，开放 local/native 配置会让部署误以为工具可执行 | 后续每个 runtime 实现后再扩展对应真实执行模式；`mock` 仅作为回归模拟模式单独处理 |
| 2026-06-25 | Responses `tool_search` 先做请求内 namespace function 本地展开 | OpenAI hosted search 的 `tool_search_call` / `tool_search_output` 是 OpenAI 服务端加载生命周期，Anthropic Messages 无等价事件；但请求内 function 工具可安全转为 Anthropic client tool | 客户端可收到标准 Responses `function_call.name` / `namespace`；需要完整 hosted search 事件时仍必须直连原生 Responses 或后续本地检索 runtime |
| 2026-06-25 | code_interpreter 首批采用 `mock` 而非 `local_runtime` | 官方 code interpreter 需要 container 沙箱；当前尚未实现 worker / 容器隔离，不能宣称真实执行能力 | 默认仍 guidance；只有显式 mock 时返回固定 OpenAI Responses `code_interpreter_call`，用于 mockai 回归 |
| 2026-06-25 | MCP 首批采用固定 allowlist mock proxy | OpenAI MCP 会把上下文和授权数据发给第三方 server；没有 allowlist、approval 和审计前不能开放任意远程 MCP | 默认仍 guidance；只有显式 mock 且命中 `mock-mcp` allowlist 时返回固定 OpenAI Responses MCP item，用于 mockai 回归 |
| 2026-06-25 | MCP real proxy 首批只做 remote `server_url` | OpenAI `connector_id` 是 OpenAI 维护连接器，不等价于任意远程 MCP server；网关没有 connector OAuth 和工具目录适配时不能伪装支持 | `connector_id` 请求继续 guidance / 本地错误；后续单独实现 connector adapter 后再升级 |
| 2026-06-25 | MCP `local_runtime` 先作为 gate 而非完整 executor | 真实远程 MCP 还缺 allowlist 存储、transport、approval 持久化和输出省略；但需要先让配置层可区分“准备接真实 runtime”和普通 guidance | 未配置 executor 时返回本地 503；这证明链路已进入 real proxy 分支，但不会误宣称已完成真实执行 |
| 2026-06-25 | MCP JSON 先落免批单轮模型驱动，不绕过 approval | 未持久化 approval 前不能让模型绕过审批触发工具调用；递归多轮工具循环需要调用次数上限和失败策略 | 本阶段只对免批工具执行单轮 Anthropic tool-result loop；默认 approval 本地返回 `mcp_approval_request`；第二轮继续要求工具调用时后续再补多轮策略 |

## 验收标准

- [x] 运行时设计文档已创建并纳入索引。
- [x] 当前无执行器 guidance mock 覆盖已记录。
- [x] Runtime Registry 和配置开关完成。
- [x] Responses `tool_search` / `namespace` 本地展开具备 JSON / SSE mock 回归。
- [x] MCP / code interpreter / computer 至少一个 runtime 具备 JSON / SSE mock 回归。
- [ ] 每个启用 runtime 都有权限、审计、超时、输出大小和凭据扫描验证。

## 验证记录

- 类型检查：2026-06-25 已复跑并通过 `pnpm --dir backend typecheck`；2026-06-25 已复跑并通过 `pnpm --dir frontend typecheck`。
- Mock 回归：2026-06-25 已复跑并通过 `pnpm --dir backend test:openai-anthropic-bridge-mock`，当前无执行器 guidance 覆盖 Responses `computer` / `code_interpreter` / `mcp` 和 Chat `code_interpreter`；Runtime Registry reject 覆盖 `code_interpreter=reject` 本地 400 且不请求 Anthropic；Responses `tool_search` + `namespace` 本地展开覆盖 JSON / SSE；Responses `code_interpreter` mock runtime 覆盖 JSON / SSE、`include=code_interpreter_call.outputs`、固定 logs marker、用户 prompt 不进入工具输出且不上游；Responses MCP mock proxy 覆盖 JSON / SSE、`allowed_tools` 过滤、approval request、未授权 server 本地拒绝、authorization 不回显、用户 prompt 不进入工具输出且不上游；MCP definition validation 覆盖重复 `server_label`、`server_url` / `connector_id` 冲突、缺少 server 标识、凭据 header 冲突本地 400 且不上游；MCP `local_runtime` gate 覆盖未配置 executor 本地 503、`connector_id` 本地 400、authorization / prompt 不泄漏且不上游；MCP real proxy mock server 覆盖 allowlist 读取、authorization 仅入 allowlist server、JSON 免批路径 Anthropic 选择工具 + MCP `tools/call` + Anthropic `tool_result` 回灌最终回答、默认 approval 本地 `mcp_approval_request` 且不上游不调用 `tools/call`、output 截断和 SSE 直返路径。
- 真实联调：2026-06-25 已复跑并通过 `pnpm --dir backend test:openai-anthropic-bridge-real`；真实上游 `https://vsllm.com`、模型 `claude-sonnet-4-6`、源模型 `gpt-5.5`，`responses_tool_search_namespace:passed`。
- 凭据检查：2026-06-25 已扫描 `backend`、`frontend`、`docs`、`package.json`、`pnpm-lock.yaml` 中固定真实 key 前缀，无命中；`git diff --check` 无 whitespace error，仅有 LF/CRLF 提示。
- 未验证项：MCP real proxy 的多轮模型工具调用循环、approval 持久化、HTTP-SSE 远程 transport、重试、重定向审计和 server 存储尚未实现；code interpreter 真实沙箱、computer adapter 真实运行时尚未实现；不得在生产配置中宣称可执行。Responses `mcp` 当前已具备本地 allowlist executor 第一段和 JSON 免批单轮 tool-result 回灌；Responses `code_interpreter` 当前只有 mock runtime，不运行 Python / shell，不产生真实文件产物；Responses `tool_search` 仅验证请求内 namespace function 本地展开，不代表完整 OpenAI hosted `tool_search_call` / `tool_search_output` 生命周期。

## 风险与注意事项

- code execution 和 shell 是最高风险项，必须先有隔离和资源限制。
- MCP server 可能访问第三方账号和私有数据，allowlist、auth 和 approval 不能省略。
- computer adapter 涉及截图和操作轨迹，审计正文省略和权限提示必须先行。
- 发布异常处理：如 runtime 出现异常，关闭对应 registry 项后恢复 L4 guidance，基础文本、Files、File Search、image_generation、thinking 和 compact 不应受影响。

## 完成总结

- 完成时间：待补充
- 实际完成内容：待补充
- 主要改动位置：待补充
- 验证结果：待补充
- 后续建议：待补充
