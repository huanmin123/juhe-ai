import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { inspect } from 'node:util'
import ts from 'typescript'

import {
  buildChatLongSessionFixture,
  buildSafeFixtureSummary,
  chatLongSessionArtifactMaxBytes,
  chatLongSessionArtifactQualityFailure,
  extractProjectArtifact,
  assertChatLongSessionScore,
  scoreChatLongSession
} from './chat-long-session-fixture.js'
import { createBoundedSseParser, resolveChatLongSessionMaxEventCount } from './chat-long-session-sse.js'
import { pickChildProcessBaseEnv, redactKnownSecrets, retryBusyCleanup, runIndependentCleanup, sanitizeErrorForDiagnostics } from './chat-long-session-runtime.js'
import { decideChatSubmissionRecovery } from './chat-long-session-recovery.js'
import { ChatLongSessionRunBudget } from './chat-long-session-budget.js'
import { listWindowsProcessIdentities, selectTrackedProcessTree, stopTrackedWindowsProcessTree, type TrackedProcessIdentity } from './chat-long-session-process-tree.js'
import { extractSafeChatStreamFailure } from './chat-long-session-failure.js'
import { isTransientChatLongSessionFailure, runChatLongSessionTurnAttempts } from './chat-long-session-attempts.js'
import {
  buildChatLongSessionAttemptIdentity,
  buildChatLongSessionResumePlan,
  chatLongSessionResumeCanonicalHash
} from './chat-long-session-checkpoint.js'
import { loadRuntimeBaseEnv } from '../../config/runtime-base-env.js'
import { ChatLongSessionStreamProgress } from './chat-long-session-stream-progress.js'
import { consumeReaderWithBoundedCancellation } from './chat-long-session-reader.js'
import { buildSafeBusyCleanupDiagnostic, runBoundedDiagnosticProcess } from './chat-long-session-cleanup-diagnostics.js'
import {
  buildChatLongSessionSemanticSeedPlan,
  chatLongSessionControlledSeedMaxTokens,
  chatLongSessionSemanticSeedMaxTurns
} from './chat-long-session-semantic-seed.js'
import { withoutChatLongSessionAcceptanceObservability } from './chat-long-session-acceptance-snapshot.js'
import { shouldRemoveChatLongSessionTemp } from './chat-long-session-temp-retention.js'

const fixture = buildChatLongSessionFixture()
const successfulTempRemoval = {
  keepTemp: false,
  executionSucceeded: true,
  acceptancePassed: true,
  realProbe: false,
  reportWritten: true,
  primaryError: false,
  cleanupHealthy: true
}
assert.equal(shouldRemoveChatLongSessionTemp(successfulTempRemoval), true)
assert.equal(shouldRemoveChatLongSessionTemp({ ...successfulTempRemoval, primaryError: true }), false, '主流程或迟到中断失败时必须保留诊断目录')
assert.equal(shouldRemoveChatLongSessionTemp({ ...successfulTempRemoval, cleanupHealthy: false }), false, '任一前置 cleanup 失败时必须保留诊断目录')
assert.equal(shouldRemoveChatLongSessionTemp({ ...successfulTempRemoval, reportWritten: false }), false, '正式报告未写成时必须保留诊断目录')
assert.equal(shouldRemoveChatLongSessionTemp({ ...successfulTempRemoval, realProbe: true, acceptancePassed: false, reportWritten: false }), true, '成功 real-probe 无需正式报告即可清理')
assert.equal(chatLongSessionSemanticSeedMaxTurns, 50, '真实验收业务轮次上限必须固定为 50')
assert.equal(buildChatLongSessionSemanticSeedPlan(50, 0).artifacts.length, 50, 'turns=50 精确边界必须可用')
for (const invalidTurns of [-1, 1.5, 51, 1e9, 2 ** 32, Number.NaN, Number.POSITIVE_INFINITY, Number.MAX_SAFE_INTEGER + 1]) {
  assert.throws(
    () => buildChatLongSessionSemanticSeedPlan(invalidTurns, 0),
    /chat_long_session_semantic_seed_turns_invalid/,
    `turns=${String(invalidTurns)} 必须在分配 artifact 前 fail-fast`
  )
}
for (const invalidTarget of [-1, 1.5, Number.NaN, Number.POSITIVE_INFINITY, Number.NEGATIVE_INFINITY, Number.MAX_SAFE_INTEGER + 1]) {
  assert.throws(
    () => buildChatLongSessionSemanticSeedPlan(1, invalidTarget),
    /chat_long_session_semantic_seed_target_tokens_invalid/,
    `target=${String(invalidTarget)} 必须在 token 计算前 fail-fast`
  )
}
assert.deepEqual(
  buildChatLongSessionSemanticSeedPlan(0, 0),
  { artifacts: [], totalBytes: 0, totalTokens: 0 },
  'turns=0,target=0 必须返回明确的空计划'
)
assert.throws(
  () => buildChatLongSessionSemanticSeedPlan(0, 1),
  /chat_long_session_semantic_seed_zero_turns_requires_zero_target/,
  '零轮次不能悄悄吞掉非零 token 目标'
)
const maximumSemanticSeed = buildChatLongSessionSemanticSeedPlan(42, chatLongSessionControlledSeedMaxTokens)
assert(maximumSemanticSeed.totalTokens <= chatLongSessionControlledSeedMaxTokens, '受控预填的实际 token 数不得越过 18 万硬上限')
assert(maximumSemanticSeed.totalTokens > chatLongSessionControlledSeedMaxTokens * 0.9, '受控预填不能因为硬上限实现而退化为过小样本')
assert.deepEqual(
  withoutChatLongSessionAcceptanceObservability({ businessHash: 'same', auditLogCount: 12, upstreamAttemptCount: 20 }),
  withoutChatLongSessionAcceptanceObservability({ businessHash: 'same', auditLogCount: 13, upstreamAttemptCount: 21 }),
  '第 51 轮业务副作用比较必须忽略异步审计日志及其 attempt 的同步增长'
)
assert(fixture.every((turn) => turn.prompt.includes(`${turn.introducedFeatureId} 的验收证据`)), '每轮必须把机器评分证据公开写入提示词，禁止隐藏验收条件')
assert.match(fixture[49]!.prompt, /REQ-50 的验收证据[^。]*aurora-acceptance-ready/, '最终轮必须明确公开 REQ-50 的验收证据')
const cumulativeEvidenceIndex = fixture[49]!.prompt.indexOf('累计验收证据')
assert(cumulativeEvidenceIndex >= 0 && fixture[49]!.prompt.indexOf('REQ-01', cumulativeEvidenceIndex) < fixture[49]!.prompt.lastIndexOf('REQ-50'), '最终 checkpoint 必须累计列出全部验收证据')
const idleProgress = new ChatLongSessionStreamProgress({ startedAt: 0, eventIdleMs: 180_000, progressIdleMs: 300_000 })
let canceledReaderCalls = 0
const fakeReader = { cancel: async () => { canceledReaderCalls += 1 } } as unknown as ReadableStreamDefaultReader<Uint8Array>
await assert.rejects(consumeReaderWithBoundedCancellation(fakeReader, async () => { throw new Error('fixture_reader_failed') }, 10), /fixture_reader_failed/)
assert.equal(canceledReaderCalls, 1, 'reader 消费异常必须立即 cancel')
await consumeReaderWithBoundedCancellation(fakeReader, async () => 'completed', 10)
assert.equal(canceledReaderCalls, 1, 'reader 正常完成不得额外 cancel')
const hangingCancelReader = { cancel: () => new Promise<void>(() => undefined) } as unknown as ReadableStreamDefaultReader<Uint8Array>
let scheduledCancelTimeoutMs: number | undefined
let clearedCancelTimeout = false
await assert.rejects(consumeReaderWithBoundedCancellation(
  hangingCancelReader,
  async () => { throw new Error('fixture_hanging_cancel') },
  10,
  {
    setTimeout: (callback, timeoutMs) => {
      scheduledCancelTimeoutMs = timeoutMs
      callback()
      return Symbol('fixture-timeout')
    },
    clearTimeout: () => { clearedCancelTimeout = true }
  }
), /fixture_hanging_cancel/)
assert.equal(scheduledCancelTimeoutMs, 10, 'cancel 自身挂起必须使用有界超时')
assert.equal(clearedCancelTimeout, true, '有界 cancel 完成后必须清理定时器')
idleProgress.observe('heartbeat', {}, 170_000, 0)
assert.equal(idleProgress.expiredReason(180_000), 'event_idle', 'heartbeat 不能掩盖无上游事件停滞')
const slowProgress = new ChatLongSessionStreamProgress({ startedAt: 0, eventIdleMs: 180_000, progressIdleMs: 300_000 })
slowProgress.observe('response.status', {}, 170_000, 0)
assert.equal(slowProgress.expiredReason(299_999), undefined)
assert.equal(slowProgress.expiredReason(300_000), 'progress_idle', '只有状态事件不能掩盖无内容/工具/思考进度')
slowProgress.observe('message.delta', { delta: '继续输出' }, 290_000, 12)
assert.equal(slowProgress.expiredReason(400_000), undefined, '持续慢输出不能被固定 300 秒误停')
assert.deepEqual(slowProgress.snapshot(400_000, 7), {
  lastEventAt: new Date(290_000).toISOString(),
  lastDeltaAt: new Date(290_000).toISOString(),
  eventCount: 7,
  partialBytes: 12,
  eventIdleMs: 110_000,
  progressIdleMs: 110_000
})
const regressionRoot = dirname(fileURLToPath(import.meta.url))
const realSource = readFileSync(resolve(regressionRoot, 'chat-long-session-real-e2e.ts'), 'utf8').replaceAll('\r\n', '\n')
const semanticSeedSource = readFileSync(resolve(regressionRoot, 'chat-long-session-semantic-seed.ts'), 'utf8')
const gatewayBodyMiddlewareSource = readFileSync(resolve(regressionRoot, '../../modules/gateway/request/body-middleware.ts'), 'utf8')
const realSourceFile = ts.createSourceFile('chat-long-session-real-e2e.ts', realSource, ts.ScriptTarget.ESNext, true, ts.ScriptKind.TS)
const mainEntryStatements = realSourceFile.statements.filter(isAwaitMainStatement)
assert.equal(mainEntryStatements.length, 1, '真实 runner 顶层必须且只能调用一次 await main()')
const mainEntryStatement = mainEntryStatements[0]
assert.equal(realSourceFile.statements.at(-1), mainEntryStatement, 'await main() 必须位于模块最后，确保全部 class/const/helper 已初始化')
const earlyMainSourceFile = ts.createSourceFile('early-main.ts', 'await main(); class LaterClass {}', ts.ScriptTarget.ESNext, true, ts.ScriptKind.TS)
assert.notEqual(earlyMainSourceFile.statements.at(-1), earlyMainSourceFile.statements.find(isAwaitMainStatement), '结构门禁必须捕获提前执行的 main')
assert(realSource.includes("process.argv.includes('--offline-stream-recovery')"), '必须提供直接执行真实模块的离线 stream error/deadline recovery 回归入口')
assert(realSource.includes("process.argv.includes('--module-initialization-check')"), '必须提供直接执行真实模块完整求值的 initialization check')
const processTreeRegressionSource = readFileSync(resolve(regressionRoot, 'chat-long-session-process-tree-regression.ts'), 'utf8')
assert(processTreeRegressionSource.includes("process.argv.includes('--cleanup-harness')"), 'Windows 进程树集成回归必须在独立子 harness 中执行 cleanup')
assert(processTreeRegressionSource.includes('HARNESS_SURVIVED_AFTER_CLEANUP'), '子 harness 必须在 cleanup 完成后输出存活 marker')
assert(processTreeRegressionSource.includes("assert.match(output, /HARNESS_SURVIVED_AFTER_CLEANUP/)"), '外层进程必须断言 cleanup 后 marker')
assert(!realSource.includes("import { runtimeConfig } from '../../config/runtime.js'"), 'real e2e 不得在清理父进程环境前静态加载 runtimeConfig')
const resolveRunSecretIndex = realSource.indexOf('resolveChatLongSessionRunSecret(tempRoot, {')
const sanitizeProcessEnvIndex = realSource.indexOf('applyHermeticProcessEnv(tempRoot, runSecret)')
const runtimeDynamicImportIndex = realSource.indexOf("await import('../../config/runtime.js')")
assert(resolveRunSecretIndex >= 0 && sanitizeProcessEnvIndex > resolveRunSecretIndex && runtimeDynamicImportIndex > sanitizeProcessEnvIndex, '必须先解析稳定 run identity、清理当前进程 PG/Redis 环境，再动态加载 runtimeConfig')
assert(!realSource.includes('...process.env'), 'child env 必须从 OS allowlist 构造，不能展开父进程全部环境')
assert(realSource.indexOf('applyHermeticProcessEnv(tempRoot, runSecret)') < realSource.indexOf("await import('../../shared/logger.js')"), '必须设置 temp log path/disabled 后再加载 logger')
assert(!/interface ChatLongSessionCheckpoint[\s\S]*?\n}\n/.exec(realSource)?.[0].match(/\b(?:secret|apiKey)\b/i), 'checkpoint schema 不得持久化 runtime secret 或 API key')
const childBaseEnv = pickChildProcessBaseEnv({ PATH: 'safe-path', SystemRoot: 'safe-root', JUHE_AI_POSTGRES_URL: 'secret-db', JUHE_AI_CHAT_REAL_API_KEY: 'secret-key' })
assert.deepEqual(childBaseEnv, { PATH: 'safe-path', SystemRoot: 'safe-root' })
const redacted = redactKnownSecrets(
  'Bearer real-key authorization: gateway-key cookie=session-token x-api-key: real-key {"cookie":"session-token"} app-secret',
  ['real-key', 'gateway-key', 'session-token', 'app-secret']
)
for (const secret of ['real-key', 'gateway-key', 'session-token', 'app-secret']) assert(!redacted.includes(secret), `redactor 泄漏 ${secret}`)
const safeStreamFailure = extractSafeChatStreamFailure('message.failed', {
  type: 'attacker-controlled-type',
  code: ' upstream bad/<script> ',
  message: `request failed with sk-probe-secret ${'tail'.repeat(2_000)}`
}, ['sk-probe-secret'])
assert.equal(safeStreamFailure.type, 'message.failed')
assert.equal(safeStreamFailure.code, 'upstream_bad__script_')
assert(!safeStreamFailure.message.includes('sk-probe-secret'))
assert(Buffer.byteLength(safeStreamFailure.message, 'utf8') <= 2_048, '失败诊断 message 必须有 2 KiB UTF-8 上限')

const replacementInputs: Array<{ attempt: number; replaceTurnId?: string }> = []
const replacementDelays: number[] = []
const replacementSequence = [
  { status: 200, terminalEvent: 'message.failed', turnId: 'failed-turn-1', firstDeltaMs: null, totalMs: 10, eventCount: 2, failure: { type: 'message.failed' as const, code: 'gateway_stream_failed', message: 'connection reset' } },
  { status: 200, terminalEvent: 'message.completed', turnId: 'completed-turn-2', firstDeltaMs: 5, totalMs: 20, eventCount: 3 }
]
const replacementOutcome = await runChatLongSessionTurnAttempts({
  maxAttempts: 3,
  sleep: async (delayMs) => { replacementDelays.push(delayMs) },
  submit: async (input) => { replacementInputs.push({ attempt: input.attempt, ...(input.replaceTurnId ? { replaceTurnId: input.replaceTurnId } : {}) }); return replacementSequence[input.attempt - 1]! },
  resolveAcceptedTurnId: async () => undefined
})
assert.equal(replacementOutcome.status, 'completed')
assert.deepEqual(replacementInputs, [{ attempt: 1 }, { attempt: 2, replaceTurnId: 'failed-turn-1' }])
assert.deepEqual(replacementDelays, [2_000], '第 2 次 transient attempt 前必须等待 2 秒')
assert.equal(replacementOutcome.attempts.length, 2)
assert.deepEqual(replacementOutcome.attempts.map((metric) => metric.delayMs), [0, 2_000])
assert.deepEqual(Object.keys(replacementOutcome.attempts[0]!).sort(), ['attempt', 'delayMs', 'eventCount', 'failure', 'firstDeltaMs', 'replacement', 'status', 'terminalEvent', 'totalMs'])

const authoritativeReplacementInputs: Array<{ attempt: number; replaceTurnId?: string }> = []
let authoritativeTurnIdLookups = 0
const authoritativeReplacementOutcome = await runChatLongSessionTurnAttempts({
  maxAttempts: 3,
  sleep: async () => undefined,
  submit: async (input) => {
    authoritativeReplacementInputs.push({ attempt: input.attempt, ...(input.replaceTurnId ? { replaceTurnId: input.replaceTurnId } : {}) })
    return input.attempt === 1
      ? { status: 200, terminalEvent: 'message.failed', firstDeltaMs: null, totalMs: 10, eventCount: 2, failure: { type: 'message.failed', code: 'gateway_stream_failed', message: 'upstream temporarily unavailable' } }
      : { status: 200, terminalEvent: 'message.completed', turnId: 'completed-authoritative-turn', firstDeltaMs: 5, totalMs: 20, eventCount: 3 }
  },
  resolveAcceptedTurnId: async () => { authoritativeTurnIdLookups += 1; return 'failed-authoritative-turn' }
})
assert.equal(authoritativeReplacementOutcome.status, 'completed')
assert.equal(authoritativeTurnIdLookups, 1)
assert.deepEqual(authoritativeReplacementInputs, [{ attempt: 1 }, { attempt: 2, replaceTurnId: 'failed-authoritative-turn' }], 'SSE 缺少 turnId 时必须使用 submission 权威查询结果替换，不能猜测')

let httpTransientSubmissions = 0
const httpTransientOutcome = await runChatLongSessionTurnAttempts({
  maxAttempts: 3,
  sleep: async () => undefined,
  submit: async (input) => {
    httpTransientSubmissions += 1
    return input.attempt === 1
      ? { status: 503, terminalEvent: null, turnId: 'http-failed-turn', firstDeltaMs: null, totalMs: 1, eventCount: 0, failure: { type: 'message.failed', code: 'http_503', message: 'HTTP 503' } }
      : { status: 200, terminalEvent: 'message.completed', turnId: 'http-recovered-turn', firstDeltaMs: 1, totalMs: 2, eventCount: 2 }
  },
  resolveAcceptedTurnId: async () => undefined
})
assert.equal(httpTransientOutcome.status, 'completed')
assert.equal(httpTransientSubmissions, 2, 'HTTP 429/5xx 必须按 transient failure 原位恢复')

const exhaustedDelays: number[] = []
const exhaustedOutcome = await runChatLongSessionTurnAttempts({
  maxAttempts: 3,
  sleep: async (delayMs) => { exhaustedDelays.push(delayMs) },
  submit: async (input) => ({ status: 200, terminalEvent: 'message.failed', turnId: `failed-${input.attempt}`, firstDeltaMs: null, totalMs: 1, eventCount: 1, failure: { type: 'message.failed', code: 'gateway_stream_failed', message: 'upstream temporarily unavailable' } }),
  resolveAcceptedTurnId: async () => undefined
})
assert.equal(exhaustedOutcome.status, 'failed')
if (exhaustedOutcome.status !== 'failed') throw new Error('expected exhausted failure')
assert.deepEqual([exhaustedOutcome.status, exhaustedOutcome.reason, exhaustedOutcome.attempts.length], ['failed', 'retry_exhausted', 3])
assert.deepEqual(exhaustedDelays, [2_000, 5_000])
assert.deepEqual(exhaustedOutcome.attempts.map((metric) => metric.delayMs), [0, 2_000, 5_000])
let budgetBoundedSubmitCount = 0
await assert.rejects(
  runChatLongSessionTurnAttempts({
    maxAttempts: 3,
    sleep: async () => { throw new Error('run_budget_aborted') },
    submit: async () => {
      budgetBoundedSubmitCount += 1
      return { status: 200, terminalEvent: 'message.failed', turnId: 'budget-turn', firstDeltaMs: null, totalMs: 1, eventCount: 1, failure: { type: 'message.failed' as const, code: 'gateway_stream_failed', message: 'upstream temporarily unavailable' } }
    },
    resolveAcceptedTurnId: async () => 'budget-turn'
  }),
  /run_budget_aborted/
)
assert.equal(budgetBoundedSubmitCount, 1, 'run budget 中止 backoff 时不得启动下一次 submit')

let deterministicSubmissions = 0
const deterministicOutcome = await runChatLongSessionTurnAttempts({
  maxAttempts: 3,
  sleep: async () => { assert.fail('deterministic failure 不得 backoff') },
  submit: async () => { deterministicSubmissions += 1; return { status: 200, terminalEvent: 'message.failed', turnId: 'deterministic-turn', firstDeltaMs: null, totalMs: 1, eventCount: 1, failure: { type: 'message.failed', code: 'invalid_request_error', message: 'unsupported parameter' } } },
  resolveAcceptedTurnId: async () => undefined
})
assert.equal(deterministicOutcome.status, 'failed')
if (deterministicOutcome.status !== 'failed') throw new Error('expected deterministic failure')
assert.deepEqual([deterministicOutcome.status, deterministicOutcome.reason, deterministicSubmissions], ['failed', 'deterministic_failure', 1])
assert.equal(isTransientChatLongSessionFailure({ type: 'message.failed', code: 'gateway_stream_failed', message: 'unsupported parameter' }), false, '折叠后的 gateway_stream_failed 不能掩盖确定性错误')
assert.equal(isTransientChatLongSessionFailure({ type: 'message.failed', code: 'gateway_stream_failed', message: 'upstream temporarily unavailable' }), true)
assert.equal(isTransientChatLongSessionFailure({ type: 'message.failed', code: 'gateway_json_parser_busy', message: '网关请求解析繁忙，请稍后重试' }), true, 'JSON 解析 worker 明确过载必须允许有界重试')
assert.equal(isTransientChatLongSessionFailure({ type: 'message.failed', code: 'gateway_json_parser_failed', message: '网关请求解析暂时不可用，请稍后重试' }), true, 'JSON 解析 worker 基础设施失败必须允许有界重试')
assert.equal(isTransientChatLongSessionFailure({ type: 'message.failed', code: 'gateway_stream_failed', message: '网关请求解析繁忙，请稍后重试' }), true, '流错误折叠后仍须识别精确的 JSON 解析过载提示')
assert.equal(isTransientChatLongSessionFailure({ type: 'message.failed', code: 'gateway_stream_failed', message: '网关请求解析暂时不可用，请稍后重试' }), true, '流错误折叠后仍须识别精确的 JSON worker 基础设施失败提示')
assert.equal(isTransientChatLongSessionFailure({ type: 'message.failed', code: 'gateway_stream_failed', message: 'maximum 500 tokens' }), false, '裸 5xx 数字不能误判为 HTTP 失败')
assert.equal(isTransientChatLongSessionFailure({ type: 'message.failed', code: 'gateway_stream_failed', message: 'unsupported parameter: timeout' }), false, '参数名 timeout 不能误判为超时')
assert.equal(isTransientChatLongSessionFailure({ type: 'message.failed', code: 'gateway_stream_failed', message: 'HTTP 503 Service Unavailable' }), true)
assert.equal(isTransientChatLongSessionFailure({ type: 'message.failed', code: 'gateway_stream_failed', message: 'upstream timed out' }), true)
assert.equal(isTransientChatLongSessionFailure({ type: 'message.failed', code: 'gateway_stream_failed', message: 'connection reset by peer' }), true)
for (const message of ['terminated', 'stream terminated', 'socket hang up', 'premature close', 'stream prematurely closed', 'read ECONNRESET']) {
  assert.equal(isTransientChatLongSessionFailure({ type: 'message.failed', code: 'gateway_stream_failed', message }), true, `${message} 必须按精确连接终止语义归 transient`)
}
for (const message of [
  'stream terminated: invalid request',
  'terminated by policy violation',
  'socket hang up after unsupported parameter',
  'connection failed: unsupported protocol',
  'socket hang up after unsupported feature',
  'premature close: tool not supported',
  'premature close: invalid_api_key'
]) {
  assert.equal(isTransientChatLongSessionFailure({ type: 'message.failed', code: 'gateway_stream_failed', message }), false, `${message} 的确定性业务语义必须优先`)
}
assert.equal(isTransientChatLongSessionFailure({ type: 'message.failed', code: 'invalid_request_error', message: 'stream terminated' }), false, '确定性 code 必须优先于连接终止 message')
const resumeFixture = fixture.slice(0, 3)
assert.deepEqual(
  buildChatLongSessionAttemptIdentity(50, 1),
  { clientMessageId: 'long-real-50', traceId: 'chat-long-main-50' },
  '首次运行必须保留稳定标识'
)
assert.deepEqual(
  buildChatLongSessionAttemptIdentity(50, 2, 'a1b2c3d4e5f6'),
  { clientMessageId: 'long-real-50-retry-2-resume-a1b2c3d4e5f6', traceId: 'chat-long-main-50-retry-2-resume-a1b2c3d4e5f6' },
  '同一次续跑必须使用 invocation 标识隔离历史幂等记录'
)
assert.notDeepEqual(
  buildChatLongSessionAttemptIdentity(50, 1, 'a1b2c3d4e5f6'),
  buildChatLongSessionAttemptIdentity(50, 1, '001122334455'),
  '不同续跑进程不得复用已取消提交的 clientMessageId'
)
const resumePlan = buildChatLongSessionResumePlan(resumeFixture, [
  { id: 'u1', turnId: 't1', sequenceNo: 1, role: 'user', status: 'completed', contentText: resumeFixture[0]!.prompt },
  { id: 'a1', turnId: 't1', sequenceNo: 2, role: 'assistant', status: 'completed', contentText: 'answer-1' },
  { id: 'u2', turnId: 't2', sequenceNo: 3, role: 'user', status: 'completed', contentText: resumeFixture[1]!.prompt },
  { id: 'a2', turnId: 't2', sequenceNo: 4, role: 'assistant', status: 'failed', contentText: '' }
])
assert.deepEqual({ lastCompletedTurn: resumePlan.lastCompletedTurn, nextTurn: resumePlan.nextTurn, replaceTurnId: resumePlan.replaceTurnId }, { lastCompletedTurn: 1, nextTurn: 2, replaceTurnId: 't2' })
assert.deepEqual(resumePlan.completedResponses, [{ turn: 1, assistantOutput: 'answer-1' }])
const resumeCanonicalHash = chatLongSessionResumeCanonicalHash([
  { id: 'u1', turnId: 't1', sequenceNo: 1, role: 'user', status: 'completed', contentText: resumeFixture[0]!.prompt },
  { id: 'a1', turnId: 't1', sequenceNo: 2, role: 'assistant', status: 'completed', contentText: 'answer-1' }
], 1)
assert.match(resumeCanonicalHash, /^[a-f0-9]{64}$/)
assert(!resumeCanonicalHash.includes('answer-1'), '续跑 canonical 只能保存内容哈希')
assert.throws(() => buildChatLongSessionResumePlan(resumeFixture, [
  { id: 'u1', turnId: 't1', sequenceNo: 1, role: 'user', status: 'completed', contentText: 'tampered prompt' },
  { id: 'a1', turnId: 't1', sequenceNo: 2, role: 'assistant', status: 'completed', contentText: 'answer-1' }
]), /fixture prompt hash mismatch/)
const artifactQualityFailure = chatLongSessionArtifactQualityFailure({
  responseMode: 'artifact',
  assistantOutput: 'x'.repeat(chatLongSessionArtifactMaxBytes + 1)
})
assert.equal(artifactQualityFailure?.code, 'chat_long_session_artifact_too_large')
assert.equal(isTransientChatLongSessionFailure(artifactQualityFailure!), false, 'artifact 超限必须按 deterministic quality failure 处理')
assert.equal(chatLongSessionArtifactQualityFailure({ responseMode: 'artifact', assistantOutput: 'x'.repeat(chatLongSessionArtifactMaxBytes) }), undefined, '32 KiB 精确边界必须允许')
assert.equal(chatLongSessionArtifactQualityFailure({ responseMode: 'artifact', assistantOutput: '中'.repeat(Math.floor(chatLongSessionArtifactMaxBytes / 3)) }), undefined)
assert.equal(chatLongSessionArtifactQualityFailure({ responseMode: 'manifest', assistantOutput: 'x'.repeat(chatLongSessionArtifactMaxBytes + 1) }), undefined, 'manifest 继续使用自身有界 JSON 合同')
assert(!realSource.includes('collectUsage('), '真实 usage 不得从 downstream SSE 事件采集')
assert(realSource.includes('listUsageRecordsAsync'), '真实 usage 必须从持久化 usage repository 按 traceId 读取')
assert(realSource.includes("typeof body.content === 'string' ? body.content : undefined"), 'message.failed 脱敏必须把本轮 prompt 作为动态敏感值')
assert(realSource.includes('assistantOutput || undefined'), 'message.failed 脱敏必须把失败前 output 作为动态敏感值')
assert(realSource.includes('usageRecords.length, 50'), '第 51 次快照前必须断言 50 条主会话成功 usage')
assert(realSource.includes('canonicalHash'), '第 51 次前后必须比较 canonical sorted row hash')
assert(realSource.includes('messageCount, 100'), '第 50 轮必须断言 100 条消息')
assert(realSource.includes('idempotencyCount, 50'), '第 50 轮必须断言 50 条幂等映射')
assert(realSource.includes('completedAssistantCount, 50'), '第 50 轮必须断言 50 条 completed assistant')
assert(realSource.includes('activeTurn, false'), '第 50 轮必须断言无 active turn')
assert(realSource.includes('minimumStableMs: 2_000'), '第 51 次后必须等待至少 2 秒连续稳定')
assert(!realSource.includes("'x'.repeat"), '受控预填不得使用重复 x 伪造上下文')
assert(semanticSeedSource.includes('countChatTextTokens'), '受控预填必须复用生产 tokenizer')
assert(realSource.includes('0.72'), '受控预填目标必须达到有效窗口 72%')
assert(semanticSeedSource.includes('chatLongSessionControlledSeedMaxTokens') && semanticSeedSource.includes('180_000'), '真实受控预填必须限制在稳定的 18 万 token，避免按超大声明窗口压垮上游')
assert(realSource.includes('observedCompactionChange'), '初始 ready 不能直接视为自然压缩完成')
assert(realSource.includes("contextState, 'compacting'"), 'controlled 压缩前必须排除 active compacting claim')
assert.deepEqual(decideChatSubmissionRecovery({ state: 'not_found', attempt: 0, timedOut: false }), { action: 'retry' })
assert.deepEqual(decideChatSubmissionRecovery({ state: 'not_found', attempt: 2, timedOut: false }), { action: 'fail', reason: 'retry_exhausted' })
assert.deepEqual(decideChatSubmissionRecovery({ state: 'accepted', assistantStatus: 'completed', attempt: 1, timedOut: false }), { action: 'recover_completed' })
assert.deepEqual(decideChatSubmissionRecovery({ state: 'preparing', attempt: 1, timedOut: false }), { action: 'wait' })
assert.deepEqual(decideChatSubmissionRecovery({ state: 'accepted', assistantStatus: 'streaming', attempt: 1, timedOut: true }), { action: 'stop' })
assert(realSource.includes('runDeadline'), '真实 50 轮必须设置总运行时限')
assert(realSource.includes('supportedReasoningEfforts.includes(reasoning)'), '每轮发送前必须重新校验 reasoning capability')
assert(realSource.includes('supportedServiceTiers.includes(service)'), '每轮发送前必须重新校验 service capability')
assert(realSource.includes("flag: 'wx'"), '真实报告必须 exclusive create，禁止覆盖同名文件')
assert(realSource.includes('runHash'), '默认报告名必须包含 run hash')
assert(realSource.includes('JUHE_AI_CHAT_REAL_RESUME_ROOT'), '真实长会话必须支持从显式临时根目录安全续跑')
assert(realSource.includes('chatLongSessionFixtureHash') && realSource.includes('chatLongSessionResumeCanonicalHash'), '续跑必须校验 fixture hash 与已完成消息 canonical hash')
assert(realSource.includes('effectiveReplaceTurnId'), '续跑必须原位替换最后一个失败轮，不能重复追加用户轮次')
assert(realSource.includes('runControlledChatTurnWithRetries'), '受控压缩前后真实轮次必须复用临时错误三次重试策略')
assert(realSource.includes('cleanupPreviousControlledConversations'), '受控阶段失败续跑前必须清理旧临时会话，避免重复占用存储配额')
assert(realSource.includes('refreshUnavailableCheckpointUsage'), '续跑必须重新查询已迟到落库的 usage，不能永久复用 unavailable 指标')
assert(realSource.includes('traceId,'), '每轮 checkpoint 必须保留非敏感 traceId 供 usage 延迟自愈')
assert(realSource.includes('withoutChatLongSessionAcceptanceObservability'), '第 51 轮无业务副作用断言必须排除后台模型探针等异步审计增长')
assert(!realSource.includes('upstreamAttempts: dataset.prepare'), '异步审计 attempt 不得进入第 51 轮业务 canonical hash')
assert(realSource.includes('executionSucceeded,') && realSource.includes('shouldRemoveChatLongSessionTemp'), '执行未完成时必须由统一门禁保留临时数据库与有界检查点供续跑')
assert(realSource.includes('assertSafeCheckpointPayload'), '检查点不得写入 prompt、回答或凭据')
assert(gatewayBodyMiddlewareSource.includes("'gateway_json_parser_busy'"), 'JSON parser queue-full 响应必须保留稳定错误码')
assert(gatewayBodyMiddlewareSource.includes("'gateway_json_parser_failed'"), 'JSON parser worker failure 响应必须与容量过载区分')
assert(realSource.includes("child.kill('SIGKILL')"), '非 Windows stop 超时必须升级 SIGKILL')
assert(realSource.includes('chat_long_session_child_stop_failed'), 'child 未确认退出必须使验收失败')
assert(realSource.lastIndexOf('console.log(JSON.stringify(completedSummary))') > realSource.lastIndexOf('await removeTempRoot(tempRoot)'), '成功输出必须发生在临时资源清理之后')
assert(realSource.includes("process.argv.includes('--local-preflight')"), '必须提供不联网 local-preflight')
assert(realSource.includes("'test:chat-gateway-mock-ai'"), 'local-preflight 必须复用本地 mock/backend 全链路')
assert(realSource.includes("mode: 'fixture-preview'"), '--dry-run 必须明确只是 fixture preview')
assert(realSource.includes("process.argv.includes('--real-probe')"), '必须提供只执行一轮且不写正式报告的 real-probe')
assert(realSource.includes("mode: 'real-probe'"), 'real-probe 输出必须明确标记模式')
const probeOutputIndex = realSource.indexOf('if (realProbe) {\n    assert(completedSummary)')
const reportWriteIndex = realSource.indexOf('writeFileSync(completedOutputPath')
const tempRemovalIndex = realSource.indexOf('await removeTempRoot(tempRoot')
assert(probeOutputIndex >= 0 && reportWriteIndex >= 0 && reportWriteIndex < tempRemovalIndex, '正式报告必须在删除可恢复临时数据前写入')
assert(realSource.includes('reportWritten,') && realSource.includes('shouldRemoveChatLongSessionTemp'), '报告写入状态与 real-probe 例外必须交给统一删除门禁判断')
assert(!realSource.includes('sleep(300_000)'), 'local-preflight timeout 必须可取消，不能在成功后保持事件循环 5 分钟')
for (const isolatedEnv of [
  'JUHE_AI_CODEX_WEB_SEARCH_ENDPOINT', 'JUHE_AI_CODEX_WEB_SEARCH_API_KEY',
  'JUHE_AI_IMAGE_GENERATION_PROVIDER_ENDPOINT', 'JUHE_AI_IMAGE_GENERATION_PROVIDER_API_KEY',
  'JUHE_AI_OAUTH_PROXY_URL', 'JUHE_AI_OPENAI_COMPATIBLE_FILES_ROOT', 'JUHE_AI_CODE_INTERPRETER_TEMP_ROOT',
  'JUHE_AI_COMPUTER_BROWSER_ADAPTER_ENDPOINT', 'JUHE_AI_HOSTED_TOOL_CODE_INTERPRETER_MODE',
  'JUHE_AI_HOSTED_TOOL_COMPUTER_MODE', 'JUHE_AI_HOSTED_TOOL_SHELL_MODE', 'JUHE_AI_HOSTED_TOOL_SKILLS_MODE',
  'JUHE_AI_HOSTED_TOOL_TOOL_SEARCH_MODE', 'JUHE_AI_USAGE_RECORD_WRITER_POOL_ENABLED', 'JUHE_AI_SQLITE_READ_WORKER_POOL_SIZE',
  'JUHE_AI_CHAT_UPSTREAM_SSE_MAX_EVENTS'
]) assert(realSource.includes(`${isolatedEnv}:`), `hermetic 缺少基础 .env 覆盖：${isolatedEnv}`)
assert(realSource.includes("buildHermeticJuheEnv(tempRoot, 'server', runtimeConfig.secret)"), 'child 必须覆盖基础 .env process role 并复用父进程 runtime secret')
assert(realSource.includes('AbortSignal.timeout(2_000)'), 'waitForReady 每次 fetch 必须有短超时')
assert(realSource.includes('const naturalCompactionBaseline = controlledContext'), '自然压缩基线必须在两次 follow-up 后读取')
assert(realSource.includes("controlledReason: 'effective_context_limit_unavailable'"), '未知 context limit 必须明确走 controlled')
assert(!realSource.includes('?? 128_000'), '未知 context limit 不得回退 128K 宣称自然候选')
assert(realSource.includes('extendedCompactionDeadline'), '进入 pending/compacting 后必须延长有界等待到 ready')
assert(realSource.includes('runIndependentCleanup'), 'stop/DB close/temp remove 必须独立尝试并聚合错误')
assert(realSource.includes('startWindowsProcessTreeTracker'), '真实 run 必须持续追踪晚生与重父化后代，不能只在启动/ready 快照')
assert(realSource.includes('backendProcessTracker.stop()'), 'cleanup 前必须停止 tracker 并取得最后身份快照')
assert(realSource.includes('15 * 60 * 1000'), '单轮 hard deadline 必须允许最长 15 分钟')
assert(realSource.includes('180 * 1000') && realSource.includes('300 * 1000'), '必须分别限制无非 heartbeat 事件与无实际输出进度')
assert(realSource.includes('recoverAcceptedStreamFailure'), 'deadline 与本地 reader 异常必须权威 stop 并返回可替换终态')
assert((realSource.match(/consumeReaderWithBoundedCancellation/g)?.length ?? 0) >= 3, 'SSE、普通有界响应和 helper import 必须统一异常 cancel 生命周期')
assert(realSource.includes('ChatLongSessionStreamConsumptionError'), 'parser/size/artifact 异常不得进入 15 分钟 generic recovery')
assert((realSource.match(/chatLongSessionArtifactQualityFailure/g)?.length ?? 0) >= 3, 'probe 与主会话 completed checkpoint 都必须执行 32 KiB artifact quality gate')
assert(realSource.indexOf("assert.equal(stream.terminalEvent, 'message.completed', `真实单轮 probe") < realSource.indexOf('const probeQualityFailure'), 'real probe 必须先确认 completed，再执行 artifact quality gate')
assert(realSource.includes('maxAttempts: 3') && realSource.includes('sleep,'), '真实 runner 必须保持三次总尝试并注入 budget-aware sleep')
assert(!realSource.includes('max_output_tokens'), '未确认请求合同前不得自造 max_output_tokens 字段')
assert(realSource.includes('let acceptancePassed = false'), '真实验收必须区分执行完整与质量阈值通过，失败也要能落量化报告')
assert(realSource.includes('acceptance: { passed: acceptancePassed'), '真实报告必须显式记录质量阈值是否通过')
assert(realSource.indexOf('completedReport = report') < realSource.indexOf('if (scoreFailure) throw scoreFailure'), '质量阈值失败必须先构造并登记报告，再抛错')
assert(realSource.includes('shouldRemoveChatLongSessionTemp({'), '临时目录删除必须走可行为验证的统一门禁')
assert(realSource.includes('cleanupHealthy = false'), '任一前置 cleanup 失败必须阻止后续删除诊断目录')
assert(realSource.includes('primaryError: Boolean(primaryError)'), '迟到中断或主流程错误必须进入删除门禁')
assert(realSource.includes("process.once('SIGINT'"), '必须注册 SIGINT 有界清理入口')
assert(realSource.includes("process.once('SIGTERM'"), '必须注册 SIGTERM 有界清理入口')
assert(realSource.includes('streamDeadline'), '每次 stream 必须接收总预算裁剪后的 deadline')
assert(realSource.includes("JUHE_AI_DISABLE_BASE_ENV: 'true'"), 'parent/child/local-preflight 必须统一禁用基础 .env')
assert(realSource.includes('runAbortController.abort'), 'SIGINT/SIGTERM 必须 abort 共享运行信号')
assert(realSource.includes('activeRunBudget'), '所有长等待必须接入统一运行 budget')
assert(realSource.includes('const naturalObservation = controlled.naturalEligible'), 'naturalEligible=false 必须跳过自然等待')

const maliciousEnvRoot = mkdtempSync(resolve(tmpdir(), 'juhe-runtime-env-'))
const maliciousEnvPath = resolve(maliciousEnvRoot, '.env')
try {
  writeFileSync(maliciousEnvPath, 'JUHE_UNLISTED_EXTERNAL_SECRET=must-not-load\nJUHE_AI_CODEX_WEB_SEARCH_ENDPOINT=https://external.invalid\n', 'utf8')
  assert.deepEqual(loadRuntimeBaseEnv(maliciousEnvPath, { JUHE_AI_DISABLE_BASE_ENV: 'true' }), {})
  assert.equal(loadRuntimeBaseEnv(maliciousEnvPath, { JUHE_AI_DISABLE_BASE_ENV: 'false' }).JUHE_UNLISTED_EXTERNAL_SECRET, 'must-not-load')
  assert.throws(() => loadRuntimeBaseEnv(maliciousEnvPath, { JUHE_AI_DISABLE_BASE_ENV: 'yes' }), /JUHE_AI_DISABLE_BASE_ENV/)
} finally { rmSync(maliciousEnvRoot, { recursive: true, force: true }) }

const expiredBudget = new ChatLongSessionRunBudget(Date.now() - 1, new AbortController().signal)
assert.throws(() => expiredBudget.assertActive('expired-test'), /chat_long_session_total_deadline_exceeded/)
const simulatedSignal = new AbortController()
const signalBudget = new ChatLongSessionRunBudget(Date.now() + 60_000, simulatedSignal.signal)
const abortedSleep = signalBudget.sleep(60_000)
simulatedSignal.abort(new Error('simulated_interrupt'))
await assert.rejects(abortedSleep, /simulated_interrupt|aborted/)
const cleanupOrder: string[] = []
const cleanupSecretMarker = 'SECRET_MARKER_7f4a2c'
const primaryCleanupTestError = new Error(`primary_acceptance_failure ${cleanupSecretMarker}`, {
  cause: new Error(`nested ${cleanupSecretMarker}`)
})
Object.assign(primaryCleanupTestError, { diagnosticToken: cleanupSecretMarker })
let sanitizedCleanupError: unknown
try {
  await runIndependentCleanup(primaryCleanupTestError, [
    async () => { cleanupOrder.push('stop'); throw new Error(`stop_failed ${cleanupSecretMarker}`) },
    async () => { cleanupOrder.push('database') },
    async () => { cleanupOrder.push('temp') }
  ], [cleanupSecretMarker])
  assert.fail('cleanup failure must throw')
} catch (error) { sanitizedCleanupError = error }
assert(sanitizedCleanupError instanceof AggregateError)
assert.notEqual(sanitizedCleanupError.cause, primaryCleanupTestError, 'AggregateError cause 不能保留原始错误对象')
const sanitizedCause = sanitizeErrorForDiagnostics(primaryCleanupTestError, [cleanupSecretMarker])
assert.notEqual(sanitizedCause, primaryCleanupTestError)
const renderedCleanupError = [
  String(sanitizedCleanupError),
  inspect(sanitizedCleanupError, { depth: 20 }),
  sanitizedCleanupError.stack ?? '',
  ...sanitizedCleanupError.errors.map((error) => error instanceof Error ? `${error.stack ?? error.message}\n${inspect(error, { depth: 20 })}` : inspect(error, { depth: 20 })),
  sanitizedCause.stack ?? '',
  inspect(sanitizedCause, { depth: 20 })
].join('\n')
assert(!renderedCleanupError.includes(cleanupSecretMarker), '脱敏错误的 format/inspect/stack/object graph 均不得泄漏 secret marker')
assert.deepEqual(cleanupOrder, ['stop', 'database', 'temp'])

const safeBusyDiagnostic = buildSafeBusyCleanupDiagnostic({
  attempt: 2,
  targetPath: 'C:/secret/temp-root/chat.sqlite3-wal',
  holders: [
    { pid: 741, parentPid: process.pid, creationTime: 'worker-v1', commandLine: 'node sqlite-read-worker.ts --secret=never-print' },
    { pid: 742, parentPid: 1, creationTime: 'other-v1', commandLine: 'unrelated.exe --private-path=C:/secret/temp-root' }
  ],
  tracked: [{ pid: 741, parentPid: process.pid, creationTime: 'worker-v1', commandLine: 'node sqlite-read-worker.ts --secret=never-print' }]
})
assert.equal(safeBusyDiagnostic.target.basename, 'chat.sqlite3-wal')
assert.match(safeBusyDiagnostic.target.pathHash, /^[a-f0-9]{12}$/)
assert.deepEqual(safeBusyDiagnostic.holders, [
  { pid: 741, commandCategory: 'sqlite-read-worker', runIdentity: 'tracked' },
  { pid: 742, commandCategory: 'other', runIdentity: 'untracked' }
])
assert(!JSON.stringify(safeBusyDiagnostic).includes('C:/secret'), 'EBUSY 诊断不得包含完整路径')
assert(!JSON.stringify(safeBusyDiagnostic).includes('never-print'), 'EBUSY 诊断不得包含原始命令行')
if (process.platform === 'win32') {
  const timeoutMarker = `chat-cleanup-diagnostic-timeout-${process.pid}-${Date.now()}`
  const scheduledDiagnosticDelays: number[] = []
  let triggerDiagnosticTimeout: (() => void) | undefined
  await assert.rejects(
    runBoundedDiagnosticProcess(process.execPath, [
      '-e',
      "const { spawn } = require('node:child_process'); spawn(process.execPath, ['-e', 'setInterval(() => {}, 1000)', process.argv[1] + '-descendant'], { stdio: 'ignore', windowsHide: true }); process.stdout.write('READY'); setInterval(() => {}, 1000)",
      timeoutMarker
    ], {
      timeoutMs: 2_000,
      maxStdoutBytes: 64,
      maxStderrBytes: 64,
      scheduleTimeout: (callback, delayMs) => {
        scheduledDiagnosticDelays.push(delayMs)
        triggerDiagnosticTimeout = callback
        return () => { triggerDiagnosticTimeout = undefined }
      },
      onStdoutChunk: (text) => { if (text.includes('READY')) triggerDiagnosticTimeout?.() }
    }),
    /chat_long_session_cleanup_diagnostic_timeout/
  )
  assert.deepEqual(scheduledDiagnosticDelays, [2_000], '持锁诊断必须配置 2 秒硬上限且测试不依赖真实墙钟')
  const remainingDiagnosticChildren = (await listWindowsProcessIdentities()).filter((identity) => identity.commandLine.includes(timeoutMarker))
  assert.equal(remainingDiagnosticChildren.length, 0, '诊断超时必须回收其子进程树')
}

let busyCleanupRemaining = 2
const busyCleanupDelays: number[] = []
const busyCleanupDiagnostics: unknown[] = []
await retryBusyCleanup(
  async () => {
    if (busyCleanupRemaining > 0) {
      busyCleanupRemaining -= 1
      throw Object.assign(new Error('temporary busy'), { code: 'EBUSY' })
    }
  },
  {
    maxAttempts: 4,
    baseDelayMs: 10,
    maxDelayMs: 40,
    sleep: async (delayMs) => { busyCleanupDelays.push(delayMs) },
    onBusy: async ({ attempt }) => {
      const diagnostic = { attempt, target: { basename: 'chat.sqlite3', pathHash: '0123456789ab' }, holders: [] }
      busyCleanupDiagnostics.push(diagnostic)
      return diagnostic
    }
  }
)
assert.equal(busyCleanupRemaining, 0)
assert.deepEqual(busyCleanupDelays, [10, 20], 'EBUSY cleanup 必须有限指数退避并最终归零')
assert.equal(busyCleanupDiagnostics.length, 2, '每次 EBUSY 都必须采集一次安全诊断')

const firstPermanentBusyError = Object.assign(new Error('permanent busy'), { code: 'EBUSY' })
let permanentBusyAttempts = 0
await assert.rejects(
  retryBusyCleanup(
    async () => { permanentBusyAttempts += 1; throw permanentBusyAttempts === 1 ? firstPermanentBusyError : Object.assign(new Error('still busy'), { code: 'EBUSY' }) },
    {
      maxAttempts: 3,
      baseDelayMs: 1,
      maxDelayMs: 2,
      sleep: async () => undefined,
      onBusy: ({ attempt }) => ({ attempt, target: { basename: 'locked.sqlite3', pathHash: 'abcdef012345' }, holders: [] })
    }
  ),
  (error: unknown) => error instanceof Error
    && error.cause instanceof Error
    && error.cause !== firstPermanentBusyError
    && `${error.cause.stack ?? error.cause.message}`.includes('locked.sqlite3')
    && !`${error.cause.stack ?? error.cause.message}`.includes('permanent busy')
)
assert.equal(permanentBusyAttempts, 3, '持续 EBUSY 必须在有限次数后停止')

const poolCloseIndex = realSource.indexOf('closeSqliteReadWorkerPool()')
const storageCloseIndex = realSource.lastIndexOf('closeStorageDatabases()')
const tempRemoveIndex = realSource.indexOf('removeTempRoot(tempRoot')
assert(poolCloseIndex >= 0, 'runner cleanup 必须显式关闭自身懒启动的 sqlite read worker pool')
assert(poolCloseIndex < storageCloseIndex && storageCloseIndex < tempRemoveIndex, 'runner cleanup 顺序必须是 read worker pool -> storage databases -> temp root')
assert(realSource.includes('assertTrackedProcessIdentitiesStopped'), 'runner cleanup 必须验证自身 read worker PID 身份已归零')

const trackedTree: TrackedProcessIdentity[] = [
  { pid: 101, parentPid: 0, creationTime: 'root-v1', commandLine: 'pnpm-root' },
  { pid: 102, parentPid: 101, creationTime: 'backend-v1', commandLine: 'tsx-server' },
  { pid: 103, parentPid: 101, creationTime: 'old-worker', commandLine: 'worker-old' }
]
const liveTree = new Map<number, TrackedProcessIdentity>([
  [101, trackedTree[0]!],
  [102, trackedTree[1]!],
  [103, { pid: 103, parentPid: 999, creationTime: 'reused-worker', commandLine: 'unrelated-process' }]
])
const killedPids: number[] = []
let processListCalls = 0
await stopTrackedWindowsProcessTree({
  rootPid: 101,
  tracked: trackedTree,
  servicePort: 34567,
  captureTree: async () => [...liveTree.values()],
  taskkillTree: async () => { liveTree.delete(101); throw new Error('taskkill_spawn_failed') },
  listCurrentProcesses: async () => { processListCalls += 1; return [...liveTree.values()] },
  killPid: async (pid) => { killedPids.push(pid); liveTree.delete(pid) },
  isPortListening: async () => liveTree.has(102),
  timeoutMs: 250,
  pollIntervalMs: 5
})
assert.deepEqual(killedPids, [102], 'fallback 必须杀后端子 PID，且不能误杀已复用 PID')
assert.equal(liveTree.has(103), true, 'PID 身份变化时必须视为复用并跳过')
assert.equal(processListCalls, 4, 'fallback 必须复用单次进程快照，不能按 tracked PID 重复全量枚举 WMI')

const reusedRootTracked: TrackedProcessIdentity[] = [
  { pid: 201, parentPid: 0, creationTime: 'old-root', commandLine: 'old-pnpm-root' }
]
const reusedRootCurrent: TrackedProcessIdentity[] = [
  { pid: 201, parentPid: 0, creationTime: 'new-root', commandLine: 'unrelated-root' },
  { pid: 202, parentPid: 201, creationTime: 'new-child', commandLine: 'unrelated-child' }
]
const reusedRootKills: number[] = []
let reusedRootTaskkillCalled = false
await stopTrackedWindowsProcessTree({
  rootPid: 201,
  tracked: reusedRootTracked,
  captureTree: async () => reusedRootCurrent,
  taskkillTree: async () => { reusedRootTaskkillCalled = true; return 0 },
  listCurrentProcesses: async () => reusedRootCurrent,
  killPid: async (pid) => { reusedRootKills.push(pid) },
  isPortListening: async () => false,
  timeoutMs: 100,
  pollIntervalMs: 5
})
assert.equal(reusedRootTaskkillCalled, false, '根 PID 已复用时不得执行 taskkill /T')
assert.deepEqual(reusedRootKills, [], '根 PID 已复用时不得把新根的后代补进旧进程树')

const descendantOnly = selectTrackedProcessTree(301, [
  { pid: 300, parentPid: 0, creationTime: 'parent', commandLine: 'caller' },
  { pid: 301, parentPid: 300, creationTime: 'root', commandLine: 'spawned-shell' },
  { pid: 302, parentPid: 301, creationTime: 'child', commandLine: 'backend' },
  { pid: 303, parentPid: 302, creationTime: 'grandchild', commandLine: 'worker' },
  { pid: 400, parentPid: 300, creationTime: 'sibling', commandLine: 'test-runner' }
])
assert.deepEqual(descendantOnly.map((item) => item.pid), [301, 302, 303], '进程树快照只能从 spawn 返回的 child identity 向下，不能合并 parent 或 sibling')

let protectedTaskkillCalled = false
let protectedKillCalled = false
const protectedIdentity: TrackedProcessIdentity = {
  pid: process.pid,
  parentPid: process.ppid,
  creationTime: 'protected-current',
  commandLine: 'current-test-process'
}
await assert.rejects(
  stopTrackedWindowsProcessTree({
    rootPid: process.pid,
    tracked: [protectedIdentity],
    captureTree: async () => [protectedIdentity],
    taskkillTree: async () => { protectedTaskkillCalled = true; return 1 },
    listCurrentProcesses: async () => [protectedIdentity],
    killPid: async () => { protectedKillCalled = true },
    isPortListening: async () => false,
    timeoutMs: 10,
    pollIntervalMs: 1
  }),
  /chat_long_session_protected_process_root/
)
assert.equal(protectedTaskkillCalled, false)
assert.equal(protectedKillCalled, false)

for (const assignment of [
  "runtimeConfig.runtimeMode = 'standalone'",
  "runtimeConfig.databaseDriver = 'sqlite'",
  "runtimeConfig.cacheDriver = 'memory'",
  "runtimeConfig.runtimeStateDriver = 'memory'",
  "runtimeConfig.queueDriver = 'memory'",
  'runtimeConfig.postgres.url = undefined',
  'runtimeConfig.redis.cacheUrl = undefined',
  'runtimeConfig.redis.stateUrl = undefined',
  'runtimeConfig.redis.queueUrl = undefined',
  'runtimeConfig.chat.retentionDays = 3',
  'runtimeConfig.chat.maxConversationsPerUser = 50',
  'runtimeConfig.chat.maxTurnsPerConversation = 50'
]) assert(realSource.includes(assignment), `当前进程缺少 hermetic 配置：${assignment}`)

for (const childEnv of [
  "JUHE_AI_RUNTIME_MODE: 'standalone'",
  "JUHE_AI_DATABASE_DRIVER: 'sqlite'",
  "JUHE_AI_CACHE_DRIVER: 'memory'",
  "JUHE_AI_RUNTIME_STATE_DRIVER: 'memory'",
  "JUHE_AI_QUEUE_DRIVER: 'memory'",
  "JUHE_AI_POSTGRES_URL: ''",
  "JUHE_AI_REDIS_CACHE_URL: ''",
  "JUHE_AI_REDIS_STATE_URL: ''",
  "JUHE_AI_REDIS_QUEUE_URL: ''",
  "DATABASE_URL: ''",
  "POSTGRES_URL: ''",
  "PGHOST: ''",
  "PGPORT: ''",
  "PGDATABASE: ''",
  "PGUSER: ''",
  "PGPASSWORD: ''",
  "REDIS_URL: ''",
  "REDIS_HOST: ''",
  "REDIS_PORT: ''",
  "JUHE_AI_CHAT_RETENTION_DAYS: '3'",
  "JUHE_AI_CHAT_MAX_CONVERSATIONS_PER_USER: '50'",
  "JUHE_AI_CHAT_MAX_TURNS_PER_CONVERSATION: '50'",
  "JUHE_AI_CHAT_UPSTREAM_SSE_MAX_EVENTS: '65536'"
]) assert(realSource.includes(childEnv), `child 缺少 hermetic env：${childEnv}`)

assert.equal(resolveChatLongSessionMaxEventCount(undefined), 65_536)
assert.equal(resolveChatLongSessionMaxEventCount('2048'), 2_048)
assert.equal(resolveChatLongSessionMaxEventCount('262144'), 262_144)
for (const invalidEventBudget of ['2047', '262145', '1.5', 'invalid']) {
  assert.throws(() => resolveChatLongSessionMaxEventCount(invalidEventBudget), /JUHE_AI_CHAT_UPSTREAM_SSE_MAX_EVENTS/)
}
const boundedEvents: string[] = []
const boundedParser = createBoundedSseParser({ maxEventCount: 3, maxBufferChars: 1024, onEvent: (event) => boundedEvents.push(event.name) })
boundedParser.push('event: one\ndata: {}\n\nevent: two\ndata: {}\n\n')
boundedParser.push('event: three\ndata: {}\n\n')
assert.deepEqual(boundedEvents, ['one', 'two', 'three'])
assert.throws(
  () => boundedParser.push('event: four\ndata: {}\n\n'),
  /chat_long_session_sse_event_count_exceeded/
)
const mixedEvents: Array<{ name: string; delta?: unknown }> = []
const mixedParser = createBoundedSseParser({ maxEventCount: 10, maxBufferChars: 32, onEvent: (event) => mixedEvents.push({ name: event.name, delta: event.data.delta }) })
mixedParser.push('event: cr\rdata: {}\r\revent: mixed\r\ndata: {}\n\r\n')
assert.deepEqual(mixedEvents.map((event) => event.name), ['cr', 'mixed'])
const utf8Events: unknown[] = []
const utf8Parser = createBoundedSseParser({ maxEventCount: 2, maxBufferChars: 64, onEvent: (event) => utf8Events.push(event.data.delta) })
const utf8Bytes = new TextEncoder().encode('event: message.delta\ndata: {"delta":"中文"}\n\n')
utf8Parser.push(utf8Bytes.slice(0, utf8Bytes.length - 2))
utf8Parser.push(utf8Bytes.slice(utf8Bytes.length - 2))
utf8Parser.finish()
assert.deepEqual(utf8Events, ['中文'])
const malformedTerminal = createBoundedSseParser({ maxEventCount: 2, maxBufferChars: 128, onEvent: () => undefined })
assert.throws(() => malformedTerminal.push('event: message.completed\ndata: {bad}\n\n'), /chat_long_session_sse_terminal_json_invalid/)
const malformedDelta = createBoundedSseParser({ maxEventCount: 2, maxBufferChars: 128, onEvent: () => undefined })
assert.throws(() => malformedDelta.push('event: message.delta\ndata: {bad}\n\n'), /chat_long_session_sse_json_invalid/)
for (const nonRecord of ['null', '[]', '42', '"text"']) {
  const nonRecordParser = createBoundedSseParser({ maxEventCount: 2, maxBufferChars: 128, onEvent: () => undefined })
  assert.throws(() => nonRecordParser.push(`event: message.delta\ndata: ${nonRecord}\n\n`), /chat_long_session_sse_data_not_record/)
}
const manyEvents: string[] = []
const drainFirst = createBoundedSseParser({ maxEventCount: 200, maxBufferChars: 16, onEvent: (event) => manyEvents.push(event.name) })
drainFirst.push(Array.from({ length: 100 }, () => 'event: ok\ndata: {}\n\n').join(''))
assert.equal(manyEvents.length, 100)

assert.equal(fixture.length, 50, '长会话 fixture 必须恰好包含 50 个用户轮次')
assert.deepEqual(fixture.filter((turn) => turn.memoryProbe).map((turn) => turn.turn), [10, 20, 30, 40, 50])
assert.deepEqual(fixture.filter((turn) => turn.controls.model).map((turn) => turn.turn), [16, 31, 41])
assert.deepEqual(fixture.filter((turn) => turn.controls.reasoning).map((turn) => turn.turn), [12, 28, 44])
assert.deepEqual(fixture.filter((turn) => turn.controls.service).map((turn) => turn.turn), [18, 35])
assert.deepEqual(fixture.filter((turn) => turn.responseMode === 'artifact').map((turn) => turn.turn), [1, 5, 10, 16, 17, 20, 25, 30, 31, 38, 40, 41, 43, 47, 50], '完整 artifact 只能出现在锚点、memory probe、模型切换关键轮和 final')
assert(fixture.filter((turn) => turn.responseMode === 'manifest').every((turn) => turn.prompt.includes('introducedFeatureId') && turn.prompt.includes('禁止输出 HTML')), '中间轮必须请求严格有界 manifest')
assert(fixture.filter((turn) => turn.responseMode === 'artifact').every((turn) => turn.prompt.includes('完整有效 HTML')), 'checkpoint 必须请求完整 artifact')
assert(fixture.filter((turn) => turn.responseMode === 'artifact').every((turn) => turn.prompt.includes('32768') && turn.prompt.includes('UTF-8') && turn.prompt.includes('仅返回一个 html 代码块')), 'checkpoint 必须声明统一 32 KiB UTF-8 与单一 HTML fence')
assert(fixture.filter((turn) => turn.responseMode === 'artifact').every((turn) => turn.prompt.includes('禁止解释') && turn.prompt.includes('禁止 HTML/CSS 注释') && turn.prompt.includes('禁止重复示例')), 'checkpoint 必须明确要求完整但高度精简')
assert.deepEqual([...new Set(fixture.map((turn) => turn.stage))], [
  'foundation', 'layout', 'components', 'responsive', 'accessibility', 'fixes', 'refactor', 'final-review'
])
assert(fixture.every((turn) => turn.prompt.includes('Aurora Dashboard')), '50 轮必须持续演进同一个 HTML+CSS 项目')
assert(fixture.every((turn) => turn.requiredFeatureIds.length > 0 && turn.exactAnchors.length > 0))
assert(fixture.every((turn) => turn.prompt.includes(turn.introducedFeatureId)), '每轮 prompt 必须植入当前客观需求 ID')
assert(fixture.every((turn) => turn.prompt.includes(`data-requirements="${turn.requiredFeatureIds.join(' ')}"`)), '每轮 prompt 必须使用单个累计 data-requirements 属性')
assert(fixture.every((turn) => turn.exactAnchors.every((anchor) => turn.prompt.includes(anchor))), '每轮 prompt 必须显式携带累计锚点')

function renderArtifact(turn: (typeof fixture)[number], options: { omitAnchor?: boolean; contradiction?: string; futureAnchor?: string; includeEvidence?: boolean } = {}): string {
  const anchors = options.omitAnchor ? turn.exactAnchors.slice(1) : turn.exactAnchors
  const featureMarkers = turn.requiredFeatureIds.join(' ')
  const evidenceCss = options.includeEvidence === false ? ':root{--surface:#fff}.app-shell{display:grid}' : `
:root{--surface:#fff;--ink:#17202a;--color-surface:#fff;--space-3:12px;--surface-elevated:#f5f7fa}
body{font-family:Arial,sans-serif;overflow-x:hidden}.app-shell{display:grid;grid-template-columns:18rem 1fr}
.content{max-width:1200px}.stats-grid{display:grid;grid-template-columns:repeat(3,1fr)}.stack{display:flex;gap:var(--space-3)}
.copy{overflow-wrap:anywhere}.action-button{min-height:44px;min-width:44px}.target{scroll-margin-top:5rem}
table{table-layout:fixed}h1{font-size:clamp(1.5rem,2rem,2.5rem);text-wrap:balance}:focus-visible{box-shadow:0 0 0 3px #0b57d0}
:where(.metric-card){background:var(--color-surface)}
@media(max-width:1024px){.app-shell{grid-template-columns:14rem 1fr}}
@media(max-width:720px){.app-shell,.stats-grid{grid-template-columns:1fr}.mobile-nav{display:flex}}
@media(prefers-reduced-motion:reduce){*{animation-duration:0.01ms}}
@media print{.mobile-nav{display:none}}`
  const evidenceHtml = options.includeEvidence === false ? '' : `
<a class="skip-link" href="#content">跳至内容</a><header><nav aria-label="主导航"><a aria-current="page">首页</a></nav><div class="mobile-nav">菜单</div></header>
<div class="app-shell"><main id="content" class="content dashboard__content"><section class="overview" aria-labelledby="overview-title"><h1 id="overview-title">概览</h1><div class="stats-grid"><article class="metric-card">指标</article><div class="trend-chart">趋势</div></div></section>
<section class="activity stack target" aria-labelledby="activity-title"><h2 id="activity-title">活动</h2><ul class="activity-list"><li><span class="status-badge">正常</span></li></ul><input type="search" aria-label="搜索活动"><button class="action-button">操作</button><p class="empty-state copy" aria-live="polite">暂无</p></section></main></div><footer data-review="passed"><span id="aurora-acceptance-ready">完成</span></footer>`
  return [
    '```html',
    '<!doctype html>',
    `<html lang="zh-CN" data-requirements="${featureMarkers}">`,
    `<head><style>${evidenceCss}</style></head>`,
    `<body><div id="aurora-dashboard">${anchors.join(' | ')} ${options.futureAnchor ?? ''} ${options.contradiction ?? ''}</div>${evidenceHtml}</body>`,
    '</html>',
    '```'
  ].join('\n')
}

function renderManifest(turn: (typeof fixture)[number], overrides: { introducedFeatureId?: string; includeHtml?: boolean } = {}): string {
  return JSON.stringify({
    introducedFeatureId: overrides.introducedFeatureId ?? turn.introducedFeatureId,
    changeSummary: overrides.includeHtml ? '<html>伪造完整项目</html>' : `完成 ${turn.introducedFeatureId} 的有界实现决策`,
    decisionAnchors: turn.exactAnchors,
    forbiddenConfirmed: turn.forbiddenRegressions.map((item) => item.id)
  })
}

const markerOnlyResponses = fixture.map((turn) => ({ turn: turn.turn, assistantOutput: renderArtifact(turn, { includeEvidence: false }) }))
assert(scoreChatLongSession(fixture, markerOnlyResponses).requirementCompletion < 1, '只有 marker、没有真实 HTML/CSS evidence 不能得到满分')
const commentForgedResponses = fixture.map((turn) => ({
  turn: turn.turn,
  assistantOutput: renderArtifact(turn, { includeEvidence: false }).replace('</body>', `<!-- ${renderArtifact(turn)} --><style>/* ${renderArtifact(turn)} */</style></body>`)
}))
assert(scoreChatLongSession(fixture, commentForgedResponses).requirementCompletion < 0.5, 'HTML/CSS 注释中的伪 evidence 不得计分')
const perfectResponses = fixture.map((turn) => ({ turn: turn.turn, assistantOutput: turn.responseMode === 'artifact' ? renderArtifact(turn) : renderManifest(turn) }))
assert(perfectResponses.filter((response) => fixture[response.turn - 1]?.responseMode === 'artifact').every((response) => Buffer.byteLength(response.assistantOutput, 'utf8') <= chatLongSessionArtifactMaxBytes), 'perfect checkpoint 必须在 32 KiB 内保留全量评分证据')
const oversizedPerfectArtifact = `${renderArtifact(fixture[49])}${'x'.repeat(chatLongSessionArtifactMaxBytes)}`
assert(Buffer.byteLength(oversizedPerfectArtifact, 'utf8') > chatLongSessionArtifactMaxBytes)
assert.equal(chatLongSessionArtifactQualityFailure({ responseMode: 'artifact', assistantOutput: oversizedPerfectArtifact })?.code, 'chat_long_session_artifact_too_large')
assert(perfectResponses.filter((response) => fixture[response.turn - 1]?.responseMode === 'artifact').every((response) => (response.assistantOutput.match(/data-requirements=/g) ?? []).length === 1))
assert(perfectResponses.filter((response) => fixture[response.turn - 1]?.responseMode === 'manifest').every((response) => !/<html|```/i.test(response.assistantOutput)))
assert(perfectResponses.every((response) => !response.assistantOutput.includes('data-requirement=')))
const normalizedForbiddenResponses = [...perfectResponses]
normalizedForbiddenResponses[9] = { turn: 10, assistantOutput: renderArtifact(fixture[9], { contradiction: '<ScRiPt \n>' }) }
assert.equal(scoreChatLongSession(fixture, normalizedForbiddenResponses).firstContradictionTurn, 10, 'forbidden 必须在 checkpoint 忽略大小写与空白变体')

const brokenStructureResponses = [...perfectResponses]
brokenStructureResponses[29] = { turn: 30, assistantOutput: renderArtifact(fixture[29]).replaceAll('app-shell', 'removed-shell') }
assert(scoreChatLongSession(fixture, brokenStructureResponses).artifactContinuity < 1, 'continuity 必须比较相邻版本真实结构')
const perfect = scoreChatLongSession(fixture, perfectResponses)
assert.doesNotThrow(() => assertChatLongSessionScore(perfect))
assert.throws(() => assertChatLongSessionScore({ ...perfect, requirementCompletion: 0.84 }), /requirementCompletion/)
assert.deepEqual(perfect, {
  requirementCompletion: 1,
  decisionRetention: 1,
  anchorPrecision: 1,
  anchorRecall: 1,
  firstOmissionTurn: null,
  firstContradictionTurn: null,
  artifactContinuity: 1,
  finalRequirementCompletion: 1,
  manifestAccuracy: 1
})
const repeatedAttributeResponses = [...perfectResponses]
repeatedAttributeResponses[4] = {
  turn: 5,
  assistantOutput: renderArtifact(fixture[4]).replace('<html ', '<html data-requirements="REQ-01" ')
}
assert(scoreChatLongSession(fixture, repeatedAttributeResponses).requirementCompletion < 1, '重复 data-requirements 属性必须判为无效 artifact')

const omissionResponses = [...perfectResponses]
omissionResponses[24] = { turn: 25, assistantOutput: renderArtifact(fixture[24], { omitAnchor: true }).replace('REQ-01 ', '').replaceAll('app-shell', 'removed-shell') }
const omission = scoreChatLongSession(fixture, omissionResponses)
assert.equal(omission.firstOmissionTurn, 25)
assert(omission.requirementCompletion < 1)
assert(omission.anchorRecall < 1)
assert(omission.decisionRetention < 1)
assert(omission.artifactContinuity < 1)

const contradictionResponses = [...perfectResponses]
contradictionResponses[37] = {
  turn: 38,
  assistantOutput: renderArtifact(fixture[37], { contradiction: fixture[37].forbiddenRegressions[0].needle })
}
assert.equal(scoreChatLongSession(fixture, contradictionResponses).firstContradictionTurn, 38)

const inaccurateManifestResponses = [...perfectResponses]
inaccurateManifestResponses[1] = { turn: 2, assistantOutput: renderManifest(fixture[1], { introducedFeatureId: 'REQ-99' }) }
const inaccurateManifestScore = scoreChatLongSession(fixture, inaccurateManifestResponses)
assert.equal(inaccurateManifestScore.firstOmissionTurn, 2)
assert(inaccurateManifestScore.manifestAccuracy < 1)
const htmlManifestResponses = [...perfectResponses]
htmlManifestResponses[2] = { turn: 3, assistantOutput: renderManifest(fixture[2], { includeHtml: true }) }
assert(scoreChatLongSession(fixture, htmlManifestResponses).manifestAccuracy < 1, 'manifest 不得夹带完整 HTML')
const escapedHtmlManifestResponses = [...perfectResponses]
escapedHtmlManifestResponses[2] = { turn: 3, assistantOutput: renderManifest(fixture[2], { includeHtml: true }).replace('<html>', '\\u003chtml>') }
assert(scoreChatLongSession(fixture, escapedHtmlManifestResponses).manifestAccuracy < 1, 'manifest 不能用 Unicode escape 绕过 HTML 禁令')
const escapedFenceManifestResponses = [...perfectResponses]
escapedFenceManifestResponses[2] = { turn: 3, assistantOutput: renderManifest(fixture[2]).replace('完成', '\\u0060\\u0060\\u0060完成') }
assert(scoreChatLongSession(fixture, escapedFenceManifestResponses).manifestAccuracy < 1, 'manifest 不能用 Unicode escape 绕过 Markdown fence 禁令')
const tildeFenceManifestResponses = [...perfectResponses]
tildeFenceManifestResponses[2] = { turn: 3, assistantOutput: renderManifest(fixture[2]).replace('完成', '~~~完成') }
assert(scoreChatLongSession(fixture, tildeFenceManifestResponses).manifestAccuracy < 1, 'manifest 不能使用波浪线 Markdown fence')
const fullwidthFenceManifestResponses = [...perfectResponses]
fullwidthFenceManifestResponses[2] = { turn: 3, assistantOutput: renderManifest(fixture[2]).replace('完成', '｀｀｀完成') }
assert(scoreChatLongSession(fixture, fullwidthFenceManifestResponses).manifestAccuracy < 1, 'manifest 不能用全角反引号绕过 fence 禁令')

const allFixtureAnchors = [...new Set(fixture.flatMap((turn) => turn.exactAnchors))]
const futureAnchorResponses = fixture.map((turn) => ({
  turn: turn.turn,
  assistantOutput: turn.responseMode === 'artifact'
    ? renderArtifact(turn, { futureAnchor: allFixtureAnchors.filter((anchor) => !turn.exactAnchors.includes(anchor)).join(' ') })
    : renderManifest(turn)
}))
assert(scoreChatLongSession(fixture, futureAnchorResponses).anchorPrecision < 1)
assert.throws(() => assertChatLongSessionScore(scoreChatLongSession(fixture, futureAnchorResponses)), /anchorPrecision/)

const dilutedTailResponses = perfectResponses.map((response, index) => {
  if (index < 35) return response
  let output = response.assistantOutput
  for (let requirement = 36; requirement <= 50; requirement += 1) output = output.replace(` REQ-${requirement}`, '')
  return { ...response, assistantOutput: output }
})
const dilutedTailScore = scoreChatLongSession(fixture, dilutedTailResponses)
assert.equal(dilutedTailScore.firstOmissionTurn, 38)
assert(dilutedTailScore.finalRequirementCompletion < 1, 'checkpoint 评分必须直接捕获尾部累计需求遗漏')
assert.throws(() => assertChatLongSessionScore(dilutedTailScore), /decisionRetention|firstOmissionTurn|finalRequirementCompletion/)

assert.match(extractProjectArtifact('前言\n```html\n<div>ok</div>\n```\n后记') ?? '', /<div>ok<\/div>/)
assert.equal(extractProjectArtifact('没有代码'), null)

const summary = buildSafeFixtureSummary(fixture)
assert.deepEqual(summary, {
  turnCount: 50,
  memoryProbeTurns: [10, 20, 30, 40, 50],
  modelSwitchTurns: [16, 31, 41],
  reasoningSwitchTurns: [12, 28, 44],
  serviceSwitchTurns: [18, 35],
  artifactCheckpointTurns: [1, 5, 10, 16, 17, 20, 25, 30, 31, 38, 40, 41, 43, 47, 50],
  stageCounts: { foundation: 6, layout: 6, components: 8, responsive: 6, accessibility: 6, fixes: 6, refactor: 6, 'final-review': 6 }
})
assert(!JSON.stringify(summary).includes(fixture[0].prompt), 'dry-run 摘要不得输出完整 prompt')

console.log(JSON.stringify({ ok: true, summary }))

function isAwaitMainStatement(statement: ts.Statement): boolean {
  return ts.isExpressionStatement(statement)
    && ts.isAwaitExpression(statement.expression)
    && ts.isCallExpression(statement.expression.expression)
    && ts.isIdentifier(statement.expression.expression.expression)
    && statement.expression.expression.expression.text === 'main'
}
