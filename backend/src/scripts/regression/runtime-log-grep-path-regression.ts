import { strict as assert } from 'node:assert'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { grepRuntimeLogFiles } from '../../modules/runtime-logs/runtime-log-grep.service.js'

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
      originalUrl: '/__aisys__/api/runtime-logs/grep?keyword=needle',
      msg: '日志搜索请求 needle'
    }),
    JSON.stringify({
      time: now,
      level: 30,
      event: 'gateway_test_event',
      path: '/v1/responses',
      originalUrl: '/v1/responses',
      msg: '业务日志 needle'
    })
  ].join('\n') + '\n', 'utf8')

  const result = await grepRuntimeLogFiles({ keywords: ['needle'], limit: 10 })
  assert.equal(result.available, true, '测试环境应可用 rg 搜索')
  assert.equal(result.items.length, 1, '日志搜索应过滤自身请求日志，只返回业务日志')
  assert.equal(result.items[0]?.event, 'gateway_test_event', '应保留非 runtime-logs 路径的业务日志')

  console.log('运行日志 grep 路径过滤回归通过：新系统 API 前缀下不会把日志搜索请求本身混入结果')
} finally {
  rmSync(tempRoot, { recursive: true, force: true })
}
