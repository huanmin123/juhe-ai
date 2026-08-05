import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const source = (path: string) => readFileSync(new URL(`../../${path}`, import.meta.url), 'utf8')

const remoteAuthorizationPrincipalOptions = source('composables/useRemoteAuthorizationPrincipalOptions.ts')
const authorizationUsageResourceFilters = source('views/authorizations/useAuthorizationUsageResourceFilters.ts')
assert.match(remoteAuthorizationPrincipalOptions, /function invalidate\(\): void \{[\s\S]*clearSearchTimer\(\)[\s\S]*requestId \+= 1[\s\S]*loadingKey = undefined[\s\S]*loadingPromise = undefined[\s\S]*loading\.value = false[\s\S]*options\.value = \[\]/, '授权身份候选项失效必须清理搜索、在途请求、加载态和旧选项')
assert.match(authorizationUsageResourceFilters, /function invalidate\(\): void \{[\s\S]*clearSearchTimer\(\)[\s\S]*requestId \+= 1[\s\S]*loadingKey = undefined[\s\S]*loadingPromise = undefined[\s\S]*resourceOptionsLoading\.value = false[\s\S]*accounts\.value = \[\][\s\S]*groups\.value = \[\]/, '授权资源候选项失效必须清理搜索、在途请求、加载态和旧选项')
assert.match(authorizationUsageResourceFilters, /const baseOptions = await[\s\S]*if \(currentRequestId !== requestId\) return \[\][\s\S]*await ensureSelected/, '授权资源候选项在补齐已选项前必须复核请求代次')

const systemMetrics = source('views/stats/SystemMetricsStatsView.vue')
assert.doesNotMatch(systemMetrics, /dynamicRangeRollover|visibilitychange|window\.addEventListener\('focus'/, '系统指标不得因日期、可见性或焦点自动刷新')
assert.doesNotMatch(activated(systemMetrics), /loadPageData|loadUsageStatsWindow|forceUsageWindow/, '系统指标重新激活不得加载数据')
assert.doesNotMatch(authRevisionWatcher(systemMetrics), /\b(?:loadPageData|loadUsageStatsWindow|loadData|loadBackgroundJobs|loadBackgroundQueues)\s*\(/, '系统指标身份变化不得加载业务数据')
assert.match(authRevisionWatcher(systemMetrics), /\.abort\(\)[\s\S]*systemMetrics\.value = undefined/, '系统指标身份变化必须取消并清空旧数据')

const usageRecords = source('views/usage-records/UsageRecordsView.vue')
const usageRecordGroupOptions = source('views/usage-records/useUsageRecordGroupOptions.ts')
assert.doesNotMatch(usageRecords, /visibilitychange|window\.addEventListener\('focus'/, '使用记录不得因可见性或焦点自动刷新')
const usageRollover = sourceBetween(usageRecords, 'function refreshAutoDateAfterRollover', 'function scheduleAutoDateRollover')
assert.match(usageRollover, /dateRangeFilter\.value = nextRange[\s\S]*resetPagination\(\)/, '使用记录跨日必须只维护自动日期范围和分页状态')
assert.doesNotMatch(usageRollover, /\bloadData\s*\(/, '使用记录跨日不得自动加载列表')
assert.match(usageRecords, /dateMode\.value === 'manual' \? usageRecordDateRangeParam/, '使用记录 auto 日期模式必须继续由省略日期参数表达')
assert.match(usageRecords, /requestSignature:[\s\S]*authState\.revision\.value/, '使用记录请求签名必须绑定身份版本')
assert.match(usageRecords, /requestAuthRevision !== authState\.revision\.value/, '使用记录旧请求响应必须因身份版本失效')
assert.doesNotMatch(authRevisionWatcher(usageRecords), /\bloadData\s*\(/, '使用记录身份变化不得加载业务数据')
assert.match(authRevisionWatcher(usageRecords), /invalidatePendingLoads\(\)[\s\S]*invalidateGroupOptions\(\)[\s\S]*records\.value = \[\][\s\S]*systemAccounts\.value = \[\]/, '使用记录身份变化必须取消并清空列表和选项')
assert.match(usageRecordGroupOptions, /function invalidate\(\): void \{[\s\S]*clearSearchTimer\(\)[\s\S]*requestId \+= 1[\s\S]*loadingKey = undefined[\s\S]*loadingPromise = undefined/, '分组选项身份失效必须取消搜索并推进请求代次')

const systemMetricsPageLoad = sourceBetween(systemMetrics, 'async function loadPageData', 'function setupRuntimeObservers')
assert.match(systemMetricsPageLoad, /const currentPageLoadGeneration = \+\+pageLoadGeneration/, '系统指标页面加载必须拥有独立 generation')
assert.match(systemMetricsPageLoad, /await windowLoad\s+if \(currentPageLoadGeneration !== pageLoadGeneration\) return[\s\S]*syncDynamicDateRangeToStatsWindow\(\)/, '系统指标等待 usage-window 后必须先校验 generation')
assert.match(authRevisionWatcher(systemMetrics), /pageLoadGeneration \+= 1/, '系统指标身份变化必须使等待 usage-window 的旧页面加载失效')
assert.match(sourceBetween(systemMetrics, 'watch(() => backgroundJobsResult.value?.total', 'watch(() => backgroundQueuesResult.value?.total'), /typeof total !== 'number'.*Number\.isFinite\(total\).*total < 0\) return[\s\S]*void loadBackgroundJobs\(\)/, '后台任务分页只能由有效响应 total 触发回页加载')
assert.match(sourceBetween(systemMetrics, 'watch(() => backgroundQueuesResult.value?.total', 'onBeforeUnmount'), /typeof total !== 'number'.*Number\.isFinite\(total\).*total < 0\) return[\s\S]*void loadBackgroundQueues\(\)/, '后台队列分页只能由有效响应 total 触发回页加载')

const statsView = source('views/stats/StatsView.vue')
assert.match(activated(statsView), /initialLoadInterrupted[\s\S]*!usageOverview\.value[\s\S]*loadData\(\{ forceUsageWindow:/, '统计概览仅能在初始请求失活中断且无首个结果时恢复一次加载')
assert.match(activated(statsView), /setupChartObservers\(false\)/, '统计概览重新激活必须恢复渐进图表观察')
assert.doesNotMatch(authRevisionWatcher(statsView), /\b(?:loadData|loadUsageStatsWindow|load)\s*\(/, '统计概览身份变化不得加载主数据或系统账户选项')
assert.match(authRevisionWatcher(statsView), /invalidateStatsRequests\(\)[\s\S]*invalidateSystemAccountOptions\(\)[\s\S]*systemAccounts\.value = \[\][\s\S]*selectedSystemAccountId\.value = allSystemAccountsValue[\s\S]*selectedSystemAccount\.value = undefined[\s\S]*resetSystemAccountOptionsSearch\(\)/, '统计概览身份变化必须使系统账户选项失效并清空旧筛选')

const aiPerformance = source('views/ai-performance/AiPerformanceView.vue')
assert.match(activated(aiPerformance), /performanceRequestGate\.activate\(\)[\s\S]*setupChartObserver/, 'AI 性能重新激活必须恢复请求门禁和图表观察')
assert.doesNotMatch(activated(aiPerformance), /\bloadPerformanceContext\s*\(/, 'AI 性能重新激活不得加载业务数据')
assert.doesNotMatch(authRevisionWatcher(aiPerformance), /\b(?:loadPerformanceContext|loadUsageStatsWindow|load)\s*\(/, 'AI 性能身份变化不得加载主数据或系统账户选项')
assert.match(authRevisionWatcher(aiPerformance), /invalidateAccountOptions\(\)[\s\S]*invalidateSystemAccountOptions\(\)[\s\S]*invalidatePerformanceRequests\(\)[\s\S]*systemAccounts\.value = \[\][\s\S]*selectedSystemAccountId\.value = allSystemAccountsValue[\s\S]*selectedSystemAccount\.value = undefined[\s\S]*resetSystemAccountOptionsSearch\(\)/, 'AI 性能身份变化必须使系统账户选项失效并清空旧筛选')

const apiKeys = source('views/api-keys/ApiKeysView.vue')
const apiKeysAuthWatcher = authRevisionWatcher(apiKeys)
assert.doesNotMatch(activated(apiKeys), /\bloadData\s*\(/, 'API Key 重新激活不得加载业务数据')
assert.doesNotMatch(apiKeysAuthWatcher, /\b(?:loadData|loadRouteStrategyOptions|loadUserReferenceData)\s*\(/, 'API Key 身份变化不得加载主数据或辅助选项')
assert.match(apiKeysAuthWatcher, /invalidateSystemAccountOptions\(\)[\s\S]*invalidatePendingLoads\(\)[\s\S]*apiKeys\.value = \[\][\s\S]*routeStrategyFilterSelection\.value = defaults\.routeStrategyFilter[\s\S]*systemAccounts\.value = \[\][\s\S]*resetSystemAccountOptionsSearch\(\)[\s\S]*resetRouteStrategyOptionsSearch\(true\)/, 'API Key 身份变化必须使主数据、系统账户和策略路由选项失效并清空筛选')

const groups = source('views/groups/GroupsView.vue')
const groupsAuthWatcher = authRevisionWatcher(groups)
assert.doesNotMatch(activated(groups), /\bloadData\s*\(/, '分组重新激活不得加载业务数据')
assert.doesNotMatch(groupsAuthWatcher, /\b(?:loadData|loadGroupOptions)\s*\(/, '分组身份变化不得加载主数据或辅助选项')
assert.match(groupsAuthWatcher, /invalidateSystemAccountOptions\(\)[\s\S]*invalidateGroupOptions\(\)[\s\S]*invalidatePendingLoads\(\)[\s\S]*groups\.value = \[\][\s\S]*systemAccountFilterSelection\.value = undefined[\s\S]*systemAccounts\.value = \[\][\s\S]*resetSystemAccountOptionsSearch\(\)/, '分组身份变化必须使主数据和系统账户选项失效并清空筛选')

const providers = source('views/providers/ProvidersView.vue')
const providerRequestInvalidation = sourceBetween(providers, 'function invalidateProviderPageRequests', 'function deactivateProviderPage')
assert.doesNotMatch(providerRequestInvalidation, /providers\.value = \[\]/, '供应商 KeepAlive 失活只能作废请求，不得清空已加载列表')
assert.doesNotMatch(providerRequestInvalidation, /pageActive\s*=/, '供应商请求失效不得关闭当前活跃页面的手动刷新门禁')
assert.match(providerRequestInvalidation, /providerListRequestSequence \+= 1[\s\S]*modelRequestSequence \+= 1[\s\S]*loading\.value = false[\s\S]*modelLoading\.value = false/, '供应商请求失效必须同步清除列表和模型加载态')
assert.match(providers, /function deactivateProviderPage\(\): void \{\s*pageActive = false[\s\S]*invalidateProviderPageRequests\(\)/, '供应商页面失活必须单独关闭请求门禁并作废在途请求')
assert.doesNotMatch(sourceBetween(providers, 'function resetProviderPageState', 'onMounted(loadProviders)'), /pageActive\s*=/, '身份切换清空供应商状态后必须保留当前页面的手动刷新门禁')
const providersAuthWatcher = authRevisionWatcher(providers)
assert.doesNotMatch(activated(providers), /\bloadProviders\s*\(/, '供应商重新激活不得加载业务数据')
assert.doesNotMatch(providersAuthWatcher, /\b(?:loadProviders|reloadActiveProviderModels|loadModel)\s*\(/, '供应商身份变化不得加载主数据或模型归属选项')
assert.match(providersAuthWatcher, /resetProviderPageState\(\)[\s\S]*invalidateModelSystemAccountOptions\(\)[\s\S]*modelSystemAccountFilter\.value = ''[\s\S]*modelSystemAccounts\.value = \[\][\s\S]*resetModelSystemAccountOptionsSearch\(\)/, '供应商身份变化必须使模型归属选项失效并清空 owner 筛选')
assert.match(providers, /@refresh="loadProviders\(true\)"/, '身份切换后供应商页面必须保留手动刷新入口')

const authorizationTeamUsage = source('views/authorizations/AuthorizationTeamUsageView.vue')
const authorizationTeamUsageAuthWatcher = authRevisionWatcher(authorizationTeamUsage)
assert.doesNotMatch(activated(authorizationTeamUsage), /\breloadFromFirstPage\s*\(/, '团队授权消耗重新激活不得加载业务数据')
assert.doesNotMatch(authorizationTeamUsageAuthWatcher, /\b(?:loadData|loadUsageSummary|loadTeamOptions|loadResourceOwnerOptions|reloadFromFirstPage)\s*\(/, '团队授权消耗身份变化不得加载主数据或辅助选项')
assert.match(authorizationTeamUsageAuthWatcher, /requestGate\.deactivate\(\)[\s\S]*invalidatePendingLoads\(\)[\s\S]*invalidateResourceOwnerOptions\(\)[\s\S]*invalidateTeamOptions\(\)[\s\S]*invalidateResourceOptions\(\)[\s\S]*Object\.assign\(filters, defaultAuthorizationTeamUsageFilters\(\)\)[\s\S]*resourceOwners\.value = \[\][\s\S]*teams\.value = \[\][\s\S]*teamRows\.value = \[\]/, '团队授权消耗身份变化必须作废并清空主数据、身份、团队和资源选项')

const authorizationUserUsage = source('views/authorizations/AuthorizationUserUsageView.vue')
const authorizationUserUsageAuthWatcher = authRevisionWatcher(authorizationUserUsage)
assert.doesNotMatch(activated(authorizationUserUsage), /\breloadFromFirstPage\s*\(/, '用户授权消耗重新激活不得加载业务数据')
assert.doesNotMatch(authorizationUserUsageAuthWatcher, /\b(?:loadData|loadUsageSummary|loadTeamOptions|loadGranteeUserOptions|loadResourceOwnerUserOptions|reloadFromFirstPage)\s*\(/, '用户授权消耗身份变化不得加载主数据或辅助选项')
assert.match(authorizationUserUsageAuthWatcher, /requestGate\.deactivate\(\)[\s\S]*invalidatePendingLoads\(\)[\s\S]*invalidateTeamOptions\(\)[\s\S]*invalidateGranteeUserOptions\(\)[\s\S]*invalidateResourceOwnerUserOptions\(\)[\s\S]*invalidateResourceOptions\(\)[\s\S]*Object\.assign\(filters, defaultAuthorizationUserUsageFilters\(\)\)[\s\S]*teams\.value = \[\][\s\S]*granteeUsers\.value = \[\][\s\S]*resourceOwnerUsers\.value = \[\][\s\S]*userRows\.value = \[\]/, '用户授权消耗身份变化必须作废并清空主数据、身份、团队和资源选项')

const aiHealth = source('views/ai-health/AiHealthView.vue')
assert.match(activated(aiHealth), /pageActive = true[\s\S]*loadInitialVisiblePage\(\)/, 'AI 健康监控重新激活必须恢复页面状态并仅检查首次可见初始化')
assert.doesNotMatch(activated(aiHealth), /\bloadData\s*\(/, 'AI 健康监控重新激活不得直接加载列表')
assert.match(aiHealth, /authState\.revision\.value[\s\S]*accounts\.value = \[\][\s\S]*pagination\.current = 1/, 'AI 健康监控身份变化必须清空旧数据')
assert.doesNotMatch(authRevisionWatcher(aiHealth), /\bloadData\s*\(/, 'AI 健康监控身份变化不得加载业务数据')
const aiHealthInitialVisibleLoad = sourceBetween(aiHealth, 'function loadInitialVisiblePage', 'onMounted')
assert.match(aiHealthInitialVisibleLoad, /initialVisibleLoadStarted\s*\|\|\s*initialVisibleLoadCompleted\s*\|\|\s*accounts\.value\.length > 0/, 'AI 健康监控首次可见加载必须受开始、完成和已有数据的单次门禁约束')
assert.match(aiHealthInitialVisibleLoad, /initialVisibleLoadStarted = true[\s\S]*const generation = initialVisibleLoadGeneration[\s\S]*void loadData\(\)\.finally/, 'AI 健康监控仅能通过带代次的首次可见门禁加载一次')
assert.match(aiHealth, /onDeactivated\(\(\) => \{[\s\S]*invalidateInitialVisibleLoad\(\)[\s\S]*requestCoordinator\.cancelList\(\)/, 'AI 健康监控失活必须作废未完成的首次加载')

const accounts = source('views/accounts/AccountsView.vue')
const accountListData = source('views/accounts/useAccountListData.ts')
assert.doesNotMatch(accounts, /shouldAutoRefreshAccountModelCatalog|scheduleAutomaticAccountModelCatalogSync|automaticModelCatalogAttemptedRequestKeys/, '账户草稿不得自动同步上游模型目录')
assert.match(accounts, /@refresh-models="refreshAccountModelCatalog"/, '账户页面必须保留手动同步上游模型入口')
assert.match(accounts, /currentModelCatalogDiscoveryRequestKey[\s\S]*cancelAccountModelCatalogSync\(\)/, '草稿变化必须取消在途手动目录同步')
assert.match(accountListData, /requestSignature:[\s\S]*authState\.revision\.value/, '账户列表请求签名必须绑定身份版本')
assert.match(accountListData, /requestAuthRevision !== authState\.revision\.value/, '账户列表旧请求响应必须因身份版本失效')
assert.doesNotMatch(authRevisionWatcher(accountListData), /\bloadData\s*\(/, '账户列表身份变化不得加载业务数据')
assert.match(authRevisionWatcher(accountListData), /invalidatePendingLoads\(\)[\s\S]*accounts\.value = \[\][\s\S]*accountPagination\.total = 0/, '账户列表身份变化必须取消并清空列表和分页')
assert.match(accounts, /watch\(\(\) => authState\.revision\.value, \(\) => \{\s*clearSelection\(\)/, '账户页面身份变化必须清空选择')

const appLayout = source('layouts/AppLayout.vue')
assert.doesNotMatch(appLayout, /announcementsRefreshTimer|announcementsRefreshRunning|refreshAnnouncementsSafely|setInterval\(/, '公告不得由应用壳周期刷新')
assert.match(appLayout, /function openAnnouncementModal[\s\S]*loadAnnouncements|function refreshAnnouncementsInModal/, '公告面板打开时必须仍可加载公告')
assert.match(currentUserWatcher(appLayout), /announcementsInitialLoadAttempted[\s\S]*void loadAnnouncements\(\)/, '公告仅能在首次认证用户确定后初始化加载一次')
assert.doesNotMatch(currentUserWatcher(appLayout), /announcementsInitialLoadAttempted\s*=\s*false/, '公告身份变化不得重置初始化加载门禁')
assert.match(currentUserWatcher(appLayout), /announcements\.value = \[\][\s\S]*resetAnnouncementContentSession\(\)/, '身份对象变化必须清空公告和内容会话')

console.log('前端页面数据禁止自动刷新回归通过')

function activated(view: string): string {
  return view.match(/onActivated\((?:async )?\(\) => \{[\s\S]*?\n\}\)/)?.[0] ?? ''
}

function authRevisionWatcher(view: string): string {
  const arrowWatcherStart = view.indexOf('watch(() => authState.revision.value')
  const refWatcherStart = view.indexOf('watch(authState.revision')
  const watcherStart = [arrowWatcherStart, refWatcherStart].filter((index) => index >= 0).sort((left, right) => left - right)[0]
  assert.ok(watcherStart !== undefined, '缺少 authState.revision watcher')
  return callExpressionAt(view, watcherStart)
}

function currentUserWatcher(view: string): string {
  return view.match(/watch\(\s*currentUser,[\s\S]*?\n\s*\},\s*\{ immediate: true \}\s*\)/)?.[0] ?? ''
}

function sourceBetween(view: string, start: string, end: string): string {
  const startIndex = view.indexOf(start)
  const endIndex = view.indexOf(end, startIndex + start.length)
  assert.ok(startIndex >= 0 && endIndex > startIndex, `缺少可核验源码区间：${start} -> ${end}`)
  return view.slice(startIndex, endIndex)
}

function callExpressionAt(view: string, start: number): string {
  let depth = 0
  for (let index = start; index < view.length; index += 1) {
    const character = view[index]
    if (character === '(') depth += 1
    if (character === ')') {
      depth -= 1
      if (depth === 0) return view.slice(start, index + 1)
    }
  }
  assert.fail('watch 调用缺少闭合括号')
}
