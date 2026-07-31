export const auditOutcomeOptions = [
  { label: '全部结果', value: 'all' },
  { label: '成功', value: 'success' },
  { label: '重试后成功', value: 'success_after_retry' },
  { label: '网关成功', value: 'gateway_succeeded' },
  { label: '网关失败', value: 'gateway_failed' },
  { label: '上游失败', value: 'upstream_failed' },
  { label: '流式失败', value: 'stream_failed' },
  { label: '下游连接关闭', value: 'downstream_closed' },
  { label: '客户端中断（历史）', value: 'client_aborted' }
]

export const auditLogColumns = [
  { title: 'traceId', key: 'traceId', width: 250 },
  { title: '会话 ID', key: 'session', width: 250 },
  { title: '结果', key: 'outcome', width: 120 },
  { title: '结果状态', key: 'status', width: 90 },
  { title: '来源', key: 'trafficSource', width: 100 },
  { title: '接口', key: 'endpoint', width: 190 },
  { title: '模型', key: 'model', width: 240 },
  { title: '类型', key: 'stream', width: 90 },
  { title: 'AI账户', key: 'account', width: 160 },
  { title: 'API Key', key: 'apiKey', width: 150 },
  { title: '分组', key: 'group', width: 150 },
  { title: '系统账户', key: 'systemAccount', width: 150 },
  { title: '链路耗时', key: 'duration', width: 100 },
  { title: '时间', key: 'createdAt', width: 180 },
  { title: '操作', key: 'actions', fixed: 'right' }
]

export const auditAttemptColumns = [
  { title: '#', dataIndex: 'attemptIndex', width: 44 },
  { title: '结果', key: 'success', width: 64 },
  { title: 'AI账户', key: 'account', width: 112 },
  { title: '状态码', dataIndex: 'upstreamStatusCode', width: 64 },
  { title: '时间', key: 'startedAt', width: 132 },
  { title: '耗时', key: 'duration', width: 64 },
  { title: '上游 URL', key: 'url', width: 190 },
  { title: '错误', key: 'error', width: 150 }
]

export const auditPayloadColumns = [
  { title: '部分', key: 'partType', width: 96 },
  { title: '序号', dataIndex: 'sequenceIndex', width: 56 },
  { title: '类型', dataIndex: 'contentType', width: 118 },
  { title: '大小', key: 'size', width: 68 },
  { title: '状态', key: 'captureStatus', width: 72 },
  { title: '时间', key: 'createdAt', width: 132 },
  { title: 'Headers SHA256', key: 'headersSha256', width: 92 },
  { title: 'Body SHA256', key: 'bodySha256', width: 92 },
  { title: '操作', key: 'actions' }
]
