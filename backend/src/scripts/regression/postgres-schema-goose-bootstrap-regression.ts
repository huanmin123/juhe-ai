import { strict as assert } from 'node:assert'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import type { DatabaseClient } from '../../storage/database-client.js'
import {
  runGooseSchemaUp,
  type GooseSchemaUpChildProcess,
  type GooseSchemaUpSpawn
} from '../../storage/postgres-goose-schema-migration.js'
import {
  initializePostgresSchemaWithGoose,
  validatePostgresSchemaBootstrapSource
} from '../../storage/postgres-schema-bootstrap.js'

const scriptDir = path.dirname(fileURLToPath(import.meta.url))
const schemaGateSource = await readFile(path.resolve(scriptDir, '../../storage/postgres-goose-schema-gate.ts'), 'utf8')
const expectedVersionMatch = schemaGateSource.match(/EXPECTED_POSTGRES_GOOSE_SCHEMA_VERSION\s*=\s*(\d+)/)
assert(expectedVersionMatch, '无法从 PostgreSQL Goose schema gate 读取当前版本')
const expectedVersion = Number(expectedVersionMatch[1])

async function assertFreshDatabaseIsAcceptedWithoutLedgerMutation(): Promise<void> {
  const client = new FakeBootstrapClient({ relationCount: 0 })
  await validatePostgresSchemaBootstrapSource(client, expectedVersion)
  assert.equal(client.executions.length, 0)
}

async function assertUnknownUntrackedDatabaseIsRejected(): Promise<void> {
  const client = new FakeBootstrapClient({ relationCount: 3 })
  await assert.rejects(
    validatePostgresSchemaBootstrapSource(client, expectedVersion),
    /没有 Goose 账本但已存在 juhe_ 业务对象/
  )
  assert.equal(client.executions.length, 0)
}

async function assertPartialGooseLedgerCanResume(): Promise<void> {
  const client = new FakeBootstrapClient({ gooseVersion: expectedVersion - 1, relationCount: 9 })
  await validatePostgresSchemaBootstrapSource(client, expectedVersion)
}

async function assertEmptyGooseLedgerIsRejected(): Promise<void> {
  const client = new FakeBootstrapClient({ gooseTablePresent: true, relationCount: 0 })
  await assert.rejects(
    validatePostgresSchemaBootstrapSource(client, expectedVersion),
    /没有已应用版本记录/
  )
}

async function assertNewerGooseVersionIsRejectedBeforeDDL(): Promise<void> {
  const client = new FakeBootstrapClient({ gooseVersion: expectedVersion + 1, relationCount: 9 })
  await assert.rejects(
    validatePostgresSchemaBootstrapSource(client, expectedVersion),
    new RegExp(`Goose 已应用高版本 ${expectedVersion + 1}，当前 catalog 为 ${expectedVersion}`)
  )
  assert.equal(client.executions.length, 0)
}

async function assertInitializationOrderAndUnlock(): Promise<void> {
  const calls: string[] = []
  const client = new FakeBootstrapClient({ relationCount: 0 }, calls)

  const result = await initializePostgresSchemaWithGoose({
    client,
    expectedVersion,
    schemaOnly: false,
    async migrate() {
      calls.push('goose-up')
    },
    async verify() {
      calls.push('verify-gate')
    },
    async applySchema() {
      calls.push('apply-schema')
      return { schemaCount: 6, statementCount: 700 }
    },
    async seed() {
      calls.push('seed')
      return { statementCount: 150 }
    }
  })

  assert.deepEqual(result, {
    schemaCount: 6,
    schemaStatementCount: 700,
    seedStatementCount: 150
  })
  assert.deepEqual(calls, [
    'bootstrap-lock',
    'goose-up',
    'verify-gate',
    'apply-schema',
    'seed',
    'bootstrap-unlock'
  ])

  const schemaOnlyCalls: string[] = []
  const schemaOnlyClient = new FakeBootstrapClient({ gooseVersion: expectedVersion, relationCount: 9 }, schemaOnlyCalls)
  await initializePostgresSchemaWithGoose({
    client: schemaOnlyClient,
    expectedVersion,
    schemaOnly: true,
    async migrate() {
      schemaOnlyCalls.push('goose-up')
    },
    async verify() {
      schemaOnlyCalls.push('verify-gate')
    },
    async applySchema() {
      schemaOnlyCalls.push('apply-schema')
      return { schemaCount: 6, statementCount: 700 }
    },
    async seed() {
      throw new Error('schema-only must not seed')
    }
  })
  assert.deepEqual(schemaOnlyCalls, ['bootstrap-lock', 'goose-up', 'verify-gate', 'apply-schema', 'bootstrap-unlock'])
}

async function assertMigrationFailureNeverAppliesNodeDDL(): Promise<void> {
  const calls: string[] = []
  const client = new FakeBootstrapClient({ relationCount: 0 }, calls)
  await assert.rejects(
    initializePostgresSchemaWithGoose({
      client,
      expectedVersion,
      schemaOnly: true,
      async migrate() {
        calls.push('goose-up')
        throw new Error('synthetic goose failure')
      },
      async verify() {
        calls.push('verify-gate')
      },
      async applySchema() {
        calls.push('apply-schema')
        return { schemaCount: 0, statementCount: 0 }
      },
      async seed() {
        return { statementCount: 0 }
      }
    }),
    /synthetic goose failure/
  )
  assert.deepEqual(calls, ['bootstrap-lock', 'goose-up', 'bootstrap-unlock'])
}

async function assertConcurrentInitializationIsRejected(): Promise<void> {
  const calls: string[] = []
  const client = new FakeBootstrapClient({ relationCount: 0, lockAcquired: false }, calls)
  await assert.rejects(
    initializePostgresSchemaWithGoose({
      client,
      expectedVersion,
      schemaOnly: true,
      async migrate() {
        calls.push('goose-up')
      },
      async verify() {
        calls.push('verify-gate')
      },
      async applySchema() {
        calls.push('apply-schema')
        return { schemaCount: 0, statementCount: 0 }
      },
      async seed() {
        return { statementCount: 0 }
      }
    }),
    /已有 PostgreSQL schema 初始化正在执行/
  )
  assert.deepEqual(calls, ['bootstrap-lock'])
}

async function assertGooseChildProcessContract(): Promise<void> {
  const captures: Array<{
    command: string
    args: readonly string[]
    options: Parameters<GooseSchemaUpSpawn>[2]
  }> = []
  const spawnImpl: GooseSchemaUpSpawn = (command, args, options) => {
    captures.push({ command, args, options })
    return new SuccessfulChildProcess()
  }
  const postgresURL = 'postgres://bootstrap-user:do-not-put-in-argv@127.0.0.1:5432/bootstrap'
  const goBackendRoot = path.resolve(scriptDir, '../../../../backend-go')

  await runGooseSchemaUp({ postgresURL, goBackendRoot, spawnImpl })
  assert.equal(captures[0]?.command, 'go')
  assert.deepEqual(captures[0]?.args.slice(0, 3), ['run', './cmd/juhe-ai-maintenance', 'schema-up'])
  assert.equal(captures[0]?.args.join(' ').includes(postgresURL), false)
  assert.equal(captures[0]?.options.env.JUHE_AI_POSTGRES_URL, postgresURL)
  assert.equal(captures[0]?.options.shell, false)
  assert.equal(captures[0]?.options.cwd, goBackendRoot)

  await runGooseSchemaUp({
    postgresURL,
    maintenanceBinary: 'C:\\opt\\juhe-ai-maintenance.exe',
    migrationDir: 'C:\\catalog',
    spawnImpl
  })
  assert.equal(captures[1]?.command, 'C:\\opt\\juhe-ai-maintenance.exe')
  assert.deepEqual(captures[1]?.args.slice(0, 2), ['schema-up', '--dir'])
  assert.equal(captures[1]?.args.join(' ').includes(postgresURL), false)
}

interface FakeBootstrapState {
  gooseTablePresent?: boolean
  gooseVersion?: number
  lockAcquired?: boolean
  relationCount: number
}

class FakeBootstrapClient implements Pick<DatabaseClient, 'one' | 'execute'> {
  readonly executions: Array<{ sql: string; params: readonly unknown[] }> = []

  constructor(
    private readonly state: FakeBootstrapState,
    private readonly calls?: string[]
  ) {}

  async one<T extends object>(sql: string): Promise<T | undefined> {
    if (/pg_try_advisory_lock/i.test(sql)) {
      this.calls?.push('bootstrap-lock')
      return { acquired: this.state.lockAcquired !== false } as T
    }
    if (/pg_advisory_unlock/i.test(sql)) {
      this.calls?.push('bootstrap-unlock')
      return { released: true } as T
    }
    if (/FROM latest_versions/i.test(sql)) {
      return (this.state.gooseVersion === undefined
        ? undefined
        : { version_id: String(this.state.gooseVersion), is_applied: true }) as T | undefined
    }
    if (/COUNT\(\*\).*relation_count/is.test(sql)) {
      return { relation_count: String(this.state.relationCount) } as T
    }
    if (/to_regclass\('public\.goose_db_version'\)/i.test(sql)) {
      return {
        goose_table: this.state.gooseTablePresent === true || this.state.gooseVersion !== undefined
          ? 'goose_db_version'
          : null
      } as T
    }
    throw new Error(`unexpected one SQL: ${sql}`)
  }

  async execute(sql: string, params: readonly unknown[] = []): Promise<{ changes: number }> {
    this.executions.push({ sql, params })
    return { changes: 1 }
  }
}

class SuccessfulChildProcess implements GooseSchemaUpChildProcess {
  readonly stdout = { on: (_event: 'data', _listener: (chunk: Buffer | string) => void): void => {} }
  readonly stderr = { on: (_event: 'data', _listener: (chunk: Buffer | string) => void): void => {} }

  on(event: 'error' | 'close', listener: ((error: Error) => void) | ((code: number | null) => void)): this {
    if (event === 'close') {
      queueMicrotask(() => (listener as (code: number | null) => void)(0))
    }
    return this
  }
}

await assertFreshDatabaseIsAcceptedWithoutLedgerMutation()
await assertUnknownUntrackedDatabaseIsRejected()
await assertPartialGooseLedgerCanResume()
await assertEmptyGooseLedgerIsRejected()
await assertNewerGooseVersionIsRejectedBeforeDDL()
await assertInitializationOrderAndUnlock()
await assertMigrationFailureNeverAppliesNodeDDL()
await assertConcurrentInitializationIsRejected()
await assertGooseChildProcessContract()

console.log('PostgreSQL Node 初始化与 Goose 账本对齐回归通过')
