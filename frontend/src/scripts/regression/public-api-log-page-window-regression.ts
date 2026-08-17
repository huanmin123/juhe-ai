import assert from 'node:assert/strict'

import { mergePublicApiLogListItems } from '../../views/public-api-logs/publicApiLogPageWindow.js'
import { parseStoredPublicApiLogTimeRange } from '../../views/public-api-logs/publicApiLogFormatters'

const first = [
  { id: 'log-3', createdAt: '2026-07-22T00:00:03.000Z', method: 'GET', path: '/3', success: true },
  { id: 'log-2', createdAt: '2026-07-22T00:00:02.000Z', method: 'GET', path: '/2', success: true }
]
const second = [
  { id: 'log-2', createdAt: '2026-07-22T00:00:02.000Z', method: 'GET', path: '/2', success: true },
  { id: 'log-1', createdAt: '2026-07-22T00:00:01.000Z', method: 'GET', path: '/1', success: true }
]

assert.deepEqual(
  mergePublicApiLogListItems(first, second).map((item) => item.id),
  ['log-3', 'log-2', 'log-1'],
  '移动分页追加必须按 id 去重并保持首次出现顺序'
)

assert.deepEqual(
  parseStoredPublicApiLogTimeRange(['2026-07-22T08:00:00+08:00', '2026-07-22T00:30:00Z'])?.map((item) => item.toISOString()),
  ['2026-07-22T00:00:00.000Z', '2026-07-22T00:30:00.000Z'],
  '持久化公开日志范围必须按 epoch 排序'
)
assert.equal(parseStoredPublicApiLogTimeRange(['2026-07-22 08:00:00', '2026-07-22T00:30:00Z']), undefined, '持久化公开日志范围不得接受无时区时间')

console.log('公开 API 日志分页窗口回归通过')
