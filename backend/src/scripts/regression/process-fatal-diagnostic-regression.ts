import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import {
  drainProcessDiagnosticAsyncForTest,
  processDiagnosticAsyncStatsForTest,
  serializeProcessFatalDiagnostic,
  setProcessDiagnosticAsyncWriterForTest,
  writeProcessDiagnosticAsync
} from '../../shared/process-fatal-diagnostic.js'

const secret = 'sk-regression-secret-value'
const diagnostic = serializeProcessFatalDiagnostic({
  event: 'process_uncaught_exception',
  error: new Error(`fatal-marker Authorization: Bearer ${secret} api_key=${secret}`),
  processRole: 'server',
  pid: 123,
  secrets: [secret],
})

assert.equal(diagnostic.endsWith('\n'), true)
assert.equal(diagnostic.split('\n').length, 2, '致命诊断必须保持单行 JSON')
assert.equal(Buffer.byteLength(diagnostic, 'utf8') <= 4096, true, '致命诊断必须有界')
assert.equal(diagnostic.includes(secret), true, '诊断输出必须遵循项目原文保留规则，不得做 sanitizer 替换')

const payload = JSON.parse(diagnostic) as Record<string, unknown>
assert.equal(payload.event, 'process_uncaught_exception')
assert.equal(payload.processRole, 'server')
assert.equal(payload.pid, 123)
assert.match(String(payload.message), /fatal-marker/)
assert.match(String(payload.message), new RegExp(secret))

const namedError = new Error('bounded-message') as Error & { code: string }
namedError.name = `Fatal${secret.repeat(200)}`
namedError.code = `CODE_${secret.repeat(200)}`
const namedDiagnostic = serializeProcessFatalDiagnostic({
  event: 'process_uncaught_exception',
  error: namedError,
  processRole: 'server',
  pid: 456,
  secrets: [secret],
})
assert.equal(namedDiagnostic.includes(secret), true, 'error name/code 在容量范围内也必须保持原文')
assert.equal(Buffer.byteLength(namedDiagnostic, 'utf8') <= 4096, true, '所有字段合计必须保持在 4 KiB 内')
const tinyDiagnostic = serializeProcessFatalDiagnostic({
  event: secret.repeat(100),
  error: namedError,
  processRole: secret.repeat(100),
  pid: 789,
  secrets: [secret],
  maxBytes: 256,
})
assert.equal(Buffer.byteLength(tinyDiagnostic, 'utf8') <= 256, true, '显式更小上限也必须作用于完整 JSON')
for (const thrownValue of [undefined, Symbol('fatal-symbol')]) {
  const nonErrorDiagnostic = serializeProcessFatalDiagnostic({
    event: 'process_uncaught_exception',
    error: thrownValue,
    processRole: 'server',
    pid: 999,
    secrets: [secret],
  })
  assert.doesNotThrow(() => JSON.parse(nonErrorDiagnostic), '任意 thrown value 都必须生成合法 JSON')
  assert.equal(Buffer.byteLength(nonErrorDiagnostic, 'utf8') <= 4096, true)
}

const pendingCallbacks: Array<(error?: Error | null) => void> = []
const asyncLines: string[] = []
setProcessDiagnosticAsyncWriterForTest((line, callback) => {
  asyncLines.push(line)
  pendingCallbacks.push(callback)
})
for (let index = 0; index < 100; index += 1) {
  writeProcessDiagnosticAsync({
    event: `async_diagnostic_${index}`,
    error: new Error(`async failure ${index}`),
    processRole: 'server',
    pid: 123
  })
}
const saturatedStats = processDiagnosticAsyncStatsForTest()
assert.equal(saturatedStats.active, true)
assert.equal(saturatedStats.pending, true)
assert.equal(saturatedStats.dropped, 98)
assert.equal(asyncLines.length, 1, '异步 stderr writer 同时最多只能有一条 in-flight')
pendingCallbacks.shift()?.()
await new Promise<void>((resolve) => setImmediate(resolve))
assert.equal(asyncLines.length, 2, '释放后只补写最后一条有界现场')
pendingCallbacks.shift()?.()
await drainProcessDiagnosticAsyncForTest()
assert.deepEqual(processDiagnosticAsyncStatsForTest(), {
  active: false,
  pending: false,
  dropped: 98,
  pendingBytes: 0
})
setProcessDiagnosticAsyncWriterForTest()

const loggerSource = readFileSync(new URL('../../shared/logger.ts', import.meta.url), 'utf8')
const handlerStart = loggerSource.indexOf("process.on('uncaughtException'")
const outerTry = loggerSource.indexOf('try {', handlerStart)
const stderrWrite = loggerSource.indexOf('writeProcessFatalDiagnostic({', handlerStart)
const structuredFatal = loggerSource.indexOf('logger.fatal(', handlerStart)
const forcedExit = loggerSource.indexOf('setImmediate(() => process.exit(1))', handlerStart)
assert(handlerStart >= 0 && stderrWrite > handlerStart, 'uncaught handler 必须调用同步 stderr 诊断')
assert(outerTry > handlerStart && outerTry < stderrWrite, '同步诊断也必须位于保证 exit 的 try/finally 内')
assert(stderrWrite < structuredFatal, '同步 stderr 诊断必须早于可失败的常规 logger')
assert(structuredFatal < forcedExit, '常规 fatal 日志后必须保证退出')

console.log('进程诊断回归通过：fatal 同步保底原文有界，非 fatal stderr 异步队列有界')
