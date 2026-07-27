import { strict as assert } from 'node:assert'
import { existsSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

function source(relativeUrl: string): string {
  return readFileSync(fileURLToPath(new URL(relativeUrl, import.meta.url)), 'utf8')
}

for (const relativeUrl of [
  '../../shared/entityDetailCache.ts',
  '../../shared/shortLivedQueryCache.ts',
  '../../shared/shortLivedRequestCache.ts',
  '../../views/accounts/accountDetailCache.ts'
]) {
  assert.equal(existsSync(fileURLToPath(new URL(relativeUrl, import.meta.url))), false, `${relativeUrl} 不应继续缓存动态接口结果`)
}

const directDetailReads = [
  ['../../views/announcements/AnnouncementsView.vue', /api\.announcements\.detail\(record\.id\)/],
  ['../../views/public-api-logs/PublicApiLogsView.vue', /api\.publicApiLogs\.detail\(record\.id\)/],
  ['../../views/model-checks/ModelChecksView.vue', /modelChecksApi\.detail\(id, modelCheckScopeParams\.value\)/],
  ['../../views/operation-logs/OperationLogsView.vue', /operationLogsApi\.detail\(record\.id\)/],
  ['../../views/runtime-logs/useRuntimeLogDetailState.ts', /api\.runtimeLogs\.detail\(record\.id\)/],
  ['../../views/audit-logs/useAuditLogDetailPayload.ts', /api\.auditLogs\.detail\(record\.id\)/]
] as const

for (const [relativeUrl, expectedRequest] of directDetailReads) {
  const value = source(relativeUrl)
  assert.match(value, expectedRequest, `${relativeUrl} 每次打开都应读取后端详情`)
  assert.doesNotMatch(value, /loadEntityDetailCached|entityDetailCache/, `${relativeUrl} 不得复用动态详情结果`)
}

const accountEditSource = source('../../views/accounts/useAccountEditForm.ts')
assert.match(accountEditSource, /api\.accounts\.advancedDetail\(accountId, scopeParams\)/, '管理账户高级详情必须直接读取后端')
assert.match(accountEditSource, /api\.myAccounts\.advancedDetail\(accountId\)/, '个人账户高级详情必须直接读取后端')
assert.doesNotMatch(accountEditSource, /loadAccountDetailCached|accountDetailCache/, '账户编辑不能复用旧详情结果')

const modelCheckOptionsSource = source('../../views/model-checks/useModelCheckAccountOptions.ts')
const modelChecksViewSource = source('../../views/model-checks/ModelChecksView.vue')
assert.doesNotMatch(modelCheckOptionsSource, /createShortLivedQueryCache|OptionsCache\.(?:get|set)/, '模型检测账户选项不得保留 TTL 结果缓存')
assert.match(modelCheckOptionsSource, /accountOptionsInFlight/, '模型检测账户选项应保留相同在途请求合并')
assert.match(modelChecksViewSource, /runDetailRequestId/, '模型检测详情必须用请求代次隔离快速切换产生的旧响应')
assert.match(modelChecksViewSource, /if \(!isCurrentRunDetailRequest\(requestId, systemAccountId\)\) return/, '模型检测详情写入和报错前必须确认请求仍有效')
assert.match(modelCheckOptionsSource, /onBeforeUnmount\(invalidateAccountOptionRequests\)/, '模型检测账户选项卸载时必须作废在途请求')
assert.equal(
  [...modelCheckOptionsSource.matchAll(/catch \(error\) \{\s*if \(!isCurrentAccountOptionRequest/g)].length,
  3,
  '三类模型检测账户选项的失败提示都必须忽略过期请求'
)
for (const handler of ['handleTargetDropdownVisibleChange', 'handleComparisonDropdownVisibleChange', 'handleHistoryTargetDropdownVisibleChange']) {
  assert.match(modelCheckOptionsSource, new RegExp(`function ${handler}\\(open: boolean\\) \\{[\\s\\S]{0,120}if \\(open\\)`), `${handler} 每次展开都必须重新请求`)
}

const appLayoutSource = source('../../layouts/AppLayout.vue')
assert.match(appLayoutSource, /viewRoute\.meta\.keepAlive === true/, '动态页面组件缓存必须显式 opt-in')
assert.match(appLayoutSource, /routePrefetches/, '路由组件代码预加载可以保留')

const chatPerformanceSource = source('../../views/chat/chatConversationPerformance.ts')
assert.doesNotMatch(chatPerformanceSource, /cacheTtlMilliseconds|this\.cache\.set/, '聊天模型列表和能力不得保留 TTL 结果缓存')

console.log('动态页面缓存边界回归通过：业务结果始终重读，只保留页面状态、代码预加载和在途请求合并')
