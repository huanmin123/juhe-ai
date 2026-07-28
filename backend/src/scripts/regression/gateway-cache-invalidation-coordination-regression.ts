import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import { createGatewayCacheInvalidationSyncCoordinator } from '../../shared/gateway-cache-invalidation.js'

await assertForcedSyncStartsPostCallRound()
await assertFailedSyncCanBeRetried()
assertRuntimeLoadGenerationFence()

console.log('网关缓存失效协调回归通过：强制同步不复用旧轮次，失败后可重试，迟到 runtime 读取受代际围栏保护')

async function assertForcedSyncStartsPostCallRound(): Promise<void> {
  const releases: Array<() => void> = []
  let started = 0
  const sync = createGatewayCacheInvalidationSyncCoordinator({
    intervalMs: 1_000,
    syncRound: async () => {
      started += 1
      await new Promise<void>((resolve) => releases.push(resolve))
    }
  })

  const beforePublishRound = sync({ force: true })
  await waitUntil(() => started === 1)
  const afterPublishRound = sync({ force: true })
  releases.shift()?.()
  await waitUntil(() => started === 2)
  let afterPublishResolved = false
  void afterPublishRound.then(() => { afterPublishResolved = true })
  await Promise.resolve()
  assert.equal(afterPublishResolved, false, '失效发布后发起的强制同步不能复用发布前的读轮次')
  releases.shift()?.()
  await Promise.all([beforePublishRound, afterPublishRound])
  assert.equal(started, 2, '并发强制同步应补一轮发布后读取')
}

async function assertFailedSyncCanBeRetried(): Promise<void> {
  let attempts = 0
  const observedErrors: unknown[] = []
  const sync = createGatewayCacheInvalidationSyncCoordinator({
    intervalMs: 1_000,
    syncRound: async () => {
      attempts += 1
      if (attempts === 1) throw new Error('forced runtime-state read failure')
    },
    onError: (error) => observedErrors.push(error)
  })

  await assert.rejects(sync({ force: true }), /forced runtime-state read failure/)
  await sync({ force: true })
  assert.equal(attempts, 2, '同步失败不得锁死协调器，下次强制读必须可重试')
  assert.equal(observedErrors.length, 1, '每次失败应有一次可观测回调')
}

function assertRuntimeLoadGenerationFence(): void {
  const source = readFileSync(new URL('../../modules/gateway/runtime/runtime-cache.service.ts', import.meta.url), 'utf8')
  const body = functionBody(source, 'loadGatewayRuntimeOnce')
  assert.match(body, /gatewayRuntimeLoadAttemptLimit/, '迟到 runtime 读取应使用有界代际重试')
  assertOrdered(body, [
    'const runtime = await pending.promise',
    'isGatewayRuntimeLoadGenerationCurrent(pending.generation)',
    'return runtime'
  ], '只有仍处于当前代际的 runtime 读取才能返回 pre-auth')
  assert.match(body, /连续失效后仍发生变化/, '连续竞态耗尽后必须失败关闭，不得退回旧 runtime')
}

async function waitUntil(predicate: () => boolean): Promise<void> {
  for (let attempt = 0; attempt < 100; attempt += 1) {
    if (predicate()) return
    await new Promise<void>((resolve) => setTimeout(resolve, 0))
  }
  throw new Error('等待并发回归状态超时')
}

function assertOrdered(source: string, markers: readonly string[], message: string): void {
  let previous = -1
  for (const marker of markers) {
    const index = source.indexOf(marker, previous + 1)
    assert(index > previous, `${message}：缺少或顺序错误 ${marker}`)
    previous = index
  }
}

function functionBody(source: string, functionName: string): string {
  const start = source.indexOf(`function ${functionName}`)
  assert(start >= 0, `缺少函数 ${functionName}`)
  const openBrace = source.indexOf('{', start)
  assert(openBrace >= 0, `${functionName} 缺少函数体`)
  let depth = 0
  for (let index = openBrace; index < source.length; index += 1) {
    if (source[index] === '{') depth += 1
    if (source[index] === '}') {
      depth -= 1
      if (depth === 0) return source.slice(openBrace, index + 1)
    }
  }
  throw new Error(`${functionName} 函数体未闭合`)
}
