import { strict as assert } from 'node:assert'
import { existsSync, readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const sourceRoot = resolve('src')
const read = (relativePath: string): string => readFileSync(resolve(sourceRoot, relativePath), 'utf8')
const readProject = (relativePath: string): string => readFileSync(resolve('..', relativePath), 'utf8')

const forbiddenProductionReferences = [
  'modules/runtime-logs/runtime-log-index-queue.service.ts',
  'modules/runtime-logs/runtime-log-redis-producer.ts',
  'modules/runtime-logs/runtime-log-stream.ts'
]

for (const relativePath of forbiddenProductionReferences) {
  assert.equal(existsSync(resolve(sourceRoot, relativePath)), false, `${relativePath} 不应继续存在`)
}

for (const relativePath of [
  'worker.ts',
  'modules/background/background-ipc.ts',
  'modules/background/background-ipc.types.ts',
  'modules/db-service/db-service-ipc.ts',
  'modules/db-service/db-service-types.ts',
  'modules/background/background-jobs.ts',
  'scripts/operations/drain-redis-streams.ts'
]) {
  const source = read(relativePath)
  for (const forbidden of [
    'background_worker_runtime_log_line',
    'sendRuntimeLogLineToWorker',
    'enqueueRuntimeLogLine',
    'startRuntimeLogRedisStreamConsumer',
    'stopRuntimeLogRedisStreamConsumer',
    'flushRuntimeLogIndexQueue',
    'runtime-log-index-queue.service',
    'RuntimeLogIndexStream',
    'setRuntimeLogLineSink'
  ]) {
    assert.equal(source.includes(forbidden), false, `${relativePath} 不得引用运行日志 Redis/IPC 队列：${forbidden}`)
  }
}

const drainSource = read('shared/redis-stream-drain.ts')
assert.equal(drainSource.includes('runtimeLogIndex'), false, 'Redis Stream drain contract 不得再声明运行日志队列')

for (const relativePath of [
  'docs/deploy/高性能模式部署指南.md',
  'docs/deploy/macos/macOS部署指南.md'
]) {
  const document = readProject(relativePath)
  for (const forbidden of ['runtime-log-index', '六类 consumer', '六个目标 group', '六条 Stream']) {
    assert.equal(document.includes(forbidden), false, `${relativePath} 不得保留已删除的运行日志 Redis drain 契约：${forbidden}`)
  }
}

const performanceStorageDesign = readProject('docs/functions/PostgreSQL与Redis高性能模式设计.md')
assert.doesNotMatch(
  performanceStorageDesign,
  /Redis queue[^\n]*runtime log/,
  'PostgreSQL/Redis 高性能模式设计不得把普通运行日志声明为 Redis queue 消息'
)

const fileImporterSource = read('modules/runtime-logs/runtime-log-file-import.service.ts')
assert.match(fileImporterSource, /parseRuntimeLogLineForIndex/, '文件导入器必须独占运行日志解析入口')
assert.equal(fileImporterSource.includes('runtime-log-index-queue.service'), false, '文件导入器不得从旧队列模块导入解析器')

console.log('runtime-log-redis-removal-regression: PASS')
