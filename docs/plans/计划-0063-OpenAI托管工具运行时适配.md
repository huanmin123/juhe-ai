# PLAN-0063 OpenAI 托管工具运行时适配

## 基本信息

- 编号：PLAN-0063
- 状态：进行中
- 创建时间：2026-06-24
- 更新时间：2026-06-24
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
- [x] 设计 Runtime Registry 和配置开关；首批只允许 `guidance` / `reject`，不允许未实现执行器的 local/native 模式。
- [ ] 实现 MCP proxy 首批 allowlist + mock server。
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
- [ ] MCP proxy：server allowlist、auth、approval mock、tool result 映射和审计。
- [ ] Code interpreter：受限 worker、临时目录、超时、输出上限、stderr 和文件产物策略。
- [ ] Computer adapter：受控浏览器状态、动作协议、截图省略和拒绝策略。
- [ ] 对每个 runtime 补 JSON / SSE mock 回归，再做真实联调。

## 测试项

| 测试类型 | 测试项 | 验证方式 / 命令 | 预期结果 | 状态 | 实际结果或备注 |
| --- | --- | --- | --- | --- | --- |
| Mock 回归 | 当前 guidance | `pnpm --dir backend test:openai-anthropic-bridge-mock` | 无执行器时返回 OpenAI 形态 guidance，且不请求 Anthropic | 已通过 | 已覆盖 Responses `computer` / `code_interpreter` / `mcp` 和 Chat `code_interpreter` |
| Mock 回归 | Runtime Registry reject | `pnpm --dir backend test:openai-anthropic-bridge-mock` | 显式配置 `code_interpreter=reject` 时返回本地 400，且不请求 Anthropic | 已通过 | 已覆盖临时切换 `runtimeConfig.hostedToolRuntimes.codeInterpreter='reject'` 后 Responses 本地拒绝 |
| 设计检查 | 运行时边界 | 文档审查 | 不在主 Web 进程执行代码；MCP 需要 allowlist；computer 不能文本伪造 | 已完成 | 已写入运行时设计 |
| Mock 回归 | MCP proxy | 待实现 | allowlist / auth / approval / 输出超限均可诊断 | 未开始 | 待 Runtime Registry |
| Mock 回归 | Code interpreter | 待实现 | 成功、stderr、超时、输出超限、网络禁止均可诊断 | 未开始 | 待沙箱 worker |
| Mock 回归 | Computer adapter | 待实现 | 动作成功、动作拒绝、截图省略、超时均可诊断 | 未开始 | 待 adapter 设计 |
| 安全检查 | 凭据与敏感输出 | 固定 key 前缀和工具输出扫描 | 不落真实 key、截图或大输出正文 | 待执行 | 每次实现后执行 |

## 进度记录

| 日期 | 状态 | 记录人 | 进展 / 决策 / 阻塞 |
| --- | --- | --- | --- |
| 2026-06-24 | 进行中 | AI | 已确认剩余 hosted runtime 不能靠协议字段映射解决；创建 PLAN-0063，并新增运行时设计文档。 |
| 2026-06-24 | 进行中 | AI | 当前 guidance mock 已覆盖 Responses `computer` / `code_interpreter` / `mcp` 和 Chat `code_interpreter`，真实运行时仍需 Runtime Registry、权限、沙箱、allowlist 和审计后才能启用。 |
| 2026-06-24 | 进行中 | AI | 已落地 Runtime Registry 首批骨架和环境配置：`code_interpreter`、`computer`、`mcp`、`shell`、`skills`、`tool_search` 默认 `guidance`，可显式切到 `reject`；mock 覆盖 reject 不上游。 |

## 决策记录

| 日期 | 决策 | 原因 | 影响 |
| --- | --- | --- | --- |
| 2026-06-24 | 默认保持 L4 guidance | 任意代码、远程 MCP 和 computer use 都有安全边界，不能静默伪造执行 | 未实现 runtime 时不请求 Anthropic、不执行本地工具 |
| 2026-06-24 | code execution 不进主 Web 进程 | 防止请求路径执行任意代码拖垮或污染网关进程 | 后续必须使用 worker / 子进程 / 容器类隔离 |
| 2026-06-24 | MCP 必须 allowlist + approval | remote MCP 可访问外部系统和敏感数据 | 未配置 allowlist 或 approval 时 guidance / 拒绝 |
| 2026-06-24 | computer 首批只考虑受控 adapter | 宿主桌面暴露风险过高，纯文本模拟没有语义价值 | 首批优先受控浏览器或上游原生 computer use |
| 2026-06-24 | Registry 首批只开放 `guidance` / `reject` | 真实 local/native 执行器尚未实现，开放 local/native 配置会让部署误以为工具可执行 | 后续每个 runtime 实现后再扩展对应模式枚举 |

## 验收标准

- [x] 运行时设计文档已创建并纳入索引。
- [x] 当前无执行器 guidance mock 覆盖已记录。
- [x] Runtime Registry 和配置开关完成。
- [ ] MCP / code interpreter / computer 至少一个 runtime 具备 JSON / SSE mock 回归。
- [ ] 每个启用 runtime 都有权限、审计、超时、输出大小和凭据扫描验证。

## 验证记录

- 类型检查：2026-06-24 已复跑并通过 `pnpm --dir backend typecheck`。
- Mock 回归：2026-06-24 已复跑并通过 `pnpm --dir backend test:openai-anthropic-bridge-mock`，当前无执行器 guidance 覆盖 Responses `computer` / `code_interpreter` / `mcp` 和 Chat `code_interpreter`；Runtime Registry reject 覆盖 `code_interpreter=reject` 本地 400 且不请求 Anthropic。
- 未验证项：MCP proxy、code interpreter 沙箱、computer adapter 真实运行时尚未实现；不得在生产配置中宣称可执行。

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
