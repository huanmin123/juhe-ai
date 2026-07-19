import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import type { Server } from 'node:http'
import { resolve } from 'node:path'

import express from 'express'

import {
  managementSecurityHeaders,
  managementSecurityHeadersMiddleware,
  setManagementSecurityHeaders
} from '../../shared/http-security.js'

const expectedCsp = "default-src 'self'; base-uri 'self'; object-src 'none'; frame-ancestors 'none'; form-action 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: https:; font-src 'self' data: https:; connect-src 'self' https: wss:; worker-src 'self' blob:; media-src 'self' data: blob: https:; manifest-src 'self'"

const serverSource = readFileSync(resolve('src/server.ts'), 'utf8')
assert.match(serverSource, /app\.disable\(['"]x-powered-by['"]\)/, '正式服务器必须关闭 Express 技术栈响应标识')
assert.match(serverSource, /app\.use\(systemPrefix,\s*managementSecurityHeadersMiddleware\)/, '正式服务器必须只在 systemPrefix 挂载管理面安全头')

const headers = managementSecurityHeaders()
assert.equal(headers['content-security-policy'], expectedCsp, '管理面 CSP 必须与审核后的兼容策略一致')
assert(!headers['content-security-policy'].includes('unsafe-eval'), '管理面 CSP 不能允许字符串执行')
assert(!headers['content-security-policy'].includes('script-src *'), '管理面 CSP 不能允许通配脚本源')
assert.equal(headers['x-frame-options'], 'DENY', '管理面必须禁止被 iframe 嵌入')
assert.equal(headers['x-content-type-options'], 'nosniff', '管理面必须禁止 MIME 嗅探')
assert.equal(headers['referrer-policy'], 'strict-origin-when-cross-origin', '管理面跨站请求不能泄露完整路径')

const writtenHeaders = new Map<string, string>()
setManagementSecurityHeaders({
  setHeader(name, value) {
    writtenHeaders.set(name.toLowerCase(), String(value))
  }
})
assert.equal(writtenHeaders.get('content-security-policy'), expectedCsp, '安全头写入函数必须写入完整 CSP')
assert.equal(writtenHeaders.get('x-frame-options'), 'DENY', '安全头写入函数必须写入防嵌套头')

const app = express()
app.disable('x-powered-by')
app.use('/__aisys__', managementSecurityHeadersMiddleware)
app.get('/__aisys__/login', (_req, res) => res.type('html').send('<!doctype html><title>login</title>'))
app.get('/v1/models', (_req, res) => res.json({ object: 'list', data: [] }))

async function main(): Promise<void> {
  let server: Server | undefined
  try {
    server = app.listen(0, '127.0.0.1')
    await listen(server)
    const baseUrl = `http://127.0.0.1:${serverPort(server)}`

    const managementResponse = await fetch(`${baseUrl}/__aisys__/login`)
    assert.equal(managementResponse.status, 200, '管理面测试路由必须正常返回')
    assert.equal(managementResponse.headers.get('content-security-policy'), expectedCsp, '管理面路由必须返回 CSP')
    assert.equal(managementResponse.headers.get('x-frame-options'), 'DENY', '管理面路由必须返回防嵌套头')
    assert.equal(managementResponse.headers.get('x-content-type-options'), 'nosniff', '管理面路由必须返回 MIME 保护头')
    assert.equal(managementResponse.headers.get('referrer-policy'), 'strict-origin-when-cross-origin', '管理面路由必须返回 Referrer-Policy')
    assert.equal(managementResponse.headers.get('x-powered-by'), null, '响应不能暴露 Express 标识')

    const gatewayResponse = await fetch(`${baseUrl}/v1/models`)
    assert.equal(gatewayResponse.status, 200, '网关测试路由必须正常返回')
    assert.equal(gatewayResponse.headers.get('content-security-policy'), null, '/v1 不能套用管理面 CSP')
    assert.equal(gatewayResponse.headers.get('x-powered-by'), null, '网关响应也不能暴露 Express 标识')

    console.log('管理面 HTTP 安全头回归通过：CSP 仅作用于 /__aisys__，/v1 行为保持隔离')
  } finally {
    await closeServer(server)
  }
}

function listen(server: Server): Promise<void> {
  if (server.listening) return Promise.resolve()
  return new Promise((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

function serverPort(server: Server): number {
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('服务地址不可用')
  return address.port
}

async function closeServer(server: Server | undefined): Promise<void> {
  if (!server?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.close((error) => error ? rejectPromise(error) : resolvePromise())
  })
}

main().catch((error) => {
  console.error('\n管理面 HTTP 安全头回归失败')
  console.error(error instanceof Error ? error.message : error)
  process.exitCode = 1
})
