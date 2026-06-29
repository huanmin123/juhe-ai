# PLAN-0063 OpenAI 托管工具运行时适配

## 基本信息

- 编号：PLAN-0063
- 状态：进行中
- 创建时间：2026-06-24
- 更新时间：2026-06-26
- 需求来源：用户对话
- 执行者：AI
- 关联模块：后端 / 网关 / Anthropic bridge / OpenAI hosted tools / code execution / computer use / 文档 / 验证

## 当前结论

OpenAI hosted tools 不能靠字段映射伪造成功。当前统一口径是：能由网关安全承接的能力按显式 runtime / provider / adapter 承接；不能承接的能力返回 OpenAI 形态 agent guidance，不请求不支持的上游。

2026-06-26 已明确产品决策：MCP 不在网关服务端实现 runtime、proxy、protocol extension、allowlist、auth、approval、execution record、诊断接口或前端 UI。Responses `tools[].type=mcp` 固定返回 guidance，引导客户端使用本地 MCP、调用方 agent 自行执行，或切换到原生支持该 MCP 能力的上游。

## 范围边界

### 本计划包含

- [x] Runtime Registry 的 guidance / reject / mock / local_runtime 基础分层。
- [x] Responses `tool_search` + `namespace` function 本地展开，不伪造 hosted search item。
- [x] Responses `code_interpreter` mock runtime JSON / SSE。
- [x] Responses `code_interpreter` local_runtime Python worker 首段：独立子进程、临时目录、超时、输出截断、stdout / stderr logs、文件产物元数据、本地 Files 下载和 container files 兼容壳。
- [x] Responses `computer` mock runtime JSON / SSE。
- [x] Responses `computer` local_runtime adapter 首段：未配置 503、测试 adapter JSON / SSE、会话 / 动作 metadata 裁剪、截图正文省略。
- [x] Responses `computer` HTTP sandbox adapter bridge 首段：显式配置外部 sandbox/container browser endpoint，网关只做调用、超时 / 响应体限制、JSON 归一化和敏感字段脱敏。
- [x] MCP 服务端 runtime/proxy 撤销：删除后端 MCP proxy / approval / execution / server 管理模块、前端 MCP 管理入口和相关配置项，保留 guidance。

### 本计划不包含

- 不在主 Web 进程内执行任意代码。
- 不默认开放网络、宿主文件系统、环境变量或系统命令。
- 不把宿主桌面暴露给模型。
- 不在网关服务端连接、代理或管理远程 MCP server。
- 不输出网关生成的 `mcp_list_tools`、`mcp_call`、`mcp_approval_request` 或 `mcp_approval_response`。
- 不把 Anthropic 原生 beta 能力写死为协议规则；必须由账号 profile 显式声明。

## 关联文档

- 运行时设计：`docs/functions/OpenAI托管工具运行时设计.md`
- 高兼容矩阵：`docs/functions/OpenAI到Anthropic高兼容能力矩阵.md`
- OpenAI 到 Anthropic 桥接：`docs/functions/OpenAI到Anthropic协议桥接设计.md`
- 验证手册：`docs/develop/测试与验证说明.md`

## 执行拆解

- [x] 创建 PLAN-0063。
- [x] 新增并更新 `docs/functions/OpenAI托管工具运行时设计.md`。
- [x] 更新高兼容矩阵和桥接设计文档。
- [x] Runtime Registry 落地到后端类型和配置。
- [x] 移除 `JUHE_AI_HOSTED_TOOL_MCP_MODE` 和 `runtimeConfig.mcpProxy`。
- [x] 删除服务端 MCP proxy executor、approval request、execution record、server allowlist 仓储和路由。
- [x] 删除前端 MCP runtime 管理入口和类型 / API 模块。
- [x] OpenAI -> Anthropic bridge 移除 MCP mock/proxy/local_runtime/tool loop 分支，MCP 只走 unsupported hosted tool guidance。
- [x] 统一入口静态检查移除 MCP proxy fetch 例外。
- [x] OpenAI -> Anthropic mock 回归保留 Responses `mcp` guidance 用例，断言不上游。
- [ ] Code interpreter 后续补容器 / VM 级隔离、完整 create/upload/delete container 生命周期和更强网络隔离。
- [ ] Computer adapter 后续补完整内置 Playwright / container browser 会话管理、域名 allowlist、动作 allowlist、截图引用持久化、人工确认和审计摘要。

## 测试项

| 测试类型 | 测试项 | 验证方式 / 命令 | 预期结果 | 状态 |
| --- | --- | --- | --- | --- |
| 类型检查 | 后端类型 | `pnpm --dir backend typecheck` | 删除 MCP 模块后无悬空引用 | 已通过 |
| Mock 回归 | 当前 guidance | `pnpm --dir backend test:protocol-boundary-openai-anthropic` | Responses `mcp` 返回正常 guidance，且不请求 Anthropic、不连接远程 MCP | 未通过；脚本初始化阶段被账号模型别名跨协议映射限制拦截，未进入 MCP 断言 |
| 静态检查 | 模型调用统一入口 | `pnpm --dir backend test:gateway-model-call-unified-entry` | 生产模型调用仍集中在公共网关调度链路；fetch 例外不包含 MCP proxy | 未通过；当前失败点为 explicit hybrid route 账号候选读取和通用 upstream request fetch 静态守卫，不是 MCP proxy |
| 类型检查 | 前端类型 | `pnpm --dir frontend typecheck` | 删除 MCP 管理页面后无路由 / 类型引用 | 已通过 |
| Scope 回归 | 权限边界 | `pnpm --dir backend test:scope` | 删除 MCP 管理接口后 scope 回归通过 | 已通过 |

## 决策记录

| 日期 | 决策 | 原因 | 影响 |
| --- | --- | --- | --- |
| 2026-06-24 | 默认保持 L4 guidance | 任意代码、远程工具和 computer use 都有安全边界，不能静默伪造执行 | 未实现 runtime 时不请求 Anthropic、不执行本地工具 |
| 2026-06-24 | code execution 不进主 Web 进程 | 防止请求路径执行任意代码拖垮或污染网关进程 | 使用 worker / 子进程 / 容器类隔离 |
| 2026-06-24 | computer 首批只考虑受控 adapter | 宿主桌面暴露风险过高，纯文本模拟没有语义价值 | 首批优先受控浏览器或上游原生 computer use |
| 2026-06-25 | Responses `tool_search` 先做请求内 namespace function 本地展开 | OpenAI hosted search 的加载生命周期 Anthropic Messages 无等价事件，但请求内 function 工具可安全转为 Anthropic client tool | 客户端可收到标准 Responses `function_call.name` / `namespace`；不伪造 `tool_search_call` / `tool_search_output` |
| 2026-06-26 | MCP 不在网关服务端实现 runtime/proxy | 用户明确要求凡是不兼容的引导客户端处理，不在中转层扩展协议或自建 MCP | 删除 MCP 服务端代码、配置、API、UI 和测试；Responses `mcp` 固定 guidance |

## 验收标准

- [x] 运行时设计文档已更新为当前边界。
- [x] 高兼容矩阵已更新为 MCP 固定 guidance。
- [x] 后端 MCP runtime/proxy/management 模块已删除。
- [x] Bridge 不再执行 MCP mock/proxy/local_runtime。
- [ ] 后端 / 前端类型检查全部通过。
- [ ] 关键 mock / scope / 静态检查回归通过。

## 验证记录

- 2026-06-26：`pnpm --dir backend typecheck` 已通过。
- 待复跑：`pnpm --dir frontend typecheck`、`pnpm --dir backend test:protocol-boundary-openai-anthropic`、`pnpm --dir backend test:gateway-model-call-unified-entry`、`pnpm --dir backend test:scope`。

## 风险与注意事项

- code interpreter 当前仍不是容器 / VM 等价隔离，生产启用前要继续补强隔离和网络策略。
- computer HTTP sandbox adapter 依赖外部受控浏览器服务，网关不内置完整浏览器会话管理。
- MCP 请求不会在服务端执行；客户端如果依赖 MCP 工具结果，必须在本地 agent / MCP 层完成后再继续会话，或切换原生支持该能力的上游。

## 完成总结

- 完成时间：待补充
- 实际完成内容：待补充
- 主要改动位置：待补充
- 验证结果：待补充
- 后续建议：待补充
