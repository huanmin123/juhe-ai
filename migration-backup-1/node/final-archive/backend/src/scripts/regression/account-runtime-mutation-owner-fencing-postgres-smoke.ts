import { strict as assert } from 'node:assert'

import { Pool, type PoolClient } from 'pg'

const postgresUrl = process.env.JUHE_AI_POSTGRES_URL?.trim()
assert(postgresUrl, 'owner runtime mutation fencing PG smoke 需要 JUHE_AI_POSTGRES_URL')

const schemaName = `juhe_owner_runtime_fence_${Date.now().toString(36)}_${Math.random().toString(16).slice(2, 8)}`
assert.match(schemaName, /^juhe_owner_runtime_fence_[a-z0-9]+_[a-f0-9]+$/, '隔离 schema 名称无效')
const schemaSql = quoteIdentifier(schemaName)
const tableSql = `${schemaSql}.accounts`
const pool = new Pool({
  connectionString: postgresUrl,
  application_name: 'juhe-ai:owner-runtime-fencing-smoke',
  max: 5,
  connectionTimeoutMillis: 10_000
})

try {
  await pool.query(`CREATE SCHEMA ${schemaSql}`)
  await pool.query(`
    CREATE TABLE ${tableSql} (
      id text PRIMARY KEY,
      status text NOT NULL,
      config_revision integer NOT NULL,
      dispatch_revision integer NOT NULL,
      schedulable integer NOT NULL,
      last_error_code text,
      last_error_message text,
      last_health_success_at text,
      updated_at text NOT NULL
    )
  `)

  await verifyLateHardStateWins({
    accountId: 'cooldown-race',
    hardStatus: 'disabled',
    manualErrorCode: 'manual_disabled',
    manualErrorMessage: 'manual disabled wins',
    runtimeStatus: 'rate_limited',
    runtimeErrorCode: null,
    runtimeErrorMessage: 'late runtime cooldown'
  })
  await verifyLateHardStateWins({
    accountId: 'exception-race',
    hardStatus: 'error',
    manualErrorCode: 'manual_error',
    manualErrorMessage: 'manual error wins',
    runtimeStatus: 'error',
    runtimeErrorCode: 'late_runtime_error',
    runtimeErrorMessage: 'late runtime exception'
  })
  await verifyCurrentExplicitPolicyStillMutates()
  await verifyPrecheckDispatchRevisionFence()
  await verifyConcurrentSuccessWinsPrecheck()

  console.log(JSON.stringify({
    message: 'owner account runtime mutation fencing PG smoke 通过',
    cooldownRaceProtected: true,
    exceptionRaceProtected: true,
    explicitPolicyPreserved: true,
    precheckFenceProtected: true,
    precheckSuccessWinsTies: true,
    concurrentSuccessWinsPrecheck: true
  }))
} finally {
  if (/^juhe_owner_runtime_fence_[a-z0-9]+_[a-f0-9]+$/.test(schemaName)) {
    await pool.query(`DROP SCHEMA IF EXISTS ${schemaSql} CASCADE`).catch(() => undefined)
  }
  await pool.end()
}

async function verifyLateHardStateWins(input: {
  accountId: string
  hardStatus: 'disabled' | 'error'
  manualErrorCode: string
  manualErrorMessage: string
  runtimeStatus: 'rate_limited' | 'error'
  runtimeErrorCode: string | null
  runtimeErrorMessage: string
}): Promise<void> {
  await seedActiveAccount(input.accountId)
  const blocker = await pool.connect()
  const runtimeWriter = await pool.connect()
  let transactionOpen = false
  try {
    const blockerPid = await backendPid(blocker)
    const runtimeWriterPid = await backendPid(runtimeWriter)
    await blocker.query('BEGIN')
    transactionOpen = true
    await blocker.query(`
      UPDATE ${tableSql}
      SET status = $2,
          config_revision = config_revision + 1,
          schedulable = 0,
          last_error_code = $3,
          last_error_message = $4,
          updated_at = $5
      WHERE id = $1
    `, [input.accountId, input.hardStatus, input.manualErrorCode, input.manualErrorMessage, new Date().toISOString()])

    const observed = await runtimeWriter.query<{ status: string; config_revision: number }>(`
      SELECT status, config_revision
      FROM ${tableSql}
      WHERE id = $1
    `, [input.accountId])
    assert.equal(observed.rows[0]?.status, 'active', 'runtime writer 必须先读到人工事务提交前的 active 快照')
    assert.equal(Number(observed.rows[0]?.config_revision), 1)

    const mutation = runtimeWriter.query(`
      UPDATE ${tableSql}
      SET status = $2,
          schedulable = $3,
          last_error_code = $4,
          last_error_message = $5,
          updated_at = $6
      WHERE id = $1
        AND status = $7
        AND config_revision = $8
    `, [
      input.accountId,
      input.runtimeStatus,
      input.runtimeStatus === 'error' ? 0 : 1,
      input.runtimeErrorCode,
      input.runtimeErrorMessage,
      new Date().toISOString(),
      observed.rows[0]?.status,
      Number(observed.rows[0]?.config_revision)
    ])
    const settledMutation = mutation.then(
      (result) => ({ ok: true as const, result }),
      (error: unknown) => ({ ok: false as const, error })
    )
    await waitForBlockedUpdate(blocker, blockerPid, runtimeWriterPid, settledMutation)
    await blocker.query('COMMIT')
    transactionOpen = false

    const outcome = await settledMutation
    if ('error' in outcome) throw outcome.error
    assert.equal(outcome.result.rowCount, 0, 'PostgreSQL 必须在行锁释放后重新检查 status/config revision 条件')
    const finalRow = await readAccount(input.accountId)
    assert.equal(finalRow.status, input.hardStatus)
    assert.equal(finalRow.schedulable, 0)
    assert.equal(finalRow.last_error_code, input.manualErrorCode)
    assert.equal(finalRow.last_error_message, input.manualErrorMessage)
  } finally {
    if (transactionOpen) {
      await blocker.query('ROLLBACK').catch(() => undefined)
    }
    runtimeWriter.release()
    blocker.release()
  }
}

async function verifyCurrentExplicitPolicyStillMutates(): Promise<void> {
  const accountId = 'explicit-policy'
  await seedActiveAccount(accountId)
  const result = await pool.query(`
    UPDATE ${tableSql}
    SET status = 'error',
        schedulable = 0,
        last_error_code = 'explicit_configured_error',
        last_error_message = 'explicit configured exception',
        updated_at = $2
    WHERE id = $1
      AND status = 'active'
      AND config_revision = 1
  `, [accountId, new Date().toISOString()])
  assert.equal(result.rowCount, 1, '无并发 hard-state 变更时显式策略条件写仍必须执行')
  assert.equal((await readAccount(accountId)).status, 'error')
}

async function verifyPrecheckDispatchRevisionFence(): Promise<void> {
  const observedAt = new Date().toISOString()
  const staleRevisionAccountId = 'precheck-stale-revision'
  await seedActiveAccount(staleRevisionAccountId)
  const staleRevision = await runPrecheckUpdate(staleRevisionAccountId, {
    expectedStatus: 'active',
    expectedDispatchRevision: 2,
    precheckStartedAt: observedAt
  })
  assert.equal(staleRevision.rowCount, 0, 'PG owner UPDATE 必须实际执行 precheck dispatch revision 原子条件')
  assert.equal((await readAccount(staleRevisionAccountId)).status, 'active')

  const staleStatusAccountId = 'precheck-stale-status'
  await seedActiveAccount(staleStatusAccountId)
  const staleStatus = await runPrecheckUpdate(staleStatusAccountId, {
    expectedStatus: 'rate_limited',
    expectedDispatchRevision: 1,
    precheckStartedAt: observedAt
  })
  assert.equal(staleStatus.rowCount, 0, 'PG owner UPDATE 必须原子校验 precheck 开始时的账户状态')
  assert.equal((await readAccount(staleStatusAccountId)).status, 'active')

  const sameMillisecondSuccessAccountId = 'precheck-success-tie'
  await seedActiveAccount(sameMillisecondSuccessAccountId, observedAt)
  const sameMillisecondSuccess = await runPrecheckUpdate(sameMillisecondSuccessAccountId, {
    expectedStatus: 'active',
    expectedDispatchRevision: 1,
    precheckStartedAt: observedAt
  })
  assert.equal(sameMillisecondSuccess.rowCount, 0, '同毫秒真实成功必须优先于迟到 precheck 失败')
  assert.equal((await readAccount(sameMillisecondSuccessAccountId)).status, 'active')

  const currentAccountId = 'precheck-current'
  await seedActiveAccount(currentAccountId, new Date(Date.now() - 60_000).toISOString())
  const current = await runPrecheckUpdate(currentAccountId, {
    expectedStatus: 'active',
    expectedDispatchRevision: 1,
    precheckStartedAt: observedAt
  })
  assert.equal(current.rowCount, 1, '当前 revision/status 且成功水位更早时，precheck 条件写应仍可执行')
  assert.equal((await readAccount(currentAccountId)).status, 'rate_limited')
}

async function verifyConcurrentSuccessWinsPrecheck(): Promise<void> {
  const accountId = 'precheck-concurrent-success'
  await seedActiveAccount(accountId, new Date(Date.now() - 60_000).toISOString())
  const successWriter = await pool.connect()
  const precheckWriter = await pool.connect()
  let transactionOpen = false
  try {
    const successWriterPid = await backendPid(successWriter)
    const precheckWriterPid = await backendPid(precheckWriter)
    const observedAt = new Date().toISOString()
    await successWriter.query('BEGIN')
    transactionOpen = true
    await successWriter.query(`
      UPDATE ${tableSql}
      SET last_health_success_at = $2,
          updated_at = $2
      WHERE id = $1
    `, [accountId, observedAt])

    const mutation = precheckWriter.query(`
      UPDATE ${tableSql}
      SET status = 'rate_limited',
          updated_at = $2
      WHERE id = $1
        AND status = 'active'
        AND config_revision = 1
        AND dispatch_revision = 1
        AND (last_health_success_at IS NULL OR last_health_success_at < $3)
    `, [accountId, new Date().toISOString(), observedAt])
    const settledMutation = mutation.then(
      (result) => ({ ok: true as const, result }),
      (error: unknown) => ({ ok: false as const, error })
    )
    await waitForBlockedUpdate(successWriter, successWriterPid, precheckWriterPid, settledMutation)
    await successWriter.query('COMMIT')
    transactionOpen = false

    const outcome = await settledMutation
    if ('error' in outcome) throw outcome.error
    assert.equal(outcome.result.rowCount, 0, '成功事务提交后，迟到 precheck UPDATE 必须在行锁释放后重检成功水位')
    assert.equal((await readAccount(accountId)).status, 'active')
  } finally {
    if (transactionOpen) {
      await successWriter.query('ROLLBACK').catch(() => undefined)
    }
    precheckWriter.release()
    successWriter.release()
  }
}

function runPrecheckUpdate(accountId: string, input: {
  expectedStatus: 'active' | 'rate_limited'
  expectedDispatchRevision: number
  precheckStartedAt: string
}) {
  return pool.query(`
    UPDATE ${tableSql}
    SET status = 'rate_limited',
        updated_at = $2
    WHERE id = $1
      AND status = $3
      AND config_revision = 1
      AND dispatch_revision = $4
      AND (last_health_success_at IS NULL OR last_health_success_at < $5)
  `, [accountId, new Date().toISOString(), input.expectedStatus, input.expectedDispatchRevision, input.precheckStartedAt])
}

async function seedActiveAccount(accountId: string, lastHealthSuccessAt?: string): Promise<void> {
  await pool.query(`
    INSERT INTO ${tableSql} (
      id, status, config_revision, dispatch_revision, schedulable,
      last_error_code, last_error_message, last_health_success_at, updated_at
    ) VALUES ($1, 'active', 1, 1, 1, NULL, NULL, $2, $3)
  `, [accountId, lastHealthSuccessAt ?? null, new Date(Date.now() - 60_000).toISOString()])
}

async function readAccount(accountId: string): Promise<{
  status: string
  schedulable: number
  last_error_code: string | null
  last_error_message: string | null
}> {
  const result = await pool.query<{
    status: string
    schedulable: number
    last_error_code: string | null
    last_error_message: string | null
  }>(`
    SELECT status, schedulable, last_error_code, last_error_message
    FROM ${tableSql}
    WHERE id = $1
  `, [accountId])
  const row = result.rows[0]
  assert(row, `缺少 smoke account: ${accountId}`)
  return row
}

async function backendPid(client: PoolClient): Promise<number> {
  const result = await client.query<{ pid: number }>('SELECT pg_backend_pid() AS pid')
  const pid = Number(result.rows[0]?.pid)
  assert(Number.isInteger(pid) && pid > 0, '无法读取 PostgreSQL backend pid')
  return pid
}

async function waitForBlockedUpdate<T>(
  observer: PoolClient,
  blockerPid: number,
  runtimeWriterPid: number,
  mutation: Promise<{ ok: true; result: T } | { ok: false; error: unknown }>
): Promise<void> {
  const deadline = Date.now() + 10_000
  while (Date.now() < deadline) {
    const settled = await Promise.race([
      mutation.then((outcome) => ({ settled: true as const, outcome })),
      new Promise<{ settled: false }>((resolve) => setTimeout(() => resolve({ settled: false }), 0))
    ])
    if (settled.settled) {
      if ('error' in settled.outcome) throw settled.outcome.error
      throw new Error('runtime mutation 未等待人工 hard-state 行锁，无法复现 read-then-write 竞态')
    }
    const result = await observer.query<{ blocked: boolean }>(`
      SELECT $1::integer = ANY(pg_blocking_pids($2::integer)) AS blocked
    `, [blockerPid, runtimeWriterPid])
    if (result.rows[0]?.blocked === true) return
    await new Promise((resolve) => setTimeout(resolve, 20))
  }
  throw new Error('等待 runtime mutation 进入账户 UPDATE 行锁超时')
}

function quoteIdentifier(identifier: string): string {
  return `"${identifier.replace(/"/g, '""')}"`
}
