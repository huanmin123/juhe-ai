import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const targetSources = {
  externalAuth: readFileSync(new URL('../../storage/external-integration-source-auth.repository.ts', import.meta.url), 'utf8'),
  accountQuality: readFileSync(new URL('../../storage/account-quality.repository.ts', import.meta.url), 'utf8'),
  oauthRotation: readFileSync(new URL('../../storage/oauth-credential-rotation.repository.ts', import.meta.url), 'utf8'),
  oauthUsage: readFileSync(new URL('../../storage/oauth-usage-loaders.ts', import.meta.url), 'utf8')
}

assert.doesNotMatch(targetSources.externalAuth, /Date\.parse\(/, '外部集成授权不得宽松解析 DB 时间')
assert.match(targetSources.externalAuth, /requiredRfc3339Instant/, '外部集成授权 DB 时间必须严格 canonical')
assert.match(targetSources.externalAuth, /rfc3339InstantMilliseconds/, '外部集成授权比较必须统一使用 epoch')

assert.doesNotMatch(targetSources.accountQuality, /Date\.parse\(/, '账户质量不得宽松解析 DB 时间')
assert.match(targetSources.accountQuality, /normalizeAccountQualityRow/, '账户质量 SQLite/PG 行读取必须经过时间归一化')
assert.match(targetSources.accountQuality, /requiredRfc3339Instant/, '账户质量 supplied/DB 时间必须严格 canonical')
assert.match(targetSources.accountQuality, /rfc3339InstantMilliseconds/, '账户质量年龄比较必须统一使用 epoch')

assert.match(targetSources.oauthRotation, /normalizeSuppliedOAuthCredentialTimes/, 'OAuth rotation supplied expires_at 必须先区分缺失并严格解析')
assert.match(targetSources.oauthRotation, /normalizeStoredOAuthCredentials/, 'OAuth rotation DB 凭据 expires_at 必须严格 canonical')
assert.match(targetSources.oauthRotation, /requiredRfc3339Instant/, 'OAuth rotation 时间必须严格 canonical')

assert.doesNotMatch(targetSources.oauthUsage, /Date\.parse\(/, 'OAuth usage snapshot 不得宽松解析 DB 时间')
assert.match(targetSources.oauthUsage, /requiredRfc3339Instant/, 'OAuth usage snapshot 时间必须严格 canonical')
assert.match(targetSources.oauthUsage, /rfc3339InstantMilliseconds/, 'OAuth usage reset 比较必须统一使用 epoch')

const { requiredRfc3339Instant } = await import('../../shared/rfc3339.js')
assert.equal(
  requiredRfc3339Instant('2026-08-16T06:34:49.137+08:00', '时间'),
  '2026-08-15T22:34:49.137Z',
  'numeric offset 必须 canonical 为 UTC Z'
)
for (const value of ['', '2026-08-16T06:34:49.137', 'not-a-time']) {
  assert.throws(
    () => requiredRfc3339Instant(value, '时间'),
    /时间必须是带 Z 或数值 offset 的 RFC3339 时间/,
    `非法时间必须显式失败：${value || '<empty>'}`
  )
}

console.log('外部集成、账户质量和 OAuth 时间边界回归通过')
