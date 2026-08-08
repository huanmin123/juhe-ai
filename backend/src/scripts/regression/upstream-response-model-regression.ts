import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { DatabaseSync } from 'node:sqlite'

import { runtimeConfig } from '../../config/runtime.js'
import {
  createUpstreamResponseModelObservation,
  observeUpstreamResponseModelBody,
  upstreamResponseModelProtocolForRequest
} from '../../modules/gateway/observability/upstream-response-model.js'
import { hasUpstreamResponseModelMismatch } from '../../modules/gateway/usage/types.js'
import { logger } from '../../shared/logger.js'

await assertRawUpstreamResponseObservation()
await assertUsageRecordStorage()

console.log('上游响应模型审计回归通过：原始协议观察、映射比较与使用记录持久化均符合预期')

async function assertRawUpstreamResponseObservation(): Promise<void> {
  const openAIObservation = createUpstreamResponseModelObservation({ protocol: 'openai', sse: true })
  const openAISse = [
    'event: response.created\ndata: {"type":"response.created","response":{"model":"gpt-5.6-sol"}}\n\n',
    'event: response.completed\ndata: {"type":"response.completed","response":{"model":"gpt-5.4-mini-2026-03-17"}}\n\n'
  ]
  assert.equal(
    await observedBodyText(openAISse, openAIObservation),
    openAISse.join(''),
    '观察器不得改写 OpenAI SSE 原始响应'
  )
  assert.equal(openAIObservation.model, 'gpt-5.4-mini-2026-03-17', 'OpenAI 终态模型应覆盖先前模型')
  assert.equal(openAIObservation.conflict, true, '同一 OpenAI 流内模型变化应记录冲突')

  const anthropicObservation = createUpstreamResponseModelObservation({ protocol: 'anthropic', sse: true })
  await observedBodyText([
    'event: message_start\ndata: {"type":"message_start","message":{"model":"claude-sonnet-4-20250514"}}\n\n'
  ], anthropicObservation)
  assert.equal(anthropicObservation.model, 'claude-sonnet-4-20250514', 'Anthropic 应读取 message.model')

  const geminiObservation = createUpstreamResponseModelObservation({ protocol: 'gemini', sse: false })
  await observedBodyText([
    '{"candidates":[],"modelVersion":"gemini-2.5-pro"}'
  ], geminiObservation)
  assert.equal(geminiObservation.model, 'gemini-2.5-pro', 'Gemini 应读取 modelVersion')

  assert.equal(
    upstreamResponseModelProtocolForRequest({
      headers: new Headers({ 'anthropic-version': '2023-06-01' }),
      upstreamUrl: 'https://example.test/v1/messages',
      providerCode: 'hybrid'
    }),
    'anthropic',
    '混合账户应以实际 Anthropic 请求头识别原始响应协议'
  )
  assert.equal(
    upstreamResponseModelProtocolForRequest({
      headers: new Headers({ 'x-goog-api-key': 'test' }),
      upstreamUrl: 'https://example.test/v1/models/gemini-2.5-pro:generateContent',
      providerCode: 'hybrid'
    }),
    'gemini',
    '混合账户应以实际 Gemini 请求头识别原始响应协议'
  )
  assert.equal(
    upstreamResponseModelProtocolForRequest({
      headers: new Headers({ 'x-goog-user-project': 'quota-project' }),
      upstreamUrl: 'https://generativelanguage.googleapis.com/v1beta/openai/chat/completions',
      providerCode: 'gemini',
      protocolCode: 'openai-v1'
    }),
    'openai',
    'Gemini OpenAI Chat 档案必须优先采用明确的 OpenAI 协议，不能被 Google 请求头或域名覆盖'
  )
  assert.equal(
    upstreamResponseModelProtocolForRequest({
      headers: new Headers({ 'x-goog-user-project': 'quota-project' }),
      upstreamUrl: 'https://generativelanguage.googleapis.com/v1beta/openai/chat/completions',
      providerCode: 'gemini'
    }),
    'openai',
    '缺少协议档案时仍应根据 Google OpenAI 兼容路径识别 OpenAI 响应格式'
  )
  assert.equal(
    upstreamResponseModelProtocolForRequest({
      headers: new Headers(),
      upstreamUrl: 'https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-pro:generateContent',
      providerCode: 'gemini',
      protocolCode: 'gemini-v1beta'
    }),
    'gemini',
    'Gemini 原生档案仍必须识别为 Gemini 响应格式'
  )
  assert.equal(hasUpstreamResponseModelMismatch('gpt-5.4', 'gpt-5.4'), false, '映射后的实际发送模型相同不得显示不一致')
  assert.equal(hasUpstreamResponseModelMismatch('gpt-5.4', 'gpt-5.4-mini'), true, '上游响应模型不同必须显示不一致')
  assert.equal(hasUpstreamResponseModelMismatch(undefined, 'gpt-5.4-mini'), false, '缺少实际发送模型时不得伪造不一致')
}

async function assertUsageRecordStorage(): Promise<void> {
  const tempRoot = resolve(tmpdir(), `juhe-ai-upstream-response-model-${Date.now()}-${Math.random().toString(16).slice(2)}`)
  runtimeConfig.databaseDriver = 'sqlite'
  runtimeConfig.cacheDriver = 'memory'
  runtimeConfig.queueDriver = 'memory'
  runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
  runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
  runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
  runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
  runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
  runtimeConfig.secret = 'upstream-response-model-regression-secret'
  runtimeConfig.log.consoleEnabled = false
  runtimeConfig.log.fileEnabled = false
  runtimeConfig.processRole = 'worker'
  mkdirSync(tempRoot, { recursive: true })
  logger.level = 'silent'

  const [databaseModule, repositories, usageRecordShards] = await Promise.all([
    import('../../storage/database.js'),
    import('../../storage/repositories.js'),
    import('../../storage/usage-record-shards.js')
  ])
  const createdAt = '2026-08-08T08:00:00.000Z'
  const id = usageRecordShards.generateUsageRecordId(createdAt, 'upstream-response-model')
  try {
    repositories.createUsageRecord({
      id,
      traceId: 'trace-upstream-response-model',
      trafficSource: 'gateway',
      systemAccountId: 'sys_admin',
      endpoint: '/v1/responses',
      providerCode: 'gpt',
      model: 'gpt-5.6-sol',
      upstreamModel: 'gpt-5.4',
      upstreamResponseModel: 'gpt-5.4-mini-2026-03-17',
      success: true,
      statusCode: 200,
      createdAt
    })
    const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
    const detail = repositories.getUsageRecordDetail(id, access)
    assert.equal(detail?.upstreamResponseModel, 'gpt-5.4-mini-2026-03-17', '详情必须返回原始上游响应模型')
    assert.equal(detail?.upstreamModelMismatch, true, '详情必须使用持久化实际发送模型计算不一致')
    const list = repositories.listUsageRecords(access, { traceId: 'trace-upstream-response-model' })
    assert.equal(list.items[0]?.upstreamResponseModel, 'gpt-5.4-mini-2026-03-17', '列表必须返回原始上游响应模型')
    assert.equal(list.items[0]?.upstreamModelMismatch, true, '列表必须返回后端计算的不一致标记')

    const legacyShardLocation = usageRecordShards.usageRecordShardLocationFromKey('20260809:s15')
    assert(legacyShardLocation, '应能解析用于升级验证的历史 shard 位置')
    mkdirSync(resolve(legacyShardLocation.filePath, '..'), { recursive: true })
    const legacyDatabase = new DatabaseSync(legacyShardLocation.filePath)
    usageRecordShards.applyUsageRecordShardBaseSchema(legacyDatabase)
    legacyDatabase.exec('ALTER TABLE usage_records DROP COLUMN upstream_response_model')
    legacyDatabase.close()
    usageRecordShards.closeUsageRecordShardDatabases()
    runtimeConfig.workerRole = 'temporary-maintenance-worker'
    const migratedDatabase = usageRecordShards.getUsageRecordShardDatabase(legacyShardLocation, { registerLocation: false })
    const migratedColumns = migratedDatabase.prepare('PRAGMA table_info(usage_records)').all() as Array<{ name?: string }>
    assert(migratedColumns.some((column) => column.name === 'upstream_response_model'), '历史 SQLite shard 必须离线补齐上游响应模型列')
  } finally {
    try {
      databaseModule.closeStorageDatabases()
    } catch {
    }
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

async function observedBodyText(
  chunks: string[],
  observation: ReturnType<typeof createUpstreamResponseModelObservation>
): Promise<string> {
  const source = {
    async *[Symbol.asyncIterator](): AsyncGenerator<Uint8Array> {
      for (const chunk of chunks) {
        const bytes = Buffer.from(chunk, 'utf8')
        const midpoint = Math.max(1, Math.floor(bytes.length / 2))
        yield bytes.subarray(0, midpoint)
        yield bytes.subarray(midpoint)
      }
    }
  }
  const forwarded: Buffer[] = []
  for await (const chunk of observeUpstreamResponseModelBody(source, observation)) {
    forwarded.push(Buffer.from(chunk))
  }
  return Buffer.concat(forwarded).toString('utf8')
}
