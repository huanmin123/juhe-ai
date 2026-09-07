import assert from 'node:assert/strict'

import {
  assertBusinessSchemaPresent,
  classifyTargetNewTables,
  compareKeyedRows,
  databaseIdentitiesMatch,
  normalizeKnownBusinessTypeEvolutionValue,
  rowDigest,
  stableJson,
  withConnectedClients
} from '../operations/verify-business-data-preservation.js'

assert.doesNotThrow(() => assertBusinessSchemaPresent('source', ['accounts', 'system_accounts']))
assert.throws(
  () => assertBusinessSchemaPresent('target', []),
  /缺少 system_accounts 基准表/,
  '空库或错误 schema 不得被误报为业务数据完整'
)

assert.equal(
  databaseIdentitiesMatch(
    { database_name: 'juhe_ai', database_oid: '16384', server_address: null, server_port: null },
    { database_name: 'juhe_ai', database_oid: '16384', server_address: '127.0.0.1', server_port: 5432 }
  ),
  true,
  '不同连接串解析到同一 PostgreSQL 数据库时必须可识别'
)
assert.equal(
  databaseIdentitiesMatch(
    { database_name: 'juhe_ai', database_oid: '16384', server_address: '127.0.0.1', server_port: 5432 },
    { database_name: 'juhe_ai_after', database_oid: '16385', server_address: '127.0.0.1', server_port: 5432 }
  ),
  false,
  '同服务器上的不同数据库必须允许对账'
)

let operationCalled = false
let closeCount = 0
await assert.rejects(
  withConnectedClients([
    {
      async connect() {},
      async end() { closeCount += 1 }
    },
    {
      async connect() { throw new Error('synthetic connection failure') },
      async end() { closeCount += 1 }
    }
  ], async () => {
    operationCalled = true
  }),
  AggregateError,
  '任一 PostgreSQL 连接失败必须阻断对账'
)
assert.equal(operationCalled, false, '连接未全部成功时不得进入对账逻辑')
assert.equal(closeCount, 2, '任一连接失败时必须清理两端客户端')

assert.deepEqual(
  classifyTargetNewTables(['accounts'], ['accounts', 'new_current_table'], new Set(['new_current_table'])),
  {
    targetNewTables: ['new_current_table'],
    allowedNewTables: ['new_current_table'],
    violations: []
  },
  '当前 schema 新增业务表必须由显式白名单批准'
)
assert.deepEqual(
  classifyTargetNewTables(['accounts'], ['accounts', 'unexpected_table'], new Set(['missing_expected_table'])).violations.sort(),
  [
    'missing_expected_table: configured allowed new table does not exist',
    'unexpected_table: unapproved target business table'
  ],
  '未批准新增表和不存在的白名单项都必须阻断'
)

assert.equal(
  stableJson({ z: 1, a: Buffer.from('secret'), nested: { b: 2, a: 1 } }),
  '{"a":{"$buffer":"c2VjcmV0"},"nested":{"a":1,"b":2},"z":1}',
  '稳定序列化必须排序对象键并安全编码 Buffer'
)
assert.notEqual(stableJson(Number.NaN), stableJson(null), '特殊数值不得与 null 产生相同摘要')
assert.notEqual(stableJson(new Date('2026-07-28T00:00:00Z')), stableJson('2026-07-28T00:00:00.000Z'), 'Date 与普通文本必须保持类型边界')
assert.equal(rowDigest({ id: '1', value: 'same' }), rowDigest({ value: 'same', id: '1' }), '行哈希不得依赖对象键顺序')

const source = [
  { id: 'a', value: 'one' },
  { id: 'b', value: 'two' }
]
const preservedWithAddition = compareKeyedRows(source, [...source, { id: 'c', value: 'three' }], ['id'])
assert.deepEqual(
  {
    sourceRows: preservedWithAddition.sourceRows,
    targetRows: preservedWithAddition.targetRows,
    addedRows: preservedWithAddition.addedRows,
    missingRows: preservedWithAddition.missingRows,
    modifiedRows: preservedWithAddition.modifiedRows
  },
  { sourceRows: 2, targetRows: 3, addedRows: 1, missingRows: 0, modifiedRows: 0 },
  '目标新增行不得误报为源业务修改'
)

const changed = compareKeyedRows(source, [
  { id: 'a', value: 'changed' },
  { id: 'c', value: 'three' }
], ['id'])
assert.equal(changed.missingRows, 1, '缺失源主键必须阻断')
assert.equal(changed.modifiedRows, 1, '源主键内容变化必须阻断')
assert.equal(changed.addedRows, 1, '替换源行时仍必须识别新增目标主键')
assert.throws(
  () => compareKeyedRows([{ id: 'a' }], [{ id: 'a' }, { id: 'a' }], ['id']),
  /主键结果重复/,
  '目标主键重复不得被静默覆盖'
)

const unkeyed = compareKeyedRows(
  [{ value: 'same' }, { value: 'same' }],
  [{ value: 'same' }, { value: 'other' }],
  []
)
assert.equal(unkeyed.missingRows, 1, '无主键表必须按行哈希多重集识别缺失')
assert.equal(unkeyed.addedRows, 1, '无主键表必须按行哈希多重集识别新增')

assert.equal(normalizeKnownBusinessTypeEvolutionValue('integer_boolean', 0, 'source'), false)
assert.equal(normalizeKnownBusinessTypeEvolutionValue('integer_boolean', 1, 'source'), true)
assert.equal(normalizeKnownBusinessTypeEvolutionValue('integer_boolean', true, 'target'), true)
assert.throws(
  () => normalizeKnownBusinessTypeEvolutionValue('integer_boolean', 2, 'source'),
  /不是 0\/1/,
  '旧布尔整数存在非 0/1 值时必须阻断业务数据升级'
)
assert.equal(
  normalizeKnownBusinessTypeEvolutionValue('text_timestamptz', '2026-07-27T01:02:03+08:00', 'source'),
  '2026-07-26T17:02:03.000Z',
  'text -> timestamptz 必须按绝对时间规范化后对账'
)

console.log('业务数据保留对账回归通过')
