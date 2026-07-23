import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { aiPerformanceParams, aiPerformanceSeriesParams } from '../../api/params'
import { buildAiPerformanceRequestSignature, createAiPerformanceRequestGate } from '../../views/ai-performance/aiPerformanceRequestGate'

const frontendRoot = resolve(fileURLToPath(new URL('../..', import.meta.url)))
const viewSource = readFileSync(resolve(frontendRoot, 'views/ai-performance/AiPerformanceView.vue'), 'utf8')
const selectionSource = readFileSync(resolve(frontendRoot, 'views/ai-performance/useAiPerformanceAccountSelection.ts'), 'utf8')

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
assert.match(viewSource, /if \(desiredIds\.has\(id\)\) resolvedSeriesAccountIds\.add\(id\)/, '删除后迟到的 series 不得把已删除账户标记为已解析')
assert.match(viewSource, /invalidateAccountOptions\(\)/, 'owner/auth scope 变化必须使旧账户 options 失效')
assert.match(selectionSource, /function invalidateAccountOptions\(\)/, '账户 options 必须提供作用域失效入口')

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
