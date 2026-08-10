import assert from 'node:assert/strict'

import { buildCcSwitchExportUrl } from '../../views/api-keys/ccswitchExport'

const url = buildCcSwitchExportUrl({
  apiKey: 'sk-test-key',
  endpoint: 'https://gateway.example/v1/',
  homepage: 'https://gateway.example/',
  name: '测试 网关'
})
const parsed = new URL(url)

assert.equal(parsed.protocol, 'ccswitch:')
assert.equal(parsed.hostname, 'v1')
assert.equal(parsed.pathname, '/import')
assert.equal(parsed.searchParams.get('resource'), 'provider')
assert.equal(parsed.searchParams.get('app'), 'codex')
assert.equal(parsed.searchParams.get('model'), 'gpt-5.5')
assert.equal(parsed.searchParams.get('name'), '测试 网关')
assert.equal(parsed.searchParams.get('endpoint'), 'https://gateway.example/v1')
assert.equal(parsed.searchParams.get('homepage'), 'https://gateway.example')
assert.equal(parsed.searchParams.get('apiKey'), 'sk-test-key')
assert.equal(parsed.searchParams.get('configFormat'), 'json')
assert.equal(parsed.searchParams.get('enabled'), 'true')
assert.equal(parsed.searchParams.has('usageScript'), false)
assert.equal(url.includes('sk-test-key'), true)

console.log('ccswitch-export regression passed')
