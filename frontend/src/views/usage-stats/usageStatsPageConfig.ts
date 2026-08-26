export interface AccountUsagePageState {
  current: number
  pageSize: number
}

export function accountUsageStatsTableScrollX(isManagementView: boolean): number {
  return isManagementView ? 1650 : 1470
}

export function accountUsageStatsTableColumns(isManagementView: boolean): Array<Record<string, unknown>> {
  const baseColumns: Array<Record<string, unknown>> = [
    { title: '排名', key: 'rank', width: 76 },
    { title: 'AI账户名称', dataIndex: 'name', key: 'name', width: 240 },
    { title: '账户类型', dataIndex: 'type', key: 'type', width: 110 },
    { title: '供应商', dataIndex: 'providerCode', key: 'providerCode', width: 110 },
    { title: '状态', key: 'status', width: 110 }
  ]
  if (isManagementView) {
    baseColumns.push({ title: '系统账户', key: 'systemAccount', width: 170 })
  }
  baseColumns.push(
    { title: '请求', key: 'requests', width: 120, align: 'right' },
    { title: 'Token', key: 'tokens', width: 130, align: 'right' },
    { title: '缓存读占比', key: 'cacheRate', width: 130, align: 'right' },
    { title: '缓存成本', key: 'cacheCost', width: 130, align: 'right' },
    { title: '成本', key: 'cost', width: 130, align: 'right' },
    { title: '最后使用', key: 'lastUsedAt', width: 180 }
  )
  return baseColumns
}

export function accountUsageStatsParams(input: {
  systemAccountId: string | undefined
  dateRange?: readonly [string, string]
  accountIds: string[]
  pageState: AccountUsagePageState
}) {
  const [startDate, endDate] = input.dateRange ?? []
  return {
    systemAccountId: input.systemAccountId,
    accountIds: input.accountIds,
    page: input.pageState.current,
    pageSize: input.pageState.pageSize,
    startDate,
    endDate
  }
}
