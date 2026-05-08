import { strict as assert } from 'node:assert'
import type { Request } from 'express'

import { buildUpstreamHeaders, buildUpstreamRequestBody } from '../modules/gateway/openai-gateway-upstream.js'

type TestRequest = Request & { rawBody?: Buffer }

const apiKeyAccount = {
  apiKey: 'sk-upstream',
  passthroughEnabled: true,
  type: 'api_key',
  credentials: {}
}

function main(): void {
  testRawBodyPassthrough()
  testApiKeyHeaderFiltering()
  testOpenAIAccountHeadersAreNotClientOrCredentialControlled()
  testOpenAIBetaPreservedFromClient()
  testJsonBodyFallbackWhenPassthroughDisabled()
  console.log('OpenAI API Key passthrough regression passed')
}

function testRawBodyPassthrough(): void {
  const rawBody = Buffer.from('{"model":"gpt-5.4","input":"hello","metadata":{"keep":true}}')
  const req = createRequest({ model: 'gpt-5.4', input: 'hello' }, { 'content-type': 'application/json' }, rawBody)
  const body = buildUpstreamRequestBody(req, true)

  assert.ok(Buffer.isBuffer(body))
  assert.equal(Buffer.compare(body, rawBody), 0)
}

function testApiKeyHeaderFiltering(): void {
  const headers = buildUpstreamHeaders({
    authorization: 'Bearer local-key',
    'content-type': 'application/json; charset=utf-8',
    accept: 'text/event-stream',
    cookie: 'secret=value',
    'x-api-key': 'local-x-api-key',
    'x-goog-api-key': 'local-google-key',
    'api-key': 'local-api-key',
    'chatgpt-account-id': 'client-chatgpt-account',
    'accept-encoding': 'gzip, br',
    'content-encoding': 'gzip',
    'content-length': '123',
    host: '127.0.0.1:3000',
    connection: 'keep-alive',
    'x-forwarded-for': '127.0.0.1',
    'x-forwarded-proto': 'https',
    'x-real-ip': '127.0.0.1',
    forwarded: 'for=127.0.0.1',
    via: 'proxy',
    'cf-connecting-ip': '127.0.0.1',
    'x-request-id': 'client-request',
    traceparent: '00-abc-abc-01',
    tracestate: 'state',
    baggage: 'local=value',
    'x-amzn-trace-id': 'Root=1',
    'x-cloud-trace-context': 'trace/span',
    'x-stainless-lang': 'js',
    'x-stainless-package-version': '4.0.0',
    'x-openai-client-user-agent': 'sdk-noise',
    'x-vercel-id': 'iad1::abc',
    'idempotency-key': 'idem-123',
    'x-custom-header': 'kept'
  }, apiKeyAccount)

  assert.equal(headers.get('authorization'), 'Bearer sk-upstream')
  assert.equal(headers.get('content-type'), 'application/json; charset=utf-8')
  assert.equal(headers.get('accept'), 'text/event-stream')
  assert.equal(headers.get('idempotency-key'), 'idem-123')
  assert.equal(headers.get('x-custom-header'), 'kept')

  for (const name of [
    'cookie',
    'x-api-key',
    'x-goog-api-key',
    'api-key',
    'chatgpt-account-id',
    'accept-encoding',
    'content-encoding',
    'content-length',
    'host',
    'connection',
    'x-forwarded-for',
    'x-forwarded-proto',
    'x-real-ip',
    'forwarded',
    'via',
    'cf-connecting-ip',
    'x-request-id',
    'traceparent',
    'tracestate',
    'baggage',
    'x-amzn-trace-id',
    'x-cloud-trace-context',
    'x-stainless-lang',
    'x-stainless-package-version',
    'x-openai-client-user-agent',
    'x-vercel-id'
  ]) {
    assert.equal(headers.get(name), null, `${name} should be stripped`)
  }
}

function testOpenAIAccountHeadersAreNotClientOrCredentialControlled(): void {
  const headers = buildUpstreamHeaders({
    'openai-organization': 'client-org',
    'openai-project': 'client-project',
    'openai-beta': 'responses=experimental'
  }, {
    ...apiKeyAccount,
    credentials: {
      openai_organization: 'acct-org',
      openai_project: 'acct-project',
      openai_beta: 'acct-beta'
    }
  })

  assert.equal(headers.get('openai-organization'), null)
  assert.equal(headers.get('openai-project'), null)
  assert.equal(headers.get('openai-beta'), 'responses=experimental')
}

function testOpenAIBetaPreservedFromClient(): void {
  const clientHeaders = buildUpstreamHeaders({
    'openai-beta': 'assistants=v2'
  }, apiKeyAccount)
  assert.equal(clientHeaders.get('openai-beta'), 'assistants=v2')
}

function testJsonBodyFallbackWhenPassthroughDisabled(): void {
  const req = createRequest({ model: 'gpt-5.4', input: 'hello' }, { accept: 'application/json' })
  const body = buildUpstreamRequestBody(req, false)
  const headers = buildUpstreamHeaders(req.headers, {
    ...apiKeyAccount,
    passthroughEnabled: false
  })

  assert.equal(body, JSON.stringify({ model: 'gpt-5.4', input: 'hello' }))
  assert.equal(headers.get('content-type'), 'application/json')
  assert.equal(headers.get('accept'), 'application/json')
}

function createRequest(
  body: unknown,
  headers: Record<string, string | string[] | undefined> = {},
  rawBody = Buffer.from(JSON.stringify(body ?? {}))
): TestRequest {
  return {
    method: 'POST',
    originalUrl: '/v1/responses',
    path: '/v1/responses',
    headers,
    body,
    rawBody
  } as TestRequest
}

main()
