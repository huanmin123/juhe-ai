import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import type { Request } from 'express'

import {
  consumeCaptchaIssueAllowance,
  consumeCaptchaIssueAllowanceAsync,
  createCaptchaChallenge,
  createCaptchaChallengeAsync,
  verifyCaptchaChallengeAsync
} from '../../modules/auth/captcha.service.js'
import { parseCookie, sessionCookieName } from '../../modules/auth/auth.routes.js'
import { checkLoginAllowed, getLoginClientIp, recordFailedLogin } from '../../modules/auth/login-guard.service.js'

const backendSrcRoot = fileURLToPath(new URL('../..', import.meta.url))
const captchaSource = readSource('modules/auth/captcha.service.ts')
const authRoutesSource = readSource('modules/auth/auth.routes.ts')
const loginGuardSource = readSource('modules/auth/login-guard.service.ts')

assert(!/\[\s*\.\.\.captchaChallenges\.entries\(\)\s*\]/.test(captchaSource), '验证码容量维护不应展开全部 challenge 条目')
assert(!/captchaChallenges\.entries\(\)\s*\.sort|\bsort\(/.test(captchaSource), '验证码容量维护不应通过排序淘汰旧 challenge')
assert(captchaSource.includes('captchaCleanupBatchSize'), '验证码过期清理必须有固定批量上限')
assert(captchaSource.includes('runCaptchaMaintenance(now)'), '验证码创建和校验应只触发有节流的维护入口')
assert(captchaSource.includes('consumeCaptchaIssueAllowance'), '公开验证码生成必须先通过独立限频入口')
assert(captchaSource.includes('captchaIssueThreshold'), '验证码生成限频必须有固定单 key 阈值')
assert(captchaSource.includes("createRuntimeStateStore('auth_captcha')"), '验证码高性能模式必须使用 RuntimeStateStore 承接跨进程 challenge 和限频状态')
assert(captchaSource.includes('createCaptchaChallengeAsync'), '验证码服务必须暴露 async 入口，供 HTTP 路由按 runtime state driver 切换')
assert(captchaSource.includes('consumeCaptchaIssueAllowanceAsync'), '验证码生成限频必须暴露 async 入口，避免高性能模式只走进程内 Map')
assert(captchaSource.includes('verifyCaptchaChallengeAsync'), '验证码校验必须暴露 async 入口，避免高性能模式只走进程内 Map')
assert(captchaSource.includes('getDeleteJson<CaptchaChallengeRecord>'), 'Redis 验证码校验必须一次性读取并删除 challenge，避免并发重复使用')
assert(authRoutesSource.includes('await consumeCaptchaIssueAllowanceAsync(clientIp)'), '验证码 HTTP 路由必须 await async 限频入口')
assert(authRoutesSource.includes('await createCaptchaChallengeAsync()'), '验证码 HTTP 路由必须 await async challenge 创建入口')
assert(authRoutesSource.includes('await verifyCaptchaChallengeAsync'), '登录路由必须 await async 验证码校验入口')

assert(!/\[\s*\.\.\.records\.entries\(\)\s*\]/.test(loginGuardSource), '登录失败维护不应展开全部限频记录')
assert(!/\bsort\(/.test(loginGuardSource), '登录失败维护不应通过排序淘汰旧记录')
assert(loginGuardSource.includes('loginGuardCleanupBatchSize'), '登录失败过期清理必须有固定批量上限')
assert(
  loginGuardSource.includes('trimRecentTimestamps(record.timestamps, now, threshold - 1)'),
  '登录失败单 key 时间戳应按阈值截断，避免单条记录在热路径变成长数组'
)
assert(!/x-forwarded-for/i.test(loginGuardSource), '登录限流不应在业务代码中直接信任 X-Forwarded-For，应交给 Express trust proxy 处理 req.ip')
assert.doesNotThrow(() => parseCookie('foo=%'), '畸形 Cookie 值不应让认证路径抛出 URIError')
assert.deepEqual(parseCookie('foo=%; valid=value'), { valid: 'value' }, '畸形 Cookie 应按条忽略，不影响同一请求中的其他有效 Cookie')
assert.equal(parseCookie(`${sessionCookieName}=%`)[sessionCookieName], undefined, '畸形会话 Cookie 应按未登录处理，而不是抛出 500')

const spoofedForwardedForRequest = {
  headers: { 'x-forwarded-for': '203.0.113.250' },
  ip: '198.51.100.10',
  socket: { remoteAddress: '198.51.100.11' }
} as unknown as Request
assert.equal(getLoginClientIp(spoofedForwardedForRequest), '198.51.100.10', '登录限流客户端 IP 应使用 req.ip，不能被直连客户端伪造 X-Forwarded-For 绕过')

const socketFallbackRequest = {
  headers: { 'x-forwarded-for': '203.0.113.250' },
  ip: '',
  socket: { remoteAddress: '198.51.100.11' }
} as unknown as Request
assert.equal(getLoginClientIp(socketFallbackRequest), '198.51.100.11', '缺少 req.ip 时登录限流应回退到 socket remoteAddress')

const originalSort = Array.prototype.sort
let sortCalled = false

try {
  Array.prototype.sort = function patchedSort<T>(this: T[], compareFn?: (a: T, b: T) => number): T[] {
    sortCalled = true
    return originalSort.call(this, compareFn)
  }

  for (let index = 0; index <= 1000; index += 1) {
    createCaptchaChallenge()
  }

  const asyncCaptcha = await createCaptchaChallengeAsync()
  assert.equal(await verifyCaptchaChallengeAsync(asyncCaptcha.captchaId, 'wrong'), false, 'async 验证码校验错误答案应失败')
  assert.equal(await verifyCaptchaChallengeAsync(asyncCaptcha.captchaId, 'wrong'), false, 'async 验证码被消费后不能重复使用')
  const asyncIssueResult = await consumeCaptchaIssueAllowanceAsync('198.51.100.21')
  assert.equal(asyncIssueResult.blocked, false, 'async 验证码限频入口在内存模式下应保持兼容')

  let captchaIssueBlocked = false
  for (let index = 0; index < 80; index += 1) {
    const result = consumeCaptchaIssueAllowance('198.51.100.20')
    if (result.blocked) {
      captchaIssueBlocked = true
      assert(typeof result.retryAfterSeconds === 'number' && result.retryAfterSeconds > 0, '验证码生成限频应返回 Retry-After 秒数')
      break
    }
  }
  assert(captchaIssueBlocked, '同一 IP 高频生成验证码应触发限频')

  for (let index = 0; index <= 2000; index += 1) {
    recordFailedLogin(`198.51.100.${index}`, `user-${index}@example.test`)
  }

  const blockedIp = '203.0.113.10'
  for (let index = 0; index < 10; index += 1) {
    recordFailedLogin(blockedIp, `blocked-${index}@example.test`)
  }
  const blockResult = checkLoginAllowed(blockedIp, 'blocked-check@example.test')
  assert.equal(blockResult.blocked, true, '同一 IP 达到失败阈值后仍应被临时限制')
  assert.equal(sortCalled, false, '认证内存维护不应在验证码或登录失败热路径触发数组排序')

  console.log('认证内存维护边界回归通过：验证码、验证码生成限频和登录失败状态按固定批量维护，容量淘汰不再全量展开排序')
} finally {
  Array.prototype.sort = originalSort
}

function readSource(relativePath: string): string {
  return readFileSync(resolve(backendSrcRoot, relativePath), 'utf8')
}
