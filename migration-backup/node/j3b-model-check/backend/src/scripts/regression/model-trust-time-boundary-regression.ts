import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { requiredRfc3339Instant, rfc3339InstantMilliseconds } from '../../shared/rfc3339.js'

const repositoryFiles = [
  'model-trust.repository.ts',
  'model-trust-token-baseline.repository.ts',
  'model-trust-identity.repository.ts'
]

assert.equal(
  requiredRfc3339Instant('2026-08-18T12:00:00+08:00'),
  '2026-08-18T04:00:00.000Z',
  '数字 offset 必须 canonicalize 到 UTC'
)
assert.equal(rfc3339InstantMilliseconds('2026-08-18T04:00:00.000Z'), 1_787_025_600_000)
for (const value of ['', '2026-08-18T12:00:00', 'not-a-time', '2026-02-30T00:00:00Z']) {
  assert.throws(() => requiredRfc3339Instant(value, 'model trust time'), /RFC3339/)
}

for (const file of repositoryFiles) {
  const source = readFileSync(resolve(import.meta.dirname, '../../storage', file), 'utf8')
  assert.match(source, /requiredRfc3339Instant/, `${file} 必须使用严格 RFC3339 helper`)
  assert.doesNotMatch(source, /Date\.parse\s*\(/, `${file} 不得使用宽松 Date.parse`)
}

const repositorySource = readFileSync(resolve(import.meta.dirname, '../../storage/model-trust.repository.ts'), 'utf8')
assert.match(repositorySource, /requiredRfc3339Instant\(input\.createdAt, '模型可信 observation createdAt'\)/)
assert.match(repositorySource, /normalizedObservationRow/)
assert.match(repositorySource, /durationDaysBetween/)

const tokenSource = readFileSync(resolve(import.meta.dirname, '../../storage/model-trust-token-baseline.repository.ts'), 'utf8')
assert.match(tokenSource, /normalizedSourceInterceptRow/)
assert.match(tokenSource, /requiredInstantMilliseconds/)

const identitySource = readFileSync(resolve(import.meta.dirname, '../../storage/model-trust-identity.repository.ts'), 'utf8')
assert.match(identitySource, /normalizedSourceFeatureRow/)
assert.match(identitySource, /compareInstantDescending/)
assert.match(identitySource, /earliestInstant/)

const [{ refreshTokenInterceptBaselines }, { evaluateIdentityTrust }] = await Promise.all([
  import('../../storage/model-trust-token-baseline.repository.js'),
  import('../../storage/model-trust-identity.repository.js')
])
const fakeClient = (queryResult: unknown[]) => ({
  dialect: { qualifyTable: (_schema: string, table: string) => table },
  query: async <T extends object = Record<string, unknown>>(_sql: string, _params?: readonly unknown[]) => queryResult as T[],
  one: async <T extends object = Record<string, unknown>>(_sql: string, _params?: readonly unknown[]) => undefined as T | undefined,
  execute: async () => ({ changes: 1 })
}) as unknown as import('../../storage/database-client.js').DatabaseClient

await assert.rejects(
  refreshTokenInterceptBaselines(fakeClient([{
    system_account_id: 'sys', account_id: 'acct', upstream_bucket_hmac: 'bucket',
    intercept: 1, slope: 1, round_count: 3, valid_sample_count: 6,
    first_observed_at: '2026-08-18T00:00:00', last_observed_at: '2026-08-18T01:00:00Z'
  }]), [{ cohortKeyHmac: 'cohort', requestedModel: 'model', tokenizerVersion: 'tokenizer', probeSetVersion: 'probe' }]),
  /first_observed_at/
)
await assert.rejects(
  evaluateIdentityTrust(fakeClient([{
    population_key_hmac: 'population', feature_version: 'features',
    first_observed_at: '', last_observed_at: '2026-08-18T01:00:00Z'
  }]), { systemAccountId: 'sys', accountId: 'acct', requestedModel: 'model' }),
  /first_observed_at/
)

console.log('模型可信时间边界回归通过：offset canonical、裸/空/非法拒绝、三仓储严格边界和 epoch 比较契约符合预期')
