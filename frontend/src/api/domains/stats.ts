import type {
  AccountUsageStatsListResult,
  AccountUsageStatsSummaryResult,
  AccountUsageStatsTrendOverview,
  AiPerformanceAccountOption,
  AiPerformanceBaseResult,
  AiPerformanceSeriesResult,
  AiHealthListResult,
  DatabaseStorageHistoryPoint,
  NonBusinessDataCleanupResult,
  SystemMetricsOverview,
  SystemMetricsTrendOverview,
  SystemMetricsRuntimeOverview,
  TableStorageOverview,
  UsageStatsWindow,
  UsageStatsOverview,
  UsageStatsOverviewErrorsResult,
  UsageStatsOverviewHourlyTrendResult,
  UsageStatsOverviewModelDistributionResult,
  UsageStatsOverviewSummaryResult
} from '@/types/domain'
import type {
  AccountUsageStatsParams,
  AiPerformanceAccountOptionsParams,
  AiPerformanceParams,
  AiPerformanceSeriesParams,
  AiHealthParams,
  NonBusinessDataCleanupPayload,
  TableMonitorDatabaseHistoryParams,
  TableMonitorOverviewParams,
  UsageOverviewParams
} from '../contracts'
import { http, noTimeout, unwrap } from '../http'
import { accountUsageStatsParams, aiPerformanceAccountOptionsParams, aiPerformanceParams, aiPerformanceSeriesParams, stripSystemAccountParam } from '../params'

export const statsApi = {
  usageWindow: () => unwrap<UsageStatsWindow>(http.get('/stats/usage-window')),
  usageOverview: (params?: UsageOverviewParams) => unwrap<UsageStatsOverview>(http.get('/stats/usage-overview', { params })),
  usageOverviewSummary: (params?: UsageOverviewParams) => unwrap<UsageStatsOverviewSummaryResult>(http.get('/stats/usage-overview/summary', { params })),
  usageOverviewHourlyTrend: (params?: UsageOverviewParams) => unwrap<UsageStatsOverviewHourlyTrendResult>(http.get('/stats/usage-overview/hourly-trend', { params })),
  usageOverviewModelDistribution: (params?: UsageOverviewParams) => unwrap<UsageStatsOverviewModelDistributionResult>(http.get('/stats/usage-overview/model-distribution', { params })),
  usageOverviewErrors: (params?: UsageOverviewParams) => unwrap<UsageStatsOverviewErrorsResult>(http.get('/stats/usage-overview/errors', { params })),
  accountUsage: (params?: AccountUsageStatsParams) => unwrap<AccountUsageStatsListResult>(http.get('/stats/account-usage', { params: accountUsageStatsParams(params) })),
  accountUsageSummary: (params?: AccountUsageStatsParams) => unwrap<AccountUsageStatsSummaryResult>(http.get('/stats/account-usage/summary', { params: accountUsageStatsParams(params) })),
  accountUsageTrend: (params?: AccountUsageStatsParams) => unwrap<AccountUsageStatsTrendOverview>(http.get('/stats/account-usage/trend', { params: accountUsageStatsParams(params) })),
  aiPerformanceAccounts: (params?: AiPerformanceAccountOptionsParams) => unwrap<AiPerformanceAccountOption[]>(http.get('/stats/ai-performance/accounts', { params: aiPerformanceAccountOptionsParams(params) })),
  aiPerformance: (params?: AiPerformanceParams) => unwrap<AiPerformanceBaseResult>(http.get('/stats/ai-performance', { params: aiPerformanceParams(params) })),
  aiPerformanceSeries: (params: AiPerformanceSeriesParams) => unwrap<AiPerformanceSeriesResult>(http.get('/stats/ai-performance/series', { params: aiPerformanceSeriesParams(params) })),
  aiHealth: (params?: AiHealthParams) => unwrap<AiHealthListResult>(http.get('/stats/ai-health', { params })),
  systemMetrics: (params?: Pick<UsageOverviewParams, 'startDate' | 'endDate'>) => unwrap<SystemMetricsOverview>(http.get('/stats/system-metrics', { params })),
  systemMetricsTrend: (params?: Pick<UsageOverviewParams, 'startDate' | 'endDate'>) => unwrap<SystemMetricsTrendOverview>(http.get('/stats/system-metrics/trend', { params })),
  systemMetricsRuntime: () => unwrap<SystemMetricsRuntimeOverview>(http.get('/stats/system-metrics/runtime'))
}

export const tableMonitorApi = {
  overview: (params?: TableMonitorOverviewParams) => unwrap<TableStorageOverview>(http.get('/table-monitor/overview', { params })),
  databaseHistory: (params?: TableMonitorDatabaseHistoryParams) => unwrap<DatabaseStorageHistoryPoint[]>(http.get('/table-monitor/database-history', { params })),
  cleanupNonBusinessData: (payload: NonBusinessDataCleanupPayload) => unwrap<NonBusinessDataCleanupResult>(http.post('/table-monitor/non-business-data/cleanup', payload, noTimeout))
}

export const myStatsApi = {
  usageWindow: () => unwrap<UsageStatsWindow>(http.get('/my-stats/usage-window')),
  usageOverview: (params?: UsageOverviewParams) => unwrap<UsageStatsOverview>(http.get('/my-stats/usage-overview', { params: stripSystemAccountParam(params) })),
  usageOverviewSummary: (params?: UsageOverviewParams) => unwrap<UsageStatsOverviewSummaryResult>(http.get('/my-stats/usage-overview/summary', { params: stripSystemAccountParam(params) })),
  usageOverviewHourlyTrend: (params?: UsageOverviewParams) => unwrap<UsageStatsOverviewHourlyTrendResult>(http.get('/my-stats/usage-overview/hourly-trend', { params: stripSystemAccountParam(params) })),
  usageOverviewModelDistribution: (params?: UsageOverviewParams) => unwrap<UsageStatsOverviewModelDistributionResult>(http.get('/my-stats/usage-overview/model-distribution', { params: stripSystemAccountParam(params) })),
  usageOverviewErrors: (params?: UsageOverviewParams) => unwrap<UsageStatsOverviewErrorsResult>(http.get('/my-stats/usage-overview/errors', { params: stripSystemAccountParam(params) })),
  accountUsage: (params?: AccountUsageStatsParams) => unwrap<AccountUsageStatsListResult>(http.get('/my-stats/account-usage', { params: accountUsageStatsParams(params, false) })),
  accountUsageSummary: (params?: AccountUsageStatsParams) => unwrap<AccountUsageStatsSummaryResult>(http.get('/my-stats/account-usage/summary', { params: accountUsageStatsParams(params, false) })),
  accountUsageTrend: (params?: AccountUsageStatsParams) => unwrap<AccountUsageStatsTrendOverview>(http.get('/my-stats/account-usage/trend', { params: accountUsageStatsParams(params, false) })),
  aiPerformanceAccounts: (params?: AiPerformanceAccountOptionsParams) => unwrap<AiPerformanceAccountOption[]>(http.get('/my-stats/ai-performance/accounts', { params: aiPerformanceAccountOptionsParams(params, false) })),
  aiPerformance: (params?: AiPerformanceParams) => unwrap<AiPerformanceBaseResult>(http.get('/my-stats/ai-performance', { params: aiPerformanceParams(params, false) })),
  aiPerformanceSeries: (params: AiPerformanceSeriesParams) => unwrap<AiPerformanceSeriesResult>(http.get('/my-stats/ai-performance/series', { params: aiPerformanceSeriesParams(params, false) }))
  ,aiHealth: (params?: AiHealthParams) => unwrap<AiHealthListResult>(http.get('/my-stats/ai-health', { params: stripSystemAccountParam(params) }))
}
