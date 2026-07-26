import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createServer, type Socket } from 'node:net'
import { performance } from 'node:perf_hooks'

import {
  closeRedisClients,
  hasRedisClient,
  isRecoverableRedisClientError,
  RedisOperationDeadlineError,
  runRedisOperationWithDeadline,
  type RedisCommandClient
} from '../../shared/redis-client.js'
import { RedisStreamQueue } from '../../shared/redis-stream-queue.js'

const source = readFileSync(new URL('../../shared/redis-client.ts', import.meta.url), 'utf8')
const createClientBody = functionBody(source, 'createRedisClient')

assert.match(
  createClientBody,
  /disableOfflineQueue:\s*options\.disableOfflineQueue\s*\?\?\s*true/,
  'Redis client 默认必须禁用 offline queue，断线命令应交给调用方的有界重试处理'
)
assert.doesNotMatch(
  createClientBody,
  /options\.disableOfflineQueue\s*===\s*true/,
  'Redis client 不得仅在调用方显式指定时才禁用 offline queue'
)

assert.match(source, /export async function invalidateRedisClient/, '共享 Redis client 必须提供按代际淘汰入口')
assert.match(
  source,
  /const generation = getOrCreateRedisClientGeneration\(normalizedUrl\)[\s\S]*invalidateRedisClientGeneration\(normalizedUrl, generation\)/,
  'deadline 操作必须捕获 exact Promise generation，不能在超时后仅按 URL 淘汰当前代际'
)
assert.match(
  source,
  /redisClients\.get\(normalizedUrl\) !== expectedGeneration[\s\S]*redisClients\.delete\(normalizedUrl\)/,
  '共享 client 淘汰必须用 generation identity 做 CAS'
)
const streamQueueSource = readFileSync(new URL('../../shared/redis-stream-queue.ts', import.meta.url), 'utf8')
assert.match(
  streamQueueSource,
  /runRedisOperationWithDeadline\(this\.redisUrl,[\s\S]*Redis Stream 入队[\s\S]*timeoutMs: this\.producerTimeoutMs/,
  'Redis Stream 共享 producer 必须通过统一 hard deadline 执行入队脚本'
)
assert.match(
  streamQueueSource,
  /isRecoverableRedisClientError\(error\)[\s\S]*resetConsumerClient\(client\)/,
  'Redis Stream consumer 命令超时后必须重建独占 client'
)
assert.equal(isRecoverableRedisClientError(Object.assign(new Error('Command timed out'), { name: 'TimeoutError' })), true)
assert.equal(isRecoverableRedisClientError(Object.assign(new Error('socket closed'), { code: 'ECONNRESET' })), true)
assert.equal(isRecoverableRedisClientError(new Error('BUSYGROUP Consumer Group name already exists')), false)

// 先预热 ESM 依赖，计时只覆盖 Redis 连接与命令，不把冷启动模块解析算入网络 deadline。
await import('redis')
await assertSharedClientDeadlineInvalidationAndReconnect()
await assertConnectionGenerationCas()
await assertInjectedStreamProducerDeadline()

console.log('Redis client 可靠性回归通过：hard deadline 有界失败，失效共享代际会被淘汰并在后续操作重连')

async function assertSharedClientDeadlineInvalidationAndReconnect(): Promise<void> {
  const sockets = new Set<Socket>()
  let responsive = false
  let connectionCount = 0
  const server = createServer((socket) => {
    connectionCount += 1
    sockets.add(socket)
    socket.on('close', () => sockets.delete(socket))
    let pending = Buffer.alloc(0)
    socket.on('data', (chunk) => {
      pending = Buffer.concat([pending, chunk])
      while (true) {
        const parsed = parseRespCommand(pending)
        if (!parsed) return
        pending = pending.subarray(parsed.consumedBytes)
        if (!responsive) continue
        socket.write(redisFixtureResponse(parsed.command))
      }
    })
  })

  await new Promise<void>((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', resolve)
  })
  const address = server.address()
  assert(address && typeof address === 'object', 'TCP fixture 必须取得监听地址')
  const redisUrl = `redis://127.0.0.1:${address.port}/0`

  try {
    const startedAt = performance.now()
    await assert.rejects(
      runRedisOperationWithDeadline(redisUrl, {
        operationName: 'Redis 半开连接 GET',
        timeoutMs: 250
      }, (client) => client.get('deadline-fixture')),
      (error: unknown) => error instanceof RedisOperationDeadlineError
    )
    const elapsedMs = performance.now() - startedAt
    assert(elapsedMs < 2_500, `Redis hard deadline 应有界失败，实际耗时 ${Math.round(elapsedMs)}ms`)
    await waitFor(() => !hasRedisClient(redisUrl), 750)
    assert.equal(hasRedisClient(redisUrl), false, 'deadline 后必须淘汰共享 Redis client 代际')

    responsive = true
    const value = await runRedisOperationWithDeadline(redisUrl, {
      operationName: 'Redis deadline 后重连 GET',
      timeoutMs: 3_000
    }, (client) => client.get('deadline-fixture'))
    assert.equal(value, 'ok', 'deadline 后的下一次操作必须通过新连接成功')
    assert(connectionCount >= 2, `deadline 后必须建立新连接，实际连接数 ${connectionCount}`)

    const queue = new RedisStreamQueue<{ id: string }>({
      streamKey: 'juhe-ai:test:deadline-stream',
      groupName: 'juhe-ai:test:deadline-group',
      redisUrl,
      producerTimeoutMs: 250
    })
    const connectionCountBeforeQueueFailure = connectionCount
    responsive = false
    const queueStartedAt = performance.now()
    await assert.rejects(
      queue.enqueue({ id: 'deadline' }),
      (error: unknown) => error instanceof RedisOperationDeadlineError
    )
    assert(performance.now() - queueStartedAt < 2_500, 'Redis Stream producer 必须受统一 hard deadline 约束')
    await waitFor(() => !hasRedisClient(redisUrl), 750)
    responsive = true
    const reconnectedQueue = new RedisStreamQueue<{ id: string }>({
      streamKey: 'juhe-ai:test:deadline-stream',
      groupName: 'juhe-ai:test:deadline-group',
      redisUrl,
      producerTimeoutMs: 3_000
    })
    assert.equal(await reconnectedQueue.enqueue({ id: 'reconnected' }), 'OK', 'Redis Stream producer deadline 后必须通过新连接恢复')
    assert(connectionCount > connectionCountBeforeQueueFailure, 'Redis Stream producer deadline 后必须重建共享连接')
  } finally {
    await closeRedisClients()
    for (const socket of sockets) socket.destroy()
    await new Promise<void>((resolve) => server.close(() => resolve()))
  }
}

async function assertConnectionGenerationCas(): Promise<void> {
  const sockets = new Set<Socket>()
  let responsive = false
  let connectionCount = 0
  const server = createServer((socket) => {
    connectionCount += 1
    sockets.add(socket)
    socket.on('close', () => sockets.delete(socket))
    let pending = Buffer.alloc(0)
    socket.on('data', (chunk) => {
      pending = Buffer.concat([pending, chunk])
      while (true) {
        const parsed = parseRespCommand(pending)
        if (!parsed) return
        pending = pending.subarray(parsed.consumedBytes)
        // 丢弃旧连接的 AUTH，保证 A/B 都停留在同一个尚未 ready 的 P1 代际。
        if (!responsive) continue
        socket.write(redisFixtureResponse(parsed.command))
      }
    })
  })

  await new Promise<void>((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', resolve)
  })
  const address = server.address()
  assert(address && typeof address === 'object', '连接代际 fixture 必须取得监听地址')
  const redisUrl = `redis://fixture-user:fixture-password@127.0.0.1:${address.port}/0`

  try {
    const first = runRedisOperationWithDeadline(redisUrl, {
      operationName: 'Redis 连接代际 A',
      timeoutMs: 250
    }, (client) => client.get('generation'))
    await delay(20)
    const second = runRedisOperationWithDeadline(redisUrl, {
      operationName: 'Redis 连接代际 B',
      timeoutMs: 1_000
    }, (client) => client.get('generation'))

    await assert.rejects(first, (error: unknown) => error instanceof RedisOperationDeadlineError)
    assert.equal(hasRedisClient(redisUrl), false, 'A 超时后应同步摘除 P1 代际')
    await assert.rejects(
      second,
      (error: unknown) => error instanceof RedisOperationDeadlineError || /连接不可用|closed/i.test(String(error))
    )
    assert.equal(hasRedisClient(redisUrl), false, 'P1 被主动取消后，共享该代际的 B 不得留下失效缓存')

    responsive = true
    assert.equal(await runRedisOperationWithDeadline(redisUrl, {
      operationName: 'Redis 连接代际 C',
      timeoutMs: 3_000
    }, (client) => client.get('generation')), 'ok', 'C 应创建并使用 P2 代际')
    const connectionCountAfterReplacement = connectionCount
    assert.equal(hasRedisClient(redisUrl), true, 'C 成功后 P2 必须保留在共享池')
    assert.equal(await runRedisOperationWithDeadline(redisUrl, {
      operationName: 'Redis 连接代际 P2 复用',
      timeoutMs: 3_000
    }, (client) => client.get('generation')), 'ok')
    assert.equal(connectionCount, connectionCountAfterReplacement, 'B 超时后应继续复用 P2，不得触发连接风暴')
  } finally {
    await closeRedisClients()
    for (const socket of sockets) socket.destroy()
    await new Promise<void>((resolve) => server.close(() => resolve()))
  }
}

async function assertInjectedStreamProducerDeadline(): Promise<void> {
  let destroyed = false
  const client = {
    isOpen: true,
    isReady: true,
    connect: async () => undefined,
    get: async () => null,
    set: async () => null,
    del: async () => 0,
    eval: async () => await new Promise<never>(() => undefined),
    sendCommand: async () => undefined,
    on: () => undefined,
    destroy: () => {
      destroyed = true
    }
  } satisfies RedisCommandClient
  const queue = new RedisStreamQueue<{ id: string }>({
    streamKey: 'juhe-ai:test:injected-deadline-stream',
    groupName: 'juhe-ai:test:injected-deadline-group',
    redisUrl: 'redis://127.0.0.1:1/0',
    producerTimeoutMs: 150,
    producerClient: async () => client
  })
  const keepAlive = setInterval(() => undefined, 1_000)
  try {
    await assert.rejects(queue.enqueue({ id: 'timeout' }), (error: unknown) => error instanceof RedisOperationDeadlineError)
    assert.equal(destroyed, true, '注入 producerClient 的入队超时也必须销毁失效 client')
  } finally {
    clearInterval(keepAlive)
  }
}

interface ParsedRespCommand {
  command: string[]
  consumedBytes: number
}

function parseRespCommand(input: Buffer): ParsedRespCommand | undefined {
  if (input.length === 0) return undefined
  assert.equal(input[0], 42, 'Redis fixture 仅接受 RESP array 命令')
  const headerEnd = input.indexOf('\r\n')
  if (headerEnd < 0) return undefined
  const itemCount = Number(input.subarray(1, headerEnd).toString('utf8'))
  assert(Number.isInteger(itemCount) && itemCount >= 0, 'RESP array 长度非法')
  const command: string[] = []
  let offset = headerEnd + 2
  for (let index = 0; index < itemCount; index += 1) {
    if (input.length <= offset) return undefined
    assert.equal(input[offset], 36, 'Redis fixture 仅接受 bulk string 参数')
    const lengthEnd = input.indexOf('\r\n', offset)
    if (lengthEnd < 0) return undefined
    const byteLength = Number(input.subarray(offset + 1, lengthEnd).toString('utf8'))
    assert(Number.isInteger(byteLength) && byteLength >= 0, 'RESP bulk string 长度非法')
    const valueStart = lengthEnd + 2
    const valueEnd = valueStart + byteLength
    if (input.length < valueEnd + 2) return undefined
    assert.equal(input.subarray(valueEnd, valueEnd + 2).toString('utf8'), '\r\n', 'RESP bulk string 结尾非法')
    command.push(input.subarray(valueStart, valueEnd).toString('utf8'))
    offset = valueEnd + 2
  }
  return { command, consumedBytes: offset }
}

function redisFixtureResponse(command: string[]): string {
  const commandName = command[0]?.toUpperCase()
  if (commandName === 'GET') return '$2\r\nok\r\n'
  if (commandName === 'PING') return '+PONG\r\n'
  return '+OK\r\n'
}

async function waitFor(predicate: () => boolean, timeoutMs: number): Promise<void> {
  const deadlineAt = Date.now() + timeoutMs
  while (!predicate() && Date.now() < deadlineAt) {
    await new Promise((resolve) => setTimeout(resolve, 10))
  }
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

function functionBody(sourceText: string, functionName: string): string {
  const start = sourceText.indexOf(`function ${functionName}`)
  assert(start >= 0, `缺少函数 ${functionName}`)
  let openBrace = -1
  let parenthesisDepth = 0
  for (let index = start; index < sourceText.length; index += 1) {
    const char = sourceText[index]
    if (char === '(') parenthesisDepth += 1
    if (char === ')') parenthesisDepth = Math.max(0, parenthesisDepth - 1)
    if (char === '{' && parenthesisDepth === 0) {
      openBrace = index
      break
    }
  }
  assert(openBrace >= 0, `${functionName} 缺少函数体`)
  let depth = 0
  for (let index = openBrace; index < sourceText.length; index += 1) {
    if (sourceText[index] === '{') depth += 1
    if (sourceText[index] === '}') {
      depth -= 1
      if (depth === 0) return sourceText.slice(openBrace, index + 1)
    }
  }
  throw new Error(`${functionName} 函数体未闭合`)
}
