import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

const loggerSource = readFileSync(new URL('../../shared/logger.ts', import.meta.url), 'utf8')
const serverSource = readFileSync(new URL('../../server.ts', import.meta.url), 'utf8')
const dbServiceSource = readFileSync(new URL('../../db-service.ts', import.meta.url), 'utf8')
const workerSource = readFileSync(new URL('../../worker.ts', import.meta.url), 'utf8')
const runtimeLogQueueSource = readFileSync(new URL('../../modules/runtime-logs/runtime-log-index-queue.service.ts', import.meta.url), 'utf8')
const runtimeLogRedisProducerSource = readFileSync(new URL('../../modules/runtime-logs/runtime-log-redis-producer.ts', import.meta.url), 'utf8')
const runtimeLogProducerExportPattern = /export function enqueueRuntimeLogLine\(/

const violations: string[] = []

forbid(loggerSource, /emitIndexedLines/, 'logger 仍在文件写入热路径逐行扫描运行日志')
forbid(loggerSource, /emitRuntimeLogLine/, 'logger 仍在文件写入热路径投递运行日志索引')
forbid(loggerSource, /RuntimeLogIndexStream/, 'logger 仍在构造运行日志索引 stream')
forbid(loggerSource, /pendingIndexLine/, 'logger 仍在文件写入热路径拼接 pending line Buffer')
forbid(loggerSource, /runtimeLogIndexMaxLineBytes/, 'logger 仍在文件写入热路径执行索引行截断')
forbid(loggerSource, /\.indexOf\(10\s*,/, 'logger 仍在文件写入热路径扫描换行字节')
forbid(loggerSource, /\.toString\(['"]utf8['"]\)/, 'logger 仍在文件写入热路径同步转换 UTF-8')

forbidEntrypoint(serverSource, 'server')
forbidEntrypoint(dbServiceSource, 'db-service')
forbidEntrypoint(workerSource, 'worker')

assert.deepEqual(
  violations,
  [],
  `运行日志热路径仍包含同步索引或 sink 注册：\n- ${violations.join('\n- ')}`
)

assert.match(
  loggerSource,
  /private writeBuffer\([^]*?this\.currentSize \+= buffer\.byteLength\s+this\.stream\.write\(buffer,/,
  'RotatingFileLogStream.writeBuffer 应只更新文件大小并把原始 Buffer 追加到文件 stream'
)
assert.match(runtimeLogQueueSource, /startRuntimeLogRedisStreamConsumer/, '任务 2 不应删除运行日志 Redis consumer 契约')
assert.doesNotMatch('enqueueRuntimeLogLineLocal', runtimeLogProducerExportPattern, '运行日志 Redis producer 断言不得误命中 local queue 入口')
assert.match(runtimeLogQueueSource, runtimeLogProducerExportPattern, '任务 2 不应删除运行日志 Redis producer 入口')
assert.match(runtimeLogRedisProducerSource, /BoundedRuntimeLogRedisProducer/, '任务 2 不应删除运行日志 Redis producer 实现')

console.log('运行日志热路径边界回归通过：业务进程只追加文件，Redis producer/consumer 契约仍保留')

function forbidEntrypoint(source: string, name: string): void {
  forbid(source, /setRuntimeLogLineSink/, `${name} 仍在注册 runtime log index sink`)
  forbid(source, /\benqueueRuntimeLogLine\b/, `${name} 仍在直接注册 runtime log Redis producer`)
  forbid(source, /\bRuntimeLogIndexStream\b/, `${name} 仍在直接构造 RuntimeLogIndexStream`)
  forbid(source, /\bBoundedRuntimeLogRedisProducer\b/, `${name} 仍在直接构造 BoundedRuntimeLogRedisProducer`)
  forbid(source, /\bnew\s+\w*RuntimeLog(?:Redis)?Producer\b/, `${name} 仍在直接构造 runtime log Redis producer`)
}

function forbid(source: string, pattern: RegExp, message: string): void {
  if (pattern.test(source)) {
    violations.push(message)
  }
}
