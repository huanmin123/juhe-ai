import type { RowActionItem } from '@/components/rowActions'
import type { PublicApiLogResultFilter } from '@/types/domain'

export const publicApiLogResultOptions: Array<{ label: string; value: PublicApiLogResultFilter }> = [
  { label: '全部结果', value: 'all' },
  { label: '成功', value: 'success' },
  { label: '失败', value: 'failed' }
]

export const publicApiLogColumns: Array<Record<string, unknown>> = [
  { title: '调用时间', key: 'createdAt', width: 180 },
  { title: '来源系统', key: 'source', minWidth: 180 },
  { title: '接口', key: 'path', minWidth: 260, responsiveFlex: true },
  { title: '结果', key: 'result', width: 92 },
  { title: '状态码', key: 'statusCode', width: 92 },
  { title: '耗时', key: 'duration', width: 100 },
  { title: '客户端 IP', dataIndex: 'clientIp', key: 'clientIp', width: 140 },
  { title: 'traceId', key: 'traceId', width: 300 },
  { title: '操作', key: 'actions', fixed: 'right', actionCount: 1 }
]

export const publicApiLogDetailActions: RowActionItem[] = [
  { key: 'detail', label: '详情', icon: 'detail', tone: 'info' }
]
