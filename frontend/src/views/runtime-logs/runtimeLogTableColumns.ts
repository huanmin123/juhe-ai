export const runtimeLogViewModeOptions = [
  { label: '索引查询', value: 'index' },
  { label: 'grep 模式', value: 'grep' }
]

export const runtimeLogLevelOptions = [
  { label: '全部级别', value: 'all' },
  { label: 'fatal', value: 'fatal' },
  { label: 'error', value: 'error' },
  { label: 'warn', value: 'warn' },
  { label: 'info', value: 'info' },
  { label: 'debug', value: 'debug' },
  { label: 'trace', value: 'trace' }
]

export const runtimeLogColumns = [
  { title: '时间', key: 'time', width: 180 },
  { title: '级别', key: 'level', width: 90 },
  { title: 'traceId', key: 'traceId', width: 250 },
  { title: '事件', key: 'event', width: 230 },
  { title: '消息', key: 'message', width: 620, responsiveFlex: true },
  { title: '操作', key: 'actions', fixed: 'right' }
]
