import { strict as assert } from 'node:assert'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { getRuntimeLogGrepDetail, grepRuntimeLogFiles } from '../../modules/runtime-logs/runtime-log-grep.service.js'

const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-ai-runtime-log-grep-'))
runtimeConfig.log.directory = tempRoot
runtimeConfig.log.fileEnabled = true

try {
  const now = new Date().toISOString()
  const logPath = join(tempRoot, 'juhe-ai.log')
  writeFileSync(logPath, [
    JSON.stringify({
      time: now,
      level: 30,
      event: 'http_request_completed',
      path: '/__aisys__/api/runtime-logs/grep',
      originalUrl: '/__aisys__/api/runtime-logs/grep?keywords=needle',
      msg: '日志搜索请求 needle'
    }),
    JSON.stringify({
      time: now,
      level: 30,
      event: 'gateway_test_event',
      path: '/v1/responses',
      originalUrl: '/v1/responses',
      msg: '业务日志 needle'
    }),
    JSON.stringify({
      time: now,
      level: 30,
      event: 'large_gateway_event',
      path: '/v1/responses',
      originalUrl: '/v1/responses',
      msg: '大日志 needle',
      body: 'x'.repeat(24_000)
    })
  ].join('\n') + '\n', 'utf8')

  const result = await grepRuntimeLogFiles({ keywords: ['needle'], limit: 10 })
  assert.equal(result.available, true, '测试环境应可用 rg 搜索')
  assert.equal(result.items.length, 1, '日志搜索应过滤自身请求日志，并跳过超过安全行长的命中')
  assert(result.items.some((item) => item.event === 'gateway_test_event'), '应保留非 runtime-logs 路径的业务日志')
  assert(!result.items.some((item) => item.event === 'large_gateway_event'), '超长命中日志不应由 rg 输出到 Node 侧解析')
  const grepItemKeys = Object.keys(result.items[0]!)
  assert(!grepItemKeys.includes('rawJson'), 'grep 响应不得重复返回 rawJson')
  assert(!grepItemKeys.includes('line'), 'grep 列表不得提前返回完整原始行')
  assert(!grepItemKeys.includes('file'), 'grep 列表不得提前返回完整服务器路径')
  assert(
    grepItemKeys.every((key) => ['errorMessage', 'event', 'fileName', 'id', 'level', 'lineNumber', 'message', 'time', 'traceId'].includes(key)),
    `grep 响应出现未定义字段：${grepItemKeys.join(', ')}`
  )
  const item = result.items[0]!
  const detail = await getRuntimeLogGrepDetail(item)
  assert.equal(detail.status, 'ok', '点击 grep 行后应可按文件名和行号读取原文')
  assert.match(detail.status === 'ok' ? detail.detail.line : '', /业务日志 needle/, 'grep 详情应返回完整匹配行')
  assert.equal(detail.status === 'ok' ? detail.detail.file : '', logPath, 'grep 详情才返回服务器完整路径')

  const traversal = await getRuntimeLogGrepDetail({ ...item, fileName: '../juhe-ai.log' })
  assert.equal(traversal.status, 'not_found', 'grep 详情不得接受目录穿越路径')
  writeFileSync(logPath, JSON.stringify({ time: now, level: 30, event: 'rotated_event', msg: '业务日志 needle' }) + '\n', 'utf8')
  const staleDetail = await getRuntimeLogGrepDetail(item)
  assert.equal(staleDetail.status, 'stale', '日志轮转后不得按旧定位返回其他原始行')

  runtimeConfig.log.directory = resolve(tempRoot, 'missing-log-dir')
  await assert.rejects(
    () => grepRuntimeLogFiles({ keywords: ['needle'], limit: 10 }),
    /ENOENT|no such file|找不到/i,
    '日志目录读取失败不能伪装成空搜索结果'
  )

  console.log('运行日志 grep 路径过滤回归通过：新系统 API 前缀下不会把日志搜索请求本身混入结果')
} finally {
  rmSync(tempRoot, { recursive: true, force: true })
}
