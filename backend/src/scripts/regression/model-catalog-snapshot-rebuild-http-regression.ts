import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { createHmac } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import http from 'node:http'
import { gzipSync } from 'node:zlib'

import express, { type NextFunction, type Request, type Response } from 'express'

import {
  createModelCatalogSnapshotRebuildRouter,
  createModelCatalogSnapshotRebuildSignature,
  modelCatalogSnapshotRebuildInternalPrefix,
  modelCatalogSnapshotRebuildSignatureDomain
} from '../../modules/internal-api/model-catalog-snapshot-rebuild.routes.js'

const secret = 'model-catalog-snapshot-rebuild-http-secret'
const rebuildCalls: Array<{ scope: 'all' | 'personal'; systemAccountId?: string }> = []
const handledErrors: unknown[] = []
let deferNextAll = false
let resolveDeferredAll: (() => void) | undefined
const app = express()

app.use(modelCatalogSnapshotRebuildInternalPrefix, createModelCatalogSnapshotRebuildRouter({
  secret,
  rebuildAll: async () => {
    rebuildCalls.push({ scope: 'all' })
    if (deferNextAll) {
      deferNextAll = false
      await new Promise<void>((resolve) => {
        resolveDeferredAll = resolve
      })
    }
  },
  rebuildPersonal: async (systemAccountId) => {
    rebuildCalls.push({ scope: 'personal', systemAccountId })
    if (systemAccountId === 'throws') throw new Error('snapshot rebuild failed')
  }
}))
app.use((_req, res) => {
  res.status(404).json({ message: '资源不存在' })
})
app.use((error: unknown, _req: Request, res: Response, _next: NextFunction) => {
  handledErrors.push(error)
  res.status(500).json({ message: '服务器内部错误' })
})

const nonLoopbackCalls: string[] = []
const nonLoopbackApp = express()
nonLoopbackApp.use(modelCatalogSnapshotRebuildInternalPrefix, (req, _res, next) => {
  Object.defineProperty(req.socket, 'remoteAddress', {
    configurable: true,
    value: '192.0.2.10'
  })
  next()
}, createModelCatalogSnapshotRebuildRouter({
  secret,
  rebuildAll: async () => {
    nonLoopbackCalls.push('all')
  },
  rebuildPersonal: async (systemAccountId) => {
    nonLoopbackCalls.push(systemAccountId)
  }
}))

const server = app.listen(0, '127.0.0.1')
const nonLoopbackServer = nonLoopbackApp.listen(0, '127.0.0.1')

try {
  await listen(server)
  await listen(nonLoopbackServer)
  const baseUrl = `http://127.0.0.1:${serverPort(server)}`
  const nonLoopbackBaseUrl = `http://127.0.0.1:${serverPort(nonLoopbackServer)}`

  assertSignatureGoldenVector()
  assertServerWiring()

  const all = await request(baseUrl, Buffer.from('{"scope":"all"}'))
  assert.equal(all.statusCode, 202)
  assert.match(all.headers['content-type'] ?? '', /^application\/json(?:;|$)/)
  assert.equal(all.headers['cache-control'], 'no-store')
  assert.deepEqual(parseJson(all), { accepted: true })
  assert.deepEqual(rebuildCalls.at(-1), { scope: 'all' })

  await runGoClientCrossRuntime(baseUrl)

  const personal = await request(baseUrl, Buffer.from('{"scope":"personal","systemAccountId":"  account-1  "}'))
  assert.equal(personal.statusCode, 202)
  assert.deepEqual(parseJson(personal), { accepted: true })
  assert.deepEqual(rebuildCalls.at(-1), { scope: 'personal', systemAccountId: 'account-1' })

  deferNextAll = true
  const deferredRequest = request(baseUrl, Buffer.from('{"scope":"all"}'))
  const acceptedBeforeRebuildCompletes = await Promise.race([
    deferredRequest,
    new Promise<'timeout'>((resolve) => setTimeout(() => resolve('timeout'), 100))
  ])
  assert.equal(acceptedBeforeRebuildCompletes, 'timeout', '202 必须等待快照重建完成')
  resolveDeferredAll?.()
  assert.equal((await deferredRequest).statusCode, 202)

  for (const body of [
    '{}',
    '{"scope":"personal"}',
    '{"scope":"personal","systemAccountId":"   "}',
    '{"scope":"all","systemAccountId":"account-1"}',
    '{"scope":"other"}',
    '{"scope":"all","extra":true}',
    '[]',
    'null',
    '"all"',
    '{"scope":'
  ]) {
    assert.equal((await request(baseUrl, Buffer.from(body))).statusCode, 400, `${body} 必须返回 400`)
  }

  const authBody = Buffer.from('{"scope":"all"}')
  assert.equal((await request(baseUrl, authBody, { omitSignature: true })).statusCode, 401)
  assert.equal((await request(baseUrl, authBody, { signature: `v1=${'0'.repeat(64)}` })).statusCode, 401)
  const validSignature = createModelCatalogSnapshotRebuildSignature(secret, authBody)
  for (const signature of [
    `v2=${validSignature.slice(3)}`,
    `v1=${'a'.repeat(63)}`,
    `v1=${validSignature.slice(3).toUpperCase()}`,
    [validSignature, validSignature]
  ]) {
    assert.equal((await request(baseUrl, authBody, { signature })).statusCode, 401)
  }
  assert.equal((await request(baseUrl, Buffer.from('{ "scope": "all" }'), {
    signature: validSignature
  })).statusCode, 401, '签名必须绑定原始字节')
  assert.equal((await request(baseUrl, authBody, { omitContentType: true })).statusCode, 415)
  assert.equal((await request(baseUrl, authBody, { contentType: 'text/plain' })).statusCode, 415)
  assert.equal((await request(baseUrl, authBody, {
    headers: { 'content-encoding': 'Identity' }
  })).statusCode, 202)
  assert.equal((await request(baseUrl, gzipSync(authBody), {
    headers: { 'content-encoding': 'gzip' }
  })).statusCode, 415)
  assert.equal((await request(baseUrl, authBody, {
    headers: { 'content-encoding': 'gzip, identity' }
  })).statusCode, 415)

  const exactLimitBody = createExactPersonalBody(1024)
  assert.equal((await request(baseUrl, exactLimitBody)).statusCode, 202)
  assert.equal((await request(baseUrl, createExactPersonalBody(1025))).statusCode, 413)

  const beforeStrictPaths = rebuildCalls.length
  assert.equal((await request(baseUrl, authBody, {
    path: '/__AIINTERNAL__/v1/model-catalog-snapshots/rebuild'
  })).statusCode, 404)
  assert.equal((await request(baseUrl, authBody, {
    path: '/__aiinternal__/v1/Model-catalog-snapshots/rebuild'
  })).statusCode, 404)
  assert.equal((await request(baseUrl, authBody, {
    path: '/__aiinternal__/v1/model-catalog-snapshots/rebuild/'
  })).statusCode, 404)
  assert.equal(rebuildCalls.length, beforeStrictPaths)

  assert.equal((await request(baseUrl, Buffer.alloc(0), {
    method: 'GET',
    omitSignature: true,
    omitContentType: true
  })).statusCode, 404)
  assert.equal((await request(baseUrl, Buffer.alloc(0), {
    method: 'OPTIONS',
    omitSignature: true,
    omitContentType: true
  })).statusCode, 404)
  assert.equal((await request(baseUrl, Buffer.alloc(0), {
    path: '/__aiinternal__/v1/other',
    method: 'GET',
    omitSignature: true,
    omitContentType: true
  })).statusCode, 404)

  const nonLoopback = await request(nonLoopbackBaseUrl, authBody)
  assert.equal(nonLoopback.statusCode, 403)
  assert.deepEqual(parseJson(nonLoopback), { message: '禁止访问' })
  assert.deepEqual(nonLoopbackCalls, [])

  const failure = await request(baseUrl, Buffer.from('{"scope":"personal","systemAccountId":"throws"}'))
  assert.equal(failure.statusCode, 500)
  assert.equal(handledErrors.length, 1)
  assert.equal(handledErrors[0] instanceof Error ? handledErrors[0].message : undefined, 'snapshot rebuild failed')

  console.log('模型目录快照重建 HTTP bridge 回归通过')
} finally {
  resolveDeferredAll?.()
  await closeServer(nonLoopbackServer)
  await closeServer(server)
}

function assertSignatureGoldenVector(): void {
  const domain = 'juhe-ai:model-catalog-snapshot-rebuild:v1\n'
  const goldenSecret = 'model-catalog-snapshot-rebuild-golden-secret'
  const body = Buffer.from('{"scope":"all"}')
  const independent = `v1=${createHmac('sha256', goldenSecret).update(domain, 'utf8').update(body).digest('hex')}`
  assert.equal(modelCatalogSnapshotRebuildSignatureDomain, domain)
  assert.equal(createModelCatalogSnapshotRebuildSignature(goldenSecret, body), independent)
}

function assertServerWiring(): void {
  const source = readFileSync(new URL('../../server.ts', import.meta.url), 'utf8')
  const snapshotServiceSource = readFileSync(new URL('../../modules/model-pricing/published-model-catalog.service.ts', import.meta.url), 'utf8')
  const snapshotMountIndex = source.indexOf('createModelCatalogSnapshotRebuildRouter({')
  const accountMountIndex = source.indexOf('mountAccountHealthCheckDispatchBridge(app, {')
  assert(snapshotMountIndex >= 0, 'server.ts 必须装配模型目录快照重建路由')
  assert(accountMountIndex > snapshotMountIndex, '模型目录快照重建路由必须先于旧 internal router 装配')
  assert(source.includes("if (runtimeConfig.databaseDriver === 'postgres')"), '快照重建 bridge 只能在 PostgreSQL 共存迁移场景挂载')
  assert(source.includes("reconcileModelCatalogSnapshotScopeAsync({ scope: 'all' })"), 'all 必须消费持久 dirty generation')
  assert(source.includes("reconcileModelCatalogSnapshotScopeAsync({ scope: 'personal', systemAccountId })"), 'personal 必须消费对应 owner 的持久 dirty generation')
  assert(source.includes('if (result && !result.acknowledged) throw new Error'), 'generation 未确认时 bridge 不得返回成功')
  assert(source.includes("runtimeConfig.databaseDriver === 'postgres'"), '快照重建 bridge 必须限制为 PostgreSQL，避免绕过 SQLite DB service 单写者')
  assert(source.includes('reconcileDirtyModelCatalogSnapshotsOnceAsync()'), 'Node 必须后台扫描并重试持久 dirty generation')
  assert(source.includes('setInterval(run, 5_000)'), 'dirty generation 必须按有界周期自动重试')
  assert(snapshotServiceSource.includes('let snapshotRebuildTail: Promise<void> = Promise.resolve()'), '模型目录重建必须共享串行尾指针')
  assert(snapshotServiceSource.includes('function enqueueSnapshotRebuild<T>'), '模型目录重建必须通过统一串行队列')
  assert(snapshotServiceSource.includes('rebuildPublishedModelCatalogSnapshotsForSystemAccountInternalAsync'), '全量重建不得递归进入公共队列造成死锁')
  assert(snapshotServiceSource.includes('let allRebuildInFlight: Promise<number> | undefined'), '全量重建必须复用进行中的任务')
  assert(snapshotServiceSource.includes('let allRebuildAgain = false'), '全量重建期间的新变更必须只追加一次后续重建')
  assert(snapshotServiceSource.includes('const personalRebuildInFlight = new Map'), '同一 personal owner 的重建必须复用进行中的任务')
  assert(snapshotServiceSource.includes('const personalRebuildAgain = new Set'), '同一 personal owner 的新变更必须只追加一次后续重建')
  assert(snapshotServiceSource.includes('personalRebuildInFlight.size > 0'), '不同 personal owner 并发时必须升级为共享全量重建')
  assert(snapshotServiceSource.includes('return enqueueSnapshotRebuild(async () => {'), '启动预热必须和重建共享串行队列')
}

function createExactPersonalBody(totalBytes: number): Buffer {
  const prefix = '{"scope":"personal","systemAccountId":"'
  const suffix = '"}'
  const body = Buffer.from(`${prefix}${'x'.repeat(totalBytes - Buffer.byteLength(prefix) - Buffer.byteLength(suffix))}${suffix}`)
  assert.equal(body.length, totalBytes)
  return body
}

interface RequestOptions {
  path?: string
  method?: string
  contentType?: string
  signature?: string | string[]
  omitSignature?: boolean
  omitContentType?: boolean
  headers?: http.OutgoingHttpHeaders
}

function request(baseUrl: string, body: Buffer, options: RequestOptions = {}): Promise<{
  statusCode: number
  headers: http.IncomingHttpHeaders
  body: Buffer
}> {
  const signature = options.omitSignature
    ? undefined
    : options.signature ?? createModelCatalogSnapshotRebuildSignature(secret, body)
  const headers: http.OutgoingHttpHeaders = {
    'content-length': body.length,
    ...options.headers
  }
  if (!options.omitContentType) headers['content-type'] = options.contentType ?? 'application/json; charset=utf-8'
  if (signature !== undefined) headers['x-juhe-ai-signature'] = signature

  return new Promise((resolve, reject) => {
    const req = http.request(`${baseUrl}${options.path ?? '/__aiinternal__/v1/model-catalog-snapshots/rebuild'}`, {
      method: options.method ?? 'POST',
      headers
    }, (res) => {
      const chunks: Buffer[] = []
      res.on('data', (chunk: Buffer) => chunks.push(Buffer.from(chunk)))
      res.on('end', () => resolve({
        statusCode: res.statusCode ?? 0,
        headers: res.headers,
        body: Buffer.concat(chunks)
      }))
    })
    req.on('error', reject)
    req.end(body)
  })
}

function parseJson(response: { body: Buffer }): unknown {
  return JSON.parse(response.body.toString('utf8')) as unknown
}

async function runGoClientCrossRuntime(baseUrl: string): Promise<void> {
  const goRoot = resolve(process.cwd(), '../backend-go')
  await new Promise<void>((resolvePromise, reject) => {
    const child = spawn('go', [
      'test', './internal/platform/modelcatalogsnapshotrebuild',
      '-run', '^TestCrossRuntimeNodeBridge$', '-count=1'
    ], {
      cwd: goRoot,
      env: {
        ...process.env,
        JUHE_MODEL_CATALOG_SNAPSHOT_BRIDGE_URL: baseUrl,
        JUHE_MODEL_CATALOG_SNAPSHOT_BRIDGE_SECRET: secret
      },
      stdio: ['ignore', 'pipe', 'pipe']
    })
    let output = ''
    child.stdout.on('data', (chunk: Buffer) => { output += chunk.toString('utf8') })
    child.stderr.on('data', (chunk: Buffer) => { output += chunk.toString('utf8') })
    child.on('error', reject)
    child.on('close', (code) => {
      if (code !== 0) {
        reject(new Error(`Go -> Node bridge cross-runtime regression failed (${code}): ${output}`))
        return
      }
      resolvePromise()
    })
  })
  assert.deepEqual(rebuildCalls.at(-1), { scope: 'all' })
}

async function listen(server: http.Server): Promise<void> {
  if (server.listening) return
  await new Promise<void>((resolve, reject) => {
    server.once('listening', resolve)
    server.once('error', reject)
  })
}

async function closeServer(server: http.Server): Promise<void> {
  if (!server.listening) return
  await new Promise<void>((resolve, reject) => {
    server.close((error) => error ? reject(error) : resolve())
  })
}

function serverPort(server: http.Server): number {
  const address = server.address()
  assert(address && typeof address !== 'string')
  return address.port
}
