/**
 * 媒体工作室域类型（媒体工作室设计 §4 API 契约）：/my-studio 资产、账户候选、
 * 域模型目录、生成任务与请求 DTO。字段名与 Go 注册面 JSON 投影一致（§4.2/§4.4）。
 */

export type StudioWorkbenchDomain = 'image' | 'audio' | 'video'

export type StudioAssetKind = 'image' | 'audio' | 'video'

export type StudioTaskKind = 'image' | 'video'

/** 六值状态词表同 media_jobs（§3.2）。 */
export type StudioTaskStatus = 'queued' | 'in_progress' | 'completed' | 'failed' | 'cancelled' | 'expired'

/** 结算子状态与 status 正交：completed + 未 settled = 上游完成但产物尚未入库（前端不显示成品）。 */
export type StudioTaskSettlement = 'pending_settled' | 'settled' | 'failed'

/** 资产（§4.2 AssetDTO）。width/height 仅 image；durationMs 来源缺失为 null。 */
export interface StudioAsset {
  id: string
  kind: StudioAssetKind
  mime: string
  bytes: number
  width?: number | null
  height?: number | null
  durationMs?: number | null
  sha256: string
  sourceTaskId?: string | null
  params: Record<string, unknown>
  createdAt: string
}

export interface StudioTaskError {
  code: string
  message?: string
}

/** 生成任务（§4.4 TaskDTO）。assetId 仅 settlement='settled' 且资产存在时非空。 */
export interface StudioTask {
  id: string
  kind: StudioTaskKind
  status: StudioTaskStatus
  settlement: StudioTaskSettlement
  model: string
  accountId?: string | null
  providerCode?: string | null
  promptSummary: string
  assetId?: string | null
  error?: StudioTaskError | null
  clientOperationId: string
  createdAt: string
  updatedAt?: string
}

export interface StudioListResult<T> {
  rows: T[]
  total: number
}

/** 账户候选（§4.1）：全状态可见（不按启用过滤），可派发性由受理裁决。 */
export interface StudioAccountOption {
  accountId: string
  name: string
  providerCode: string
  domain: string
  status: string
}

/**
 * 域模型目录行（§4.1）。capabilities 为能力载荷原样对象（键未声明即不渲染，
 * 前端不硬编码参数项）；提取须经 shared/studioCapabilities 的安全读取函数。
 */
export interface StudioModelOption {
  model: string
  providerCode: string
  domain: string
  endpointModes: string[]
  capabilities: Record<string, unknown>
}

/** 图像生成请求（§4.3）：无数量参数（决策 #9④ 首版单张）。 */
export interface StudioImageGenerationRequest {
  accountId: string
  model: string
  prompt: string
  negativePrompt?: string
  size?: string
  referenceAssetId?: string
  providerOptions?: Record<string, unknown>
  clientOperationId: string
}

/** 视频生成请求（§4.3）。seconds/size 取值须在能力载荷档位内。 */
export interface StudioVideoGenerationRequest {
  accountId: string
  model: string
  prompt: string
  firstFrameAssetId?: string
  seconds?: string | number
  size?: string
  providerOptions?: Record<string, unknown>
  clientOperationId: string
}

/** TTS 请求（§4.3）：同步返回已保存资产（决策 #9③，无独立保存开关）。 */
export interface StudioSpeechGenerationRequest {
  accountId: string
  model: string
  input: string
  voice?: string
  speed?: string | number
  volume?: string | number
  emotion?: string
  responseFormat?: string
  providerOptions?: Record<string, unknown>
  clientOperationId: string
}

/** STT 结果（§4.3）：srt 仅在模型 timestampCapable 且上游返回时出现。 */
export interface StudioTranscriptionResult {
  text: string
  srt?: string
}

export const studioTaskStatusLabels: Record<StudioTaskStatus, string> = {
  queued: '排队中',
  in_progress: '生成中',
  completed: '已完成',
  failed: '失败',
  cancelled: '已取消',
  expired: '已过期'
}

/** 六态 Tag 用色沿项目习惯（同 media-jobs）：成功 green、失败 red、进行中 blue、过期 orange、取消 volcano、排队 default。 */
export const studioTaskStatusColors: Record<StudioTaskStatus, string> = {
  queued: 'default',
  in_progress: 'blue',
  completed: 'green',
  failed: 'red',
  cancelled: 'volcano',
  expired: 'orange'
}

export const studioTaskKindLabels: Record<StudioTaskKind, string> = {
  image: '图像',
  video: '视频'
}

/** 账户候选状态标注（§4.1 全状态可见；未知原文原样展示）。 */
export function studioAccountStatusLabel(status?: string): string {
  if (!status) return '-'
  const labels: Record<string, string> = {
    active: '可用',
    pending_test: '待检测',
    disabled: '已停用'
  }
  return labels[status] ?? status
}

/** 不可调度状态（前端标注，不阻断选择——可派发性由受理结果裁决）。 */
export function isStudioAccountDispatchable(status?: string): boolean {
  return status === 'active' || status === 'pending_test'
}
