import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('../../storage/usage-records.repository.ts', import.meta.url), 'utf8')
const batchWriter = sourceBetween(source, 'async function createUsageRecordsBatchPostgres', 'async function enrichUsageRecordPricingAsync')
const lockHelper = sourceBetween(source, 'async function lockPostgresUsageRecordBusinessSideEffectAccounts', 'function mergePostgresMaxIsoValue')

const collectIndex = batchWriter.indexOf('collectPostgresUsageRecordBusinessSideEffects(')
const transactionIndex = batchWriter.indexOf('await client.transaction(')
const lockIndex = batchWriter.indexOf('await lockPostgresUsageRecordBusinessSideEffectAccounts(')
const insertIndex = batchWriter.indexOf('await insertPostgresUsageRecordRows(')
const flushIndex = batchWriter.indexOf('await flushPostgresUsageRecordBusinessSideEffects(')

assert.ok(collectIndex >= 0, 'PostgreSQL usage 批写必须先汇总账户副作用')
assert.ok(transactionIndex > collectIndex, '账户副作用汇总必须在事务开始前完成，避免无意义持锁')
assert.ok(lockIndex > transactionIndex, '事务开始后必须先锁定账户副作用行')
assert.ok(insertIndex > lockIndex, 'usage 分区写入必须在账户稳定预锁后执行')
assert.ok(flushIndex > insertIndex, '账户条件更新必须在 usage 写入后执行')
assert.match(lockHelper, /accountLastUsedAt\.keys\(\)[\s\S]*accountHealthSuccessAt\.keys\(\)/, '预锁集合必须覆盖两类账户副作用')
assert.match(lockHelper, /AND deleted_at IS NULL[\s\S]*ORDER BY id[\s\S]*FOR NO KEY UPDATE/, '预锁查询必须忽略已删除账户，并由数据库按 id 稳定获取行锁')

console.log('使用记录 PostgreSQL 锁顺序回归通过：先稳定预锁 accounts，再写 usage，最后刷新账户副作用')

function sourceBetween(sourceText: string, startMarker: string, endMarker: string): string {
  const start = sourceText.indexOf(startMarker)
  const end = sourceText.indexOf(endMarker, start)
  assert.ok(start >= 0, `缺少源码标记：${startMarker}`)
  assert.ok(end > start, `缺少源码结束标记：${endMarker}`)
  return sourceText.slice(start, end)
}
