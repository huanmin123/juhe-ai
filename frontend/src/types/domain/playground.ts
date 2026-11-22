/**
 * 演练场前端类型契约（docs/functions/演练场设计.md §3/§4）。
 * 演练场是模型能力试玩台：对话/向量/重排零持久化；语音/视频产物引用由服务端 24h 暂存
 * （§3.8 展示缓存，覆盖式替换、过期即删）；图像生成 v1.2 起为异步任务——任务记录落库长期
 * 保留、结果图与参考图按任务保留 7 天到期删字节（§3.4/§3.8），历史走 GET /images/jobs；
 * v1.3 起工作台四参数必选（固定默认 16:9/1K/标准/PNG）且演练场 UI 不展示 tokens/费用。
 * 2026-11-22 演练场 Key 化：目录改「先选 API Key 后选模型」两级联动（GET /playground/key-models），
 * 六域请求载荷 accountId → apiKeyId，SSE 归一事件只剩 delta/usage/error；v1.3「不展示费用」裁决继续有效。
 */

/** 六值模型分类（设计 §4.5 v1.1）：chat/image/video/audio 四类 + embedding/rerank 向量域两值。 */
export type PlaygroundModelKind = 'chat' | 'image' | 'video' | 'audio' | 'embedding' | 'rerank'

/** Key→模型目录中的单个模型行（GET /playground/key-models 按所选 Key 返回，行已按目录协议归一分类）。v1.3「演练场不展示费用」裁决：不含 imageEstimate 计价投影。 */
export interface PlaygroundModelOption {
  modelId: string
  kind: PlaygroundModelKind
  providerCode: string
}

/** GET /playground/key-models 响应：所选 Key 可路由的模型行（已按 kind 归一），信封同 playground 其余 JSON 端点。 */
export interface PlaygroundKeyModelsResult {
  models: PlaygroundModelOption[]
}

/** 一次性对话消息：全量上下文由客户端携带，服务端零状态（设计 §3.3）；工具上下文由服务端循环内部持有，客户端回放只发 user/assistant 正文。 */
export interface PlaygroundChatMessage {
  role: 'user' | 'assistant'
  content: string
}

export interface PlaygroundChatStreamRequest {
  apiKeyId: string
  modelId: string
  messages: PlaygroundChatMessage[]
  system?: string
  temperature?: number
  maxTokens?: number | null
  /** 后端仅支持 stream: true（false 直接 400），前端恒传 true。 */
  stream: true
  topP?: number
  frequencyPenalty?: number
  presencePenalty?: number
}

/**
 * 归一 SSE 事件（`data: <json>\n\n` 帧，与上游 chat chunk 解耦，设计 §3.3）。
 * 工具循环退役后只余三值：delta 正文增量、usage 用量、error 归一错误；流关闭即完成（无 done 事件）。
 */
export type PlaygroundStreamEvent =
  | { type: 'delta'; text: string }
  | { type: 'usage'; promptTokens: number; completionTokens: number; totalTokens: number }
  | { type: 'error'; code?: string; message: string }

/** 图像任务状态（设计 §3.4）：任务行四态；`expired` 不落库,为读时派生态(completed 且图像已过期),仅作为历史列表筛选词表值。 */
export type PlaygroundImageJobStatus = 'queued' | 'running' | 'completed' | 'failed'

/** 历史列表筛选状态词表（设计 §3.4.3）：任务四态外含两个派生态——`active` = queued+running（「生成中」筛选项一次覆盖两种在途状态），`expired` = completed 且图像已过期。 */
export type PlaygroundImageJobStatusFilter = PlaygroundImageJobStatus | 'active' | 'expired'

/** 图像任务用量（设计 §3.4.4）：仅 completed 有值,上游 usage 宽容解析、缺省 0。 */
export interface PlaygroundImageUsage {
  inputTokens: number
  outputTokens: number
  totalTokens: number
}

/** 图像任务产物引用（结果图/参考图同形）：url 为同域内容地址,api 域归一后 <img> 展示与 a[download] 下载直接可用。 */
export interface PlaygroundImageArtifactRef {
  id: string
  url: string
}

/** 图像任务参数词表（设计 §3.4.2）：API 层四项可省略（透传上游默认）;v1.3 起工作台 UI 必选其一（固定默认）恒传。比例 × 分辨率为近似映射,实际 size 以上游返回为准。 */
export type PlaygroundImageAspectRatio = '1:1' | '16:9' | '9:16'
export type PlaygroundImageResolution = '1k' | '2k' | '4k'
export type PlaygroundImageQuality = 'standard' | 'fine'
export type PlaygroundImageOutputFormat = 'png' | 'jpeg' | 'webp'

/** POST /playground/images/jobs 创建请求（设计 §3.4.2）：单次恒 1 张,无 n 参数。 */
export interface PlaygroundImageJobCreateRequest {
  apiKeyId: string
  modelId: string
  prompt: string
  aspectRatio?: PlaygroundImageAspectRatio
  resolution?: PlaygroundImageResolution
  quality?: PlaygroundImageQuality
  outputFormat?: PlaygroundImageOutputFormat
  /** 0~10 条,须为本人未删参考图（§3.4.1 上传返回的 id）。 */
  referenceIds: string[]
}

/** POST /playground/images/jobs 响应：落库 queued 后立即返回,无队列满之外的等待。 */
export interface PlaygroundImageJobCreateResult {
  jobId: string
  status: 'queued'
}

/** GET /playground/images/jobs 查询参数（设计 §3.4.3）：limit 默认 24、≤100;before 为上一页 nextCursor（createdAt UnixMilli）。2026-11-22 Key 化：历史筛选 accountId → apiKeyId（任务行记录创建时的 Key）。 */
export interface PlaygroundImageJobsParams {
  keyword?: string
  apiKeyId?: string
  status?: PlaygroundImageJobStatusFilter
  quality?: PlaygroundImageQuality
  limit?: number
  before?: number
}

/** 图像任务卡片（设计 §3.4.4,列表与详情同形）：createdAt/completedAt/expiresAt 为 unix 秒。2026-11-22 Key 化（v1.4）：归属改创建时 Key（后端记 params.apiKeyId 投影到卡片行；account_id 为历史列写空串）。 */
export interface PlaygroundImageJob {
  jobId: string
  status: PlaygroundImageJobStatus
  prompt: string
  model: string
  apiKeyId: string
  aspectRatio?: PlaygroundImageAspectRatio
  resolution?: PlaygroundImageResolution
  quality?: PlaygroundImageQuality
  outputFormat?: PlaygroundImageOutputFormat
  /** 上游实际 size（"W×H"）;比例×分辨率为近似映射,卡片标签以此实际值为准。 */
  size?: string
  referenceCount: number
  /** completed 且未删时给出;过期或失败为空数组。 */
  references: PlaygroundImageArtifactRef[]
  images: PlaygroundImageArtifactRef[]
  /** 读时派生（expiresAt 已过 × 清理标记取或）：展示恒收敛,与字节删除进度无关。 */
  imageDeleted: boolean
  /** 后端恒输出（非 completed 为 null）;值语义见设计 §3.4.4。v1.3 起演练场 UI 不展示,字段保留（API 形状不变）。 */
  usage: PlaygroundImageUsage | null
  /** 仅 completed 有值;null 表示目录无价。v1.3 起演练场 UI 不展示,字段保留（API 形状不变）。 */
  costUsd: number | null
  error: string
  createdAt: number
  /** 后端恒输出数字（未完成为 0）;unix 秒。 */
  completedAt: number
  /** 仅 completed 非删时有值（完成时间 + 7d）,否则 0;unix 秒。 */
  expiresAt: number
}

/** GET /playground/images/jobs 响应：按创建时间倒序;nextCursor 缺省 = 无更多。 */
export interface PlaygroundImageJobsPage {
  jobs: PlaygroundImageJob[]
  nextCursor?: number
}

/** 状态角标词表（设计 §4.2）：queued/running 合并显示「生成中」;imageDeleted 派生态由展示层覆盖为「已删除图像」。 */
export const playgroundImageJobStatusLabels: Record<PlaygroundImageJobStatus, string> = {
  queued: '生成中',
  running: '生成中',
  completed: '已完成',
  failed: '失败'
}

/** 状态角标样式映射（视频页 playground-status 徽章配色）：pending→accent、completed→ok、failed→danger;imageDeleted 用基类中性灰。 */
export const playgroundImageJobStatusClass: Record<PlaygroundImageJobStatus, string> = {
  queued: 'is-running',
  running: 'is-running',
  completed: 'is-completed',
  failed: 'is-failed'
}

/** 生成质量中文名（设计 §4.2 参数标签与筛选词表）。 */
export const playgroundImageQualityLabels: Record<PlaygroundImageQuality, string> = {
  standard: '标准',
  fine: '精细'
}

export interface PlaygroundVideoCreateRequest {
  apiKeyId: string
  modelId: string
  prompt: string
  size?: string
  seconds?: number
  inputImageBase64?: string
}

export interface PlaygroundVideoCreateResult {
  /** jobId 由前端持有，服务端不落库（设计 §3.5）。 */
  jobId: string
}

export type PlaygroundVideoStatus = 'queued' | 'running' | 'completed' | 'failed' | 'cancelled'

export interface PlaygroundVideoPollResult {
  status: PlaygroundVideoStatus
  progress?: number
  /** 归一任务错误对象（后端 videosPollError：code/message 原样透出；未知状态原文在 message）。 */
  error?: PlaygroundVideoPollError
}

export interface PlaygroundVideoPollError {
  code?: string
  message?: string
}

export interface PlaygroundSpeechRequest {
  apiKeyId: string
  modelId: string
  /** ≤2000 字。 */
  input: string
  voice: string
  speed?: number
  format?: string
}

/** POST /playground/speech 响应：音频落服务端暂存（设计 §3.6/§3.8），响应为 JSON 引用而非字节流。 */
export interface PlaygroundSpeechResult {
  id: string
  url: string
  mimeType: string
}

/** 演练场向量域用量行（设计 §3.9/§3.10 归一形态）：无 completionTokens（非生成式）。 */
export interface PlaygroundUsage {
  promptTokens: number
  totalTokens: number
}

/** POST /playground/embeddings 请求（设计 §3.9）：前端恒发字符串数组（多行 textarea 按行拆分、空行跳过）。 */
export interface PlaygroundEmbeddingsRequest {
  apiKeyId: string
  modelId: string
  input: string[]
  /** 可选正整数，透传上游。 */
  dimensions?: number
}

/** 单条向量结果（设计 §3.9）：完整向量随响应回传，相似度矩阵由前端本地计算（§4.3）。 */
export interface PlaygroundEmbeddingItem {
  /** 请求 input[] 下标。 */
  index: number
  dimensions: number
  vector: number[]
}

/** POST /playground/embeddings 响应（设计 §3.9）：纯 JSON 结果随响应呈现，无产物落盘。 */
export interface PlaygroundEmbeddingsResult {
  model: string
  usage?: PlaygroundUsage
  items: PlaygroundEmbeddingItem[]
}

/** POST /playground/rerank 请求（设计 §3.10）：documents 恒字符串形态（1~20 条）。 */
export interface PlaygroundRerankRequest {
  apiKeyId: string
  modelId: string
  query: string
  documents: string[]
  /** 可选正整数。 */
  topN?: number
  /** 可选布尔，默认 true（回显文档便于前端对照）。 */
  returnDocuments?: boolean
}

/** 单条重排结果（设计 §3.10）：index 是请求 documents[] 下标；returnDocuments=false 时无 document。 */
export interface PlaygroundRerankResultItem {
  index: number
  relevanceScore: number
  document?: { text: string }
}

/** POST /playground/rerank 响应（设计 §3.10）：results 按分数降序，纯 JSON 无产物落盘。 */
export interface PlaygroundRerankResult {
  results: PlaygroundRerankResultItem[]
  usage?: PlaygroundUsage
}

/** GET /playground/artifacts 暂存清单中的语音行：最近一条合成，无则 null（设计 §3.8）；createdAt 为 unix 秒。 */
export interface PlaygroundArtifactSpeech {
  id: string
  url: string
  mimeType: string
  createdAt: number
}

/** 视频任务引用（设计 §3.5/§3.8）：产物本体不落盘，凭 jobId 走既有轮询与内容代理；createdAt 为 unix 秒。2026-11-22 Key 化：携带创建时的 apiKeyId，刷新恢复后轮询/下载仍传该 Key。 */
export interface PlaygroundArtifactVideo {
  jobId: string
  apiKeyId: string
  modelId: string
  status: PlaygroundVideoStatus
  createdAt: number
}

/** GET /playground/artifacts 响应：当前登录用户暂存清单（设计 §3.8）——v1.2 起移除 images 字段，图片历史改走 GET /images/jobs（§3.4.3）。 */
export interface PlaygroundArtifacts {
  speech: PlaygroundArtifactSpeech | null
  video: PlaygroundArtifactVideo | null
}

/** 演练场错误载荷:扁平 {"message","code"}(kernel messageCodePayload,与 chat 面同款),非嵌套形态。 */
export interface PlaygroundErrorBody {
  code?: string
  message?: string
}
