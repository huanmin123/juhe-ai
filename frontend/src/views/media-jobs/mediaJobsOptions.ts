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

/** 列表只保留通用管理列；供应商/上游任务号/归属账户/时长尺寸/用量/错误等细节入详情抽屉（媒体设计 §8.2）。 */
export const mediaJobColumns: Array<Record<string, unknown>> = [
  { title: '任务 ID', key: 'id', width: 210 },
  { title: '类型', key: 'kind', width: 116 },
  { title: '状态', key: 'status', width: 96 },
  { title: '模型', key: 'model', minWidth: 180, responsiveFlex: true },
  { title: '提示词', key: 'promptSummary', minWidth: 240, responsiveFlex: true },
  { title: 'API Key', key: 'apiKeyId', width: 150 },
  { title: '成本 USD', key: 'costUsd', width: 110 },
  { title: '创建时间', key: 'createdAt', width: 170 },
  { title: '操作', key: 'actions', width: 88, fixed: 'right', align: 'left' }
]

export const mediaJobTableScrollX = 1310
