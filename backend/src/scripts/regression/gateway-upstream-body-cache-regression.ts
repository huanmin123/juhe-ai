import { strict as assert } from 'node:assert'
import type { Request } from 'express'

import { buildUpstreamRequestBody } from '../../modules/gateway/openai-gateway-upstream.js'
import type { GatewayRawBodyRequest } from '../../modules/gateway/openai-gateway-request-body.js'

let stringifyCount = 0
const req = {
  method: 'POST',
  body: {
    model: 'gpt-5.4-mini',
    input: 'hello',
    toJSON() {
      stringifyCount += 1
      return {
        model: 'gpt-5.4-mini',
        input: 'hello'
      }
    }
  }
} as unknown as Request

const first = buildUpstreamRequestBody(req, false)
const second = buildUpstreamRequestBody(req, false)
assert.equal(first, second, '同一请求的非透传 upstream body 应复用缓存')
assert.equal(stringifyCount, 1, '同一请求切换候选账号时不应重复 JSON.stringify')

const passthroughReq = {
  method: 'POST',
  body: { ignored: true },
  rawBody: Buffer.from('{"raw":true}')
} as unknown as GatewayRawBodyRequest
const passthroughFirst = buildUpstreamRequestBody(passthroughReq, true)
const passthroughSecond = buildUpstreamRequestBody(passthroughReq, true)
assert.equal(passthroughFirst, passthroughReq.rawBody, '透传 upstream body 应使用 raw body')
assert.equal(passthroughSecond, passthroughReq.rawBody, '透传 upstream body 应复用 raw body 缓存')

console.log('网关 upstream body 缓存回归通过：切号不重复序列化同一请求体')
