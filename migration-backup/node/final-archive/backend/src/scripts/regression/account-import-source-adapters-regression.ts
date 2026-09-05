import assert from 'node:assert/strict'

import { adaptAccountImportSource } from '../../modules/accounts/account-import-source-adapters.js'

function assertNoSecretInMessages(messages: string[], secret: string): void {
  assert.equal(messages.some((message) => message.includes(secret)), false)
}

const sub2api = adaptAccountImportSource({
  type: 'sub2api-data',
  version: 1,
  proxies: [{
    proxy_key: 'http|proxy.example|8080||',
    name: 'Sub2API Proxy',
    protocol: 'http',
    host: 'proxy.example',
    port: 8080,
    status: 'active',
    password: 'proxy-secret'
  }],
  accounts: [
    {
      name: 'Sub2API API Key',
      platform: 'openai',
      type: 'apikey',
      credentials: {
        api_key: 'sub2api-key',
        base_url: 'https://relay.example/v1',
        runtime_only: true
      },
      proxy_key: 'http|proxy.example|8080||',
      concurrency: 3,
      priority: 10
    },
    {
      name: 'Sub2API OAuth',
      platform: 'openai',
      type: 'oauth',
      credentials: { refresh_token: 'sub2api-refresh' }
    }
  ],
  skipped_shadows: 1
}, 'sub2api')
assert.equal(sub2api.source.records, 2)
assert.equal(sub2api.source.accepted, 2)
assert.equal(sub2api.source.skipped, 0)
assert.equal(sub2api.source.ignoredFields > 0, true)
assert.equal((sub2api.data as { accounts: unknown[] }).accounts.length, 2)
assert.equal((sub2api.data as { proxies: unknown[] }).proxies.length, 1)
assertNoSecretInMessages(sub2api.source.messages, 'sub2api-key')
assertNoSecretInMessages(sub2api.source.messages, 'proxy-secret')
const subAccountCredentials = (sub2api.data as { accounts: Array<{ credentials: Record<string, unknown> }> }).accounts[0]?.credentials
assert.equal(subAccountCredentials?.runtime_only, undefined)

const newApi = adaptAccountImportSource({
  data: {
    items: [
      {
        id: 7,
        type: 1,
        key: 'new-api-key',
        base_url: 'https://new-api.example/v1',
        name: 'NewAPI OpenAI',
        group: 'new-api',
        status: 1,
        models: 'gpt-4o',
        channel_info: { is_multi_key: true }
      },
      { id: 8, type: 2, key: 'non-openai-key', name: 'Midjourney' }
    ]
  }
}, 'newapi')
assert.equal(newApi.source.records, 2)
assert.equal(newApi.source.accepted, 1)
assert.equal(newApi.source.skipped, 1)
assert.equal((newApi.data as { accounts: Array<{ providerCode: string }> }).accounts[0]?.providerCode, 'openai')
assertNoSecretInMessages(newApi.source.messages, 'new-api-key')

const oneApi = adaptAccountImportSource([
  { type: 'openai', key: 'one-api-key', name: 'One-API OpenAI' }
], 'oneapi')
assert.equal(oneApi.source.accepted, 1)
assert.equal((oneApi.data as { accounts: unknown[] }).accounts.length, 1)

const oneApiNumeric = adaptAccountImportSource([
  { type: 1, key: 'one-api-numeric-key', name: 'One-API Numeric', status: 1 }
], 'oneapi')
assert.equal(oneApiNumeric.source.accepted, 1)
assert.equal(oneApiNumeric.source.skipped, 0)
assert.equal(
  (oneApiNumeric.data as { accounts: Array<{ status: string }> }).accounts[0]?.status,
  'active',
  'One-API type=1 是来源定义的 OpenAI 渠道，必须导入为可用账户'
)

for (const [mode, status, expectedStatus] of [
  ['newapi', 2, 'disabled'],
  ['newapi', 3, 'disabled'],
  ['oneapi', 2, 'disabled'],
  ['oneapi', 3, 'disabled']
] as const) {
  const disabledChannel = adaptAccountImportSource([
    { type: 1, key: `${mode}-status-${status}-key`, name: `${mode} 状态 ${status}`, status }
  ], mode)
  assert.equal(disabledChannel.source.accepted, 1, `${mode} 已禁用渠道仍应可作为禁用账户导入`)
  assert.equal(
    (disabledChannel.data as { accounts: Array<{ status: string }> }).accounts[0]?.status,
    expectedStatus,
    `${mode} status=${status} 不能被重新启用`
  )
}

const maskedChannel = adaptAccountImportSource([
  { type: 'openai', key: 'sk-****abcd', name: 'Masked Channel' },
  { type: 'openai', key: '***', name: 'Masked Channel 2' }
], 'oneapi')
assert.equal(maskedChannel.source.accepted, 0)
assert.equal(maskedChannel.source.skipped, 2)

const unsafeChannel = adaptAccountImportSource([
  { type: 1, key: 'unsafe-key', base_url: 'http://127.0.0.1:8080/v1' }
], 'newapi')
assert.equal(unsafeChannel.source.accepted, 0)
assert.equal(unsafeChannel.source.skipped, 1)
assert.match(unsafeChannel.source.messages.join('；'), /Base URL/)
assert.equal((unsafeChannel.data as { accounts: unknown[] }).accounts.length, 0)
assertNoSecretInMessages(unsafeChannel.source.messages, 'unsafe-key')

const unsafeSub2Api = adaptAccountImportSource({
  accounts: [{
    name: 'Unsafe Sub2API',
    platform: 'openai',
    type: 'apikey',
    credentials: { api_key: 'unsafe-sub-key', base_url: 'http://127.0.0.1:8080/v1' }
  }]
}, 'sub2api')
assert.equal(unsafeSub2Api.source.accepted, 0)
assert.equal(unsafeSub2Api.source.skipped, 1)
assert.match(unsafeSub2Api.source.messages.join('；'), /Base URL/)
assertNoSecretInMessages(unsafeSub2Api.source.messages, 'unsafe-sub-key')

const cpaConfig = adaptAccountImportSource(`
codex-api-key:
  - api-key: cpa-codex-key
    headers:
      X-Internal: ignored
openai-compatibility:
  - name: CPA Provider
    base-url: https://cpa.example/v1
    api-key-entries:
      - api-key: cpa-key-1
      - api-key: cpa-key-2
`, 'cpa')
assert.equal(cpaConfig.source.records, 3)
assert.equal(cpaConfig.source.accepted, 3)
assert.equal((cpaConfig.data as { accounts: unknown[] }).accounts.length, 3)
assert.equal(cpaConfig.source.ignoredFields > 0, true)
assertNoSecretInMessages(cpaConfig.source.messages, 'cpa-codex-key')

const cpaAuth = adaptAccountImportSource({
  type: 'codex',
  email: 'codex@example.com',
  token_data: {
    refresh_token: 'cpa-refresh',
    id_token: 'cpa-id-token'
  },
  metadata: { plan_type: 'plus' }
}, 'cpa')
assert.equal(cpaAuth.source.records, 1)
assert.equal(cpaAuth.source.accepted, 1)
assert.equal((cpaAuth.data as { accounts: Array<{ type: string, providerCode: string }> }).accounts[0]?.type, 'oauth')
assert.equal((cpaAuth.data as { accounts: Array<{ type: string, providerCode: string }> }).accounts[0]?.providerCode, 'gpt')
assertNoSecretInMessages(cpaAuth.source.messages, 'cpa-refresh')

console.log('account-import-source-adapters regression passed')
