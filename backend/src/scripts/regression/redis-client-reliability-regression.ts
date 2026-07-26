import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import { isRecoverableRedisClientError } from '../../shared/redis-client.js'

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
const streamQueueSource = readFileSync(new URL('../../shared/redis-stream-queue.ts', import.meta.url), 'utf8')
assert.match(
  streamQueueSource,
  /isRecoverableRedisClientError\(error\)[\s\S]*invalidateRedisClient\(this\.redisUrl, client\)/,
  'Redis Stream producer 命令超时后必须淘汰当前共享 client'
)
assert.match(
  streamQueueSource,
  /isRecoverableRedisClientError\(error\)[\s\S]*resetConsumerClient\(client\)/,
  'Redis Stream consumer 命令超时后必须重建独占 client'
)
assert.equal(isRecoverableRedisClientError(Object.assign(new Error('Command timed out'), { name: 'TimeoutError' })), true)
assert.equal(isRecoverableRedisClientError(Object.assign(new Error('socket closed'), { code: 'ECONNRESET' })), true)
assert.equal(isRecoverableRedisClientError(new Error('BUSYGROUP Consumer Group name already exists')), false)

console.log('Redis client 可靠性回归通过：默认 fail-fast，命令超时会淘汰失效代际并重建 producer/consumer')

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
