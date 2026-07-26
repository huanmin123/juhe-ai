import { strict as assert } from 'node:assert'
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'

import { parseOwnerLockEnabled } from '../../config/runtime.js'
import {
  EXPECTED_POSTGRES_GOOSE_SCHEMA_VERSION,
  POSTGRES_GOOSE_CURRENT_VERSION_QUERY,
  enforcePostgresGooseSchemaGate,
  validatePostgresGooseSchemaState,
  type PostgresGooseSchemaGatePool,
  type PostgresGooseSchemaGatePoolConfig
} from '../../storage/postgres-goose-schema-gate.js'

type QueryResult = { rows: Array<{ version_id: string; is_applied: boolean }> }

const postgresConfig = {
  ownerLock: { enabled: true },
  databaseDriver: 'postgres' as const,
  postgres: {
    url: 'postgres://schema-gate.example/juhe_ai',
    connectionTimeoutMs: 4321,
    statementTimeoutMs: 8765,
    lockTimeoutMs: 987
  }
}

await assertMatchesGoMigrationCatalog()
await assertMatchesDeploymentOwnerManifest()
assertOwnerLockEnabledParsing()

await assertDoesNotQueryWhenDisabled()
await assertDoesNotQueryForSqlite()
await assertAcceptsExpectedSchemaAndClosesPool()
await assertAcceptsRolledBackNewerVersion()
assertRejectsInvalidCurrentStates()
await assertRejectsAppliedVersionAboveExpected()
await assertPreservesOperationErrorWhenCloseSucceeds()
await assertThrowsCloseErrorWhenOperationSucceeds()
await assertPreservesOperationAndCloseErrors()
await assertServerStartupOrder()
await assertRuntimeConfigContract()

console.log('PostgreSQL Goose schema 启动门禁回归通过')

async function assertMatchesGoMigrationCatalog(): Promise<void> {
  const catalogSource = await readFile(
    resolve(process.cwd(), '../backend-go/internal/migrationcatalog/catalog.go'),
    'utf8'
  )
  const catalogVersion = Number(
    /^const CurrentSchemaVersion int64 = (\d+)$/mu.exec(catalogSource)?.[1]
  )

  assert(Number.isSafeInteger(catalogVersion), 'Go migration catalog 必须声明有效的当前 schema 版本')
  assert.equal(
    EXPECTED_POSTGRES_GOOSE_SCHEMA_VERSION,
    catalogVersion,
    'Node PostgreSQL Goose schema 启动门禁必须与 Go migration catalog 当前版本保持一致'
  )
}

async function assertMatchesDeploymentOwnerManifest(): Promise<void> {
  const ownerManifest = JSON.parse(
    await readFile(resolve(process.cwd(), '../deploy/owner-manifest.json'), 'utf8')
  ) as { release?: { schemaVersion?: unknown } }
  assert.equal(
    ownerManifest.release?.schemaVersion,
    EXPECTED_POSTGRES_GOOSE_SCHEMA_VERSION,
    '部署 owner manifest 的 release schemaVersion 必须与 Node PostgreSQL Goose schema 启动门禁保持一致'
  )
}

function assertOwnerLockEnabledParsing(): void {
  for (const value of ['true', 'TRUE', 'TrUe']) {
    assert.equal(parseOwnerLockEnabled(value), true, `${value} 必须启用 owner lock`)
  }
  for (const value of [undefined, '', 'false', '1', 'yes', 'on', '0', 'no', 'off']) {
    assert.equal(parseOwnerLockEnabled(value), false, `${String(value)} 不得启用 owner lock`)
  }
}

async function assertDoesNotQueryWhenDisabled(): Promise<void> {
  let poolCreated = false
  await enforcePostgresGooseSchemaGate(
    { ...postgresConfig, ownerLock: { enabled: false } },
    () => {
      poolCreated = true
      throw new Error('disabled gate must not create a PostgreSQL pool')
    }
  )
  assert.equal(poolCreated, false, 'owner lock 关闭时不得查询 PostgreSQL')
}

async function assertDoesNotQueryForSqlite(): Promise<void> {
  let poolCreated = false
  await enforcePostgresGooseSchemaGate(
    { ...postgresConfig, databaseDriver: 'sqlite' },
    () => {
      poolCreated = true
      throw new Error('SQLite mode must not create a PostgreSQL pool')
    }
  )
  assert.equal(poolCreated, false, 'SQLite 正式模式不得连接 PostgreSQL')
}

async function assertAcceptsExpectedSchemaAndClosesPool(): Promise<void> {
  const queryTexts: string[] = []
  const queryValues: Array<readonly unknown[] | undefined> = []
  let receivedPoolConfig: PostgresGooseSchemaGatePoolConfig | undefined
  let ended = false
  const results: QueryResult[] = [
    { rows: [{ version_id: String(EXPECTED_POSTGRES_GOOSE_SCHEMA_VERSION), is_applied: true }] },
    { rows: [] }
  ]

  await enforcePostgresGooseSchemaGate(postgresConfig, (config) => {
    receivedPoolConfig = config
    return {
      async query(text, values) {
        queryTexts.push(text)
        queryValues.push(values)
        return results.shift() ?? { rows: [] }
      },
      async end() {
        ended = true
      }
    }
  })

  assert.deepEqual(receivedPoolConfig, {
    connectionString: postgresConfig.postgres.url,
    connectionTimeoutMillis: postgresConfig.postgres.connectionTimeoutMs,
    max: 1,
    statement_timeout: postgresConfig.postgres.statementTimeoutMs,
    lock_timeout: postgresConfig.postgres.lockTimeoutMs
  })
  assert.equal(queryTexts[0], POSTGRES_GOOSE_CURRENT_VERSION_QUERY)
  assertLatestVersionFoldQuery(queryTexts[0] ?? '')
  assert.match(
    queryTexts[0] ?? '',
    /FROM latest_versions\s+WHERE is_applied = TRUE\s+ORDER BY id DESC\s+LIMIT 1/i
  )
  assertLatestVersionFoldQuery(queryTexts[1] ?? '')
  assert.match(
    queryTexts[1] ?? '',
    /FROM latest_versions\s+WHERE version_id > \$1 AND is_applied = TRUE\s+ORDER BY id DESC\s+LIMIT 1/i
  )
  assert.deepEqual(queryValues[1], [EXPECTED_POSTGRES_GOOSE_SCHEMA_VERSION])
  assert.equal(ended, true, '成功校验后必须关闭连接池')
}

async function assertAcceptsRolledBackNewerVersion(): Promise<void> {
  let ended = false
  const pool = createSequencedPool([
    { rows: [{ version_id: String(EXPECTED_POSTGRES_GOOSE_SCHEMA_VERSION), is_applied: true }] },
    { rows: [] }
  ], () => {
    ended = true
  })

  await enforcePostgresGooseSchemaGate(postgresConfig, () => pool)
  assert.equal(ended, true, '较新版本 up/down 折叠后必须允许期望版本启动并关闭连接池')
}

function assertRejectsInvalidCurrentStates(): void {
  const invalidStates: Array<{
    label: string
    currentRows: QueryResult['rows']
  }> = [
    { label: 'version 56', currentRows: [{ version_id: '56', is_applied: true }] },
    { label: 'version 57', currentRows: [{ version_id: '57', is_applied: true }] },
    { label: 'version 58', currentRows: [{ version_id: '58', is_applied: true }] },
    { label: 'version 67', currentRows: [{ version_id: '67', is_applied: true }] },
    { label: 'version 68', currentRows: [{ version_id: '68', is_applied: true }] },
    { label: 'version 69', currentRows: [{ version_id: '69', is_applied: true }] },
    { label: 'version 70', currentRows: [{ version_id: '70', is_applied: true }] },
    { label: 'version 86', currentRows: [{ version_id: '86', is_applied: true }] },
    { label: 'not applied', currentRows: [{ version_id: String(EXPECTED_POSTGRES_GOOSE_SCHEMA_VERSION), is_applied: false }] },
    { label: 'empty result', currentRows: [] }
  ]

  for (const testCase of invalidStates) {
    assert.throws(
      () => validatePostgresGooseSchemaState({
        currentRows: testCase.currentRows,
        newerAppliedRows: []
      }),
      Error,
      `${testCase.label} 必须拒绝启动`
    )
  }
}

async function assertRejectsAppliedVersionAboveExpected(): Promise<void> {
  let ended = false
  const pool = createSequencedPool([
    { rows: [{ version_id: String(EXPECTED_POSTGRES_GOOSE_SCHEMA_VERSION), is_applied: true }] },
    { rows: [{ version_id: String(EXPECTED_POSTGRES_GOOSE_SCHEMA_VERSION + 1), is_applied: true }] }
  ], () => {
    ended = true
  })

  await assert.rejects(
    enforcePostgresGooseSchemaGate(postgresConfig, () => pool),
    new RegExp(String(EXPECTED_POSTGRES_GOOSE_SCHEMA_VERSION + 1))
  )
  assert.equal(ended, true, '发现高版本记录后必须关闭连接池')
}

async function assertPreservesOperationErrorWhenCloseSucceeds(): Promise<void> {
  const queryError = new Error('synthetic query failure')
  let ended = false
  const pool: PostgresGooseSchemaGatePool = {
    async query() {
      throw queryError
    },
    async end() {
      ended = true
    }
  }

  await assert.rejects(
    enforcePostgresGooseSchemaGate(postgresConfig, () => pool),
    (error: unknown) => error === queryError
  )
  assert.equal(ended, true, '查询异常后必须关闭连接池')
}

async function assertThrowsCloseErrorWhenOperationSucceeds(): Promise<void> {
  const closeError = new Error('synthetic close failure')
  const pool = createSequencedPool([
    { rows: [{ version_id: String(EXPECTED_POSTGRES_GOOSE_SCHEMA_VERSION), is_applied: true }] },
    { rows: [] }
  ], () => {
    throw closeError
  })

  await assert.rejects(
    enforcePostgresGooseSchemaGate(postgresConfig, () => pool),
    (error: unknown) => error === closeError
  )
}

async function assertPreservesOperationAndCloseErrors(): Promise<void> {
  const operationError = new Error('synthetic operation failure')
  const closeError = new Error('synthetic close failure')
  const pool: PostgresGooseSchemaGatePool = {
    async query() {
      throw operationError
    },
    async end() {
      throw closeError
    }
  }

  await assert.rejects(
    enforcePostgresGooseSchemaGate(postgresConfig, () => pool),
    (error: unknown) => {
      assert(error instanceof AggregateError)
      assert.deepEqual(error.errors, [operationError, closeError])
      return true
    }
  )
}

async function assertServerStartupOrder(): Promise<void> {
  const source = await readFile(resolve(process.cwd(), 'src/server.ts'), 'utf8')
  const installIndex = source.indexOf('installProcessLogHandlers()')
  const gateIndex = source.indexOf('await enforcePostgresGooseSchemaGate()', installIndex)
  const eventLoopIndex = source.indexOf('startProcessEventLoopMonitor()', installIndex)
  const dbSupervisorIndex = source.indexOf('startDbServiceSupervisor(', installIndex)
  const workerSupervisorIndex = source.indexOf('startBackgroundWorkerSupervisor()', installIndex)
  const listenIndex = source.indexOf('const server = app.listen(', installIndex)

  assert(installIndex >= 0, 'server 必须安装进程日志处理器')
  assert(gateIndex > installIndex, 'schema gate 必须位于进程日志处理器之后')
  assert.equal(source.includes('setRuntimeLogLineSink('), false, 'server 不得注册 runtime log index sink')
  for (const [label, index] of [
    ['event loop monitor', eventLoopIndex],
    ['DB service supervisor', dbSupervisorIndex],
    ['background worker supervisor', workerSupervisorIndex],
    ['HTTP listen', listenIndex]
  ] as const) {
    assert(index > gateIndex, `schema gate 必须位于 ${label} 之前`)
  }
  assert.equal(source.includes('JUHE_AI_OWNER_LOCK_ENABLED'), false, 'server 不得直接读取 owner lock 环境变量')
}

async function assertRuntimeConfigContract(): Promise<void> {
  const source = await readFile(resolve(process.cwd(), 'src/config/runtime.ts'), 'utf8')
  assert.match(source, /ownerLock:\s*{\s*enabled:\s*parseOwnerLockEnabled\(rawStringConfig\('JUHE_AI_OWNER_LOCK_ENABLED'\)\)\s*}/)
}

function createSequencedPool(results: QueryResult[], onEnd: () => void): PostgresGooseSchemaGatePool {
  return {
    async query() {
      return results.shift() ?? { rows: [] }
    },
    async end() {
      onEnd()
    }
  }
}

function assertLatestVersionFoldQuery(query: string): void {
  assert.match(query, /WITH latest_versions AS \(\s*SELECT DISTINCT ON \(version_id\)/i)
  assert.match(query, /FROM goose_db_version\s+ORDER BY version_id, id DESC\s*\)/i)
}
