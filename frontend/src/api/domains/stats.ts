import type {
  AccountUsageStatsOverview,
  AccountUsageStatsTrendOverview,
  AiPerformanceAccountOption,
  AiPerformanceOverview,
  DatabaseStorageSnapshotSummary,
  NonBusinessDataCleanupResult,
  SystemMetricsOverview,
  SystemMetricsRuntimeOverview,
  TableStorageOverview,
  UsageStatsWindow,
  UsageStatsOverview
} from '@/types/domain'
import type {
  AccountUsageStatsParams,
  AiPerformanceAccountOptionsParams,
  AiPerformanceParams,
  NonBusinessDataCleanupPayload,
  TableMonitorDatabaseHistoryParams,
  TableMonitorOverviewParams,
  UsageOverviewParams
} from '../contracts'
import { http, noTimeout, unwrap } from '../http'
import { accountUsageStatsParams, aiPerformanceAccountOptionsParams, aiPerformanceParams, stripSystemAccountParam } from '../params'

export const statsApi = {
  usageWindow: () => unwrap<UsageStatsWindow>(http.get('/stats/usage-window')),
  usageOverview: (params?: UsageOverviewParams) => unwrap<UsageStatsOverview>(http.get('/stats/usage-overview', { params })),
  accountUsage: (params?: AccountUsageStatsParams) => unwrap<AccountUsageStatsOverview>(http.get('/stats/account-usage', { params: accountUsageStatsParams(params) })),
  accountUsageTrend: (params?: AccountUsageStatsParams) => unwrap<AccountUsageStatsTrendOverview>(http.get('/stats/account-usage/trend', { params: accountUsageStatsParams(params) })),
  aiPerformanceAccounts: (params?: AiPerformanceAccountOptionsParams) => unwrap<AiPerformanceAccountOption[]>(http.get('/stats/ai-performance/accounts', { params: aiPerformanceAccountOptionsParams(params) })),
  aiPerformance: (params?: AiPerformanceParams) => unwrap<AiPerformanceOverview>(http.get('/stats/ai-performance', { params: aiPerformanceParams(params) })),
  systemMetrics: (params?: Pick<UsageOverviewParams, 'startDate' | 'endDate'>) => unwrap<SystemMetricsOverview>(http.get('/stats/system-metrics', { params })),
  systemMetricsRuntime: () => unwrap<SystemMetricsRuntimeOverview>(http.get('/stats/system-metrics/runtime'))
}

export const tableMonitorApi = {
  overview: (params?: TableMonitorOverviewParams) => unwrap<TableStorageOverview>(http.get('/table-monitor/overview', { params })),
  databaseHistory: (params?: TableMonitorDatabaseHistoryParams) => unwrap<DatabaseStorageSnapshotSummary[]>(http.get('/table-monitor/database-history', { params })),
  cleanupNonBusinessData: (payload: NonBusinessDataCleanupPayload) => unwrap<NonBusinessDataCleanupResult>(http.post('/table-monitor/non-business-data/cleanup', payload, noTimeout))
}

export const myStatsApi = {
  usageWindow: () => unwrap<UsageStatsWindow>(http.get('/my-stats/usage-window')),
  usageOverview: (params?: UsageOverviewParams) => unwrap<UsageStatsOverview>(http.get('/my-stats/usage-overview', { params: stripSystemAccountParam(params) })),
  accountUsage: (params?: AccountUsageStatsParams) => unwrap<AccountUsageStatsOverview>(http.get('/my-stats/account-usage', { params: accountUsageStatsParams(params, false) })),
  accountUsageTrend: (params?: AccountUsageStatsParams) => unwrap<AccountUsageStatsTrendOverview>(http.get('/my-stats/account-usage/trend', { params: accountUsageStatsParams(params, false) })),
  aiPerformanceAccounts: (params?: AiPerformanceAccountOptionsParams) => unwrap<AiPerformanceAccountOption[]>(http.get('/my-stats/ai-performance/accounts', { params: aiPerformanceAccountOptionsParams(params, false) })),
  aiPerformance: (params?: AiPerformanceParams) => unwrap<AiPerformanceOverview>(http.get('/my-stats/ai-performance', { params: aiPerformanceParams(params, false) }))
}
