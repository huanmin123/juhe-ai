import assert from 'node:assert/strict'

import { sanitizeDiagnosticPayload } from '../../modules/gateway/diagnostics/diagnostic-sanitizer.js'

const diagnostic = sanitizeDiagnosticPayload({
  upstreamUrl: 'https://url-user:url-password@example.com/v1/chat/completions?client_secret=url-secret&safe=ok',
  message: 'fallback id_token=fallback-id-token client_secret=fallback-client-secret',
  error: {
    message: '上游失败 id_token=diagnostic-id-token client_secret=diagnostic-client-secret Authorization: Bearer sk-diagnostic-secret-token',
    type: 'invalid_request_error',
    details: {
      id_token: 'diagnostic-id-token',
      client_secret: 'diagnostic-client-secret',
      nested: {
        apiKey: 'diagnostic-api-key'
      }
    }
  }
})

assertNoLeak(JSON.stringify(diagnostic), [
  'diagnostic-id-token',
  'diagnostic-client-secret',
  'diagnostic-api-key',
  'sk-diagnostic-secret-token',
  'fallback-client-secret',
  'fallback-id-token',
  'url-user',
  'url-password',
  'url-secret'
], '上游诊断错误响应正文不应泄露敏感字段或 token')
assert(JSON.stringify(diagnostic).includes('example.com/v1/chat/completions?client_secret=[redacted]&safe=ok'), '诊断 URL 应保留主机、路径和安全查询参数')

const sanitizedText = sanitizeDiagnosticPayload('proxy failed at https://diagnostic-user:diagnostic-password@example.com/v1?safe=ok')
assertNoLeak(sanitizedText, [
  'diagnostic-user',
  'diagnostic-password'
], '诊断普通字符串中的 URL 用户信息不应保留敏感原文')
assert(sanitizedText.includes('example.com/v1?safe=ok'), '诊断 URL 脱敏后应保留主机、路径和安全查询参数')

console.log('网关诊断 payload 回归通过：诊断正文按展示策略清理')

function assertNoLeak(text: string, markers: string[], message: string): void {
  for (const marker of markers) {
    assert(!text.includes(marker), `${message}：${marker}`)
  }
}
