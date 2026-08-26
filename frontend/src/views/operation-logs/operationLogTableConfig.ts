export function operationLogTableColumns(isManagementView: boolean): Array<Record<string, unknown>> {
  const columns: Array<Record<string, unknown>> = [
    { title: '模块', key: 'module', width: 120 },
    { title: '动作', key: 'action', width: 110 },
    { title: '操作人', key: 'actor', width: 170 }
  ]
  if (isManagementView) {
    columns.push({ title: '业务归属', key: 'scope', width: 170 })
  }
  columns.push(
    { title: '摘要', key: 'summary', width: 300, responsiveFlex: true },
    { title: 'traceId', key: 'traceId', width: 190 },
    { title: '时间', key: 'createdAt', width: 180 },
    { title: '操作', key: 'actions', fixed: 'right' }
  )
  return columns
}

export function operationLogTableScrollX(isManagementView: boolean): number {
  return isManagementView ? 1280 : 1060
}
