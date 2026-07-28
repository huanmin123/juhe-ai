import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import {
  createUserReferenceDataResource,
  userReferenceDataScopeKey,
  type UserReferenceDataRequestScope
} from '@/composables/useUserReferenceData'
import type { UserReferenceData } from '@/types/domain'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, reject, resolve }
}

function scope(input: Partial<UserReferenceDataRequestScope> = {}): UserReferenceDataRequestScope {
  return {
    viewerSystemAccountId: input.viewerSystemAccountId ?? 'viewer-a',
    authRevision: input.authRevision ?? 1,
    viewScope: input.viewScope ?? 'self',
    ownerSystemAccountId: input.ownerSystemAccountId ?? 'viewer-a'
  }
}

function fixture(systemAccountId: string): UserReferenceData {
  return {
    systemAccountId,
    providerDefaults: [{
      providerCode: 'gpt',
      defaultGroup: { id: `group-${systemAccountId}`, name: '默认 GPT 分组' },
      defaultRouteStrategy: { id: `route-${systemAccountId}`, name: '默认 GPT 路由', mode: 'normal', status: 'active' }
    }],
    preferredDefaultRouteStrategy: { id: `route-${systemAccountId}`, name: '默认 GPT 路由', mode: 'normal', status: 'active' }
  }
}

const firstRequest = deferred<UserReferenceData>()
let fetchCount = 0
const resource = createUserReferenceDataResource({
  fetch: () => {
    fetchCount += 1
    return firstRequest.promise
  }
})
const selfScope = scope()
resource.syncAuthSession(selfScope.viewerSystemAccountId, selfScope.authRevision)
const firstLoad = resource.load(selfScope)
const sharedLoad = resource.load(selfScope)
assert.equal(fetchCount, 1, '同 scope 并发加载必须 single-flight')
firstRequest.resolve(fixture(selfScope.ownerSystemAccountId))
assert.deepEqual(await firstLoad, fixture('viewer-a'))
assert.deepEqual(await sharedLoad, fixture('viewer-a'))
assert.deepEqual(resource.get(selfScope), fixture('viewer-a'), '成功结果应保存在当前会话内存中')
assert.deepEqual(await resource.load(selfScope), fixture('viewer-a'))
assert.equal(fetchCount, 1, '成功缓存不能重复请求')

const adminScope = scope({ viewScope: 'admin', ownerSystemAccountId: 'owner-b' })
const adminSameOwnerScope = scope({ viewScope: 'admin', ownerSystemAccountId: selfScope.ownerSystemAccountId })
const adminOtherOwnerScope = scope({ viewScope: 'admin', ownerSystemAccountId: 'owner-c' })
const adminOtherViewerScope = scope({
  viewerSystemAccountId: 'viewer-b',
  viewScope: 'admin',
  ownerSystemAccountId: adminScope.ownerSystemAccountId
})
const adminOtherRevisionScope = scope({
  authRevision: 2,
  viewScope: 'admin',
  ownerSystemAccountId: adminScope.ownerSystemAccountId
})
assert.notEqual(userReferenceDataScopeKey(selfScope), userReferenceDataScopeKey(adminSameOwnerScope), 'self/admin 必须使用不同 cache key')
assert.notEqual(userReferenceDataScopeKey(adminScope), userReferenceDataScopeKey(adminOtherOwnerScope), '不同目标 owner 必须使用不同 cache key')
assert.notEqual(userReferenceDataScopeKey(adminScope), userReferenceDataScopeKey(adminOtherViewerScope), '不同查看者必须使用不同 cache key')
assert.notEqual(userReferenceDataScopeKey(adminScope), userReferenceDataScopeKey(adminOtherRevisionScope), '不同认证 revision 必须使用不同 cache key')
assert.equal(resource.get(adminScope), undefined, 'self 缓存不能泄漏到 admin target')

const staleRequest = deferred<UserReferenceData>()
const staleResource = createUserReferenceDataResource({ fetch: () => staleRequest.promise })
staleResource.syncAuthSession('viewer-a', 1)
const staleLoad = staleResource.load(selfScope)
staleResource.invalidate(selfScope)
staleRequest.resolve(fixture('viewer-a'))
assert.equal(await staleLoad, undefined, 'scope 失效后的迟到响应不能返回旧数据')
assert.equal(staleResource.get(selfScope), undefined, 'scope 失效后的迟到响应不能写缓存')

const switchedRequest = deferred<UserReferenceData>()
const switchedResource = createUserReferenceDataResource({ fetch: () => switchedRequest.promise })
switchedResource.syncAuthSession('viewer-a', 1)
const switchedLoad = switchedResource.load(selfScope)
switchedResource.syncAuthSession('viewer-b', 2)
switchedRequest.resolve(fixture('viewer-a'))
assert.equal(await switchedLoad, undefined, '登出或切换用户后的旧响应不能返回给调用方')
assert.equal(switchedResource.get(selfScope), undefined, '旧用户响应不能写入新 session')

let retryCount = 0
const retryResource = createUserReferenceDataResource({
  fetch: async (requestScope) => {
    retryCount += 1
    if (retryCount === 1) throw new Error('temporary failure')
    return fixture(requestScope.ownerSystemAccountId)
  }
})
retryResource.syncAuthSession('viewer-a', 1)
await assert.rejects(retryResource.load(selfScope), /temporary failure/)
assert.deepEqual(await retryResource.load(selfScope), fixture('viewer-a'))
assert.equal(retryCount, 2, '失败请求不能残留 rejected in-flight，后续必须可重试')

const mismatchResource = createUserReferenceDataResource({ fetch: async () => fixture('other-owner') })
mismatchResource.syncAuthSession('viewer-a', 1)
await assert.rejects(mismatchResource.load(selfScope), /响应与请求作用域不一致/)
assert.equal(mismatchResource.get(selfScope), undefined, 'owner 不匹配响应不得写缓存')

const source = readFileSync(new URL('../../composables/useUserReferenceData.ts', import.meta.url), 'utf8')
const mainSource = readFileSync(new URL('../../main.ts', import.meta.url), 'utf8')
assert.doesNotMatch(source, /localStorage|sessionStorage/, '默认资源引用不能持久化到浏览器存储')
assert.match(mainSource, /syncUserReferenceDataAuthState\(currentUser\?\.id, authRevision\)/, '认证身份或 revision 变化必须同步清理作用域缓存')
assert.match(mainSource, /\{\s*immediate:\s*true,\s*flush:\s*['"]sync['"]\s*\}/, '认证变化必须同步清理缓存，避免退出与迟到响应之间的微任务窗口')
assert.match(mainSource, /void prewarmSelfUserReferenceData\(\)/, '登录恢复后应异步非阻塞预热 self scope')
assert.match(mainSource, /const authSessionKey = `\$\{currentUser\.id\}:\$\{authRevision\}`/, '预热去重必须同时包含用户 ID 和认证 revision')
assert.match(mainSource, /authSessionKey === lastPrewarmedAuthSessionKey/, '同一认证会话内不应重复预热')
assert.match(mainSource, /const currentUserChanged = [^\n]*currentUser !== lastObservedUser/, '必须区分成功发布的用户快照与 revision-only 认证过渡')
assert.match(mainSource, /if \(!currentUserChanged\) return[\s\S]*const authSessionKey/, '退出或认证写操作的 revision-only 过渡只应清缓存，不应预热旧用户')
assert.match(mainSource, /!value[\s\S]*lastPrewarmedAuthSessionKey = undefined/, '预热失败不得永久锁死当前会话的后续重试')

console.log('user reference data cache regression passed')
