import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

import type { ProviderDefinition, RouteStrategyGroupBindingSummary } from '../../types/domain'
import {
  buildCcSwitchExportModelOptions,
  buildCcSwitchExportGroupOptions,
  buildCcSwitchExportUrl,
  canSubmitCcSwitchExport,
  ccswitchClientOptions,
  defaultCcSwitchClientAppForGroups,
  defaultCcSwitchClientAppForProvider,
  isCcSwitchExportModelSelectionValid,
  shouldLoadCcSwitchExportModelOptions
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
assert.equal(canSubmitCcSwitchExport({ groupId: 'group-openai', app: 'codex', modelsReady: true }), true)
assert.equal(canSubmitCcSwitchExport({ groupId: 'group-openai', app: 'codex', modelsReady: false }), false)
assert.equal(canSubmitCcSwitchExport({ groupId: 'group-openai', app: 'codex', modelsLoading: true, modelsReady: true }), false)
assert.equal(canSubmitCcSwitchExport({ groupId: 'group-openai', modelsReady: true }), false)
assert.deepEqual(ccswitchClientOptions, [
  { label: 'Codex', value: 'codex' },
  { label: 'Claude CLI', value: 'claude' },
  { label: 'Claude Desktop', value: 'claude-desktop' },
  { label: 'Gemini', value: 'gemini' },
  { label: 'Grok Build', value: 'grokbuild' },
  { label: 'OpenCode', value: 'opencode' }
])
assert.equal(defaultCcSwitchClientAppForProvider('gpt'), 'codex')
assert.equal(defaultCcSwitchClientAppForProvider('openai'), 'codex')
assert.equal(defaultCcSwitchClientAppForProvider('anthropic'), 'claude')
assert.equal(defaultCcSwitchClientAppForProvider('gemini'), 'gemini')
assert.equal(defaultCcSwitchClientAppForProvider('xai'), 'grokbuild')
assert.equal(defaultCcSwitchClientAppForProvider('deepseek'), undefined)

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
assert.equal(defaultCcSwitchClientAppForGroups(groups), 'codex')
assert.equal(defaultCcSwitchClientAppForGroups([
  ...groups,
  { ...groups[0], groupId: 'group-gemini', providerCode: 'gemini' }
]), undefined)
assert.deepEqual(buildCcSwitchExportModelOptions([
  { label: 'GPT 5.4', value: 'gpt-5.4' },
  { label: 'GPT 5.4', value: 'gpt-5.4' },
  { label: 'GPT 5.4 Mini', value: 'gpt-5.4-mini' }
]), [
  { label: 'GPT 5.4', value: 'gpt-5.4' },
  { label: 'GPT 5.4 Mini', value: 'gpt-5.4-mini' }
])
assert.equal(isCcSwitchExportModelSelectionValid(groups.map((group) => ({ label: group.defaultModel, value: group.defaultModel })), ''), true)
assert.equal(isCcSwitchExportModelSelectionValid([
  { label: 'GPT 5.4 Mini', value: 'gpt-5.4-mini' }
], 'gpt-5.4-mini'), true)
assert.equal(isCcSwitchExportModelSelectionValid([
  { label: 'GPT 5.4 Mini', value: 'gpt-5.4-mini' }
], 'gpt-5.4'), false)
assert.equal(shouldLoadCcSwitchExportModelOptions({ groupId: 'group-openai' }), true)
assert.equal(shouldLoadCcSwitchExportModelOptions({
  groupId: 'group-openai',
  catalogGroupId: 'group-openai',
  modelsReady: true
}), false)
assert.equal(shouldLoadCcSwitchExportModelOptions({
  groupId: 'group-openai',
  catalogGroupId: 'group-openai',
  modelsLoading: true
}), false)
assert.equal(shouldLoadCcSwitchExportModelOptions({
  groupId: 'group-gemini',
  catalogGroupId: 'group-openai',
  modelsReady: true
}), true)
assert.equal(shouldLoadCcSwitchExportModelOptions({
  groupId: 'group-gemini',
  catalogGroupId: 'group-openai',
  modelsLoading: true
}), true)

const [ccsModalSource, apiKeysViewSource] = await Promise.all([
  readFile(new URL('../../views/api-keys/ApiKeyCcsExportModal.vue', import.meta.url), 'utf8'),
  readFile(new URL('../../views/api-keys/ApiKeysView.vue', import.meta.url), 'utf8')
])
assert.match(ccsModalSource, /:disabled="!modelsReady"/, '已有模型目录时，模型下拉不得因后台刷新而被锁住')
assert.doesNotMatch(ccsModalSource, /:disabled="!modelsReady \|\| modelsLoading"/, '模型下拉不能在打开时立即被自身刷新锁住')
assert.match(apiKeysViewSource, /:model-options="ccsExportModelOptions"/, '父页面必须传入模型候选')
assert.match(apiKeysViewSource, /:models-loading="ccsExportModelsLoading"/, '父页面必须传入模型加载状态')
assert.match(apiKeysViewSource, /:models-ready="ccsExportModelsReady"/, '父页面必须传入模型目录就绪状态')
assert.match(apiKeysViewSource, /@model-options-open="handleCcSwitchModelOptionsOpen"/, '父页面必须处理模型下拉打开事件')
assert.match(apiKeysViewSource, /@model-options-search="handleCcSwitchModelOptionsSearch"/, '父页面必须处理模型搜索事件')

console.log('ccswitch-export regression passed')
