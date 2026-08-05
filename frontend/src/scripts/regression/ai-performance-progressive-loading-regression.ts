import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { aiPerformanceParams, aiPerformanceSeriesParams } from '../../api/params'
import { buildAiPerformanceRequestSignature, createAiPerformanceRequestGate } from '../../views/ai-performance/aiPerformanceRequestGate'

const frontendRoot = resolve(fileURLToPath(new URL('../..', import.meta.url)))
const viewSource = readFileSync(resolve(frontendRoot, 'views/ai-performance/AiPerformanceView.vue'), 'utf8')
const selectionSource = readFileSync(resolve(frontendRoot, 'views/ai-performance/useAiPerformanceAccountSelection.ts'), 'utf8')
const filterToolbarSource = readFileSync(resolve(frontendRoot, 'views/ai-performance/AiPerformanceFilterToolbar.vue'), 'utf8')
const systemAccountOptionsSource = readFileSync(resolve(frontendRoot, 'composables/useRemoteSystemAccountOptions.ts'), 'utf8')
const accountTypeSource = readFileSync(resolve(frontendRoot, 'types/domain/usage-stats.ts'), 'utf8')

assert.match(viewSource, /aiPerformanceSeries/, '页面必须调用独立 series API')
assert.match(viewSource, /loadPerformanceBase/, 'base 必须独立加载')
assert.match(viewSource, /async function loadAdditionalSeries\(candidateIds: string\[\]\) \{\s*if \(!overview\.value\) return/, 'base 尚未建立时不得发送无法绑定日期范围的 series 请求')
assert.match(viewSource, /baseError[\s\S]*retryBase/, 'base 必须提供独立重试')
assert.match(viewSource, /function retryBase\(\) \{[\s\S]{0,100}loadPerformanceContext\(\)/, 'base 重试必须恢复完整上下文及追加账户 series')
assert.match(viewSource, /seriesError[\s\S]*retrySeries/, 'series 必须提供独立重试')
assert.match(selectionSource, /options\.loadMissingSeries\(acceptedIds\)/, '新增账户只能补缺失 series')
assert.doesNotMatch(selectionSource, /options\.reloadPerformance\(\)/, '账户选择变化不得重取 base')
const removeAccountBody = selectionSource.match(/function removeAddedAccount\(id: string\) \{([\s\S]*?)\n  \}/)?.[1] ?? ''
assert.ok(removeAccountBody, '必须保留删除追加账户入口')
assert.doesNotMatch(removeAccountBody, /load|api\./, '删除追加账户必须纯本地完成且零请求')
assert.match(viewSource, /onDeactivate:[\s\S]*deactivatePerformanceRequests/, 'KeepAlive 失活必须推进请求 epoch')
assert.match(viewSource, /onDeactivate:[\s\S]*invalidateSystemAccountOptions\(\)/, 'KeepAlive 失活必须取消系统账户搜索 debounce 并推进请求 generation')
const activatedSource = viewSource.match(/onActivated\(\(\) => \{[\s\S]*?\n\}\)/)?.[0] ?? ''
assert.match(activatedSource, /performanceRequestGate\.activate\(\)[\s\S]*setupChartObserver/, 'KeepAlive 重新激活必须恢复请求门禁和图表观察')
assert.doesNotMatch(activatedSource, /\bloadPerformanceContext\s*\(/, 'KeepAlive 重新激活不得加载 AI 性能数据')
const authRevisionWatcher = viewSource.match(/watch\(\(\) => authState\.revision\.value, \(\) => \{[\s\S]*?\n\}\)/)?.[0] ?? ''
assert.doesNotMatch(authRevisionWatcher, /\b(?:loadPerformanceContext|loadUsageStatsWindow|load)\s*\(/, '身份变化不得加载 AI 性能数据或系统账户选项')
assert.match(authRevisionWatcher, /invalidateAccountOptions\(\)[\s\S]*invalidateSystemAccountOptions\(\)[\s\S]*invalidatePerformanceRequests\(\)[\s\S]*systemAccounts\.value = \[\][\s\S]*selectedSystemAccountId\.value = allSystemAccountsValue[\s\S]*selectedSystemAccount\.value = undefined[\s\S]*resetSystemAccountOptionsSearch\(\)/, '身份变化必须使系统账户选项失效并清空旧筛选')
assert.match(systemAccountOptionsSource, /function invalidate\(\)[\s\S]*clearSearchTimer\(\)[\s\S]*requestId \+= 1/, '系统账户远程选项失效必须同时取消 debounce 并推进 generation')
assert.match(systemAccountOptionsSource, /finally \{[\s\S]*if \(currentRequestId === requestId\) \{[\s\S]*loadingKey === requestKey/, '旧代次请求的 finally 不得清理相同 key 的新请求状态')
assert.match(viewSource, /new IntersectionObserver[\s\S]*visibleChartMetrics\.add/, 'AI 性能图表必须在进入视口后才初始化')
assert.match(viewSource, /overview\.value = result[\s\S]{0,220}observeAndRenderPerformanceCharts\(\)/, 'base 返回并生成图表 DOM 后必须重新建立可见性观察')
assert.match(viewSource, /mergeAdditionalSeries\(result, requestedIds\)[\s\S]{0,120}observeAndRenderPerformanceCharts\(\)/, '追加 series 生成图表 DOM 后必须重新建立可见性观察')
assert.match(viewSource, /performanceCharts\.value[\s\S]*\.filter\(\(chart\) => visibleChartMetrics\.has\(chart\.metric\)\)/, '批量渲染只能覆盖已经进入视口的图表')
assert.doesNotMatch(viewSource, /Promise\.all\(performanceCharts\.value\.map/, 'base 返回后不得一次初始化全部四张 ECharts')
assert.match(viewSource, /contextLoading/, 'usage-window 加载期间必须有独立 loading 状态')
assert.match(viewSource, /const windowLoad = loadUsageStatsWindow\([\s\S]*await loadPerformanceBase\(\)/, '可见 base 必须与 usage-window 元数据并行启动')
assert.doesNotMatch(viewSource, /await loadUsageStatsWindow\([\s\S]{0,180}await loadPerformanceBase\(\)/, 'base 不得串行等待 usage-window 元数据')
assert.match(viewSource, /:loading="initialLoading"/, '元数据仍在后台加载时不得继续禁用已可用的筛选栏')
assert.match(viewSource, /if \(!dateRangeExplicit\.value\) return \{\}/, '未显式选日期时必须由 Node 按统计时区归一化默认窗口')
assert.match(viewSource, /if \(desiredIds\.has\(id\)\) resolvedSeriesAccountIds\.add\(id\)/, '删除后迟到的 series 不得把已删除账户标记为已解析')
assert.match(viewSource, /invalidateAccountOptions\(\)/, 'owner/auth scope 变化必须使旧账户 options 失效')
assert.match(selectionSource, /function invalidateAccountOptions\(\)/, '账户 options 必须提供作用域失效入口')
assert.match(selectionSource, /applyAccountOptions\(result, requestSeq, request\.key\)/, '账户 options 成功响应必须携带发起时的完整请求 key')
assert.match(selectionSource, /function isCurrentAccountOptionsRequest[\s\S]*requestSeq === accountSearchSeq && requestKey === currentAccountOptionsRequest\(\)\.key/, '账户 options 必须同时校验 generation 和当前搜索上下文')
assert.match(selectionSource, /catch \(error\) \{\s*if \(!isCurrentAccountOptionsRequest\(requestSeq, request\.key\)\) return\s*console\.error/, '失效账户 options 请求失败不得在新搜索上下文弹错')
assert.match(viewSource, /await loadPerformanceBase\(\)[\s\S]*overview\.value \? loadAdditionalSeries\(addedAccountIds\.value\)/, '恢复上下文时必须先读取 base，再仅补仍缺失的 series')
assert.doesNotMatch(viewSource, /activeAccountIds:\s*\[\.\.\.activeAccountIds\.value\]/, '临时账户筛选不得跨页面进入持久化')
assert.doesNotMatch(viewSource, /addedAccountIds:\s*\[\.\.\.addedAccountIds\.value\]/, '临时追加账户不得跨页面进入持久化')
assert.doesNotMatch(viewSource, /function refreshPerformance\(\)[\s\S]{0,120}force:\s*true/, '手动刷新不得绕过 usage-window 短缓存')
assert.doesNotMatch(accountTypeSource, /requestCountLast7d|defaultVisible|selected:\s*boolean/, 'AI 性能 HTTP DTO 不得返回页面自行派生或未渲染字段')
assert.match(filterToolbarSource, /<AccountAppendSelect[\s\S]*?:record-preference="false"/, '临时追加账户不得写入本地选择偏好')
assert.match(viewSource, /channel === 'series' && overview\.value[\s\S]*?startDate: overview\.value\.range\.startDate[\s\S]*?endDate: overview\.value\.range\.endDate/, '追加 series 必须锁定已接受的 base 日期范围')

const query = aiPerformanceSeriesParams({ accountIds: ['a', 'a', 'b'], startDate: '2026-07-01', endDate: '2026-07-03' })
assert.equal(query.toString(), 'startDate=2026-07-01&endDate=2026-07-03&accountIds=a&accountIds=b', 'series 必须使用重复裸键并去重')
assert.deepEqual(aiPerformanceParams({ startDate: '2026-07-01', endDate: '2026-07-03' }), { startDate: '2026-07-01', endDate: '2026-07-03' }, 'base 请求不得携带 accountIds')

const gate = createAiPerformanceRequestGate()
const implicitSignature = buildAiPerformanceRequestSignature({ channel: 'base', scope: 'self', authRevision: 1, viewerId: 'u1' })
const initialImplicitToken = gate.begin('base', implicitSignature)
assert.equal(
  gate.acceptsRange(initialImplicitToken, implicitSignature, { startDate: '2026-07-27', endDate: '2026-07-29', days: 3, maxDays: 31 }, {}),
  true,
  '首次无缓存加载必须接受 Node 按统计时区归一化的隐式日期窗口'
)
const implicitSeriesSignature = buildAiPerformanceRequestSignature({ channel: 'series', scope: 'self', authRevision: 1, viewerId: 'u1', accountIds: ['a'] })
const implicitSeriesToken = gate.begin('series', implicitSeriesSignature)
assert.equal(
  gate.acceptsRange(implicitSeriesToken, implicitSeriesSignature, { startDate: '2026-07-27', endDate: '2026-07-29', days: 3, maxDays: 31 }, {}),
  false,
  'series 不得接受未绑定 base 日期范围的隐式响应'
)
const beforeMidnightToken = gate.begin('base', implicitSignature)
const resetAfterMidnightToken = gate.begin('base', implicitSignature)
assert.equal(
  gate.acceptsRange(beforeMidnightToken, implicitSignature, { startDate: '2026-07-27', endDate: '2026-07-29', days: 3, maxDays: 31 }, {}),
  false,
  '重置或跨日后的新请求必须使之前相同 default signature 的迟到响应失效'
)
assert.equal(
  gate.acceptsRange(resetAfterMidnightToken, implicitSignature, { startDate: '2026-07-28', endDate: '2026-07-30', days: 3, maxDays: 31 }, {}),
  true,
  '重置和跨日竞态中的最新隐式请求必须接受服务端返回的新默认窗口'
)
const signatureA = buildAiPerformanceRequestSignature({ channel: 'series', scope: 'self', authRevision: 1, viewerId: 'u1', startDate: '2026-07-01', endDate: '2026-07-03', accountIds: ['a'] })
const signatureB = buildAiPerformanceRequestSignature({ channel: 'series', scope: 'self', authRevision: 1, viewerId: 'u1', startDate: '2026-07-02', endDate: '2026-07-04', accountIds: ['a'] })
const tokenA = gate.begin('series', signatureA)
const tokenB = gate.begin('series', signatureB)
assert.equal(gate.isCurrent(tokenA, signatureA), false, 'A→B 后迟到 A 必须失效')
assert.equal(gate.isCurrent(tokenB, signatureB), true)
const tokenANew = gate.begin('series', signatureA)
assert.equal(gate.isCurrent(tokenB, signatureB), false, 'B→A 后迟到 B 必须失效')
assert.equal(gate.acceptsRange(tokenANew, signatureA, { startDate: '2026-07-01', endDate: '2026-07-03', days: 3, maxDays: 31 }, { startDate: '2026-07-01', endDate: '2026-07-03' }), true)
assert.equal(gate.acceptsRange(tokenANew, signatureA, { startDate: '2026-07-02', endDate: '2026-07-04', days: 3, maxDays: 31 }, { startDate: '2026-07-01', endDate: '2026-07-03' }), false, '显式日期请求仍必须拒绝范围不一致的响应')
gate.deactivate()
assert.equal(gate.isCurrent(tokenANew, signatureA), false, '失活后迟到响应必须失效')

console.log('ai-performance-progressive-loading regression passed')
