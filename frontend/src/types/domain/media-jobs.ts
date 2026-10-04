export type MediaJobKind = 'video' | 'audio_transcription' | 'audio_speech'

export type MediaJobStatus = 'queued' | 'in_progress' | 'completed' | 'failed' | 'cancelled' | 'expired'

export interface MediaJobError {
  code: string
  message?: string
}

/** 管理面媒体任务列表行（媒体设计 §8.2，只读快照，不含 request_snapshot 与 artifact 定位）。 */
export interface MediaJobListItem {
  id: string
  kind: MediaJobKind
  status: MediaJobStatus
  providerCode?: string
  providerJobId?: string
  model?: string
  promptSummary?: string
  /** 媒体产物时长（秒），终态回填。 */
  seconds?: number
  /** 媒体产物尺寸（WxH 像素串，如 1280x720），创建请求快照回显。 */
  size?: string
  apiKeyId?: string
  accountId?: string
  costUsd?: number
  error?: MediaJobError
  createdAt: string
  updatedAt?: string
}

export interface MediaJobListResult {
  rows: MediaJobListItem[]
  total: number
}

export const mediaJobKindLabels: Record<MediaJobKind, string> = {
  video: '视频',
  audio_transcription: '长音频转写',
  audio_speech: '长音频合成'
}

export const mediaJobStatusLabels: Record<MediaJobStatus, string> = {
  queued: '排队中',
  in_progress: '进行中',
  completed: '已完成',
  failed: '失败',
  cancelled: '已取消',
  expired: '已过期'
}

/** 六态 Tag 用色沿项目 Tag 习惯：成功 green、失败 red、进行中 blue、过期 orange、取消 volcano、排队 default。 */
export const mediaJobStatusColors: Record<MediaJobStatus, string> = {
  queued: 'default',
  in_progress: 'blue',
  completed: 'green',
  failed: 'red',
  cancelled: 'volcano',
  expired: 'orange'
}
