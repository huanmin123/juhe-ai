import assert from 'node:assert/strict'

import type { ProviderDefinition, RouteStrategyGroupBindingSummary } from '../../types/domain'
import {
  buildCcSwitchExportGroupOptions,
  buildCcSwitchExportUrl,
  canSubmitCcSwitchExport
} from '../../views/api-keys/ccswitchExport'

const url = buildCcSwitchExportUrl({
  apiKey: 'sk-test-key',
  app: 'gemini',
  model: 'gemini-3.5-flash',
  endpoint: 'https://gateway.example/v1/',
  homepage: 'https://gateway.example/',
  name: '测试 网关'
})
const parsed = new URL(url)

assert.equal(parsed.protocol, 'ccswitch:')
assert.equal(parsed.hostname, 'v1')
assert.equal(parsed.pathname, '/import')
assert.equal(parsed.searchParams.get('resource'), 'provider')
assert.equal(parsed.searchParams.get('app'), 'gemini')
assert.equal(parsed.searchParams.get('model'), 'gemini-3.5-flash')
assert.equal(parsed.searchParams.get('name'), '测试 网关')
assert.equal(parsed.searchParams.get('endpoint'), 'https://gateway.example/v1')
assert.equal(parsed.searchParams.get('homepage'), 'https://gateway.example')
assert.equal(parsed.searchParams.get('apiKey'), 'sk-test-key')
assert.equal(parsed.searchParams.get('configFormat'), 'json')
assert.equal(parsed.searchParams.get('enabled'), 'true')
assert.equal(parsed.searchParams.has('usageScript'), false)
assert.equal(url.includes('sk-test-key'), true)

const modelOptionalUrl = new URL(buildCcSwitchExportUrl({
  apiKey: 'sk-without-model',
  app: 'claude',
  endpoint: 'https://gateway.example',
  name: 'Claude 网关'
}))
assert.equal(modelOptionalUrl.searchParams.get('app'), 'claude')
assert.equal(modelOptionalUrl.searchParams.has('model'), false)
assert.equal(canSubmitCcSwitchExport({ groupId: 'group-openai', app: 'codex', confirmed: true }), true)
assert.equal(canSubmitCcSwitchExport({ groupId: 'group-openai', app: 'codex', confirmed: false }), false)
assert.equal(canSubmitCcSwitchExport({ groupId: 'group-openai', confirmed: true }), false)

const groups = buildCcSwitchExportGroupOptions([
  {
    id: 'binding-openai',
    groupId: 'group-openai',
    groupName: 'OpenAI 分组',
    providerCode: 'openai',
    priority: 10,
    weight: 1,
    status: 'active',
    groupEnabled: true
  },
  {
    id: 'binding-disabled',
    groupId: 'group-disabled',
    groupName: '停用分组',
    providerCode: 'gemini',
    priority: 10,
    weight: 1,
    status: 'disabled',
    groupEnabled: true
  },
  {
    id: 'binding-group-disabled',
    groupId: 'group-unavailable',
    groupName: '不可用分组',
    providerCode: 'gpt',
    priority: 10,
    weight: 1,
    status: 'active',
    groupEnabled: false
  }
] satisfies RouteStrategyGroupBindingSummary[], [
  {
    id: 'openai',
    code: 'openai',
    name: 'OpenAI',
    enabled: true,
    defaultProtocolProfileId: 'profile_openai_openai_v1',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    baseUrl: 'https://api.openai.com/v1',
    defaultHealthCheckModel: 'gpt-5.4-mini',
    defaultSupportedModels: [],
    accountTypes: ['api_key'],
    capabilities: [],
    protocolProfiles: []
  }
] satisfies ProviderDefinition[])
assert.deepEqual(groups, [{
  groupId: 'group-openai',
  groupName: 'OpenAI 分组',
  providerCode: 'openai',
  providerName: 'OpenAI',
  defaultModel: 'gpt-5.4-mini'
}])

console.log('ccswitch-export regression passed')
