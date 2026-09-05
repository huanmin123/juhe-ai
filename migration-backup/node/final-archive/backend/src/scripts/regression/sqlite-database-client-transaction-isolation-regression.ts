import assert from 'node:assert/strict'
import { setImmediate as waitForImmediate } from 'node:timers/promises'
import { DatabaseSync } from 'node:sqlite'

import { createSqliteDatabaseClient } from '../../storage/database-client.js'

const database = new DatabaseSync(':memory:')
database.exec('CREATE TABLE transaction_probe (id TEXT PRIMARY KEY, value TEXT NOT NULL)')

try {
  const firstClient = createSqliteDatabaseClient(database)
  const secondClient = createSqliteDatabaseClient(database)
  let markOuterStarted: (() => void) | undefined
  let releaseOuter: (() => void) | undefined
  const outerStarted = new Promise<void>((resolve) => {
    markOuterStarted = resolve
  })
  const outerRelease = new Promise<void>((resolve) => {
    releaseOuter = resolve
  })

  const outerResult = firstClient.transaction(async (tx) => {
    await tx.execute('INSERT INTO transaction_probe (id, value) VALUES (?, ?)', ['outer', 'rolled-back'])
    markOuterStarted?.()
    await outerRelease
    throw new Error('forced outer rollback')
  }).then(
    () => undefined,
    (error: unknown) => error
  )

  await outerStarted
  let standaloneSettled = false
  const standaloneResult = secondClient
    .execute('INSERT INTO transaction_probe (id, value) VALUES (?, ?)', ['standalone', 'committed'])
    .then((value) => {
      standaloneSettled = true
      return value
    })
  let independentSettled = false
  const independentResult = secondClient.transaction(async (tx) => {
    await tx.execute('INSERT INTO transaction_probe (id, value) VALUES (?, ?)', ['independent', 'committed'])
    return 'committed'
  }).then((value) => {
    independentSettled = true
    return value
  })

  await waitForImmediate()
  assert.equal(standaloneSettled, false, '事务外 SQLite execute 不得串入仍未结束的其他事务')
  assert.equal(independentSettled, false, '独立 SQLite 顶层事务不得加入仍未结束的事务并提前报告成功')

  releaseOuter?.()
  const outerError = await outerResult
  assert.match(String(outerError), /forced outer rollback/, '外层事务应按测试夹具回滚')
  assert.equal((await standaloneResult).changes, 1, '事务外写入应在前一事务回滚后独立提交')
  assert.equal(await independentResult, 'committed', '等待中的独立事务应在前一事务回滚后独立提交')

  const rows = (database.prepare('SELECT id, value FROM transaction_probe ORDER BY id').all() as Array<{ id: string; value: string }>)
    .map((row) => ({ id: row.id, value: row.value }))
  assert.deepEqual(rows, [
    { id: 'independent', value: 'committed' },
    { id: 'standalone', value: 'committed' }
  ], '前一事务回滚不得撤销后续独立写入')

  await firstClient.transaction(async () => {
    await secondClient.transaction(async (nestedTx) => {
      await nestedTx.execute('INSERT INTO transaction_probe (id, value) VALUES (?, ?)', ['nested', 'committed'])
    })
  })
  assert.equal(
    database.prepare('SELECT value FROM transaction_probe WHERE id = ?').get('nested')?.value,
    'committed',
    '同一异步调用链的 SQLite 嵌套事务应复用当前事务且不死锁'
  )

  console.log('sqlite-database-client-transaction-isolation-regression passed')
} finally {
  database.close()
}
