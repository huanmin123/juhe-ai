import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import type { DatabaseClient } from '../../storage/database-client.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-batch-edit-context-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-batch-edit-context-regression-secret'
runtimeConfig.processRole = 'worker'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  repositories,
  contextRepository,
  databaseClientModule,
  { encryptJson }
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/account-batch-edit-context.repository.js'),
  import('../../storage/database-client.js'),
  import('../../storage/crypto.js')
])

interface CapturedQuery {
  sql: string
  params: readonly unknown[]
}

try {
  const adminAccess = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const group = repositories.createGroup({ providerCode: 'gpt', name: '批量上下文查询预算分组' }, adminAccess)
  const accountIds: string[] = []
  for (let index = 0; index < 100; index += 1) {
    const account = repositories.createAccount({
      providerCode: 'gpt',
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: `批量上下文账户 ${String(index).padStart(3, '0')}`,
      type: 'api_key',
      credentials: {
        api_key: `sk-batch-context-${index}`,
        base_url: 'https://api.openai.com/v1',
        supported_endpoint_modes: ['chat_json', 'responses_sse']
      },
      supportedModels: ['gpt-5.5'],
      healthCheckModel: 'gpt-5.5',
      groupId: group.id
    }, adminAccess)
    accountIds.push(account.id)
  }

  const sqliteQueries: CapturedQuery[] = []
  const sqliteClient = captureQueries(
    databaseClientModule.createSqliteDatabaseClient(databaseModule.getBusinessDatabase()),
    sqliteQueries
  )
  const baseContext = await contextRepository.loadAccountBatchEditContextRecordsWithClientAsync(
    sqliteClient,
    accountIds,
    [],
    adminAccess
  )
  assert.equal(baseContext.length, 100, '100 个账户的基础上下文必须完整返回')
  assert.equal(sqliteQueries.length, 1, '基础上下文必须固定为 1 条批量查询')
  assert.deepEqual(Object.keys(baseContext[0] ?? {}).sort(), [
    'configRevision',
    'id',
    'ownerSystemAccountId',
    'providerCode',
    'providerProtocolProfileId',
    'protocolCode',
    'protocolVersion',
    'type'
  ].sort(), '基础仓储记录只能包含权限校验和前端身份字段')
  assertAccountProjection(sqliteQueries[0]?.sql ?? '', false)

  sqliteQueries.length = 0
  const modelContext = await contextRepository.loadAccountBatchEditContextRecordsWithClientAsync(
    sqliteClient,
    accountIds,
    ['supportedModels', 'modelMappings', 'supportedEndpointModes'],
    adminAccess
  )
  assert.equal(modelContext.length, 100, '100 个账户的模型上下文必须完整返回')
  assert.equal(sqliteQueries.length, 3, '模型上下文查询数必须固定为账户 + 支持模型 + 模型映射三条')
  assertAccountProjection(sqliteQueries[0]?.sql ?? '', true)
  assertRelationProjection(sqliteQueries[1]?.sql ?? '', 'account_supported_models')
  assertRelationProjection(sqliteQueries[2]?.sql ?? '', 'account_model_mappings')
  assert.deepEqual(modelContext[0]?.supportedModels, ['gpt-5.5'], '支持模型必须由一次关系表批量查询装配')
  assert.deepEqual(modelContext[0]?.modelMappings, [], '无映射账户必须返回空映射数组')
  assert.deepEqual(modelContext[0]?.supportedEndpointModes, ['chat_json', 'responses_sse'], '只应从密文中提取 endpoint modes')

  const otherOwner = repositories.createSystemAccount({
    username: 'batch_context_owner',
    displayName: '批量上下文其他用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const otherAccess = { systemAccountId: otherOwner.id, role: 'user' as const }
  const otherGroup = repositories.createGroup({ providerCode: 'gpt', name: '其他用户批量上下文分组' }, otherAccess)
  const otherIds = [0, 1].map((index) => repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: `其他用户批量上下文账户 ${index}`,
    type: 'api_key',
    credentials: { api_key: `sk-other-owner-${index}`, base_url: 'https://api.openai.com/v1' },
    supportedModels: ['gpt-5.5'],
    healthCheckModel: 'gpt-5.5',
    groupId: otherGroup.id
  }, otherAccess).id)
  sqliteQueries.length = 0
  const isolated = await contextRepository.loadAccountBatchEditContextRecordsWithClientAsync(
    sqliteClient,
    [...otherIds, accountIds[0] as string],
    [],
    otherAccess
  )
  assert.deepEqual(isolated.map((item) => item.id), otherIds, '普通用户 SQL 必须在读取时排除其他 owner 账户')
  assert.match(sqliteQueries[0]?.sql ?? '', /AND "system_account_id" = \?/, 'SQLite owner 条件必须进入账户查询 SQL')
  assert.equal(sqliteQueries[0]?.params.at(-1), otherOwner.id, 'SQLite owner 参数必须绑定当前用户')

  const postgresQueries: CapturedQuery[] = []
  const postgresRows = accountIds.map((id) => ({
    id,
    config_revision: 1,
    system_account_id: 'sys_owner',
    provider_code: 'gpt',
    provider_protocol_profile_id: 'openai_v1',
    protocol_code: 'openai',
    protocol_version: 'v1',
    type: 'api_key',
    credentials_encrypted: encryptJson({ supported_endpoint_modes: ['chat_json'] })
  }))
  const postgresClient = fakePostgresClient(postgresRows, postgresQueries, databaseClientModule.postgresDialect)
  const postgresContext = await contextRepository.loadAccountBatchEditContextRecordsWithClientAsync(
    postgresClient,
    accountIds,
    ['supportedModels', 'modelMappings', 'supportedEndpointModes'],
    { systemAccountId: 'sys_owner', role: 'user' }
  )
  assert.equal(postgresContext.length, 100, 'PostgreSQL 100 账户上下文不得拆成逐账户查询')
  assert.equal(postgresQueries.length, 3, 'PostgreSQL 模型上下文查询预算也必须固定为 3')
  assert.match(postgresQueries[0]?.sql ?? '', /FROM "juhe_business"\."accounts"/, 'PostgreSQL 必须使用业务 schema')
  assert.match(postgresQueries[0]?.sql ?? '', /AND "system_account_id" = \$101/, 'PostgreSQL owner 条件必须与 100 个 ID 一起下推')
  assertAccountProjection(postgresQueries[0]?.sql ?? '', true)
  assert.equal(postgresQueries[0]?.params.at(-1), 'sys_owner', 'PostgreSQL owner 参数必须绑定当前用户')

  console.log('account-batch-edit-context-regression passed')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function captureQueries(client: DatabaseClient, output: CapturedQuery[]): DatabaseClient {
  return {
    ...client,
    async query<T extends object = Record<string, unknown>>(sql: string, params: readonly unknown[] = []): Promise<T[]> {
      output.push({ sql, params })
      return client.query<T>(sql, params)
    }
  }
}

function fakePostgresClient(
  accountRows: Array<Record<string, unknown>>,
  output: CapturedQuery[],
  dialect: DatabaseClient['dialect']
): DatabaseClient {
  const client: DatabaseClient = {
    driver: 'postgres',
    dialect,
    async query<T extends object = Record<string, unknown>>(sql: string, params: readonly unknown[] = []): Promise<T[]> {
      const bound = dialect.bind(sql, params)
      output.push({ sql: bound.sql, params: bound.params })
      if (bound.sql.includes('FROM "juhe_business"."accounts"')) return accountRows as T[]
      return []
    },
    async one<T extends object = Record<string, unknown>>(sql: string, params: readonly unknown[] = []): Promise<T | undefined> {
      return (await client.query<T>(sql, params))[0]
    },
    async execute(): Promise<{ changes: number }> {
      throw new Error('批量编辑上下文回归不应执行 DML')
    },
    async transaction<T>(operation: (tx: DatabaseClient) => Promise<T>): Promise<T> {
      return operation(client)
    }
  }
  return client
}

function assertAccountProjection(sql: string, includesEndpointCredentials: boolean): void {
  const projection = sql.slice(sql.indexOf('SELECT') + 'SELECT'.length, sql.indexOf('FROM'))
  for (const forbidden of [
    '*', 'usage', 'today_usage', 'permissions', 'runtime', 'tags', 'current_concurrency',
    'status', 'schedulable', 'cooldown', 'last_error', 'authorization_sources'
  ]) {
    assert.equal(projection.includes(forbidden), false, `账户批量上下文投影不得包含 ${forbidden}`)
  }
  assert.equal(projection.includes('credentials_encrypted'), includesEndpointCredentials, '只有请求 endpoint modes 才能读取密文列')
}

function assertRelationProjection(sql: string, tableName: string): void {
  assert.match(sql, new RegExp(`FROM .*${tableName}`), `必须读取 ${tableName}`)
  assert.equal(sql.slice(sql.indexOf('SELECT'), sql.indexOf('FROM')).includes('*'), false, `${tableName} 不得使用星号投影`)
}
