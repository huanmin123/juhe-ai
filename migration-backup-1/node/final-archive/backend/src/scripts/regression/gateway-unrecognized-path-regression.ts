import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import type { Request } from 'express'

import { isGatewayProtocolRequest } from '../../modules/gateway/protocols/registry.js'

function request(method: string, originalUrl: string): Request {
  return {
    method,
    originalUrl,
    path: originalUrl.split('?', 1)[0] ?? originalUrl
  } as Request
}

assert.equal(isGatewayProtocolRequest(request('GET', '/')), false)
assert.equal(isGatewayProtocolRequest(request('GET', '/.env')), false)
assert.equal(isGatewayProtocolRequest(request('GET', '/static/js/main.js')), false)

assert.equal(isGatewayProtocolRequest(request('POST', '/v1/chat/completions')), true)
assert.equal(isGatewayProtocolRequest(request('POST', '/v1/messages')), true)
assert.equal(isGatewayProtocolRequest(request('POST', '/v1beta/models/gemini-2.5-flash:generateContent')), true)

const serverPath = fileURLToPath(new URL('../../server.ts', import.meta.url))
const serverSource = await readFile(serverPath, 'utf8')
const gatewayChain = serverSource.slice(
  serverSource.indexOf('app.use(\n  rejectGatewayTrafficOnControlNode,'),
  serverSource.indexOf('function rejectGatewayTrafficOnControlNode')
)
assert(gatewayChain.includes('rejectUnrecognizedGatewayProtocolRequest,'), '未识别路径必须在运行时鉴权之前被拒绝')
assert(
  gatewayChain.indexOf('rejectUnrecognizedGatewayProtocolRequest,') < gatewayChain.indexOf('preResolveGatewayRuntime,'),
  '未识别路径不得进入运行时解析'
)
