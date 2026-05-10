export const auditOutcomeOptions = [
  { label: '全部结果', value: 'all' },
  { label: '成功', value: 'success' },
  { label: '重试后成功', value: 'success_after_retry' },
  { label: '网关失败', value: 'gateway_failed' },
  { label: '上游失败', value: 'upstream_failed' },
  { label: '流式失败', value: 'stream_failed' },
  { label: '客户端断开', value: 'client_aborted' }
]

export const auditLogColumns = [
  { title: 'traceId', key: 'traceId', width: 250 },
  { title: '结果', key: 'outcome', width: 120 },
  { title: '状态码', key: 'status', width: 90 },
  { title: '接口', key: 'endpoint', width: 190 },
  { title: '模型', key: 'model', width: 150 },
  { title: '类型', key: 'stream', width: 90 },
  { title: '账号', key: 'account', width: 160 },
  { title: 'API Key', key: 'apiKey', width: 150 },
  { title: '分组', key: 'group', width: 150 },
  { title: '系统账户', key: 'systemAccount', width: 150 },
  { title: '耗时', key: 'duration', width: 90 },
  { title: '时间', key: 'createdAt', width: 180 },
  { title: '操作', key: 'actions', width: 90, fixed: 'right' }
]

export const auditAttemptColumns = [
  { title: '#', dataIndex: 'attemptIndex', width: 52 },
  { title: '结果', key: 'success', width: 76 },
  { title: '账号', key: 'account', width: 132 },
  { title: '状态码', dataIndex: 'upstreamStatusCode', width: 76 },
  { title: '耗时', key: 'duration', width: 76 },
  { title: '上游 URL', key: 'url', width: 220 },
  { title: '错误', key: 'error', width: 160 }
]

export const auditPayloadColumns = [
  { title: '部分', key: 'partType', width: 116 },
  { title: '序号', dataIndex: 'sequenceIndex', width: 76 },
  { title: '类型', dataIndex: 'contentType', width: 150 },
  { title: '大小', key: 'size', width: 82 },
  { title: 'Headers SHA256', key: 'headersSha256', width: 130 },
  { title: 'Body SHA256', key: 'bodySha256', width: 130 },
  { title: '操作', key: 'actions', width: 72 }
]
