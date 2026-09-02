import type {
  AccountUsageStatsListResult,
  AccountUsageStatsOption,
  AccountUsageStatsSummaryResult,
  AccountUsageStatsTrendOverview,
  AiPerformanceAccountOption,
  AiPerformanceBaseResult,
  AiPerformanceSeriesResult,
  AiHealthListResult,
  AiHealthHourDetail,
  DatabaseStorageHistoryPoint,
  NonBusinessDataCleanupResult,
  SystemMetricsRuntimeJobsResult,
  SystemMetricsRuntimeQueuesResult,
  SystemMetricsRuntimeSummary,
  SystemMetricsTrendOverview,
  GoRuntimeTrendOverview,
  TableStorageHistoryPoint,
  TableStorageOverview,
  UsageStatsWindow,
  UsageStatsOverviewDailyTrendResult,
  UsageStatsOverviewErrorsResult,
  UsageStatsOverviewHourlyTrendResult,
  UsageStatsOverviewModelDistributionResult,
  UsageStatsOverviewSummaryResult
} from '@/types/domain'
import type {
  AccountUsageStatsParams,
  AccountUsageStatsOptionParams,
  AiPerformanceAccountOptionsParams,
  AiPerformanceParams,
  AiPerformanceSeriesParams,
  AiHealthParams,
  AiHealthHourDetailParams,
  NonBusinessDataCleanupPayload,
  TableMonitorDatabaseHistoryParams,
  TableMonitorHistoryParams,
  TableMonitorOverviewParams,
  UsageOverviewParams
} from '../contracts'
import { http, unwrap } from '../http'
import { accountUsageStatsOptionParams, accountUsageStatsParams, aiPerformanceAccountOptionsParams, aiPerformanceParams, aiPerformanceSeriesParams, stripSystemAccountParam } from '../params'

export const statsApi = {
  usageWindow: () => unwrap<UsageStatsWindow>(http.get('/stats/usage-window')),
  usageOverviewSummary: (params?: UsageOverviewParams) => unwrap<UsageStatsOverviewSummaryResult>(http.get('/stats/usage-overview/summary', { params })),
  usageOverviewDailyTrend: (params?: UsageOverviewParams) => unwrap<UsageStatsOverviewDailyTrendResult>(http.get('/stats/usage-overview/daily-trend', { params })),
  usageOverviewHourlyTrend: (params?: UsageOverviewParams) => unwrap<UsageStatsOverviewHourlyTrendResult>(http.get('/stats/usage-overview/hourly-trend', { params })),
  usageOverviewModelDistribution: (params?: UsageOverviewParams) => unwrap<UsageStatsOverviewModelDistributionResult>(http.get('/stats/usage-overview/model-distribution', { params })),
  usageOverviewErrors: (params?: UsageOverviewParams) => unwrap<UsageStatsOverviewErrorsResult>(http.get('/stats/usage-overview/errors', { params })),
  accountUsage: (params?: AccountUsageStatsParams) => unwrap<AccountUsageStatsListResult>(http.get('/stats/account-usage', { params: accountUsageStatsParams(params) })),
  accountUsageOptions: (params?: AccountUsageStatsOptionParams) => unwrap<AccountUsageStatsOption[]>(http.get('/stats/account-usage/options', { params: accountUsageStatsOptionParams(params) })),
  accountUsageSummary: (params?: AccountUsageStatsParams) => unwrap<AccountUsageStatsSummaryResult>(http.get('/stats/account-usage/summary', { params: accountUsageStatsParams(params) })),
  accountUsageTrend: (params?: AccountUsageStatsParams) => unwrap<AccountUsageStatsTrendOverview>(http.get('/stats/account-usage/trend', { params: accountUsageStatsParams(params) })),
  aiPerformanceAccounts: (params?: AiPerformanceAccountOptionsParams) => unwrap<AiPerformanceAccountOption[]>(http.get('/stats/ai-performance/accounts', { params: aiPerformanceAccountOptionsParams(params) })),
  aiPerformance: (params?: AiPerformanceParams) => unwrap<AiPerformanceBaseResult>(http.get('/stats/ai-performance', { params: aiPerformanceParams(params) })),
  aiPerformanceSeries: (params: AiPerformanceSeriesParams) => unwrap<AiPerformanceSeriesResult>(http.get('/stats/ai-performance/series', { params: aiPerformanceSeriesParams(params) })),
  aiHealth: (params?: AiHealthParams, options?: { signal?: AbortSignal }) => unwrap<AiHealthListResult>(http.get('/stats/ai-health', { params, signal: options?.signal })),
  aiHealthHourDetail: (params: AiHealthHourDetailParams, options?: { signal?: AbortSignal }) => unwrap<AiHealthHourDetail>(http.get('/stats/ai-health/hour-detail', { params, signal: options?.signal })),
  systemMetricsTrend: (params?: Pick<UsageOverviewParams, 'startDate' | 'endDate'>, options?: { signal?: AbortSignal }) => unwrap<SystemMetricsTrendOverview>(http.get('/stats/system-metrics/trend', { params, signal: options?.signal })),
  goRuntimeTrend: (params?: Pick<UsageOverviewParams, 'startDate' | 'endDate'>, options?: { signal?: AbortSignal }) => unwrap<GoRuntimeTrendOverview>(http.get('/stats/system-metrics/go-runtime-trend', { params, signal: options?.signal })),
  systemMetricsRuntimeSummary: (options?: { signal?: AbortSignal }) => unwrap<SystemMetricsRuntimeSummary>(http.get('/stats/system-metrics/runtime/summary', { signal: options?.signal })),
  systemMetricsRuntimeJobs: (params: { page: number; pageSize: number }, options?: { signal?: AbortSignal }) => unwrap<SystemMetricsRuntimeJobsResult>(http.get('/stats/system-metrics/runtime/jobs', { params, signal: options?.signal })),
  systemMetricsRuntimeQueues: (params: { page: number; pageSize: number }, options?: { signal?: AbortSignal }) => unwrap<SystemMetricsRuntimeQueuesResult>(http.get('/stats/system-metrics/runtime/queues', { params, signal: options?.signal }))
}

export const tableMonitorApi = {
  overview: (params?: TableMonitorOverviewParams) => unwrap<TableStorageOverview>(http.get('/table-monitor/overview', { params })),
  history: (params: TableMonitorHistoryParams) => unwrap<TableStorageHistoryPoint[]>(http.get('/table-monitor/history', { params })),
  databaseHistory: (params?: TableMonitorDatabaseHistoryParams) => unwrap<DatabaseStorageHistoryPoint[]>(http.get('/table-monitor/database-history', { params })),
  cleanupNonBusinessData: (payload: NonBusinessDataCleanupPayload) => unwrap<NonBusinessDataCleanupResult>(http.post('/table-monitor/non-business-data/cleanup', payload))
}

export const myStatsApi = {
  usageWindow: () => unwrap<UsageStatsWindow>(http.get('/my-stats/usage-window')),
  usageOverviewSummary: (params?: UsageOverviewParams) => unwrap<UsageStatsOverviewSummaryResult>(http.get('/my-stats/usage-overview/summary', { params: stripSystemAccountParam(params) })),
  usageOverviewDailyTrend: (params?: UsageOverviewParams) => unwrap<UsageStatsOverviewDailyTrendResult>(http.get('/my-stats/usage-overview/daily-trend', { params: stripSystemAccountParam(params) })),
  usageOverviewHourlyTrend: (params?: UsageOverviewParams) => unwrap<UsageStatsOverviewHourlyTrendResult>(http.get('/my-stats/usage-overview/hourly-trend', { params: stripSystemAccountParam(params) })),
  usageOverviewModelDistribution: (params?: UsageOverviewParams) => unwrap<UsageStatsOverviewModelDistributionResult>(http.get('/my-stats/usage-overview/model-distribution', { params: stripSystemAccountParam(params) })),
  usageOverviewErrors: (params?: UsageOverviewParams) => unwrap<UsageStatsOverviewErrorsResult>(http.get('/my-stats/usage-overview/errors', { params: stripSystemAccountParam(params) })),
  accountUsage: (params?: AccountUsageStatsParams) => unwrap<AccountUsageStatsListResult>(http.get('/my-stats/account-usage', { params: accountUsageStatsParams(params, false) })),
  accountUsageOptions: (params?: AccountUsageStatsOptionParams) => unwrap<AccountUsageStatsOption[]>(http.get('/my-stats/account-usage/options', { params: accountUsageStatsOptionParams(params, false) })),
  accountUsageSummary: (params?: AccountUsageStatsParams) => unwrap<AccountUsageStatsSummaryResult>(http.get('/my-stats/account-usage/summary', { params: accountUsageStatsParams(params, false) })),
  accountUsageTrend: (params?: AccountUsageStatsParams) => unwrap<AccountUsageStatsTrendOverview>(http.get('/my-stats/account-usage/trend', { params: accountUsageStatsParams(params, false) })),
  aiPerformanceAccounts: (params?: AiPerformanceAccountOptionsParams) => unwrap<AiPerformanceAccountOption[]>(http.get('/my-stats/ai-performance/accounts', { params: aiPerformanceAccountOptionsParams(params, false) })),
  aiPerformance: (params?: AiPerformanceParams) => unwrap<AiPerformanceBaseResult>(http.get('/my-stats/ai-performance', { params: aiPerformanceParams(params, false) })),
  aiPerformanceSeries: (params: AiPerformanceSeriesParams) => unwrap<AiPerformanceSeriesResult>(http.get('/my-stats/ai-performance/series', { params: aiPerformanceSeriesParams(params, false) })),
  aiHealth: (params?: AiHealthParams, options?: { signal?: AbortSignal }) => unwrap<AiHealthListResult>(http.get('/my-stats/ai-health', { params: stripSystemAccountParam(params), signal: options?.signal })),
  aiHealthHourDetail: (params: AiHealthHourDetailParams, options?: { signal?: AbortSignal }) => unwrap<AiHealthHourDetail>(http.get('/my-stats/ai-health/hour-detail', { params: stripSystemAccountParam(params), signal: options?.signal }))
}
