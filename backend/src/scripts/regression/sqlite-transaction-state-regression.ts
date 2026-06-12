import { strict as assert } from 'node:assert'
import type { DatabaseSync } from 'node:sqlite'

import {
  beginDatabaseTransaction,
  beginImmediateDatabaseTransaction,
  commitDatabaseTransaction,
  rollbackDatabaseTransaction,
  runAfterDatabaseCommit
} from '../../storage/database.js'

class UnreliableTransactionStateDatabase {
  readonly isTransaction = false
  readonly operations: string[] = []
  private actualTransactionOpen = false

  exec(sql: string): void {
    const normalized = sql.trim().toUpperCase()
    if (normalized === 'BEGIN' || normalized === 'BEGIN IMMEDIATE') {
      if (this.actualTransactionOpen) {
        throw new Error('cannot start a transaction within a transaction')
      }
      this.actualTransactionOpen = true
      this.operations.push(normalized)
      return
    }
    if (normalized === 'COMMIT') {
      assert.equal(this.actualTransactionOpen, true, 'COMMIT should only run for the outer transaction')
      this.actualTransactionOpen = false
      this.operations.push(normalized)
      return
    }
    if (normalized === 'ROLLBACK') {
      assert.equal(this.actualTransactionOpen, true, 'ROLLBACK should only run for the outer transaction')
      this.actualTransactionOpen = false
      this.operations.push(normalized)
      return
    }
    throw new Error(`Unexpected SQL in fake database: ${sql}`)
  }
}

const committedDatabase = new UnreliableTransactionStateDatabase() as unknown as DatabaseSync
let commitEffectCount = 0
const outerTransaction = beginDatabaseTransaction(committedDatabase)
const nestedTransaction = beginDatabaseTransaction(committedDatabase)
assert.equal(outerTransaction, true, 'outer transaction should be started by the helper')
assert.equal(nestedTransaction, false, 'nested transaction should reuse the active helper transaction')
runAfterDatabaseCommit(() => {
  commitEffectCount += 1
}, committedDatabase)
commitDatabaseTransaction(committedDatabase, nestedTransaction)
assert.equal(commitEffectCount, 0, 'after-commit effects must wait for the outer commit')
commitDatabaseTransaction(committedDatabase, outerTransaction)
assert.equal(commitEffectCount, 1, 'after-commit effects should run after the outer commit')
assert.deepEqual(
  (committedDatabase as unknown as UnreliableTransactionStateDatabase).operations,
  ['BEGIN', 'COMMIT'],
  'nested helper calls must not emit a second BEGIN'
)

const rolledBackDatabase = new UnreliableTransactionStateDatabase() as unknown as DatabaseSync
let rollbackEffectCount = 0
const outerImmediateTransaction = beginImmediateDatabaseTransaction(rolledBackDatabase)
const nestedDeferredTransaction = beginDatabaseTransaction(rolledBackDatabase)
assert.equal(outerImmediateTransaction, true, 'outer immediate transaction should be started by the helper')
assert.equal(nestedDeferredTransaction, false, 'nested deferred transaction should reuse the immediate transaction')
runAfterDatabaseCommit(() => {
  rollbackEffectCount += 1
}, rolledBackDatabase)
rollbackDatabaseTransaction(rolledBackDatabase, nestedDeferredTransaction)
rollbackDatabaseTransaction(rolledBackDatabase, outerImmediateTransaction)
assert.equal(rollbackEffectCount, 0, 'after-commit effects should be discarded on rollback')
assert.deepEqual(
  (rolledBackDatabase as unknown as UnreliableTransactionStateDatabase).operations,
  ['BEGIN IMMEDIATE', 'ROLLBACK'],
  'nested rollback path must not emit a second BEGIN'
)

console.log('SQLite transaction state regression passed: nested helper calls are guarded without relying on DatabaseSync.isTransaction')
