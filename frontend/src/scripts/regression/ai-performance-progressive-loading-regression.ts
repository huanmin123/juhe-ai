import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { aiPerformanceParams, aiPerformanceSeriesParams } from '../../api/params'
import { buildAiPerformanceRequestSignature, createAiPerformanceRequestGate } from '../../views/ai-performance/aiPerformanceRequestGate'

const frontendRoot = resolve(fileURLToPath(new URL('../..', import.meta.url)))
const viewSource = readFileSync(resolve(frontendRoot, 'views/ai-performance/AiPerformanceView.vue'), 'utf8')
const selectionSource = readFileSync(resolve(frontendRoot, 'views/ai-performance/useAiPerformanceAccountSelection.ts'), 'utf8')
const accountTypeSource = readFileSync(resolve(frontendRoot, 'types/domain/usage-stats.ts'), 'utf8')

assert.match(viewSource, /aiPerformanceSeries/, '页面必须调用独立 series API')
assert.match(viewSource, /loadPerformanceBase/, 'base 必须独立加载')
assert.match(viewSource, /baseError[\s\S]*retryBase/, 'base 必须提供独立重试')
assert.match(viewSource, /seriesError[\s\S]*retrySeries/, 'series 必须提供独立重试')
assert.match(selectionSource, /options\.loadMissingSeries\(acceptedIds\)/, '新增账户只能补缺失 series')
assert.doesNotMatch(selectionSource, /options\.reloadPerformance\(\)/, '账户选择变化不得重取 base')
const removeAccountBody = selectionSource.match(/function removeAddedAccount\(id: string\) \{([\s\S]*?)\n  \}/)?.[1] ?? ''
assert.ok(removeAccountBody, '必须保留删除追加账户入口')
assert.doesNotMatch(removeAccountBody, /load|api\./, '删除追加账户必须纯本地完成且零请求')
assert.match(viewSource, /onDeactivate:[\s\S]*deactivatePerformanceRequests/, 'KeepAlive 失活必须推进请求 epoch')
assert.match(viewSource, /contextLoading/, 'usage-window 加载期间必须有独立 loading 状态')
assert.match(viewSource, /const windowLoad = loadUsageStatsWindow\([\s\S]*await loadPerformanceBase\(\)/, '可见 base 必须与 usage-window 元数据并行启动')
assert.doesNotMatch(viewSource, /await loadUsageStatsWindow\([\s\S]{0,180}await loadPerformanceBase\(\)/, 'base 不得串行等待 usage-window 元数据')
assert.match(viewSource, /:loading="initialLoading"/, '元数据仍在后台加载时不得继续禁用已可用的筛选栏')
assert.match(viewSource, /if \(!dateRangeExplicit\.value\) return \{\}/, '未显式选日期时必须由 Node 按统计时区归一化默认窗口')
assert.match(viewSource, /if \(desiredIds\.has\(id\)\) resolvedSeriesAccountIds\.add\(id\)/, '删除后迟到的 series 不得把已删除账户标记为已解析')
assert.match(viewSource, /invalidateAccountOptions\(\)/, 'owner/auth scope 变化必须使旧账户 options 失效')
assert.match(selectionSource, /function invalidateAccountOptions\(\)/, '账户 options 必须提供作用域失效入口')
assert.match(viewSource, /await loadPerformanceBase\(\)[\s\S]*overview\.value \? loadAdditionalSeries\(addedAccountIds\.value\)/, '恢复上下文时必须先读取 base，再仅补仍缺失的 series')
assert.doesNotMatch(viewSource, /activeAccountIds:\s*\[\.\.\.activeAccountIds\.value\]/, '临时账户筛选不得跨页面进入持久化')
assert.doesNotMatch(viewSource, /addedAccountIds:\s*\[\.\.\.addedAccountIds\.value\]/, '临时追加账户不得跨页面进入持久化')
assert.doesNotMatch(viewSource, /function refreshPerformance\(\)[\s\S]{0,120}force:\s*true/, '手动刷新不得绕过 usage-window 短缓存')
assert.doesNotMatch(accountTypeSource, /requestCountLast7d|defaultVisible|selected:\s*boolean/, 'AI 性能 HTTP DTO 不得返回页面自行派生或未渲染字段')

const query = aiPerformanceSeriesParams({ accountIds: ['a', 'a', 'b'], startDate: '2026-07-01', endDate: '2026-07-03' })
assert.equal(query.toString(), 'startDate=2026-07-01&endDate=2026-07-03&accountIds=a&accountIds=b', 'series 必须使用重复裸键并去重')
assert.deepEqual(aiPerformanceParams({ startDate: '2026-07-01', endDate: '2026-07-03' }), { startDate: '2026-07-01', endDate: '2026-07-03' }, 'base 请求不得携带 accountIds')

const gate = createAiPerformanceRequestGate()
const signatureA = buildAiPerformanceRequestSignature({ channel: 'series', scope: 'self', authRevision: 1, viewerId: 'u1', startDate: '2026-07-01', endDate: '2026-07-03', accountIds: ['a'] })
const signatureB = buildAiPerformanceRequestSignature({ channel: 'series', scope: 'self', authRevision: 1, viewerId: 'u1', startDate: '2026-07-02', endDate: '2026-07-04', accountIds: ['a'] })
const tokenA = gate.begin('series', signatureA)
const tokenB = gate.begin('series', signatureB)
assert.equal(gate.isCurrent(tokenA, signatureA), false, 'A→B 后迟到 A 必须失效')
assert.equal(gate.isCurrent(tokenB, signatureB), true)
const tokenANew = gate.begin('series', signatureA)
assert.equal(gate.isCurrent(tokenB, signatureB), false, 'B→A 后迟到 B 必须失效')
assert.equal(gate.acceptsRange(tokenANew, signatureA, { startDate: '2026-07-01', endDate: '2026-07-03', days: 3, maxDays: 31 }, { startDate: '2026-07-01', endDate: '2026-07-03' }), true)
gate.deactivate()
assert.equal(gate.isCurrent(tokenANew, signatureA), false, '失活后迟到响应必须失效')

console.log('ai-performance-progressive-loading regression passed')
