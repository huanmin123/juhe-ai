import type {
  StudioAccountOption,
  StudioAsset,
  StudioAssetKind,
  StudioImageGenerationRequest,
  StudioListResult,
  StudioModelOption,
  StudioSpeechGenerationRequest,
  StudioTask,
  StudioTaskKind,
  StudioTaskStatus,
  StudioTranscriptionResult,
  StudioVideoGenerationRequest,
  StudioWorkbenchDomain
} from '@/types/domain'
import { apiUrl, http, unwrap } from '../http'

/**
 * 媒体工作室 API（媒体工作室设计 §4）：账户候选、域模型目录、资产上传/列表/
 * 删除/内容、四类生成受理与任务查询/取消。全部相对 baseURL /__aisys__/api。
 */

/** 列表 limit 归一（§4.0 对齐 mediaJobs 先例：默认 20、上限 100、非法回落默认由后端执行）。 */
export function studioListLimit(value?: number): number | undefined {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < 1) return undefined
  return Math.min(Math.floor(value), 100)
}

/** 创建类请求幂等键（§4.0）：用户范围 1..128 字符；前端统一 crypto.randomUUID()。 */
export function newStudioClientOperationId(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }
  return `studio-${Date.now()}-${Math.random().toString(16).slice(2)}`
}

export interface StudioAssetUploadOptions {
  paramsJson?: Record<string, unknown>
  signal?: AbortSignal
  onProgress?: (percent: number) => void
}

export interface StudioTranscriptionOptions {
  language?: string
  signal?: AbortSignal
  onProgress?: (percent: number) => void
}

export interface StudioAssetContentUrlOptions {
  variant?: 'original' | 'preview'
  download?: boolean
}

export const studioApi = {
  /** 账户候选（§4.1）：domain 可选过滤（image|audio|video|text）。 */
  listAccounts: (domain?: StudioWorkbenchDomain) =>
    unwrap<StudioListResult<StudioAccountOption>>(
      http.get('/my-studio/accounts', { params: domain ? { domain } : undefined })
    ),

  /** 域模型目录（§4.1）：domain 必填；capabilities 能力载荷驱动表单。 */
  listModels: (domain: StudioWorkbenchDomain) =>
    unwrap<StudioListResult<StudioModelOption>>(http.get('/my-studio/models', { params: { domain } })),

  /** 素材上传（§4.2，P0 仅 image）：FormData + 进度 + timeout 0 + AbortSignal（chat uploadAsset 模式）。 */
  uploadAsset: (file: File, options?: StudioAssetUploadOptions) => {
    const body = new FormData()
    body.append('file', file, file.name)
    if (options?.paramsJson) body.append('paramsJson', JSON.stringify(options.paramsJson))
    return unwrap<StudioAsset>(http.post('/my-studio/assets', body, {
      signal: options?.signal,
      timeout: 0,
      onUploadProgress: (event) => {
        if (!event.total || event.total <= 0) return
        options?.onProgress?.(Math.min(100, Math.max(0, Math.round((event.loaded / event.total) * 100))))
      }
    }))
  },

  /** 素材库列表（§4.2）：{rows,total}，limit/offset，kind 可选。 */
  listAssets: (params?: { limit?: number; offset?: number; kind?: StudioAssetKind }) =>
    unwrap<StudioListResult<StudioAsset>>(http.get('/my-studio/assets', {
      params: params?.limit === undefined
        ? params
        : { ...params, limit: studioListLimit(params.limit) }
    })),

  /** 删除素材（§4.2）：204；引用保护 409 studio_asset_referenced。 */
  deleteAsset: (assetId: string) =>
    http.delete(`/my-studio/assets/${encodeURIComponent(assetId)}`),

  /** 生成任务列表（§4.4）：status/kind/operation_id 过滤；operation_id 是响应丢失后的受理确认入口。 */
  listTasks: (params?: { limit?: number; offset?: number; status?: StudioTaskStatus; kind?: StudioTaskKind; operation_id?: string }) =>
    unwrap<StudioListResult<StudioTask>>(http.get('/my-studio/tasks', {
      params: params?.limit === undefined
        ? params
        : { ...params, limit: studioListLimit(params.limit) }
    })),

  /** 任务快照（轮询端点，§4.4）。 */
  getTask: (taskId: string, options?: { signal?: AbortSignal }) =>
    unwrap<StudioTask>(http.get(`/my-studio/tasks/${encodeURIComponent(taskId)}`, { signal: options?.signal })),

  /** 取消任务（§4.4 分层取消语义）：返回取消后任务快照。 */
  cancelTask: (taskId: string) =>
    unwrap<StudioTask>(http.delete(`/my-studio/tasks/${encodeURIComponent(taskId)}`)),

  /** 图像生成（§4.3）：异步受理返回任务；clientOperationId 必填。 */
  generateImage: (payload: StudioImageGenerationRequest) =>
    unwrap<StudioTask>(http.post('/my-studio/generations/images', payload)),

  /** 视频生成（§4.3）：异步受理返回任务。 */
  generateVideo: (payload: StudioVideoGenerationRequest) =>
    unwrap<StudioTask>(http.post('/my-studio/generations/videos', payload)),

  /** TTS（§4.3）：同步返回已保存资产。 */
  generateSpeech: (payload: StudioSpeechGenerationRequest) =>
    unwrap<StudioAsset>(http.post('/my-studio/generations/speech', payload)),

  /** STT（§4.3）：同步返回文本（multipart 上传音频）。 */
  generateTranscription: (
    file: File,
    fields: { accountId: string; model: string; clientOperationId: string; language?: string },
    options?: StudioTranscriptionOptions
  ) => {
    const body = new FormData()
    body.append('file', file, file.name)
    body.append('accountId', fields.accountId)
    body.append('model', fields.model)
    body.append('clientOperationId', fields.clientOperationId)
    if (fields.language) body.append('language', fields.language)
    return unwrap<StudioTranscriptionResult>(http.post('/my-studio/generations/transcriptions', body, {
      signal: options?.signal,
      timeout: 0,
      onUploadProgress: (event) => {
        if (!event.total || event.total <= 0) return
        options?.onProgress?.(Math.min(100, Math.max(0, Math.round((event.loaded / event.total) * 100))))
      }
    }))
  }
}

/** 素材内容地址（§4.2）：ETag/304；download=1 附 attachment。 */
export function studioAssetContentUrl(assetId: string, options?: StudioAssetContentUrlOptions): string {
  const params: Record<string, string | undefined> = {}
  if (options?.variant && options.variant !== 'original') params.variant = options.variant
  if (options?.download) params.download = '1'
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value) search.set(key, value)
  }
  const path = apiUrl(`/my-studio/assets/${encodeURIComponent(assetId)}/content`)
  const query = search.toString()
  return query ? `${path}?${query}` : path
}
