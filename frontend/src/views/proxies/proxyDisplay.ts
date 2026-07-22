import type { RowActionItem } from '@/components/rowActions'
import { formatDateTime, formatMillisecondsAsSeconds } from '@/shared/formatters'
import type { ProxyProfileSummary, ProxyTestItemStatus } from '@/types/domain'

export const proxyColumns = [
  { title: '名称', dataIndex: 'name', key: 'name', width: 180, fixed: 'left' },
  { title: '类型', dataIndex: 'type', key: 'type', width: 100 },
  { title: '地址', dataIndex: 'host', key: 'host', width: 140 },
  { title: '端口', dataIndex: 'port', key: 'port', width: 80 },
  { title: '用户', dataIndex: 'username', key: 'username', width: 130 },
  { title: '状态', key: 'status', width: 160 },
  { title: '延迟', key: 'latency', width: 100 },
  { title: '出口 IP', key: 'outboundIp', width: 140 },
  { title: '地区', key: 'outboundRegion', width: 100 },
  { title: '说明', dataIndex: 'description', key: 'description', width: 200 },
  { title: '操作', key: 'actions', fixed: 'right', customRender: () => '' }
]

export const proxyReportColumns = [
  { title: '检测项', dataIndex: 'name', key: 'name', width: 120 },
  { title: '状态', dataIndex: 'status', key: 'status', width: 90 },
  { title: 'HTTP', dataIndex: 'httpStatus', key: 'httpStatus', width: 80 },
  { title: '延迟', dataIndex: 'latencyMs', key: 'latencyMs', width: 90 },
  { title: '说明', dataIndex: 'message', key: 'message' }
]

export const proxyTypeOptions = [
  { label: 'HTTP', value: 'http' },
  { label: 'HTTPS', value: 'https' },
  { label: 'SOCKS5', value: 'socks5' },
  { label: 'SOCKS5H', value: 'socks5h' }
]

export const proxyActions: RowActionItem[] = [
  { key: 'test', label: '测试', icon: 'test', tone: 'info' },
  { key: 'edit', label: '编辑', icon: 'edit', tone: 'primary' },
  {
    key: 'delete',
    label: '删除',
    icon: 'delete',
    tone: 'danger',
    confirmTitle: '确认删除这个代理？',
    confirmOkText: '删除'
  }
]

export function proxyTypeColor(type: string): string {
  if (type === 'socks5' || type === 'socks5h') return 'purple'
  if (type === 'https') return 'green'
  return 'blue'
}

export function testStatusColor(status: string): string {
  if (status === 'passed') return 'green'
  if (status === 'warning') return 'gold'
  if (status === 'failed') return 'red'
  return 'default'
}

export function testStatusText(status: string): string {
  if (status === 'passed') return '检测通过'
  if (status === 'warning') return '有告警'
  if (status === 'failed') return '检测失败'
  return '未检测'
}

export function testItemStatusColor(status: ProxyTestItemStatus): string {
  return status === 'passed' ? 'green' : status === 'warning' ? 'gold' : 'red'
}

export function testItemStatusText(status: ProxyTestItemStatus): string {
  return status === 'passed' ? '通过' : status === 'warning' ? '告警' : '失败'
}

export function formatLatency(value?: number): string {
  return formatMillisecondsAsSeconds(value)
}

export function latencyTooltip(proxy: ProxyProfileSummary): string {
  const parts = [
    proxy.lastTestMessage || testStatusText(proxy.testStatus),
    proxy.lastTestedAt ? `检测时间：${formatDateTime(proxy.lastTestedAt)}` : ''
  ].filter(Boolean)
  return parts.join('\n') || '尚未检测'
}
