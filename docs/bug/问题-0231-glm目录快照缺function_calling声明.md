# BUG-0231：glm 目录快照全系缺 function_calling 声明——glm 主模型会话工具体系整体不可用

## 基本信息

- 编号：BUG-0231
- 状态：已上线（随 09-29 14:0x~14:4x 系列发布）
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

## 全供应商盘点（2026-09-29，用户指令「其他供应商也需要看看」）

| 供应商 | 覆盖 | 结论 |
| --- | --- | --- |
| deepseek | 3/3 chat 行声明 | ✓ 无缺口 |
| glm | 16/16（本修复） | ✓ |
| anthropic | 构造器统一声明 `messages:[function_calling, code_execution]` | ✓ |
| gemini | 文本模型 9/9 传 supportedTools；缺的 1 个是 embedding 模型（不注入工具，口径内） | ✓ |
| xai | 文本构造器统一声明；image 模型（grok-imagine 系）不声明（口径内） | ✓ |
| openai | 45/60 声明；**缺 8 个旧 chat 模型**：gpt-4-turbo、gpt-4-turbo-2024-04-09、gpt-4-1106-preview、gpt-4、gpt-4-0613、gpt-3.5-turbo、gpt-3.5-turbo-0125、gpt-3.5-turbo-1106（官方均支持 function calling）；其余 7 个缺口为 gpt-image 系 image 模式（口径内） | **同批补齐**（8 行按各自 SupportedAPIProtocols 声明 function_calling，responses 协议下同仅 function_calling——旧模型无 hosted 搜索） |

- golden test 同步升级：glm 全系 + openai 全部 chat 模式行「必须声明 function_calling」断言（image/embedding 模式豁免），防止回归。
- 口径口径化：**chat 模式行必须声明 function_calling；image/embedding/audio 等非对话模式不声明**——本断言已固化在 golden test。

## 全列空值盘点（2026-09-29，用户指令「某些模型的某个列或者行是空的是否合理」）

反射审计全部六家快照的核心结构化列（价格/参数/协议/模态/工具/推理档/缓存），逐列判定：

**补全（不合理空，共三项，有内部佐证或公开 GA 事实）**：

1. **anthropic 构造器缺 `Mode: "chat"`**——生产 `provider_model_catalog` 中 anthropic 46 行 mode 列全空（唯一空值供应商，其余家全部有值）；空 Mode 无功能阻断（`isSupportedCatalogModel` 仅排除 audio 系）但属数据完整性缺口。构造器一处补齐即全覆盖。
2. **openai 家 46 行缺 `ReleaseDate`**——其余五家全部行都维护发布日期，仅 openai 大面积缺。补全依据三级：①名字自带 `YYYY-MM-DD` 后缀的 dated 行（19 行，名字即日期，完全佐证）；②同系列 dated 行佐证的主行（gpt-5.5→2026-04-23、gpt-5.4→2026-03-05 等 16 行）；③公开 GA 事实（gpt-4.1 系→2025-04-14、gpt-4o-mini→2024-07-18、o1→2024-12-05、o3→2025-04-16、o3-mini→2025-01-31、o4-mini→2025-04-16、gpt-4/gpt-3.5-turbo→2023-06-13、gpt-image-1→2025-04-23）。**不补**（无佐证）：gpt-image-1.5、gpt-image-1-mini、gpt-image-2.5-sunburst/flare、gpt-5.3-codex、o1-pro、o3-pro。
3. **gpt-4o-2024-05-13 缺 `CacheReadInputTokenCost` 与 `SupportsPromptCaching`**——gpt-4o 全系官方支持 prompt caching 且 cached=input×50%（主行 2.5→1.25 同款比例），dated 行按快照日价 $5/M 补 $2.5/M（不抄主行降后价）。

**判定为合理空（不补，列出判据）**：

| 列 | 范围 | 判据 |
| --- | --- | --- |
| MaxTokens | 全部供应商基本不维护 | 家内/跨家一致口径；openai 旧行保留历史值 |
| MaxInputTokens | openai/deepseek/glm/xai 全系 | 家内一致不维护（ContextWindow+MaxOutput 已表达） |
| MaxOutputTokens | xai 构造器不传 | 家内一致 |
| CacheRead / SupportsPromptCaching | gpt-4-turbo/4/3.5 旧行 | 官方不支持 prompt caching（2024-10 才上线），不支持即无缓存价 |
| CacheRead | o1-pro/o3-pro/gpt-*-pro | 无公开佐证，保守不编 |
| ReasoningEfforts | glm-4.5~5.1、claude-4-5 系、gemini-2.5 系、grok-4.20 系、o1-pro/o3-pro | 代际分界：这些代不支持 effort 枚举（thinking 开关型或预算型），快照对新一代维护、旧代不维护是有意边界 |
| SupportedToolsByProtocol | image/embedding 行 | 非对话模式不注入工具（口径） |
| ContextWindow/MaxOutput | gpt-image-1/1.5/2 旧行 | 按张计费的图像模型，快照未维护 token 参数（2.5 系新行有值属来源差异，无官方 token 参数佐证旧行数值） |

**防回归固化**：新增 `catalog_completeness_test.go` 三条门禁——①全供应商 chat 行必须声明 function_calling；②全部行 Mode 非空；③名字带 `YYYY-MM-DD` 后缀的行必须带同值 ReleaseDate。

2026-10-06 状态同步：随 09-29 14:0x~14:4x 系列发布（发布记录显式点名）；HEAD 复核「glm function_calling 静态派生断言（chain_catalog_builtin_static_derived_test）」命中。
