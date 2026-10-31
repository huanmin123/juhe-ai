/**
 * 演练场前端类型契约（docs/functions/演练场设计.md §3/§4）。
 * 演练场是模型能力试玩台：消息与产物只存在于浏览器内存，零落库零持久化。
 */

export type PlaygroundModelKind = 'chat' | 'image' | 'video' | 'audio'

/** 账户目录中的单个模型行（按目录协议行归一分类，见设计 §3.2）。 */
export interface PlaygroundModelOption {
  modelId: string
  kind: PlaygroundModelKind
  /** 模型目录声明 function_calling 时为 true，后端在该模型上默认注入 fetch_url 工具。 */
  functionCalling?: boolean
}

/** GET /playground/accounts 返回的账户行（仅 active 且启用、当前用户可见者）。 */
export interface PlaygroundAccount {
  accountId: string
  accountName: string
  providerCode: string
  models: PlaygroundModelOption[]
}

export interface PlaygroundAccountsResult {
  accounts: PlaygroundAccount[]
}

/** 一次性对话消息：全量上下文由客户端携带，服务端零状态（设计 §3.3）；工具上下文由服务端循环内部持有，客户端回放只发 user/assistant 正文。 */
export interface PlaygroundChatMessage {
  role: 'user' | 'assistant'
  content: string
}

export interface PlaygroundChatStreamRequest {
  accountId: string
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

/** 归一 SSE 事件（`data: <json>\n\n` 帧，与上游 chat chunk 解耦，设计 §3.3）。 */
export type PlaygroundStreamEvent =
  | { type: 'round'; round: number }
  | { type: 'delta'; text: string }
  /** tool_start.args 是模型产出的 arguments JSON 字符串（后端 sse.go wire 原文），展示层自行安全解析。 */
  | { type: 'tool_start'; callId: string; name: string; args?: string }
  | { type: 'tool_end'; callId: string; name: string; ok: boolean; summary?: string }
  | { type: 'usage'; promptTokens: number; completionTokens: number; totalTokens: number }
  | { type: 'done'; rounds: number }
  | { type: 'error'; code?: string; message: string }

export interface PlaygroundImageGenerationRequest {
  accountId: string
  modelId: string
  prompt: string
  size?: string
  /** 产物上限 n≤4。 */
  n?: number
  /** 图生图参考图（纯 base64，无 data: 前缀），走 /v1/images/edits。 */
  inputImageBase64?: string
}

export interface PlaygroundImageData {
  /** 内联 base64 dataUrl，零存储零新端点（设计 §3.4）。 */
  dataUrl: string
}

export interface PlaygroundImageResult {
  images: PlaygroundImageData[]
  model: string
}

export interface PlaygroundVideoCreateRequest {
  accountId: string
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
  accountId: string
  modelId: string
  /** ≤2000 字。 */
  input: string
  voice: string
  speed?: number
  format?: string
}

/** 演练场错误载荷:扁平 {"message","code"}(kernel messageCodePayload,与 chat 面同款),非嵌套形态。 */
export interface PlaygroundErrorBody {
  code?: string
  message?: string
}
