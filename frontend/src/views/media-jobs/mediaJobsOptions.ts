import type { MediaJobKind, MediaJobStatus } from '@/types/domain'

export type MediaJobStatusFilter = MediaJobStatus | 'all'
export type MediaJobKindFilter = MediaJobKind | 'all'

export const mediaJobStatusFilterOptions: Array<{ label: string; value: MediaJobStatusFilter }> = [
  { label: '全部状态', value: 'all' },
  { label: '排队中', value: 'queued' },
  { label: '进行中', value: 'in_progress' },
  { label: '已完成', value: 'completed' },
  { label: '失败', value: 'failed' },
  { label: '已取消', value: 'cancelled' },
  { label: '已过期', value: 'expired' }
]

export const mediaJobKindFilterOptions: Array<{ label: string; value: MediaJobKindFilter }> = [
  { label: '全部类型', value: 'all' },
  { label: '视频', value: 'video' },
  { label: '长音频转写', value: 'audio_transcription' },
  { label: '长音频合成', value: 'audio_speech' }
]

export const mediaJobColumns: Array<Record<string, unknown>> = [
  { title: '任务 ID', key: 'id', width: 210 },
  { title: '类型', key: 'kind', width: 116 },
  { title: '状态', key: 'status', width: 96 },
  { title: '供应商', key: 'providerCode', width: 110 },
  { title: '模型', key: 'model', minWidth: 190, responsiveFlex: true },
  { title: '时长', key: 'seconds', width: 92 },
  { title: '尺寸', key: 'size', width: 100 },
  { title: 'API Key', key: 'apiKeyId', width: 150 },
  { title: 'AI 账户', key: 'accountId', width: 150 },
  { title: '成本 USD', key: 'costUsd', width: 110 },
  { title: '错误', key: 'error', width: 150 },
  { title: '创建时间', key: 'createdAt', width: 170 },
  { title: '更新时间', key: 'updatedAt', width: 170 }
]

export const mediaJobTableScrollX = 1818
