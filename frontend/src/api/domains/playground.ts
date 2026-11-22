import type {
  PlaygroundArtifacts,
  PlaygroundChatStreamRequest,
  PlaygroundEmbeddingsRequest,
  PlaygroundEmbeddingsResult,
  PlaygroundErrorBody,
  PlaygroundImageArtifactRef,
  PlaygroundImageJob,
  PlaygroundImageJobCreateRequest,
  PlaygroundImageJobCreateResult,
  PlaygroundImageJobsPage,
  PlaygroundImageJobsParams,
  PlaygroundKeyModelsResult,
  PlaygroundModelOption,
  PlaygroundRerankRequest,
  PlaygroundRerankResult,
  PlaygroundSpeechRequest,
  PlaygroundSpeechResult,
  PlaygroundStreamEvent,
  PlaygroundVideoCreateRequest,
  PlaygroundVideoCreateResult,
  PlaygroundVideoPollResult
} from '@/types/domain/playground'
import { apiUrl, http, readFetchErrorMessage, unwrap } from '../http'

/**
 * 演练场 API 域（docs/functions/演练场设计.md §3）。
 * 鉴权走会话 Cookie（http credentials: include / fetch credentials: 'include'）；
 * 错误体为扁平 {"message","code"}（kernel messageCodePayload，与 chat 面同款）。
 */
export const playgroundApi = {
  /** 所选 Key 的模型目录（先选 Key 后选模型，六页共用；前端按 apiKeyId 在内存缓存，会话内一次）。 */
  keyModels: async (apiKeyId: string): Promise<PlaygroundModelOption[]> => {
    const result = await unwrap<PlaygroundKeyModelsResult>(http.get('/playground/key-models', { params: { apiKeyId } }))
    return result.models
  },
  /** 当前登录用户暂存清单（设计 §3.8）：v1.2 起移除 images 字段（图片历史改走 imagesJobs），刷新后凭此恢复语音播放器与视频任务轮询。 */
  artifacts: async (): Promise<PlaygroundArtifacts> => {
    const result = await unwrap<PlaygroundArtifacts>(http.get(`/playground/artifacts`))
    return {
      speech: result.speech ? withContentUrl(result.speech) : null,
      video: result.video
    }
  },
  /** 上传参考图（设计 §3.4.1）：纯 base64（无 data: 前缀），落暂存孤儿态，24h 内未绑定任务由懒清理删除。 */
  imagesReferences: async (payload: { dataBase64: string }): Promise<PlaygroundImageArtifactRef> => {
    const result = await unwrap<PlaygroundImageArtifactRef>(http.post('/playground/images/references', payload))
    return withContentUrl(result)
  },
  /** 创建异步生成任务（设计 §3.4.2）：落库 queued 后立即返回，普通超时即可（无需 timeout:0）。 */
  imagesCreateJob: (payload: PlaygroundImageJobCreateRequest): Promise<PlaygroundImageJobCreateResult> =>
    unwrap<PlaygroundImageJobCreateResult>(http.post('/playground/images/jobs', payload)),
  /** 生成历史列表（设计 §3.4.3）：按创建时间倒序，nextCursor 缺省 = 无更多。 */
  imagesJobs: async (params: PlaygroundImageJobsParams = {}): Promise<PlaygroundImageJobsPage> => {
    const result = await unwrap<PlaygroundImageJobsPage>(http.get('/playground/images/jobs', { params }))
    return { jobs: result.jobs.map(withJobContentUrls), nextCursor: result.nextCursor }
  },
  /** 任务详情（设计 §3.4.4）：列表与详情同形卡片，前端对 queued/running 卡片 3s 轮询。 */
  imagesJob: async (jobId: string): Promise<PlaygroundImageJob> => {
    const job = await unwrap<PlaygroundImageJob>(http.get(`/playground/images/jobs/${encodeURIComponent(jobId)}`))
    return withJobContentUrls(job)
  },
  /** 手动删除任务（设计 §3.4.6，v1.5）：任务记录行与产物字节一并删除，仅终态可删；成功 204 无响应体，不 unwrap。 */
  imagesJobRemove: (jobId: string) => http.delete(`/playground/images/jobs/${encodeURIComponent(jobId)}`),
  /** 创建异步视频任务；任务引用随暂存区记录（设计 §3.5/§3.8），jobId 由前端持有。 */
  videosCreate: (payload: PlaygroundVideoCreateRequest) => unwrap<PlaygroundVideoCreateResult>(http.post('/playground/videos', payload, { timeout: 0 })),
  /** 代理上游轮询并归一状态；网关重启后凭前端持有的 jobId 仍可轮询（传创建时记住的 Key）。 */
  videosPoll: (jobId: string, apiKeyId: string) => unwrap<PlaygroundVideoPollResult>(http.get(`/playground/videos/${encodeURIComponent(jobId)}`, { params: { apiKeyId } })),
  /** 流式代理视频字节并转为 blob URL 供 <video> 播放；调用方负责 URL.revokeObjectURL（传创建时记住的 Key）。 */
  videosContent: async (jobId: string, apiKeyId: string): Promise<string> => {
    const blob = await playgroundBlob(`/playground/videos/${encodeURIComponent(jobId)}/content?apiKeyId=${encodeURIComponent(apiKeyId)}`)
    return URL.createObjectURL(blob)
  },
  /** 同步语音合成：音频落服务端暂存（设计 §3.6/§3.8），响应 JSON 引用 {id,url,mimeType}；合成耗时可能超默认 15s，禁用 axios 超时。 */
  speech: async (payload: PlaygroundSpeechRequest): Promise<PlaygroundSpeechResult> => {
    const result = await unwrap<PlaygroundSpeechResult>(http.post(`/playground/speech`, payload, { timeout: 0 }))
    return withContentUrl(result)
  },
  /** 同步向量生成（设计 §3.9）：完整向量随响应回传（相似度矩阵由前端本地计算，§4.3），纯 JSON 无产物落盘（响应无 url，不走 withContentUrl）；耗时可能超默认 15s，禁用 axios 超时。 */
  embeddings: (payload: PlaygroundEmbeddingsRequest) => unwrap<PlaygroundEmbeddingsResult>(http.post('/playground/embeddings', payload, { timeout: 0 })),
  /** 同步重排（设计 §3.10）：results 按分数降序、index 为请求 documents 下标，纯 JSON 无产物落盘（响应无 url，不走 withContentUrl）；禁用 axios 超时。 */
  rerank: (payload: PlaygroundRerankRequest) => unwrap<PlaygroundRerankResult>(http.post('/playground/rerank', payload, { timeout: 0 })),
  /** 一次性对话（SSE）。事件经 onEvent 逐帧回调；error 事件同样以事件透出，由页面渲染错误条。 */
  chatStream: async (input: PlaygroundChatStreamRequest & {
    signal?: AbortSignal
    onEvent: (event: PlaygroundStreamEvent) => void
  }): Promise<void> => {
    const response = await fetch(apiUrl(`/playground/chat/stream`), {
      method: 'POST',
      credentials: 'include',
      headers: { 'content-type': 'application/json', accept: 'text/event-stream' },
      body: JSON.stringify({
        apiKeyId: input.apiKeyId,
        modelId: input.modelId,
        messages: input.messages,
        system: input.system,
        temperature: input.temperature,
        maxTokens: input.maxTokens,
        stream: true,
        topP: input.topP,
        frequencyPenalty: input.frequencyPenalty,
        presencePenalty: input.presencePenalty
      }),
      signal: input.signal
    })
    await consumePlaygroundSseResponse(response, `/playground/chat/stream`, input.onEvent)
  }
}

/**
 * 暂存产物取流地址归一：后端契约字面量为相对 playground 基路径（`/playground/artifacts/{id}/content`），
 * 在 api 域边界统一拼 http 实例 baseURL（与 apiUrl 同源；apiUrl 不接受动态实参——api-contract
 * 检查器将独立 apiUrl 调用点按静态 GET 端点解析），视图 `<img>/<audio>/a[download]` 直接可用（同域自动带会话 Cookie）。
 */
function withContentUrl<T extends { url: string }>(entry: T): T {
  return { ...entry, url: `${http.defaults.baseURL ?? '/__aisys__/api'}${entry.url}` }
}

/** 图像任务卡片 references/images 的内容地址归一（同 withContentUrl 口径；失败/过期为空数组，天然跳过）。 */
function withJobContentUrls(job: PlaygroundImageJob): PlaygroundImageJob {
  return { ...job, references: job.references.map(withContentUrl), images: job.images.map(withContentUrl) }
}

/** 视频内容代理专用（图片/语音已改为服务端暂存 url 直引）：会话 Cookie 鉴权，错误响应按信封 JSON 解出 message 抛出。 */
async function playgroundBlob(path: string, init?: RequestInit): Promise<Blob> {
  const response = await fetch(apiUrl(path), { credentials: 'include', ...init })
  if (!response.ok) {
    throw new Error(await readFetchErrorMessage(response, path))
  }
  return response.blob()
}

export class PlaygroundStreamHttpError extends Error {
  constructor(
    public readonly status: number,
    public readonly code: string | undefined,
    message: string
  ) {
    super(message)
    this.name = 'PlaygroundStreamHttpError'
  }
}

export class PlaygroundStreamProtocolError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'PlaygroundStreamProtocolError'
  }
}

/**
 * 消费演练场 SSE 响应：`data: <json>\n\n` 分帧（兼容 CRLF），与上游 chat chunk 解耦。
 * 导出供单测与回归脚本直接驱动（构造 Response 形对象回放多事件、error 帧与跨 chunk 分帧）。
 */
export async function consumePlaygroundSseResponse(response: Response, path: string, onEvent: (event: PlaygroundStreamEvent) => void): Promise<void> {
  if (!response.ok || !response.body) {
    throw await playgroundStreamHttpError(response, path)
  }
  const reader = response.body.getReader()
  const decoder = new TextDecoder('utf-8', { fatal: true })
  let buffer = ''
  let completed = false
  try {
    while (true) {
      const next = await reader.read()
      if (next.done) {
        completed = true
        break
      }
      buffer += decoder.decode(next.value, { stream: true })
      let boundary = findSseBoundary(buffer)
      while (boundary) {
        const frame = buffer.slice(0, boundary.index)
        buffer = buffer.slice(boundary.index + boundary.length)
        consumePlaygroundSseFrame(frame, onEvent)
        boundary = findSseBoundary(buffer)
      }
    }
    buffer += decoder.decode()
    consumePlaygroundSseFrame(buffer, onEvent)
  } finally {
    if (!completed) await reader.cancel().catch(() => undefined)
    reader.releaseLock()
  }
}

/** 解析单个 SSE 帧（可含多行 data:，按 SSE 规范以换行拼接）；无 data 行返回 null，JSON 非法时抛出原错误。 */
export function parsePlaygroundSseData(frame: string): PlaygroundStreamEvent | null {
  const dataLines = frame
    .split(/\r?\n/)
    .filter((line) => line.startsWith('data:'))
    .map((line) => line.slice('data:'.length).trim())
  if (!dataLines.length) return null
  const parsed = JSON.parse(dataLines.join('\n')) as PlaygroundStreamEvent
  if (typeof parsed.type !== 'string') {
    throw new PlaygroundStreamProtocolError('演练场流事件缺少 type 字段')
  }
  return parsed
}

function consumePlaygroundSseFrame(frame: string, onEvent: (event: PlaygroundStreamEvent) => void): void {
  const trimmed = frame.trim()
  if (!trimmed || trimmed.startsWith(':')) return
  let event: PlaygroundStreamEvent | null
  try {
    event = parsePlaygroundSseData(trimmed)
  } catch (error) {
    if (error instanceof PlaygroundStreamProtocolError) throw error
    throw new PlaygroundStreamProtocolError(`收到格式无效的演练场流事件：${error instanceof Error ? error.message : String(error)}`)
  }
  if (!event) throw new PlaygroundStreamProtocolError('收到缺少 data 帧的演练场流事件')
  onEvent(event)
}

async function playgroundStreamHttpError(response: Response, path: string): Promise<PlaygroundStreamHttpError> {
  const message = await readFetchErrorMessage(response.clone(), path)
  const code = await readPlaygroundErrorCode(response)
  return new PlaygroundStreamHttpError(response.status, code, message)
}

async function readPlaygroundErrorCode(response: Response): Promise<string | undefined> {
  try {
    const payload = JSON.parse(await response.text()) as PlaygroundErrorBody
    const code = payload.code
    return typeof code === 'string' && code.trim() ? code : undefined
  } catch {
    return undefined
  }
}

function findSseBoundary(value: string): { index: number; length: number } | undefined {
  const lf = value.indexOf('\n\n')
  const crlf = value.indexOf('\r\n\r\n')
  if (lf < 0 && crlf < 0) return undefined
  return crlf >= 0 && (lf < 0 || crlf < lf) ? { index: crlf, length: 4 } : { index: lf, length: 2 }
}
