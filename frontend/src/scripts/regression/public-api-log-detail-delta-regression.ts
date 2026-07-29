import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import type { PublicApiLogDetailSupplement, PublicApiLogListItem } from '../../types/domain/public-api-logs.js'
import { mergePublicApiLogDetail } from '../../views/public-api-logs/publicApiLogDetail.js'

const row: PublicApiLogListItem = {
  id: 'publog-1',
  createdAt: '2026-07-29T00:00:00.000Z',
  sourceName: '来源系统',
  method: 'POST',
  path: '/__aipublic__/account/add',
  success: false,
  statusCode: 400,
  durationMs: 21,
  clientIp: '127.0.0.1',
  traceId: 'trace-1'
}
const supplement: PublicApiLogDetailSupplement = {
  tokenName: '管理 Token',
  tokenPrefix: 'tok_',
  isTestToken: false,
  userAgent: 'regression',
  errorCode: 'invalid_request',
  errorMessage: '请求无效',
  requestData: { request: true },
  responseData: { response: true }
}

const detail = mergePublicApiLogDetail(row, supplement)
assert.deepEqual(detail, { ...row, ...supplement }, '详情必须由已加载列表行与增量响应完整合并')
assert.equal(detail.id, row.id)
assert.equal(detail.requestData.request, true)
assert.equal(detail.responseData.response, true)
assert.deepEqual(row, {
  id: 'publog-1',
  createdAt: '2026-07-29T00:00:00.000Z',
  sourceName: '来源系统',
  method: 'POST',
  path: '/__aipublic__/account/add',
  success: false,
  statusCode: 400,
  durationMs: 21,
  clientIp: '127.0.0.1',
  traceId: 'trace-1'
}, '详情合并不得修改列表行')

const apiSource = readFileSync(new URL('../../api/domains/logs.ts', import.meta.url), 'utf8')
const viewSource = readFileSync(new URL('../../views/public-api-logs/PublicApiLogsView.vue', import.meta.url), 'utf8')
assert.match(apiSource, /unwrap<PublicApiLogDetailSupplement>/, '详情 API 必须声明增量 DTO')
assert.match(viewSource, /mergePublicApiLogDetail\(record, supplement\)/, '详情打开后必须显式合并列表行与增量')
assert.match(viewSource, /catch \(error\) \{\s*if \(requestId !== detailRequestId\) return/, '过期详情失败不得关闭或污染当前抽屉')

console.log('公开 API 日志详情增量前端回归通过')
