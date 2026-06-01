import assert from 'node:assert/strict'

import { buildDiagnosticUpstreamError } from '../../modules/gateway/openai-gateway-error-helpers.js'
import { sanitizeAuditPayloadBody, sanitizeDiagnosticPayload } from '../../modules/gateway/payload-sanitizer.js'

const diagnostic = buildDiagnosticUpstreamError({
  accountId: 'acct_diagnostic',
  accountName: '诊断脱敏账号',
  upstreamUrl: 'https://url-user:url-password@example.com/v1/chat/completions?client_secret=url-secret&safe=ok',
  status: 401,
  message: 'fallback id_token=fallback-id-token client_secret=fallback-client-secret',
  responseHeaders: { 'content-type': 'application/json; charset=utf-8' },
  responseBodyText: JSON.stringify({
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
}, 'fallback client_secret=fallback-client-secret')

assert(diagnostic, '诊断错误应生成响应')
assertNoLeak(JSON.stringify(diagnostic), [
  'diagnostic-id-token',
  'diagnostic-client-secret',
  'diagnostic-api-key',
  'sk-diagnostic-secret-token',
  'url-user',
  'url-password',
  'url-secret',
  'fallback-client-secret',
  'fallback-id-token'
], '上游诊断错误响应不应泄露敏感字段、token 或 URL 用户信息')

const jsonBody = JSON.stringify({
  model: 'gpt-4o-mini',
  client_secret: 'audit-json-client-secret',
  id_token: 'audit-json-id-token',
  nested: {
    apiKey: 'audit-json-api-key',
    message: 'Authorization: Bearer audit-json-bearer-token and sk-audit-json-secret-token'
  },
  safe: 'ok'
})
const sanitizedJson = sanitizeAuditPayloadBody({
  body: jsonBody,
  contentType: 'application/json; charset=utf-8'
})
assert.equal(sanitizedJson.redacted, true, 'JSON 审计 payload 应发生脱敏')
assert.equal(sanitizedJson.originalSizeBytes, Buffer.byteLength(jsonBody), 'JSON 审计 payload 应保留原始体积')
const sanitizedJsonText = bodyText(sanitizedJson.body)
assertNoLeak(sanitizedJsonText, [
  'audit-json-client-secret',
  'audit-json-id-token',
  'audit-json-api-key',
  'audit-json-bearer-token',
  'sk-audit-json-secret-token'
], 'JSON 审计 payload 不应保留敏感原文')
assert.equal(JSON.parse(sanitizedJsonText).safe, 'ok', 'JSON 审计 payload 应保留安全字段')

const sanitizedText = sanitizeDiagnosticPayload('proxy failed at https://diagnostic-user:diagnostic-password@example.com/v1?safe=ok')
assertNoLeak(sanitizedText, [
  'diagnostic-user',
  'diagnostic-password'
], '诊断普通字符串中的 URL 用户信息不应保留敏感原文')
assert(sanitizedText.includes('example.com/v1?safe=ok'), '诊断 URL 脱敏后应保留主机、路径和安全查询参数')

const formBody = 'safe=ok&client_secret=audit-form-client-secret&id_token=audit-form-id-token&message=Bearer audit-form-bearer-token'
const sanitizedForm = sanitizeAuditPayloadBody({
  body: formBody,
  contentType: 'application/x-www-form-urlencoded'
})
assert.equal(sanitizedForm.redacted, true, '表单审计 payload 应发生脱敏')
assert.equal(sanitizedForm.originalSizeBytes, Buffer.byteLength(formBody), '表单审计 payload 应保留原始体积')
const sanitizedFormParams = new URLSearchParams(bodyText(sanitizedForm.body))
assert.equal(sanitizedFormParams.get('safe'), 'ok', '表单审计 payload 应保留安全参数')
assert.equal(sanitizedFormParams.get('client_secret'), '[redacted]', '表单 client_secret 应脱敏')
assert.equal(sanitizedFormParams.get('id_token'), '[redacted]', '表单 id_token 应脱敏')
assert.equal(sanitizedFormParams.get('message'), 'Bearer [redacted]', '表单普通字段内的 Bearer token 应脱敏')
assertNoLeak(bodyText(sanitizedForm.body), [
  'audit-form-client-secret',
  'audit-form-id-token',
  'audit-form-bearer-token'
], '表单审计 payload 不应保留敏感原文')

const largeJsonText = `{"padding":"${'x'.repeat(4 * 1024 * 1024 + 1)}","client_secret":"large-audit-client-secret","id_token":"large-audit-id-token","apiKey":"large-audit-api-key"}`
const sanitizedLarge = sanitizeAuditPayloadBody({
  body: largeJsonText,
  contentType: 'application/json'
})
assert.equal(sanitizedLarge.redacted, true, '超过内联 JSON 解析阈值的大文本也应通过字符串规则脱敏')
assert.equal(sanitizedLarge.originalSizeBytes, Buffer.byteLength(largeJsonText), '大文本审计 payload 应保留原始体积')
assertNoLeak(bodyText(sanitizedLarge.body), [
  'large-audit-client-secret',
  'large-audit-id-token',
  'large-audit-api-key'
], '大文本审计 payload 不应因跳过 JSON 解析而泄露敏感赋值片段')

console.log('网关 payload 脱敏回归通过：诊断错误和审计 payload 均会清理敏感字段、token 与 URL 凭据')

function bodyText(body: Buffer | string | undefined): string {
  return Buffer.isBuffer(body) ? body.toString('utf8') : body ?? ''
}

function assertNoLeak(text: string, markers: string[], message: string): void {
  for (const marker of markers) {
    assert(!text.includes(marker), `${message}：${marker}`)
  }
}
