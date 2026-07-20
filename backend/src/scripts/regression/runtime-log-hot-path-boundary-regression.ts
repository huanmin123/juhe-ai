import { strict as assert } from 'node:assert'
import { existsSync, readFileSync } from 'node:fs'

const read = (path: string): string => readFileSync(new URL(path, import.meta.url), 'utf8')
const loggerSource = read('../../shared/logger.ts')
const serverSource = read('../../server.ts')
const dbServiceSource = read('../../db-service.ts')
const workerSource = read('../../worker.ts')
const fileImporterSource = read('../../modules/runtime-logs/runtime-log-file-import.service.ts')
const parserSource = read('../../modules/runtime-logs/runtime-log-line-parser.ts')

for (const path of [
  '../../modules/runtime-logs/runtime-log-index-queue.service.ts',
  '../../modules/runtime-logs/runtime-log-redis-producer.ts',
  '../../modules/runtime-logs/runtime-log-stream.ts'
]) {
  assert.equal(existsSync(new URL(path, import.meta.url)), false, `${path} 不应存在`)
}

for (const [name, source] of [['logger', loggerSource], ['server', serverSource], ['db-service', dbServiceSource], ['worker', workerSource]] as const) {
  for (const forbidden of ['RuntimeLogIndexStream', 'BoundedRuntimeLogRedisProducer', 'sendRuntimeLogLineToWorker', 'background_worker_runtime_log_line']) {
    assert.equal(source.includes(forbidden), false, `${name} 热路径不得包含 ${forbidden}`)
  }
}
assert.equal(loggerSource.includes('JSON.parse'), false, 'logger 文件写入热路径不得解析运行日志')
assert.equal(loggerSource.includes('createHash'), false, 'logger 文件写入热路径不得计算运行日志哈希')
assert.match(loggerSource, /this\.stream\.write\(buffer,/, 'logger 必须只追加完整 Buffer 到日志文件')
assert.match(fileImporterSource, /parseRuntimeLogLineForIndex/, '文件消费端必须独占解析入口')
assert.match(parserSource, /createHash/, '稳定哈希只能位于文件消费解析模块')
assert.equal(fileImporterSource.includes('runtime-log-index-queue.service'), false, '文件消费端不得依赖旧 Redis 队列模块')

console.log('运行日志热路径边界回归通过：业务进程只追加文件，解析/哈希仅在 ingest-worker 文件消费端执行')
