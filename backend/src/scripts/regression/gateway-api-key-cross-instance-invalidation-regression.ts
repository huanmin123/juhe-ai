import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('../../storage/gateway-api-key.repository.ts', import.meta.url), 'utf8')
const body = functionBody(source, 'validateGatewayApiKeyAsync')
const synchronization = body.indexOf("await syncGatewayCacheInvalidationsFromRuntimeState({ force: true })")
const processCacheRead = body.indexOf('gatewayApiKeyProcessCache.get(keyHash)')

assert(synchronization >= 0, '跨实例 API Key 鉴权必须强制同步 Redis runtime state 失效版本')
assert(processCacheRead >= 0, 'API Key 异步鉴权应保留进程内热缓存')
assert(synchronization < processCacheRead, '必须先同步失效版本，再读取进程内 API Key 热缓存')
assert.doesNotMatch(
  body,
  /void syncGatewayCacheInvalidationsFromRuntimeState\(\)\.catch\(\(\) => undefined\)/,
  'API Key 鉴权不得在已命中本地缓存后异步同步失效版本'
)

console.log('跨实例 API Key 失效回归通过：鉴权读取本地热缓存前强制确认 Redis 失效版本')

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
