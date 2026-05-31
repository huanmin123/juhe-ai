import { strict as assert } from 'node:assert'
import { buildUpstreamRequestBody } from '../../modules/gateway/openai-gateway-upstream.js'
import type { GatewayRawBodyRequest } from '../../modules/gateway/openai-gateway-request-body.js'

const passthroughReq = {
  method: 'POST',
  body: { ignored: true },
  rawBody: Buffer.from('{"raw":true}')
} as unknown as GatewayRawBodyRequest
const passthroughFirst = buildUpstreamRequestBody(passthroughReq)
const passthroughSecond = buildUpstreamRequestBody(passthroughReq)
assert.equal(passthroughFirst, passthroughReq.rawBody, '透传 upstream body 应使用 raw body')
assert.equal(passthroughSecond, passthroughReq.rawBody, '透传 upstream body 应复用 raw body 缓存')

console.log('网关 upstream body 缓存回归通过：切号复用同一 raw body')
