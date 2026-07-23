import assert from 'node:assert/strict'

import { mergePublicApiLogListItems } from '../../views/public-api-logs/publicApiLogPageWindow.js'

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

console.log('公开 API 日志分页窗口回归通过')
