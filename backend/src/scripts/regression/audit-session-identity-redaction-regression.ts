import { strict as assert } from 'node:assert'

import {
  redactAuditSessionIdentityJson,
  redactAuditSessionIdentityRequestBody
} from '../../modules/gateway/audit/session-identity-redaction.js'
import { sanitizeHeaderRecord } from '../../modules/gateway/upstream/headers.js'

function main(): void {
  testIdentityHeadersAreRedacted()
  testJsonIdentityFieldsAreRedactedWithoutMutatingRequest()
  testSerializedUpstreamJsonIsRedacted()
  testLargeUnparsedJsonIsOmitted()
  console.log('audit session identity redaction regression: passed')
}

function testSerializedUpstreamJsonIsRedacted(): void {
  const rawBody = Buffer.from(JSON.stringify({
    model: 'claude-sonnet-4',
    request: { session_id: 'raw-upstream-session' },
    input: 'keep-upstream-input'
  }))
  const sanitized = redactAuditSessionIdentityRequestBody({
    rawBody,
    contentType: 'application/json'
  })
  assert(Buffer.isBuffer(sanitized))
  assert(!sanitized.toString('utf8').includes('raw-upstream-session'))
  assert(sanitized.toString('utf8').includes('keep-upstream-input'))
}

function testIdentityHeadersAreRedacted(): void {
  const headers = sanitizeHeaderRecord({
    'session-id': 'raw-session',
    'thread-id': 'raw-thread',
    'x-claude-code-session-id': 'raw-claude-session',
    'x-codex-turn-metadata': '{"session_id":"raw-codex"}',
    'x-client-request-id': 'raw-client-request',
    'content-type': 'application/json'
  })
  assert.equal(headers['session-id'], '[redacted]')
  assert.equal(headers['thread-id'], '[redacted]')
  assert.equal(headers['x-claude-code-session-id'], '[redacted]')
  assert.equal(headers['x-codex-turn-metadata'], '[redacted]')
  assert.equal(headers['x-client-request-id'], '[redacted]')
  assert.equal(headers['content-type'], 'application/json')
}

function testJsonIdentityFieldsAreRedactedWithoutMutatingRequest(): void {
  const body = {
    model: 'gpt-5.3-codex',
    input: 'keep-me',
    conversation: { id: 'conv_raw' },
    previous_response_id: 'resp_raw',
    prompt_cache_key: 'cache_raw',
    request: { session_id: 'gemini_raw', keep: true },
    client_metadata: {
      session_id: 'codex_flat_raw',
      'x-codex-turn-metadata': JSON.stringify({
        session_id: 'codex_blob_raw',
        thread_id: 'thread_raw',
        turn_id: 'turn_raw'
      })
    },
    metadata: {
      user_id: JSON.stringify({
        device_id: 'device-keep',
        session_id: 'claude_raw'
      })
    }
  }
  const sanitized = redactAuditSessionIdentityJson(body) as Record<string, unknown>
  const serialized = JSON.stringify(sanitized)
  assert.equal(sanitized.model, body.model)
  assert.equal(sanitized.input, body.input)
  assert(!serialized.includes('conv_raw'))
  assert(!serialized.includes('resp_raw'))
  assert(!serialized.includes('cache_raw'))
  assert(!serialized.includes('gemini_raw'))
  assert(!serialized.includes('codex_flat_raw'))
  assert(!serialized.includes('codex_blob_raw'))
  assert(!serialized.includes('thread_raw'))
  assert(!serialized.includes('turn_raw'))
  assert(!serialized.includes('claude_raw'))
  assert(serialized.includes('device-keep'))
  assert.equal(body.previous_response_id, 'resp_raw', '审计脱敏不得修改转发请求对象')
  assert.equal(body.request.session_id, 'gemini_raw', '嵌套请求对象必须保持原值')

  const encoded = redactAuditSessionIdentityRequestBody({
    body,
    rawBody: Buffer.from(JSON.stringify(body)),
    contentType: 'application/json'
  })
  assert(Buffer.isBuffer(encoded))
  assert(!encoded.toString('utf8').includes('claude_raw'))
  assert(encoded.toString('utf8').includes('keep-me'))
}

function testLargeUnparsedJsonIsOmitted(): void {
  const rawBody = Buffer.from(JSON.stringify({
    session_id: 'large-raw-session',
    padding: 'x'.repeat(300 * 1024)
  }))
  const sanitized = redactAuditSessionIdentityRequestBody({
    body: {},
    rawBody,
    contentType: 'application/json'
  })
  assert(Buffer.isBuffer(sanitized))
  assert(!sanitized.toString('utf8').includes('large-raw-session'))
  assert(sanitized.toString('utf8').includes('_audit_body_omitted'))
}

main()
