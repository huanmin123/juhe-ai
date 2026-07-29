import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import type { OperationLogDetailSupplement, OperationLogListItem } from '../../types/domain/operation-logs'
import { mergeOperationLogDetail } from '../../views/operation-logs/operationLogDetail'

const row: OperationLogListItem = {
  id: 'operation-1',
  traceId: 'trace-1',
  actorSystemAccountId: 'sys-admin',
  actorDisplayName: '管理员',
  actorSystemAccountName: '管理员账户',
  operationScopeSystemAccountId: 'sys-owner',
  operationScopeSystemAccountName: '资源用户',
  module: 'accounts',
  action: 'update',
  summary: '更新账户',
  createdAt: '2026-07-29T00:00:00.000Z'
}

const supplement: OperationLogDetailSupplement = {
  operationKey: 'accounts.update',
  resourceType: 'account',
  resourceId: 'account-1',
  resourceName: '账户 1',
  visibilityScope: 'targeted',
  changes: [{ field: 'name', label: '名称', before: 'A', after: 'B' }],
  method: 'PATCH',
  path: '/accounts/account-1',
  clientIp: '127.0.0.1',
  targets: [{ id: 'target-1', targetType: 'account', targetId: 'account-1', relation: 'affected' }],
  viewers: []
}

const detail = mergeOperationLogDetail(row, supplement)
assert.equal(detail.id, row.id)
assert.equal(detail.summary, row.summary)
assert.equal(detail.actorDisplayName, row.actorDisplayName)
assert.equal(detail.operationKey, supplement.operationKey)
assert.equal(detail.resourceId, supplement.resourceId)
assert.equal(detail.resourceName, supplement.resourceName)
assert.equal(detail.targets[0]?.targetId, 'account-1')
assert.deepEqual(detail.changes, supplement.changes)

const viewSource = readFileSync(new URL('../../views/operation-logs/OperationLogsView.vue', import.meta.url), 'utf8')
assert.match(viewSource, /mergeOperationLogDetail\(record, supplement\)/, '详情抽屉必须合并当前列表行与详情增量')
assert.match(viewSource, /detail\.value = undefined[\s\S]{0,500}catch \(error\) \{\s*if \(requestId !== detailRequestId\) return/, '连续打开详情时必须清空旧值并忽略过期失败')
assert.match(viewSource, /@close="closeTransientDetails"/, '关闭详情抽屉必须使在途请求失效')
const apiSource = readFileSync(new URL('../../api/domains/logs.ts', import.meta.url), 'utf8')
assert.equal((apiSource.match(/unwrap<OperationLogDetailSupplement>/g) ?? []).length, 2, '管理端和用户端详情 API 都必须声明增量 DTO')

console.log('操作日志前端详情增量回归通过')
