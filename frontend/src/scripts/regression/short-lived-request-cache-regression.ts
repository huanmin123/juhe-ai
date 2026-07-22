import { createShortLivedRequestCache } from '@/shared/shortLivedRequestCache'

async function main(): Promise<void> {
  const cache = createShortLivedRequestCache<number>({ ttlMs: 1_000, maxEntries: 4 })
  let loads = 0

  const [first, second] = await Promise.all([
    cache.load('same-key', async () => {
      loads += 1
      await delay(10)
      return 11
    }),
    cache.load('same-key', async () => {
      loads += 1
      return 22
    })
  ])
  assertEqual(first, 11, '第一个并发请求应返回首次 loader 的结果')
  assertEqual(second, 11, '第二个并发请求应复用首次 loader 的结果')
  assertEqual(loads, 1, '相同 key 的并发请求应复用同一个 loader')

  const cached = await cache.load('same-key', async () => {
    loads += 1
    return 33
  })
  assertEqual(cached, 11, '短缓存命中时应返回已缓存结果')
  assertEqual(loads, 1, '短缓存命中时不应重新执行 loader')

  const forced = await cache.load('same-key', async () => {
    loads += 1
    return 44
  }, true)
  assertEqual(forced, 44, 'force=true 应返回重新加载结果')
  assertEqual(loads, 2, 'force=true 应绕过短缓存重新加载')

  cache.remove('same-key')
  const afterRemove = await cache.load('same-key', async () => {
    loads += 1
    return 55
  })
  assertEqual(afterRemove, 55, 'remove 后应返回重新加载结果')
  assertEqual(loads, 3, 'remove 后应重新加载')

  console.log('short-lived request cache regression passed')
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

function assertEqual<T>(actual: T, expected: T, message: string): void {
  if (!Object.is(actual, expected)) {
    throw new Error(`${message}，实际 ${String(actual)}，预期 ${String(expected)}`)
  }
}

await main()
