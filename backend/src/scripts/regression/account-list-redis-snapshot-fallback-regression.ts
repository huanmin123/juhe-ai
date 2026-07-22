import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('../../storage/account-summary.repository.ts', import.meta.url), 'utf8')
const fallbackBody = functionBody(source, 'accountCurrentConcurrencySnapshotFallback')

assert.match(fallbackBody, /runtimeConfig\.runtimeStateDriver !== 'redis'[\s\S]*throw error/, '非 Redis 错误不能被列表快照降级吞掉')
assert.match(fallbackBody, /return new Map<string, number>\(\)/, 'Redis 快照失败时账户列表必须返回空并发快照而不是 500')

for (const functionName of ['loadAuthorizedAccountSummaryContextAsync', 'ownerAccountSummariesFromRowsAsync']) {
  assert.match(
    functionBody(source, functionName),
    /loadAccountCurrentConcurrencyByIdsAsync[\s\S]*accountCurrentConcurrencySnapshotFallback/,
    `${functionName} 必须经过 Redis 并发快照降级边界`
  )
}

console.log('账户列表 Redis 快照降级回归通过：Redis 不可用不再导致账户列表 500')

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
