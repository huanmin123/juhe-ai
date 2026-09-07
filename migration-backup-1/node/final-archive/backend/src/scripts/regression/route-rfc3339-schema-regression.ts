import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

import { rfc3339InstantSchema } from '../../shared/zod-rfc3339.js'

const instant = '2026-08-15T22:34:49.137Z'
const schema = rfc3339InstantSchema('版本格式不正确')

assert.equal(
  schema.parse('2026-08-16T06:34:49.137+08:00'),
  instant,
  '路由绝对时间 schema 必须接受数值 offset 并输出 UTC'
)
for (const value of ['2026-08-16T06:34:49.137', '2026-08-16 06:34:49.137', 'not-a-time']) {
  assert.equal(schema.safeParse(value).success, false, `路由 schema 必须拒绝裸时间或非法时间：${value}`)
}

const routeSources = [
  '../../modules/authorizations/authorizations.routes.ts',
  '../../modules/delegated-api/delegated-api.routes.ts',
  '../../modules/external-integrations/external-integration-sources.routes.ts',
  '../../modules/groups/groups.routes.ts',
  '../../modules/providers/providers.routes.ts',
  '../../modules/proxies/proxies.routes.ts',
  '../../modules/response-inspection-policies/response-inspection-policies.routes.ts',
  '../../modules/route-strategies/route-strategies.routes.ts',
  '../../modules/system-accounts/system-accounts.routes.ts',
  '../../modules/system-teams/system-teams.routes.ts'
] as const

for (const path of routeSources) {
  const source = readFileSync(new URL(path, import.meta.url), 'utf8')
  assert.match(source, /rfc3339InstantSchema/, `${path} 必须使用严格 RFC3339 路由 schema`)
  assert.doesNotMatch(source, /z\.string\(\)\.datetime\(/, `${path} 不得回退为只接受 Z 且不 canonical 的 Zod datetime schema`)
}

const authorizationsSource = readFileSync(new URL('../../modules/authorizations/authorizations.routes.ts', import.meta.url), 'utf8')
assert.match(authorizationsSource, /const authorizationExpiresAtSchema = rfc3339InstantSchema/, '授权 expiresAt 必须在路由边界 canonical UTC')

const externalIntegrationsSource = readFileSync(new URL('../../modules/external-integrations/external-integration-sources.routes.ts', import.meta.url), 'utf8')
assert.match(externalIntegrationsSource, /rfc3339InstantSchema\('过期时间无效'\)/, '外部来源 expiresAt 必须在路由边界 canonical UTC')

console.log('路由 RFC3339 schema 回归通过：数值 offset canonical UTC，裸时间被拒绝')
