import { strict as assert } from 'node:assert'

import {
  dispatchOperationLogToGo,
  getOperationLogDetailFromGo,
  listOperationLogsFromGo
} from '../../modules/operation-logs/operation-log-go-input.service.js'

const createdAt = '2026-08-13T00:00:00.000Z'
const id = 'oplog-node-go-node-smoke'

await dispatchOperationLogToGo({
  id,
  createdAt,
  actorSystemAccountId: 'actor',
  actorRole: 'user',
  module: 'accounts',
  action: 'update',
  operationKey: 'accounts.update',
  resourceType: 'account',
  resourceId: 'account-1',
  summary: 'Node Go Node smoke Alpha account',
  clientIp: '203.0.113.9',
  viewers: [{ systemAccountId: 'viewer', visibilityReason: 'actor_self', detailLevel: 'full' }]
})

const list = await listOperationLogsFromGo({ page: 1, pageSize: 20, summaryKeyword: 'pha acc' }, 'viewer')
assert.equal(list.items.length, 1)
assert.equal(list.items[0]?.id, id)

const detail = await getOperationLogDetailFromGo(id, 'viewer')
assert.ok(detail)
assert.equal(detail?.clientIp, undefined, '个人详情不应返回 clientIp')
assert.equal(detail?.resourceId, 'account-1')

console.log('F4 real Node-Go-Node smoke passed.')
