import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

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

console.log('Redis client 可靠性回归通过：默认 fail-fast，不保留无界 offline queue')

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
