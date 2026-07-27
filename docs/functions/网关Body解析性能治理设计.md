# 网关 Body 解析性能治理设计

## 1. 目标

盘点请求接收、路由、账号适配、协议桥接、响应处理、SSE、审计、用量和运行日志中的 Body 解析，减少网关热路径上的重复 `JSON.parse`、重复 UTF-8 转换和纯日志目的结构扫描。

本轮范围仅限 Node 后端，不包含 Go 实现。

本设计不以“删除所有解析”为目标。协议转换、模型改写、usage 计费、精确客户端响应语义和用户显式响应检查依赖结构化数据，必须保留。治理原则是：

- 原生透传不解析完整 Body。
- 同一请求或响应最多完成一次完整 JSON 解析，后续消费者复用结果。
- 只为日志、审计展示生成结构摘要的 Body 解析直接删除。
- 大 Body 优先使用已有顶层元数据扫描；只有确定要改写或解释协议时才完整解析。
- generic 客户端保持上游语义不透明，不为错误分类或状态副作用解析正文。

## 2. 当前链路

| 阶段 | 当前行为 | 初步结论 |
| --- | --- | --- |
| 请求接收 | Express raw parser 读取字节；JSON 小于等于 256KB 时完整解析，大于阈值时只扫描顶层元数据 | raw 读取必须保留；小 Body 全量解析是否可改为统一按需解析，需要单独验证 |
| 请求元数据 | 提取 model、stream、service tier、reasoning、输出上限和生图提示 | 路由必需；优先复用扫描结果，不应重复完整解析 |
| 混合路由 | scoring、routing、quality inspection、quality repair 各自可能从 `rawBody` 解析 | 同请求应统一缓存；禁止各模块重复解析 |
| 账号与协议适配 | OAuth normalizer、模型映射、请求覆盖、OpenAI/Anthropic/Gemini bridge 在实际重建 Body 时解析 | 重建时必需；原生透传不应进入；跨账号重试应复用下游请求解析结果 |
| 非流式响应 | 响应检查、usage、错误对象、协议成功校验、usage fallback、Gemini interaction ID 可能分别解析同一正文 | 需要统一 parsed response context，解析一次后派生各项结果 |
| 流式响应 | 协议 inspector 按 SSE event 解析，响应检查 interceptor 也可能消费相同事件 | 协议语义必需；重点检查同一 event 是否被两套 parser 重复解析 |
| 原始审计 | 超限摘要为了展示顶层类型和 key，会解析或扫描 Body | 纯日志目的，删除结构解析，只保留 hash、大小、首尾字节和文本预览 |
| 使用记录 | 快照只做大小、深度和字段数约束 | 不做 Body JSON 解析，保持 |
| 运行日志索引 | worker/查询侧解析 JSONL 日志行 | 不在网关请求热路径，保持 |

## 3. 一次性交付范围与落地结果

本计划不再拆分第一、第二阶段。请求入口、账号准备、非流式响应、SSE、账户诊断、模型检测、OAuth、审计和运行日志中已经识别的重复或纯展示解析，必须在同一次验收中完成；无法删除的解析必须属于明确的 HTTP、协议、计费、持久化或 IPC 边界，并在终版清单中说明。

| 项目 | 结果 | 判定 |
| --- | --- | --- |
| 原始请求 JSON | 新增 request-scoped 解析结果与 in-flight Promise，Codex context/compact、OAuth、模型映射、API Key 兼容、hybrid、Gemini Interactions 和六种 bridge 统一复用 | 同一原始请求最多完整解析一次 |
| Body 改写 | `replaceGatewayJsonBody`、compact 合成请求和账号能力模型覆盖均清除旧 Promise/解析结果 | 必须保留失效边界 |
| 审计超限摘要 | 删除原始 Body 完整 JSON 解析和顶层 key 扫描 | 纯展示用途，直接删除 |
| 会话身份 | 会话请求类型删除 `body`；账户熔断和 Codex OAuth 不再从 Body 的 session/conversation/metadata 字段推导会话 | 只消费客户端专属 Header resolver 的结果 |
| Anthropic 原生请求 | 删除 driver 阶段的第一次同步解析/规范化，保留真正发送前基于最终 Header、URL 和 Body 的规范化 | 直接删除重复工作 |
| OpenAI OAuth 大请求 | 解析结果已跨账号尝试复用；继续把规范化和重新序列化移出主线程 | 本次完成 parsed-object worker 或等价结构化 builder |
| Codex 客户端画像 | 删除每次画像计算对 Body 的采样 SHA-256；该值只用于审计展示，不参与 state key | 纯诊断全量扫描，直接删除 |
| limited 后台账户探针 | 不再构造最终会被 limited 投影丢弃的展示 `responseBody` | 纯展示解析，直接删除 |
| 模型检测 | 删除聚合解析已经生成 `errorMessage` 后的第二次错误 fallback | 直接删除重复工作 |
| 账户请求覆盖 | 仍需处理已经过协议/账号改写的 Body | 本次通过结构化 builder 或 parsed-body 伴随值消除可避免的 stringify-parse，不能错用原始请求缓存 |
| 非流式成功响应 | 响应检查、usage、fallback、图片探测和协议成功证明按功能组合可对同一完整 JSON 解析 2～5 次 | 本次以最终客户端协议建立 attempt-local parsed context |
| 非 2xx 错误响应 | policy、usage、状态原因和最终错误可对同一 Body 解析 2～5 次 | 本次以真实上游协议建立 attempt-local error context |
| SSE | 协议 inspector 必须按 event 解析；启用响应策略/Codex guard 时 interceptor 可能再解析同一 event | 本次共享有界 SSE decoder，保留各协议状态机但不重复解码事件 |

## 4. 删除与保留判定

### 4.1 直接删除

- 审计 payload summary 的原始 Body JSON 解析。
- 审计 payload summary 的 JSON 前缀顶层 key 扫描。
- 任何仅用于写日志字段、但不影响业务结果的 Body 结构识别。

审计超限摘要继续保留原始 SHA-256、原始大小、content type、首尾窗口、遗漏字节数和文本预览。

### 4.2 改为一次解析、多方复用

- 大 JSON 下游请求完整解析。
- 非流式完整 JSON 上游响应解析。
- 同一 SSE event 的协议分类、usage、错误和响应检查。

缓存必须绑定单个 Express request 或单次 upstream attempt，Body 被改写时必须失效；不能跨请求、跨租户或跨账号保存解析对象。

### 4.3 必须保留

- raw Body 字节读取、请求大小和在途容量限制。
- 协议 bridge 对实际重建请求的结构化解析。
- 模型映射和账户请求覆盖实际启用时的结构化解析。
- usage 计费所需字段提取。
- Codex、Claude Code、Gemini CLI 精确画像的协议完成、错误和重试语义。
- 用户显式开启的响应检查和混合质量检查。

## 5. 实施顺序

1. 删除审计摘要中的 Body JSON 解析与前缀 key 扫描。
2. 为下游请求增加 request-scoped 完整 JSON 解析结果与 in-flight Promise 复用。
3. 将混合路由、OAuth、模型映射和协议 bridge 迁移到统一请求解析入口。
4. 为非流式上游响应建立单次解析结果，复用到响应检查、usage、错误、成功校验和 interaction ID。
5. 审计 SSE inspector 与响应检查 interceptor，确认是否可以共享同一事件解析结果。
6. 最后评估小于等于 256KB 请求是否从“入口立即完整解析”调整为“顶层扫描 + 按需完整解析”。

## 6. 验收

- 原生 API Key raw passthrough 不新增完整 Body 解析。
- 同一大 JSON 请求即使发生混合评分、协议重建和多账号重试，也只完整解析一次下游 Body。
- 同一完整非流式 JSON 响应不被 usage、错误和协议校验重复解析。
- 审计、usage 和运行日志不为展示目的解析原始模型 Body。
- Body 改写后缓存失效，后续上游请求使用新 Body。
- 现有协议转换、计费、响应检查、错误处理和审计保全回归保持通过。
