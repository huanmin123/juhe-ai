# BUG-0231：glm 目录快照全系缺 function_calling 声明——glm 主模型会话工具体系整体不可用

## 基本信息

- 编号：BUG-0231
- 状态：已修复（待发布）
- 严重程度：P1
- 发现时间：2026-09-29
- 发现方式：用户反馈（生产 AI 对话 `chat_conv_f548583419ab5df348829d0505e0d0cf`，2026-09-29 13:44 问「上海今天天气怎么样？」不联网）
- 模块：模型目录静态快照（`internal/pricing/data_glm.go`）
- 关联 bug：BUG-0229（同族：目录能力数据空缺致工具体系失效，彼为 custom 行继承缺口，本为内置行漏写）
- 责任人：主代理

## 问题概述

- 现象：glm 系模型做主对话模型的 AI 问答会话，`web_search` / `generate_image` 内部工具定义完全不注入——主模型不知道有工具可用，问实时信息时纯文本回答「无法联网」（生产实证：assistant reasoning 自述 "I don't have access to real-time weather data or any tools"），也不触发 `tool.binding_required` 绑定引导（工具未注入则模型无从发起调用）。
- 期望：glm 主模型会话与 gpt/xai 同等：主模型目录矩阵声明 `function_calling`（chat_completions 协议）→ 内部工具注入 → 未绑定时走 binding_required 引导，已绑定则子代理执行。
- 实际：`internal/pricing/data_glm.go` 全部 16 个模型行（glm-4.5 ~ glm-5.3 系）均无 `SupportedToolsByProtocol` 字段——静态矩阵空 → `ChatModelOption.supportsTool("function_calling")` 恒 false（`stream_route.go` resolveTools 入参）→ 工具零注入。
- 影响范围：所有 glm 主模型会话的联网搜索与生图子代理（含 `toolCapabilities` 会话工具面板判定、绑定引导）；glm 作为搜索/生图子代理绑定候选不受影响（候选按绑定侧模型矩阵判定，glm 本就不进搜索/生图候选）。
- 根因：§6.4 静态快照升级契约「各供应商官方文档的协议级能力」落地时 glm 快照漏写工具声明（deepseek / xai / anthropic / gemini 均有写）；智谱 GLM 4.5+ 系 chat_completions 官方支持 `tools` 参数（function calling）。

## 复现步骤

1. 任一 glm 账户（生产实证：`acc_1789554391450_44168fa8` api.zeekai.cc-极客-glm，模型 glm-5.3-flash）新建 AI 对话会话。
2. 问「上海今天天气怎么样？」（会话无论是否绑定搜索账户）。
3. 模型无工具可用，纯文本道歉回答；会话工具面板恒不可用、无绑定引导。

## 环境信息

- 分支 / 版本：生产国内单机 Docker go-only（103.36.63.105），gateway `6cd74720ccfd`（2026-09-29 13:36 发布）。
- 是否稳定复现：是（能力判定确定性 false）。

## 根因分析

- 表象：glm 会话不联网、无工具。
- 真实根因：`data_glm.go` 快照 16 行全部缺 `SupportedToolsByProtocol` 字段；工具注入判定链 `resolveTools(modelOption.supportsTool("function_calling"))` 读矩阵任一协议声明，空矩阵恒 false。
- 非根因：glm 上游不支持 tools（不成立，智谱官方支持）；会话搜索绑定缺失（绑定缺失只影响「未绑定引导」路径，不影响工具定义注入——注入仅依赖主模型矩阵）。

## 修复方案（已实施）

- `data_glm.go` 16 个模型行全部补 `SupportedToolsByProtocol: toolsByProtocol([]string{"chat_completions"}, []string{"function_calling"})`（glm 无 hosted web_search，仅 function calling 通道；`toolsByProtocol` 在 chat_completions 键下仅放行 function_calling，与 deepseek 同款）。
- `pricing_golden_test.go` 补 glm 断言（glm-5.3-flash 矩阵 chat_completions=[function_calling]）。
- 能力数据经代码内静态快照读取链派生（BUG-0210/0229 同款边界），发布新 gateway 即全量生效，无需刷库；Redis 目录共享缓存此前已实证为空。
- 契约依据：`docs/functions/AI问答工具体系与主子模型设计.md` §6.4（静态快照升级口径），无需契约变更。
