# PLAN-0063 OpenAI 托管工具运行时适配

## 基本信息

- 编号：PLAN-0063
- 状态：进行中
- 创建时间：2026-06-24
- 更新时间：2026-06-26
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
- [x] 设计 Runtime Registry 和配置开关；首批允许 `guidance` / `reject`，并为 mockai 回归开放受控 `mock`；MCP 和 code interpreter 允许 `local_runtime` 作为真实执行器入口闸门，但未配置对应 executor 时必须本地 503。
- [x] 实现 Responses `tool_search` + `namespace` function 本地展开，并补 JSON / SSE mock 回归。
- [x] 实现 Responses `code_interpreter` mock runtime 首批 JSON / SSE 回归。
- [x] 实现 MCP proxy 首批 allowlist + mock server。
- [x] 补齐 MCP real proxy 长期设计：远程 server 首批、connector adapter 后置、allowlist、auth 引用、approval 状态、transport、输出限制和审计。
- [x] 实现 code interpreter local_runtime 安全闸门：未配置 executor 时本地 503，且不请求 Anthropic、不在主 Web 进程执行代码。
- [x] 设计 computer adapter 的受控浏览器 / 屏幕状态协议。

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
- [x] Code interpreter local_runtime gate：`local_runtime` 模式下识别 Responses `code_interpreter`，允许 `include=code_interpreter_call.outputs` 交给运行时处理；未配置 executor 时本地 503，不请求 Anthropic、不执行代码。
- [x] Code interpreter worker 沙箱首段：Anthropic client tool、Python 子进程、临时目录、超时、输出截断、stdout / stderr logs 和 Responses `code_interpreter_call` 回灌。
- [x] Code interpreter 文件产物元数据首段：worker 退出后扫描临时目录，返回 `metadata.artifacts` 和 `[artifacts]` logs 摘要，不暴露临时路径。
- [x] Code interpreter 文件产物 Files 持久化首段：符合上限的 worker 产物写入本地 OpenAI 兼容 Files 存储，返回 `file_id` 和 `/v1/files/{file_id}/content` 下载路径。
- [x] Code interpreter container files 兼容壳首段：把本地 worker 产物绑定 `container_id`，支持 `/v1/containers/{container_id}/files`、`/v1/containers/{container_id}/files/{file_id}` 和 `/v1/containers/{container_id}/files/{file_id}/content`；不宣称完整 create/upload/delete container。
- [x] MCP proxy mock：固定 allowlist、approval request、allowed_tools 过滤、tool result 映射和敏感字段不回显。
- [x] MCP proxy real 设计：远程 MCP server 首批，OpenAI `connector_id` 独立 adapter 后置；明确 server allowlist、auth 引用、tool cache、approval request、execution record、transport 和拒绝边界。
- [x] MCP proxy real 入口定义校验：重复 `server_label`、`server_url` / `connector_id` 冲突、缺少 server 标识、非 HTTPS `server_url`、`allowed_tools` / `require_approval` 形态错误、`authorization` 与 `headers.Authorization` 冲突均本地拒绝且不上游。
- [x] MCP proxy real allowlist / executor 第一段：server allowlist 读取、真实 executor 接口落地、重复 label 和 server 标识冲突继续本地拒绝。
- [x] MCP proxy real transport 第一段：Streamable HTTP JSON-RPC `initialize` / `notifications/initialized` / `tools/list` / `tools/call`、超时、输出上限和错误映射。
- [x] MCP proxy JSON 免批模型驱动工具循环：`require_approval=never` 或请求 `allowed_tools` 全部命中 `require_approval.never.tool_names` 时，`tools/list` 导入 Anthropic client tools，Anthropic `tool_use` 转 Responses `mcp_call` 并执行 MCP `tools/call`，再把 MCP 输出作为 Anthropic `tool_result` 回灌给同一上游生成最终 assistant answer；默认 approval 返回本地 `mcp_approval_request` 且不上游、不调用 `tools/call`。
- [x] MCP proxy Responses SSE 免批缓冲式工具循环：首轮 Anthropic SSE 聚合出 `tool_use` 后执行 MCP `tools/call`，再用 Anthropic `tool_result` 二次回灌生成最终回答，并输出 OpenAI Responses typed SSE terminal snapshot。
- [x] MCP proxy JSON 多轮工具循环：第二轮 Anthropic 如果继续要求工具调用，按上限继续执行或受控收口。
- [x] MCP proxy real transport HTTP-SSE mock 覆盖：SSE 形式 JSON-RPC result frame 已可解析，覆盖 `initialize` / `tools/list` / `tools/call`。
- [x] MCP proxy legacy HTTP+SSE mock 覆盖：Streamable HTTP `initialize` POST 4xx 后回退 GET SSE，读取 `endpoint` 事件，再通过同源 endpoint POST JSON-RPC，并从 SSE `message` 帧读取匹配 id 的 response。
- [x] MCP proxy approval id 绑定校验：`mcp_approval_request.id` 绑定 server / tool / arguments；错误 `mcp_approval_response.approval_request_id` 本地 400，且不上游、不执行 `tools/call`。
- [x] MCP proxy real transport 诊断增强：有限重试、重定向拒绝稳定错误码和错误正文省略进入 mock 覆盖。
- [x] MCP proxy real approval 首段：approval request 业务库状态机、当前 API Key / 分组 scope 绑定、approve / reject / expired / replay 边界。
- [x] MCP proxy real execution record 首段：真实 `tools/call` 成功 / 失败 / 截断写入业务库记录，绑定 scope 和摘要，不保存远程输出正文。
- [x] MCP proxy real execution record 查询 API 首段：管理侧和用户侧分页查询 execution record 摘要，支持 scope、trace、approval、server、tool、status 和时间窗口筛选，不提供远程输出正文读取。
- [x] MCP proxy real approval 管理 API 首段：管理侧和用户侧分页查询 approval request 摘要，并允许把当前 scope 内的 `pending` request 标记为 `approved` / `rejected`；该 API 不执行远程 MCP `tools/call`。
- [x] MCP proxy server allowlist 存储化首段：新增业务库 server allowlist，按系统账户 scope 管理 label / URL / enabled / allowed tools / approval policy / limits，环境变量仅保留 bootstrap / 应急来源。
- [x] MCP proxy real approval 完整化 UI 首段：新增管理侧 / 用户侧运营入口，覆盖 server allowlist、approval request 和 execution record 摘要；不访问远程 `tools/list`，不展示远程输出正文。
- [ ] MCP proxy real transport 完整化：legacy HTTP+SSE 双端点长连接状态机、mockai 覆盖和真实第三方 HTTP-SSE 联调。
- [x] MCP proxy real approval 完整化首段：server 可用性诊断和工具 schema 缓存，长期审计视图增强后续继续。
- [~] Code interpreter：受限 worker、临时目录、超时、输出上限、stderr、文件产物元数据、本地 Files 下载和 container files 兼容壳首段已落地；容器 / VM 级隔离和完整 create/upload/delete container 生命周期继续后续推进。
- [x] Computer adapter 协议设计：受控浏览器状态、动作协议、截图省略、拒绝策略和审批边界。
- [x] Computer adapter mock runtime：固定 `computer_call` / `computer_call_output` 外形、截图正文省略和 SSE 生命周期回归，不启动真实浏览器。
- [x] Computer adapter local_runtime 首段：先落 adapter 接口、未配置 503 闸门、测试 adapter `computer_call` JSON / SSE 输出、会话 / 动作 metadata 和截图正文省略。
- [x] Computer adapter HTTP sandbox bridge 首段：显式配置 HTTP adapter endpoint，网关调用外部 sandbox/container browser 服务，限制超时 / 响应体 / JSON schema，并继续裁剪截图、prompt、token、cookie、DOM 和 base64。
- [ ] Computer adapter 真实浏览器运行时：Playwright / container browser adapter、会话 TTL、域名 allowlist、动作 allowlist、真实截图引用、人工确认和审计摘要。
- [ ] 对每个 runtime 补 JSON / SSE mock 回归，再做真实联调。

## 测试项

| 测试类型 | 测试项 | 验证方式 / 命令 | 预期结果 | 状态 | 实际结果或备注 |
| --- | --- | --- | --- | --- | --- |
| Mock 回归 | 当前 guidance | `pnpm --dir backend test:openai-anthropic-bridge-mock` | 无执行器时返回 OpenAI 形态 guidance，且不请求 Anthropic | 已通过 | 已覆盖 Responses `computer` / `code_interpreter` / `mcp` 和 Chat `code_interpreter` |
| Mock 回归 | Runtime Registry reject | `pnpm --dir backend test:openai-anthropic-bridge-mock` | 显式配置 `code_interpreter=reject` 时返回本地 400，且不请求 Anthropic | 已通过 | 已覆盖临时切换 `runtimeConfig.hostedToolRuntimes.codeInterpreter='reject'` 后 Responses 本地拒绝 |
| Mock 回归 | Code interpreter local_runtime gate | `pnpm --dir backend test:openai-anthropic-bridge-mock` | `code_interpreter=local_runtime` 且未配置 executor 时返回本地 503，不被 include 校验误拦截，且不请求 Anthropic | 已通过 | 2026-06-26 已覆盖 503、`service_unavailable`、include 不误拦截和零 Anthropic 上游命中 |
| Mock 回归 | Code interpreter worker loop | `pnpm --dir backend test:openai-anthropic-bridge-mock` | `code_interpreter=local_runtime` 且 executor 已配置时，Anthropic 选择本地 Python tool，网关执行后返回 `code_interpreter_call`、stdout / stderr logs 和最终 assistant message | 已通过 | 2026-06-26 已覆盖成功、stderr、输出截断、超时、安全 env 不泄漏、Anthropic tool_result 回灌和最终 assistant message |
| Mock 回归 | Code interpreter 文件产物元数据 | `pnpm --dir backend test:openai-anthropic-bridge-mock` | Python worker 生成文件后，Responses `code_interpreter_call.outputs` 追加 `[artifacts]` logs 摘要，`metadata.artifacts` 返回文件名、大小、MIME 和内容省略原因，不包含文件正文和临时目录路径 | 已通过 | 2026-06-26 已覆盖 artifact logs、metadata.artifacts、文件正文不进入 Responses payload / Anthropic tool_result |
| Mock 回归 | Code interpreter 文件产物可下载 | `pnpm --dir backend test:openai-anthropic-bridge-mock` | Python worker 生成小文件后，Responses `metadata.artifacts[].file_id` 可通过本地 `/v1/files/{file_id}/content` 下载；大文件只返回元数据并标记 `file_too_large` | 已通过 | 2026-06-26 已覆盖 `file_id`、`download_path`、`[artifacts]` logs 带 file_id、`/v1/files/{file_id}/content` 下载原始文件内容，以及超限文件 `content_omitted=true` / `omit_reason=file_too_large` |
| Mock 回归 | Code interpreter container files 兼容壳 | `pnpm --dir backend test:openai-anthropic-bridge-mock` | Python worker 生成小文件后，Responses `container_id` 下可列出 container files、获取文件摘要，并通过 `/v1/containers/{container_id}/files/{file_id}/content` 下载原始内容；错误 container 不可读，跨 API Key 继续沿用 Files scope 隔离 | 已通过 | 2026-06-26 已覆盖 `container_id`、`container_download_path`、container 文件列表、文件摘要、content 下载、错误 container 404 和普通 `/v1/files/{file_id}/content` 回退下载 |
| Mock 回归 | Responses compact snapshot 完整性 | `pnpm --dir backend test:openai-anthropic-bridge-mock` | `/responses/compact` 生成 `response.compaction` 后，后续 Responses 可恢复 compact summary；跨 API Key 和 digest 篡改必须本地拒绝，且不请求 Anthropic | 已通过 | 2026-06-26 已覆盖 `juhecmp.v2` snapshot 恢复、跨 API Key 403 boundary mismatch、digest 篡改 404 snapshot not found 和零 Anthropic 上游命中 |
| 设计检查 | 运行时边界 | 文档审查 | 不在主 Web 进程执行代码；MCP 需要 allowlist；computer 不能文本伪造 | 已完成 | 已写入运行时设计 |
| Mock 回归 | Responses tool_search namespace 本地展开 | `pnpm --dir backend test:openai-anthropic-bridge-mock` | JSON / SSE 都能把 namespace function 展开给 Anthropic，并把 tool_use 恢复为 Responses `function_call.namespace` | 已通过 | 2026-06-25 已覆盖 JSON 强制 namespace function、SSE added / done namespace 恢复、Anthropic 展开工具名 `namespace__function` |
| Mock 回归 | Responses code_interpreter mock runtime | `pnpm --dir backend test:openai-anthropic-bridge-mock` | `code_interpreter=mock` 时 JSON / SSE 返回 `code_interpreter_call`，不上游，不执行真实代码 | 已通过 | 2026-06-25 已覆盖 JSON、SSE、`include=code_interpreter_call.outputs`、固定 logs marker、用户 prompt 不进入工具输出、零 Anthropic 上游命中 |
| Mock 回归 | MCP proxy mock | `pnpm --dir backend test:openai-anthropic-bridge-mock` | `mcp=mock` 时固定 allowlist server 返回 `mcp_list_tools` / `mcp_call`，approval required 返回 `mcp_approval_request`，未授权 server 本地拒绝，authorization 不回显 | 已通过 | 2026-06-25 已覆盖 JSON、SSE、`allowed_tools` 过滤、approval request、approval id 错误本地 400、approval id 正确后返回 `mcp_call`、未授权 server 本地拒绝、authorization 不回显、用户 prompt 不进入工具输出、零 Anthropic 上游命中 |
| 设计检查 | MCP real proxy | 文档审查 | 真实 proxy 不混淆 remote MCP 和 OpenAI connector，明确 allowlist、auth、approval、transport、输出限制和审计边界 | 已完成 | 2026-06-25 已写入运行时设计；首批只承接 `server_url` 远程 MCP，`connector_id` 后置 |
| Mock 回归 | MCP definition validation | `pnpm --dir backend test:openai-anthropic-bridge-mock` | MCP 工具定义错误本地 400，不进入 guidance、不请求 Anthropic | 已通过 | 2026-06-25 已覆盖重复 `server_label`、`server_url` / `connector_id` 冲突、缺少 server 标识、`authorization` 与 `headers.Authorization` 冲突 |
| Mock 回归 | MCP local_runtime gate | `pnpm --dir backend test:openai-anthropic-bridge-mock` | `mcp=local_runtime` 但未配置 executor 时本地 503；`connector_id` 本地 400；均不请求 Anthropic | 已通过 | 2026-06-25 已覆盖未配置 executor 本地 503、`connector_id` 本地 400、authorization / prompt 不泄漏、零 Anthropic 上游命中 |
| Mock 回归 | MCP real proxy mock server | `pnpm --dir backend test:openai-anthropic-bridge-mock` | 本机 mock MCP server 覆盖 tools/list、tools/call、auth、approval、超时、输出超限、JSON-RPC transport、HTTP-SSE transport、重试和重定向拒绝 | 已通过 | 2026-06-25 已覆盖 allowlist 读取、authorization 仅入 allowlist server、JSON 免批路径 Anthropic 选择工具 + MCP `tools/call` + Anthropic `tool_result` 回灌最终回答、Responses SSE 免批缓冲式工具循环、JSON 受限多轮工具循环、HTTP-SSE transport、默认 approval 本地 `mcp_approval_request` 且不上游不调用 `tools/call`、approval id 错误本地 400 且不执行 `tools/call`、approval id 正确后执行 `tools/call`、重试后成功、重定向本地拒绝且不泄漏敏感 query、输出截断 |
| Mock 回归 | MCP legacy HTTP+SSE transport | `pnpm --dir backend test:openai-anthropic-bridge-mock` | 本机 mock MCP server 覆盖 POST `initialize` 4xx 后 GET SSE、`endpoint` 事件、endpoint POST、SSE `message` 匹配 JSON-RPC id、notification 不等待响应、endpoint 同源限制和 `tools/call` 不重复重放 | 已通过 | 2026-06-25 已覆盖 `/mcp-legacy-sse` GET endpoint 发现、`/mcp-legacy-message` JSON-RPC POST、`initialize` / `notifications/initialized` / `tools/list` / `tools/call`、同一 prepared runtime 结束后关闭 SSE 会话 |
| Mock 回归 | MCP approval 持久化首段 | `pnpm --dir backend test:openai-anthropic-bridge-mock` | pending / approve / reject / expired / replay / 跨 API Key 边界均可诊断，未批准或非法审批不执行 `tools/call` | 已通过 | 2026-06-25 已覆盖业务库 pending 记录、scope 字段、跨 API Key 403、approve 后 consumed、replay 400、reject 普通响应、expired 400 |
| Mock 回归 | MCP execution record 首段 | 待实现 | 成功、失败、输出截断都写入业务库记录，记录 scope、digest、耗时、状态和错误码，不保存远程输出正文 | 已通过 | 2026-06-25 已覆盖 success / approval success / truncated / remote failure，execution record 仅保留摘要与错误码 |
| Mock 回归 | MCP execution record 查询 API | `pnpm --dir backend test:mcp-execution-records-route` | 管理侧可分页筛选，用户侧只能查看自身 scope，详情不返回远程输出正文 | 已通过 | 2026-06-25 已覆盖管理侧分页、组合筛选、详情 scope、用户侧 self scope、跨用户 404、非管理员 403 和远程输出正文不返回 |
| Mock 回归 | MCP approval 管理 API | `pnpm --dir backend test:mcp-approval-requests-route` | 管理侧和用户侧可分页查询审批摘要，用户侧只能查看自身 scope，approve / reject 只改状态不执行远程 MCP | 已通过 | 2026-06-25 已覆盖管理侧分页 / 组合筛选、用户侧 self scope、跨用户 404、approve / reject、过期和重复审批 409、非管理员 403、人工审批不写 execution record |
| Mock 回归 | MCP server allowlist 存储化 | `pnpm --dir backend test:mcp-servers-route` | 管理侧和用户侧按 scope 管理 server，label 唯一，URL 匹配，禁用项不参与 runtime，凭据不回显，列表页不访问远程 MCP | 已通过 | 2026-06-25 已覆盖用户侧 self scope、管理侧 systemAccountId 筛选、label 唯一、禁用 / 删除不进入 runtime、allowedTools 去重、per-server limit 进入 runtime、明文 authorization 不回显 |
| 前端验证 | MCP 运行时运营入口 | `pnpm --filter juhe-ai-frontend typecheck` / `pnpm --filter juhe-ai-frontend build` | 用户侧和管理侧路由可编译，server / approval / execution 三个标签页具备分页、筛选、详情和审批操作；管理侧可按系统账户筛选，用户侧不暴露管理筛选 | 已通过 | 2026-06-25 已通过前端 typecheck 和生产构建；新增 `/my-mcp-runtime` 与 `/mcp-runtime` 复用同一页面 |
| Mock 回归 | MCP server 诊断与工具 schema 缓存 | `pnpm --dir backend test:mcp-servers-route` | 手动诊断只访问当前 scope 内 enabled server，成功时缓存 `tools/list` schema，失败时记录错误摘要且不清空旧缓存；列表 / 详情不触发远程 MCP，不回显 authorization | 已通过 | 2026-06-25 已覆盖本机 mock MCP server、显式诊断、allowlist 过滤缓存、失败诊断摘要、跨用户 404、禁用 server 拒绝诊断 |
| Mock 回归 | Code interpreter worker 沙箱首段 | `pnpm --dir backend test:openai-anthropic-bridge-mock` | 成功、stderr、超时、输出超限、安全 env 不泄漏均可诊断 | 已通过 | 2026-06-26 已通过真实 Python 子进程 mock 回归；网络禁止为 runner best-effort，容器 / VM 级隔离和文件产物仍后续 |
| Mock 回归 | Computer adapter mock runtime | `pnpm --dir backend test:openai-anthropic-bridge-mock` | `computer=mock` 时 JSON / SSE 返回 `computer_call`，收到 `computer_call_output` 后正常收口，不启动浏览器、不请求 Anthropic、不回显截图正文 | 已通过 | 2026-06-25 已覆盖 JSON 固定 `computer_call.actions=[screenshot]`、SSE typed event、`computer_call_output` 收口、用户 prompt / 截图正文不回显、零 Anthropic 上游命中 |
| Mock 回归 | Computer adapter local_runtime 首段 | `pnpm --dir backend test:openai-anthropic-bridge-mock` | `computer=local_runtime` 未配置 adapter 时本地 503 且不上游；配置测试 adapter 后 JSON / SSE 返回 `computer_call`、会话 / 动作 metadata，不回显截图正文 | 已通过 | 2026-06-26 已覆盖未配置 adapter 本地 503、测试 adapter JSON / SSE `computer_call`、`computer_call_output` 收口、metadata 裁剪、截图 data URL / prompt / token / base64 不回显、零 Anthropic 上游命中 |
| Mock 回归 | Computer adapter HTTP sandbox bridge | `pnpm --dir backend test:openai-anthropic-bridge-mock` | `computer=local_runtime` 且显式配置 HTTP adapter endpoint 时，网关调用本机 mock adapter，返回 `computer_call` / message，限制响应大小和敏感字段回显，且不请求 Anthropic | 已通过 | 2026-06-26 已覆盖显式配置 endpoint、本机 HTTP adapter 调用、`computer_call` / message 归一化、metadata `adapter=http_browser`、动作 text 省略、截图 / prompt / token / base64 不回显、adapter 响应体超限本地 502 和零 Anthropic 上游命中 |
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
| 2026-06-26 | 进行中 | AI | 已把 Responses `code_interpreter` 接到 `local_runtime` 闸门：未配置 executor 时本地 503，且 `include=code_interpreter_call.outputs` 由 runtime 接管，不再提前被 include 校验误拦截。 |
| 2026-06-26 | 已完成 | AI | 已通过 Responses `code_interpreter` local_runtime gate 回归：未配置 executor 时本地 503、`service_unavailable`、include 不误拦截、零 Anthropic 上游命中；真实 worker / 容器沙箱仍后续推进。 |
| 2026-06-26 | 进行中 | AI | 开始实现 code interpreter worker 沙箱首段：先走 Anthropic client tool loop，由上游模型生成 Python code，网关独立子进程执行并回灌 `tool_result`，不在主 Web 进程执行。 |
| 2026-06-26 | 已完成 | AI | 已实现 code interpreter worker 沙箱首段：`local_runtime` 下 Anthropic 选择 `python` client tool，网关独立 Python 子进程执行，回灌 `tool_result` 后生成最终 Responses；mock 回归覆盖 stdout、stderr、截断、超时和安全 env 不泄漏。 |
| 2026-06-26 | 已完成 | AI | 已实现 code interpreter container files 兼容壳首段：本地 worker 产物写入 Files 时绑定 `container_id`，支持 container 文件列表、详情和 content 下载；mock 回归覆盖 `container_download_path`、错误 container 404 和普通 Files 下载回退。 |
| 2026-06-25 | 进行中 | AI | 依据 OpenAI MCP 官方形态，MCP 输出需要覆盖 `mcp_list_tools`、`mcp_call`、`mcp_approval_request`；本轮先做固定 allowlist 的 mock proxy，不连接真实远程 MCP server。 |
| 2026-06-25 | 进行中 | AI | 已实现 Responses MCP mock proxy：仅 `mcp=mock` 且命中 `mock-mcp` allowlist 时启用，JSON / SSE 返回 `mcp_list_tools`、approval request 或 `mcp_call`，不请求 Anthropic、不连接远程 MCP、不回显 authorization。 |
| 2026-06-25 | 进行中 | AI | 已补真实 MCP proxy 长期设计：首批只承接 `server_url` 远程 MCP，`connector_id` 必须等独立 connector adapter；真实执行需 server allowlist、auth 引用、tool cache、approval request、execution record、transport 限制、输出省略和审计。 |
| 2026-06-25 | 进行中 | AI | 已补 MCP 入口定义校验骨架和 mock 回归：重复 `server_label`、`server_url` / `connector_id` 冲突、缺少 server 标识、凭据 header 冲突等错误本地 400，避免被 guidance 或 mock runtime 吞掉。 |
| 2026-06-25 | 进行中 | AI | 已实现 MCP `local_runtime` 首段 gate：只用于 Responses MCP real proxy；`connector_id` 本地拒绝，未配置 executor 本地 503，避免误打 Anthropic 或远程 MCP。 |
| 2026-06-25 | 进行中 | AI | 已实现 MCP real proxy allowlist / executor 第一段：本地 allowlist server、JSON-RPC `initialize` / `tools/list` / `tools/call`、authorization 白名单透传、`allowed_tools` 过滤、输出截断。 |
| 2026-06-25 | 进行中 | AI | 已实现 MCP JSON 免批模型驱动工具循环：非流式 Responses 在免批策略下先把 MCP 工具导入 Anthropic client tools，Anthropic `tool_use` 触发网关执行 `tools/call`，再把 `tool_result` 回灌给同一 Anthropic 上游生成最终回答；默认 approval 仍本地返回 `mcp_approval_request`。 |
| 2026-06-25 | 进行中 | AI | 已实现 MCP Responses SSE 免批缓冲式工具循环：首轮 Anthropic SSE 聚合成完整 `tool_use`，执行 MCP `tools/call` 后二次回灌给 Anthropic，并以 OpenAI typed SSE 输出 `mcp_list_tools`、`mcp_call` 和最终 assistant message。 |
| 2026-06-25 | 进行中 | AI | 已实现 MCP JSON 受限多轮工具循环：免批路径下最多执行 4 轮 Anthropic `tool_use` / MCP `tools/call` / Anthropic `tool_result` 回灌，并在 Responses metadata 记录执行轮数。 |
| 2026-06-25 | 进行中 | AI | 已补 MCP HTTP-SSE transport mock 回归：本机 mock MCP server 的 `/mcp-sse` 以 `text/event-stream` 返回 JSON-RPC result frame，覆盖 `initialize`、`tools/list` 和 `tools/call`。 |
| 2026-06-25 | 进行中 | AI | 已补 MCP approval id 绑定校验：mock runtime 和 local_runtime proxy 都要求 `mcp_approval_response.approval_request_id` 匹配当前 server / tool / arguments，错误 ID 本地 400，正确 ID 才进入 `mcp_call` 或 `tools/call`。 |
| 2026-06-25 | 进行中 | AI | 继续补 MCP transport 完整化的可落地子项：有限重试、重定向拒绝诊断和错误正文省略先用 mockai 覆盖，真实第三方 HTTP-SSE 长连接联调仍后置。 |
| 2026-06-25 | 进行中 | AI | 已实现 MCP real approval 首段：`mcp_approval_request` 写入业务库状态机，绑定当前 API Key / 分组 scope；approve 后执行前标记 consumed，reject / expired / replay / 跨 API Key 均不执行 `tools/call`。人工审批 UI 和 execution record 查询后置。 |
| 2026-06-25 | 已完成 | AI | 已实现 MCP real execution record 首段：每次真实 `tools/call` 写入业务库记录，记录 scope、server、tool、arguments digest、output digest / bytes、耗时、状态和错误码，不保存远程输出正文；mock 回归已覆盖成功、失败和输出截断。 |
| 2026-06-25 | 已完成 | AI | 已补 MCP real execution record 查询 API 首段：新增管理侧 `/__aisys__/api/mcp-execution-records` 和用户侧 `/__aisys__/api/my-mcp-execution-records` 摘要查询 / 详情读取，按 system account scope 裁剪，不提供远程输出正文读取路径。 |
| 2026-06-25 | 已完成 | AI | 已补 MCP real approval 管理 API 首段：新增管理侧 `/__aisys__/api/mcp-approval-requests` 和用户侧 `/__aisys__/api/my-mcp-approval-requests` 摘要查询 / 详情 / approve / reject，审批 API 只改状态，不直接执行远程 MCP，执行仍由后续 `mcp_approval_response` 请求触发。 |
| 2026-06-25 | 已完成 | AI | 已补 MCP server allowlist 存储化首段：新增业务库 `openai_compatible_mcp_servers`、管理侧 `/__aisys__/api/mcp-servers` 和用户侧 `/__aisys__/api/my-mcp-servers`；运行时按当前 system account 读取 DB enabled server 并合并环境 bootstrap。 |
| 2026-06-25 | 已完成 | AI | 已补 MCP 运营入口 UI 首段：新增用户侧 `/my-mcp-runtime` 与管理侧 `/mcp-runtime`，同一页面三标签页覆盖 server allowlist、approval request 和 execution record；页面不触发远程 MCP 探测、不回显凭据、不读取远程输出正文。 |
| 2026-06-25 | 进行中 | AI | 继续补 MCP server 可用性诊断和工具 schema 缓存：该能力必须是显式人工 / 后台动作，复用 MCP proxy transport，只执行 `initialize` / `tools/list`，不在列表页隐式访问远程 server，不保存明文 authorization。 |
| 2026-06-25 | 进行中 | AI | 继续补 MCP legacy HTTP+SSE transport：按 MCP 2025-06-18 回退规则，Streamable HTTP `initialize` 4xx 后 GET 同 URL，等待 `endpoint` 事件，再通过同源 endpoint POST JSON-RPC，并从 SSE `message` 事件读取匹配 id 的 response。 |
| 2026-06-25 | 进行中 | AI | 已实现 MCP legacy HTTP+SSE mock 状态机：普通 Streamable HTTP 优先，`initialize` POST 返回 404 / 405 / 410 / 415 时回退 GET SSE；endpoint 必须同源，JSON-RPC request 等待匹配 id 的 `message` 帧，notification 不等待响应；桥接 executor 增加 `close(prepared)` 生命周期钩子，避免模型驱动工具循环后 SSE 连接泄漏。 |
| 2026-06-25 | 已完成 | AI | 已补 MCP server 诊断与工具 schema 缓存首段：新增业务库缓存 / 诊断摘要、管理侧 / 用户侧 `tools` 读取和 `diagnose` 手动入口；前端 Server 行新增诊断动作，详情抽屉显示最近诊断和缓存工具摘要。 |
| 2026-06-25 | 已完成 | AI | 已补 MCP Responses SSE 兼容事件：参考 OpenAI Realtime MCP 生命周期事件，`mcp_call` 在完整参数已知后输出 `response.mcp_call_arguments.delta`、`response.mcp_call_arguments.done` 和 `response.mcp_call.in_progress`，再输出最终 `response.output_item.done`；该 delta 暂为缓冲式单片段，不宣称实时逐片段。 |
| 2026-06-25 | 已完成 | AI | 已补 MCP `response.mcp_call.failed` 兼容事件：MCP `tools/call` 失败时生成带稳定 `error.code` 的 `mcp_call` item，SSE 输出 `response.mcp_call.failed`，再用 `response.completed` 收口；响应检查管线不再把该工具生命周期失败误判为整个 Responses 流失败。 |
| 2026-06-25 | 已完成 | AI | 已补 computer adapter 协议设计：按 OpenAI GA `computer_call.actions[]` / `computer_call_output` loop 建模，明确 `native_bridge` 与 `local_runtime` 两条路径、受控浏览器默认边界、会话状态、动作 allowlist、截图省略、人工确认和 mockai 先行验收。 |
| 2026-06-25 | 已完成 | AI | 已实现 Responses `computer=mock` 首段：JSON / SSE 本地返回固定 `computer_call`，带 `computer_call_output` 的后续请求正常收口；不启动浏览器、不请求 Anthropic、不回显用户 prompt 或截图正文。 |
| 2026-06-26 | 已完成 | AI | 已完成 Computer adapter `local_runtime` 首段：不把 Playwright 加入默认依赖；新增 adapter 接口、未配置本地 503、测试 adapter JSON / SSE 协议回归、会话 / 动作 metadata 裁剪和截图正文省略边界。 |
| 2026-06-26 | 已完成 | AI | 已完成 Computer adapter HTTP sandbox bridge 首段：显式配置 `JUHE_AI_COMPUTER_BROWSER_ADAPTER_*` 后，网关调用外部 sandbox/container browser HTTP adapter，归一化 `message` / `computer_call`，并继续执行响应大小限制和敏感字段脱敏；完整内置 Playwright / container browser 会话管理仍后续。 |

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
| 2026-06-25 | MCP JSON 多轮只对免批工具开放并设置轮次上限 | 未持久化 approval 前不能让模型绕过审批触发工具调用；递归工具循环必须有调用次数上限和失败策略 | 本阶段只对免批工具执行最多 4 轮 Anthropic tool-result loop；默认 approval 本地返回 `mcp_approval_request`；超过上限时受控收口 |
| 2026-06-25 | MCP Responses SSE 在缓冲式 terminal snapshot 中补 MCP 生命周期事件 | OpenAI Realtime MCP flow 明确 `response.mcp_call_arguments.delta/done`、`response.mcp_call.in_progress`、成功后的 `response.output_item.done` 和失败时的 `response.mcp_call.failed`；当前 Responses bridge 已能在完整参数已知后输出单片段 arguments delta/done、in_progress 和失败专用事件 | 流式客户端可消费完整 `mcp_list_tools`、`mcp_call_arguments.delta/done`、`mcp_call.in_progress`、成功 `mcp_call` 或失败 `mcp_call.failed`、最终消息；实时逐 token / 逐 arguments 增量后续单独补 |
| 2026-06-25 | MCP approval 先补业务库状态机首段，不伪装完整 UI | 当前可从请求上下文取得 API Key / 分组 scope，但还没有人工审批页面、server 管理和 execution record 查询 | `approval_request_id` 记录到业务库，绑定 server / tool / arguments 与当前 scope；错误 ID、跨 scope、过期、非 pending 和 replay 本地拒绝；人工审批和完整审计查询仍作为后续项 |
| 2026-06-25 | MCP execution record 首段只记录摘要，不保存远程正文 | MCP 输出可能包含第三方敏感数据或大 payload，不能为了排障把正文写进业务库 | 记录 arguments / output digest、字节数、truncated、错误码和耗时；查询 UI 与长期审计视图后续单独补 |
| 2026-06-25 | 人工审批 API 不执行远程工具 | 后台审批应是状态变更，避免管理 API 成为绕过客户端上下文和 OpenAI Responses 生命周期的远程工具执行入口 | approve / reject API 只改 `openai_compatible_mcp_approval_requests` 状态；后续执行必须由客户端请求带 `mcp_approval_response.approval_request_id` 进入 gateway，并在执行前 consume |
| 2026-06-25 | MCP server allowlist 长期以业务库为主 | 环境变量 JSON 适合本地回归和应急启动，但不适合按系统账户、分组和 API Key 做长期权限管理 | 新增 server allowlist 存储 / API 后，runtime resolver 先读业务库 enabled server，再合并环境 bootstrap；管理列表不访问远程 MCP，不保存明文 token |
| 2026-06-25 | MCP 运营 UI 不做远程探测 | 列表页访问第三方 MCP 会带来延迟、凭据和副作用风险 | UI 首段只读写本地业务库和审批 / 执行摘要；server 可用性检测、`tools/list` schema 缓存和长连接诊断后续做独立诊断任务 |
| 2026-06-25 | MCP server 诊断只做显式 `tools/list` | OpenAI Responses 文档建议复用 `mcp_list_tools` 上下文来降低延迟，但远程 MCP server 有敏感数据和副作用风险 | 新增诊断 / 缓存时只允许手动或后台任务触发，成功缓存 schema 摘要，失败保留错误摘要，不执行 `tools/call`、不生成 approval、不写 execution record |
| 2026-06-25 | computer adapter 不直接触碰宿主桌面 | OpenAI computer use 官方建议隔离浏览器 / VM，并把第三方页面内容视为不可信输入；宿主桌面包含本机文件、凭据和应用状态，不能作为默认运行环境 | 首批只设计受控浏览器 / container browser；动作、域名、截图和敏感输入都要经过 allowlist / confirmation / omission 策略；未实现 adapter 前继续 guidance / reject |

## 验收标准

- [x] 运行时设计文档已创建并纳入索引。
- [x] 当前无执行器 guidance mock 覆盖已记录。
- [x] Runtime Registry 和配置开关完成。
- [x] Responses `tool_search` / `namespace` 本地展开具备 JSON / SSE mock 回归。
- [x] MCP / code interpreter / computer 至少一个 runtime 具备 JSON / SSE mock 回归。
- [x] MCP Responses SSE 成功路径具备 `mcp_call_arguments.delta/done`、`mcp_call.in_progress` 与最终 `mcp_call.arguments` 一致性回归；失败路径具备 `response.mcp_call.failed`、最终 `mcp_call.error` 和 `response.completed` 收口回归。
- [x] Computer adapter 已完成协议设计，明确真实运行时启用前必须具备隔离、权限、动作校验、截图省略、人工确认和审计。
- [x] Responses `computer=mock` 具备 JSON / SSE `computer_call` 和 `computer_call_output` 收口回归。
- [ ] 每个启用 runtime 都有权限、审计、超时、输出大小和凭据扫描验证。

## 验证记录

- 类型检查：2026-06-26 已复跑并通过 `pnpm --dir backend typecheck`；2026-06-25 已复跑并通过 `pnpm --dir frontend typecheck`。
- 响应检查：2026-06-25 已复跑并通过 `pnpm --dir backend test:response-inspection-policy`、`pnpm --dir backend test:response-inspection-gateway-e2e` 和 `pnpm --dir backend test:response-inspection-mock-ai-fields`，`response.mcp_call.failed` 不再被误判为整条 Responses 失败，默认 error 语义仍保持稳定。
- Mock 回归：2026-06-25 已复跑并通过 `pnpm --dir backend test:openai-anthropic-bridge-mock`，当前无执行器 guidance 覆盖 Responses `computer` / `code_interpreter` / `mcp` 和 Chat `code_interpreter`；Runtime Registry reject 覆盖 `code_interpreter=reject` 本地 400 且不请求 Anthropic；Responses `tool_search` + `namespace` 本地展开覆盖 JSON / SSE；Responses `code_interpreter` mock runtime 覆盖 JSON / SSE、`include=code_interpreter_call.outputs`、固定 logs marker、用户 prompt 不进入工具输出且不上游；Responses `computer` mock runtime 覆盖 JSON 固定 `computer_call.actions=[screenshot]`、SSE typed event、`computer_call_output` 收口、用户 prompt / 截图正文不回显且不上游；Responses MCP mock proxy 覆盖 JSON / SSE、`allowed_tools` 过滤、approval request、approval id 错误本地 400、approval id 正确后返回 `mcp_call`、SSE `mcp_call_arguments.delta/done`、`mcp_call.in_progress`、未授权 server 本地拒绝、authorization 不回显、用户 prompt 不进入工具输出且不上游；MCP definition validation 覆盖重复 `server_label`、`server_url` / `connector_id` 冲突、缺少 server 标识、凭据 header 冲突本地 400 且不上游；MCP `local_runtime` gate 覆盖未配置 executor 本地 503、`connector_id` 本地 400、authorization / prompt 不泄漏且不上游；MCP real proxy mock server 覆盖 allowlist 读取、authorization 仅入 allowlist server、JSON 免批路径 Anthropic 选择工具 + MCP `tools/call` + Anthropic `tool_result` 回灌最终回答、Responses SSE 免批缓冲式工具循环及 `mcp_call_arguments.delta/done` / `mcp_call.in_progress`、MCP `tools/call` 失败 JSON `mcp_call.error` 与 SSE `response.mcp_call.failed`、JSON 受限多轮工具循环、Streamable HTTP JSON、POST-SSE result frame、legacy HTTP+SSE endpoint/message 长连接、transport retry 后成功、redirect blocked 本地 502 且不泄漏 Location query、默认 approval 本地 `mcp_approval_request` 且不上游不调用 `tools/call`、approval request 业务库 pending 记录、跨 API Key scope 403、approval id 错误本地 400、approval id 正确后执行 `tools/call` 并标记 consumed、replay 400、reject 普通响应、expired 400、output 截断；2026-06-25 已通过 `pnpm --dir backend test:mcp-execution-records-route`，覆盖 MCP execution record 管理侧分页 / 组合筛选、用户侧 self scope、详情 scope 裁剪、非管理员拒绝和远程输出正文不返回；2026-06-25 已通过 `pnpm --dir backend test:mcp-approval-requests-route`，覆盖 MCP approval 管理侧分页 / 组合筛选、用户侧 self scope、approve / reject、过期和重复审批 409、非管理员拒绝以及人工审批不写 execution record；2026-06-25 已通过 `pnpm --dir backend test:mcp-servers-route`，覆盖 MCP server allowlist 存储化 scope、唯一性、runtime enabled 过滤、per-server limit、显式诊断、工具 schema 缓存和 authorization 不回显。
- Code interpreter local_runtime gate：2026-06-26 已通过 `pnpm --dir backend test:openai-anthropic-bridge-mock`，覆盖未配置 executor 本地 503、`service_unavailable`、`include=code_interpreter_call.outputs` 不误拦截和零 Anthropic 上游命中。
- Code interpreter 文件产物元数据：2026-06-26 已通过 `pnpm --dir backend test:openai-anthropic-bridge-mock`，覆盖 worker 生成文件后的 `[artifacts]` logs 摘要、`metadata.artifacts` 文件名 / 字节数 / MIME / 内容省略原因，以及文件正文不进入 Responses payload 或 Anthropic `tool_result`。
- Code interpreter 文件产物可下载：2026-06-26 已通过 `pnpm --dir backend test:openai-anthropic-bridge-mock`，覆盖小文件产物写入本地 OpenAI 兼容 Files、Responses 返回 `file_id` / `download_path`、`[artifacts]` logs 带 `file_id`、可通过 `/v1/files/{file_id}/content` 下载原始内容，以及超限文件只返回元数据并标记 `file_too_large`。
- Responses compact snapshot 完整性：2026-06-26 已通过 `pnpm --dir backend test:openai-anthropic-bridge-mock`，覆盖 `/responses/compact` 返回 `response.compaction`、`juhecmp.v2` snapshot 后续恢复、跨 API Key 403 boundary mismatch、digest 篡改 404 snapshot not found，以及异常路径零 Anthropic 上游命中。
- Computer adapter local_runtime 首段：2026-06-26 已通过 `pnpm --dir backend test:openai-anthropic-bridge-mock`，覆盖未配置 adapter 本地 503、测试 adapter JSON / SSE `computer_call`、`computer_call_output` 收口、会话 / 动作 metadata 裁剪、动作 `text` 省略、截图 data URL / prompt / token / base64 不回显和零 Anthropic 上游命中；同日补 HTTP sandbox adapter 首段，覆盖显式配置 endpoint、本机 adapter HTTP 调用、`computer_call` / message 归一化、metadata `adapter=http_browser`、adapter 响应体超限本地 502 和不上游。
- 真实联调：2026-06-25 已复跑并通过 `pnpm --dir backend test:openai-anthropic-bridge-real`；真实上游 `https://vsllm.com`、模型 `claude-sonnet-4-6`、源模型 `gpt-5.5`，`responses_tool_search_namespace:passed`。
- 凭据检查：2026-06-26 已扫描固定真实 key 前缀，无命中；`git diff --check` 无 whitespace error，仅有 LF/CRLF 提示。
- MCP 运营 API：2026-06-25 已通过 `pnpm --dir backend test:mcp-servers-route`，覆盖 server allowlist 存储、显式诊断、工具 schema 缓存、失败诊断摘要、scope 和禁用边界；已通过 `pnpm --dir backend test:scope`，确认新增 MCP 运行时接口仍按用户侧 / 管理侧 scope 裁剪；已通过 `pnpm --dir backend typecheck` 和 `pnpm --filter juhe-ai-frontend typecheck`。
- 未验证项：MCP real proxy 的真实第三方 HTTP-SSE 联调、人工审批 UI、execution record 查询页面尚未实现；code interpreter 容器 / VM 级沙箱、computer adapter 完整内置 Playwright / container browser 会话管理尚未实现；不得在生产配置中宣称等价 OpenAI hosted runtime。Responses `mcp` 当前已具备本地 allowlist executor 第一段、DB server allowlist、JSON 免批多轮 tool-result 回灌、Responses SSE 缓冲式工具循环、Streamable HTTP / POST-SSE / legacy HTTP+SSE mock transport、有限重试、重定向拒绝诊断、approval 管理 API 和 execution record 摘要查询 API；Responses `code_interpreter` 当前具备 mock runtime、local_runtime Python worker、文件产物元数据和本地 Files 下载，但仍不是容器 / VM 等价隔离；Responses `computer` 当前已有 adapter 协议设计、`computer=mock` 固定协议外形、`computer=local_runtime` adapter gate / 测试 adapter 协议回归，以及 HTTP sandbox adapter 桥接外部受控浏览器服务的首段能力；该 HTTP adapter 仍依赖外部 sandbox/container browser 实现域名 allowlist、动作执行、截图持久化和人工确认，不代表网关内置完整 browser runtime；Responses `tool_search` 仅验证请求内 namespace function 本地展开，不代表完整 OpenAI hosted `tool_search_call` / `tool_search_output` 生命周期。

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
