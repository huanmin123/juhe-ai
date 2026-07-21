import { strict as assert } from 'node:assert'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'

const root = mkdtempSync(join(tmpdir(), 'juhe-ai-runtime-log-import-failure-'))
const logDir = join(root, 'logs')
mkdirSync(logDir)

runtimeConfig.log.directory = logDir
runtimeConfig.log.fileEnabled = true
runtimeConfig.log.consoleEnabled = false
runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'ingest-worker'

const importer = await import('../../modules/runtime-logs/runtime-log-file-import.service.js')
const sourceBodyMarker = 'raw-log-body-must-not-enter-fallback'
const credentialMarker = 'sk-runtime-import-super-secret'
const logPath = join(logDir, 'juhe-ai.worker.20260721T010207Z.00000000-0000-0000-0000-000000000005.log')
writeFileSync(logPath, `${JSON.stringify({ event: 'source-event', msg: sourceBodyMarker })}\n`)

const rootCause = Object.assign(new Error(`cursor connection failed password=${credentialMarker}`), {
  code: 'ECONNRESET'
})
const failure = Object.assign(new Error('runtime log cursor persistence failed', { cause: rootCause }), {
  code: 'CURSOR_WRITE_FAILED'
})
const stderrLines: string[] = []
const originalStderrWrite = process.stderr.write.bind(process.stderr)
process.stderr.write = ((chunk: Uint8Array | string) => {
  stderrLines.push(String(chunk))
  return true
}) as typeof process.stderr.write

try {
  const dependencies = importer.createRuntimeLogFileImportTestDependencies({
    getCursor: async () => undefined,
    getCursorByIdentity: async () => undefined,
    upsertCursor: async () => { throw failure },
    createBatch: async () => undefined,
    batchSize: 17
  })
  await importer.importRuntimeLogFileDeltaForTest({
    path: logPath,
    role: 'worker-rotated',
    kind: 'rotated'
  }, dependencies)

  assert.equal(stderrLines.length, 1, '一次 importer 不可预知失败只能写一条独立 stderr 诊断，不能递归放大')
  const rawLine = stderrLines[0] ?? ''
  assert(rawLine.endsWith('\n'), 'fallback 必须输出单行并以换行结束')
  assert.equal(rawLine.trim().split('\n').length, 1, 'fallback JSON 不得产生物理多行')
  assert.ok(Buffer.byteLength(rawLine, 'utf8') <= 32 * 1024, 'fallback 单行必须有 32KiB 硬上限')
  assert(!rawLine.includes(sourceBodyMarker), 'fallback 不得复制被消费日志的原始正文')
  assert(rawLine.includes(credentialMarker), 'fallback 已捕获的错误原文不得被输出层改写')

  const event = JSON.parse(rawLine) as Record<string, any>
  assert.equal(event.version, 1)
  assert.equal(event.service, 'juhe-ai')
  assert.equal(event.role, 'ingest-worker')
  assert.equal(event.event, 'runtime_log_file_import_failed')
  assert.equal(event.path, logPath)
  assert.equal(event.kind, 'rotated')
  assert.equal(event.fileRole, 'worker-rotated')
  assert.match(event.fileIdentity, /^\d+:\d+:/)
  assert.equal(event.truncationGeneration, 0)
  assert.equal(event.cursorOffset, 0)
  assert.equal(event.batch.configuredSize, 17)
  assert.equal(event.phase, 'cursor.resolve')
  assert.equal(event.error.name, 'Error')
  assert.equal(event.error.message, 'runtime log cursor persistence failed')
  assert.match(event.error.stack, /runtime log cursor persistence failed/)
  assert.equal(event.error.code, 'CURSOR_WRITE_FAILED')
  assert.equal(event.error.cause.name, 'Error')
  assert.match(event.error.cause.message, /cursor connection failed/)
  assert.equal(event.error.cause.code, 'ECONNRESET')
  assert.match(event.error.cause.message, new RegExp(credentialMarker))
  assert.equal(event.error.cause.cause, null, '错误链末端也必须稳定保留 cause 字段')

  stderrLines.length = 0
  const oversizedPath = join(logDir, 'juhe-ai.worker.20260721T010208Z.00000000-0000-0000-0000-000000000006.log')
  writeFileSync(oversizedPath, '{"event":"oversized-error-source"}\n')
  let oversizedCause: Error | undefined
  for (let depth = 4; depth >= 1; depth -= 1) {
    oversizedCause = Object.assign(new Error(`oversized cause ${depth} ${'x'.repeat(20 * 1024)}`, { cause: oversizedCause }), {
      code: `CAUSE_${depth}`
    })
  }
  const oversizedFailure = Object.assign(new Error(`oversized root ${'y'.repeat(20 * 1024)}`, { cause: oversizedCause }), {
    code: 'OVERSIZED_ROOT'
  })
  const oversizedDependencies = importer.createRuntimeLogFileImportTestDependencies({
    getCursor: async () => undefined,
    getCursorByIdentity: async () => undefined,
    upsertCursor: async () => { throw oversizedFailure },
    createBatch: async () => undefined
  })
  await importer.importRuntimeLogFileDeltaForTest({ path: oversizedPath, role: 'worker-rotated', kind: 'rotated' }, oversizedDependencies)
  assert.equal(stderrLines.length, 1)
  const oversizedLine = stderrLines[0] ?? ''
  assert.ok(Buffer.byteLength(oversizedLine, 'utf8') <= 32 * 1024, '超大错误链也必须遵守整行硬上限')
  const oversizedEvent = JSON.parse(oversizedLine) as Record<string, any>
  assert.equal(oversizedEvent.error.code, 'OVERSIZED_ROOT', '整行压缩不能丢失原始根错误 code')
  assert.match(oversizedEvent.error.message, /^oversized root/, '整行压缩不能用泛化错误覆盖根错误 message')
  assert.match(oversizedEvent.error.stack, /^Error: oversized root/, '整行压缩必须保留有界根错误 stack')
  assert.equal(oversizedEvent.error.cause.code, 'CAUSE_1', '整行压缩必须保留有界 cause 链')

  stderrLines.length = 0
  const hostileFailure = new Error('hostile diagnostic error')
  Object.defineProperty(hostileFailure, 'cause', { get: () => { throw new Error('cause getter failed') } })
  const hostileDependencies = importer.createRuntimeLogFileImportTestDependencies({
    getCursor: async () => undefined,
    getCursorByIdentity: async () => undefined,
    upsertCursor: async () => { throw hostileFailure },
    createBatch: async () => undefined
  })
  await importer.importRuntimeLogFileDeltaForTest({ path: logPath, role: 'worker-rotated', kind: 'rotated' }, hostileDependencies)
  assert.equal(stderrLines.length, 1, '诊断序列化失败只能退化写一次固定 stderr JSON')
  const serializationFallback = JSON.parse(stderrLines[0] ?? '') as Record<string, any>
  assert.equal(serializationFallback.error.code, 'DIAGNOSTIC_SERIALIZE_FAILED')
  assert.equal(serializationFallback.phase, 'diagnostic.serialize')

  process.stderr.write = (() => { throw new Error('stderr unavailable') }) as typeof process.stderr.write
  await assert.doesNotReject(
    importer.importRuntimeLogFileDeltaForTest({ path: logPath, role: 'worker-rotated', kind: 'rotated' }, dependencies),
    '独立 stderr fallback 自身失败时不得递归记录或把异常抛回 importer 轮询'
  )

  console.log('运行日志文件 importer 独立失败现场回归通过')
} finally {
  process.stderr.write = originalStderrWrite
  await importer.resetRuntimeLogFileDiscoveryForTest()
  rmSync(root, { recursive: true, force: true })
}
