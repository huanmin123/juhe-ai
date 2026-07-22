import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

import { redisStreamQueueContracts } from '../../shared/redis-stream-drain.js'

const queueSource = readFileSync(new URL('../../modules/gateway/usage/record-queue.service.ts', import.meta.url), 'utf8')
const writerPoolSource = readFileSync(new URL('../../storage/usage-record-writer-pool.ts', import.meta.url), 'utf8')
const writerWorkerSource = readFileSync(new URL('../../storage/usage-record-writer-worker.ts', import.meta.url), 'utf8')

assert.equal(redisStreamQueueContracts.usageRecords.streamKey, 'juhe-ai:queue:usage-records', 'usage Redis Stream 名称是切流和 drain 的兼容契约')
assert.equal(redisStreamQueueContracts.usageRecords.groupName, 'juhe-ai:usage-record-writers', 'usage Redis Stream consumer group 是切流和 drain 的兼容契约')

assert(queueSource.includes("const usageRecordFlushIntervalMs = 500"), '本地 usage queue 应保持 500ms flush cadence')
assert(queueSource.includes("const usageRecordBatchSize = 1000"), '本地 usage queue 应保持 1000 条 batch 上限')
assert(queueSource.includes("const usageRecordFlushBatchMaxBytes = 8 * 1024 * 1024"), '本地 usage queue 应保持 8 MiB batch 上限')
assert(queueSource.includes("const usageRecordShutdownFlushMaxBatches = 100"), 'shutdown drain 应有 100 batch 上限')
assert(queueSource.includes("const usageRecordQueueMaxItems = 10_000"), '本地 queue 应有 10,000 条保护上限')
assert(queueSource.includes("const usageRecordQueueMaxBytes = 64 * 1024 * 1024"), '本地 queue 应有 64 MiB 保护上限')
assert(queueSource.includes("recordUsageRecordLocalDrop(queued, 'oversize')"), '超大记录当前会被拒绝而非无限保留')
assert(queueSource.includes("recordUsageRecordLocalDrop(queued, 'overflow')"), '本地 queue 满时当前会丢弃新记录')

assert(queueSource.includes("await freezeUsageRecordPricingFactsAsync(queuedInput)"), 'Redis Stream 投递前必须冻结请求时定价事实')
assert(queueSource.includes("高性能模式禁止回退 IPC 或本地队列"), 'Redis Stream 投递失败必须向调用方暴露，禁止降级到另一条 owner 链路')
assert(queueSource.includes("await queue.ack(messages.map((message) => message.id))"), 'Redis Stream 只能在写库成功后 ACK')
assert(queueSource.includes("消息保持 pending 等待重投"), 'Redis Stream 写库失败必须保留 pending 消息')

assert(queueSource.includes("runtimeConfig.processRole === 'server'"), 'server 角色必须把 usage 投递给 ingest-worker')
assert(queueSource.includes("runtimeConfig.processRole === 'db-service'"), 'db-service 默认不应直接写 usage')
assert(queueSource.includes("必须投递 ingest-worker"), '非 owner 角色直接落库必须被拒绝')
assert(queueSource.includes("droppedDispatchCount += 1"), '当前 IPC 不可用会记录并丢弃 usage；这是 Go 接管时必须消除的已知缺陷')
assert(queueSource.includes("normalized.accountId = undefined"), '不完整的账户授权作用域必须整体清空，禁止留下孤立 accountId')
assert(queueSource.includes("normalized.groupId = undefined"), '不完整的分组授权作用域必须整体清空，禁止留下孤立 groupId')
assert(queueSource.includes("normalized.accountAccessType === 'group_authorized'"), '分组授权账户必须同时具有完整的 authorized group 事实')

assert(writerPoolSource.includes("runtimeConfig.databaseDriver === 'sqlite'"), 'child writer pool 仅是 SQLite 单写规避，不得被误迁为 PostgreSQL owner')
assert(writerPoolSource.includes("runtimeConfig.workerRole === 'ingest-worker'"), 'child writer pool 只能由 ingest-worker 启用')
assert(writerPoolSource.includes("usageRecordShardCount() > 1"), '单 shard 不应启动 child writer pool')
assert(writerWorkerSource.includes("{ registerLocation: false }"), 'child writer 不得同时写 usage catalog；catalog 仍由父 ingest worker 单写')

console.log('使用记录 writer/queue Node 契约回归通过：owner、容量、ACK、定价冻结和已知投递丢失语义已冻结')
