import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('../../storage/gateway-api-key.repository.ts', import.meta.url), 'utf8')
const body = functionBody(source, 'validateGatewayApiKeyAsync')
const processCacheRead = body.indexOf('gatewayApiKeyProcessCache.get(keyHash)')
const cachedReturn = body.indexOf('return cloneGatewayApiKeyRow(processCached.row)')
const backgroundSynchronization = body.indexOf('void syncGatewayCacheInvalidationsFromRuntimeState().catch(() => undefined)')
const cacheMissSynchronization = body.indexOf('await syncGatewayCacheInvalidationsFromRuntimeState()', cachedReturn)

assert(processCacheRead >= 0, 'API Key 异步鉴权应保留进程内热缓存')
assert(backgroundSynchronization > processCacheRead, '热缓存命中后应在后台同步 Redis 失效版本')
assert(backgroundSynchronization < cachedReturn, '后台同步必须在返回热缓存前发起')
assert(cacheMissSynchronization > cachedReturn, '本地缓存 miss 时必须同步 Redis 失效版本并保持失败关闭')
assert.match(source, /GATEWAY_API_KEY_CACHE_TTL_MS\s*=\s*60_000/, '断线降级必须受 60 秒进程内缓存 TTL 约束')

console.log('跨实例 API Key 失效回归通过：热缓存命中非阻塞同步，miss 失败关闭，断线降级受 TTL 约束')

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
