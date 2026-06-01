import { strict as assert } from 'node:assert'
import type { Request } from 'express'

import { extractClientIp as extractRequestContextClientIp } from '../../shared/request-context.js'
import { extractClientIp as extractGatewayClientIp } from '../../modules/gateway/openai-gateway-usage.js'

const spoofedForwardedForRequest = {
  header: (name: string) => name.toLowerCase() === 'x-forwarded-for' ? '203.0.113.250' : undefined,
  ip: '198.51.100.10',
  socket: { remoteAddress: '198.51.100.11' }
} as unknown as Request

assert.equal(
  extractRequestContextClientIp(spoofedForwardedForRequest),
  '198.51.100.10',
  '请求上下文 clientIp 应使用 Express req.ip，不能被直连客户端伪造 X-Forwarded-For 覆盖'
)
assert.equal(
  extractGatewayClientIp(spoofedForwardedForRequest),
  '198.51.100.10',
  '网关 clientIp 应使用 Express req.ip，不能被直连客户端伪造 X-Forwarded-For 覆盖'
)

const socketFallbackRequest = {
  header: () => undefined,
  ip: '',
  socket: { remoteAddress: '::ffff:198.51.100.12' }
} as unknown as Request

assert.equal(
  extractRequestContextClientIp(socketFallbackRequest),
  '198.51.100.12',
  '缺少 req.ip 时请求上下文 clientIp 应回退并规范化 socket remoteAddress'
)
assert.equal(
  extractGatewayClientIp(socketFallbackRequest),
  '198.51.100.12',
  '缺少 req.ip 时网关 clientIp 应回退并规范化 socket remoteAddress'
)

console.log('请求上下文 client IP 回归通过：clientIp 由 Express trust proxy 后的 req.ip 决定，不直接信任转发头')
