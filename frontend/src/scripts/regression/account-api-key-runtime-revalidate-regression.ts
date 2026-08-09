import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = resolve(fileURLToPath(new URL('../../../', import.meta.url)))
const rules = readFileSync(resolve(root, 'src/views/accounts/accountRules.ts'), 'utf8')
const actions = readFileSync(resolve(root, 'src/views/accounts/useAccountMenuActions.ts'), 'utf8')
const api = readFileSync(resolve(root, 'src/api/domains/accounts.ts'), 'utf8')
assert.match(rules, /revalidate-api-key-runtime/)
assert.match(rules, /apiKeyRuntime\?\.unavailable/)
assert.match(rules, /apiKeyRuntime\?\.disabled/)
assert.match(rules, /account\.status === 'active'/)
assert.match(rules, /account\.schedulable/)
assert.match(rules, /icon: 'refresh', tone: 'info'/)
assert.match(actions, /const payload = \{ expectedConfigRevision \}/)
assert.match(actions, /revalidateApiKeyRuntime\(/)
assert.match(actions, /已提交 Key 池重新验证/)
assert.match(actions, /重新验证 Key 池失败/)
assert.match(actions, /account\.status !== 'active'/)
assert.match(actions, /!account\.schedulable/)
assert.match(api, /api-key-runtime\/revalidate/)
console.log('account-api-key-runtime-revalidate 前端回归通过')
