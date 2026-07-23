import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import { createAppBrandSettingsResource } from '@/composables/useAppBrand'

interface BrandValue {
  appName: string
  appIcon: string
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, reject, resolve }
}

const firstRequest = deferred<BrandValue>()
let fetchCount = 0
const committed: BrandValue[] = []
const resource = createAppBrandSettingsResource({
  fetch: () => {
    fetchCount += 1
    return firstRequest.promise
  },
  commit: (value) => committed.push(value)
})

const firstLoad = resource.load()
const sharedLoad = resource.load()
assert.equal(fetchCount, 1, '并发品牌加载必须 singleflight')
firstRequest.resolve({ appName: '首次品牌', appIcon: '/first.svg' })
assert.deepEqual(await firstLoad, { appName: '首次品牌', appIcon: '/first.svg' })
assert.deepEqual(await sharedLoad, { appName: '首次品牌', appIcon: '/first.svg' })
assert.equal(committed.length, 1, 'singleflight 结果只能提交一次')
assert.deepEqual(await resource.load(), { appName: '首次品牌', appIcon: '/first.svg' })
assert.equal(fetchCount, 1, '成功品牌必须复用会话缓存')

const staleRequest = deferred<BrandValue>()
const staleCommitted: BrandValue[] = []
const staleResource = createAppBrandSettingsResource({
  fetch: () => staleRequest.promise,
  commit: (value) => staleCommitted.push(value)
})
const staleLoad = staleResource.load()
staleResource.set({ appName: '管理员新品牌', appIcon: '/new.svg' })
staleRequest.resolve({ appName: '旧公共品牌', appIcon: '/old.svg' })
assert.deepEqual(await staleLoad, { appName: '管理员新品牌', appIcon: '/new.svg' }, '旧请求迟到时应返回当前 revision 的品牌')
assert.deepEqual(staleCommitted, [{ appName: '管理员新品牌', appIcon: '/new.svg' }], '旧请求不得覆盖管理员保存的新品牌')

let retryCount = 0
const retryResource = createAppBrandSettingsResource({
  fetch: async () => {
    retryCount += 1
    if (retryCount === 1) throw new Error('temporary failure')
    return { appName: '重试品牌', appIcon: '/retry.svg' }
  },
  commit: () => undefined
})
await assert.rejects(retryResource.load(), /temporary failure/)
assert.deepEqual(await retryResource.load(), { appName: '重试品牌', appIcon: '/retry.svg' })
assert.equal(retryCount, 2, '失败不得写缓存或保留 rejected promise')

const brandSource = readFileSync(new URL('../../composables/useAppBrand.ts', import.meta.url), 'utf8')
const loginSource = readFileSync(new URL('../../views/login/LoginView.vue', import.meta.url), 'utf8')
const layoutSource = readFileSync(new URL('../../layouts/AppLayout.vue', import.meta.url), 'utf8')
assert.doesNotMatch(brandSource, /loadGlobalBrandSettings/, '品牌 composable 只应保留一个公开加载入口')
assert.match(loginSource, /loadAppBrandSettings/, '登录页应复用统一品牌加载入口')
assert.match(layoutSource, /loadAppBrandSettings/, '应用壳应复用统一品牌加载入口')

console.log('app brand singleflight regression passed')
