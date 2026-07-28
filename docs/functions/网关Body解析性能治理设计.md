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
| 请求接收 | Express raw parser 读取字节；所有普通网关 JSON 均执行受 Body 上限约束的严格语法和元数据扫描，不构建完整对象 | raw 读取和语法校验必须保留；小 Body 与大 Body 均只在业务消费者需要改写或解释协议时按需完整解析 |
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
| 原始请求 JSON | 普通网关入口统一写入 `scanned_json` 元数据状态并保留 raw Buffer；新增 request-scoped 解析结果与 in-flight Promise，并把成功物化的对象绑定回该原始 Buffer，Codex context/compact、OAuth、模型映射、API Key 兼容、hybrid、账户覆盖、Gemini Interactions 和六种 bridge 统一复用 | 原生 API Key 透传完整解析 0 次；真正需要结构化 Body 时同一原始请求最多完整解析一次 |
| Body 改写 | `replaceGatewayJsonBody`、compact 合成请求和账号能力模型覆盖均清除旧 Promise/解析结果 | 必须保留失效边界 |
| 审计超限摘要 | 删除原始 Body 完整 JSON 解析和顶层 key 扫描 | 纯展示用途，直接删除 |
| 会话身份 | 会话请求类型删除 `body`；账户熔断和 Codex OAuth 不再从 Body 的 session/conversation/metadata 字段推导会话 | 只消费客户端专属 Header resolver 的结果 |
| Anthropic 原生请求 | 删除 driver 阶段的第一次同步解析/规范化，保留真正发送前基于最终 Header、URL 和 Body 的规范化 | 直接删除重复工作 |
| Codex 客户端画像 | 删除每次画像计算对 Body 的采样 SHA-256；该值只用于审计展示，不参与 state key | 纯诊断全量扫描，直接删除 |
| limited 后台账户探针 | 不再构造最终会被 limited 投影丢弃的展示 `responseBody` | 纯展示解析，直接删除 |
| 模型检测 | 删除聚合解析已经生成 `errorMessage` 后的第二次错误 fallback | 直接删除重复工作 |
| OpenAI OAuth 大请求 | 原始请求按 request-scoped materializer 完整解析一次；大对象规范化和序列化进入 worker；等价账号重试按原 Body、身份、Header、覆盖值和能力建立请求级 Promise 缓存 | 多账号重试不重复完整解析，也不重复 structured clone 等价规范化输入；账号差异进入缓存键 |
| 账户请求覆盖 | 协议 bridge、模型映射、Gemini Interactions 和 Code Assist 生成的 Buffer 绑定对应结构对象；无实际生效覆盖时不读取 Body | 已知结构化 Body 不再 stringify 后重新 parse；Body 改写仍按新对象重新绑定，不能错用原始请求缓存 |
| 非流式成功响应 | 以最终客户端协议建立 attempt-local `GatewayNonStreamJsonBody`，响应检查、usage、fallback、图片探测、协议成功证明、Codex repair 和 Gemini affinity 共享 | 同一完整成功正文最多完整解析一次 |
| 非 2xx 错误响应 | 以真实上游协议建立 failure context，并把 parsed response 随 attempt 传递给最终诊断错误；策略、usage、摘要、账号副作用和最终错误共享，显式区分“已解析但无摘要” | 有策略路径最多完整解析一次；generic opaque 无策略路径完整解析 0 次，最终确需诊断时也只解析一次 |
| SSE | 响应策略 interceptor、Codex guard 和 OpenAI/Anthropic/Gemini inspector 共享同一 parsed event；内存网关把已解析 event 和 inspection 直接交给账户诊断、模型检测 | 同一 SSE event 只解码一次；诊断 event 缓存受 256 KiB 预览上限约束 |
| 账户诊断与模型检测 | JSON、SSE、错误、输出、模型、usage 和完成证据统一消费 `DiagnosticResponseContext`；非流式与流式均复用内存网关解析结果 | 不再对完整诊断正文重复解析；图片响应只做有界 envelope 扫描，不物化 base64 |
| Hybrid 辅助响应 | dispatch 一次解析并把 parsed value 同时交给 usage、评分和质量检查；错误摘要复用内部网关发布的非流式 context | 同一辅助响应最多完整解析一次 |
| 审计与运行日志 | 审计传输按精确字节账本裁剪，最终 Redis codec 只整体编码一次；预处理 IPC 不再二次裁剪；公开接口日志和操作日志删除纯展示反解析 | 原始模型 Body 不为日志展示解析；JSONL、Redis、DB 和 IPC 的边界编解码保留 |

## 4. 删除与保留判定

### 4.1 直接删除

- 审计 payload summary 的原始 Body JSON 解析。
- 审计 payload summary 的 JSON 前缀顶层 key 扫描。
- 任何仅用于写日志字段、但不影响业务结果的 Body 结构识别。

审计超限摘要继续保留原始 SHA-256、原始大小、content type、首尾窗口、遗漏字节数和文本预览。

### 4.2 改为一次解析、多方复用

- 下游请求按需完整解析。
- 非流式完整 JSON 上游响应解析。
- 同一 SSE event 的协议分类、usage、错误和响应检查。
- 账户测试、健康探针和模型检测对内存网关 JSON/SSE 解析结果的复用。
- Hybrid 辅助响应对 usage、评分、质量检查和错误摘要的复用。

缓存必须绑定单个 Express request 或单次 upstream attempt，Body 被改写时必须失效；不能跨请求或跨租户保存解析对象。单请求内跨账号复用时，必须把所有账号敏感输入纳入缓存键。

### 4.3 必须保留

- raw Body 字节读取、请求大小和在途容量限制。
- 协议 bridge 对实际重建请求的结构化解析。
- 模型映射和账户请求覆盖实际启用时的结构化解析。
- usage 计费所需字段提取。
- Codex、Claude Code、Gemini CLI 精确画像的协议完成、错误和重试语义。
- 用户显式开启的响应检查和混合质量检查。
- 跨协议 transformer 对源协议文档的解析，以及生成目标协议后由目标协议状态机执行的解析。二者是不同 wire document，不属于同一 Body 的重复解析。
- OAuth/token、余额、图片执行器等独立 HTTP 客户端对各自响应的单次协议解析。
- Redis、DB、IPC、JSONL、加密状态和 usage spool 的序列化边界解析。
- `json-metadata-scanner` 对单个 JSON string token 的反转义；它不构建或解析完整 Body。
- 审计裁剪器对自身已经生成的 summary 对象进行缩容；它不解析原始模型 Body。

## 5. 实施顺序

1. 删除审计摘要中的 Body JSON 解析与前缀 key 扫描。
2. 为下游请求增加 request-scoped 完整 JSON 解析结果与 in-flight Promise 复用。
3. 将混合路由、OAuth、模型映射和协议 bridge 迁移到统一请求解析入口。
4. 为非流式上游响应建立单次解析结果，复用到响应检查、usage、错误、成功校验和 interaction ID。
5. 审计 SSE inspector 与响应检查 interceptor，确认是否可以共享同一事件解析结果。
6. 小于等于 256KB 请求已从入口立即完整解析改为单遍严格语法/元数据扫描和按需完整解析；扫描器只解码顶层目标键与嵌套 `type`，大请求继续在 worker 扫描。

该方案优化的是完整对象分配、GC 压力和重复解析次数，不假设 JavaScript 严格扫描的 CPU 必然低于原生 `JSON.parse`。当前本机典型 messages 基准中，约 11KB/112KB 请求的扫描耗时约为 0.024ms/0.108ms。严格扫描器使用每层 1 byte 的紧凑栈；8MB、400 万层构造输入实测堆增量约 8MB、ArrayBuffer 增量约 16MB，不再出现对象栈约 30 倍的内存放大。运行期仍应持续观测事件循环 CPU、吞吐和 GC，再按事实调整内联扫描阈值。

## 6. 验收

- 原生 API Key raw passthrough 不新增完整 Body 解析。
- 小 JSON 原生 API Key raw passthrough 的完整 Body 物化次数为 0，按需消费者跨模块复用时为 1。
- 同一大 JSON 请求即使发生混合评分、协议重建和多账号重试，也只完整解析一次下游 Body。
- 同一完整非流式 JSON 响应不被 usage、错误和协议校验重复解析。
- 审计、usage 和运行日志不为展示目的解析原始模型 Body。
- Body 改写后缓存失效，后续上游请求使用新 Body。
- 现有协议转换、计费、响应检查、错误处理和审计保全回归保持通过。

## 7. 最终解析边界清单

| 边界 | 完整解析次数 | 说明 |
| --- | ---: | --- |
| 原生 API Key 请求透传 | 0 | 只做严格语法与路由元数据扫描，原 Buffer 直接上游发送 |
| 需要改写的原始请求 | 每个 `rawBody` 1 | request-scoped singleflight 缓存成功或 terminal failure；局部 waiter 取消不取消共享任务 |
| 等价 OAuth 账号重试 | 原始解析 1、规范化 1 | 大对象 clone/遍历/stringify 在 worker；账号差异由请求级缓存键隔离 |
| 非流式成功响应 | 每次 attempt 1 | 按最终客户端协议解释 |
| 非流式失败响应 | 每次 attempt 0 或 1 | generic opaque 无策略为 0；需要策略/副作用时按真实上游协议为 1 |
| SSE | 每个完整 event 1 | interceptor、guard、协议 inspector、账户诊断和模型检测共享 parsed event |
| 图片账户诊断 | 完整解析 0 | 有界 envelope scanner 识别成功、错误和缺失结果，不复制 base64 |
| 审计/公开日志/操作日志 | 原始模型 Body 0 | 只保留字节、hash、窗口和必要传输编码 |
| 协议转换与持久化 | 每个独立边界 1 | 源/目标 wire document、HTTP、Redis、DB、IPC、JSONL 分别属于独立正确性边界 |
