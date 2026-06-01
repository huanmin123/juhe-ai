import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { createCaptchaChallenge } from '../../modules/auth/captcha.service.js'
import { checkLoginAllowed, recordFailedLogin } from '../../modules/auth/login-guard.service.js'

const backendSrcRoot = fileURLToPath(new URL('../..', import.meta.url))
const captchaSource = readSource('modules/auth/captcha.service.ts')
const loginGuardSource = readSource('modules/auth/login-guard.service.ts')

assert(!/\[\s*\.\.\.captchaChallenges\.entries\(\)\s*\]/.test(captchaSource), '验证码容量维护不应展开全部 challenge 条目')
assert(!/captchaChallenges\.entries\(\)\s*\.sort|\bsort\(/.test(captchaSource), '验证码容量维护不应通过排序淘汰旧 challenge')
assert(captchaSource.includes('captchaCleanupBatchSize'), '验证码过期清理必须有固定批量上限')
assert(captchaSource.includes('runCaptchaMaintenance(now)'), '验证码创建和校验应只触发有节流的维护入口')

assert(!/\[\s*\.\.\.records\.entries\(\)\s*\]/.test(loginGuardSource), '登录失败维护不应展开全部限频记录')
assert(!/\bsort\(/.test(loginGuardSource), '登录失败维护不应通过排序淘汰旧记录')
assert(loginGuardSource.includes('loginGuardCleanupBatchSize'), '登录失败过期清理必须有固定批量上限')
assert(
  loginGuardSource.includes('trimRecentTimestamps(record.timestamps, now, threshold - 1)'),
  '登录失败单 key 时间戳应按阈值截断，避免单条记录在热路径变成长数组'
)

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

  console.log('认证内存维护边界回归通过：验证码和登录失败状态按固定批量维护，容量淘汰不再全量展开排序')
} finally {
  Array.prototype.sort = originalSort
}

function readSource(relativePath: string): string {
  return readFileSync(resolve(backendSrcRoot, relativePath), 'utf8')
}
